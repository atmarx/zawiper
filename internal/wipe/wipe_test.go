package wipe

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	moved := map[string][]int64{}
	j := &Job{Path: p, Size: size, Block: blk, Buf: bf, OnCell: func(string, int, int, Cell) { calls++ },
		OnBytes: func(pass string, done int64) { moved[pass] = append(moved[pass], done) }}
	r, err := j.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || len(r.Cells) != 3 || calls != 6 {
		t.Fatalf("ok=%v cells=%d calls=%d %+v", r.OK(), len(r.Cells), calls, r)
	}
	// Every buffer reports, so the wall moves inside a block.
	for _, pass := range []string{"write", "verify"} {
		m := moved[pass]
		if len(m) != int((size+bf-1)/bf) || m[0] != bf || m[len(m)-1] != size || !slices.IsSorted(m) {
			t.Errorf("%s pass reported %v", pass, m)
		}
	}
	b, _ := os.ReadFile(p)
	key, _ := hex.DecodeString(r.Key)
	iv, _ := hex.DecodeString(r.IV)
	want := make([]byte, 4096)
	if err := Keystream(key, iv, blk+bf, want); err != nil || !bytes.Equal(want, b[blk+bf:blk+bf+4096]) {
		t.Errorf("the published key doesn't regenerate what's on the drive: %v", err)
	}
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

// The identity check runs before each pass and on schedule, and an error
// stops the run before the offset it was asked about.
func TestCheck(t *testing.T) {
	var offs []int64
	j := &Job{Path: drive(t), Size: size, Block: blk, Buf: bf, CheckEvery: blk,
		Check: func(off int64) error { offs = append(offs, off); return nil }}
	if r, err := j.Run(context.Background()); err != nil || !r.OK() {
		t.Fatalf("%v %+v", err, r)
	}
	want := []int64{0, blk, 2 * blk, 0, blk, 2 * blk}
	if len(offs) != len(want) {
		t.Fatalf("checks at %v, want %v", offs, want)
	}
	for i := range want {
		if offs[i] != want[i] {
			t.Fatalf("checks at %v, want %v", offs, want)
		}
	}

	j.Check = func(off int64) error {
		if off >= blk {
			return errors.New("the drive says it's someone else")
		}
		return nil
	}
	r, _ := j.Run(context.Background())
	if r.OK() || r.Alarm != "the drive says it's someone else" || r.Written != blk || r.Aborted == "" {
		t.Errorf("alarm %q written %d aborted %q", r.Alarm, r.Written, r.Aborted)
	}
}

// OnStart fires before each block, so the live map can show W and R.
func TestOnStart(t *testing.T) {
	var seen []string
	j := &Job{Path: drive(t), Size: size, Block: blk, Buf: bf,
		OnStart: func(pass string, i, n int) { seen = append(seen, fmt.Sprintf("start %s %d", pass, i)) },
		OnCell:  func(pass string, i, n int, c Cell) { seen = append(seen, fmt.Sprintf("end %s %d", pass, i)) }}
	j.Run(context.Background())
	got := strings.Join(seen, ", ")
	if !strings.HasPrefix(got, "start write 0, end write 0, start write 1") || !strings.HasSuffix(got, "start verify 2, end verify 2") {
		t.Errorf("order: %s", got)
	}
}

// A buffer bigger than a block, or one that doesn't divide it, must not
// spill writes into the next block's cell.
func TestBufferLargerThanBlock(t *testing.T) {
	p := drive(t)
	r, err := (&Job{Path: p, Size: size, Block: blk, Buf: blk * 3}).Run(context.Background())
	if err != nil || !r.OK() || len(r.Cells) != 3 {
		t.Fatalf("%v %+v", err, r)
	}
	r, err = (&Job{Path: p, Size: size, Block: blk, Buf: 3 * bf}).Run(context.Background())
	if err != nil || !r.OK() {
		t.Fatalf("%v %+v", err, r)
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
