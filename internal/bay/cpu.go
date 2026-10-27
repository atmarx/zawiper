package bay

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cpuMeter measures what a job costs the dock host between two polls:
// bayd's own CPU, from getrusage, and the host's, from /proc/stat.  The
// first look only sets the baseline.
type cpuMeter struct {
	root string // /proc, or a test's stand-in

	at        time.Time
	self      time.Duration // bayd's user and system time
	busy, all uint64        // the host's jiffies
}

type cpuLoad struct {
	Self, Host, Load float64
	Cores            int
}

// sample returns the load since the last look, and false on the first look,
// after a gap of more than a minute (the bay was idle between jobs), or
// when nothing could be read.
func (m *cpuMeter) sample(now time.Time) (cpuLoad, bool) {
	root := m.root
	if root == "" {
		root = "/proc"
	}
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return cpuLoad{}, false
	}
	self := time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	busy, all, hostOK := hostJiffies(root)

	prev := *m
	m.at, m.self, m.busy, m.all = now, self, busy, all
	wall := now.Sub(prev.at)
	if prev.at.IsZero() || wall <= 0 || wall > time.Minute {
		return cpuLoad{}, false
	}
	l := cpuLoad{Self: 100 * float64(self-prev.self) / float64(wall), Cores: runtime.NumCPU()}
	if hostOK && all > prev.all && busy >= prev.busy {
		l.Host = 100 * float64(busy-prev.busy) / float64(all-prev.all)
	}
	l.Load = loadAvg(root)
	return l, true
}

// hostJiffies reads the first line of /proc/stat: user nice system idle
// iowait irq softirq steal.  Guest time is already counted in user.
func hostJiffies(root string) (busy, all uint64, ok bool) {
	b, err := os.ReadFile(filepath.Join(root, "stat"))
	if err != nil {
		return 0, 0, false
	}
	line, _, _ := strings.Cut(string(b), "\n")
	f := strings.Fields(line)
	if len(f) < 9 || f[0] != "cpu" {
		return 0, 0, false
	}
	var v [8]uint64
	for i := range v {
		if v[i], err = strconv.ParseUint(f[i+1], 10, 64); err != nil {
			return 0, 0, false
		}
		all += v[i]
	}
	idle := v[3] + v[4]
	return all - idle, all, true
}

func loadAvg(root string) float64 {
	b, err := os.ReadFile(filepath.Join(root, "loadavg"))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}
