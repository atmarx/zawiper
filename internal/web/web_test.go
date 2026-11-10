package web

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atmarx/zawiper/internal/hub"
	linkpkg "github.com/atmarx/zawiper/internal/link"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/proto"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/sign"
	"github.com/atmarx/zawiper/internal/smart"
	"github.com/atmarx/zawiper/internal/wipe"
)

const host = "zawiper.example.org"

// fakeFleet is a hub with no bays behind it: it records every call and
// ticks when told to.
type fakeFleet struct {
	mu    sync.Mutex
	bays  map[string]hub.Bay
	calls []call
	subs  []chan struct{}
}

type call struct {
	Key string
	Req proto.Request
}

func (f *fakeFleet) Bays() []hub.Bay {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hub.Bay
	for _, b := range f.bays {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (f *fakeFleet) Bay(key string) (hub.Bay, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bays[key]
	return b, ok
}

func (f *fakeFleet) Call(_ context.Context, key string, req proto.Request) (*model.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{key, req})
	b, ok := f.bays[key]
	if !ok {
		return nil, errors.New("no such bay")
	}
	s := b.Snap
	return &s, nil
}

func (f *fakeFleet) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch, func() {}
}

func (f *fakeFleet) set(key string, mod func(*hub.Bay)) {
	f.mu.Lock()
	b := f.bays[key]
	mod(&b)
	f.bays[key] = b
	subs := f.subs
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *fakeFleet) took() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

var (
	t0      = time.Date(2026, 10, 23, 14, 2, 11, 0, time.UTC)
	session = time.Date(2026, 10, 25, 9, 30, 0, 123456789, time.UTC)
)

