// Package proto is the unix-socket conversation between a bay's link and its
// disk side.  One JSON request, one JSON reply, then the connection closes.
// The verbs are fixed and none of them takes a device, a path, or a command.
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
	// Wipe carries the drive and insertion the confirm was drawn from.  The
	// disk side refuses unless that's what it holds right now.
	Wipe Verb = "wipe"
	// Ack clears DONE, FAILED, INTERRUPTED or ALARM back to the drive.
	Ack Verb = "ack"
	// Eject syncs, spins the drive down, and lets it go.
	Eject Verb = "eject"
)

// There is no abort verb, on purpose (spec §4.3).

type Request struct {
	Verb    Verb      `json:"verb"`
	Serial  string    `json:"serial,omitempty"`
	Session time.Time `json:"session,omitzero"`
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
	Wipe(serial string, session time.Time) error
	Ack() error
	Eject() error
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
		case Wipe, Ack, Eject:
			var err error
			switch req.Verb {
			case Wipe:
				err = h.Wipe(req.Serial, req.Session)
			case Ack:
				err = h.Ack()
			case Eject:
				err = h.Eject()
			}
			s := h.Status()
			rep.Snapshot = &s
			if err != nil {
				rep.Error = err.Error()
			} else {
				rep.OK = true
			}
		default:
			rep.Error = fmt.Sprintf("unknown verb %q", req.Verb)
		}
	}
	json.NewEncoder(c).Encode(rep)
}

// Client talks to bayd.
type Client struct{ Socket string }

func (c *Client) Call(ctx context.Context, v Verb) (*model.Snapshot, error) {
	return c.Do(ctx, Request{Verb: v})
}

// Do sends a full request, for the verbs that carry more than their name.
func (c *Client) Do(ctx context.Context, req Request) (*model.Snapshot, error) {
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
	if err := json.NewEncoder(conn).Encode(req); err != nil {
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
