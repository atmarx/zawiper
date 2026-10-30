// Package link is a bay's side of the hub: bayd-link.  It dials out to the
// hub, says hello, reports what the disk side says, passes the hub's commands
// to the disk side, and uploads finished records from the spool.  It never
// touches a disk itself.
package link

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/atmarx/zawiper/internal/proto"
)

const (
	pollEvery   = time.Second      // ask the disk side for status
	heartbeat   = 10 * time.Second // send status even when nothing changed
	spoolEvery  = 10 * time.Second // look for finished records
	maxBackoff  = 30 * time.Second
	dialTimeout = 10 * time.Second
	sendTimeout = 10 * time.Second
	// maxFrame bounds one line from the hub.  Commands and stored frames
	// are tiny.
	maxFrame = 1 << 20
)

// Link is one bay's connection to the hub.
type Link struct {
	Bay     string      // the bay's name, as in its certificate
	Host    string      // the dock host, as in its certificate
	Hub     string      // host:port
	TLS     *tls.Config // from ClientTLS
	Socket  string      // the disk side's unix socket
	State   string      // the disk side's job state file
	Spool   string      // finished records waiting for the hub
	Caps    []string
	Version string

	// Backoff is the first wait after a failed connection; it doubles up
	// to 30 s.  One second when zero.
	Backoff time.Duration
}

// Run keeps the link up until ctx ends, reconnecting with backoff.
func (l *Link) Run(ctx context.Context) error {
	first := l.Backoff
	if first <= 0 {
		first = time.Second
	}
	wait := first
	for {
		start := time.Now()
		err := l.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			wait = first // it held for a while; this is a fresh failure
		}
		log.Printf("link to %s: %v; trying again in %s", l.Hub, err, wait)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		wait = min(wait*2, maxBackoff)
	}
}

// session is one connection, from dial to drop.
type session struct {
	l       *Link
	w       *wire
	pending map[string]string // record frame ID → spool folder
	poke    chan struct{}     // poll status now, after a command

	last     []byte // the last snapshot sent, as JSON
	lastSent time.Time
	lastErr  string
}

func (l *Link) session(ctx context.Context) error {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout, KeepAlive: 15 * time.Second}, Config: l.TLS}
	c, err := d.DialContext(ctx, "tcp", l.Hub)
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	s := &session{l: l, w: &wire{c: c}, pending: map[string]string{}, poke: make(chan struct{}, 1)}
	hello := &proto.Hello{Host: l.Host, Bay: l.Bay, Version: l.Version, Caps: l.Caps}
	if err := s.w.send(proto.Frame{Type: proto.FrameHello, Hello: hello}); err != nil {
		return err
	}
	log.Printf("connected to %s as bay/%s/%s", l.Hub, l.Host, l.Bay)

	in := make(chan proto.Frame)
	errc := make(chan error, 1)
	go func() { errc <- readFrames(ctx, c, in) }()

	s.status(ctx)
	s.upload()
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	spool := time.NewTicker(spoolEvery)
	defer spool.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case f := <-in:
			s.handle(ctx, f)
		case <-poll.C:
			s.status(ctx)
		case <-s.poke:
			s.status(ctx)
		case <-spool.C:
			s.upload()
		}
		if s.w.broken() {
			return fmt.Errorf("can't write to the hub")
		}
	}
}

// readFrames passes the hub's frames to the session until the link drops.
// A line that isn't a frame ends the link: something is badly wrong.
func readFrames(ctx context.Context, r io.Reader, in chan<- proto.Frame) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), maxFrame)
	for sc.Scan() {
		var f proto.Frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			return fmt.Errorf("the hub sent something that isn't a frame: %w", err)
		}
		select {
		case in <- f:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// handle acts on a frame from the hub.  Only two kinds are ever acted on: a
// command with a verb from the contract, and stored for a record this
// session sent.  Everything else is logged and dropped.
func (s *session) handle(ctx context.Context, f proto.Frame) {
	switch f.Type {
	case proto.FrameCommand:
		if f.Request == nil {
			s.reply(f.ID, proto.Reply{Error: "a command with no request"})
			return
		}
		// Copy only the fields the contract has, whatever else came.
		req := proto.Request{Verb: f.Request.Verb, Serial: f.Request.Serial, Session: f.Request.Session}
		go func() {
			s.reply(f.ID, s.l.command(ctx, req))
			s.pokeStatus()
		}()
	case proto.FrameStored:
		s.stored(f.ID)
	default:
		log.Printf("the hub sent a %q frame; ignored", f.Type)
	}
}

func (s *session) reply(id string, rep proto.Reply) {
	if err := s.w.send(proto.Frame{Type: proto.FrameReply, ID: id, Reply: &rep}); err != nil {
		log.Printf("reply to the hub: %v", err)
	}
}

func (s *session) pokeStatus() {
	select {
	case s.poke <- struct{}{}:
	default:
	}
}

// status asks the disk side, and tells the hub when the answer changed or a
// heartbeat is due.
func (s *session) status(ctx context.Context) {
	snap, err := s.l.snapshot(ctx)
	if err != nil {
		// The disk side is there but didn't answer.  Say nothing new
		// rather than guess; the heartbeat repeats the last snapshot.
		if err.Error() != s.lastErr {
			log.Printf("status: %v", err)
			s.lastErr = err.Error()
		}
		if s.last != nil && time.Since(s.lastSent) >= heartbeat {
			s.sendStatus(s.last)
		}
		return
	}
	s.lastErr = ""
	js, _ := json.Marshal(snap)
	if bytes.Equal(js, s.last) && time.Since(s.lastSent) < heartbeat {
		return
	}
	s.sendStatus(js)
}

// sendStatus sends a snapshot already in JSON, so a heartbeat repeats
// exactly what the hub last heard.
func (s *session) sendStatus(js []byte) {
	f := struct {
		Type     string          `json:"type"`
		Snapshot json.RawMessage `json:"snapshot"`
	}{proto.FrameStatus, js}
	if err := s.w.sendAny(f); err != nil {
		log.Printf("status to the hub: %v", err)
		return
	}
	s.last, s.lastSent = js, time.Now()
}

// wire writes frames, one at a time, from any goroutine.  A failed write
// closes the connection, which ends the session.
type wire struct {
	mu   sync.Mutex
	c    net.Conn
	dead bool
}

func (w *wire) send(f proto.Frame) error { return w.sendAny(f) }

func (w *wire) sendAny(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead {
		return net.ErrClosed
	}
	w.c.SetWriteDeadline(time.Now().Add(sendTimeout))
	if _, err := w.c.Write(append(b, '\n')); err != nil {
		w.dead = true
		w.c.Close()
		return err
	}
	return nil
}

func (w *wire) broken() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dead
}
