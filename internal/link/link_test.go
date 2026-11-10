package link_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/hub"
	"github.com/atmarx/zawiper/internal/link"
	"github.com/atmarx/zawiper/internal/link/testpki"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/proto"
	"github.com/atmarx/zawiper/internal/records"
)

const key = "dock1/a"

// disk is a fake disk side.
type disk struct {
	mu      sync.Mutex
	state   model.State
	rereads int
	serial  string
	session time.Time
}

func (d *disk) Status() model.Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return model.Snapshot{State: d.state, Bay: "a"}
}
func (d *disk) Reread() { d.mu.Lock(); d.rereads++; d.mu.Unlock() }
func (d *disk) Wipe(serial string, session time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.serial, d.session, d.state = serial, session, model.Running
	return nil
}
func (d *disk) Ack() error   { return nil }
func (d *disk) Eject() error { return errors.New("the drive is busy") }

func (d *disk) set(s model.State) { d.mu.Lock(); d.state = s; d.mu.Unlock() }

type rig struct {
	hub    *hub.Hub
	store  *records.Store
	link   *link.Link
	stored chan [2]string
	stop   context.CancelFunc // drops the link
}

// newRig starts a hub, and a fake disk side when d isn't nil.  The link
// isn't started until start, so a test can fill the spool first.
func newRig(t *testing.T, d *disk) *rig {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ca := testpki.New(t)
	r := &rig{store: &records.Store{Root: t.TempDir()}, stored: make(chan [2]string, 4)}

	hubCert, hubKey := ca.Server(t, "hub")
	h, err := hub.New(hub.Config{CAFile: ca.CAFile, CertFile: hubCert, KeyFile: hubKey, Store: r.store,
		OnStored: func(serial, id string) { r.stored <- [2]string{serial, id} }})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go h.Serve(ctx, ln)
	r.hub = h

	dir := t.TempDir()
	bayCert, bayKey := ca.Client(t, "a", "bay/dock1/a")
	tc, cn, err := link.ClientTLS(ca.CAFile, bayCert, bayKey)
	if err != nil {
		t.Fatal(err)
	}
	host, name, err := link.ParseCN(cn)
	if err != nil {
		t.Fatal(err)
	}
	r.link = &link.Link{Bay: name, Host: host, Hub: ln.Addr().String(), TLS: tc,
		Socket: filepath.Join(dir, "bayd.sock"), State: filepath.Join(dir, "job.json"),
		Spool: filepath.Join(dir, "spool"), Caps: []string{"wipe"}, Version: "test",
		Backoff: 50 * time.Millisecond}
	if d != nil {
		go proto.Serve(ctx, r.link.Socket, -1, d)
	}
	return r
}

func (r *rig) start(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r.stop = cancel
	go r.link.Run(ctx)
}

