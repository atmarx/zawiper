package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/proto"
	"github.com/atmarx/zawiper/internal/records"
)

const (
	// silence is how long a bay can say nothing before the hub calls it
	// offline.  Bays send a heartbeat every 10 s.
	silence = 30 * time.Second
	// callTimeout is how long Call waits for a bay's reply.
	callTimeout = 10 * time.Second
)

// Config is what the hub needs to hold the bays' links.
type Config struct {
	CAFile   string // the CA every bay certificate chains to
	CertFile string // the hub's own certificate
	KeyFile  string
	Addr     string // where to listen; ":8461" when empty

	// Store is where uploaded records are filed.
	Store *records.Store
	// OnStored, if set, runs on its own goroutine after each record is
	// filed and the bay has been told.  serial is the drive's folder name.
	OnStored func(serial, id string)
}

// Hub accepts bays' links and implements Fleet.
type Hub struct {
	cfg     Config
	tls     *tls.Config
	silence time.Duration

	mu   sync.Mutex
	bays map[string]*entry
	subs map[chan struct{}]struct{}
	seq  uint64
}

type entry struct {
	bay  Bay
	peer *peer // the live link, or nil
}

var _ Fleet = (*Hub)(nil)

// New loads the hub's certificate and the bays' CA.  It doesn't listen
// until Run.
func New(cfg Config) (*Hub, error) {
	if cfg.Store == nil {
		return nil, errors.New("the hub needs a records store")
	}
	if cfg.Addr == "" {
		cfg.Addr = ":" + strconv.Itoa(proto.LinkPort)
	}
	tc, err := serverTLS(cfg.CAFile, cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	return &Hub{
		cfg: cfg, tls: tc, silence: silence,
		bays: map[string]*entry{},
		subs: map[chan struct{}]struct{}{},
	}, nil
}

// serverTLS requires TLS 1.3 and a client certificate that chains to the
// CA.  Which bay it is comes from that certificate, later.
func serverTLS(caFile, certFile, keyFile string) (*tls.Config, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("can't read the bays' CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificates", caFile)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("can't load the hub's certificate: %w", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}

// Run listens on Config.Addr until ctx ends.
func (h *Hub) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", h.cfg.Addr)
	if err != nil {
		return err
	}
	log.Printf("listening for bays on %s", ln.Addr())
	return h.Serve(ctx, ln)
}

// Serve accepts bays on a listener the caller made.
func (h *Hub) Serve(ctx context.Context, ln net.Listener) error {
	tl := tls.NewListener(ln, h.tls)
	go func() { <-ctx.Done(); tl.Close() }()
	for {
		c, err := tl.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("accept: %v", err)
			continue
		}
		go h.serve(ctx, c.(*tls.Conn))
	}
}

// Bays lists every bay seen since the hub started, sorted by key.
func (h *Hub) Bays() []Bay {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Bay, 0, len(h.bays))
	for _, e := range h.bays {
		out = append(out, e.copy())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (h *Hub) Bay(key string) (Bay, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.bays[key]
	if !ok {
		return Bay{}, false
	}
	return e.copy(), true
}

func (e *entry) copy() Bay {
	b := e.bay
	b.Caps = append([]string(nil), b.Caps...)
	return b
}

// Call sends a command down a bay's link and waits for its reply.  A refusal
// comes back as an error, with the bay's snapshot when it sent one.
func (h *Hub) Call(ctx context.Context, key string, req proto.Request) (*model.Snapshot, error) {
	if !knownVerb(req.Verb) {
		return nil, fmt.Errorf("%q isn't a command a bay takes", req.Verb)
	}
	p := h.live(key)
	if p == nil {
		return nil, fmt.Errorf("bay %s is offline", key)
	}
	id := h.nextID()
	ch := p.expect(id)
	defer p.forget(id)
	cmd := proto.Request{Verb: req.Verb, Serial: req.Serial, Session: req.Session}
	if err := p.send(proto.Frame{Type: proto.FrameCommand, ID: id, Request: &cmd}); err != nil {
		return nil, fmt.Errorf("the command didn't reach bay %s: %w", key, err)
	}
	t := time.NewTimer(callTimeout)
	defer t.Stop()
	select {
	case rep := <-ch:
		if !rep.OK {
			return rep.Snapshot, errors.New(rep.Error)
		}
		return rep.Snapshot, nil
	case <-p.closed:
		return nil, fmt.Errorf("the link to bay %s dropped before it answered", key)
	case <-t.C:
		return nil, fmt.Errorf("bay %s didn't answer within %s", key, callTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func knownVerb(v proto.Verb) bool {
	switch v {
	case proto.Status, proto.Reread, proto.Wipe, proto.Ack, proto.Eject:
		return true
	}
	return false
}

func (h *Hub) live(key string) *peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e, ok := h.bays[key]; ok {
		return e.peer
	}
	return nil
}

func (h *Hub) nextID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	return "c" + strconv.FormatUint(h.seq, 10)
}

// Subscribe ticks whenever any bay's snapshot or link changes.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
}

// tick tells every subscriber something changed.  A subscriber that hasn't
// read the last tick yet doesn't get a second one.
func (h *Hub) tick() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
