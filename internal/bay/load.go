package bay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/payload"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/wipe"
)

const (
	loadBuf = 8 << 20
	// fetchPatience is how long a load waits on a hub that stops answering.
	// A job needs nothing from the hub until now, and the keystream is down.
	fetchPatience = 30 * time.Minute
	// listTimeout is how long a start waits for the set's list.  The hub's
	// call to the bay gives up not long after.
	listTimeout = 4 * time.Second
)

// Filer makes and mounts the filesystem a load fills.  The real one is Sys;
// optional, so a Source that can't load simply refuses to.
type Filer interface {
	// LoadReady says what's missing on this host for a load to run.  It's
	// asked before the wipe, so a host without mkfs.exfat costs no drive.
	LoadReady() error
	Format(ctx context.Context, d Device, filesystem, label string) (part string, err error)
	Mount(ctx context.Context, part string, readOnly bool) (dir string, err error)
	Unmount(ctx context.Context, dir string) error
	// Free is the bytes a filesystem has left.
	Free(dir string) (int64, error)
}

// Fetcher is how the disk side reaches a payload: through the link, since it
// has no network of its own.
type Fetcher interface {
	List(ctx context.Context, set string) ([]byte, error)
	Open(ctx context.Context, set, path string, off int64) (io.ReadCloser, error)
}

// Load starts a Wipe + Load, if it's the drive and the insertion the confirm
// was drawn from, and the set is still the one it showed.
func (b *Bay) Load(serial string, session time.Time, set, list string) error {
	// Ask before taking the lock: this crosses the network.
	s, err := b.plan(set, list)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if !b.Loads {
		return errors.New("This bay isn't set up to load.  Its config would need \"load\" in caps.")
	}
	if err := b.mayWipe(serial, session); err != nil {
		return err
	}
	if !s.Fits(b.dev.Size) {
		return fmt.Errorf("Set %q is %s, too big for this %s drive.", s.Name, records.Size(s.Bytes()), records.Size(b.dev.Size))
	}
	return b.start(serial, "load", s)
}

