package link

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// PayloadProxy hands the disk side the payload bytes it asks for (spec §6.3).
// The disk side has no network and this has no devices, so this socket is the
// only way across, and it carries nothing but GETs for a set's files.  The
// disk side checks every byte against the list's hash; nothing here is trusted.
type PayloadProxy struct {
	Hub    string // host:port of the hub's payload port
	TLS    *tls.Config
	Socket string
}

// Serve listens on the socket until ctx ends.
func (p *PayloadProxy) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(p.Socket), 0o750); err != nil {
		return err
	}
	_ = os.Remove(p.Socket)
	ln, err := net.Listen("unix", p.Socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(p.Socket, 0o660); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{Handler: p.handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	err = srv.Serve(ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (p *PayloadProxy) handler() http.Handler {
	tc := p.TLS.Clone()
	tc.NextProtos = []string{"http/1.1"}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(&url.URL{Scheme: "https", Host: p.Hub})
		},
		Transport: &http.Transport{
			TLSClientConfig:       tc,
			ResponseHeaderTimeout: time.Minute,
			DisableCompression:    true,
			ForceAttemptHTTP2:     false,
		},
		FlushInterval: -1,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "payloads are read-only", http.StatusMethodNotAllowed)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
