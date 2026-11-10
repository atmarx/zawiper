package bay

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
)

// Five 256 KiB blocks, the last one short.
const (
	devBlock = 256 << 10
	devSize  = 4*devBlock + 4096
	serial   = "9VPAX9NA"
)

type rig struct {
	b    *Bay
	src  *Dev
	dir  string
	snap model.Snapshot // the read the confirm is drawn from
}

// newRig is a bay on the Dev source: a file for the drive, a copy of the
// fixture for SMART, and a state file and spool of its own.
func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	js, err := os.ReadFile("../smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{dir: dir, src: &Dev{File: filepath.Join(dir, "bay.img"), Smart: filepath.Join(dir, "smart.json")}}
	os.WriteFile(r.src.Smart, js, 0o644)
	if err := os.WriteFile(r.src.File, bytes.Repeat([]byte("old data "), devSize/9+1)[:devSize], 0o644); err != nil {
		t.Fatal(err)
	}
	r.b = r.bay()
	r.read(t)
	return r
}

func (r *rig) bay() *Bay {
	return &Bay{Name: "test", Src: r.src, Host: "dock1", Software: "bayd test",
		StatePath: filepath.Join(r.dir, "state", "job.json"), Spool: &Spool{Root: filepath.Join(r.dir, "spool")},
		Block: devBlock, Buf: 64 << 10}
}

func (r *rig) read(t *testing.T) {
	t.Helper()
	r.b.Poll(context.Background(), false)
	r.snap = r.b.Status()
	if r.snap.State != model.Present || len(r.snap.Refusals) > 0 {
		t.Fatalf("the dev bay didn't read clean: %s %s %v", r.snap.State, r.snap.Message, r.snap.Refusals)
	}
}

func (r *rig) wipe(t *testing.T) {
	t.Helper()
	if err := r.b.Wipe(serial, r.snap.Session); err != nil {
		t.Fatal(err)
	}
}

// settle waits for the job to end.
func (r *rig) settle(t *testing.T) model.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := r.b.Status(); s.State != model.Running {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("the job never ended")
	return model.Snapshot{}
}

