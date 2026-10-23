package proto

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"zawiper/internal/model"
)

type h struct{ rereads int }

func (x *h) Status() model.Snapshot { return model.Snapshot{State: model.Empty, Bay: "test"} }
func (x *h) Reread()                { x.rereads++ }

func TestSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "bayd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x := &h{}
	go Serve(ctx, sock, -1, x)
	c := &Client{Socket: sock}
	var s *model.Snapshot
	var err error
	for i := 0; i < 50; i++ { // wait for the listener
		if s, err = c.Call(ctx, Status); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || s.State != model.Empty {
		t.Fatalf("status: %v %v", s, err)
	}
	if _, err := c.Call(ctx, Reread); err != nil || x.rereads != 1 {
		t.Errorf("reread: %v, %d", err, x.rereads)
	}
	if _, err := c.Call(ctx, "wipe"); err == nil {
		t.Error("unknown verb accepted")
	}

	// A request that tries to carry more than a verb is refused outright.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	json.NewEncoder(conn).Encode(map[string]string{"verb": "reread", "device": "/dev/sda"})
	var rep Reply
	json.NewDecoder(conn).Decode(&rep)
	if rep.OK || x.rereads != 1 {
		t.Errorf("extra field: %+v, rereads %d", rep, x.rereads)
	}
}
