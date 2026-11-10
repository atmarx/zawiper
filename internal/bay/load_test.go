package bay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/payload"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/wipe"
)

// loadRig is a dev bay on a file, with a real payload folder behind an HTTP
// server standing in for the link.  flaky cuts every response short once.
type loadRig struct {
	b     *Bay
	file  string
	lib   *payload.Library
	srv   *httptest.Server
	spool string
	flaky atomic.Bool
	reqs  atomic.Int64
}

type httpFetcher struct {
	base string
}

func (h httpFetcher) List(ctx context.Context, set string) ([]byte, error) {
	res, err := http.Get(h.base + "/sets/" + set)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s", res.Status)
	}
	return io.ReadAll(res.Body)
}

func (h httpFetcher) Open(ctx context.Context, set, path string, off int64) (io.ReadCloser, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", h.base+"/sets/"+set+"/files/"+path, nil)
	if off > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", off))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("%s", res.Status)
	}
	return res.Body, nil
}

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newLoadRig(t *testing.T, size int64) *loadRig {
	t.Helper()
	return newLoadRigOn(t, size, nil)
}

// newLoadRigOn lets wrap stand in front of the source, before the bay starts
// reading it.
func newLoadRigOn(t *testing.T, size int64, wrap func(*Dev) Source) *loadRig {
	t.Helper()
	dir := t.TempDir()
	lib := filepath.Join(dir, "payloads")
	os.MkdirAll(filepath.Join(lib, "sub"), 0o755)
	a := make([]byte, 3<<20)
	for i := range a {
		a[i] = byte(i * 7)
	}
	os.WriteFile(filepath.Join(lib, "a.bin"), a, 0o644)
	os.WriteFile(filepath.Join(lib, "sub", "b file.txt"), []byte("hello"), 0o644)
	txt := fmt.Sprintf("set test\n  description A test\n  label TEST\n  file %s  %d  a.bin\n  file %s  5  sub/b file.txt\n", sumOf(a), len(a), sumOf([]byte("hello")))
	os.WriteFile(filepath.Join(lib, payload.FileName), []byte(txt), 0o644)

	r := &loadRig{lib: &payload.Library{Dir: lib}, file: filepath.Join(dir, "bay.img"), spool: filepath.Join(dir, "spool")}
	h := r.lib.Handler()
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.reqs.Add(1)
		if r.flaky.CompareAndSwap(true, false) && strings.Contains(q.URL.Path, "a.bin") {
			w.Header().Set("Content-Length", fmt.Sprint(len(a)))
			w.Write(a[:1<<20])
			panic(http.ErrAbortHandler)
		}
		h.ServeHTTP(w, q)
	}))
	t.Cleanup(r.srv.Close)

	if err := os.WriteFile(r.file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(r.file, size); err != nil {
		t.Fatal(err)
	}
	dev := &Dev{File: r.file, Smart: "../smart/testdata/st31000528as.json"}
	var src Source = dev
	if wrap != nil {
		src = wrap(dev)
	}
	r.b = &Bay{Name: "t", Src: src, Interval: 5 * time.Millisecond, StatePath: filepath.Join(dir, "job.json"),
		Spool: &Spool{Root: r.spool}, Host: "h", Software: "test", Block: 1 << 20, Buf: 1 << 20,
		Loads: true, Fetcher: httpFetcher{r.srv.URL}, RetryWait: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); r.b.Wait() })
	go r.b.Run(ctx)
	r.present(t)
	return r
}

func (r *loadRig) present(t *testing.T) model.Snapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s := r.b.Status(); s.State == model.Present {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	s := r.b.Status()
	t.Fatalf("the bay never showed the drive: %s %s %v", s.State, s.Message, s.Refusals)
	return s
}

