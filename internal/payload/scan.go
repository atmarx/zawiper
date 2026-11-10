package payload

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Scan walks a folder and makes a set from what's in it, hashing every file.
// A symlink to a file is followed and listed, since exFAT can't hold the link
// but can hold what it points at.  Warnings are what the drive would lose
// without a word: exFAT drops permissions and symlinks to folders.
func Scan(dir string, s Set, exclude []string, progress func(path string)) (*Set, []string, error) {
	var warn []string
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, x := range exclude {
			if ok, _ := filepath.Match(x, d.Name()); ok || rel == x {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		if rel == FileName {
			return nil
		}
		fi, err := os.Stat(p) // follows a link
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		switch {
		case fi.IsDir():
			warn = append(warn, fmt.Sprintf("%s is a link to a folder, which exFAT can't hold.  It's left out.", rel))
			return nil
		case !fi.Mode().IsRegular():
			warn = append(warn, fmt.Sprintf("%s isn't a regular file.  It's left out.", rel))
			return nil
		case fi.Mode()&0o111 != 0:
			warn = append(warn, fmt.Sprintf("%s is executable here, and exFAT won't keep that.  If it matters, put it in a tar or zip.", rel))
		}
		if err := CheckPath(rel); err != nil {
			return err
		}
		if Reserved(rel) {
			return fmt.Errorf("%s is a name the load keeps for itself", rel)
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(paths)
	s.Files = nil
	for _, rel := range paths {
		if progress != nil {
			progress(rel)
		}
		sum, n, err := hashFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, nil, err
		}
		s.Files = append(s.Files, File{SHA256: sum, Size: n, Path: rel})
	}
	if len(s.Files) == 0 {
		return nil, nil, fmt.Errorf("%s has no files", dir)
	}
	return &s, warn, nil
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
