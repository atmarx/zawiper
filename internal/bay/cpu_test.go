package bay

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCPUMeter(t *testing.T) {
	root := t.TempDir()
	put := func(stat, load string) {
		t.Helper()
		for name, s := range map[string]string{"stat": stat, "loadavg": load} {
			if err := os.WriteFile(filepath.Join(root, name), []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	m := cpuMeter{root: root}
	t0 := time.Now()
	// user nice system idle iowait irq softirq steal guest guest_nice
	put("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 1 2 3\n", "0.50 0.40 0.30 1/200 999\n")
	if _, ok := m.sample(t0); ok {
		t.Error("the first look is only the baseline")
	}
	put("cpu  250 0 150 1150 150 0 0 0 0 0\n", "1.25 0.40 0.30 1/200 999\n")
	l, ok := m.sample(t0.Add(2 * time.Second))
	if !ok {
		t.Fatal("the second look should measure")
	}
	// 200 busy of 700 jiffies.
	if l.Host < 28.5 || l.Host > 28.6 || l.Load != 1.25 || l.Cores < 1 || l.Self < 0 {
		t.Errorf("%+v", l)
	}
	if _, ok := m.sample(t0.Add(2 * time.Minute)); ok {
		t.Error("a look after an idle gap measures the gap")
	}
}

func TestUSBSpeed(t *testing.T) {
	for in, want := range map[string]int{"480": 480, "5000": 5000, "1.5": 1, "": 0, "unknown": 0} {
		if got := usbSpeed(in); got != want {
			t.Errorf("usbSpeed(%q) = %d, want %d", in, got, want)
		}
	}
}