// plan fetches the set and holds it to what the confirm showed.
func (b *Bay) plan(set, list string) (*payload.Set, error) {
	f, ok := b.Src.(Filer)
	if !ok || b.Fetcher == nil {
		return nil, errors.New("This bay can't load: it has no way to reach a payload.")
	}
	if err := f.LoadReady(); err != nil {
		return nil, fmt.Errorf("This host can't load yet, and nothing was touched: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	js, err := b.Fetcher.List(ctx, set)
	if err != nil {
		return nil, fmt.Errorf("Couldn't get set %q from the hub, and nothing was touched: %v", set, err)
	}
	sets, err := payload.Parse(bytes.NewReader(js))
	if err != nil || len(sets) != 1 || sets[0].Name != set {
		return nil, fmt.Errorf("The hub's list for set %q doesn't make sense, and nothing was touched: %v", set, err)
	}
	if h := sets[0].Hash(); h != list {
		return nil, errors.New("That set has changed since the confirm was drawn.  Look at it again and confirm again.")
	}
	return &sets[0], nil
}

// loading is a load in hand: what the phases share.
type loading struct {
	set    *payload.Set
	filer  Filer
	res    *wipe.Result
	key    []byte
	iv     []byte
	result *payload.Result
	part   string
	done   int64 // bytes since the last identity check
}

// runLoad is everything after the overwrite: format, load, stamp, ballast,
// and the full read.  A wipe has already proved itself by sampling.  Nil means
// it all came back right.
func (b *Bay) runLoad(ctx context.Context, j *job, res *wipe.Result) (*payload.Result, *ending) {
	f := b.Src.(Filer)
	l := &loading{set: j.set, filer: f, res: res}
	l.result = &payload.Result{Set: j.set.Name, Label: j.set.Label, Hash: j.set.Hash(), Files: len(j.set.Files), Bytes: j.set.Bytes()}
	var err error
	if l.key, err = hex.DecodeString(res.Key); err != nil {
		return l.result, failed("The wipe's key doesn't read: %v", err)
	}
	if l.iv, err = hex.DecodeString(res.IV); err != nil {
		return l.result, failed("The wipe's IV doesn't read: %v", err)
	}

	b.setPhase(j, "format")
	b.step(j, 0, 0, "")
	l.part, err = f.Format(ctx, j.dev, j.set.Filesystem, j.set.Label)
	if err != nil {
		return l.result, failed("Couldn't partition and format the drive: %v", err)
	}
	j.part = l.part

	dir, err := f.Mount(ctx, l.part, false)
	if err != nil {
		return l.result, failed("Couldn't mount the new filesystem: %v", err)
	}
	if e := b.fill(ctx, j, l, dir); e != nil {
		if err := f.Unmount(context.Background(), dir); err != nil {
			log.Printf("load: unmounting: %v", err)
		}
		return l.result, e
	}
	if err := f.Unmount(ctx, dir); err != nil {
		return l.result, failed("Couldn't unmount to read it back: %v", err)
	}

	// A fresh mount, read-only, so nothing below comes from a cache of what
	// went down.
	b.setPhase(j, "read")
	dir, err = f.Mount(ctx, l.part, true)
	if err != nil {
		return l.result, failed("Couldn't mount the filesystem again to read it back: %v", err)
	}
	e := b.readBack(ctx, j, l, dir)
	if err := f.Unmount(context.Background(), dir); err != nil {
		log.Printf("load: unmounting: %v", err)
	}
	if e != nil {
		return l.result, e
	}
	if !l.result.OK() {
		return l.result, failed("%s", loadDamage(l.result))
	}
	return l.result, nil
}

// fill is the writing: the files, the stamp, the ballast.
func (b *Bay) fill(ctx context.Context, j *job, l *loading, dir string) *ending {
	free, err := l.filer.Free(dir)
	if err != nil {
		return failed("Couldn't measure the new filesystem: %v", err)
	}
	if need := l.result.Bytes + payload.Reserve; free < need {
		return failed("The new filesystem has %s free, and set %q needs %s with its reserve.", records.Size(free), l.set.Name, records.Size(need))
	}

	b.setPhase(j, "load")
	var copied int64
	for i, file := range l.set.Files {
		if err := payload.CheckPath(file.Path); err != nil || payload.Reserved(file.Path) {
			return alarm("The hub's list named a file I won't write, #%d: %v", i+1, err)
		}
		if e := b.pull(ctx, j, l, dir, i, file, &copied); e != nil {
			return e
		}
	}

	b.setPhase(j, "stamp")
	if err := writeSync(filepath.Join(dir, payload.StampList), l.set.Text()); err != nil {
		return failed("Couldn't write the stamp: %v", err)
	}
	if err := writeSync(filepath.Join(dir, payload.StampText), stampText(j, l)); err != nil {
		return failed("Couldn't write the stamp: %v", err)
	}

	b.setPhase(j, "ballast")
	if free, err = l.filer.Free(dir); err != nil {
		return failed("Couldn't measure what's left for the ballast: %v", err)
	}
	if e := b.ballast(ctx, j, l, dir, free-payload.Reserve); e != nil {
		return e
	}
	return nil
}

// pull copies one file through the link, hashing on the way down.  A dropped
// connection picks up from what's already on the drive.
func (b *Bay) pull(ctx context.Context, j *job, l *loading, dir string, i int, file payload.File, copied *int64) *ending {
	p := filepath.Join(dir, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(p), 0o777); err != nil {
		return failed("Couldn't make a folder for file #%d: %v", i+1, err)
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o666)
	if err != nil {
		return failed("Couldn't create file #%d: %v", i+1, err)
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, loadBuf)
	var have int64
	last := time.Now()
	for have < file.Size {
		if ctx.Err() != nil {
			return ctxEnding(ctx)
		}
		r, err := b.Fetcher.Open(ctx, l.set.Name, file.Path, have)
		if err == nil {
			for have < file.Size {
				if b.coolOff(ctx, j) > 0 {
					last = time.Now() // the hub didn't make us wait
				}
				var n int
				n, err = r.Read(buf)
				if n > 0 {
					if have+int64(n) > file.Size {
						r.Close()
						return failed("The hub sent more of file #%d than the list says it holds.", i+1)
					}
					if _, werr := f.Write(buf[:n]); werr != nil {
						r.Close()
						return failed("Couldn't write file #%d, at byte %s: %v", i+1, records.Commas(have), werr)
					}
					h.Write(buf[:n])
					have += int64(n)
					*copied += int64(n)
					last = time.Now()
					b.step(j, *copied, l.result.Bytes, fmt.Sprintf("file %d of %d", i+1, len(l.set.Files)))
					if e := b.checkEvery(ctx, j, l, int64(n)); e != nil {
						r.Close()
						return e
					}
				}
				if err != nil {
					break
				}
			}
			r.Close()
		}
		if have >= file.Size {
			break
		}
		if ctx.Err() != nil {
			return ctxEnding(ctx)
		}
		if time.Since(last) > fetchPatience {
			return failed("The hub stopped sending file #%d, %s bytes in, and hasn't come back in %s: %v", i+1, records.Commas(have), fetchPatience, err)
		}
		log.Printf("load: file #%d at byte %d: %v; trying again", i+1, have, err)
		select {
		case <-ctx.Done():
			return ctxEnding(ctx)
		case <-time.After(b.retryWait()):
		}
	}
	if err := f.Sync(); err != nil {
		return failed("Couldn't flush file #%d: %v", i+1, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != file.SHA256 {
		return failed("File #%d didn't match its hash as it came from the hub: %s, and the list says %s.  Check that file on the hub.", i+1, got, file.SHA256)
	}
	return nil
}

func (b *Bay) retryWait() time.Duration {
	if b.RetryWait > 0 {
		return b.RetryWait
	}
	return 5 * time.Second
}

// ctxEnding is why the context ended: a cause raised by the job's own checks,
// or nothing, for bayd shutting down.
func ctxEnding(ctx context.Context) *ending {
	var e *ending
	if errors.As(context.Cause(ctx), &e) {
		return e
	}
	return &ending{model.Interrupted, "bayd is stopping"}
}

// checkEvery holds to the wipe's rule: name the drive at least every 100 GiB.
func (b *Bay) checkEvery(ctx context.Context, j *job, l *loading, n int64) *ending {
	l.done += n
	every := b.CheckEvery
	if every == 0 {
		every = wipe.CheckEvery
	}
	if l.done < every {
		return nil
	}
	l.done = 0
	if err := b.checkIdentity(ctx, j, -1); err != nil {
		return ctxEnding(ctx)
	}
	if ctx.Err() != nil {
		return ctxEnding(ctx)
	}
	return nil
}

// ballast fills what's left with keystream, up to want bytes.  Running out of
// room early isn't a failure: the filesystem's own sums were an estimate, and
// the record says how much went down.
func (b *Bay) ballast(ctx context.Context, j *job, l *loading, dir string, want int64) *ending {
	want -= want % loadBuf
	if want <= 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, payload.Ballast), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o666)
	if err != nil {
		return failed("Couldn't create the ballast: %v", err)
	}
	defer f.Close()
	buf := make([]byte, loadBuf)
	var off int64
	for off < want {
		if ctx.Err() != nil {
			return ctxEnding(ctx)
		}
		b.coolOff(ctx, j)
		m := min(int64(len(buf)), want-off)
		if err := wipe.Keystream(l.key, l.iv, off, buf[:m]); err != nil {
			return failed("Couldn't make the ballast's keystream: %v", err)
		}
		n, err := f.Write(buf[:m])
		off += int64(n)
		if errors.Is(err, syscall.ENOSPC) {
			// A partial buffer stays: the read-back checks what's there.
			break
		}
		if err != nil {
			return failed("Couldn't write the ballast at byte %s: %v", records.Commas(off), err)
		}
		b.step(j, off, want, "")
		if e := b.checkEvery(ctx, j, l, int64(n)); e != nil {
			return e
		}
	}
	if err := f.Sync(); err != nil {
		return failed("Couldn't flush the ballast: %v", err)
	}
	l.result.Ballast = off
	return nil
}

// readBack reads every file against its hash and the ballast against the
// keystream.  Damage doesn't stop it: the record should say all that's wrong.
func (b *Bay) readBack(ctx context.Context, j *job, l *loading, dir string) *ending {
	total := l.result.Bytes + l.result.Ballast
	var seen int64
	buf := make([]byte, loadBuf)
	for i, file := range l.set.Files {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(file.Path)))
		if err != nil {
			l.result.BadFiles = append(l.result.BadFiles, i+1)
			log.Printf("load: read-back of file #%d: %v", i+1, err)
			continue
		}
		h := sha256.New()
		var n int64
		for {
			if ctx.Err() != nil {
				f.Close()
				return ctxEnding(ctx)
			}
			b.coolOff(ctx, j)
			m, err := f.Read(buf)
			if m > 0 {
				h.Write(buf[:m])
				n += int64(m)
				seen += int64(m)
				b.step(j, seen, total, fmt.Sprintf("file %d of %d", i+1, len(l.set.Files)))
				if e := b.checkEvery(ctx, j, l, int64(m)); e != nil {
					f.Close()
					return e
				}
			}
			if err != nil {
				if err != io.EOF {
					log.Printf("load: read-back of file #%d at byte %d: %v", i+1, n, err)
					n = -1
				}
				break
			}
		}
		f.Close()
		if n != file.Size || hex.EncodeToString(h.Sum(nil)) != file.SHA256 {
			l.result.BadFiles = append(l.result.BadFiles, i+1)
			continue
		}
		l.result.VerifiedFiles += file.Size
	}
	if l.result.Ballast == 0 {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, payload.Ballast))
	if err != nil {
		l.result.BadBallast = l.result.Ballast
		log.Printf("load: read-back of the ballast: %v", err)
		return nil
	}
	defer f.Close()
	want := make([]byte, loadBuf)
	for off := int64(0); off < l.result.Ballast; {
		if ctx.Err() != nil {
			return ctxEnding(ctx)
		}
		b.coolOff(ctx, j)
		m := min(int64(loadBuf), l.result.Ballast-off)
		n, err := io.ReadFull(f, buf[:m])
		if err := wipe.Keystream(l.key, l.iv, off, want[:m]); err != nil {
			return failed("Couldn't make the ballast's keystream: %v", err)
		}
		if err != nil {
			log.Printf("load: read-back of the ballast at byte %d: %v", off+int64(n), err)
			l.result.BadBallast += l.result.Ballast - off
			break
		}
		if bytes.Equal(buf[:m], want[:m]) {
			l.result.VerifiedBallast += m
		} else {
			var bad int64
			for k := range buf[:m] {
				if buf[k] != want[k] {
					bad++
				}
			}
			l.result.BadBallast += bad
			l.result.VerifiedBallast += m - bad
		}
		off += m
		seen += m
		b.step(j, seen, total, "the ballast")
		if e := b.checkEvery(ctx, j, l, m); e != nil {
			return e
		}
	}
	return nil
}

