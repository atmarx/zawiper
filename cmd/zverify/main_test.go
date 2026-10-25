package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/records"
	"zawiper/internal/sign"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

// A "drive" that isn't a whole number of MiB, so the last sample isn't
// aligned with the first.
const size = 5*mib + 3*sector

type fixture struct {
	dir, drive, record, pub string
	key                     ed25519.PrivateKey
}

// wiped makes a file drive, wipes it with the real engine in small blocks,
// and files a signed record for it the way the hub would.
func wiped(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	fx := &fixture{dir: dir, drive: filepath.Join(dir, "drive"), record: filepath.Join(dir, "record.json"), pub: filepath.Join(dir, "za-wiper.pub")}
	f, err := os.Create(fx.drive)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(size)
	f.Close()
	res, err := (&wipe.Job{Path: fx.drive, Size: size, Block: mib, Buf: 256 << 10}).Run(context.Background())
	if err != nil || !res.OK() {
		t.Fatalf("wipe: %v %+v", err, res)
	}
	js, err := os.ReadFile("../../internal/smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := smart.Parse(js)
	r := records.Record{Schema: records.Schema, ID: "2026-10-24T09-15-40Z-wipe", Kind: "wipe", Serial: info.Serial,
		Started: res.Started, Finished: res.Verified, Host: "test", Bay: "test", Software: "test",
		Drive: model.Drive{Size: size, SMART: info}, Health: smart.Assess(info), Wrote: true, Wipe: res}
	b, _ := json.MarshalIndent(r, "", "  ")
	os.WriteFile(fx.record, b, 0o644)

	keyPath := filepath.Join(dir, "za-wiper")
	if _, err := sign.GenerateKey(keyPath); err != nil {
		t.Fatal(err)
	}
	fx.key, _ = sign.LoadKey(keyPath)
	os.WriteFile(fx.record+".sig", sign.Sign(fx.key, b), 0o644)
	// The buyer's copy of the drive is read-only to them, and zverify
	// must manage with that.
	os.Chmod(fx.drive, 0o400)
	return fx
}

func zverify(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String() + errw.String()
}

func (fx *fixture) damage(t *testing.T, off int64, b []byte) {
	t.Helper()
	os.Chmod(fx.drive, 0o600)
	f, err := os.OpenFile(fx.drive, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt(b, off)
	f.Close()
	os.Chmod(fx.drive, 0o400)
}

func contains(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

func TestCleanDrive(t *testing.T) {
	fx := wiped(t)
	code, out := zverify(t, "-pubkey", fx.pub, fx.record, fx.drive)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Signature: good", "serial 9VPAX9NA", "the same as the record", "would cover the whole drive", "Every byte matches the keystream", "says 9VPAX9NA")

	code, out = zverify(t, "-pubkey", fx.pub, "-sample", "3", fx.record, fx.drive)
	if code != 0 {
		t.Fatalf("-sample 3 exit %d:\n%s", code, out)
	}
	contains(t, out, "Checking 3 samples", "All 3 samples match the keystream: 3,145,728 bytes")

	code, out = zverify(t, "-no-sig", "-full", fx.record, fx.drive)
	if code != 0 {
		t.Fatalf("-full exit %d:\n%s", code, out)
	}
	contains(t, out, "not checked (-no-sig)", "Every byte matches the keystream: all 5,244,416 of them")
}

func TestTamperedDrive(t *testing.T) {
	fx := wiped(t)
	// Someone wrote a partition table, and a few KiB in the middle.
	fx.damage(t, 0, bytes.Repeat([]byte{0}, 512))
	fx.damage(t, 2*mib+4096, bytes.Repeat([]byte("X"), 8192))

	code, out := zverify(t, "-pubkey", fx.pub, "-full", fx.record, fx.drive)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "MISMATCH", "Bytes 0 to 511 (512 bytes) don't match", "Bytes 2,101,248 to 2,109,439 (8,192 bytes) don't match", "Something has written to this drive")

	// The first MiB is always in the sample, so a partition table can't
	// slip past even two samples.
	code, out = zverify(t, "-pubkey", fx.pub, "-sample", "2", fx.record, fx.drive)
	if code != 1 || !strings.Contains(out, "1 of 2 samples don't match") {
		t.Errorf("sampled, exit %d:\n%s", code, out)
	}
}

func TestLastMiBAlwaysSampled(t *testing.T) {
	fx := wiped(t)
	fx.damage(t, size-sector, []byte("the very end"))
	code, out := zverify(t, "-no-sig", "-sample", "2", fx.record, fx.drive)
	if code != 1 || !strings.Contains(out, "Bytes 5,243,904 to 5,244,415") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestTamperedRecord(t *testing.T) {
	fx := wiped(t)
	b, _ := os.ReadFile(fx.record)
	os.WriteFile(fx.record, bytes.Replace(b, []byte(`"PASS WITH NOTES"`), []byte(`"PASS"`), 1), 0o644)
	code, out := zverify(t, "-pubkey", fx.pub, fx.record, fx.drive)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Signature: BAD", "changed after signing", "Stopping here")
	if strings.Contains(out, "Device:") {
		t.Error("went on to the drive after a bad signature")
	}

	// A key the buyer didn't get from us.
	other := filepath.Join(fx.dir, "other")
	sign.GenerateKey(other)
	code, out = zverify(t, "-pubkey", other+".pub", fx.record, fx.drive)
	if code != 1 || !strings.Contains(out, "not by the key you gave") {
		t.Errorf("wrong key, exit %d:\n%s", code, out)
	}

	os.Remove(fx.record + ".sig")
	if code, out := zverify(t, "-pubkey", fx.pub, fx.record, fx.drive); code != 1 || !strings.Contains(out, "Signature: missing") {
		t.Errorf("no signature, exit %d:\n%s", code, out)
	}
}

func TestWrongDrive(t *testing.T) {
	fx := wiped(t)
	os.Chmod(fx.drive, 0o600)
	os.Truncate(fx.drive, size+mib)
	code, out := zverify(t, "-no-sig", fx.record, fx.drive)
	if code != 1 || !strings.Contains(out, "This isn't the drive the record is for") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

// A record that says its own wipe failed has nothing to check against.
func TestFailedWipeRecord(t *testing.T) {
	fx := wiped(t)
	var r records.Record
	b, _ := os.ReadFile(fx.record)
	json.Unmarshal(b, &r)
	r.Wipe.Bad, r.Wipe.Checked = 4096, r.Wipe.Checked-4096
	b, _ = json.Marshal(r)
	os.WriteFile(fx.record, b, 0o644)
	code, out := zverify(t, "-no-sig", fx.record, fx.drive)
	if code != 1 || !strings.Contains(out, "didn't read back clean") {
		t.Errorf("exit %d:\n%s", code, out)
	}
}

func TestFromTheHub(t *testing.T) {
	fx := wiped(t)
	mux := http.NewServeMux()
	for path, file := range map[string]string{
		"/za-wiper.pub": fx.pub,
		"/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe/record.json":     fx.record,
		"/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe/record.json.sig": fx.record + ".sig",
	} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, file) })
	}
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	old := client
	client = srv.Client()
	client.Timeout = 5 * time.Second
	defer func() { client = old }()

	code, out := zverify(t, "-pubkey", srv.URL+"/za-wiper.pub", srv.URL+"/drives/9VPAX9NA/2026-10-24T09-15-40Z-wipe/record.json", fx.drive)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "Signature: good", "matches the keystream")
	if code, out := zverify(t, "-no-sig", srv.URL+"/nope/record.json", fx.drive); code != 2 || !strings.Contains(out, "404") {
		t.Errorf("missing record, exit %d:\n%s", code, out)
	}
}

