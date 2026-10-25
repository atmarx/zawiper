// Package wipe overwrites a drive with a keystream, then reads every byte
// back and checks it.  The keystream is AES-256-CTR under a fresh random key,
// and the key goes in the record on purpose: anyone holding the record can
// regenerate the stream and check the drive themselves (spec §7).  The old
// data is gone either way; the key only proves it.
//
// Each block is timed on the way down and on the way back.  Those timings
// are the surface map: a block well slower than its neighbours is a weak
// spot the drive is retrying through.
package wipe

import (
	"bytes"
	"cmp"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"
)

const (
	// Block is one cell of the surface map.
	Block = 1 << 30
	// buf is one write or read.  It divides Block.
	buf = 8 << 20
	// CheckEvery is the most a wipe goes between identity checks (spec
	// §4.1): before each pass, and at least every 100 GiB.
	CheckEvery = 100 << 30
)

// Method is how a wipe overwrites, as the record states it.
const Method = "AES-256-CTR keystream, one pass, read back and compared"

type Cell struct {
	Write float64 `json:"write_s"`
	Read  float64 `json:"read_s"`
	// Bad counts bytes that read back wrong or couldn't be read at all.
	Bad int64  `json:"bad,omitempty"`
	Err string `json:"err,omitempty"`
}

type Result struct {
	Method   string    `json:"method"`
	Size     int64     `json:"size"`
	Block    int64     `json:"block"`
	Cells    []Cell    `json:"cells"`
	Started  time.Time `json:"started"`
	Wrote    time.Time `json:"wrote"`    // the write pass finished
	Verified time.Time `json:"verified"` // the read pass finished
	// Written and Checked are bytes that went down and came back right.
	Written int64  `json:"written"`
	Checked int64  `json:"checked"`
	Bad     int64  `json:"bad"`
	Aborted string `json:"aborted,omitempty"`
	// Alarm is what the identity check said when it stopped the run.
	Alarm string `json:"alarm,omitempty"`
	// Key and IV, hex, published so the wipe can be re-checked (Keystream).
	Key string `json:"key"`
	IV  string `json:"iv"`
}

// OK is a finished wipe where every byte read back as written.
func (r *Result) OK() bool {
	return r.Aborted == "" && r.Alarm == "" && r.Bad == 0 && r.Written == r.Size && r.Checked == r.Size
}

// MBps is the average for a pass, in decimal megabytes a second.
func (r *Result) MBps(read bool) float64 {
	var s float64
	for _, c := range r.Cells {
		if read {
			s += c.Read
		} else {
			s += c.Write
		}
	}
	if s == 0 {
		return 0
	}
	n := r.Written
	if read {
		n = r.Checked + r.Bad
	}
	return float64(n) / s / 1e6
}

