package hub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/proto"
)

const (
	// maxFrame bounds one line from a bay.  A record with its raw SMART
	// output is well under a megabyte; base64 adds a third.
	maxFrame = 32 << 20
	// handshakeTimeout covers the TLS handshake and the hello.
	handshakeTimeout = 10 * time.Second
	writeTimeout     = 10 * time.Second
)

// peer is one live link from one bay.
type peer struct {
	key, host, name string
	c               *tls.Conn
	closed          chan struct{} // closed when the link is done

	wmu sync.Mutex // one frame on the wire at a time

	mu      sync.Mutex
	waiting map[string]chan *proto.Reply
}

// serve runs one bay's link from handshake to hangup.
func (h *Hub) serve(ctx context.Context, c *tls.Conn) {
	defer c.Close()
	remote := c.RemoteAddr()
	c.SetDeadline(time.Now().Add(handshakeTimeout))
	if err := c.HandshakeContext(ctx); err != nil {
		log.Printf("%s: handshake: %v", remote, err)
		return
	}
	host, name, err := identity(c.ConnectionState())
	if err != nil {
		log.Printf("%s: %v", remote, err)
		return
	}
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 64<<10), maxFrame)
	hl, err := hello(sc, host, name)
	if err != nil {
		log.Printf("%s (bay/%s/%s): %v", remote, host, name, err)
		return
	}
	c.SetDeadline(time.Time{})

	p := &peer{key: host + "/" + name, host: host, name: name, c: c,
		closed: make(chan struct{}), waiting: map[string]chan *proto.Reply{}}
	h.attach(p, hl)
	defer h.detach(p)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	log.Printf("bay %s is online from %s", p.key, remote)
	err = h.read(p, sc)
	log.Printf("bay %s is offline: %v", p.key, err)
}

// identity is the bay a certificate names: CN bay/<host>/<name>.  It's the
// only place the hub learns who it's talking to.
func identity(cs tls.ConnectionState) (host, name string, err error) {
	if len(cs.PeerCertificates) == 0 {
		return "", "", errors.New("no client certificate")
	}
	cn := cs.PeerCertificates[0].Subject.CommonName
	parts := strings.Split(cn, "/")
	if len(parts) != 3 || parts[0] != "bay" || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("certificate CN %q isn't bay/<host>/<name>", cn)
	}
	return parts[1], parts[2], nil
}

// hello reads the first frame and checks it agrees with the certificate.
func hello(sc *bufio.Scanner, host, name string) (*proto.Hello, error) {
	f, err := next(sc)
	if err != nil {
		return nil, fmt.Errorf("no hello: %w", err)
	}
	if f.Type != proto.FrameHello || f.Hello == nil {
		return nil, fmt.Errorf("the first frame was %q, not hello", f.Type)
	}
	if f.Hello.Host != host || f.Hello.Bay != name {
		return nil, fmt.Errorf("hello says bay/%s/%s, which isn't what its certificate says", f.Hello.Host, f.Hello.Bay)
	}
	return f.Hello, nil
}

func next(sc *bufio.Scanner) (proto.Frame, error) {
	var f proto.Frame
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return f, err
		}
		return f, io.EOF
	}
	if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
		return f, fmt.Errorf("a frame that isn't JSON: %w", err)
	}
	return f, nil
}

// read handles frames until the link drops or goes quiet too long.
func (h *Hub) read(p *peer, sc *bufio.Scanner) error {
	for {
		p.c.SetReadDeadline(time.Now().Add(h.silence))
		f, err := next(sc)
		if err != nil {
			return err
		}
		h.heard(p)
		switch f.Type {
		case proto.FrameStatus:
			h.status(p, f.Snapshot)
		case proto.FrameReply:
			p.deliver(f.ID, f.Reply)
		case proto.FrameRecord:
			h.record(p, f.ID, f.Record)
		default:
			log.Printf("bay %s sent a %q frame; ignored", p.key, f.Type)
		}
	}
}