// recordOnDrive puts the record in the reserve.  It's the last thing written,
// and the one file nothing has verified; the record says it's there.
func (b *Bay) recordOnDrive(ctx context.Context, j *job, rec *records.Record) error {
	f := b.Src.(Filer)
	if err := b.Src.SetRW(j.dev); err != nil {
		return err
	}
	defer func() {
		if err := b.Src.SetRO(j.dev); err != nil {
			log.Printf("load: setting read-only again: %v", err)
		}
	}()
	dir, err := f.Mount(ctx, j.part, false)
	if err != nil {
		return err
	}
	rec.Load.RecordOnDrive = true
	js, merr := json.MarshalIndent(rec, "", "  ")
	if merr == nil {
		merr = writeSync(filepath.Join(dir, payload.RecordFile), append(js, '\n'))
	}
	if err := f.Unmount(ctx, dir); err != nil {
		merr = errors.Join(merr, err)
	}
	if merr != nil {
		rec.Load.RecordOnDrive = false
	}
	return merr
}

// step moves the bar through the load's phases.  A job's bar is Done of
// 2×Total: the overwrite is the first half, and format-to-read is the second.
func (b *Bay) step(j *job, done, total int64, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	live := b.snap.Job
	if live == nil {
		return
	}
	size := j.dev.Size
	var from, width int64
	switch live.Phase {
	case "load":
		from, width = 0, size/5*2
	case "ballast":
		from, width = size/5*2, size/10
	case "stamp", "format":
		from, width = size/5*2, 0
	case "read":
		from, width = size/2, size/2
	default:
		return
	}
	at := size + from
	if total > 0 {
		at += int64(float64(width) * float64(done) / float64(total))
	}
	if at > live.Done {
		live.Done = at
		eta(j, live, size)
	}
	live.Detail = detail
}

