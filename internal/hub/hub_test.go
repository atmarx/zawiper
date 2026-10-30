package hub

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/link/testpki"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/proto"
	"github.com/atmarx/zawiper/internal/records"
)

func startHub(t *testing.T, ca *testpki.CA) (*Hub, string) {
	t.Helper()
	cert, key := ca.Server(t, "hub")
	h, err := New(Config{CAFile: ca.CAFile, CertFile: cert, KeyFile: key, Store: &records.Store{Root: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Serve(ctx, ln)
	return h, ln.Addr().String()
}

// fakeBay is a bay's end of the link, driven by hand.
type fakeBay struct {
	c  *tls.Conn
	sc *bufio.Scanner
}

// dial connects with a client certificate from issuer, trusting hubCA.
func dial(t *testing.T, addr string, hubCA, issuer *testpki.CA, cn string) *fakeBay {
	t.Helper()
	cert, key := issuer.Client(t, "bay", cn)
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	pem, _ := os.ReadFile(hubCA.CAFile)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	c, err := tls.Dial("tcp", addr, &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &fakeBay{c: c, sc: bufio.NewScanner(c)}
}

func (b *fakeBay) send(f proto.Frame) error {
	js, _ := json.Marshal(f)
	_, err := b.c.Write(append(js, '\n'))
	return err
}

func (b *fakeBay) hello(host, name string) error {
	return b.send(proto.Frame{Type: proto.FrameHello, Hello: &proto.Hello{Host: host, Bay: name, Caps: []string{"wipe"}}})
}

// dropped says the hub hung up.
func (b *fakeBay) dropped() bool {
	b.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	return !b.sc.Scan()
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOtherCARefused(t *testing.T) {
	ca, stranger := testpki.New(t), testpki.New(t)
	h, addr := startHub(t, ca)
	b := dial(t, addr, ca, stranger, "bay/dock1/a")
	// TLS 1.3 finishes the client's handshake before the hub has judged
	// its certificate; the refusal shows up on the first read.
	b.hello("dock1", "a")
	if !b.dropped() {
		t.Error("the hub kept a link from a certificate it shouldn't trust")
	}
	if len(h.Bays()) != 0 {
		t.Errorf("bays %v", h.Bays())
	}
}

func TestHelloMustMatchCertificate(t *testing.T) {
	ca := testpki.New(t)
	h, addr := startHub(t, ca)
	for _, c := range []struct{ cn, host, name string }{
		{"bay/dock1/a", "dock1", "b"},
		{"bay/dock1/a", "dock2", "a"},
		{"hub", "hub", ""},
		{"bay/dock1/a/extra", "dock1", "a/extra"},
	} {
		b := dial(t, addr, ca, ca, c.cn)
		b.hello(c.host, c.name)
		if !b.dropped() {
			t.Errorf("%s saying %s/%s: still connected", c.cn, c.host, c.name)
		}
	}
	if len(h.Bays()) != 0 {
		t.Errorf("bays %v", h.Bays())
	}
}

func TestSilenceIsOffline(t *testing.T) {
	ca := testpki.New(t)
	h, addr := startHub(t, ca)
	h.silence = 300 * time.Millisecond
	b := dial(t, addr, ca, ca, "bay/dock1/a")
	b.hello("dock1", "a")
	b.send(proto.Frame{Type: proto.FrameStatus, Snapshot: &model.Snapshot{State: model.Empty}})
	waitFor(t, "the bay to show up", func() bool {
		bay, ok := h.Bay("dock1/a")
		return ok && bay.Online && bay.Snap.State == model.Empty
	})
	bay, _ := h.Bay("dock1/a")
	if bay.Host != "dock1" || bay.Name != "a" || len(bay.Caps) != 1 {
		t.Errorf("bay %+v", bay)
	}
	waitFor(t, "a quiet bay to go offline", func() bool {
		bay, _ := h.Bay("dock1/a")
		return !bay.Online && bay.Snap.State == model.Offline
	})
}

func TestNewLinkReplacesOld(t *testing.T) {
	ca := testpki.New(t)
	h, addr := startHub(t, ca)
	first := dial(t, addr, ca, ca, "bay/dock1/a")
	first.hello("dock1", "a")
	waitFor(t, "the first link", func() bool { b, _ := h.Bay("dock1/a"); return b.Online })
	second := dial(t, addr, ca, ca, "bay/dock1/a")
	second.hello("dock1", "a")
	if !first.dropped() {
		t.Error("the older link stayed up")
	}
	second.send(proto.Frame{Type: proto.FrameStatus, Snapshot: &model.Snapshot{State: model.Present}})
	waitFor(t, "the second link's status", func() bool {
		b, _ := h.Bay("dock1/a")
		return b.Online && b.Snap.State == model.Present
	})
	if n := len(h.Bays()); n != 1 {
		t.Errorf("%d bays", n)
	}
}

func TestCallRefusals(t *testing.T) {
	ca := testpki.New(t)
	h, addr := startHub(t, ca)
	ctx := context.Background()
	if _, err := h.Call(ctx, "dock1/a", proto.Request{Verb: proto.Status}); err == nil {
		t.Error("a call to a bay never seen went through")
	}
	b := dial(t, addr, ca, ca, "bay/dock1/a")
	b.hello("dock1", "a")
	waitFor(t, "the link", func() bool { x, _ := h.Bay("dock1/a"); return x.Online })
	if _, err := h.Call(ctx, "dock1/a", proto.Request{Verb: "abort"}); err == nil {
		t.Error("the hub sent a verb that isn't in the contract")
	}
	// A bay that never answers: the link dropping ends the wait.
	go func() { b.sc.Scan(); b.c.Close() }()
	if _, err := h.Call(ctx, "dock1/a", proto.Request{Verb: proto.Status}); err == nil {
		t.Error("a call with no reply succeeded")
	}
}

func TestSubscribeCoalesces(t *testing.T) {
	ca := testpki.New(t)
	h, addr := startHub(t, ca)
	ticks, cancel := h.Subscribe()
	b := dial(t, addr, ca, ca, "bay/dock1/a")
	b.hello("dock1", "a")
	for _, s := range []model.State{model.Empty, model.Reading, model.Present} {
		b.send(proto.Frame{Type: proto.FrameStatus, Snapshot: &model.Snapshot{State: s}})
	}
	waitFor(t, "the last status", func() bool { x, _ := h.Bay("dock1/a"); return x.Snap.State == model.Present })
	time.Sleep(50 * time.Millisecond) // the last tick lands just after the snapshot
	<-ticks
	select {
	case <-ticks:
		t.Error("a slow reader got a backlog")
	default:
	}
	cancel()
	cancel()
	if _, ok := <-ticks; ok {
		t.Error("cancel left the channel open")
	}
}
