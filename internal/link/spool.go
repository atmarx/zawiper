package link

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zawiper/internal/proto"
	"zawiper/internal/records"
)

// settle is how old a record.json must be before it's sent.  A record
// folder fills one file at a time, in no fixed order.
const settle = 2 * time.Second

// spooled is one finished record folder: spool/drives/<serial>/<id>/.
type spooled struct{ serial, id, dir string }

func (s spooled) key() string { return s.serial + "/" + s.id }

// spooled lists record folders that are ready to send.
func (l *Link) spooled() []spooled {
	matches, _ := filepath.Glob(filepath.Join(l.Spool, "drives", "*", "*", "record.json"))
	var out []spooled
	for _, m := range matches {
		dir := filepath.Dir(m)
		r := spooled{serial: filepath.Base(filepath.Dir(dir)), id: filepath.Base(dir), dir: dir}
		if !records.Valid(r.serial) || !records.Valid(r.id) {
			log.Printf("spool: %s can't be a record folder; left alone", dir)
			continue
		}
		if ready(dir, m) {
			out = append(out, r)
		}
	}
	return out
}

// ready says a folder has stopped changing: record.json has settled and no
// file is half written.
func ready(dir, recordJSON string) bool {
	fi, err := os.Stat(recordJSON)
	if err != nil || time.Since(fi.ModTime()) < settle {
		return false
	}
	tmps, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	return len(tmps) == 0
}

// readRecord gathers the files the hub keeps, and only those.
func readRecord(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for name := range records.Files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files[name] = b
	}
	return files, nil
}

// upload sends every ready record not already sent on this connection.
func (s *session) upload() {
	for _, r := range s.l.spooled() {
		if _, sent := s.pending[r.key()]; sent {
			continue
		}
		files, err := readRecord(r.dir)
		if err != nil {
			log.Printf("spool: %s: %v", r.dir, err)
			continue
		}
		up := &proto.RecordUpload{Serial: r.serial, ID: r.id, Files: files}
		if err := s.w.send(proto.Frame{Type: proto.FrameRecord, ID: r.key(), Record: up}); err != nil {
			log.Printf("spool: sending %s: %v", r.key(), err)
			return
		}
		s.pending[r.key()] = r.dir
		log.Printf("spool: sent record %s", r.key())
	}
}

// stored drops a record the hub has filed.  Only a folder this session sent
// can be dropped; an ID the link didn't send names nothing.
func (s *session) stored(id string) {
	dir, ok := s.pending[id]
	if !ok {
		log.Printf("the hub says it stored %q, which this link didn't send; ignored", id)
		return
	}
	delete(s.pending, id)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("spool: the hub has %s, but it can't be dropped here: %v", id, err)
		return
	}
	log.Printf("spool: the hub has %s; dropped", id)
	tidy(filepath.Dir(dir))
}

// tidy removes a drive's spool folder once its last record has gone.
func tidy(driveDir string) {
	ents, err := os.ReadDir(driveDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "drive.json") {
			return
		}
	}
	for _, e := range ents {
		os.Remove(filepath.Join(driveDir, e.Name()))
	}
	os.Remove(driveDir)
}
