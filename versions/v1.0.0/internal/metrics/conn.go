package metrics

import (
	"bufio"
	"os"
	"runtime"

	"github.com/shirou/gopsutil/v4/net"
)

// connectionCounts returns open TCP and UDP socket counts. On Linux it counts
// lines in /proc/net/* — enumerating every socket with its owning process the
// way gopsutil does costs hundreds of milliseconds on a busy box.
func connectionCounts() (tcp int, udp int) {
	if runtime.GOOS == "linux" {
		t4, e1 := countProcNet("/proc/net/tcp")
		t6, _ := countProcNet("/proc/net/tcp6")
		u4, e2 := countProcNet("/proc/net/udp")
		u6, _ := countProcNet("/proc/net/udp6")
		if e1 == nil && e2 == nil {
			return t4 + t6, u4 + u6
		}
	}
	if conns, err := net.Connections("tcp"); err == nil {
		tcp = len(conns)
	}
	if conns, err := net.Connections("udp"); err == nil {
		udp = len(conns)
	}
	return tcp, udp
}

func countProcNet(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	if n > 0 {
		n-- // drop the column header
	}
	return n, nil
}