func (r *rig) bay() hub.Bay { b, _ := r.hub.Bay(key); return b }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// spoolRecord leaves a finished record in the spool, settled.
func spoolRecord(t *testing.T, spool, serial, id string) string {
	t.Helper()
	dir := filepath.Join(spool, "drives", serial, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 10, 24, 9, 15, 40, 0, time.UTC)
	js, _ := json.Marshal(records.Record{Schema: 1, ID: id, Kind: "wipe", Serial: serial,
		Started: started, Finished: started.Add(time.Hour), Host: "dock1", Bay: "a"})
	for name, b := range map[string][]byte{
		"record.json": js, "record.md": []byte("# " + serial + "\n"), "notes.txt": []byte("not a record file"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(spool, "drives", serial, "drive.json"), []byte("{}"), 0o644)
	old := time.Now().Add(-time.Minute)
	os.Chtimes(filepath.Join(dir, "record.json"), old, old)
	return dir
}

func TestRoundTrip(t *testing.T) {
	d := &disk{state: model.Present}
	r := newRig(t, d)
	const serial, id = "TESTSER1", "2026-10-24T09-15-40Z-wipe"
	spooled := spoolRecord(t, r.link.Spool, serial, id)
	ticks, cancel := r.hub.Subscribe()
	defer cancel()
	r.start(t)

	waitFor(t, "the bay to come online", func() bool {
		b := r.bay()
		return b.Online && b.Snap.State == model.Present
	})
	if bays := r.hub.Bays(); len(bays) != 1 || bays[0].Host != "dock1" || bays[0].Name != "a" || bays[0].Version != "test" {
		t.Errorf("bays %+v", bays)
	}

	// A status change on the disk side ticks Subscribe.
	drain(ticks)
	d.set(model.Reading)
	select {
	case <-ticks:
	case <-time.After(5 * time.Second):
		t.Fatal("no tick after the disk side changed")
	}
	waitFor(t, "READING on the hub", func() bool { return r.bay().Snap.State == model.Reading })

	// Commands reach the disk side and come back.
	ctx := context.Background()
	if s, err := r.hub.Call(ctx, key, proto.Request{Verb: proto.Reread}); err != nil || s == nil {
		t.Errorf("reread: %v %v", s, err)
	}
	session := time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)
	s, err := r.hub.Call(ctx, key, proto.Request{Verb: proto.Wipe, Serial: serial, Session: session})
	if err != nil || s.State != model.Running {
		t.Errorf("wipe: %v %v", s, err)
	}
	d.mu.Lock()
	if d.rereads != 1 || d.serial != serial || !d.session.Equal(session) {
		t.Errorf("the disk side saw rereads %d, wipe %q %v", d.rereads, d.serial, d.session)
	}
	d.mu.Unlock()
	if _, err := r.hub.Call(ctx, key, proto.Request{Verb: proto.Eject}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Errorf("the disk side's refusal came back as %v", err)
	}

	// The spooled record lands on the hub and leaves the spool.
	select {
	case got := <-r.stored:
		if got != [2]string{serial, id} {
			t.Errorf("OnStored %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the record never arrived")
	}
	rec, err := r.store.Record(serial, id)
	if err != nil || rec.Serial != serial {
		t.Fatalf("the hub's copy: %v %v", rec, err)
	}
	if _, err := os.Stat(filepath.Join(r.store.Root, "drives", serial, id, "notes.txt")); err == nil {
		t.Error("a file outside records.Files was uploaded")
	}
	waitFor(t, "the spool to empty", func() bool {
		_, err := os.Stat(filepath.Dir(spooled))
		return os.IsNotExist(err)
	})
}

func drain(ch <-chan struct{}) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func TestDroppedLinkIsOffline(t *testing.T) {
	r := newRig(t, &disk{state: model.Empty})
	r.start(t)
	waitFor(t, "the bay to come online", func() bool { return r.bay().Online })
	r.stop()
	waitFor(t, "the bay to go offline", func() bool {
		b := r.bay()
		return !b.Online && b.Snap.State == model.Offline
	})
	if _, err := r.hub.Call(context.Background(), key, proto.Request{Verb: proto.Status}); err == nil {
		t.Error("a call to an offline bay went through")
	}
	if len(r.hub.Bays()) != 1 {
		t.Error("an offline bay left the list")
	}
}

func TestInterruptedFromStateFile(t *testing.T) {
	r := newRig(t, nil) // no drive, so no disk side
	writeJob(t, r.link.State, model.Running)
	r.start(t)
	waitFor(t, "INTERRUPTED", func() bool { return r.bay().Snap.State == model.Interrupted })
	if j := r.bay().Snap.Job; j == nil || j.Serial != "9VPAX9NA" || j.Phase != "write" {
		t.Errorf("job %+v", j)
	}

	ctx := context.Background()
	if _, err := r.hub.Call(ctx, key, proto.Request{Verb: proto.Wipe, Serial: "9VPAX9NA"}); err == nil || !strings.Contains(err.Error(), "no drive") {
		t.Errorf("wipe with no disk side: %v", err)
	}
	s, err := r.hub.Call(ctx, key, proto.Request{Verb: proto.Ack})
	if err != nil || s.State != model.Empty {
		t.Fatalf("ack: %v %v", s, err)
	}
	// The link's sandbox can't touch the state file; it leaves a note in
	// the spool, and bayd clears the file when it next starts.
	if _, err := os.Stat(filepath.Join(r.link.Spool, "acked.json")); err != nil {
		t.Errorf("no acknowledgement in the spool: %v", err)
	}
	if recs, _ := filepath.Glob(filepath.Join(r.link.Spool, "drives", "9VPAX9NA", "*-wipe", "record.json")); len(recs) != 1 {
		t.Errorf("the interrupted job's record isn't in the spool: %v", recs)
	}
	waitFor(t, "EMPTY", func() bool { return r.bay().Snap.State == model.Empty })

	// A drive pulled after DONE: its record is already filed, so the ack
	// just clears the wall.
	writeJobAt(t, r.link.State, model.Done, time.Now())
	waitFor(t, "DONE", func() bool { return r.bay().Snap.State == model.Done })
	if s, err := r.hub.Call(ctx, key, proto.Request{Verb: proto.Ack}); err != nil || s.State != model.Empty {
		t.Errorf("ack of DONE without the drive: %v %v", s, err)
	}
}

func writeJob(t *testing.T, path string, s model.State) {
	t.Helper()
	writeJobAt(t, path, s, time.Time{})
}

func writeJobAt(t *testing.T, path string, s model.State, session time.Time) {
	t.Helper()
	js, _ := json.Marshal(model.JobFile{State: s, Job: model.Job{Kind: "wipe", Phase: "write", Serial: "9VPAX9NA", Session: session}})
	if err := os.WriteFile(path, js, 0o644); err != nil {
		t.Fatal(err)
	}
}