func (r *rig) stateFile(t *testing.T) model.JobFile {
	t.Helper()
	js, err := os.ReadFile(r.b.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var jf model.JobFile
	if err := json.Unmarshal(js, &jf); err != nil {
		t.Fatal(err)
	}
	return jf
}

func (r *rig) record(t *testing.T, id string) *records.Record {
	t.Helper()
	rec, err := (&records.Store{Root: r.b.Spool.Root}).Record(serial, id)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestWipeDone(t *testing.T) {
	r := newRig(t)
	r.wipe(t)
	s := r.settle(t)
	if s.State != model.Done || s.Job == nil {
		t.Fatalf("%s: %s", s.State, s.Message)
	}
	j := s.Job
	if strings.Trim(j.Map, "vs") != "" || len(j.Map) != 5 {
		t.Errorf("map %q", j.Map)
	}
	if len(j.WriteMBps) != 5 || len(j.ReadMBps) != 5 || slices.Contains(j.WriteMBps, 0) || slices.Contains(j.ReadMBps, 0) {
		t.Errorf("rates %v %v", j.WriteMBps, j.ReadMBps)
	}
	if j.Done != 2*devSize || j.Total != devSize || j.Bad != 0 || j.Phase != "record" || j.Ended.IsZero() {
		t.Errorf("job %+v", j)
	}
	if s.Host != "dock1" || j.RecordID == "" || !strings.Contains(s.Message, "Grade") {
		t.Errorf("host %q record %q message %q", s.Host, j.RecordID, s.Message)
	}
	if jf := r.stateFile(t); jf.State != model.Done || jf.Job.RecordID != j.RecordID {
		t.Errorf("state file %s %q", jf.State, jf.Job.RecordID)
	}

	rec := r.record(t, j.RecordID)
	if rec.Kind != "wipe" || rec.Wipe == nil || !rec.Wipe.OK() || !rec.Wrote || rec.Before == nil || rec.Host != "dock1" {
		t.Errorf("record %+v", rec)
	}
	dir := filepath.Join(r.b.Spool.Root, "drives", serial, j.RecordID)
	for _, f := range []string{"record.md", "smart.json", "smart-before.json", "smart-before.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("spool: %v", err)
		}
	}
	// Built in staging, moved in whole: nothing of it stays behind.
	if _, err := os.Stat(filepath.Join(r.b.Spool.Root, stageDir, "drives", serial, j.RecordID)); !os.IsNotExist(err) {
		t.Errorf("staging still holds the record: %v", err)
	}
	if b, _ := os.ReadFile(r.src.File); bytes.Contains(b, []byte("old data")) {
		t.Error("the old data survived")
	}

	// DONE holds until acknowledged, and nothing new starts meanwhile.
	r.b.Poll(context.Background(), true)
	if r.b.Status().State != model.Done {
		t.Error("a poll cleared DONE")
	}
	if err := r.b.Wipe(serial, r.snap.Session); err == nil || !strings.Contains(err.Error(), "acknowledge") {
		t.Errorf("wipe over DONE: %v", err)
	}
	if err := r.b.Ack(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.b.StatePath); !os.IsNotExist(err) {
		t.Errorf("state file after ack: %v", err)
	}
	r.b.Poll(context.Background(), false)
	if s := r.b.Status(); s.State != model.Present || s.Session.Equal(r.snap.Session) {
		t.Errorf("after ack: %s, same session %v", s.State, s.Session.Equal(r.snap.Session))
	}
}

func TestWipeRefusals(t *testing.T) {
	r := newRig(t)
	cases := map[string]struct {
		serial  string
		session time.Time
		want    string
	}{
		"wrong serial":  {"Z1D2XXXX", r.snap.Session, "not Z1D2XXXX"},
		"no serial":     {"", r.snap.Session, "didn't say which drive"},
		"stale session": {serial, r.snap.Session.Add(-time.Minute), "earlier insertion"},
	}
	for name, c := range cases {
		if err := r.b.Wipe(c.serial, c.session); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if r.b.Status().State != model.Present {
		t.Error("a refusal changed the state")
	}

	// A swapped drive: same serial, but the port shows something else.
	os.Truncate(r.src.File, devSize+devBlock)
	if err := r.b.Wipe(serial, r.snap.Session); err == nil || !strings.Contains(err.Error(), "doesn't show the drive") {
		t.Errorf("changed port: %v", err)
	}

	empty := &Bay{Name: "empty", Src: &Dev{File: filepath.Join(t.TempDir(), "none")}}
	empty.Poll(context.Background(), false)
	if err := empty.Wipe(serial, time.Time{}); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Errorf("empty bay: %v", err)
	}

	f := drive(t)
	f.devs[0].Size = 4140000000 // the placeholder size a two-slot ASM1156 dock has reported
	refused := &Bay{Name: "refused", Src: f, Spool: &Spool{Root: t.TempDir()}}
	refused.Poll(context.Background(), false)
	s := refused.Status()
	if err := refused.Wipe(serial, s.Session); err == nil || !strings.Contains(err.Error(), "refused this drive") {
		t.Errorf("refusals present: %v", err)
	}
}

func TestOneJobAtATime(t *testing.T) {
	r := newRig(t)
	hold, release := make(chan struct{}), make(chan struct{})
	r.b.hook = func(pass string, i int) {
		if pass == "write" && i == 0 {
			close(hold)
			<-release
		}
	}
	r.wipe(t)
	<-hold
	if err := r.b.Wipe(serial, r.snap.Session); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Errorf("second wipe: %v", err)
	}
	if err := r.b.Ack(); err == nil {
		t.Error("ack while running")
	}
	if err := r.b.Eject(); err == nil {
		t.Error("eject while running")
	}
	r.b.Poll(context.Background(), true) // a re-read must not happen under a wipe
	s := r.b.Status()
	if s.State != model.Running || s.Job.Map[0] != model.CellWritten || s.Job.Map[1] != model.CellTodo {
		t.Errorf("mid-job: %s %q", s.State, s.Job.Map)
	}
	if jf := r.stateFile(t); jf.State != model.Running || jf.Job.Phase != "write" || jf.Job.Done != devBlock {
		t.Errorf("state file mid-job: %s %s %d", jf.State, jf.Job.Phase, jf.Job.Done)
	}
	close(release)
	if s := r.settle(t); s.State != model.Done {
		t.Errorf("%s: %s", s.State, s.Message)
	}
}

