package bay

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/records"
)

// Bay watches the port, keeps the current snapshot, and runs the bay's one
// job.  One goroutine does all the reading, so two reads can never overlap,
// and no read happens while a job holds the drive.
type Bay struct {
	Name     string
	Src      Source
	Interval time.Duration
	// OnRead is called after each read, before the snapshot is published.
	// It files the record and returns its ID.
	OnRead func(*model.Snapshot) (string, error)

	// StatePath is the job's state file, written before every transition
	// so a power cut leaves a trace.  Spool is where wipe records wait for
	// the link to ship them.  Host and Software go in those records.
	StatePath      string
	Spool          *Spool
	Host, Software string
	// ReadOnly is a bay whose config doesn't grant "wipe": it reads and
	// files records, and refuses every job.
	ReadOnly bool
	// Direct opens the drive with O_DIRECT.  Off only in dev mode and
	// tests, where the drive is a file.
	Direct bool
	// Block, Buf and CheckEvery shrink a wipe for dev mode and tests.
	Block, Buf, CheckEvery int64

	kick chan struct{}
	base context.Context // jobs run under it; cancelling it is a power cut

	mu   sync.Mutex
	snap model.Snapshot
	sig  string // what's on the port, so a change is noticed
	dev  Device // the device the snapshot describes
	job  *job   // the running job, or nil
	hook func(pass string, i int)
}

func (b *Bay) init() {
	if b.kick == nil {
		b.kick = make(chan struct{}, 1)
		b.snap = model.Snapshot{State: model.Empty, Bay: b.Name, Host: b.Host}
	}
}

// Status is a copy of the snapshot: the live job is copied too, so nobody
// reads it while the job writes it.
func (b *Bay) Status() model.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	s := b.snap
	if s.Job != nil {
		j := *s.Job
		// The job's rates fill in place; the copy can't share them.
		j.WriteMBps, j.ReadMBps = slices.Clone(j.WriteMBps), slices.Clone(j.ReadMBps)
		s.Job = &j
	}
	return s
}

// Reread asks for a fresh read of the drive in the bay.  While a job runs or
// waits for an acknowledgement, the poll ignores it.
func (b *Bay) Reread() {
	b.mu.Lock()
	b.init()
	b.mu.Unlock()
	b.nudge()
}

func (b *Bay) nudge() {
	select {
	case b.kick <- struct{}{}:
	default: // one is already queued
	}
}

func (b *Bay) Run(ctx context.Context) {
	b.mu.Lock()
	b.init()
	b.base = ctx
	b.mu.Unlock()
	b.restore()
	if b.Interval == 0 {
		b.Interval = 2 * time.Second
	}
	t := time.NewTicker(b.Interval)
	defer t.Stop()
	b.Poll(ctx, false)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.Poll(ctx, false)
		case <-b.kick:
			b.Poll(ctx, true)
		}
	}
}

// Poll reads the bay if what's on the port changed, or if forced.  While a
// job runs it only watches the port: it never re-reads or sets the drive
// read-only under a wipe.  A finished job holds the bay until Ack.
func (b *Bay) Poll(ctx context.Context, force bool) {
	devs, err := b.Src.Devices()
	b.mu.Lock()
	b.init()
	if j := b.job; j != nil {
		b.mu.Unlock()
		if err == nil {
			b.watch(j, devs)
		}
		return
	}
	if held(b.snap.State) {
		b.mu.Unlock()
		return
	}
	if err != nil {
		b.set(model.Snapshot{State: model.Empty, Message: "Can't see the bay: " + err.Error()})
		b.mu.Unlock()
		return
	}
	sig := signature(devs)
	changed := sig != b.sig
	b.sig = sig
	session := b.snap.Session
	if !changed && !force {
		b.mu.Unlock()
		return
	}
	if changed {
		session = time.Now().UTC()
	}
	if len(devs) == 0 {
		b.set(model.Snapshot{State: model.Empty, Session: session})
		b.mu.Unlock()
		return
	}
	// READING is set under the lock, so a wipe (which needs PRESENT) can't
	// start between deciding to read and reading.
	b.set(model.Snapshot{State: model.Reading, Session: session, Message: "Reading identity, SMART, and what's on it."})
	b.mu.Unlock()

	s := Read(ctx, b.Src, devs)
	s.Session = session
	if s.State == model.Present && s.Drive.Serial() != "" && b.OnRead != nil {
		id, err := b.OnRead(&s)
		if err != nil {
			log.Printf("record: %v", err)
			s.Refusals = append(s.Refusals, "Couldn't save the record: "+err.Error())
		}
		s.RecordID = id
	}
	b.mu.Lock()
	b.dev = devs[0]
	b.set(s)
	b.mu.Unlock()
}

// set publishes a snapshot.  The caller holds b.mu.
func (b *Bay) set(s model.Snapshot) {
	s.Bay, s.Host = b.Name, b.Host
	b.snap = s
	log.Printf("bay: %s %s %s", s.State, s.Drive.Serial(), strings.Join(s.Refusals, " | "))
}

func signature(devs []Device) string {
	var sb strings.Builder
	for _, d := range devs {
		fmt.Fprintf(&sb, "%s:%d;", d.Name, d.Size)
	}
	return sb.String()
}

// Spool is where finished records wait for the link to ship them.  The link
// uploads any record folder holding a record.json, and a records.Store
// writes its files in no particular order, so each record is built in a
// staging store beside the spool (Root + ".tmp", the same filesystem) and
// renamed in whole.  drive.json stays in staging: the hub rebuilds its own.
type Spool struct {
	Root string // /var/lib/zawiper/<bay>/spool
}

func (s *Spool) SaveInspect(r *records.Record, smartJSON, smartText []byte) error {
	return s.file(r, func(st *records.Store) error { return st.SaveInspect(r, smartJSON, smartText) })
}

func (s *Spool) SaveWipe(r *records.Record, raw map[string][]byte) error {
	return s.file(r, func(st *records.Store) error { return st.SaveWipe(r, raw) })
}

func (s *Spool) SaveInterrupted(r *records.Record, raw map[string][]byte) error {
	return s.file(r, func(st *records.Store) error { return st.SaveInterrupted(r, raw) })
}

func (s *Spool) file(r *records.Record, save func(*records.Store) error) error {
	stage := &records.Store{Root: s.Root + ".tmp"}
	if err := save(stage); err != nil {
		return err
	}
	serial := records.DirName(r.Serial)
	from := filepath.Join(stage.Root, "drives", serial, r.ID)
	to := filepath.Join(s.Root, "drives", serial, r.ID)
	if err := os.MkdirAll(filepath.Dir(to), 0o777); err != nil {
		return err
	}
	// A re-read of the same insertion files under the same ID.  Rename
	// can't replace a folder that has files in it, so the old copy goes
	// first; the link ships the new one.
	if err := os.RemoveAll(to); err != nil {
		return err
	}
	return os.Rename(from, to)
}

func find(devs []Device, name string) *Device {
	for i := range devs {
		if devs[i].Name == name {
			return &devs[i]
		}
	}
	return nil
}
