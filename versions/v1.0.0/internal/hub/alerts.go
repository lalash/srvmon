package hub

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"srvmon/internal/metrics"
)

// alertState tracks one server's standing so the engine only notifies on
// transitions, never once per tick.
type alertState struct {
	firing  map[string]bool
	streak  map[string]int
	offline bool
}

type alertEngine struct {
	hub *Hub

	mu    sync.Mutex
	state map[int64]*alertState
}

func newAlertEngine(h *Hub) *alertEngine {
	return &alertEngine{hub: h, state: map[int64]*alertState{}}
}

// stateFor must be called with the mutex held.
func (a *alertEngine) stateFor(serverID int64) *alertState {
	st := a.state[serverID]
	if st == nil {
		st = &alertState{firing: map[string]bool{}, streak: map[string]int{}}
		a.state[serverID] = st
	}
	return st
}

func (a *alertEngine) forget(serverID int64) {
	a.mu.Lock()
	delete(a.state, serverID)
	a.mu.Unlock()
}

// observe evaluates the usage thresholds on every incoming snapshot. A metric
// must stay over its threshold for `sustain` consecutive pushes before it
// fires, which filters out momentary spikes.
func (a *alertEngine) observe(server *Server, snap *metrics.Snapshot, now time.Time) {
	if !a.enabled() {
		return
	}

	sustain := a.hub.settingInt(keyAlertSustain, 3)
	if sustain < 1 {
		sustain = 1
	}

	checks := []struct {
		kind      string
		value     float64
		threshold float64
	}{
		{"cpu", snap.Cpu, a.hub.settingFloat(keyAlertCPU, 90)},
		{"memory", snap.Mem.Percent(), a.hub.settingFloat(keyAlertMem, 85)},
		{"disk", snap.Disk.Percent(), a.hub.settingFloat(keyAlertDisk, 90)},
	}

	type pending struct {
		kind  string
		state string
		value float64
	}
	var events []pending

	a.mu.Lock()
	st := a.stateFor(server.ID)
	if st.offline {
		st.offline = false
		events = append(events, pending{"offline", "cleared", 0})
	}

	for _, check := range checks {
		if check.value >= check.threshold {
			st.streak[check.kind]++
			if st.streak[check.kind] >= sustain && !st.firing[check.kind] {
				st.firing[check.kind] = true
				events = append(events, pending{check.kind, "firing", check.value})
			}
			continue
		}
		st.streak[check.kind] = 0
		// Clear only once usage drops a clear 5 points below the threshold, so
		// a value hovering on the line does not flap between the two states.
		if st.firing[check.kind] && check.value < check.threshold-5 {
			st.firing[check.kind] = false
			events = append(events, pending{check.kind, "cleared", check.value})
		}
	}
	a.mu.Unlock()

	for _, e := range events {
		a.fire(server.ID, server.Name, e.kind, e.state, e.value, now)
	}
}

// run watches for silence. Usage alerts ride on incoming pushes; an offline
// server by definition sends none, so it needs a timer of its own.
func (a *alertEngine) run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.checkOffline()
		}
	}
}

func (a *alertEngine) checkOffline() {
	if !a.enabled() {
		return
	}
	servers, err := a.hub.store.ListServers()
	if err != nil {
		return
	}

	now := time.Now()
	limit := a.hub.offlineAfter()

	for _, server := range servers {
		if server.LastSeen == 0 {
			continue // never reported; nothing to go quiet
		}
		down := now.Unix()-server.LastSeen > limit

		a.mu.Lock()
		st := a.stateFor(server.ID)
		changed := down && !st.offline
		if changed {
			st.offline = true
		}
		a.mu.Unlock()

		if changed {
			a.fire(server.ID, server.Name, "offline", "firing", float64(now.Unix()-server.LastSeen), now)
		}
	}
}

func (a *alertEngine) enabled() bool {
	return a.hub.store.Setting(keyAlertsEnabled, "1") == "1"
}

func (a *alertEngine) fire(serverID int64, name, kind, state string, value float64, now time.Time) {
	event := AlertEvent{
		ServerID:   serverID,
		ServerName: name,
		Kind:       kind,
		State:      state,
		Value:      value,
		TS:         now.Unix(),
	}
	if err := a.hub.store.AddAlertEvent(event); err != nil {
		log.Printf("store alert event: %v", err)
	}
	log.Printf("alert %s %s on %q (%.1f)", kind, state, name, value)

	if message := alertMessage(event); message != "" {
		if err := a.hub.sendTelegram(message); err != nil {
			log.Printf("telegram alert: %v", err)
		}
	}
}

func alertMessage(e AlertEvent) string {
	when := time.Unix(e.TS, 0).Format("2006-01-02 15:04:05")
	switch {
	case e.Kind == "offline" && e.State == "firing":
		return fmt.Sprintf("🔴 *%s* is offline\nNo data for %.0f seconds\n%s", e.ServerName, e.Value, when)
	case e.Kind == "offline":
		return fmt.Sprintf("🟢 *%s* is back online\n%s", e.ServerName, when)
	case e.State == "firing":
		return fmt.Sprintf("⚠️ *%s* — %s at %.1f%%\n%s", e.ServerName, e.Kind, e.Value, when)
	default:
		return fmt.Sprintf("✅ *%s* — %s back to normal (%.1f%%)\n%s", e.ServerName, e.Kind, e.Value, when)
	}
}