// The live map shows the block in hand as W, then R.
func TestLiveMap(t *testing.T) {
	r := newRig(t)
	var seen []string
	r.b.hook = func(pass string, i int) {
		if i == 1 {
			seen = append(seen, r.b.Status().Job.Map)
		}
	}
	r.wipe(t)
	r.settle(t)
	if len(seen) != 2 || seen[0] != "ww..." || seen[1] != "vvwww" {
		t.Errorf("maps after block 2 of each pass: %q", seen)
	}
}

// Whatever the poll loop or the identity check sees mid-job ends it.
func TestJobStops(t *testing.T) {
	cases := map[string]struct {
		do       func(r *rig)
		check    bool // the identity check, not the poll loop, catches it
		state    model.State
		evidence string
	}{
		"vanished": {func(r *rig) { os.Remove(r.src.File) }, false, model.Failed, "left the bay during the write pass, in block #2"},
		"resized":  {func(r *rig) { os.Truncate(r.src.File, devSize*2) }, false, model.Alarm, "not 1,052,672"},
		"serial changed": {func(r *rig) {
			js, _ := os.ReadFile(r.src.Smart)
			os.WriteFile(r.src.Smart, bytes.ReplaceAll(js, []byte(serial), []byte("Z1D2XXXX")), 0o644)
		}, true, model.Alarm, "now answers as serial Z1D2XXXX, not 9VPAX9NA, during the write pass, at byte 524,288"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.b.CheckEvery = devBlock
			r.b.hook = func(pass string, i int) {
				if pass == "write" && i == 1 {
					c.do(r)
					if !c.check {
						r.b.Poll(context.Background(), false)
					}
				}
			}
			r.wipe(t)
			s := r.settle(t)
			if s.State != c.state || !strings.Contains(s.Job.Evidence, c.evidence) || s.Message != s.Job.Evidence {
				t.Fatalf("%s: %q", s.State, s.Job.Evidence)
			}
			if jf := r.stateFile(t); jf.State != c.state {
				t.Errorf("state file says %s", jf.State)
			}
			rec := r.record(t, s.Job.RecordID)
			if rec.Wipe == nil || rec.Wipe.OK() || !rec.Wrote || rec.Health.Grade == "PASS" {
				t.Errorf("record %+v", rec.Wipe)
			}
			if c.check && rec.Wipe.Alarm == "" {
				t.Error("the record doesn't carry the alarm")
			}
			if strings.ContainsAny(s.Job.Map, "WR") || s.Job.Map[4] != model.CellTodo {
				t.Errorf("map %q", s.Job.Map)
			}
			if err := r.b.Eject(); err == nil {
				t.Errorf("ejected over %s without an ack", s.State)
			}
		})
	}
}

// bayd going down mid-job leaves RUNNING in the state file; the next start
// calls it INTERRUPTED, and acking it files what's known.
func TestInterrupted(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	r.b.base = ctx
	r.b.hook = func(pass string, i int) {
		if pass == "write" && i == 1 {
			cancel()
		}
	}
	r.wipe(t)
	r.b.Wait()
	if jf := r.stateFile(t); jf.State != model.Running {
		t.Fatalf("after shutdown the state file says %s", jf.State)
	}

	r.b = r.bay() // the next start
	r.b.restore()
	s := r.b.Status()
	if s.State != model.Interrupted || s.Job.Serial != serial || s.Drive.Serial() != serial || !strings.Contains(s.Message, "won't pretend") {
		t.Fatalf("restored %s %q", s.State, s.Message)
	}
	if jf := r.stateFile(t); jf.State != model.Interrupted {
		t.Errorf("state file says %s", jf.State)
	}
	r.b.Poll(context.Background(), true)
	if r.b.Status().State != model.Interrupted {
		t.Error("a poll cleared INTERRUPTED")
	}
	if err := r.b.Wipe(serial, s.Session); err == nil {
		t.Error("wiped over INTERRUPTED")
	}
	if err := r.b.Ack(); err != nil {
		t.Fatal(err)
	}
	ids, _ := (&records.Store{Root: r.b.Spool.Root}).Records(serial)
	if len(ids) != 1 {
		t.Fatalf("records %v", ids)
	}
	rec := r.record(t, ids[0])
	if !rec.Wrote || rec.Wipe == nil || !strings.Contains(rec.Wipe.Aborted, "went down") || rec.Wipe.Written != 2*devBlock ||
		rec.Before == nil || rec.Drive.SMART == nil || !strings.Contains(strings.Join(rec.Health.Notes, " "), "won't pretend") {
		t.Errorf("interrupted record %+v %+v", rec.Wipe, rec.Health)
	}
	r.b.Poll(context.Background(), false)
	if s := r.b.Status(); s.State != model.Present {
		t.Errorf("after ack: %s", s.State)
	}
}

