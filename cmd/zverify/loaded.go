package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/atmarx/zawiper/internal/payload"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/wipe"
)

// verifyLoaded checks a Wipe + Load through its mounted filesystem: the stamp
// against the record, every file against the stamp, and the ballast against
// the keystream.  It tells a drive someone has used from a bad one.  A deleted
// ballast or an added file is the drive being used.  A file that doesn't match
// its hash is the failure.
func (v *verifier) verifyLoaded(r *records.Record, dir string) int {
	l, x := r.Load, r.Wipe
	fi, err := os.Stat(dir)
	if err != nil {
		fmt.Fprintf(v.errw, "Couldn't look at %s: %v\n", dir, err)
		return 2
	}
	if !fi.IsDir() {
		v.say("This drive was loaded after its wipe, so it's checked through its filesystem.  Mount it read-only and point me at the mount:")
		v.say("  mount -o ro %s1 /mnt/drive", dir)
		v.say("  zverify ... record.json /mnt/drive")
		return 2
	}
	v.say("Record: a wipe and load of serial %s, set %q, %s files.  Grade: %s.", r.Serial, l.Set, records.Commas(int64(l.Files)), r.Health.Grade)
	switch {
	case !x.OK():
		v.say("The record says the wipe didn't verify, so it was never loaded.")
		return 1
	case !l.OK():
		v.say("The record says the load didn't read back clean: %s of %s bytes.", records.Commas(l.Verified()), records.Commas(l.Bytes+l.Ballast))
		return 1
	}
	v.say("The record says %s bytes of files and ballast read back clean.", records.Commas(l.Verified()))
	v.say("The wipe under them was checked by sampling (IEEE 2883 §7.3).  The load overwrote most of that, so that part rests on the record.")

	js, err := os.ReadFile(filepath.Join(dir, payload.StampList))
	if err != nil {
		v.say("%s isn't on the drive (%v), so I don't know what should be.", payload.StampList, err)
		return 1
	}
	sets, err := payload.Parse(bytes.NewReader(js))
	if err != nil || len(sets) != 1 {
		v.say("%s doesn't parse: %v", payload.StampList, err)
		return 1
	}
	s := &sets[0]
	if h := s.Hash(); h != l.Hash {
		v.say("The stamp's list hashes to %s, and the record says %s.  This isn't the list that was loaded.", h, l.Hash)
		return 1
	}
	v.say("Stamp: matches the record.  Checking %s files, %s bytes.", records.Commas(int64(len(s.Files))), records.Commas(s.Bytes()))

	var bad, missing int
	listed := map[string]bool{payload.Ballast: true, payload.StampList: true, payload.StampText: true, payload.RecordFile: true}
	for _, f := range s.Files {
		listed[f.Path] = true
		got, n, err := hash(filepath.Join(dir, filepath.FromSlash(f.Path)))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			v.say("  missing: %s", f.Path)
			missing++
		case err != nil:
			v.say("  UNREADABLE: %s: %v", f.Path, err)
			bad++
		case n != f.Size || got != f.SHA256:
			v.say("  BAD: %s doesn't match its hash", f.Path)
			bad++
		}
	}

	ballastBad := v.checkBallast(r, dir)

	var extra []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			if !listed[filepath.ToSlash(rel)] {
				extra = append(extra, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if len(extra) > 0 {
		v.say("Added since the load, which is someone using their drive: %s file%s.", records.Commas(int64(len(extra))), plural(len(extra)))
	}

	switch {
	case bad > 0 || ballastBad:
		v.say("Result: BAD.  %d file%s didn't match, or the ballast is damaged.  This drive isn't what the record says.", bad, plural(bad))
		return 1
	case missing > 0:
		v.say("Result: %d file%s missing, and everything else matches.  The ones that are there are what was loaded.", missing, plural(missing))
		return 1
	}
	v.say("Result: every file matches its hash.")
	return 0
}

// checkBallast says whether the ballast is damaged.  Gone or shorter isn't
// damage: it's there to be deleted.
func (v *verifier) checkBallast(r *records.Record, dir string) bool {
	x, l := r.Wipe, r.Load
	key, kerr := hex.DecodeString(x.Key)
	iv, ierr := hex.DecodeString(x.IV)
	if kerr != nil || ierr != nil {
		v.say("Ballast: the record's key doesn't read, so it can't be checked.")
		return false
	}
	f, err := os.Open(filepath.Join(dir, payload.Ballast))
	if errors.Is(err, fs.ErrNotExist) {
		if l.Ballast > 0 {
			v.say("Ballast: deleted, which is what it's for.")
		}
		return false
	}
	if err != nil {
		v.say("Ballast: couldn't open it: %v", err)
		return true
	}
	defer f.Close()
	buf, want := make([]byte, 8<<20), make([]byte, 8<<20)
	var off, wrong int64
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			if kerr := wipe.Keystream(key, iv, off, want[:n]); kerr != nil {
				v.say("Ballast: %v", kerr)
				return true
			}
			if !bytes.Equal(buf[:n], want[:n]) {
				for i := range buf[:n] {
					if buf[i] != want[i] {
						wrong++
					}
				}
			}
			off += int64(n)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			v.say("Ballast: couldn't read it at byte %s: %v", records.Commas(off), err)
			return true
		}
	}
	switch {
	case wrong > 0:
		v.say("Ballast: %s bytes don't match the keystream.", records.Commas(wrong))
		return true
	case off != l.Ballast:
		v.say("Ballast: %s bytes match, of the %s written.  It's been changed in size, so it isn't what was written, though what's there is.", records.Commas(off), records.Commas(l.Ballast))
		return false
	}
	v.say("Ballast: all %s bytes match the keystream.", records.Commas(off))
	return false
}

func hash(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
