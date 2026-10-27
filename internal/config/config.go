// Package config reads the bay configs in /etc/zawiper/bays/<bay>.toml.
// Each file is root-owned and written once, by a person provisioning the
// host.  What's in it is what the bay can touch: the udev rules, the units'
// DeviceAllow= and bayd's own device search are all made from it.
//
// The format is the flat subset of TOML the files need: `key = "value"`
// lines, `key = ["a", "b"]` lists, and # comments.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BaysDir is where the bay configs live.
const BaysDir = "/etc/zawiper/bays"

// Caps a bay can have (spec §8).  None means the bay only reads.
var knownCaps = map[string]bool{"wipe": true, "load": true, "sas": true}

var (
	// A bay name ends up in a unit instance, a /dev/zawiper/ node, a path
	// under /run and /var/lib, and a certificate CN.  Keep it plain.
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	// A USB port as sysfs names it: bus-port, then .port per hub.
	usbRE = regexp.MustCompile(`^[0-9]+-[0-9]+(\.[0-9]+)*$`)
	lunRE = regexp.MustCompile(`^[0-9]{1,3}$`)
	// A /dev/disk/by-path name: no slashes, nothing that needs quoting in a
	// udev rule.
	byPathRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]*$`)
)

// Bay is one bay's config.
type Bay struct {
	Name string
	// Exactly one selector is set.
	USBPort string // "10-1": the dock's port, as this machine's sysfs names it
	SASPath string // "pci-0000:03:00.0-sas-phy2-lun-0": a /dev/disk/by-path name
	// USBLUN narrows usb_port to one slot of a dock that shows each slot
	// as a LUN of one USB device ("0", "1").  "" takes the whole port.
	USBLUN    string
	SmartType string // smartctl -d: "sat" or "scsi"
	Caps      []string
	Path      string // the file it came from
}

// Can says whether the bay has a capability.
func (b *Bay) Can(c string) bool {
	for _, x := range b.Caps {
		if x == c {
			return true
		}
	}
	return false
}

// Selector says what the bay is pinned to, for logs and error messages.
func (b *Bay) Selector() string {
	if b.SASPath != "" {
		return "sas_path " + b.SASPath
	}
	if b.USBLUN != "" {
		return "usb_port " + b.USBPort + " lun " + b.USBLUN
	}
	return "usb_port " + b.USBPort
}

// BayPath is where the config for a named bay lives.
func BayPath(name string) string { return filepath.Join(BaysDir, name+".toml") }

// ValidName says whether a bay name is usable.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("bay name %q: use 1-32 of a-z, 0-9, _ and -, starting with a letter or digit", name)
	}
	return nil
}

// LoadBay reads and checks one bay config.  The file's name, less .toml,
// must be the bay's name.
func LoadBay(path string) (*Bay, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseBay(path, f)
}

// ParseBay reads a bay config from r.  path names the file, for errors and
// for the name check.
func ParseBay(path string, r io.Reader) (*Bay, error) {
	kv, err := parse(path, r)
	if err != nil {
		return nil, err
	}
	b := &Bay{Path: path}
	for k, v := range kv {
		switch k {
		case "name":
			b.Name, err = str(path, k, v)
		case "usb_port":
			b.USBPort, err = str(path, k, v)
		case "usb_lun":
			b.USBLUN, err = str(path, k, v)
		case "sas_path":
			b.SASPath, err = str(path, k, v)
		case "smart_type":
			b.SmartType, err = str(path, k, v)
		case "caps":
			b.Caps = v.list
			if !v.isList {
				// caps = "wipe" or caps = "wipe, load"
				b.Caps = strings.FieldsFunc(v.s, func(r rune) bool { return r == ',' || r == ' ' })
			}
		case "port":
			return nil, fmt.Errorf("%s: `port` is the Phase 1 key.  Rename it `usb_port`, give the bay a name, and keep one file per bay in %s/<name>.toml", path, BaysDir)
		default:
			return nil, fmt.Errorf("%s: unknown key %q (keys: name, usb_port, usb_lun, sas_path, smart_type, caps)", path, k)
		}
		if err != nil {
			return nil, err
		}
	}
	return b, b.check()
}

func (b *Bay) check() error {
	p := b.Path
	if b.Name == "" {
		return fmt.Errorf("%s: no name", p)
	}
	if err := ValidName(b.Name); err != nil {
		return fmt.Errorf("%s: %v", p, err)
	}
	if stem := strings.TrimSuffix(filepath.Base(p), ".toml"); stem != b.Name {
		return fmt.Errorf("%s: the file says name = %q, so it should be %s.toml.  A copied file that kept the old name is how two bays end up on one disk", p, b.Name, b.Name)
	}
	switch {
	case b.USBPort == "" && b.SASPath == "":
		return fmt.Errorf("%s: no selector.  Set usb_port (from `bayd ports`) or sas_path (a name in /dev/disk/by-path)", p)
	case b.USBPort != "" && b.SASPath != "":
		return fmt.Errorf("%s: both usb_port and sas_path.  A bay has exactly one selector", p)
	case b.USBPort != "" && !usbRE.MatchString(b.USBPort):
		return fmt.Errorf("%s: usb_port %q isn't a sysfs port name like \"10-1\" or \"3-2.4\"", p, b.USBPort)
	case b.USBLUN != "" && b.USBPort == "":
		return fmt.Errorf("%s: usb_lun %q without usb_port.  It picks a slot on a USB dock", p, b.USBLUN)
	case b.USBLUN != "" && !lunRE.MatchString(b.USBLUN):
		return fmt.Errorf("%s: usb_lun %q isn't a LUN number like \"0\" or \"1\"", p, b.USBLUN)
	case b.SASPath != "":
		if !byPathRE.MatchString(b.SASPath) || !strings.Contains(b.SASPath, "-sas-") {
			return fmt.Errorf("%s: sas_path %q isn't a SAS by-path name like \"pci-0000:03:00.0-sas-phy2-lun-0\"", p, b.SASPath)
		}
		if strings.Contains(b.SASPath, "-part") {
			return fmt.Errorf("%s: sas_path %q names a partition.  Use the whole disk's name", p, b.SASPath)
		}
	}
	if b.SmartType == "" {
		b.SmartType = "sat"
		if b.SASPath != "" {
			b.SmartType = "scsi"
		}
	}
	if b.SmartType != "sat" && b.SmartType != "scsi" {
		return fmt.Errorf("%s: smart_type %q: use \"sat\" (USB-SATA bridges, SATA drives) or \"scsi\" (SAS)", p, b.SmartType)
	}
	seen := map[string]bool{}
	for _, c := range b.Caps {
		if !knownCaps[c] {
			return fmt.Errorf("%s: unknown cap %q (caps: wipe, load, sas)", p, c)
		}
		if seen[c] {
			return fmt.Errorf("%s: cap %q twice", p, c)
		}
		seen[c] = true
	}
	return nil
}

// LoadBays reads every *.toml in dir.  Any bad file fails the lot, and so
// do two bays with the same selector.
func LoadBays(dir string) ([]*Bay, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var bays []*Bay
	for _, p := range paths {
		b, err := LoadBay(p)
		if err != nil {
			return nil, err
		}
		for _, o := range bays {
			if overlap(o, b) {
				return nil, fmt.Errorf("bays %s (%s) and %s (%s) can both reach one drive.  One device, one bay", o.Name, o.Selector(), b.Name, b.Selector())
			}
		}
		bays = append(bays, b)
	}
	return bays, nil
}

// Siblings names the bays in dir that sit behind the same USB bridge as b:
// the same port, another LUN.  A bad config elsewhere in dir is ignored,
// since this only words a refusal.
func Siblings(dir string, b *Bay) []string {
	if b.USBPort == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.toml"))
	sort.Strings(paths)
	var out []string
	for _, p := range paths {
		o, err := LoadBay(p)
		if err == nil && o.Name != b.Name && o.USBPort == b.USBPort {
			out = append(out, o.Name)
		}
	}
	return out
}

// overlap says whether two bays' selectors can match the same disk.  A bay
// on a whole USB port takes every LUN on it.
func overlap(a, b *Bay) bool {
	if a.USBPort != "" && a.USBPort == b.USBPort {
		return a.USBLUN == "" || b.USBLUN == "" || a.USBLUN == b.USBLUN
	}
	return a.Selector() == b.Selector()
}

// value is a parsed right-hand side: a string or a list of strings.
type value struct {
	s      string
	list   []string
	isList bool
}

func str(path, k string, v value) (string, error) {
	if v.isList {
		return "", fmt.Errorf("%s: %s takes a string, not a list", path, k)
	}
	return v.s, nil
}

func parse(path string, r io.Reader) (map[string]value, error) {
	out := map[string]value{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, rest, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("%s:%d: expected key = \"value\"", path, n)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s:%d: %s set twice", path, n, k)
		}
		v, err := parseValue(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %s: %v", path, n, k, err)
		}
		out[k] = v
	}
	return out, sc.Err()
}

// parseValue reads "a string" or ["a", "list"], then allows only a comment.
func parseValue(s string) (value, error) {
	var v value
	if strings.HasPrefix(s, "[") {
		v.isList = true
		s = strings.TrimSpace(s[1:])
		for !strings.HasPrefix(s, "]") {
			q, err := strconv.QuotedPrefix(s)
			if err != nil || !strings.HasPrefix(q, `"`) {
				return v, errors.New("a list holds double-quoted strings: [\"wipe\", \"load\"]")
			}
			u, _ := strconv.Unquote(q)
			v.list = append(v.list, u)
			s = strings.TrimSpace(s[len(q):])
			if strings.HasPrefix(s, ",") {
				s = strings.TrimSpace(s[1:])
			} else if !strings.HasPrefix(s, "]") {
				return v, errors.New("expected , or ] in the list")
			}
		}
		s = strings.TrimSpace(s[1:])
	} else {
		q, err := strconv.QuotedPrefix(s)
		if err != nil || !strings.HasPrefix(q, `"`) {
			return v, errors.New("values are double-quoted strings")
		}
		v.s, _ = strconv.Unquote(q)
		s = strings.TrimSpace(s[len(q):])
	}
	if s != "" && !strings.HasPrefix(s, "#") {
		return v, fmt.Errorf("unexpected %q after the value", s)
	}
	return v, nil
}
