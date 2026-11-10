// Package payload is what a Wipe + Load leaves on a drive: a set of files
// with hashes (spec §6.3).  Za Wiper knows nothing about what they are.
//
// A hub's payload folder holds them, with payloads.txt at its top:
//
//	set mega
//	  description The full medium loadout
//	  label THE-ARK
//	  filesystem exfat
//	  file 9f86d081…  19276733775  zim/wikisource_en_all_maxi_2026-02.zim
//
// Plain text with no dependencies, so a person can read it and any tool can
// write it.  One bad line fails the whole file: a set that's half right would
// load a drive that's half right.
package payload

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// FileName is the options file, at the top of a payload folder.
	FileName = "payloads.txt"
	// Reserve stays free on a loaded drive for the record, written after the
	// last read.
	Reserve = 16 << 20
	// Ballast fills what's left.  It's keystream, and its owner may delete it.
	Ballast = "DELETE_THIS_IF_YOU_NEED_SPACE"
	// StampText and StampList are the stamp: for people, and for zverify.
	StampText = "ZA-WIPER.txt"
	StampList = "ZA-WIPER-FILES.txt"
	// RecordFile is the record, written into the reserve.
	RecordFile = "ZA-WIPER.json"
)

// File is one file of a set.
type File struct {
	SHA256 string
	Size   int64
	Path   string // relative, with forward slashes
}

// Set is one choice on the confirm.
type Set struct {
	Name        string
	Description string
	Label       string
	Filesystem  string
	// Root is where the files sit in the payload folder, when a folder holds
	// several sets.  A file's path is where it lands on the drive.
	Root  string
	Files []File
}

// Result is what a load did, for the record.  Filenames stay out of it: a
// department's payload may be private, and the full list is on the drive.
type Result struct {
	Set   string `json:"set"`
	Label string `json:"label"`
	// Hash is the SHA-256 of the set's list, as the stamp holds it.
	Hash    string `json:"hash"`
	Files   int    `json:"files"`
	Bytes   int64  `json:"bytes"`
	Ballast int64  `json:"ballast"`
	// VerifiedFiles and VerifiedBallast are bytes that came back right in
	// the full read.
	VerifiedFiles   int64 `json:"verified_files"`
	VerifiedBallast int64 `json:"verified_ballast"`
	// BadFiles are positions in the stamp's list, counting from 1.
	BadFiles   []int `json:"bad_files,omitempty"`
	BadBallast int64 `json:"bad_ballast,omitempty"`
	// RecordOnDrive says the drive has its copy of this record.  Nothing has
	// verified that one file: it goes down after the last read.
	RecordOnDrive bool `json:"record_on_drive"`
}

// OK is a load where everything came back right.
func (r *Result) OK() bool {
	return len(r.BadFiles) == 0 && r.BadBallast == 0 &&
		r.VerifiedFiles == r.Bytes && r.VerifiedBallast == r.Ballast
}

// Verified is every byte the full read checked.
func (r *Result) Verified() int64 { return r.VerifiedFiles + r.VerifiedBallast }

// Bytes is what the set's files add up to.
func (s *Set) Bytes() int64 {
	var n int64
	for _, f := range s.Files {
		n += f.Size
	}
	return n
}

// Find returns the file with this path, or nil.
func (s *Set) Find(p string) *File {
	for i := range s.Files {
		if s.Files[i].Path == p {
			return &s.Files[i]
		}
	}
	return nil
}

// Text is the set as payloads.txt holds it, one way only, so its hash means
// something.  The stamp is this.
func (s *Set) Text() []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "set %s\n", s.Name)
	if s.Description != "" {
		fmt.Fprintf(&b, "  description %s\n", s.Description)
	}
	fmt.Fprintf(&b, "  label %s\n  filesystem %s\n", s.Label, s.Filesystem)
	if s.Root != "" {
		fmt.Fprintf(&b, "  root %s\n", s.Root)
	}
	for _, f := range s.Files {
		fmt.Fprintf(&b, "  file %s  %d  %s\n", f.SHA256, f.Size, f.Path)
	}
	return b.Bytes()
}

// Hash is the SHA-256 of Text: what a confirm names, and what a record keeps.
func (s *Set) Hash() string {
	h := sha256.Sum256(s.Text())
	return hex.EncodeToString(h[:])
}

// Fits says whether the set can go on a drive this size.  Nothing can measure
// a filesystem that doesn't exist yet, so this holds back 1% for its overhead
// and the rounding of each file up to a cluster, plus the reserve and the
// stamp.  The bay measures the real thing before it loads.
func (s *Set) Fits(drive int64) bool {
	return s.Bytes()+int64(len(s.Text()))+Reserve+drive/100 <= drive
}

// Biggest is the index of the largest set that fits, or -1.
func Biggest(sets []Set, drive int64) int {
	best := -1
	for i := range sets {
		if sets[i].Fits(drive) && (best < 0 || sets[i].Bytes() > sets[best].Bytes()) {
			best = i
		}
	}
	return best
}

