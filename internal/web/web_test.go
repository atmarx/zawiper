package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/proto"
	"zawiper/internal/records"
	"zawiper/internal/smart"
)

const host = "zawiper.example.org"

type fakeBayd struct {
	snap    model.Snapshot
	rereads int
}

func (f *fakeBayd) Call(_ context.Context, v proto.Verb) (*model.Snapshot, error) {
	if v == proto.Reread {
		f.rereads++
	}
	s := f.snap
	return &s, nil
}

func setup(t *testing.T) (*fakeBayd, http.Handler) {
	t.Helper()
	js, err := os.ReadFile("../smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := smart.Parse(js)
	t0 := time.Date(2026, 10, 23, 14, 2, 11, 0, time.UTC)
	drv := &model.Drive{
		Dev: "/dev/sdc", Size: info.CapacityBytes, LogicalSector: 512, PhysicalSector: 512, ReadOnly: true,
		Bridge: model.Bridge{VendorID: "174c", ProductID: "1156", Manufacturer: "ASMedia"},
		SMART:  info, Health: smart.Assess(info),
		Table: model.Table{Type: "gpt", Parts: []model.Part{
			{Path: "/dev/sdc1", Size: 500e9, FSType: "ext4", Label: "backup-gamma", Used: 312e9, Backup: `labelled "backup-gamma"`},
			{Path: "/dev/sdc2", Size: 500e9, FSType: "ntfs", Used: -1, Peek: "couldn't mount it read-only"},
		}},
	}
	store := &records.Store{Root: t.TempDir()}
	rec := &records.Record{Serial: info.Serial, Started: t0, Finished: t0.Add(4 * time.Second), Host: "dock-host", Bay: "the bay", Software: "test", Drive: *drv, Health: drv.Health}
	if err := store.SaveInspect(rec, js, []byte("smartctl text")); err != nil {
		t.Fatal(err)
	}
	f := &fakeBayd{snap: model.Snapshot{State: model.Present, Bay: "the bay", Drive: drv, RecordID: rec.ID}}
	s := &Server{Bayd: f, Store: store, Hosts: []string{host}, Version: "test"}
	return f, s.Handler()
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestPages(t *testing.T) {
	_, h := setup(t)
	for path, wants := range map[string][]string{
		"/":                {"ST31000528AS", "9VPA", "X9NA", "PASS WITH NOTES", "312.00 GB used", "Looks like a backup", "Re-read", "Recently in the bay"},
		"/fragment/bay":    {"PRESENT", "This read's record"},
		"/drives":          {"9VPAX9NA", "1 drive on record"},
		"/drives?q=x9n":    {"9VPAX9NA"},
		"/drives/9VPAX9NA": {"2026-10-23T14-02-11Z-inspect", "inspect"},
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect":           {"DRIVE RECORD", "Airflow_Temperature_Cel", "5 000c50 02d635ade", "Nothing was written", "PASSED"},
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/record.md": {"# 9VPAX9NA"},
		"/static/zawiper.css":                                     {"--hazard"},
	} {
		w := get(h, path)
		if w.Code != 200 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
			continue
		}
		for _, want := range wants {
			if !strings.Contains(w.Body.String(), want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
	}
	if w := get(h, "/drives?q=nosuchdrive"); !strings.Contains(w.Body.String(), "hasn't been through the bay") {
		t.Error("empty search")
	}
	for _, path := range []string{
		"/drives/NOPE", "/drives/9VPAX9NA/nope",
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/drive.json",
		"/drives/9VPAX9NA/..%2f..%2fetc/record.md",
	} {
		if w := get(h, path); w.Code != 404 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}

func TestWrongHost(t *testing.T) {
	_, h := setup(t)
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "evil.example"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("rebinding: %d", w.Code)
	}
}

func TestRereadGuard(t *testing.T) {
	f, h := setup(t)
	post := func(mod func(*http.Request)) int {
		r := httptest.NewRequest("POST", "/api/bay/reread", strings.NewReader("{}"))
		r.Host = host
		r.Header.Set("Origin", "https://"+host)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Za-Wiper", "1")
		mod(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	refused := map[string]func(*http.Request){
		"no origin":    func(r *http.Request) { r.Header.Del("Origin") },
		"other origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"form post":    func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") },
		"no header":    func(r *http.Request) { r.Header.Del("X-Za-Wiper") },
		"null origin":  func(r *http.Request) { r.Header.Set("Origin", "null") },
	}
	for name, mod := range refused {
		if code := post(mod); code < 400 {
			t.Errorf("%s: %d", name, code)
		}
	}
	if f.rereads != 0 {
		t.Fatalf("a refused request reached bayd")
	}
	if code := post(func(*http.Request) {}); code != 200 || f.rereads != 1 {
		t.Errorf("good request: %d, rereads %d", code, f.rereads)
	}
	if code := post(func(*http.Request) {}); code != http.StatusTooManyRequests {
		t.Errorf("second re-read inside 2s: %d", code)
	}
	r := httptest.NewRequest("GET", "/api/bay/reread", nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET re-read: %d", w.Code)
	}
}

func TestEmptyAndReadingStates(t *testing.T) {
	f, h := setup(t)
	f.snap = model.Snapshot{State: model.Empty, Bay: "the bay"}
	if !strings.Contains(get(h, "/fragment/bay").Body.String(), "The viper is hungry") {
		t.Error("empty")
	}
	f.snap = model.Snapshot{State: model.Reading, Message: "Reading identity"}
	if !strings.Contains(get(h, "/fragment/bay").Body.String(), "Reading identity") {
		t.Error("reading")
	}
	f.snap = model.Snapshot{State: model.Present, Refusals: []string{"There are 2 drives"}, Devices: []string{"/dev/sdc", "/dev/sdd"}}
	if body := get(h, "/fragment/bay").Body.String(); !strings.Contains(body, "won't act") || !strings.Contains(body, "/dev/sdd") {
		t.Error("refusal without a drive")
	}
}
