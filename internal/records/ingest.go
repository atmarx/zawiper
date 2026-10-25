package records

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Ingest files a record folder a bay uploaded from its spool.  serial is the
// folder name (DirName of the drive's serial) and id the record's folder.
// Everything in it is checked before anything is written: only names in
// Files, and a record.json that says it's this serial's record with this ID.
//
// Ingesting the same record twice is fine.  A bay keeps a record until the
// hub says it's stored, and that message can get lost.
func (s *Store) Ingest(serial, id string, files map[string][]byte) error {
	r, err := checkUpload(serial, id, files)
	if err != nil {
		return err
	}
	dir := filepath.Join(s.drives(), serial, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		if err := writeAtomic(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	return s.updateSummary(r)
}

// checkUpload refuses an upload that names files we don't keep, unsafe
// folders, or a record.json that belongs somewhere else.
func checkUpload(serial, id string, files map[string][]byte) (*Record, error) {
	if !Valid(serial) || !Valid(id) {
		return nil, fmt.Errorf("%q/%q can't name a record folder", serial, id)
	}
	for name := range files {
		if _, ok := Files[name]; !ok {
			return nil, fmt.Errorf("%s isn't a record file", name)
		}
	}
	js, ok := files["record.json"]
	if !ok {
		return nil, errors.New("the upload has no record.json")
	}
	var r Record
	if err := json.Unmarshal(js, &r); err != nil {
		return nil, fmt.Errorf("record.json doesn't parse: %w", err)
	}
	if r.Serial == "" {
		return nil, errors.New("record.json has no serial")
	}
	if DirName(r.Serial) != serial || r.ID != id {
		return nil, fmt.Errorf("record.json is %s/%s, but it was sent as %s/%s", DirName(r.Serial), r.ID, serial, id)
	}
	return &r, nil
}
