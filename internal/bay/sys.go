package bay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"zawiper/internal/model"
)

// Sys is the real Source: sysfs, blkid, smartctl, blockdev, mount.
type Sys struct {
	// Port is the bay's USB port as this machine's sysfs names it ("10-1").
	// Only block devices whose sysfs path runs through it are the bay's.
	Port      string
	SmartType string // smartctl -d; "sat" for USB-SATA bridges
	SysBlock  string // "/sys/block"; tests point it elsewhere
}

func (s *Sys) sysBlock() string {
	if s.SysBlock == "" {
		return "/sys/block"
	}
	return s.SysBlock
}

// usbDir returns the sysfs directory of the bay's USB device in a block
// device's resolved path, or "" when the device isn't on the bay's port.
func (s *Sys) usbDir(real string) string {
	segs := strings.Split(real, "/")
	usb := false
	for i, seg := range segs {
		if strings.HasPrefix(seg, "usb") {
			usb = true
		}
		if usb && seg == s.Port {
			return strings.Join(segs[:i+1], "/")
		}
	}
	return ""
}

func (s *Sys) Devices() ([]Device, error) {
	if s.Port == "" {
		return nil, errors.New("no bay port configured")
	}
	ents, err := os.ReadDir(s.sysBlock())
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, e := range ents {
		real, err := filepath.EvalSymlinks(filepath.Join(s.sysBlock(), e.Name()))
		if err != nil || s.usbDir(real) == "" {
			continue
		}
		dir := filepath.Join(s.sysBlock(), e.Name())
		sectors := readInt(filepath.Join(dir, "size"))
		out = append(out, Device{
			Name:     e.Name(),
			Path:     "/dev/" + e.Name(),
			Size:     sectors * 512, // sysfs counts 512-byte sectors whatever the drive's sector size
			Logical:  int(readInt(filepath.Join(dir, "queue/logical_block_size"))),
			Physical: int(readInt(filepath.Join(dir, "queue/physical_block_size"))),
		})
	}
	return out, nil
}

func (s *Sys) Bridge(d Device) model.Bridge {
	real, err := filepath.EvalSymlinks(filepath.Join(s.sysBlock(), d.Name))
	if err != nil {
		return model.Bridge{}
	}
	u := s.usbDir(real)
	return model.Bridge{
		VendorID:     readStr(filepath.Join(u, "idVendor")),
		ProductID:    readStr(filepath.Join(u, "idProduct")),
		Manufacturer: readStr(filepath.Join(u, "manufacturer")),
		Product:      readStr(filepath.Join(u, "product")),
		Serial:       readStr(filepath.Join(u, "serial")),
	}
}

// partitions lists the kernel's partitions of a disk: sdc1, sdc2...
func (s *Sys) partitions(d Device) []string {
	ents, _ := os.ReadDir(filepath.Join(s.sysBlock(), d.Name))
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), d.Name) {
			if _, err := os.Stat(filepath.Join(s.sysBlock(), d.Name, e.Name(), "partition")); err == nil {
				out = append(out, e.Name())
			}
		}
	}
	return out
}

func (s *Sys) InUse(d Device) (string, error) {
	names := append([]string{d.Name}, s.partitions(d)...)
	devnum := map[string]string{} // "8:32" -> sdc
	for _, n := range names {
		dir := filepath.Join(s.sysBlock(), d.Name)
		if n != d.Name {
			dir = filepath.Join(dir, n)
		}
		if h, _ := os.ReadDir(filepath.Join(dir, "holders")); len(h) > 0 {
			return fmt.Sprintf("%s is held by %s", n, h[0].Name()), nil
		}
		devnum[readStr(filepath.Join(dir, "dev"))] = n
	}
	mi, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(mi), "\n") {
		f := strings.Fields(line)
		if len(f) > 4 {
			if n, ok := devnum[f[2]]; ok {
				return fmt.Sprintf("%s is mounted at %s", n, f[4]), nil
			}
		}
	}
	sw, _ := os.ReadFile("/proc/swaps")
	for _, line := range strings.Split(string(sw), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 {
			for _, n := range names {
				if f[0] == "/dev/"+n {
					return fmt.Sprintf("%s is swap", n), nil
				}
			}
		}
	}
	return "", nil
}

func (s *Sys) SetRO(d Device) error {
	for _, n := range append([]string{d.Name}, s.partitions(d)...) {
		if out, _, err := run(context.Background(), "blockdev", "--setro", "/dev/"+n); err != nil {
			return fmt.Errorf("blockdev --setro /dev/%s: %v %s", n, err, bytes.TrimSpace(out))
		}
	}
	return nil
}

func (s *Sys) SMART(ctx context.Context, d Device) ([]byte, []byte, error) {
	typ := s.SmartType
	if typ == "" {
		typ = "sat"
	}
	// smartctl's exit status is a bitmask of findings, so a non-zero exit is
	// normal.  smart.Parse reads the bits from the JSON; here only a failure
	// to run at all counts.
	js, _, err := runAnyExit(ctx, "smartctl", "-d", typ, "-a", "-j", d.Path)
	if err != nil {
		return nil, nil, err
	}
	text, _, err := runAnyExit(ctx, "smartctl", "-d", typ, "-x", d.Path)
	return js, text, err
}

