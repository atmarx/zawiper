package web

import (
	"context"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/proto"
	"zawiper/internal/records"
	"zawiper/internal/sign"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
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
	w := wipeRecord(rec, info)
	if err := store.SaveWipe(w, map[string][]byte{"smart-before.txt": []byte("before")}); err != nil {
		t.Fatal(err)
	}
	f := &fakeBayd{snap: model.Snapshot{State: model.Present, Bay: "the bay", Drive: drv, RecordID: rec.ID}}
	s := &Server{Bayd: f, Store: store, Hosts: []string{host}, Version: "test"}
	return f, s.Handler()
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

func TestPages(t *testing.T) {
	_, h := setup(t)
	for path, wants := range map[string][]string{
		"/":                {"ST31000528AS", "9VPA", "X9NA", "PASS WITH NOTES", "312.00 GB used", "Looks like a backup", "Re-read", "Recently in the bay"},
		"/fragment/bay":    {"PRESENT", "This read's record"},
		"/drives":          {"9VPAX9NA", "1 drive on record"},
		"/drives?q=x9n":    {"9VPAX9NA"},
		"/drives/9VPAX9NA": {"2026-10-23T14-02-11Z-inspect", "inspect"},
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect":               {"DRIVE RECORD", "Airflow_Temperature_Cel", "5 000c50 02d635ade", "Nothing was written", "PASSED"},
		"/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/record.md":     {"# 9VPAX9NA"},
		"/drives/9VPAX9NA/2026-10-23T15-02-11Z-wipe":                  {"Wiped, verified", "Wipe: verified", "1,000,204,886,016 of 1,000,204,886,016", "██▒█", "#401", "Found before the wipe", "smart-before.txt", "Every byte", "The key is published", "/static/certificate.css"},
		"/drives/9VPAX9NA/2026-10-23T15-02-11Z-wipe/record.md":        {"## Certificate of sanitization", "| Level | **Clear** |", "## Check it yourself", "d198657ce190af6f5093548e428b3b28", "openssl enc -aes-256-ctr", "Not signed yet"},
		"/static/certificate.css":                                     {"@page za-sheet", "size: letter"},
		"/drives/9VPAX9NA/2026-10-23T15-02-11Z-wipe/smart-before.txt": {"before"},
		"/static/zawiper.css":                                         {"--hazard"},
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
	rec := &records.Record{Serial: info.Serial, Started: t0, Finished: t0.Add(5 * time.Hour), Host: "dock-host", Bay: "bay1", Software: "bayd 0.2.0 (ddc40cb)",
		Drive: model.Drive{Size: info.CapacityBytes, LogicalSector: 512, PhysicalSector: 512, SMART: info,
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
	_, h := setup(t)
	body := get(h, "/drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect").Body.String()
	if strings.Contains(body, "sheet-preview") || strings.Contains(body, "Certificate of") {
		t.Error("an inspection got a certificate")
	}
	if c := records.NewCertificate(&records.Record{Kind: "inspect"}, nil); c != nil {
		t.Error("NewCertificate for an inspection")
	}
}
