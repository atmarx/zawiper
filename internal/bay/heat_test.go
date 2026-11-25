package bay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/smart"
)

// thermometer scripts the fixture's temperature: the next reading in the list,
// holding the last one.  A zero is a read that reports nothing.
type thermometer struct {
	mu    sync.Mutex
	r     *rig
	temps []int
	reads int
}

func (th *thermometer) next() int {
	th.mu.Lock()
	defer th.mu.Unlock()
	t := th.temps[min(th.reads, len(th.temps)-1)]
	th.reads++
	return t
}

// heatSource reads the script.  Identity checks still get the fixture.
type heatSource struct {
	*Dev
	th *thermometer
}

func (h *heatSource) Temperature(context.Context, Device) (int, error) {
	return h.th.next(), nil
}

func heatRig(t *testing.T, temps ...int) (*rig, *thermometer) {
	t.Helper()
	r := newRig(t)
	th := &thermometer{r: r, temps: temps}
	r.b.Src = &heatSource{Dev: r.src, th: th}
	r.b.HeatEvery, r.b.CoolEvery = time.Nanosecond, 5*time.Millisecond
	return r, th
}

func heatRecord(t *testing.T, r *rig, s model.Snapshot) *records.Record {
	t.Helper()
	js, err := os.ReadFile(filepath.Join(r.dir, "spool", "drives", records.DirName(serial), s.Job.RecordID, "record.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec records.Record
	if err := json.Unmarshal(js, &rec); err != nil {
		t.Fatal(err)
	}
	return &rec
}

func TestAHotDrivePausesAndCarriesOn(t *testing.T) {
	// Cool, over the 55 °C ceiling, cooling but not enough, cool enough.
	r, _ := heatRig(t, 40, 60, 53, 50, 45)
	r.wipe(t)
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	rec := heatRecord(t, r, end)
	h := rec.Heat
	if h == nil || h.Pauses < 1 || h.PeakC != 60 || h.CeilingC != smart.CeilingSpinning {
		t.Fatalf("heat: %+v", h)
	}
	if !rec.Wipe.OK() {
		t.Errorf("a paused wipe doesn't prove the same: %+v", rec.Wipe.Method)
	}
	found := false
	for _, f := range rec.Health.Findings {
		if f.Tag == "PAUSED" {
			found = true
			if f.Fail || f.About == smart.Drive {
				t.Errorf("a pause counted against the drive: %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("no PAUSED finding: %+v", rec.Health.Findings)
	}
	if !strings.Contains(records.HeatLine(rec), "peak 60 °C") {
		t.Errorf("line: %s", records.HeatLine(rec))
	}
	if !strings.Contains(records.Markdown(rec), "| Temperature | peak 60") {
		t.Error("the record doesn't say how hot it ran")
	}
	if end.Job.Paused {
		t.Error("the job ended still paused")
	}
}

func TestACoolDriveNeverPauses(t *testing.T) {
	r, _ := heatRig(t, 40)
	r.wipe(t)
	end := r.settle(t)
	rec := heatRecord(t, r, end)
	if rec.Heat.Pauses != 0 || rec.Heat.WaitedS != 0 || rec.Heat.PeakC != 40 {
		t.Errorf("heat: %+v", rec.Heat)
	}
	for _, f := range rec.Health.Findings {
		if f.Tag == "PAUSED" {
			t.Errorf("finding: %+v", f)
		}
	}
}

func TestABaysCeilingOnlyGoesDown(t *testing.T) {
	r, _ := heatRig(t, 40)
	r.b.TempCeiling = 35
	r.wipe(t)
	// It waits at 40, over 35, and a cooler drive lets it go.
	r.b.Src.(*heatSource).th.mu.Lock()
	r.b.Src.(*heatSource).th.temps = []int{40, 40, 30}
	r.b.Src.(*heatSource).th.reads = 0
	r.b.Src.(*heatSource).th.mu.Unlock()
	end := r.settle(t)
	if rec := heatRecord(t, r, end); rec.Heat.CeilingC != 35 || !strings.Contains(rec.Heat.Why, "temp_ceiling") {
		t.Errorf("heat: %+v", rec.Heat)
	}

	s := &Bay{TempCeiling: 90}
	j := &job{read: model.Snapshot{Drive: &model.Drive{SMART: &smart.Info{RotationRate: 7200}}}}
	if h := s.newHeat(j); h.ceiling != smart.CeilingSpinning {
		t.Errorf("a config raised the ceiling to %d", h.ceiling)
	}
}

func TestPauseShowsOnTheJobAndStaysOutOfTheTimings(t *testing.T) {
	// Hold the drive hot until the test has seen the pause.
	r, th := heatRig(t, 60)
	r.b.Block, r.b.Buf = devBlock, devBlock
	r.wipe(t)
	deadline := time.Now().Add(10 * time.Second)
	var seen model.Snapshot
	for time.Now().Before(deadline) {
		seen = r.b.Status()
		if seen.Job != nil && seen.Job.Paused {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if seen.Job == nil || !seen.Job.Paused || seen.Job.ResumeC != smart.CeilingSpinning-coolMargin || seen.State != model.Running {
		t.Fatalf("not paused: %+v", seen.Job)
	}
	// Hold the pause long against a block of a tmp file, even on a busy CI
	// host, where one took 0.143 s (2026-11-15).
	const hold = 600 * time.Millisecond
	time.Sleep(hold)
	th.mu.Lock()
	th.temps = []int{40}
	th.reads = 0
	th.mu.Unlock()
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	rec := heatRecord(t, r, end)
	for i, c := range rec.Wipe.Cells {
		if c.Write > (hold / 2).Seconds() {
			t.Errorf("block %d's write took %.3f s: the pause landed in the timing", i, c.Write)
		}
	}
	if len(rec.Wipe.Slow(false)) != 0 {
		t.Errorf("a pause read as slow blocks: %v", rec.Wipe.Slow(false))
	}
}

func TestABrokenThermometerDoesntStopTheJob(t *testing.T) {
	// It never reports anything: no reading, no pause, and the job ends.
	r, _ := heatRig(t, 0)
	r.wipe(t)
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	rec := heatRecord(t, r, end)
	if rec.Heat.Pauses != 0 {
		t.Errorf("heat: %+v", rec.Heat)
	}
	said := false
	for _, f := range rec.Health.Findings {
		said = said || f.Tag == "NO-TEMP"
	}
	if !said {
		t.Error("a job that never read the temperature didn't say so")
	}
}

func TestShutdownDuringAPauseEndsTheWait(t *testing.T) {
	r := newRig(t)
	th := &thermometer{r: r, temps: []int{70}}
	r.b.Src = &heatSource{Dev: r.src, th: th}
	r.b.HeatEvery, r.b.CoolEvery = time.Nanosecond, time.Hour
	ctx, cancel := contextWithCancel()
	r.b.base = ctx
	r.wipe(t)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := r.b.Status(); s.Job != nil && s.Job.Paused {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() { r.b.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a paused job held up shutdown")
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