// attach makes p the bay's live link, replacing any older one.  The
// snapshot stays OFFLINE until the bay's first status.
func (h *Hub) attach(p *peer, hl *proto.Hello) {
	h.mu.Lock()
	e, ok := h.bays[p.key]
	if !ok {
		e = &entry{bay: Bay{Key: p.key, Host: p.host, Name: p.name, Snap: offline(p)}}
		h.bays[p.key] = e
	}
	old := e.peer
	e.peer = p
	e.bay.Online, e.bay.Seen = true, time.Now()
	e.bay.Caps, e.bay.Version = append([]string(nil), hl.Caps...), hl.Version
	h.mu.Unlock()
	if old != nil {
		log.Printf("bay %s connected again; dropping its older link", p.key)
		old.c.Close()
	}
	h.tick()
}

// detach marks the bay offline, unless a newer link has already replaced p.
func (h *Hub) detach(p *peer) {
	close(p.closed)
	h.mu.Lock()
	e := h.bays[p.key]
	mine := e.peer == p
	if mine {
		e.peer = nil
		e.bay.Online = false
		e.bay.Snap = offline(p)
	}
	h.mu.Unlock()
	if mine {
		h.tick()
	}
}

// offline is what the wall shows for a bay whose link is down.  Nothing
// from before the drop carries over: the page draws only what a bay reports.
func offline(p *peer) model.Snapshot {
	return model.Snapshot{State: model.Offline, Bay: p.name, Host: p.host}
}

func (h *Hub) heard(p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e := h.bays[p.key]; e.peer == p {
		e.bay.Seen = time.Now()
	}
}

// status files a snapshot, and ticks subscribers if it changed.
func (h *Hub) status(p *peer, s *model.Snapshot) {
	if s == nil {
		return
	}
	if s.State == model.Offline || s.State == "" {
		log.Printf("bay %s reported state %q; ignored", p.key, s.State)
		return
	}
	h.mu.Lock()
	e := h.bays[p.key]
	changed := false
	if e.peer == p {
		changed = !same(e.bay.Snap, *s)
		e.bay.Snap = *s
	}
	h.mu.Unlock()
	if changed {
		h.tick()
	}
}

func same(a, b model.Snapshot) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// record files an uploaded record and tells the bay it's safe to drop.  A
// record the store refuses stays in the bay's spool, and the bay sends it
// again next time it connects.
func (h *Hub) record(p *peer, id string, r *proto.RecordUpload) {
	if r == nil {
		return
	}
	if err := h.cfg.Store.Ingest(r.Serial, r.ID, r.Files); err != nil {
		log.Printf("bay %s sent record %s/%s, and the hub won't keep it: %v", p.key, r.Serial, r.ID, err)
		return
	}
	log.Printf("bay %s: stored record %s/%s", p.key, r.Serial, r.ID)
	if err := p.send(proto.Frame{Type: proto.FrameStored, ID: id}); err != nil {
		log.Printf("bay %s: couldn't confirm record %s/%s: %v", p.key, r.Serial, r.ID, err)
	}
	if h.cfg.OnStored != nil {
		go h.cfg.OnStored(r.Serial, r.ID)
	}
}

func (p *peer) send(f proto.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.c.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := p.c.Write(append(b, '\n')); err != nil {
		p.c.Close()
		return err
	}
	return nil
}

func (p *peer) expect(id string) chan *proto.Reply {
	ch := make(chan *proto.Reply, 1)
	p.mu.Lock()
	p.waiting[id] = ch
	p.mu.Unlock()
	return ch
}

func (p *peer) forget(id string) {
	p.mu.Lock()
	delete(p.waiting, id)
	p.mu.Unlock()
}

// deliver hands a reply to the Call waiting for it.  A reply nobody is
// waiting for, late or made up, goes nowhere.
func (p *peer) deliver(id string, rep *proto.Reply) {
	if rep == nil {
		rep = &proto.Reply{Error: "the bay sent an empty reply"}
	}
	p.mu.Lock()
	ch, ok := p.waiting[id]
	delete(p.waiting, id)
	p.mu.Unlock()
	if ok {
		ch <- rep
	}
}
