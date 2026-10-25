package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"zawiper/internal/hub"
	"zawiper/internal/model"
)

// The live wall.  On a Fleet tick, one frame is built and its bytes go to
// every viewer, unchanged.  Ten people watching get the same frame (§4.3).

// minGap keeps frames to a few a second, however fast the bays report.
const minGap = 250 * time.Millisecond

// heartbeat keeps proxies from closing a quiet stream.
const heartbeat = 20 * time.Second

type frame struct {
	Bays []frameBay `json:"bays"`
}

type frameBay struct {
	Key   string      `json:"key"`
	State model.State `json:"state"`
	// Sig changes when the page has to redraw the bay, rather than update it.
	Sig  string `json:"sig"`
	Live *Live  `json:"live,omitempty"` // RUNNING only
}

func buildFrame(f hub.Fleet) []byte {
	now := time.Now()
	fr := frame{Bays: []frameBay{}}
	for _, b := range f.Bays() {
		fb := frameBay{Key: b.Key, State: stateOf(b), Sig: Sig(b)}
		if fb.State == model.Running && b.Snap.Job != nil {
			fb.Live = LiveOf(b.Snap.Job, now)
		}
		fr.Bays = append(fr.Bays, fb)
	}
	blob, _ := json.Marshal(fr)
	return blob
}

type events struct {
	fleet hub.Fleet
	once  sync.Once

	mu   sync.Mutex
	subs map[chan []byte]struct{}
	last []byte
}

func newEvents(f hub.Fleet) *events {
	return &events{fleet: f, subs: map[chan []byte]struct{}{}}
}

// pump turns Fleet ticks into frames.  It starts with the first viewer and
// runs for the life of the process.
func (e *events) pump() {
	ticks, _ := e.fleet.Subscribe()
	var sent time.Time
	for range ticks {
		if wait := minGap - time.Since(sent); wait > 0 {
			time.Sleep(wait)
		}
		// A tick that came in while we waited is covered by this frame.
		select {
		case <-ticks:
		default:
		}
		sent = time.Now()
		b := buildFrame(e.fleet)
		e.mu.Lock()
		if !bytes.Equal(b, e.last) {
			e.last = b
			for ch := range e.subs {
				offer(ch, b)
			}
		}
		e.mu.Unlock()
	}
}

// offer hands a viewer the newest frame.  A viewer who hasn't taken the last
// one yet loses it: the newer frame says everything it did.
func offer(ch chan []byte, b []byte) {
	select {
	case ch <- b:
	default:
		select {
		case <-ch:
		default:
		}
		ch <- b
	}
}

func (e *events) subscribe() (chan []byte, func()) {
	e.once.Do(func() { go e.pump() })
	ch := make(chan []byte, 1)
	e.mu.Lock()
	if e.last == nil {
		e.last = buildFrame(e.fleet)
	}
	ch <- e.last
	e.subs[ch] = struct{}{}
	e.mu.Unlock()
	return ch, func() {
		e.mu.Lock()
		delete(e.subs, ch)
		e.mu.Unlock()
	}
}

func (e *events) serve(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, cancel := e.subscribe()
	defer cancel()

	fmt.Fprint(w, "retry: 2000\n\n")
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-ch:
			if _, err := fmt.Fprintf(w, "event: frame\ndata: %s\n\n", b); err != nil {
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": still here\n\n"); err != nil {
				return
			}
		}
		if rc.Flush() != nil {
			return
		}
	}
}