func (s *Sys) Table(ctx context.Context, d Device) (model.Table, error) {
	t := model.Table{Parts: []model.Part{}}
	disk, err := blkid(ctx, d.Path)
	if err != nil {
		return t, err
	}
	t.Type = disk["PTTYPE"]
	if t.Type == "" && disk["TYPE"] != "" {
		// A filesystem on the bare disk, no partition table.
		t.Parts = append(t.Parts, model.Part{Path: d.Path, Size: d.Size, FSType: disk["TYPE"], Label: disk["LABEL"], UUID: disk["UUID"]})
		return t, nil
	}
	for _, n := range s.partitions(d) {
		p := model.Part{Path: "/dev/" + n, Size: readInt(filepath.Join(s.sysBlock(), d.Name, n, "size")) * 512}
		if v, err := blkid(ctx, p.Path); err == nil {
			p.FSType, p.Label, p.UUID = v["TYPE"], v["LABEL"], v["UUID"]
		}
		t.Parts = append(t.Parts, p)
	}
	return t, nil
}

// mountOpts are the read-only options per filesystem: nothing replays a
// journal or log, so a mount writes nothing.  Absent means "don't mount it".
var mountOpts = map[string]struct{ fs, opts string }{
	"ext2":     {"ext2", "ro"},
	"ext3":     {"ext3", "ro,noload"},
	"ext4":     {"ext4", "ro,noload"},
	"xfs":      {"xfs", "ro,norecovery"},
	"btrfs":    {"btrfs", "ro,rescue=nologreplay"},
	"vfat":     {"vfat", "ro"},
	"exfat":    {"exfat", "ro"},
	"ntfs":     {"ntfs3", "ro"},
	"hfsplus":  {"hfsplus", "ro"},
	"iso9660":  {"iso9660", "ro"},
	"udf":      {"udf", "ro"},
	"f2fs":     {"f2fs", "ro,norecovery"},
	"squashfs": {"squashfs", "ro"},
}

// peekScript mounts in a private mount namespace, so the host never sees the
// mount, measures, looks for backup repositories at the top level, and
// leaves.  The namespace dies with the process, taking the mount with it.
const peekScript = `
d=$(mktemp -d /run/zawiper-peek.XXXXXX) || exit 90
trap 'umount "$d" 2>/dev/null; rmdir "$d"' EXIT
mount -t "$1" -o "$2,noexec,nosuid,nodev" "$3" "$d" || exit 91
df -B1 --output=used "$d" | tail -n 1
for f in config keys snapshots .chunks; do [ -e "$d/$f" ] && echo "has $f"; done
exit 0
`

func (s *Sys) Peek(ctx context.Context, p *model.Part) {
	if p.FSType == "" {
		p.Peek = "no filesystem signature"
		return
	}
	m, ok := mountOpts[p.FSType]
	if !ok {
		p.Peek = fmt.Sprintf("not mounted: I don't open %s", p.FSType)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, code, err := runAnyExit(ctx, "unshare", "--mount", "--propagation", "private", "--",
		"/bin/sh", "-c", peekScript, "peek", m.fs, m.opts, p.Path)
	if err != nil || code != 0 {
		p.Peek = fmt.Sprintf("couldn't mount it read-only to look (exit %d): %s", code, firstLine(out))
		return
	}
	has := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for i := 0; sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		if i == 0 {
			if n, err := strconv.ParseInt(line, 10, 64); err == nil {
				p.Used = n
			}
		} else if f, ok := strings.CutPrefix(line, "has "); ok {
			has[f] = true
		}
	}
	p.Peek = "mounted read-only in a private namespace to measure"
	switch {
	case has["config"] && has["keys"] && has["snapshots"]:
		p.Backup = "holds a restic repository"
	case has[".chunks"]:
		p.Backup = "holds a Proxmox Backup Server datastore"
	}
}

// blkid -p probes the device itself, not udev's cache, which can lag a
// fresh insert.  Exit 2 means it found nothing.
func blkid(ctx context.Context, path string) (map[string]string, error) {
	out, code, err := runAnyExit(ctx, "blkid", "-p", "-o", "export", path)
	if err != nil {
		return nil, err
	}
	v := map[string]string{}
	if code == 2 {
		return v, nil
	}
	if code != 0 {
		return nil, fmt.Errorf("blkid exit %d: %s", code, firstLine(out))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if k, val, ok := strings.Cut(line, "="); ok {
			v[k] = val
		}
	}
	return v, nil
}

// run fails on any non-zero exit.
func run(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	out, code, err := runAnyExit(ctx, name, args...)
	if err == nil && code != 0 {
		err = fmt.Errorf("exit %d", code)
	}
	return out, code, err
}

// runAnyExit returns stdout and the exit code; err is only for failing to run.
func runAnyExit(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, sbin(name), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if len(out) == 0 {
			out = stderr.Bytes()
		}
		return out, ee.ExitCode(), nil
	}
	return out, 0, err
}

// sbin finds admin tools that live outside a non-root PATH (dev mode).
func sbin(name string) string {
	if _, err := exec.LookPath(name); err == nil {
		return name
	}
	for _, dir := range []string{"/usr/sbin", "/sbin"} {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	return name
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func firstLine(b []byte) string {
	l, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return l
}

func readStr(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(string(b))
}

func readInt(path string) int64 {
	n, _ := strconv.ParseInt(readStr(path), 10, 64)
	return n
}
