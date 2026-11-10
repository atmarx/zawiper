package link

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/hub"
	"github.com/atmarx/zawiper/internal/link/testpki"
	"github.com/atmarx/zawiper/internal/payload"
	"github.com/atmarx/zawiper/internal/records"
)

const helloSum = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"

// The whole path but the disk: hub over mutual TLS, the link's socket, the
// disk side's client.
func TestPayloadCrossesTheWall(t *testing.T) {
	ca := testpki.New(t)
	hubCert, hubKey := ca.Server(t, "localhost")
	h, err := hub.New(hub.Config{CAFile: ca.CAFile, CertFile: hubCert, KeyFile: hubKey, Store: &records.Store{Root: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret"), []byte("no"), 0o644)
	os.WriteFile(filepath.Join(dir, payload.FileName), []byte("set a\n  label A\n  file "+helloSum+" 5 hello\n"), 0o644)

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.ServePayloads(ctx, addr, (&payload.Library{Dir: dir}).Handler())
	_, port, _ := net.SplitHostPort(addr)
	hubAddr := net.JoinHostPort("localhost", port)
	for i := 0; i < 200; i++ {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cert, key := ca.Client(t, "bay", "bay/host/a")
	tc, _, err := ClientTLS(ca.CAFile, cert, key)
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "payload.sock")
	p := &PayloadProxy{Hub: hubAddr, TLS: tc, Socket: sock}
	go p.Serve(ctx)
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	c := payload.NewClient(sock)
	list, err := c.List(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	sets, err := payload.ParseBytes(list)
	if err != nil || sets[0].Files[0].Path != "hello" {
		t.Fatalf("list %q: %v", list, err)
	}
	r, err := c.Open(ctx, "a", "hello", 0)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "hello" {
		t.Errorf("got %q", b)
	}
	r, err = c.Open(ctx, "a", "hello", 2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(r)
	r.Close()
	if string(b) != "llo" {
		t.Errorf("resumed: %q", b)
	}
	if _, err := c.Open(ctx, "a", "secret", 0); err == nil {
		t.Error("a file no set lists came through")
	}

	// The socket is read-only, however it's asked.
	un := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}}
	res, err := un.Post("http://link/sets/a", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST: %s", res.Status)
	}

	// No bay certificate, no payload: one from another CA, then none.
	pool := x509.NewCertPool()
	pem, _ := os.ReadFile(ca.CAFile)
	pool.AppendCertsFromPEM(pem)
	stranger := testpki.New(t)
	sc, sk := stranger.Client(t, "bay", "bay/host/a")
	kp, _ := tls.LoadX509KeyPair(sc, sk)
	for name, certs := range map[string][]tls.Certificate{"another CA": {kp}, "no certificate": nil} {
		cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: certs}}}
		res, err := cl.Get(fmt.Sprintf("https://%s/sets/a", hubAddr))
		if err == nil {
			res.Body.Close()
			t.Errorf("%s got through: %s", name, res.Status)
		}
	}
	// A certificate from the right CA that isn't a bay's.
	oc, ok := ca.Client(t, "other", "someone/else")
	okp, _ := tls.LoadX509KeyPair(oc, ok)
	cl := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{okp}}}}
	if res, err := cl.Get(fmt.Sprintf("https://%s/sets/a", hubAddr)); err == nil {
		res.Body.Close()
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("a non-bay certificate: %s", res.Status)
		}
	}
}
