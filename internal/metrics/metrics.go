// Package metrics collects a single host's vital signs. The JSON shape is the
// wire contract between the agent and the hub, and mirrors the field names the
// dashboard already speaks.
package metrics

import (
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// CurTotal is a used/capacity pair. The dashboard derives the percentage.
type CurTotal struct {
	Current uint64 `json:"current"`
	Total   uint64 `json:"total"`
}

// Snapshot is one tick of a host's state as pushed to the hub.
type Snapshot struct {
	T           int64   `json:"t"`
	Agent       string  `json:"agent"`
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`
	Platform    string  `json:"platform"`
	Kernel      string  `json:"kernel"`
	Arch        string  `json:"arch"`
	Uptime      uint64  `json:"uptime"`
	Cpu         float64 `json:"cpu"`
	CpuCores    int     `json:"cpuCores"`
	LogicalPro  int     `json:"logicalPro"`
	CpuSpeedMhz float64 `json:"cpuSpeedMhz"`
	CpuModel    string  `json:"cpuModel"`

	Mem   CurTotal  `json:"mem"`
	Swap  CurTotal  `json:"swap"`
	Disk  CurTotal  `json:"disk"`
	Loads []float64 `json:"loads"`

	NetIO struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent uint64 `json:"sent"`
		Recv uint64 `json:"recv"`
	} `json:"netTraffic"`

	TcpCount  int `json:"tcpCount"`
	UdpCount  int `json:"udpCount"`
	ProcCount int `json:"procCount"`

	PublicIP struct {
		IPv4 string `json:"ipv4"`
		IPv6 string `json:"ipv6"`
	} `json:"publicIP"`
}

// Percent returns the used share of a CurTotal, 0 when capacity is unknown.
func (c CurTotal) Percent() float64 {
	if c.Total == 0 {
		return 0
	}
	return float64(c.Current) * 100 / float64(c.Total)
}

// Collector turns cumulative counters into per-second rates, so it must be
// reused across ticks — a fresh one always reports zero throughput.
type Collector struct {
	mu sync.Mutex

	agentVersion string
	diskPath     string

	lastCPU    cpu.TimesStat
	hasLastCPU bool
	emaCPU     float64

	lastSent, lastRecv uint64
	lastNetAt          time.Time

	cpuSpeed   float64
	cpuModel   string
	cpuQueried bool

	hostInfo   *host.InfoStat
	publicIP   [2]string
	publicIPAt time.Time
}

// NewCollector builds a collector; diskPath selects the filesystem reported as
// "storage" (empty means the platform root).
func NewCollector(agentVersion, diskPath string) *Collector {
	if diskPath == "" {
		diskPath = defaultDiskPath()
	}
	return &Collector{agentVersion: agentVersion, diskPath: diskPath}
}

func defaultDiskPath() string {
	if runtime.GOOS == "windows" {
		return "C:\\"
	}
	return "/"
}

// Collect reads every metric once. Errors on individual probes are swallowed:
// a host that cannot report swap should still report CPU.
func (c *Collector) Collect() *Snapshot {
	now := time.Now()
	s := &Snapshot{T: now.Unix(), Agent: c.agentVersion, Loads: []float64{0, 0, 0}}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.hostInfo == nil {
		if hi, err := host.Info(); err == nil {
			c.hostInfo = hi
		}
	}
	if c.hostInfo != nil {
		s.Hostname = c.hostInfo.Hostname
		s.OS = c.hostInfo.Platform + " " + c.hostInfo.PlatformVersion
		s.Platform = c.hostInfo.Platform
		s.Kernel = c.hostInfo.KernelVersion
		s.Arch = c.hostInfo.KernelArch
	}
	if s.Arch == "" {
		s.Arch = runtime.GOARCH
	}

	s.Cpu = c.sampleCPU()
	if cores, err := cpu.Counts(false); err == nil {
		s.CpuCores = cores
	}
	s.LogicalPro = runtime.NumCPU()
	c.fillCPUInfo(s)

	if up, err := host.Uptime(); err == nil {
		s.Uptime = up
	}
	if m, err := mem.VirtualMemory(); err == nil {
		s.Mem = CurTotal{Current: m.Used, Total: m.Total}
	}
	if sw, err := mem.SwapMemory(); err == nil {
		s.Swap = CurTotal{Current: sw.Used, Total: sw.Total}
	}
	if d, err := disk.Usage(c.diskPath); err == nil {
		s.Disk = CurTotal{Current: d.Used, Total: d.Total}
	}
	if avg, err := load.Avg(); err == nil {
		s.Loads = []float64{avg.Load1, avg.Load5, avg.Load15}
	}

	c.sampleNetwork(s, now)

	s.TcpCount, s.UdpCount = connectionCounts()
	if pids, err := process.Pids(); err == nil {
		s.ProcCount = len(pids)
	}

	c.refreshPublicIP(now)
	s.PublicIP.IPv4 = c.publicIP[0]
	s.PublicIP.IPv6 = c.publicIP[1]

	return s
}

// sampleCPU derives utilisation from the delta between two cumulative time
// readings, smoothed with an EMA so a single busy tick does not spike the tile.
func (c *Collector) sampleCPU() float64 {
	times, err := cpu.Times(false)
	if err != nil || len(times) == 0 {
		return c.emaCPU
	}
	cur := times[0]
	if !c.hasLastCPU {
		c.lastCPU = cur
		c.hasLastCPU = true
		return 0
	}

	busy := (cur.User - c.lastCPU.User) + (cur.System - c.lastCPU.System) +
		(cur.Nice - c.lastCPU.Nice) + (cur.Iowait - c.lastCPU.Iowait) +
		(cur.Irq - c.lastCPU.Irq) + (cur.Softirq - c.lastCPU.Softirq) +
		(cur.Steal - c.lastCPU.Steal)
	total := busy + (cur.Idle - c.lastCPU.Idle)
	c.lastCPU = cur
	if total <= 0 {
		return c.emaCPU
	}

	pct := busy * 100 / total
	if pct < 0 {
		pct = 0
	} else if pct > 100 {
		pct = 100
	}

	const alpha = 0.3
	if c.emaCPU == 0 {
		c.emaCPU = pct
	} else {
		c.emaCPU = alpha*pct + (1-alpha)*c.emaCPU
	}
	return c.emaCPU
}

func (c *Collector) fillCPUInfo(s *Snapshot) {
	if !c.cpuQueried {
		c.cpuQueried = true
		if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
			c.cpuSpeed = infos[0].Mhz
			c.cpuModel = strings.TrimSpace(infos[0].ModelName)
		}
	}
	s.CpuSpeedMhz = c.cpuSpeed
	s.CpuModel = c.cpuModel
}

func (c *Collector) sampleNetwork(s *Snapshot, now time.Time) {
	stats, err := net.IOCounters(true)
	if err != nil {
		return
	}
	var sent, recv uint64
	for _, iface := range stats {
		if isVirtualInterface(strings.ToLower(iface.Name)) {
			continue
		}
		sent += iface.BytesSent
		recv += iface.BytesRecv
	}
	s.NetTraffic.Sent = sent
	s.NetTraffic.Recv = recv

	if !c.lastNetAt.IsZero() {
		secs := now.Sub(c.lastNetAt).Seconds()
		if secs > 0 {
			if sent >= c.lastSent {
				s.NetIO.Up = uint64(float64(sent-c.lastSent) / secs)
			}
			if recv >= c.lastRecv {
				s.NetIO.Down = uint64(float64(recv-c.lastRecv) / secs)
			}
		}
	}
	c.lastSent, c.lastRecv, c.lastNetAt = sent, recv, now
}

// isVirtualInterface filters loopback, container and tunnel devices so a
// wireguard or docker bridge does not double-count real uplink traffic.
func isVirtualInterface(name string) bool {
	if name == "lo" || name == "lo0" {
		return true
	}
	prefixes := []string{"loopback", "docker", "br-", "veth", "virbr", "tun", "tap", "wg", "tailscale", "zt", "utun"}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
