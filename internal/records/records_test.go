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
		Host: "dock-host", Bay: "the bay", Software: "test",
		Drive:  model.Drive{Size: info.CapacityBytes, SMART: info, Table: model.Table{Parts: []model.Part{}}},
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
