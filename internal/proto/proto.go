// Package proto is the unix-socket conversation between zaweb and bayd.
// One JSON request, one JSON reply, then the connection closes.  The verbs
// are fixed and none of them takes a device, a path, or a command.
package proto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"zawiper/internal/model"
)

type Verb string

const (
	Status Verb = "status"
	Reread Verb = "reread"
)

type Request struct {
	Verb Verb `json:"verb"`
}

type Reply struct {
	OK       bool            `json:"ok"`
	Error    string          `json:"error,omitempty"`
	Snapshot *model.Snapshot `json:"snapshot,omitempty"`
}

// Handler answers a verb.  Unknown verbs never reach it.
type Handler interface {
	Status() model.Snapshot
	Reread()
}

// Serve listens on a unix socket.  The socket is chmod 0660 and chowned to
// gid, the group zaweb's container runs as.
func Serve(ctx context.Context, path string, gid int, h Handler) error {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	if err := os.Chmod(path, 0o660); err != nil {
		return err
	}
	if gid >= 0 {
		if err := os.Chown(path, -1, gid); err != nil {
			return err
		}
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("accept: %v", err)
			continue
		}
		go handle(c, h)
	}
}

func handle(c net.Conn, h Handler) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var req Request
	dec := json.NewDecoder(c)
	dec.DisallowUnknownFields() // a request that tries to say more than a verb is refused
	var rep Reply
	if err := dec.Decode(&req); err != nil {
		rep.Error = "bad request: " + err.Error()
	} else {
		switch req.Verb {
		case Status:
			s := h.Status()
			rep.OK, rep.Snapshot = true, &s
		case Reread:
			h.Reread()
			s := h.Status()
			rep.OK, rep.Snapshot = true, &s
		default:
			rep.Error = fmt.Sprintf("unknown verb %q", req.Verb)
		}
	}
	json.NewEncoder(c).Encode(rep)
}

// Client talks to bayd.
type Client struct{ Socket string }

func (c *Client) Call(ctx context.Context, v Verb) (*model.Snapshot, error) {
	var d net.Dialer
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, fmt.Errorf("bayd isn't answering: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(Request{Verb: v}); err != nil {
		return nil, err
	}
	var rep Reply
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		return nil, err
	}
	if !rep.OK {
		return nil, errors.New(rep.Error)
	}
	return rep.Snapshot, nil
}