func (r *loadRig) settle(t *testing.T) model.Snapshot {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if s := r.b.Status(); s.State != model.Running {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the job never ended")
	return model.Snapshot{}
}

func (r *loadRig) hash(t *testing.T) string {
	t.Helper()
	s, err := r.lib.Set("test")
	if err != nil {
		t.Fatal(err)
	}
	return s.Hash()
}

func (r *loadRig) record(t *testing.T, serial, id string) *records.Record {
	t.Helper()
	js, err := os.ReadFile(filepath.Join(r.spool, "drives", records.DirName(serial), id, "record.json"))
	if err != nil {
		matches, _ := filepath.Glob(filepath.Join(r.spool, "*", "*", "*"))
		t.Fatalf("%v (spool holds %v)", err, matches)
	}
	var rec records.Record
	if err := json.Unmarshal(js, &rec); err != nil {
		t.Fatal(err)
	}
	return &rec
}

const loadSize = 64 << 20 // too small to sample: it gets the full read instead

func TestLoadEndToEnd(t *testing.T) {
	r := newLoadRig(t, loadSize)
	s := r.b.Status()
	if err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t)); err != nil {
		t.Fatal(err)
	}
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	if end.Job.Kind != "load" {
		t.Errorf("kind %q", end.Job.Kind)
	}
	fsdir := r.file + ".fs"
	a, err := os.ReadFile(filepath.Join(fsdir, "a.bin"))
	if err != nil || len(a) != 3<<20 {
		t.Fatalf("a.bin: %d bytes, %v", len(a), err)
	}
	if b, _ := os.ReadFile(filepath.Join(fsdir, "sub", "b file.txt")); string(b) != "hello" {
		t.Errorf("b file: %q", b)
	}
	for _, n := range []string{payload.StampText, payload.StampList, payload.RecordFile, payload.Ballast} {
		if _, err := os.Stat(filepath.Join(fsdir, n)); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	// The drive's stamp is the list the confirm named.
	list, _ := os.ReadFile(filepath.Join(fsdir, payload.StampList))
	sets, err := payload.Parse(strings.NewReader(string(list)))
	if err != nil || sets[0].Hash() != r.hash(t) {
		t.Errorf("stamp: %v", err)
	}

	rec := r.record(t, s.Drive.Serial(), end.Job.RecordID)
	if rec.Load == nil || !rec.Load.OK() || !rec.Load.RecordOnDrive {
		t.Fatalf("load result: %+v", rec.Load)
	}
	if rec.Load.Ballast == 0 || rec.Load.VerifiedBallast != rec.Load.Ballast {
		t.Errorf("ballast: %+v", rec.Load)
	}
	// Filenames stay off the record.
	js, _ := json.Marshal(rec)
	if strings.Contains(string(js), "a.bin") {
		t.Error("the record names a file")
	}
	// The ballast is the wipe's keystream, so the published key checks it.
	key, _ := hex.DecodeString(rec.Wipe.Key)
	iv, _ := hex.DecodeString(rec.Wipe.IV)
	want := make([]byte, 4096)
	wipe.Keystream(key, iv, 0, want)
	got := make([]byte, 4096)
	f, _ := os.Open(filepath.Join(fsdir, payload.Ballast))
	defer f.Close()
	io.ReadFull(f, got)
	if string(got) != string(want) {
		t.Error("the ballast isn't the keystream")
	}
	// The drive's copy of the record is what went to the hub.
	dc, _ := os.ReadFile(filepath.Join(fsdir, payload.RecordFile))
	hc, _ := os.ReadFile(filepath.Join(r.spool, "drives", records.DirName(s.Drive.Serial()), end.Job.RecordID, "record.json"))
	if strings.TrimSpace(string(dc)) != strings.TrimSpace(string(hc)) {
		t.Error("the drive's record differs from the hub's")
	}
}

func TestLoadRefusesAChangedSet(t *testing.T) {
	r := newLoadRig(t, loadSize)
	s := r.b.Status()
	err := r.b.Load(s.Drive.Serial(), s.Session, "test", strings.Repeat("0", 64))
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("got %v", err)
	}
	if st := r.b.Status(); st.State != model.Present {
		t.Errorf("state %s: a refused load touched the drive", st.State)
	}
}

func TestLoadRefusesAStaleConfirm(t *testing.T) {
	r := newLoadRig(t, loadSize)
	s := r.b.Status()
	if err := r.b.Load("SOMEONE-ELSE", s.Session, "test", r.hash(t)); err == nil {
		t.Error("a different serial started a load")
	}
	if err := r.b.Load(s.Drive.Serial(), s.Session.Add(-time.Hour), "test", r.hash(t)); err == nil {
		t.Error("an old insertion started a load")
	}
}

func TestLoadNeedsTheCap(t *testing.T) {
	r := newLoadRig(t, loadSize)
	r.b.mu.Lock()
	r.b.Loads = false
	r.b.mu.Unlock()
	s := r.b.Status()
	err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t))
	if err == nil || !strings.Contains(err.Error(), "caps") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadTooBigForTheDrive(t *testing.T) {
	r := newLoadRig(t, loadSize)
	// A set the size of the drive.
	big := make([]byte, loadSize)
	os.WriteFile(filepath.Join(r.lib.Dir, "big"), big, 0o644)
	txt := fmt.Sprintf("set test\n  label TEST\n  file %s  %d  big\n", sumOf(big), len(big))
	os.WriteFile(filepath.Join(r.lib.Dir, payload.FileName), []byte(txt), 0o644)
	s := r.b.Status()
	err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t))
	if err == nil || !strings.Contains(err.Error(), "too big") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadResumesADroppedConnection(t *testing.T) {
	r := newLoadRig(t, loadSize)
	r.flaky.Store(true)
	s := r.b.Status()
	if err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t)); err != nil {
		t.Fatal(err)
	}
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	if r.flaky.Load() {
		t.Error("the connection never dropped")
	}
	if got := r.record(t, s.Drive.Serial(), end.Job.RecordID); !got.Load.OK() {
		t.Errorf("load: %+v", got.Load)
	}
}