// A state file left by hand, or garbled, still comes back INTERRUPTED.
func TestInterruptedStaleFile(t *testing.T) {
	for name, content := range map[string]string{
		"running": `{"state":"RUNNING","job":{"kind":"wipe","phase":"verify","serial":"9VPAX9NA","map":"vvR..","done":1500000,"total":1052672}}`,
		"garbled": `{"state":`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			b := &Bay{Name: "test", Src: &Dev{File: filepath.Join(dir, "none")}, StatePath: filepath.Join(dir, "job.json"), Spool: &Spool{Root: filepath.Join(dir, "spool")}}
			os.WriteFile(b.StatePath, []byte(content), 0o644)
			b.restore()
			if s := b.Status(); s.State != model.Interrupted || s.Message == "" {
				t.Fatalf("%s %q", s.State, s.Message)
			}
			if err := b.Ack(); err != nil {
				t.Fatal(err)
			}
			b.Poll(context.Background(), false)
			if s := b.Status(); s.State != model.Empty {
				t.Errorf("after ack: %s", s.State)
			}
		})
	}
}

// The link acknowledges with the drive gone, sandboxed: it can write the
// spool and nothing else.  Its record and its note go in the spool, and the
// next bayd clears the state file.
func TestAckAbsentWritesOnlyTheSpool(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory this test leans on")
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	spool := filepath.Join(dir, "spool")
	os.MkdirAll(state, 0o755)
	os.MkdirAll(spool, 0o755)
	stateFile := filepath.Join(state, "job.json")
	js := `{"state":"RUNNING","job":{"kind":"wipe","phase":"write","serial":"9VPAX9NA","session":"2026-11-10T02:05:11Z","total":1052672}}`
	os.WriteFile(stateFile, []byte(js), 0o644)
	os.Chmod(state, 0o555) // what ProtectSystem=strict leaves the link
	defer os.Chmod(state, 0o755)

	link := &Bay{Name: "test", StatePath: stateFile, Spool: &Spool{Root: spool}}
	if err := link.AckAbsent(); err != nil {
		t.Fatal(err)
	}
	if recs, _ := filepath.Glob(filepath.Join(spool, "drives", "9VPAX9NA", "*-wipe", "record.json")); len(recs) != 1 {
		t.Errorf("record not filed: %v", recs)
	}
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("the state file went, so this test proves nothing: %v", err)
	}
	if !Acked(spool, &model.Job{Kind: "wipe", Serial: "9VPAX9NA", Session: time.Date(2026, 11, 10, 2, 5, 11, 0, time.UTC)}) {
		t.Error("no note in the spool")
	}

	os.Chmod(state, 0o755)
	b := &Bay{Name: "test", Src: &Dev{File: filepath.Join(dir, "none")}, StatePath: stateFile, Spool: &Spool{Root: spool}}
	b.restore()
	if s := b.Status(); s.State == model.Interrupted {
		t.Errorf("bayd restored an acknowledged job: %s", s.State)
	}
	for _, p := range []string{stateFile, filepath.Join(spool, ackedName)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still there: %v", p, err)
		}
	}
}

func TestEject(t *testing.T) {
	r := newRig(t)
	if err := r.b.Eject(); err != nil {
		t.Fatal(err)
	}
	r.b.Poll(context.Background(), false)
	if s := r.b.Status(); s.State != model.Empty {
		t.Fatalf("after eject: %s", s.State)
	}
	if err := r.b.Eject(); err == nil || !strings.Contains(err.Error(), "nothing in the bay") {
		t.Errorf("second eject: %v", err)
	}
	// Reinserting the dev drive is touching the file.
	later := time.Now().Add(time.Minute)
	os.Chtimes(r.src.File, later, later)
	r.read(t)

	// From DONE, eject clears the job.
	r.wipe(t)
	r.settle(t)
	if err := r.b.Eject(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.b.StatePath); !os.IsNotExist(err) {
		t.Errorf("state file after eject: %v", err)
	}
	if s := r.b.Status(); s.State != model.Empty || s.Job != nil {
		t.Errorf("after eject from DONE: %s", s.State)
	}
}
