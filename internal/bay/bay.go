package bay

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"zawiper/internal/model"
)

// Bay watches the port and keeps the current snapshot.  One goroutine does
// all the reading, so two reads can never overlap.
type Bay struct {
	Name     string
	Src      Source
	Interval time.Duration
	// OnRead is called after each read, before the snapshot is published.
	// It files the record and returns its ID.
	OnRead func(*model.Snapshot) (string, error)

	kick chan struct{}

	mu   sync.Mutex
	snap model.Snapshot
	sig  string // what's on the port, so a change is noticed
}

func (b *Bay) init() {
	if b.kick == nil {
		b.kick = make(chan struct{}, 1)
		b.snap = model.Snapshot{State: model.Empty, Bay: b.Name}
	}
}

func (b *Bay) Status() model.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	return b.snap
}

// Reread asks for a fresh read of the drive in the bay.
func (b *Bay) Reread() {
	b.mu.Lock()
	b.init()
	b.mu.Unlock()
	select {
	case b.kick <- struct{}{}:
	default: // one is already queued
	}
}

func (b *Bay) Run(ctx context.Context) {
	b.mu.Lock()
	b.init()
	b.mu.Unlock()
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

// Poll reads the bay if what's on the port changed, or if forced.
func (b *Bay) Poll(ctx context.Context, force bool) {
	b.mu.Lock()
	b.init()
	b.mu.Unlock()

	devs, err := b.Src.Devices()
	if err != nil {
		b.publish(model.Snapshot{State: model.Empty, Message: "Can't see the bay: " + err.Error()})
		return
	}
	sig := signature(devs)
	b.mu.Lock()
	changed := sig != b.sig
	b.sig = sig
	session := b.snap.Session
	b.mu.Unlock()
	if !changed && !force {
		return
	}
	if changed {
		session = time.Now().UTC()
	}
	if len(devs) == 0 {
		b.publish(model.Snapshot{State: model.Empty, Session: session})
		return
	}

	b.publish(model.Snapshot{State: model.Reading, Session: session, Message: "Reading identity, SMART, and what's on it."})
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
	b.publish(s)
}

func (b *Bay) publish(s model.Snapshot) {
	s.Bay = b.Name
	b.mu.Lock()
	b.snap = s
	b.mu.Unlock()
	log.Printf("bay: %s %s %s", s.State, s.Drive.Serial(), strings.Join(s.Refusals, " | "))
}

func signature(devs []Device) string {
	var sb strings.Builder
	for _, d := range devs {
		fmt.Fprintf(&sb, "%s:%d;", d.Name, d.Size)
	}
	return sb.String()
}
