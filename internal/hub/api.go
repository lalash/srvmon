package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"srvmon/internal/metrics"
)

type serverView struct {
	ID             int64             `json:"id"`
	Name           string            `json:"name"`
	Tag            string            `json:"tag"`
	Sort           int               `json:"sort"`
	Online         bool              `json:"online"`
	CreatedAt      int64             `json:"createdAt"`
	LastSeen       int64             `json:"lastSeen"`
	Hostname       string            `json:"hostname"`
	OS             string            `json:"os"`
	Arch           string            `json:"arch"`
	Kernel         string            `json:"kernel"`
	AgentVersion   string            `json:"agentVersion"`
	IPv4           string            `json:"ipv4"`
	IPv6           string            `json:"ipv6"`
	Token          string            `json:"token,omitempty"`
	InstallCommand string            `json:"installCommand,omitempty"`
	Status         *metrics.Snapshot `json:"status,omitempty"`
	Live           []Sample          `json:"live,omitempty"`
}

type summary struct {
	Total    int     `json:"total"`
	Online   int     `json:"online"`
	Offline  int     `json:"offline"`
	CpuAvg   float64 `json:"cpuAvg"`
	MemAvg   float64 `json:"memAvg"`
	DiskAvg  float64 `json:"diskAvg"`
	NetUp    uint64  `json:"netUp"`
	NetDown  uint64  `json:"netDown"`
	TcpCount int     `json:"tcpCount"`
	UdpCount int     `json:"udpCount"`
}

type dashboardPayload struct {
	T            int64        `json:"t"`
	Version      string       `json:"version"`
	PushInterval int          `json:"pushInterval"`
	Servers      []serverView `json:"servers"`
	Summary      summary      `json:"summary"`
}

// dashboard assembles the payload the overview renders from. withLive carries
// each server's rolling sparkline window, which only the first payload a client
// receives needs; see broadcastLoop for why later ticks leave it out.
func (h *Hub) dashboard(withLive bool) dashboardPayload {
	now := time.Now()
	offlineAfter := h.offlineAfter()

	payload := dashboardPayload{
		T:            now.Unix(),
		Version:      Version,
		PushInterval: h.cfg.PushInterval,
		Servers:      []serverView{},
	}

	servers, err := h.store.ListServers()
	if err != nil {
		return payload
	}

	var cpuSum, memSum, diskSum float64
	for _, srv := range servers {
		view := viewOf(srv, now, offlineAfter)
		snap, points, _, ok := h.live.Get(srv.ID)
		if ok {
			view.Status = snap
			if withLive {
				view.Live = points
			}
		}
		if view.Online && snap != nil {
			payload.Summary.Online++
			cpuSum += snap.Cpu
			memSum += snap.Mem.Percent()
			diskSum += snap.Disk.Percent()
			payload.Summary.NetUp += snap.NetIO.Up
			payload.Summary.NetDown += snap.NetIO.Down
			payload.Summary.TcpCount += snap.TcpCount
			payload.Summary.UdpCount += snap.UdpCount
		}
		payload.Servers = append(payload.Servers, view)
	}

	payload.Summary.Total = len(servers)
	payload.Summary.Offline = payload.Summary.Total - payload.Summary.Online
	if payload.Summary.Online > 0 {
		online := float64(payload.Summary.Online)
		payload.Summary.CpuAvg = cpuSum / online
		payload.Summary.MemAvg = memSum / online
		payload.Summary.DiskAvg = diskSum / online
	}
	return payload
}

func viewOf(srv *Server, now time.Time, offlineAfter int64) serverView {
	return serverView{
		ID:           srv.ID,
		Name:         srv.Name,
		Tag:          srv.Tag,
		Sort:         srv.Sort,
		Online:       srv.LastSeen > 0 && now.Unix()-srv.LastSeen <= offlineAfter,
		CreatedAt:    srv.CreatedAt,
		LastSeen:     srv.LastSeen,
		Hostname:     srv.Hostname,
		OS:           srv.OS,
		Arch:         srv.Arch,
		Kernel:       srv.Kernel,
		AgentVersion: srv.AgentVersion,
		IPv4:         srv.IPv4,
		IPv6:         srv.IPv6,
	}
}

