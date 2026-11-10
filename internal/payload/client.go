package payload

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is how the disk side reaches a payload: HTTP over the link's unix
// socket, since it has no network of its own.
type Client struct {
	http *http.Client
}

// NewClient talks to a link's payload socket.
func NewClient(socket string) *Client {
	return &Client{http: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		ResponseHeaderTimeout: time.Minute,
		DisableCompression:    true,
	}}}
}

// List fetches a set's list, as Set.Text writes it.
func (c *Client) List(ctx context.Context, set string) ([]byte, error) {
	r, err := c.get(ctx, "/sets/"+url.PathEscape(set), 0)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 64<<20))
}

// Open fetches a file from a byte onward.  A server that ignores the range
// would corrupt a resume, so it's an error.
func (c *Client) Open(ctx context.Context, set, path string, off int64) (io.ReadCloser, error) {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.get(ctx, "/sets/"+url.PathEscape(set)+"/files/"+strings.Join(segs, "/"), off)
}

func (c *Client) get(ctx context.Context, p string, off int64) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://link"+p, nil)
	if err != nil {
		return nil, err
	}
	want := http.StatusOK
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
		want = http.StatusPartialContent
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != want {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		res.Body.Close()
		return nil, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return res.Body, nil
}