func TestUsage(t *testing.T) {
	fx := wiped(t)
	for name, args := range map[string][]string{
		"no args":         {},
		"no key choice":   {fx.record, fx.drive},
		"both key flags":  {"-no-sig", "-pubkey", fx.pub, fx.record, fx.drive},
		"sample and full": {"-no-sig", "-sample", "5", "-full", fx.record, fx.drive},
		"no such drive":   {"-no-sig", fx.record, filepath.Join(fx.dir, "nope")},
	} {
		if code, out := zverify(t, args...); code != 2 {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

func TestSampleSpans(t *testing.T) {
	s := sampleSpans(1<<40, 100)
	if len(s) != 100 || s[0][0] != 0 || s[99][0] != 1<<40-mib {
		t.Errorf("first %v last %v of %d", s[0], s[len(s)-1], len(s))
	}
	for i, x := range s {
		if x[1] != mib || x[0]%16 != 0 || (i > 0 && x[0] <= s[i-1][0]) {
			t.Errorf("span %d: %v", i, x)
		}
	}
	if s := sampleSpans(4096, 100); len(s) != 1 || s[0] != [2]int64{0, 4096} {
		t.Errorf("tiny drive: %v", s)
	}
}

// The default sample is IEEE 2883 §7.3's: two distinct blocks in each of
// 10,000 equal sections, plus the first and the last.
func TestStratifiedSpans(t *testing.T) {
	const size = 1_000_204_886_016
	s := stratifiedSpans(size)
	if len(s) < 2*sections || len(s) > 2*sections+2 || s[0] != [2]int64{0, block} || s[len(s)-1] != [2]int64{size - block, block} {
		t.Fatalf("%d spans, first %v, last %v", len(s), s[0], s[len(s)-1])
	}
	for i := 1; i < len(s); i++ {
		if s[i][0] <= s[i-1][0] {
			t.Fatalf("spans out of order or repeated at %d", i)
		}
	}
	blocks, j := int64(size/block), 0
	for i := int64(0); i < sections; i++ {
		lo, hi := blocks*i/sections*block, blocks*(i+1)/sections*block
		n := 0
		for ; j < len(s) && s[j][0] < hi; j++ {
			if s[j][0] >= lo {
				n++
			}
		}
		if n < 2 {
			t.Fatalf("section %d has %d blocks", i, n)
		}
	}
}