// Slow lists blocks that took more than twice as long as the median of
// their neighbours.  A spinning drive slows toward the inner tracks, so
// only the neighbours are a fair comparison.
func (r *Result) Slow(read bool) []int {
	t := func(i int) float64 {
		if read {
			return r.Cells[i].Read
		}
		return r.Cells[i].Write
	}
	var out []int
	for i := range r.Cells {
		var near []float64
		for j := i - 8; j <= i+8; j++ {
			if j != i && j >= 0 && j < len(r.Cells)-1 && t(j) > 0 { // the last block is short
				near = append(near, t(j))
			}
		}
		if len(near) < 4 || i == len(r.Cells)-1 {
			continue
		}
		if t(i) > 2*median(near) {
			out = append(out, i)
		}
	}
	return out
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// Progress is called after each block of each pass.
type Progress func(pass string, i, n int, c Cell)

type Job struct {
	Path string
	Size int64
	// Direct bypasses the page cache, so a read is the platter and not
	// memory.  Off only in tests, where the "drive" is a file on tmpfs.
	Direct bool
	OnCell Progress
	// OnStart is called as each block of each pass begins, so a live map
	// can show the block being written or read right now.
	OnStart func(pass string, i, n int)
	// OnBytes is called after each buffer with how far the pass has got,
	// so the wall moves between blocks.  It runs on the engine's
	// goroutine: keep it quick.
	OnBytes func(pass string, done int64)
	// Check confirms the drive is still the drive.  It's called before
	// each pass and at least every CheckEvery bytes, with the offset the
	// run is about to touch.  An error stops the run and lands in
	// Result.Alarm.
	Check func(off int64) error
	// Block, Buf and CheckEvery default to the package's; tests shrink them.
	Block, Buf, CheckEvery int64
}

// Run writes the keystream over the whole device, then verifies it.  A
// cancelled context stops it between buffers; the result says how far it
// got.
func (j *Job) Run(ctx context.Context) (*Result, error) {
	bs, every := cmp.Or(j.Block, Block), cmp.Or(j.CheckEvery, CheckEvery)
	bf := min(cmp.Or(j.Buf, buf), bs)
	if j.Size <= 0 || j.Size%512 != 0 {
		return nil, fmt.Errorf("size %d isn't a whole number of 512-byte sectors", j.Size)
	}
	key := make([]byte, 32+16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key[:32])
	if err != nil {
		return nil, err
	}
	iv := key[32:]

	flags := os.O_RDWR | syscall.O_EXCL // exclusive: refuses if anything has it open
	if j.Direct {
		flags |= syscall.O_DIRECT
	}
	f, err := os.OpenFile(j.Path, flags, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := aligned(int(bf))
	if err != nil {
		return nil, err
	}
	defer syscall.Munmap(data)
	want := make([]byte, bf)

	n := int((j.Size + bs - 1) / bs)
	r := &Result{Method: Method, Size: j.Size, Block: bs,
		Cells: make([]Cell, n), Started: time.Now().UTC(),
		Key: hex.EncodeToString(key[:32]), IV: hex.EncodeToString(iv)}

	// Write.
	next := int64(0) // the offset of the next identity check
	for i := 0; i < n; i++ {
		j.start("write", i, n)
		t0 := time.Now()
		c := &r.Cells[i]
		end := min(int64(i+1)*bs, j.Size)
		for off := int64(i) * bs; off < end; off += bf {
			if j.halt(ctx, r, "write", off, &next, every) {
				return r, nil
			}
			m := min(bf, end-off)
			keystream(block, iv, off, data[:m])
			if _, err := f.WriteAt(data[:m], off); err != nil {
				c.Err = firstErr(c.Err, err) // the verify pass counts the damage
				continue
			}
			r.Written += m
			j.moved("write", off+m)
		}
		if err := f.Sync(); err != nil { // the drive's cache is not the platter
			c.Err = firstErr(c.Err, err)
		}
		c.Write = time.Since(t0).Seconds()
		if j.OnCell != nil {
			j.OnCell("write", i, n, *c)
		}
	}
	r.Wrote = time.Now().UTC()

	// Verify.
	next = 0
	for i := 0; i < n; i++ {
		j.start("verify", i, n)
		t0 := time.Now()
		c := &r.Cells[i]
		end := min(int64(i+1)*bs, j.Size)
		for off := int64(i) * bs; off < end; off += bf {
			if j.halt(ctx, r, "verify", off, &next, every) {
				return r, nil
			}
			m := min(bf, end-off)
			keystream(block, iv, off, want[:m])
			if _, err := f.ReadAt(data[:m], off); err != nil {
				c.Bad += m
				r.Bad += m
				c.Err = firstErr(c.Err, err)
				continue
			}
			j.moved("verify", off+m)
			if bytes.Equal(data[:m], want[:m]) {
				r.Checked += m
				continue
			}
			var bad int64
			for k := range data[:m] {
				if data[k] != want[k] {
					bad++
				}
			}
			c.Bad += bad
			r.Bad += bad
			r.Checked += m - bad
		}
		c.Read = time.Since(t0).Seconds()
		if j.OnCell != nil {
			j.OnCell("verify", i, n, *c)
		}
	}
	r.Verified = time.Now().UTC()
	return r, nil
}

func (j *Job) start(pass string, i, n int) {
	if j.OnStart != nil {
		j.OnStart(pass, i, n)
	}
}

func (j *Job) moved(pass string, done int64) {
	if j.OnBytes != nil {
		j.OnBytes(pass, done)
	}
}

// halt says whether the run stops before touching off: the identity check
// failed, or the context was cancelled.  It runs the check when one is due,
// and schedules the next.
func (j *Job) halt(ctx context.Context, r *Result, pass string, off int64, next *int64, every int64) bool {
	if j.Check != nil && off >= *next {
		*next = off + every
		if err := j.Check(off); err != nil {
			r.Alarm = err.Error()
			r.Aborted = "stopped during the " + pass + " pass"
			return true
		}
	}
	if ctx.Err() != nil {
		r.Aborted = "stopped during the " + pass + " pass"
		return true
	}
	return false
}

// Keystream fills b with the stream a wipe with this key and IV wrote at
// byte off.  It's how a verifier regenerates what should be on the drive.
func Keystream(key, iv []byte, off int64, b []byte) error {
	if len(key) != 32 || len(iv) != 16 || off%16 != 0 {
		return fmt.Errorf("want a 32-byte key, a 16-byte IV and an offset on a 16-byte boundary")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	keystream(block, iv, off, b)
	return nil
}

// keystream fills b with the stream as it stands at byte off.  CTR can
// start anywhere: the counter is the IV plus the 16-byte block number.
func keystream(block cipher.Block, iv []byte, off int64, b []byte) {
	ctr := make([]byte, 16)
	hi, lo := binary.BigEndian.Uint64(iv[:8]), binary.BigEndian.Uint64(iv[8:])
	add := uint64(off / 16)
	lo2 := lo + add
	if lo2 < lo {
		hi++
	}
	binary.BigEndian.PutUint64(ctr[:8], hi)
	binary.BigEndian.PutUint64(ctr[8:], lo2)
	clear(b)
	cipher.NewCTR(block, ctr).XORKeyStream(b, b)
}

// aligned returns page-aligned memory, which O_DIRECT needs.
func aligned(n int) ([]byte, error) {
	return syscall.Mmap(-1, 0, n, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
}

func firstErr(have string, err error) string {
	if have != "" {
		return have
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