func (h *Hub) handleDashboard(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.dashboard(true))
}

// handleStream keeps one Server-Sent Events connection open per open tab. The
// first message is the full payload so the page paints before the next tick.
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if first, err := json.Marshal(h.dashboard(true)); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", first)
		flusher.Flush()
	}

	ch := h.events.subscribe()
	defer h.events.unsubscribe(ch)

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

// handleListServers powers the management screen, so it includes the agent
// token and a ready-to-paste install command.
func (h *Hub) handleListServers(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.ListServers()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}

	now := time.Now()
	offlineAfter := h.offlineAfter()
	base := h.baseURL(r)

	out := []serverView{}
	for _, srv := range servers {
		view := viewOf(srv, now, offlineAfter)
		view.Token = srv.Token
		view.InstallCommand = installCommand(base, srv.Token, srv.Name)
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "baseUrl": base})
}

type serverInput struct {
	Name string `json:"name"`
	Tag  string `json:"tag"`
	Sort int    `json:"sort"`
}

func (h *Hub) handleCreateServer(w http.ResponseWriter, r *http.Request) {
	var in serverInput
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "name is required"})
		return
	}

	srv, err := h.store.CreateServer(in.Name, strings.TrimSpace(in.Tag))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}

	view := viewOf(srv, time.Now(), h.offlineAfter())
	view.Token = srv.Token
	view.InstallCommand = installCommand(h.baseURL(r), srv.Token, srv.Name)
	writeJSON(w, http.StatusOK, view)
}

func (h *Hub) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid id"})
		return
	}
	srv, err := h.store.GetServer(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "server not found"})
		return
	}

	in := serverInput{Name: srv.Name, Tag: srv.Tag, Sort: srv.Sort}
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "name is required"})
		return
	}
	if err := h.store.UpdateServer(id, in.Name, strings.TrimSpace(in.Tag), in.Sort); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid id"})
		return
	}
	if err := h.store.DeleteServer(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	h.live.Remove(id)
	h.alerts.forget(id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) handleRotateToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid id"})
		return
	}
	srv, err := h.store.GetServer(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, apiError{Error: "server not found"})
		return
	}
	token, err := h.store.RotateToken(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"token":          token,
		"installCommand": installCommand(h.baseURL(r), token, srv.Name),
	})
}

var historyRanges = map[string][2]int64{
	"1h":  {3600, 60},
	"6h":  {21600, 300},
	"24h": {86400, 900},
	"7d":  {604800, 3600},
	"30d": {2592000, 10800},
}

func (h *Hub) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid id"})
		return
	}

	key := r.URL.Query().Get("range")
	window, ok := historyRanges[key]
	if !ok {
		key = "1h"
		window = historyRanges[key]
	}

	from := time.Now().Unix() - window[0]
	points, err := h.store.History(id, from, int(window[1]))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"range": key, "bucket": window[1], "points": points})
}

// offlineAfter is the silence, in seconds, that flips a server to offline.
func (h *Hub) offlineAfter() int64 {
	seconds, err := strconv.ParseInt(h.store.Setting("alert.offline", "120"), 10, 64)
	if err != nil || seconds < 10 {
		return 120
	}
	return seconds
}

// baseURL prefers the operator-configured public URL and falls back to the
// address the browser used, which is what a fresh install sees.
func (h *Hub) baseURL(r *http.Request) string {
	if h.cfg.BaseURL != "" {
		return strings.TrimRight(h.cfg.BaseURL, "/")
	}
	if configured := h.store.Setting("hub.baseUrl", ""); configured != "" {
		return strings.TrimRight(configured, "/")
	}
	scheme := "http"
	if isTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func installCommand(base, token, name string) string {
	return fmt.Sprintf("bash <(curl -fsSL %s/install-agent.sh) --hub %s --token %s --name %q",
		base, base, token, name)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func decodeJSON(r *http.Request, dst any) error {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst); err != nil {
		return errors.New("invalid request body")
	}
	return nil
}
