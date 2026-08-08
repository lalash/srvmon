package hub

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"srvmon/internal/metrics"
)

const maxPushBytes = 64 << 10

type agentPush struct {
	Name     string            `json:"name"`
	Snapshot *metrics.Snapshot `json:"snapshot"`
}

// handleAgentPush accepts one snapshot from an agent. It is the only endpoint
// authenticated by a bearer token instead of a session cookie.
func (h *Hub) handleAgentPush(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "missing token"})
		return
	}

	server, err := h.store.GetServerByToken(token)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unknown token"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "lookup failed"})
		return
	}

	var body agentPush
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Snapshot == nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid snapshot"})
		return
	}

	now := time.Now()
	h.live.Put(server.ID, body.Snapshot, now)

	snap := body.Snapshot
	if err := h.store.TouchServer(server.ID, now.Unix(), snap.Hostname, snap.OS, snap.Arch,
		snap.Kernel, snap.Agent, snap.PublicIP.IPv4, snap.PublicIP.IPv6); err != nil {
		log.Printf("touch server %d: %v", server.ID, err)
	}

	h.maybePersist(server.ID, snap, now)
	h.alerts.observe(server, snap, now)

	reply := map[string]any{"interval": h.cfg.PushInterval}
	// The agent is told to update on every push until it reports the target
	// version, so an update that failed halfway retries instead of going quiet.
	if server.UpdateTo != "" {
		if server.UpdateTo == snap.Agent {
			if err := h.store.ClearAgentUpdate(server.ID); err != nil {
				log.Printf("clear update flag for server %d: %v", server.ID, err)
			}
			log.Printf("server %q is now on agent %s", server.Name, snap.Agent)
		} else {
			reply["update"] = server.UpdateTo
		}
	}
	writeJSON(w, http.StatusOK, reply)
}

// maybePersist writes at most one history row per SampleEvery seconds per
// server; the live cache already covers finer detail.
func (h *Hub) maybePersist(serverID int64, snap *metrics.Snapshot, now time.Time) {
	h.persistMu.Lock()
	last := h.lastPersist[serverID]
	if now.Unix()-last < int64(h.cfg.SampleEvery) {
		h.persistMu.Unlock()
		return
	}
	h.lastPersist[serverID] = now.Unix()
	h.persistMu.Unlock()

	if err := h.store.InsertSample(serverID, sampleFrom(snap, now.Unix())); err != nil {
		log.Printf("insert sample for server %d: %v", serverID, err)
	}
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if value, ok := strings.CutPrefix(header, "Bearer "); ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}
