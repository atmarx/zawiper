package wipe

import (
	"bytes"
	"context"
	"crypto/aes"
	"os"
	"path/filepath"
	"testing"
)

// A drive that isn't a whole number of blocks or buffers, so the short
// tails get exercised.
const (
	blk  = 64 << 10
	bf   = 16 << 10
	size = 2*blk + 3*bf + 4096
)

func drive(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "drive")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func TestWipeAndVerify(t *testing.T) {
	p := drive(t)
	var calls int
	j := &Job{Path: p, Size: size, Block: blk, Buf: bf, OnCell: func(string, int, int, Cell) { calls++ }}
	r, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || len(r.Cells) != 3 || calls != 6 {
		t.Fatalf("ok=%v cells=%d calls=%d %+v", r.OK(), len(r.Cells), calls, r)
	}
	b, _ := os.ReadFile(p)
	if bytes.Count(b[:blk], []byte{0}) > blk/64 {
		t.Error("the start of the drive doesn't look like noise")
	}
	if bytes.Equal(b[:4096], b[blk:blk+4096]) {
		t.Error("the keystream repeats across blocks")
	}
}

// The seekable keystream must match one long CTR stream.
func TestKeystreamSeeks(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	iv := bytes.Repeat([]byte{0xff}, 16) // the counter wraps on the first block
	c, _ := aes.NewCipher(key)
	long := make([]byte, 1<<16)
	keystream(c, iv, 0, long)
	part := make([]byte, 4096)
	keystream(c, iv, 8192, part)
	if !bytes.Equal(part, long[8192:8192+4096]) {
		t.Error("keystream at an offset differs from the long stream")
	}
}

// A drive that loses a write must fail verification, and say where.
func TestVerifyCatchesDamage(t *testing.T) {
	p := drive(t)
	j := &Job{Path: p, Size: size, Block: blk, Buf: bf}
	j.OnCell = func(pass string, i, n int, c Cell) {
		if pass == "write" && i == n-1 {
			f, _ := os.OpenFile(p, os.O_WRONLY, 0)
			f.WriteAt(make([]byte, 100), blk+12345)
			f.Close()
		}
	}
	r, _ := j.Run(context.Background())
	if r.OK() || r.Cells[1].Bad == 0 || r.Cells[0].Bad != 0 || r.Bad > 100 {
		t.Errorf("ok=%v bad=%d cells=%+v", r.OK(), r.Bad, r.Cells)
	}
}

func TestStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{Path: drive(t), Size: size, Block: blk, Buf: bf, OnCell: func(string, int, int, Cell) { cancel() }}
	r, _ := j.Run(ctx)
	if r.OK() || r.Aborted == "" {
		t.Errorf("%+v", r)
	}
}

func TestSlow(t *testing.T) {
	r := &Result{}
	for i := 0; i < 40; i++ {
		r.Cells = append(r.Cells, Cell{Write: 7 + float64(i)/10})
	}
	r.Cells[20].Write = 30
	r.Cells[39].Write = 1 // the short last block never counts
	if s := r.Slow(false); len(s) != 1 || s[0] != 20 {
		t.Errorf("slow %v", s)
	}
}