func writeSync(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o666)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// stampText is the note a person finds on the drive.
func stampText(j *job, l *loading) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "Za Wiper loaded this drive.\n\n")
	fmt.Fprintf(&b, "Set:     %s\n", l.set.Name)
	if l.set.Description != "" {
		fmt.Fprintf(&b, "         %s\n", l.set.Description)
	}
	fmt.Fprintf(&b, "Files:   %d, %s\n", len(l.set.Files), records.Size(l.set.Bytes()))
	fmt.Fprintf(&b, "Serial:  %s\n", j.read.Drive.Serial())
	fmt.Fprintf(&b, "Date:    %s\n\n", time.Now().UTC().Format("2006-01-02"))
	fmt.Fprintf(&b, "Whatever was on this drive before was overwritten first.\n\n")
	fmt.Fprintf(&b, "%s lists every file with its SHA-256, so you can check them.\n", payload.StampList)
	fmt.Fprintf(&b, "%s is the record of what was done.\n", payload.RecordFile)
	fmt.Fprintf(&b, "%s is padding.  Delete it when you need the space.\n", payload.Ballast)
	return []byte(b.String())
}

// loadDamage is the evidence for a load that didn't read back.
func loadDamage(r *payload.Result) string {
	var parts []string
	if n := len(r.BadFiles); n > 0 {
		s := fmt.Sprintf("%d of %d files didn't read back to their hashes, first #%d in the stamp's list", n, r.Files, r.BadFiles[0])
		parts = append(parts, s)
	}
	if r.BadBallast > 0 {
		parts = append(parts, fmt.Sprintf("%s bytes of the ballast didn't read back as written", records.Commas(r.BadBallast)))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Only %s of %s bytes read back.", records.Commas(r.Verified()), records.Commas(r.Bytes+r.Ballast))
	}
	return strings.Join(parts, ", and ") + "."
}
