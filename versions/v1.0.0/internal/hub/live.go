package hub

import (
	"sync"
	"time"

	"srvmon/internal/metrics"
)

// liveWindow is how many recent ticks the hub keeps in memory per server. At
// the default 2s cadence that is three minutes — enough to hand a freshly
// opened dashboard a filled sparkline instead of a blank one.
const liveWindow = 90

type liveEntry struct {
	snap   *metrics.Snapshot
	recvAt time.Time
	points []Sample
}

// Live holds the newest snapshot per server plus a short rolling window. It is
// deliberately memory-only: losing it on restart costs three minutes of chart.
type Live struct {
	mu sync.RWMutex
	m  map[int64]*liveEntry
}

// NewLive builds an empty live cache.
func NewLive() *Live {
	return &Live{m: map[int64]*liveEntry{}}
}

// Put records a snapshot and appends it to the rolling window.
func (l *Live) Put(serverID int64, snap *metrics.Snapshot, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.m[serverID]
	if entry == nil {
		entry = &liveEntry{points: make([]Sample, 0, liveWindow)}
		l.m[serverID] = entry
	}
	entry.snap = snap
	entry.recvAt = at
	entry.points = append(entry.points, sampleFrom(snap, at.Unix()))
	if len(entry.points) > liveWindow {
		entry.points = entry.points[len(entry.points)-liveWindow:]
	}
}

// Get returns the newest snapshot and rolling window for one server.
func (l *Live) Get(serverID int64) (*metrics.Snapshot, []Sample, time.Time, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	entry := l.m[serverID]
	if entry == nil || entry.snap == nil {
		return nil, nil, time.Time{}, false
	}
	points := make([]Sample, len(entry.points))
	copy(points, entry.points)
	return entry.snap, points, entry.recvAt, true
}

// Remove forgets a deleted server.
func (l *Live) Remove(serverID int64) {
	l.mu.Lock()
	delete(l.m, serverID)
	l.mu.Unlock()
}

// sampleFrom flattens a snapshot into the history shape. The hub stores the
// arrival time rather than the agent's clock so a skewed remote clock cannot
// scatter points across the chart.
func sampleFrom(s *metrics.Snapshot, ts int64) Sample {
	return Sample{
		TS:      ts,
		Cpu:     s.Cpu,
		Mem:     s.Mem.Percent(),
		Swap:    s.Swap.Percent(),
		Disk:    s.Disk.Percent(),
		NetUp:   float64(s.NetIO.Up),
		NetDown: float64(s.NetIO.Down),
		Tcp:     float64(s.TcpCount),
		Udp:     float64(s.UdpCount),
		Load1:   load1Of(s),
	}
}

func load1Of(s *metrics.Snapshot) float64 {
	if len(s.Loads) > 0 {
		return s.Loads[0]
	}
	return 0
}
