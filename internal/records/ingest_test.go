package records

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upload reads a record folder the way a bay's link sends it.
func upload(t *testing.T, s *Store, serial, id string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for name := range Files {
		if b, err := os.ReadFile(s.Path(serial, id, name)); err == nil {
			files[name] = b
		}
	}
	return files
}

func TestIngest(t *testing.T) {
	spool := &Store{Root: t.TempDir()}
	r := record(t, time.Date(2026, 10, 24, 9, 15, 40, 0, time.UTC))
	if err := spool.SaveInspect(r, []byte("{}"), []byte("raw")); err != nil {
		t.Fatal(err)
	}
	files := upload(t, spool, "9VPAX9NA", r.ID)

	hub := &Store{Root: t.TempDir()}
	if err := hub.Ingest("9VPAX9NA", r.ID, files); err != nil {
		t.Fatal(err)
	}
	// A lost stored frame means the same record comes again.
	if err := hub.Ingest("9VPAX9NA", r.ID, files); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	got, err := hub.Record("9VPAX9NA", r.ID)
	if err != nil || got.Serial != "9VPAX9NA" {
		t.Fatalf("record %v %v", got, err)
	}
	if b, _ := os.ReadFile(hub.Path("9VPAX9NA", r.ID, "smart.txt")); string(b) != "raw" {
		t.Errorf("smart.txt %q", b)
	}
	sum, err := hub.Summary("9VPAX9NA")
	if err != nil || sum.Records != 1 || sum.LastKind != "inspect" {
		t.Errorf("summary %+v %v", sum, err)
	}
	if matches, _ := filepath.Glob(filepath.Join(hub.Root, "drives/*/*/*.tmp")); len(matches) > 0 {
		t.Errorf("leftover temp files %v", matches)
	}
}

func TestIngestRefuses(t *testing.T) {
	spool := &Store{Root: t.TempDir()}
	r := record(t, time.Date(2026, 10, 24, 9, 15, 40, 0, time.UTC))
	if err := spool.SaveInspect(r, nil, nil); err != nil {
		t.Fatal(err)
	}
	good := upload(t, spool, "9VPAX9NA", r.ID)
	with := func(name string, b []byte) map[string][]byte {
		m := map[string][]byte{}
		for k, v := range good {
			m[k] = v
		}
		m[name] = b
		return m
	}
	without := func(name string) map[string][]byte {
		m := with("x", nil)
		delete(m, "x")
		delete(m, name)
		return m
	}
	hub := &Store{Root: t.TempDir()}
	for _, c := range []struct {
		why, serial, id string
		files           map[string][]byte
	}{
		{"a name outside Files", "9VPAX9NA", r.ID, with("../evil", nil)},
		{"drive.json", "9VPAX9NA", r.ID, with("drive.json", []byte("{}"))},
		{"no record.json", "9VPAX9NA", r.ID, without("record.json")},
		{"bad JSON", "9VPAX9NA", r.ID, with("record.json", []byte("{"))},
		{"no serial", "_", r.ID, with("record.json", []byte(`{"id":"`+r.ID+`"}`))},
		{"another serial", "OTHER", r.ID, good},
		{"another id", "9VPAX9NA", "2026-01-01T00-00-00Z-wipe", good},
		{"an unsafe serial", "..", r.ID, good},
		{"an unsafe id", "9VPAX9NA", "../x", good},
	} {
		if err := hub.Ingest(c.serial, c.id, c.files); err == nil {
			t.Errorf("%s: accepted", c.why)
		} else if strings.Contains(err.Error(), "\n") {
			t.Errorf("%s: %q", c.why, err)
		}
	}
	if d, _ := hub.Drives(); len(d) != 0 {
		t.Errorf("a refused upload left %v", d)
	}
}