func setup(t *testing.T) (*fakeFleet, http.Handler, *model.Drive) {
	t.Helper()
	js, err := os.ReadFile("../smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := smart.Parse(js)
	drv := &model.Drive{
		Dev: "/dev/sdc", Size: info.CapacityBytes, LogicalSector: 512, PhysicalSector: 512, ReadOnly: true,
		Bridge: model.Bridge{VendorID: "174c", ProductID: "1156", Manufacturer: "ASMedia"},
		SMART:  info, Health: smart.Assess(info), Capacity: &model.Capacity{Method: "hdparm -N; no DCO", Visible: info.CapacityBytes / 512, Native: info.CapacityBytes / 512},
		Table: model.Table{Type: "gpt", Parts: []model.Part{
			{Path: "/dev/sdc1", Size: 500e9, FSType: "ext4", Label: "backup-gamma", Used: 312e9, Backup: `labelled "backup-gamma"`},
			{Path: "/dev/sdc2", Size: 500e9, FSType: "ntfs", Used: -1, Peek: "couldn't mount it read-only"},
		}},
	}
	store := &records.Store{Root: t.TempDir()}
	rec := &records.Record{Serial: info.Serial, Started: t0, Finished: t0.Add(4 * time.Second), Host: "lab-a", Bay: "bay1", Software: "test", Drive: *drv, Health: drv.Health}
	if err := store.SaveInspect(rec, js, []byte("smartctl text")); err != nil {
		t.Fatal(err)
	}
	w := wipeRecord(rec, info)
	if err := store.SaveWipe(w, map[string][]byte{"smart-before.txt": []byte("before")}); err != nil {
		t.Fatal(err)
	}
	f := &fakeFleet{bays: map[string]hub.Bay{
		"lab-a/bay1": {Key: "lab-a/bay1", Host: "lab-a", Name: "bay1", Caps: []string{"wipe"}, Online: true,
			Snap: model.Snapshot{State: model.Present, Bay: "bay1", Session: session, Drive: drv, RecordID: rec.ID}},
		"lab-a/bay2": {Key: "lab-a/bay2", Host: "lab-a", Name: "bay2", Caps: []string{"wipe"}, Online: true,
			Snap: model.Snapshot{State: model.Empty, Bay: "bay2"}},
		"lab-b/sas0": {Key: "lab-b/sas0", Host: "lab-b", Name: "sas0", Online: false, Seen: t0,
			Snap: model.Snapshot{State: model.Offline}},
	}}
	s := &Server{Fleet: f, Store: store, Hosts: []string{host}, Version: "test"}
	return f, s.Handler(), drv
}

// wipeRecord is a whole-drive wipe of the test drive, an hour after its
// inspection: 932 blocks of 1 GiB, one of them slow, with its key published.
func wipeRecord(rec *records.Record, info *smart.Info) *records.Record {
	w := *rec
	w.Started = rec.Started.Add(time.Hour)
	w.Wrote, w.Before = true, info
	size := info.CapacityBytes
	n := int((size + wipe.Block - 1) / wipe.Block)
	cells := make([]wipe.Cell, n)
	for i := range cells {
		cells[i] = wipe.Cell{Write: 7 + float64(i)/200, Read: 6 + float64(i)/200}
	}
	cells[400].Read = 40 // a weak spot the drive retried through
	w.Wipe = &wipe.Result{Method: "AES-256-CTR keystream, one pass, read back and compared", Size: size, Block: wipe.Block,
		Written: size, Checked: size, Cells: cells,
		Started: w.Started, Wrote: w.Started.Add(2 * time.Hour), Verified: w.Started.Add(4 * time.Hour),
		Key: "2c59dddb88edee19dfb30be18b3fccf2383f8a39292ced202d8e63814749bd32", IV: "d198657ce190af6f5093548e428b3b28"}
	w.Health.Notes = append(w.Health.Notes, "Slow blocks: #401.")
	return &w
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func post(h http.Handler, path, body string, mod func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Host = host
	r.Header.Set("Origin", "https://"+host)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Za-Wiper", "1")
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func wants(t *testing.T, what, body string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("%s lacks %q", what, s)
		}
	}
}

func TestPages(t *testing.T) {
	_, h, _ := setup(t)
	for path, want := range map[string][]string{
		"/drives":          {"9VPAX9NA", "1 drive on record"},
		"/drives?q=x9n":    {"9VPAX9NA"},
		"/drives/9VPAX9NA": {"2026-10-23T14-02-11Z-inspect", "inspect", "lab-a/bay1"},
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect":               {"DRIVE RECORD", "Airflow_Temperature_Cel", "5 000c50 02d635ade", "Nothing was written", "PASSED"},
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/record.md":     {"# 9VPAX9NA"},
		"/drives/9VPAX9NA/2026-10-23T15-02-11Z-wipe":                  {"Wiped, verified", "Wipe: verified", "1,000,204,886,016 of 1,000,204,886,016", "██▒█", "#401", "Found before the wipe", "smart-before.txt", "Every byte", "The key is published", "/static/certificate.css"},
		"/drives/9VPAX9NA/2026-10-23T15-02-11Z-wipe/record.md":        {"## Certificate of sanitization", "| Level | **Clear** |", "## Check it yourself", "d198657ce190af6f5093548e428b3b28", "openssl enc -aes-256-ctr", "Not signed yet"},
		"/static/certificate.css":                                     {"@page za-sheet", "size: letter"},
		"/drives/9VPAX9NA/2026-10-23T15-02-11Z-wipe/smart-before.txt": {"before"},
		"/static/zawiper.css":                                         {"--hazard", "Px437 IBM VGA 8x16"},
		"/static/fonts/LICENSE.txt":                                   {"Attribution-ShareAlike 4.0"},
	} {
		w := get(h, path)
		if w.Code != 200 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
			continue
		}
		wants(t, path, w.Body.String(), want...)
	}
	if w := get(h, "/static/fonts/Web437_IBM_VGA_8x16.woff"); w.Code != 200 || !strings.HasPrefix(w.Body.String(), "wOFF") {
		t.Errorf("the font: %d", w.Code)
	}
	if w := get(h, "/drives?q=nosuchdrive"); !strings.Contains(w.Body.String(), "hasn't been through the bay") {
		t.Error("empty search")
	}
	for _, path := range []string{
		"/drives/NOPE", "/drives/9VPAX9NA/nope",
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/drive.json",
		"/drives/9VPAX9NA/..%2f..%2fetc/record.md",
		"/bays/lab-a/nope", "/bays/lab-a%2fbay1/x", "/fragment/bays/nope/nope",
	} {
		if w := get(h, path); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

// Every grade carries its pills: the drive list, the drive's history, the
// record, and record.md as text.
func TestPills(t *testing.T) {
	_, h, _ := setup(t)
	pill := `<span class="pill pill-drive" title="Airflow_Temperature_Cel crossed its threshold in the past`
	for _, path := range []string{"/drives", "/drives/9VPAX9NA", "/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect"} {
		wants(t, path, get(h, path).Body.String(), pill, "PAST Airflow_Temperature_Cel</span>")
	}
	wants(t, "record.md", get(h, "/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/record.md").Body.String(),
		"- `PAST Airflow_Temperature_Cel` Airflow_Temperature_Cel crossed")
}

func TestPillOrder(t *testing.T) {
	fs := []smart.Finding{
		{Tag: "CRC", About: smart.Bridge},
		{Tag: "REALLOC", About: smart.Drive},
		{Tag: "PENDING", About: smart.Drive, Fail: true},
		{Tag: "HOT", About: smart.Drive},
	}
	p := pills(fs, 3)
	var got []string
	for _, f := range p.Shown {
		got = append(got, f.Tag+":"+pillKind(f))
	}
	if strings.Join(got, " ") != "PENDING:fail REALLOC:drive HOT:drive" || p.More != 1 {
		t.Errorf("pills %v, %d more", got, p.More)
	}
	if p := pills(fs, 0); len(p.Shown) != 4 || p.More != 0 {
		t.Errorf("all: %d shown, %d more", len(p.Shown), p.More)
	}
	if fs[0].Tag != "CRC" {
		t.Error("pills reordered the record's own findings")
	}
}

func TestWall(t *testing.T) {
	f, h, drv := setup(t)
	f.set("lab-a/bay2", func(b *hub.Bay) {
		b.Snap = model.Snapshot{State: model.Running, Drive: drv, Session: session, Job: &model.Job{
			Kind: "wipe", Phase: "verify", Serial: drv.Serial(), Map: "vvvvRwww", Done: 1500, Total: 1000, Started: t0,
		}}
	})
	for _, path := range []string{"/", "/fragment/wall"} {
		w := get(h, path)
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
		body := w.Body.String()
		wants(t, path, body,
			"lab-a", "lab-b", // grouped by host
			`href="/bays/lab-a/bay1"`, `data-key="lab-a/bay1"`, `data-sig="`,
			"ST31000528AS", `<span class="serial-tail">PAX9NA</span>`, // the last six, big
			"⚠ Looks like a backup", "tile-warn",
			"tile-running", "75.0%", "Reading it back", `data-field="mini"`, `<span class="c-verified">■■■■</span><span class="c-cur">r</span>`,
			"tile-offline", "OFFLINE", "Link down",
		)
		if strings.Index(body, "lab-a") > strings.Index(body, "lab-b") {
			t.Errorf("%s: hosts out of order", path)
		}
	}
	wants(t, "/", get(h, "/").Body.String(), `data-fragment="/fragment/wall"`, "Recently through the bays", "9VPAX9NA")
}

func TestBayStates(t *testing.T) {
	f, h, drv := setup(t)
	const page = "/bays/lab-a/bay1"
	const frag = "/fragment/bays/lab-a/bay1"
	set := func(s model.Snapshot) { f.set("lab-a/bay1", func(b *hub.Bay) { b.Snap = s }) }
	job := func(phase, m, evidence string) *model.Job {
		return &model.Job{Kind: "wipe", Phase: phase, Serial: drv.Serial(), Session: session, Started: t0,
			Ended: t0.Add(2 * time.Hour), Map: m, Done: 2 << 30, Total: 1 << 30, MBps: 150, ETA: 3725, TempC: 41,
			Evidence: evidence, RecordID: "2026-10-23T16-02-11Z-wipe"}
	}

	// PRESENT: the drive card, and the confirm drawn from it.
	body := get(h, page).Body.String()
	wants(t, "present", body, "ST31000528AS", "9VPA", "X9NA", "PASS WITH NOTES", "312.00 GB used", "Looks like a backup",
		`data-act="/api/bays/lab-a/bay1/reread"`, "Re-read", `data-fragment="/fragment/bays/lab-a/bay1"`,
		`disabled title="Phase 3">Wipe + Load`,
		`<dialog class="confirm confirm-warn"`, `data-url="/api/bays/lab-a/bay1/wipe"`,
		`data-serial="9VPAX9NA"`, `data-session="2026-10-25T09:30:00.123456789Z"`,
		"Erase ST31000528AS, serial 9VPAX9NA, 1.00 TB?", "Found on it: GPT · 2 partitions", "Erase this backup drive",
		"This read's record")
	if strings.Contains(body, "data-open-confirm disabled") {
		t.Error("Wipe is disabled on a clean PRESENT drive")
	}

	// Not a backup: the plain button.
	plain := *drv
	plain.Table = model.Table{}
	set(model.Snapshot{State: model.Present, Session: session, Drive: &plain})
	body = get(h, frag).Body.String()
	wants(t, "plain", body, ">Erase this drive<", "No partition table, no filesystems.  Looks blank already.")
	if strings.Contains(body, "confirm-warn") || strings.Contains(body, "Erase this backup drive") {
		t.Error("a blank drive got the backup treatment")
	}

	// Refusals: no Wipe, and no confirm to open.
	set(model.Snapshot{State: model.Present, Session: session, Drive: drv, Refusals: []string{"There are 2 drives"}, Devices: []string{"/dev/sdc", "/dev/sdd"}})
	body = get(h, frag).Body.String()
	wants(t, "refused", body, "won't act", "/dev/sdd", `data-open-confirm disabled`)
	if strings.Contains(body, "<dialog") {
		t.Error("a refused drive has a confirm")
	}

	// RUNNING: the warning and DEFRAG, and no buttons at all.
	set(model.Snapshot{State: model.Running, Session: session, Drive: drv, Job: &model.Job{
		Kind: "wipe", Phase: "write", Serial: drv.Serial(), Started: t0, Map: "wwW.....", Done: 3 << 30, Total: 8 << 30,
		MBps: 151.4, ETA: 3725, TempC: 41, CPU: 21.4, HostCPU: 9.6, Cores: 8, Load: 1.25}})
	body = get(h, page).Body.String()
	wants(t, "running", body, "Do not pull the drive.", "Once started, this process cannot be stopped from here.",
		"21% of a core", "10% of 8 cores · load 1.25", `data-field="band"`, `data-field="guess"`,
		`class="dos"`, "Writing the keystream", "Block 3 of 8",
		"151 MB/s", "1:02:05", "41 °C", "18.7%", `data-field="map"`,
		`<span class="c-written">■■</span><span class="c-cur">W</span><span class="c-todo">░░░░░</span>`)
	if strings.Contains(content(body), "<button") {
		t.Error("RUNNING has a button")
	}
	wants(t, "running tile", get(h, "/").Body.String(), "18.7%", `data-field="left">about 1 h 2 min left<`)
	if chrome := strings.Replace(body, content(body), "", 1); strings.Count(chrome, "<button") != 1 || !strings.Contains(chrome, "data-wide") {
		t.Error("the page's chrome has a button besides the layout toggle")
	}

	// DONE: the grade, the numbers, the record, Ack and Eject.
	set(model.Snapshot{State: model.Done, Session: session, Drive: drv, Job: job("record", "vvsvvvvv", "")})
	body = get(h, page).Body.String()
	wants(t, "done", body, "Wiped and verified.", "PASS WITH NOTES", "2:00:00", "1.07 GB", "Slow blocks",
		`href="/drives/9VPAX9NA/2026-10-23T16-02-11Z-wipe"`, "The record",
		`data-act="/api/bays/lab-a/bay1/ack"`, `data-act="/api/bays/lab-a/bay1/eject"`, "Done.  Every block",
		"Taking the drive out?", "Print the certificate", `href="/drives/9VPAX9NA/2026-10-23T16-02-11Z-wipe?print=1"`)

	// FAILED: the evidence before anything else.
	set(model.Snapshot{State: model.Failed, Session: session, Drive: drv, Job: job("verify", "vvvBwwww", "read mismatch at offset 3,221,225,472: 512 bytes differ")})
	body = get(h, page).Body.String()
	wants(t, "failed", body, "The wipe failed.", "read mismatch at offset 3,221,225,472", "Reading it back and comparing",
		"<strong>not</strong> wiped", "/ack", "/eject", `class="c-bad">B<`, "Acknowledge first", "Print the record")
	if strings.Index(body, "read mismatch") > strings.Index(body, `class="dos"`) || strings.Index(body, "read mismatch") > strings.Index(body, "ST31000528AS") {
		t.Error("FAILED doesn't lead with the evidence")
	}

	// INTERRUPTED: the spec's words, exactly.
	set(model.Snapshot{State: model.Interrupted, Session: session, Drive: drv, Job: job("write", "ww......", "")})
	body = get(h, page).Body.String()
	wants(t, "interrupted", body, "The power went out mid-wipe.  I don't know what state this drive is in, and I won't pretend I do.", "/ack")

	// ALARM: a full-width stop that needs acknowledging.
	set(model.Snapshot{State: model.Alarm, Session: session, Drive: drv, Job: job("write", "www.....", "serial changed: 9VPAX9NA became ZZZ")})
	body = get(h, page).Body.String()
	wants(t, "alarm", body, `class="stop"`, "STOP", "bayd ended the job itself", "serial changed", `data-act="/api/bays/lab-a/bay1/ack"`)
	if strings.Contains(body, "/eject") {
		t.Error("ALARM offers Eject before it's acknowledged")
	}

	// EMPTY and READING, as in Phase 1.
	set(model.Snapshot{State: model.Empty})
	wants(t, "empty", get(h, page).Body.String(), "The viper is hungry", "Recently through the bays")
	set(model.Snapshot{State: model.Reading, Message: "smartctl -x /dev/zawiper/bay1"})
	wants(t, "reading", get(h, frag).Body.String(), "smartctl -x /dev/zawiper/bay1")

	// OFFLINE, whatever the last snapshot said.
	body = get(h, "/bays/lab-b/sas0").Body.String()
	wants(t, "offline", body, "OFFLINE", "link is down")
	if strings.Contains(content(body), "<button") {
		t.Error("an offline bay has buttons")
	}
}

func TestWipeableNeedsTheCap(t *testing.T) {
	f, h, _ := setup(t)
	f.set("lab-a/bay1", func(b *hub.Bay) { b.Caps = nil })
	wants(t, "no cap", get(h, "/fragment/bays/lab-a/bay1").Body.String(), "data-open-confirm disabled", "isn&#39;t set up to wipe")
}

func TestWrongHost(t *testing.T) {
	_, h, _ := setup(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("rebinding: %d", w.Code)
	}
}

const wipeBodyOK = `{"serial":"9VPAX9NA","session":"2026-10-25T09:30:00.123456789Z"}`

func TestGuard(t *testing.T) {
	f, h, _ := setup(t)
	refused := map[string]func(*http.Request){
		"no origin":    func(r *http.Request) { r.Header.Del("Origin") },
		"other origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"form post":    func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"text post":    func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"no header":    func(r *http.Request) { r.Header.Del("X-Za-Wiper") },
		"null origin":  func(r *http.Request) { r.Header.Set("Origin", "null") },
	}
	for _, verb := range []string{"reread", "wipe", "ack", "eject"} {
		path := "/api/bays/lab-a/bay1/" + verb
		for name, mod := range refused {
			if w := post(h, path, wipeBodyOK, mod); w.Code < 400 {
				t.Errorf("%s %s: %d", verb, name, w.Code)
			}
		}
		if n := len(f.took()); n != 0 {
			t.Fatalf("%s: a refused request reached the fleet (%d calls)", verb, n)
		}
		if w := get(h, path); w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d", verb, w.Code)
		}
	}
	for _, verb := range []proto.Verb{proto.Reread, proto.Wipe, proto.Ack, proto.Eject} {
		if w := post(h, "/api/bays/lab-a/bay1/"+string(verb), wipeBodyOK, nil); w.Code != 200 {
			t.Errorf("good %s: %d %s", verb, w.Code, w.Body.String())
		}
	}
	got := f.took()
	if len(got) != 4 {
		t.Fatalf("calls: %+v", got)
	}
	for i, verb := range []proto.Verb{proto.Reread, proto.Wipe, proto.Ack, proto.Eject} {
		if got[i].Key != "lab-a/bay1" || got[i].Req.Verb != verb {
			t.Errorf("call %d: %+v", i, got[i])
		}
		if verb != proto.Wipe && (got[i].Req.Serial != "" || !got[i].Req.Session.IsZero()) {
			t.Errorf("%s carried more than its verb: %+v", verb, got[i].Req)
		}
	}
}

func TestWipePassesSerialAndSession(t *testing.T) {
	f, h, _ := setup(t)
	const path = "/api/bays/lab-a/bay1/wipe"
	for name, body := range map[string]string{
		"empty":          `{}`,
		"no session":     `{"serial":"9VPAX9NA"}`,
		"no serial":      `{"session":"2026-10-25T09:30:00Z"}`,
		"names a device": `{"serial":"9VPAX9NA","session":"2026-10-25T09:30:00Z","dev":"/dev/sda"}`,
		"names a bay":    `{"serial":"9VPAX9NA","session":"2026-10-25T09:30:00Z","key":"lab-b/sas0"}`,
		"not JSON":       `serial=9VPAX9NA`,
	} {
		if w := post(h, path, body, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if n := len(f.took()); n != 0 {
		t.Fatalf("a bad wipe reached the fleet: %+v", f.took())
	}
	if w := post(h, path, wipeBodyOK, nil); w.Code != 200 {
		t.Fatalf("good wipe: %d %s", w.Code, w.Body.String())
	}
	got := f.took()
	if len(got) != 1 {
		t.Fatalf("calls: %+v", got)
	}
	want := proto.Request{Verb: proto.Wipe, Serial: "9VPAX9NA", Session: session}
	if got[0].Key != "lab-a/bay1" || got[0].Req.Verb != want.Verb || got[0].Req.Serial != want.Serial || !got[0].Req.Session.Equal(want.Session) {
		t.Errorf("wipe went down as %+v, want %+v", got[0], want)
	}

	// The page's own confirm, as drawn, posts something this accepts.
	body := get(h, "/bays/lab-a/bay1").Body.String()
	if !strings.Contains(body, `data-session="`+session.Format(time.RFC3339Nano)+`"`) {
		t.Error("the confirm's session doesn't round-trip")
	}

	// An offline bay or an unknown one never gets a call.
	if w := post(h, "/api/bays/lab-b/sas0/wipe", wipeBodyOK, nil); w.Code != http.StatusConflict {
		t.Errorf("offline: %d", w.Code)
	}
	if w := post(h, "/api/bays/lab-b/nope/wipe", wipeBodyOK, nil); w.Code != 404 {
		t.Errorf("unknown bay: %d", w.Code)
	}
	if n := len(f.took()); n != 1 {
		t.Errorf("calls: %+v", f.took())
	}
}

func TestNoAbort(t *testing.T) {
	f, h, _ := setup(t)
	for _, verb := range []string{"abort", "stop", "cancel", "status", "load"} {
		if w := post(h, "/api/bays/lab-a/bay1/"+verb, "{}", nil); w.Code != 404 && w.Code != 405 {
			t.Errorf("%s: %d", verb, w.Code)
		}
	}
	if n := len(f.took()); n != 0 {
		t.Errorf("calls: %+v", f.took())
	}
	if strings.Contains(strings.ToLower(get(h, "/static/zawiper.js").Body.String()), "abort") {
		t.Error("the script mentions abort")
	}
	// The arcade is for whoever's watching: it never talks to anything.
	arcade := get(h, "/static/arcade.js").Body.String()
	for _, s := range []string{"fetch(", "XMLHttpRequest", "/api/", "EventSource", "sendBeacon", "abort"} {
		if strings.Contains(arcade, s) {
			t.Errorf("arcade.js has %q", s)
		}
	}
}

// content is a page without its header and footer: what's about the bay.
func content(page string) string {
	i, j := strings.Index(page, "<main>"), strings.Index(page, "</main>")
	if i < 0 || j < i {
		return page
	}
	return page[i : j+len("</main>")]
}

func TestRereadRateLimitPerBay(t *testing.T) {
	f, h, _ := setup(t)
	f.set("lab-a/bay2", func(b *hub.Bay) { b.Snap.State = model.Present })
	if w := post(h, "/api/bays/lab-a/bay1/reread", "{}", nil); w.Code != 200 {
		t.Fatalf("first: %d", w.Code)
	}
	if w := post(h, "/api/bays/lab-a/bay1/reread", "{}", nil); w.Code != http.StatusTooManyRequests {
		t.Errorf("second inside 2s: %d", w.Code)
	}
	if w := post(h, "/api/bays/lab-a/bay2/reread", "{}", nil); w.Code != 200 {
		t.Errorf("another bay: %d", w.Code)
	}
	if n := len(f.took()); n != 2 {
		t.Errorf("calls: %+v", f.took())
	}
}

func TestEventsSameFrameForEveryone(t *testing.T) {
	f, h, drv := setup(t)
	f.set("lab-a/bay1", func(b *hub.Bay) {
		b.Snap = model.Snapshot{State: model.Running, Session: session, Drive: drv, Job: &model.Job{
			Kind: "wipe", Phase: "write", Started: t0, Map: "W.......", Total: 8 << 30}}
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	open := func() (*bufio.Reader, func()) {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/events", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("content type %q", ct)
		}
		return bufio.NewReader(resp.Body), func() { cancel(); resp.Body.Close() }
	}
	next := func(r *bufio.Reader) string {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				return line
			}
		}
	}
	a, closeA := open()
	defer closeA()
	b, closeB := open()
	defer closeB()

	fa, fb := next(a), next(b)
	if fa != fb {
		t.Fatalf("first frames differ:\n%s\n%s", fa, fb)
	}
	wants(t, "first frame", fa, `"key":"lab-a/bay1"`, `"state":"RUNNING"`, `"map":"W......."`, `"key":"lab-b/sas0","state":"OFFLINE"`)

	// The bay moves on.  Both viewers get the same next frame, byte for byte,
	// and the signature holds, so the page updates in place.
	f.set("lab-a/bay1", func(b *hub.Bay) {
		j := *b.Snap.Job
		j.Map, j.Done = "wW......", 1<<30
		b.Snap.Job = &j
	})
	fa2, fb2 := next(a), next(b)
	if fa2 != fb2 {
		t.Fatalf("second frames differ:\n%s\n%s", fa2, fb2)
	}
	wants(t, "second frame", fa2, `"map":"wW......"`, `"pct":"6.2%"`)
	sig := func(s string) string { i := strings.Index(s, `"sig":"`); return s[i : i+24] }
	if sig(fa) != sig(fa2) {
		t.Error("progress changed the signature")
	}

	// The poll fallback serves the same shape.
	wants(t, "/api/frame", get(h, "/api/frame").Body.String(), `"map":"wW......"`)
}

func TestMini(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"vvvv", "vvvv"},
		{"vvvvvvvvwwwwwwww", "vw"},
		{"vvvsvvvvBvvvvvvv", "sB"},
		{"vvvvvvvwRwwwww..", "wR"},
		{"vvvvvvvv........", "v."},
	} {
		in := c.in
		if got := Mini(in, len(c.want)); len(c.in) > len(c.want) && got != c.want {
			t.Errorf("Mini(%q) = %q, want %q", in, got, c.want)
		}
	}
	if got := string(Cells("v.<script>")); strings.Contains(got, "<script") || strings.Contains(got, "script") {
		t.Errorf("a map byte reached the HTML as itself: %s", got)
	}
}

