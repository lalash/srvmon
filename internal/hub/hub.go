package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

// Version is reported by the dashboard and the /api/health endpoint.
const Version = "1.3.0"

// Config is the hub's runtime configuration, assembled by cmd/hub.
type Config struct {
	Addr          string
	DBPath        string
	CertFile      string
	KeyFile       string
	BaseURL       string
	BinDir        string
	PushInterval  int
	SampleEvery   int
	RetentionDays int
}

// Hub wires the store, the live cache and the background workers together.
type Hub struct {
	cfg    Config
	store  *Store
	live   *Live
	events *eventHub
	alerts *alertEngine
	logins *loginLimiter

	persistMu   sync.Mutex
	lastPersist map[int64]int64

	startedAt time.Time
}

// New builds a hub over an already-open store.
func New(cfg Config, store *Store) *Hub {
	if cfg.PushInterval <= 0 {
		cfg.PushInterval = 2
	}
	if cfg.SampleEvery <= 0 {
		cfg.SampleEvery = 30
	}
	if cfg.RetentionDays <= 0 {
		cfg.RetentionDays = 8
	}
	h := &Hub{
		cfg:         cfg,
		store:       store,
		live:        NewLive(),
		events:      newEventHub(),
		logins:      newLoginLimiter(),
		lastPersist: map[int64]int64{},
		startedAt:   time.Now(),
	}
	h.alerts = newAlertEngine(h)
	return h
}

// Run starts the background workers and blocks serving HTTP until ctx is done.
func (h *Hub) Run(ctx context.Context) error {
	go h.broadcastLoop(ctx)
	go h.maintenanceLoop(ctx)
	go h.alerts.run(ctx)

	server := &http.Server{
		Addr:              h.cfg.Addr,
		Handler:           h.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	scheme := "http"
	if h.cfg.CertFile != "" {
		scheme = "https"
	}
	log.Printf("srvmon-hub %s listening on %s://%s", Version, scheme, h.cfg.Addr)

	var err error
	if h.cfg.CertFile != "" {
		err = server.ListenAndServeTLS(h.cfg.CertFile, h.cfg.KeyFile)
	} else {
		err = server.ListenAndServe()
	}
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// broadcastLoop pushes the dashboard payload to every open SSE stream on the
// same cadence the agents report at. It deliberately omits the rolling window:
// the client is handed one at connect and extends it from each tick, so a
// hundred-server fleet does not resend a hundred sparklines every two seconds.
func (h *Hub) broadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(h.cfg.PushInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !h.events.hasSubscribers() {
				continue
			}
			payload, err := json.Marshal(h.dashboard(false))
			if err != nil {
				continue
			}
			h.events.broadcast(payload)
		}
	}
}

// maintenanceLoop prunes expired sessions, old samples and old alert events.
func (h *Hub) maintenanceLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	prune := func() {
		_ = h.store.PruneSessions()
		cutoff := time.Now().AddDate(0, 0, -h.cfg.RetentionDays).Unix()
		if n, err := h.store.PruneSamples(cutoff); err == nil && n > 0 {
			log.Printf("pruned %d history samples older than %d days", n, h.cfg.RetentionDays)
		}
		_ = h.store.PruneAlertEvents(500)
	}
	prune()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

type ctxKey int

const ctxUserKey ctxKey = 1

func withUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, ctxUserKey, userID)
}

func userFrom(ctx context.Context) int64 {
	if v, ok := ctx.Value(ctxUserKey).(int64); ok {
		return v
	}
	return 0
}

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}