func TestLoadFailsAFileThatDoesntMatch(t *testing.T) {
	r := newLoadRig(t, loadSize)
	// The hub's copy rots after the list was made.
	s := r.b.Status()
	hash := r.hash(t)
	os.WriteFile(filepath.Join(r.lib.Dir, "sub", "b file.txt"), []byte("hullo"), 0o644)
	if err := r.b.Load(s.Drive.Serial(), s.Session, "test", hash); err != nil {
		t.Fatal(err)
	}
	end := r.settle(t)
	if end.State != model.Failed || !strings.Contains(end.Message, "hash") {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	rec := r.record(t, s.Drive.Serial(), end.Job.RecordID)
	c := records.NewCertificate(rec, nil)
	if c.ForSale {
		t.Error("a failed load is for sale")
	}
}

// A drive that loses a file between the write and the read comes back FAILED.
type rottingDev struct {
	*Dev
	rot func(dir string)
}

func (d *rottingDev) Mount(ctx context.Context, part string, ro bool) (string, error) {
	if ro {
		d.rot(part)
	}
	return d.Dev.Mount(ctx, part, ro)
}

func TestLoadReadBackFindsDamage(t *testing.T) {
	for _, tc := range []struct {
		name string
		rot  func(dir string)
		want string
	}{
		{"a file", func(dir string) {
			f, _ := os.OpenFile(filepath.Join(dir, "a.bin"), os.O_WRONLY, 0)
			f.WriteAt([]byte{0}, 100)
			f.Close()
		}, "files"},
		{"the ballast", func(dir string) {
			f, _ := os.OpenFile(filepath.Join(dir, payload.Ballast), os.O_WRONLY, 0)
			f.WriteAt([]byte{0, 0, 0, 0, 0, 0, 0, 0}, 4096)
			f.Close()
		}, "ballast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newLoadRigOn(t, loadSize, func(d *Dev) Source { return &rottingDev{Dev: d, rot: tc.rot} })
			s := r.b.Status()
			if err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t)); err != nil {
				t.Fatal(err)
			}
			end := r.settle(t)
			if end.State != model.Failed || !strings.Contains(end.Message, tc.want) {
				t.Fatalf("ended %s: %s", end.State, end.Message)
			}
			rec := r.record(t, s.Drive.Serial(), end.Job.RecordID)
			if rec.Load == nil || rec.Load.OK() {
				t.Fatalf("result: %+v", rec.Load)
			}
			if records.NewCertificate(rec, nil).ForSale {
				t.Error("a damaged load is for sale")
			}
		})
	}
}

func TestLoadSampledClaim(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a real file")
	}
	size := int64(2*wipe.SampleSections*wipe.SampleBlock + 8<<20)
	r := newLoadRig(t, size)
	s := r.b.Status()
	if err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t)); err != nil {
		t.Fatal(err)
	}
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	rec := r.record(t, s.Drive.Serial(), end.Job.RecordID)
	if !rec.Wipe.Sampled || rec.Wipe.Samples == 0 {
		t.Fatalf("not sampled: %+v", rec.Wipe.Method)
	}
	san := records.Sanitize(rec)
	if !strings.Contains(san.Verify, "§7.3") || !strings.Contains(san.Standard, "§7.3") {
		t.Errorf("claim: %+v", san)
	}
	all := records.Markdown(rec)
	// The overwrite did reach every sector, and the record may say so.  What
	// it can't say is that every one was read.
	for _, wrong := range []string{"every sector was", "read every", "ryptographic", "Every byte of this drive was overwritten with a keystream, then read back"} {
		if strings.Contains(all, wrong) {
			t.Errorf("the record says %q", wrong)
		}
	}
}

// The load's own phases hold to the ceiling too, not just the overwrite.
type hotOnLoad struct {
	*Dev
	mu    sync.Mutex
	loads atomic.Bool
	reads int
}

func (h *hotOnLoad) Format(ctx context.Context, d Device, fs, label string) (string, error) {
	h.loads.Store(true)
	return h.Dev.Format(ctx, d, fs, label)
}

func (h *hotOnLoad) Temperature(context.Context, Device) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.loads.Load() {
		return 40, nil
	}
	h.reads++
	if h.reads == 1 {
		return 70, nil // over, once the load starts
	}
	return 40, nil
}

func TestLoadPhasesPauseToo(t *testing.T) {
	r := newLoadRigOn(t, loadSize, func(d *Dev) Source { return &hotOnLoad{Dev: d} })
	r.b.HeatEvery, r.b.CoolEvery = time.Nanosecond, time.Millisecond
	s := r.b.Status()
	if err := r.b.Load(s.Drive.Serial(), s.Session, "test", r.hash(t)); err != nil {
		t.Fatal(err)
	}
	end := r.settle(t)
	if end.State != model.Done {
		t.Fatalf("ended %s: %s", end.State, end.Message)
	}
	rec := r.record(t, s.Drive.Serial(), end.Job.RecordID)
	if rec.Heat == nil || rec.Heat.Pauses != 1 || rec.Heat.PeakC != 70 || !rec.Load.OK() {
		t.Errorf("heat %+v load %+v", rec.Heat, rec.Load)
	}
}