var (
	setNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	hashRE    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// A label goes on the filesystem: exFAT holds 11 characters.
	labelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,10}$`)
)

// CheckPath refuses a path that could leave the payload folder or the drive's
// mount, or that exFAT can't hold.  Both the hub and the bay ask.
func CheckPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("an empty path")
	case !utf8.ValidString(p):
		return fmt.Errorf("%q isn't valid UTF-8", p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%q is absolute; paths are relative to the payload folder", p)
	case path.Clean(p) != p || p == ".":
		return fmt.Errorf("%q isn't a clean relative path", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "..":
			return fmt.Errorf("%q leaves the payload folder", p)
		case len(seg) > 255:
			return fmt.Errorf("%q has a name longer than exFAT holds", p)
		case strings.HasSuffix(seg, " ") || strings.HasSuffix(seg, "."):
			return fmt.Errorf("%q has a name ending in a space or a dot, which Windows can't open", p)
		}
		for _, r := range seg {
			if r < 0x20 || strings.ContainsRune(`"*:<>?\|`, r) {
				return fmt.Errorf("%q has a character exFAT can't hold (%q)", p, r)
			}
		}
	}
	return nil
}

// Reserved says a path belongs to the load itself, so a set can't name it.
func Reserved(p string) bool {
	switch strings.ToUpper(p) {
	case strings.ToUpper(Ballast), strings.ToUpper(StampText), strings.ToUpper(StampList), strings.ToUpper(RecordFile):
		return true
	}
	return false
}

// Parse reads payloads.txt, or a single set from a stamp.
func Parse(r io.Reader) ([]Set, error) {
	var sets []Set
	var cur *Set
	names := map[string]bool{}
	var seen map[string]bool // lower-cased: exFAT doesn't care about case
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		bad := func(f string, a ...any) error {
			return fmt.Errorf("line %d: %s", n, fmt.Sprintf(f, a...))
		}
		key, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)
		if key == "set" {
			if !setNameRE.MatchString(rest) {
				return nil, bad("%q isn't a set name: letters, digits, dots, dashes", rest)
			}
			if names[rest] {
				return nil, bad("there are two sets called %q", rest)
			}
			names[rest] = true
			sets = append(sets, Set{Name: rest})
			cur = &sets[len(sets)-1]
			seen = map[string]bool{}
			continue
		}
		if cur == nil {
			return nil, bad("%q comes before any set", key)
		}
		switch key {
		case "description":
			cur.Description = rest
		case "label":
			if !labelRE.MatchString(rest) {
				return nil, bad("%q isn't a volume label: up to 11 letters, digits, spaces, dashes", rest)
			}
			cur.Label = rest
		case "filesystem":
			if rest != "exfat" {
				return nil, bad("filesystem %q: only exfat for now", rest)
			}
			cur.Filesystem = rest
		case "root":
			if err := CheckPath(rest); err != nil {
				return nil, bad("root: %v", err)
			}
			cur.Root = rest
		case "file":
			f, err := parseFile(rest)
			if err != nil {
				return nil, bad("%v", err)
			}
			if Reserved(f.Path) {
				return nil, bad("%q is a name the load keeps for itself", f.Path)
			}
			low := strings.ToLower(f.Path)
			if seen[low] {
				return nil, bad("%q is listed twice, or twice but for case, which exFAT can't tell apart", f.Path)
			}
			seen[low] = true
			cur.Files = append(cur.Files, f)
		default:
			return nil, bad("%q isn't a key: set, description, label, filesystem, root, file", key)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for i := range sets {
		s := &sets[i]
		if s.Filesystem == "" {
			s.Filesystem = "exfat"
		}
		if s.Label == "" {
			return nil, fmt.Errorf("set %q has no label", s.Name)
		}
		if len(s.Files) == 0 {
			return nil, fmt.Errorf("set %q lists no files", s.Name)
		}
	}
	return sets, nil
}

// ParseBytes is Parse on a list already in hand.
func ParseBytes(b []byte) ([]Set, error) { return Parse(bytes.NewReader(b)) }

// parseFile reads "sha256  size  path".  The path is the rest of the line, so
// it can hold spaces.
func parseFile(s string) (File, error) {
	sum, rest, ok1 := strings.Cut(s, " ")
	rest = strings.TrimLeft(rest, " ")
	size, name, ok2 := strings.Cut(rest, " ")
	name = strings.TrimLeft(name, " ")
	if !ok1 || !ok2 {
		return File{}, fmt.Errorf("a file is its sha256, its size and its path")
	}
	if !hashRE.MatchString(sum) {
		return File{}, fmt.Errorf("%q isn't a lower-case SHA-256", sum)
	}
	n, err := strconv.ParseInt(size, 10, 64)
	if err != nil || n < 0 {
		return File{}, fmt.Errorf("%q isn't a size in bytes", size)
	}
	if err := CheckPath(name); err != nil {
		return File{}, err
	}
	return File{SHA256: sum, Size: n, Path: name}, nil
}

// Library is a hub's payload folder.  It re-reads the file each time, so a new
// snapshot shows up without restarting the hub.
type Library struct{ Dir string }

// Sets reads payloads.txt.  An error here belongs on the page: the folder was
// configured, and something's wrong with it.
func (l *Library) Sets() ([]Set, error) {
	f, err := os.Open(filepath.Join(l.Dir, FileName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sets, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(l.Dir, FileName), err)
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("%s lists no sets", filepath.Join(l.Dir, FileName))
	}
	return sets, nil
}

// Set finds one by name.
func (l *Library) Set(name string) (*Set, error) {
	sets, err := l.Sets()
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].Name == name {
			return &sets[i], nil
		}
	}
	return nil, fmt.Errorf("no set called %q", name)
}
