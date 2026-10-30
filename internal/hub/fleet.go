// Package hub is the hub's side of the bays: it accepts their links, keeps
// what each last reported, files their records, and passes commands down.
package hub

import (
	"context"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/proto"
)

// Bay is one bay as the hub last heard it.
type Bay struct {
	Key     string         `json:"key"` // host/name, from the certificate
	Host    string         `json:"host"`
	Name    string         `json:"name"`
	Caps    []string       `json:"caps"`
	Version string         `json:"version"`
	Online  bool           `json:"online"`
	Seen    time.Time      `json:"seen"`
	Snap    model.Snapshot `json:"snapshot"` // State is Offline when !Online
}

// Fleet is what the page needs from the hub.  zaweb's web package is built
// against this; tests use a fake.
type Fleet interface {
	// Bays lists every bay ever seen since the hub started, sorted by Key.
	Bays() []Bay
	Bay(key string) (Bay, bool)
	// Call sends a command down a bay's link and waits for its reply.
	Call(ctx context.Context, key string, req proto.Request) (*model.Snapshot, error)
	// Subscribe ticks whenever any bay's snapshot changes.  Ticks coalesce:
	// a slow reader sees one, not a backlog.  cancel releases it.
	Subscribe() (ticks <-chan struct{}, cancel func())
}
