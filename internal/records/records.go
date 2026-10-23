// Package records keeps one folder per drive serial, one folder per job
// inside it.  The files are the truth; the drive list is a scan of them.
//
//	drives/9VPAX9NA/drive.json
//	drives/9VPAX9NA/2026-10-23T14-02-11Z-inspect/{record.json,record.md,smart.json,smart.txt}
package records

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/smart"
)

const Schema = 1

// Record is one job on one drive.  Phase 1 only files inspections.
type Record struct {
	Schema   int          `json:"schema"`
	ID       string       `json:"id"`
	Kind     string       `json:"kind"` // inspect
	Serial   string       `json:"serial"`
	Started  time.Time    `json:"started"` // when the drive went in
	Finished time.Time    `json:"finished"`
	Host     string       `json:"host"`
	Bay      string       `json:"bay"`
	Software string       `json:"software"`
	Drive    model.Drive  `json:"drive"`
	Health   smart.Health `json:"health"`
	Refusals []string     `json:"refusals,omitempty"`
	Wrote    bool         `json:"wrote"` // anything written to the drive
}

// Summary is drive.json: enough for the list without opening every record.
type Summary struct {
	Serial    string      `json:"serial"`
	Model     string      `json:"model"`
	Family    string      `json:"family,omitempty"`
	Capacity  int64       `json:"capacity"`
	FirstSeen time.Time   `json:"first_seen"`
	LastSeen  time.Time   `json:"last_seen"`
	LastKind  string      `json:"last_kind"`
	LastGrade smart.Grade `json:"last_grade"`
	Records   int         `json:"records"`
}

// Files a record folder may hold, and the only names zaweb will serve.
var Files = map[string]string{
	"record.md":   "text/markdown; charset=utf-8",
	"record.json": "application/json",
	"smart.json":  "application/json",
	"smart.txt":   "text/plain; charset=utf-8",
}

type Store struct{ Root string }

func (s *Store) drives() string { return filepath.Join(s.Root, "drives") }

var safe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// DirName makes a serial safe to use as a folder name.  Serials are what the
// drive firmware says, so they're untrusted input like everything else.
func DirName(serial string) string {
	n := safe.ReplaceAllString(strings.TrimSpace(serial), "_")
	n = strings.TrimLeft(n, ".")
	if n == "" {
		n = "_"
	}
	return n
}

var validName = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)

// Valid says whether a path element from a URL can name a serial or record.
func Valid(name string) bool { return len(name) <= 128 && validName.MatchString(name) }

// RecordID names a record folder by the start of its session.  Re-reading
// the same insertion lands in the same folder.
func RecordID(started time.Time, kind string) string {
	return started.UTC().Format("2006-01-02T15-04-05Z") + "-" + kind
}

// SaveInspect files what a read saw.  smartJSON and smartText are smartctl's
// raw output, kept beside the record.
func (s *Store) SaveInspect(r *Record, smartJSON, smartText []byte) error {
	r.Schema = Schema
	r.Kind = "inspect"
	r.ID = RecordID(r.Started, r.Kind)
	dir := filepath.Join(s.drives(), DirName(r.Serial), r.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	js, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	for name, b := range map[string][]byte{
		"record.json": append(js, '\n'),
		"record.md":   []byte(Markdown(r)),
		"smart.json":  smartJSON,
		"smart.txt":   smartText,
	} {
		if err := writeAtomic(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	return s.updateSummary(r)
}

func (s *Store) updateSummary(r *Record) error {
	path := filepath.Join(s.drives(), DirName(r.Serial), "drive.json")
	var sum Summary
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &sum)
	}
	if sum.FirstSeen.IsZero() || r.Started.Before(sum.FirstSeen) {
		sum.FirstSeen = r.Started
	}
	sum.Serial = r.Serial
	if r.Drive.SMART != nil {
		sum.Model, sum.Family, sum.Capacity = r.Drive.SMART.Model, r.Drive.SMART.Family, r.Drive.SMART.CapacityBytes
	}
	sum.LastSeen, sum.LastKind, sum.LastGrade = r.Finished, r.Kind, r.Health.Grade
	recs, _ := s.Records(r.Serial)
	sum.Records = len(recs)
	b, _ := json.MarshalIndent(sum, "", "  ")
	return writeAtomic(path, append(b, '\n'))
}

// Drives lists every drive ever seen, most recent first.
func (s *Store) Drives() ([]Summary, error) {
	ents, err := os.ReadDir(s.drives())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(s.drives(), e.Name(), "drive.json"))
		if err != nil {
			continue
		}
		var sum Summary
		if json.Unmarshal(b, &sum) == nil {
			out = append(out, sum)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

// Summary is one drive's drive.json.
func (s *Store) Summary(serial string) (*Summary, error) {
	if !Valid(serial) {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(filepath.Join(s.drives(), serial, "drive.json"))
	if err != nil {
		return nil, err
	}
	var sum Summary
	return &sum, json.Unmarshal(b, &sum)
}

// Records lists one drive's record IDs, newest first.
func (s *Store) Records(serial string) ([]string, error) {
	dir := filepath.Join(s.drives(), DirName(serial))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "record.json")); err == nil {
				ids = append(ids, e.Name())
			}
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

func (s *Store) Record(serial, id string) (*Record, error) {
	b, err := os.ReadFile(s.Path(serial, id, "record.json"))
	if err != nil {
		return nil, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", serial, id, err)
	}
	return &r, nil
}

// Path is where a record's file lives.  Unknown names and unsafe path
// elements resolve to a path that doesn't exist.
func (s *Store) Path(serial, id, file string) string {
	if _, ok := Files[file]; !ok || !Valid(serial) || !Valid(id) {
		return filepath.Join(s.Root, "nonexistent")
	}
	return filepath.Join(s.drives(), serial, id, file)
}

func writeAtomic(path string, b []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
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
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
