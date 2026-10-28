package records

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

func record(t *testing.T, started time.Time) *Record {
	t.Helper()
	js, err := os.ReadFile("../smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	info, err := smart.Parse(js)
	if err != nil {
		t.Fatal(err)
	}
	return &Record{
		Serial: info.Serial, Started: started, Finished: started.Add(5 * time.Second),
		Host: "lab-a", Bay: "the bay", Software: "test",
		Drive: model.Drive{Size: info.CapacityBytes, SMART: info, Table: model.Table{Parts: []model.Part{}},
			Capacity: &model.Capacity{Method: "hdparm -N; no DCO", Visible: info.CapacityBytes / 512, Native: info.CapacityBytes / 512}},
		Health: smart.Assess(info),
	}
}

func TestSaveAndList(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	t0 := time.Date(2026, 10, 23, 14, 2, 11, 0, time.UTC)

	r := record(t, t0)
	if err := s.SaveInspect(r, []byte("{}"), []byte("text")); err != nil {
		t.Fatal(err)
	}
	if r.ID != "2026-10-23T14-02-11Z-inspect" {
		t.Errorf("id %q", r.ID)
	}
	// Re-reading the same insertion overwrites, not appends.
	if err := s.SaveInspect(record(t, t0), nil, nil); err != nil {
		t.Fatal(err)
	}
	// A new insertion is a new record.
	if err := s.SaveInspect(record(t, t0.Add(time.Hour)), nil, nil); err != nil {
		t.Fatal(err)
	}

	drives, err := s.Drives()
	if err != nil || len(drives) != 1 {
		t.Fatalf("drives %v %v", drives, err)
	}
	d := drives[0]
	if d.Serial != "9VPAX9NA" || d.Records != 2 || d.LastGrade != smart.Notes || !d.FirstSeen.Equal(t0) {
		t.Errorf("summary %+v", d)
	}
	ids, _ := s.Records("9VPAX9NA")
	if len(ids) != 2 || ids[0] != "2026-10-23T15-02-11Z-inspect" {
		t.Errorf("ids %v", ids)
	}
	got, err := s.Record("9VPAX9NA", ids[1])
	if err != nil || got.Drive.SMART.Serial != "9VPAX9NA" {
		t.Errorf("record %v %v", got, err)
	}
	md, _ := os.ReadFile(s.Path("9VPAX9NA", ids[1], "record.md"))
	for _, want := range []string{"# 9VPAX9NA", "PASS WITH NOTES", "Airflow_Temperature_Cel", "Looks blank already", "Nothing was written"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("record.md lacks %q", want)
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(s.Root, "drives/*/*/*.tmp")); len(matches) > 0 {
		t.Errorf("leftover temp files %v", matches)
	}
}

func TestEmptyStore(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	if d, err := s.Drives(); err != nil || len(d) != 0 {
		t.Errorf("%v %v", d, err)
	}
}

// Serials come from drive firmware: untrusted.
func TestUnsafeNames(t *testing.T) {
	if DirName("../../etc") != "_.._etc" {
		t.Errorf("DirName %q", DirName("../../etc"))
	}
	if DirName("  WD-WCC4 E1 ") != "WD-WCC4_E1" {
		t.Errorf("DirName %q", DirName("  WD-WCC4 E1 "))
	}
	s := &Store{Root: "/srv/zawiper"}
	for _, c := range [][3]string{
		{"..", "x", "record.md"},
		{"ok", "..", "record.md"},
		{"ok", "x", "../../etc/passwd"},
		{"ok", "x", "drive.json"},
		{".hidden", "x", "record.md"},
	} {
		if p := s.Path(c[0], c[1], c[2]); !strings.HasSuffix(p, "nonexistent") {
			t.Errorf("%v → %s", c, p)
		}
	}
}

func TestFoundLine(t *testing.T) {
	d := &model.Drive{Table: model.Table{Type: "gpt", Parts: []model.Part{
		{FSType: "ext4", Used: 312e9}, {FSType: "ntfs", Used: 88e9},
	}}}
	if got := FoundLine(d); got != "GPT · 2 partitions · 400.00 GB of files." {
		t.Errorf("%q", got)
	}
	d.Table.Parts[1].Used = -1
	if got := FoundLine(d); !strings.Contains(got, "at least 312.00 GB") {
		t.Errorf("%q", got)
	}
}

func TestFoundLineBareDisk(t *testing.T) {
	d := &model.Drive{Table: model.Table{Parts: []model.Part{{FSType: "exfat", Used: 0}}}}
	if got := FoundLine(d); got != "No partition table · a filesystem on the bare disk · no file data." {
		t.Errorf("%q", got)
	}
	if Commas(11196) != "11,196" || Commas(7) != "7" || Commas(1000000) != "1,000,000" {
		t.Error("Commas")
	}
}

func TestSaveWipe(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	r := record(t, time.Date(2026, 10, 24, 15, 0, 0, 0, time.UTC))
	w := &wipe.Result{Method: "test", Size: 3 << 30, Block: 1 << 30, Written: 3 << 30, Checked: 3 << 30,
		Started: r.Started, Wrote: r.Started.Add(2 * time.Hour), Verified: r.Started.Add(4 * time.Hour),
		Cells: []wipe.Cell{{Write: 9, Read: 8}, {Write: 9, Read: 8}, {Write: 9, Read: 8}}}
	r.Wipe, r.Before, r.Wrote = w, r.Drive.SMART, true
	if err := s.SaveWipe(r, map[string][]byte{"smart-before.txt": []byte("x"), "smart.txt": []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if r.ID != "2026-10-24T15-00-00Z-wipe" {
		t.Errorf("id %q", r.ID)
	}
	md, _ := os.ReadFile(s.Path("9VPAX9NA", r.ID, "record.md"))
	for _, want := range []string{"Wiped, verified", "## Wipe: verified", "███", "Found before the wipe", "Every byte", "2:00:00"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("record.md lacks %q", want)
		}
	}
	if strings.Contains(string(md), "Nothing was written") {
		t.Error("a wipe record says nothing was written")
	}
	if err := s.SaveWipe(r, map[string][]byte{"../evil": nil}); err == nil {
		t.Error("saved a file outside the whitelist")
	}
}

// The certificate claims Clear only for a whole, clean overwrite of a
// spinning disk, and never claims Purge.
func TestSanitize(t *testing.T) {
	clean := func() *Record {
		r := record(t, time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
		size := r.Drive.Size
		r.Wipe = &wipe.Result{Size: size, Block: 1 << 30, Written: size, Checked: size, Cells: []wipe.Cell{{Write: 1, Read: 1}},
			Key: strings.Repeat("ab", 32), IV: strings.Repeat("cd", 16)}
		return r
	}
	if s := Sanitize(clean()); s.Level != "Clear" || s.Why != "" || !strings.Contains(s.Standard, "NIST SP 800-88 Rev. 2") ||
		!strings.Contains(s.Standard, "§8.4.2.2") || !strings.Contains(s.Technique, "No HPA or DCO") {
		t.Errorf("clean wipe: %+v", s)
	}
	cases := map[string]func(*Record){
		"stopped":    func(r *Record) { r.Wipe.Aborted = "stopped during the verify pass" },
		"bad bytes":  func(r *Record) { r.Wipe.Bad, r.Wipe.Checked = 512, r.Wipe.Checked-512 },
		"short":      func(r *Record) { r.Wipe.Checked -= 4096 },
		"wrong size": func(r *Record) { r.Drive.Size += 1 << 20 },
		"flash":      func(r *Record) { r.Drive.SMART.RotationRate = 0 },
		"no SMART":   func(r *Record) { r.Drive.SMART = nil },
		"inspection": func(r *Record) { r.Wipe = nil },
		"HPA":        func(r *Record) { r.Drive.Capacity.Visible -= 1 << 20 },
		"unchecked":  func(r *Record) { r.Drive.Capacity = &model.Capacity{Note: "hdparm -N couldn't read it"} },
		"no check":   func(r *Record) { r.Drive.Capacity = nil },
	}
	for name, mod := range cases {
		r := clean()
		mod(r)
		s := Sanitize(r)
		if s.Level != "" || s.Why == "" {
			t.Errorf("%s: claimed %q (%s)", name, s.Level, s.Why)
		}
		if c := NewCertificate(r, nil); c != nil && (c.ForSale || c.Verdict != "NOT FOR SALE") {
			t.Errorf("%s: for sale", name)
		}
	}
	for _, r := range []*Record{clean(), func() *Record { r := clean(); r.Wipe.Bad = 1; return r }()} {
		s := Sanitize(r)
		if strings.Contains(s.Level+s.Technique+s.Verify+s.Result, "Purge") {
			t.Errorf("claims Purge: %+v", s)
		}
	}

	r := clean()
	md := Markdown(r)
	for _, want := range []string{"## Certificate of sanitization", "| Level | **Clear** |", "### Health before and after",
		"## Check it yourself", "| IV | `" + strings.Repeat("cd", 16) + "` |", "cmp -n 1000204886016 - /dev/sdX", "Not signed yet", "The key is published"} {
		if !strings.Contains(md, want) {
			t.Errorf("record.md lacks %q", want)
		}
	}
	if strings.Contains(md, "existed only in memory") {
		t.Error("a published-key wipe says its key existed only in memory")
	}
	// The first real wipe kept its key in memory; its record says so.
	r.Wipe.Key, r.Wipe.IV = "", ""
	if md := Markdown(r); !strings.Contains(md, "existed only in memory") || !strings.Contains(md, "before Za Wiper published its keys") {
		t.Error("an old wipe without a key")
	}
}

func TestSurfaceSVG(t *testing.T) {
	w := &wipe.Result{Block: 1 << 30, Cells: make([]wipe.Cell, 130)}
	for i := range w.Cells {
		w.Cells[i] = wipe.Cell{Write: 1, Read: 1}
	}
	w.Cells[70].Bad = 9
	w.Cells[129].Read = 0
	svg, legend := SurfaceSVG(w)
	s := string(svg)
	for _, want := range []string{`viewBox="0 0 64 3"`, `<rect class="m-v" x="0" y="0" width="64" height="1"/>`, `<rect class="m-b" x="6" y="1" width="1" height="1"/>`, `class="m-t"`} {
		if !strings.Contains(s, want) {
			t.Errorf("svg lacks %q", want)
		}
	}
	if legend[0].Count != 128 || legend[2].Count != 1 || legend[3].Count != 1 {
		t.Errorf("legend %+v", legend)
	}
}