func TestDemoFleet(t *testing.T) {
	f := NewDemoFleet()
	defer f.Close()
	bays := f.Bays()
	states := map[model.State]int{}
	hosts := map[string]bool{}
	for _, b := range bays {
		states[stateOf(b)]++
		hosts[b.Host] = true
	}
	if len(bays) != 4 || len(hosts) != 2 || states[model.Running] != 1 || states[model.Present] != 1 || states[model.Done] != 1 || states[model.Offline] != 1 {
		t.Fatalf("demo fleet: %d bays, hosts %v, states %v", len(bays), hosts, states)
	}
	ticks, cancel := f.Subscribe()
	defer cancel()
	before, _ := f.Bay("lab-a/bay1")
	select {
	case <-ticks:
	case <-time.After(3 * time.Second):
		t.Fatal("no tick")
	}
	after, _ := f.Bay("lab-a/bay1")
	if before.Snap.Job.Done == after.Snap.Job.Done {
		t.Error("the running job didn't move")
	}

	b, _ := f.Bay("lab-a/bay2")
	if _, err := f.Call(context.Background(), b.Key, proto.Request{Verb: proto.Wipe, Serial: b.Snap.Drive.Serial(), Session: b.Snap.Session.Add(time.Second)}); err == nil {
		t.Error("the demo took a stale confirm")
	}
	if _, err := f.Call(context.Background(), b.Key, proto.Request{Verb: proto.Wipe, Serial: b.Snap.Drive.Serial(), Session: b.Snap.Session}); err != nil {
		t.Error(err)
	}
	if b, _ := f.Bay("lab-a/bay2"); b.Snap.State != model.Running {
		t.Errorf("after wipe: %s", b.Snap.State)
	}

	// The whole demo renders.
	h := (&Server{Fleet: f, Store: &records.Store{Root: t.TempDir()}, Version: "test"}).Handler()
	for _, p := range []string{"/", "/bays/lab-a/bay1", "/bays/lab-a/bay2", "/bays/lab-b/dock", "/bays/lab-b/sas0", "/api/frame"} {
		if w := get(h, p); w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
}

// recordPage renders the record page the way the handler will once it
// passes the certificate: the page's data plus "Cert".
func recordPage(t *testing.T, rec *records.Record, seal *records.Seal) string {
	t.Helper()
	tm := template.Must(template.New("").Funcs(funcs).ParseFS(templateFS,
		"templates/layout.html", "templates/parts.html", "templates/record.html"))
	data := map[string]any{"Title": rec.Serial, "Nav": "drives", "R": rec, "Dir": rec.Serial, "Version": "test"}
	if c := records.NewCertificate(rec, seal); c != nil {
		data["Cert"] = c
	}
	var b strings.Builder
	if err := tm.ExecuteTemplate(&b, "layout", data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func signedWipe(t *testing.T) (*records.Store, *records.Record, *records.Seal) {
	t.Helper()
	js, _ := os.ReadFile("../smart/testdata/st31000528as.json")
	info, _ := smart.Parse(js)
	t0 := time.Date(2026, 10, 24, 8, 15, 40, 0, time.UTC)
	rec := &records.Record{Serial: info.Serial, Started: t0, Finished: t0.Add(5 * time.Hour), Host: "lab-a", Bay: "bay1", Software: "bayd 0.2.0 (ddc40cb)",
		Drive: model.Drive{Size: info.CapacityBytes, LogicalSector: 512, PhysicalSector: 512, SMART: info, Capacity: &model.Capacity{Method: "hdparm -N; no DCO", Visible: info.CapacityBytes / 512, Native: info.CapacityBytes / 512},
			Bridge: model.Bridge{VendorID: "174c", ProductID: "1156", Manufacturer: "ASMedia"}},
		Health: smart.Assess(info)}
	w := wipeRecord(rec, info)
	store := &records.Store{Root: t.TempDir()}
	if err := store.SaveWipe(w, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "za-wiper")
	sign.GenerateKey(keyPath)
	k, _ := sign.LoadKey(keyPath)
	s := &sign.Signer{Key: k, KeyURL: "https://zawiper.example/za-wiper.pub", SiteURL: "https://zawiper.example"}
	if err := s.SignRecord(store, w.Serial, w.ID); err != nil {
		t.Fatal(err)
	}
	return store, w, s.Seal(store, w.Serial, w.ID)
}

func TestCertificate(t *testing.T) {
	_, w, seal := signedWipe(t)
	page := recordPage(t, w, seal)
	for _, want := range []string{
		`<link rel="stylesheet" href="/static/certificate.css">`,
		"Certificate of sanitization", "Certificate of Sanitization",
		"<dt>Level</dt><dd><strong>Clear</strong></dd>",
		"NIST SP 800-88 Rev. 2, Guidelines for Media Sanitization (September 2025)", "IEEE Std 2883-2022",
		"Magnetic: hard disk drive, 7,200 rpm", "Full verification",
		"WIPED · VERIFIED", `lbl-serial s-xl">9VPAX9NA<`, "PASS WITH NOTES", "2026-10-24",
		strings.ReplaceAll(seal.Fingerprint, "+", "&#43;"), "https://zawiper.example/za-wiper.pub",
		"ssh-keygen -Y verify -f allowed_signers -I za-wiper -n za-wiper -s record.json.sig &lt; record.json",
		"zverify -pubkey https://zawiper.example/za-wiper.pub https://zawiper.example/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe/record.json /dev/sdX",
		"openssl enc -aes-256-ctr -K 2c59dddb", "cmp -n 1000204886016 - /dev/sdX",
		`<svg class="qr"`, `<svg class="map"`, `class="m-s"`, "Surface map: 932 blocks",
		"https://zawiper.example/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe",
		"record.json.sig?download", "Print the sheet",
		"Reallocated sectors",
		"Clear, per NIST SP 800-88 Rev. 2", "Za Wiper makes this claim, not NIST or IEEE.",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("record page lacks %q", want)
		}
	}
	// Purge is named only as what Clear can't do, never as a claim.
	for _, bad := range []string{"<strong>Purge", "Purge, NIST", "NOT FOR SALE", "Not signed yet"} {
		if strings.Contains(page, bad) {
			t.Errorf("record page says %q", bad)
		}
	}
}

// TestProofAtHome is a hub with no public address: the sheet carries no
// dead links, and proof.zip holds everything a buyer needs to check it.
func TestProofAtHome(t *testing.T) {
	store, w, _ := signedWipe(t)
	keyPath := filepath.Join(t.TempDir(), "za-wiper")
	sign.GenerateKey(keyPath)
	k, _ := sign.LoadKey(keyPath)
	home := &sign.Signer{Key: k}
	if err := home.SignRecord(store, w.Serial, w.ID); err != nil {
		t.Fatal(err)
	}
	page := recordPage(t, w, home.Seal(store, w.Serial, w.ID))
	for _, want := range []string{"Signed proof on request · " + w.ID, "zverify -pubkey za-wiper.pub record.json /dev/sdX", "proof.zip"} {
		if !strings.Contains(page, want) {
			t.Errorf("record page lacks %q", want)
		}
	}
	for _, bad := range []string{`<svg class="qr"`, "https://", "Published at"} {
		if strings.Contains(page, bad) {
			t.Errorf("record page says %q", bad)
		}
	}

	h := (&Server{Fleet: &fakeFleet{}, Store: store, Signer: home, Hosts: []string{host}, Version: "test"}).Handler()
	req := httptest.NewRequest("GET", "http://"+host+"/drives/"+w.Serial+"/"+w.ID+"/proof.zip", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Header().Get("Content-Disposition"), w.Serial+"-"+w.ID+".zip") {
		t.Fatalf("proof.zip: %d %v", rr.Code, rr.Header())
	}
	z, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, f := range z.File {
		rc, _ := f.Open()
		got[strings.TrimPrefix(f.Name, w.Serial+"-"+w.ID+"/")], _ = io.ReadAll(rc)
		rc.Close()
	}
	for _, name := range []string{"record.json", "record.json.sig", "record.md", "za-wiper.pub"} {
		if len(got[name]) == 0 {
			t.Errorf("proof.zip lacks %s; has %v", name, z.File)
		}
	}
	pub, err := sign.SignedBy(got["record.json.sig"])
	if err != nil || sign.Verify(pub, got["record.json"], got["record.json.sig"]) != nil ||
		string(got["za-wiper.pub"]) != sign.AuthorizedKey(pub)+"\n" {
		t.Errorf("the zip's signature doesn't check against its key: %v", err)
	}
	if !strings.Contains(string(got["record.md"]), "zverify -pubkey za-wiper.pub record.json") {
		t.Error("record.md in the zip points somewhere other than the files beside it")
	}
}

func TestCertificateNotForSale(t *testing.T) {
	_, w, seal := signedWipe(t)
	w.Wipe.Cells[17].Bad, w.Wipe.Bad, w.Wipe.Checked = 4096, 4096, w.Wipe.Checked-4096
	w.Health.Grade = smart.Fail
	page := recordPage(t, w, seal)
	for _, want := range []string{"sheet-nfs", "NOT FOR SALE", "No sanitization is claimed", "4,096 bytes didn&#39;t read back as written", `class="m-b"`} {
		if !strings.Contains(page, want) {
			t.Errorf("failed record lacks %q", want)
		}
	}
	for _, bad := range []string{"WIPED · VERIFIED", "<strong>Clear</strong>", "Certificate of Sanitization<"} {
		if strings.Contains(page, bad) {
			t.Errorf("failed record says %q", bad)
		}
	}

	// A drive that verified but failed its health check isn't for sale
	// either, though the wipe itself was Clear.
	_, w, seal = signedWipe(t)
	w.Health.Grade = smart.Fail
	w.Health.Notes = append(w.Health.Notes, "reallocated sectors rose during the wipe, 0 → 8.")
	page = recordPage(t, w, seal)
	for _, want := range []string{"sheet-nfs", "NOT FOR SALE", "the drive&#39;s health failed", "<strong>Clear</strong>"} {
		if !strings.Contains(page, want) {
			t.Errorf("unhealthy record lacks %q", want)
		}
	}
}

// An inspection prints as the plain record: no certificate, no sheet.
func TestInspectHasNoCertificate(t *testing.T) {
	_, h, _ := setup(t)
	body := get(h, "/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect").Body.String()
	if strings.Contains(body, "sheet-preview") || strings.Contains(body, "Certificate of") {
		t.Error("an inspection got a certificate")
	}
	if c := records.NewCertificate(&records.Record{Kind: "inspect"}, nil); c != nil {
		t.Error("NewCertificate for an inspection")
	}
}

func TestTrace(t *testing.T) {
	// Four blocks written, two read back: 200 MB/s tops the graph, and the
	// read-back wins its columns.
	enc, axis := trace([]uint16{180, 160, 100, 100}, []uint16{190, 0, 0, 0}, 4)
	if enc != ";;:-6-6-" {
		t.Errorf("trace %q", enc)
	}
	if !strings.HasPrefix(axis, "200┤\n") || !strings.Contains(axis, "100┤") || !strings.HasSuffix(axis, "  0┤") || strings.Count(axis, "\n") != traceRows-1 {
		t.Errorf("axis %q", axis)
	}
	rows := strings.Split(string(TraceRows(enc)), "\n")
	if len(rows) != traceRows {
		t.Fatalf("%d rows", len(rows))
	}
	if rows[0] != `<span class="t-r">▄</span><span class="t-w">   </span>` || rows[traceRows-1] != `<span class="t-r">█</span><span class="t-w">███</span>` {
		t.Errorf("rows %q", rows)
	}
	if enc, _ := trace(nil, nil, 32); enc != "" {
		t.Errorf("no rates drew %q", enc)
	}
	// More blocks than columns: each column is its blocks' average, and
	// the graph tops out at the next 50 MB/s.
	if enc, _ := trace([]uint16{100, 200, 0, 0}, nil, 2); enc != "<---" {
		t.Errorf("averaged %q", enc)
	}
}

func TestGroupAndGB(t *testing.T) {
	if g := Group("vvvwW...", 3); g != "vW." {
		t.Errorf("group %q", g)
	}
	if g := Group("vw", 1); g != "vw" {
		t.Errorf("group by one %q", g)
	}
	if gb(556_410_000_000) != "556.41 GB" || gb(1_000_204_886_016) != "1.000 TB" {
		t.Errorf("%s %s", gb(556_410_000_000), gb(1_000_204_886_016))
	}
	l := LiveOf(&model.Job{Map: strings.Repeat("v", 4000), Total: 4000 * gib, Done: 4000 * gib}, nil, time.Now())
	if len(l.Map) != 1334 || l.Scale != "1 block = 3 GiB" || l.Written != "4.295 TB" || l.Checked != "0.00 GB" {
		t.Errorf("4 TB live: %d cells, %q, %q, %q", len(l.Map), l.Scale, l.Written, l.Checked)
	}
	if len(l.Fine) != 4000 || l.FineScale != "1 block = 1 GiB" {
		t.Errorf("4 TB fine: %d cells, %q", len(l.Fine), l.FineScale)
	}
}

func TestBand(t *testing.T) {
	usb3 := &model.Drive{Bridge: model.Bridge{Speed: 5000},
		SMART: &smart.Info{RotationRate: 7200, Link: "3.0 Gb/s", LinkBps: 3_000_000_000}}
	scale, guess, limit := Band(&model.Job{MBps: 69, WriteMBps: []uint16{129, 110, 69}}, usb3)
	lines := strings.Split(scale, "\n")
	if len(lines) != 2 || lines[0] != " slow 5400 7200 10k  15k  SSD  NVMe fast" {
		t.Fatalf("scale %q", scale)
	}
	rule := []rune(lines[1])
	if len(rule) != 40 || rule[bandCol(69)] != '▲' || bandCol(69) < 5 || bandCol(69) >= 10 {
		t.Errorf("69 MB/s isn't marked in the 5400 band: %q", lines[1])
	}
	// SATA 3.0 Gb/s (about 276 MB/s) is slower than USB 3, so it's the wall.
	if w := bandCol(276); rule[w] != '┤' || rule[w+1] != '░' || rule[w-1] != '─' {
		t.Errorf("no wall at 276 MB/s: %q", lines[1])
	}
	if guess != "Best guess: a 7,200 rpm disk, from a peak of 129 MB/s.  SMART says 7,200 rpm." {
		t.Errorf("guess %q", guess)
	}
	if !strings.HasPrefix(limit, "Link: SATA 3.0 Gb/s, about 276 MB/s · USB 3 (5 Gb/s), about 450 MB/s.") {
		t.Errorf("limit %q", limit)
	}

	// USB 2 caps a drive below what it can do: no guess, and say why.
	usb2 := &model.Drive{Bridge: model.Bridge{Speed: 480}, SMART: &smart.Info{}}
	if _, g, _ := Band(&model.Job{MBps: 38, WriteMBps: []uint16{38}}, usb2); !strings.Contains(g, "can't tell") || !strings.Contains(g, "USB 2") || !strings.Contains(g, "solid state") {
		t.Errorf("USB 2 guess %q", g)
	}
	// Nothing measured and no links: a plain scale, with no mark.
	scale, g, l := Band(&model.Job{}, nil)
	if strings.ContainsAny(scale, "▲┤░") || !strings.Contains(g, "first block") || !strings.Contains(l, "not reported") {
		t.Errorf("empty: %q %q %q", scale, g, l)
	}
	for _, c := range []float64{0.5, 59, 60, 119, 120, 329, 330, 699, 700, 7499, 7500, 50000} {
		if col := bandCol(c); col/slot != bandOf(c) {
			t.Errorf("%v MB/s at column %d, outside band %d", c, col, bandOf(c))
		}
	}
}

func TestSigIgnoresCPU(t *testing.T) {
	b := hub.Bay{Snap: model.Snapshot{State: model.Running, Job: &model.Job{CPU: 20, HostCPU: 5, Cores: 4, Load: 1}}}
	before := Sig(b)
	b.Snap.Job = &model.Job{CPU: 31, HostCPU: 9, Cores: 4, Load: 2}
	if Sig(b) != before {
		t.Error("a CPU sample redraws the page")
	}
}

func TestLeft(t *testing.T) {
	for d, want := range map[time.Duration]string{
		20 * time.Second:                "under a minute left",
		12*time.Minute + 20*time.Second: "about 12 min left",
		2 * time.Hour:                   "about 2 h left",
		28*time.Hour + 14*time.Minute:   "about 28 h 14 min left",
	} {
		if got := left(d); got != want {
			t.Errorf("left(%v) = %q, want %q", d, got, want)
		}
	}
}

// A bay page names both halves' builds, and the wall says so when they
// differ: an install leaves a running bayd@ alone, so a bay can come out of
// it with a new link and the old disk side.
func TestBuilds(t *testing.T) {
	f, h, drv := setup(t)
	const page = "/bays/lab-a/bay1"
	set := func(software, link string) {
		f.set("lab-a/bay1", func(b *hub.Bay) {
			b.Version = link
			b.Snap = model.Snapshot{State: model.Present, Session: session, Drive: drv, Software: software}
		})
	}

	set("bayd 0.2.0 (842215c)", "0.2.0 (842215c)")
	wants(t, "same", get(h, page).Body.String(), "bayd 0.2.0 (842215c) · link 0.2.0 (842215c)")
	for _, path := range []string{page, "/"} {
		if strings.Contains(get(h, path).Body.String(), "different builds") {
			t.Errorf("%s: a skew for matching builds", path)
		}
	}

	set(linkpkg.OldDisk, "0.2.0 (842215c)")
	wants(t, "skew page", get(h, page).Body.String(), linkpkg.OldDisk, "⚠ bayd and its link are different builds.", "systemctl restart bayd@bay1")
	wants(t, "skew tile", get(h, "/").Body.String(), `class="tile-skew"`)

	f.set("lab-a/bay1", func(b *hub.Bay) { b.Snap = model.Snapshot{State: model.Empty} })
	wants(t, "empty", get(h, page).Body.String(), "bayd isn't running; it starts when a drive goes in")
}

// A drive back in a bay says it's been through before, and whether it was
// wiped, so nobody wipes a wiped drive twice without knowing.
func TestBayHistory(t *testing.T) {
	f, h, drv := setup(t)
	const page = "/bays/lab-a/bay1"
	wants(t, "back again", get(h, page).Body.String(), "<strong>Wiped and verified</strong>", "1 earlier record →", `href="/drives/9VPAX9NA"`)

	other := *drv
	si := *drv.SMART
	si.Serial = "NEWDRIVE01"
	other.SMART = &si
	f.set("lab-a/bay1", func(b *hub.Bay) { b.Snap.Drive, b.Snap.RecordID = &other, "" })
	wants(t, "new", get(h, page).Body.String(), "First time through the bays.")
}
