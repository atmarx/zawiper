package bay

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/atmarx/zawiper/internal/config"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/smart"
)

// Sys is the real Source: sysfs, blkid, smartctl, blockdev, mount.
type Sys struct {
	// The selector: exactly one is set, and only block devices behind it
	// are the bay's.
	//
	// Port is the bay's USB port as this machine's sysfs names it ("10-1").
	// Every block device whose sysfs path runs through it is the bay's.
	Port string
	// LUN, when set, narrows Port to one slot of a dock that shows each
	// slot as a LUN of one USB device.
	LUN string
	// SASPath is the disk's name in /dev/disk/by-path
	// ("pci-0000:03:00.0-sas-phy2-lun-0").  The disk it names is the bay's,
	// and so is any other LUN on the same SCSI target, so that the one-LUN
	// refusal sees it.
	SASPath string

	SmartType string // smartctl -d; "sat" for USB-SATA bridges, "scsi" for SAS
	// RunDir is where the peek makes its mount point: /run/zawiper/<bay>,
	// the unit's RuntimeDirectory, writable under ProtectSystem=strict.
	RunDir   string
	SysBlock string // "/sys/block"; tests point it elsewhere
	ByPath   string // "/dev/disk/by-path"; tests point it elsewhere
}

// NewSys makes the Source for a configured bay.
func NewSys(cfg *config.Bay) (*Sys, error) {
	if cfg == nil {
		return nil, errors.New("no bay config")
	}
	if err := config.ValidName(cfg.Name); err != nil {
		return nil, err
	}
	if (cfg.USBPort == "") == (cfg.SASPath == "") {
		return nil, fmt.Errorf("bay %s: needs exactly one of usb_port and sas_path", cfg.Name)
	}
	return &Sys{
		Port:      cfg.USBPort,
		LUN:       cfg.USBLUN,
		SASPath:   cfg.SASPath,
		SmartType: cfg.SmartType,
		RunDir:    filepath.Join("/run/zawiper", cfg.Name),
	}, nil
}

func (s *Sys) sysBlock() string {
	if s.SysBlock == "" {
		return "/sys/block"
	}
	return s.SysBlock
}

func (s *Sys) byPath() string {
	if s.ByPath == "" {
		return "/dev/disk/by-path"
	}
	return s.ByPath
}

func (s *Sys) runDir() string {
	if s.RunDir == "" {
		return "/run"
	}
	return s.RunDir
}

// usbDir returns the sysfs directory of the bay's USB device in a block
// device's resolved path, or "" when the device isn't on the bay's port.
func (s *Sys) usbDir(real string) string {
	if s.Port == "" {
		return ""
	}
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

// lun returns the LUN of the SCSI device in a block device's resolved path
// (".../target8:0:0/8:0:0:1/block/sdd" is LUN 1), or "" if there isn't one.
func lun(real string) string {
	segs := strings.Split(real, "/")
	for i := 1; i < len(segs); i++ {
		if strings.HasPrefix(segs[i-1], "target") {
			if f := strings.Split(segs[i], ":"); len(f) == 4 {
				return f[3]
			}
		}
	}
	return ""
}

// sasTarget returns the sysfs directory of the SCSI target behind the bay's
// by-path name ("…/end_device-0:2/target0:0:2"), or "" when nothing is
// there.  Every block device under it is on the bay.
func (s *Sys) sasTarget() (string, error) {
	dev, err := filepath.EvalSymlinks(filepath.Join(s.byPath(), s.SASPath))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	name := filepath.Base(dev)
	real, err := filepath.EvalSymlinks(filepath.Join(s.sysBlock(), name))
	if errors.Is(err, os.ErrNotExist) {
		// A partition, or a node on its way out: not a whole disk.
		return "", fmt.Errorf("%s points at %s, which isn't a disk", s.SASPath, name)
	}
	if err != nil {
		return "", err
	}
	segs := strings.Split(real, "/")
	for i := len(segs) - 1; i > 0; i-- {
		if strings.HasPrefix(segs[i], "target") {
			return strings.Join(segs[:i+1], "/"), nil
		}
	}
	return "", fmt.Errorf("%s (%s) isn't behind a SCSI target", s.SASPath, name)
}

// selector returns a test for whether a block device's resolved sysfs path
// is the bay's.
func (s *Sys) selector() (func(real string) bool, error) {
	switch {
	case s.Port != "" && s.SASPath != "":
		return nil, errors.New("the bay has both a USB port and a SAS path")
	case s.Port != "":
		return func(real string) bool { return s.usbDir(real) != "" && (s.LUN == "" || lun(real) == s.LUN) }, nil
	case s.SASPath != "":
		t, err := s.sasTarget()
		if err != nil || t == "" {
			return func(string) bool { return false }, err
		}
		return func(real string) bool { return strings.HasPrefix(real, t+"/") }, nil
	}
	return nil, errors.New("no bay port configured")
}

func (s *Sys) Devices() ([]Device, error) {
	isMine, err := s.selector()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(s.sysBlock())
	if err != nil {
		return nil, err
	}
	var out []Device
	for _, e := range ents {
		real, err := filepath.EvalSymlinks(filepath.Join(s.sysBlock(), e.Name()))
		if err != nil || !isMine(real) {
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
	if u := s.usbDir(real); u != "" {
		return model.Bridge{
			VendorID:     readStr(filepath.Join(u, "idVendor")),
			ProductID:    readStr(filepath.Join(u, "idProduct")),
			Manufacturer: readStr(filepath.Join(u, "manufacturer")),
			Product:      readStr(filepath.Join(u, "product")),
			Serial:       readStr(filepath.Join(u, "serial")),
			Speed:        usbSpeed(readStr(filepath.Join(u, "speed"))),
		}
	}
	// SAS: the bridge is the HBA, the last PCI function above the disk.
	if p := pciDir(real); p != "" {
		return model.Bridge{
			VendorID:  strings.TrimPrefix(readStr(filepath.Join(p, "vendor")), "0x"),
			ProductID: strings.TrimPrefix(readStr(filepath.Join(p, "device")), "0x"),
			Product:   "SAS HBA at " + filepath.Base(p),
		}
	}
	return model.Bridge{}
}

// usbSpeed reads sysfs's speed, in Mb/s: "480", "5000", or "1.5" for low
// speed, which rounds to 1.
func usbSpeed(s string) int {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0
	}
	return max(1, int(f))
}

var pciAddrRE = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

// pciDir returns the deepest PCI function in a sysfs path, or "".
func pciDir(real string) string {
	segs := strings.Split(real, "/")
	for i := len(segs) - 1; i >= 0; i-- {
		if pciAddrRE.MatchString(segs[i]) {
			return strings.Join(segs[:i+1], "/")
		}
	}
	return ""
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
				if rest, ok := strings.CutPrefix(f[4], BrowseRoot+"/"); ok {
					bay, _, _ := strings.Cut(rest, "/")
					return fmt.Sprintf("%s is mounted at %s for browsing; `bayd browse -u %s` on the dock host unmounts it", n, f[4], bay), nil
				}
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

// SetRW clears the read-only flag, for a wipe.
func (s *Sys) SetRW(d Device) error {
	for _, n := range append([]string{d.Name}, s.partitions(d)...) {
		if out, _, err := run(context.Background(), "blockdev", "--setrw", "/dev/"+n); err != nil {
			return fmt.Errorf("blockdev --setrw /dev/%s: %v %s", n, err, bytes.TrimSpace(out))
		}
	}
	return nil
}

// Forget drops the kernel's copy of a partition table the wipe destroyed.
func (s *Sys) Forget(d Device) error {
	_, _, err := run(context.Background(), "blockdev", "--rereadpt", d.Path)
	return err
}

func (s *Sys) SMART(ctx context.Context, d Device) ([]byte, []byte, error) {
	typ := s.SmartType
	if typ == "" {
		typ = "sat"
	}
	// smartctl's exit status is a bitmask of findings, so a non-zero exit is
	// normal.  smart.Parse reads the bits from the JSON; here only a failure
	// to run at all counts.
	js, code, err := runAnyExit(ctx, "smartctl", "-d", typ, "-a", "-j", d.Path)
	if err != nil {
		return nil, nil, err
	}
	// A drive that didn't answer the first time won't answer -x either, and
	// each command it times out stalls the bridge.
	if code&(smart.ExitCmdLine|smart.ExitOpenFailed) != 0 {
		return js, nil, nil
	}
	text, _, err := runAnyExit(ctx, "smartctl", "-d", typ, "-x", d.Path)
	return js, text, err
}

var (
	maxSectors  = regexp.MustCompile(`max sectors\s*=\s*(\d+)/(\d+)`)
	realSectors = regexp.MustCompile(`Real max sectors:\s*(\d+)`)
)

// Capacity asks an ATA drive, through hdparm, whether an HPA or a DCO
// keeps sectors from the host.  Both are read-only queries.
func (s *Sys) Capacity(ctx context.Context, d Device, protocol string) *model.Capacity {
	if protocol != "ATA" {
		return &model.Capacity{Note: fmt.Sprintf("Za Wiper checks only ATA drives for hidden sectors so far, and this one speaks %s.", cmp.Or(protocol, "an unknown protocol"))}
	}
	c := &model.Capacity{Method: "hdparm -N"}
	out, _, err := run(ctx, "hdparm", "-N", d.Path)
	m := maxSectors.FindSubmatch(out)
	if err != nil || m == nil {
		c.Note = fmt.Sprintf("hdparm -N couldn't read the drive's native size through this bridge: %v %s", err, bytes.TrimSpace(out))
		return c
	}
	c.Visible, _ = strconv.ParseInt(string(m[1]), 10, 64)
	c.Native, _ = strconv.ParseInt(string(m[2]), 10, 64)
	// A drive without the DCO feature set refuses the query.  hdparm -N
	// just got through, so the bridge passes ATA commands, and a refusal
	// here is the drive's answer.
	out, _, err = run(ctx, "hdparm", "--dco-identify", d.Path)
	if m := realSectors.FindSubmatch(out); err == nil && m != nil {
		c.Method += ", hdparm --dco-identify"
		if real, _ := strconv.ParseInt(string(m[1]), 10, 64); real > c.Native {
			c.DCO = real
		}
	} else {
		c.Method += "; no DCO"
	}
	return c
}

func (s *Sys) Table(ctx context.Context, d Device) (model.Table, error) {
	t := model.Table{Parts: []model.Part{}}
	if err := openable(ctx, d.Path); err != nil {
		return t, err
	}
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
	var denied error
	for _, n := range s.partitions(d) {
		p := model.Part{Path: "/dev/" + n, Size: readInt(filepath.Join(s.sysBlock(), d.Name, n, "size")) * 512}
		if err := openable(ctx, p.Path); err != nil {
			// Say so, rather than let blkid's "nothing found" call it blank.
			p.FSType = "unreadable"
			denied = err
		} else if v, err := blkid(ctx, p.Path); err == nil {
			p.FSType, p.Label, p.UUID = v["TYPE"], v["LABEL"], v["UUID"]
		}
		t.Parts = append(t.Parts, p)
	}
	return t, denied
}

// Reach checks that bayd may open the drive at all.  A refusal after
// openable's wait is the kernel's device filter: nothing else stops root
// opening a block device read-only.
func (s *Sys) Reach(ctx context.Context, d Device) error {
	err := openable(ctx, d.Path)
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("%w: %v", ErrFiltered, err)
	}
	return err
}

// Restarted reports whether bayd has already restarted once for the drive
// as it sits now, and notes that it has.  The note lives in the runtime
// directory, which outlasts a restart.  It names the device node and when
// udev made it, so a re-inserted drive gets a restart of its own.  When it
// can't tell, it says yes: a filter that's wrong rather than stale mustn't
// restart bayd forever.
func (s *Sys) Restarted(d Device) bool {
	var st syscall.Stat_t
	if err := syscall.Stat(d.Path, &st); err != nil {
		return true
	}
	note := fmt.Sprintf("%s %d %d.%09d\n", d.Path, st.Rdev, st.Ctim.Sec, st.Ctim.Nsec)
	p := filepath.Join(s.runDir(), "filter-restart")
	if old, err := os.ReadFile(p); err == nil && string(old) == note {
		return true
	}
	return os.WriteFile(p, []byte(note), 0o640) != nil
}

// openWait is how long openable waits for the device filter to let a node in.
var openWait = 10 * time.Second

// openable checks that bayd may open a device node.  Under bayd@.service,
// the cgroup device filter admits a partition only once udev has added it
// to the unit (see deploy/bayrules), and that can trail the partition's
// appearance by a moment.  blkid treats a node it can't open as one with
// nothing on it, which would make a full partition look blank.  So wait a
// little for the filter, then fail loudly.
func openable(ctx context.Context, path string) error {
	deadline := time.Now().Add(openWait)
	for {
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			f.Close()
			return nil
		}
		if !errors.Is(err, os.ErrPermission) || time.Now().After(deadline) {
			return fmt.Errorf("can't open %s (%w), so I can't say what's on it", path, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
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
// $4 is where the mount point goes: the bay's runtime directory, the one
// place under /run that bayd@.service may write.
const peekScript = `
d=$(mktemp -d "$4/peek.XXXXXX") || exit 90
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
		"/bin/sh", "-c", peekScript, "peek", m.fs, m.opts, p.Path, s.runDir())
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

// BrowseRoot is where `bayd browse` mounts a bay's filesystems:
// /mnt/zawiper/<bay>/part<n>.
const BrowseRoot = "/mnt/zawiper"

// Browse mounts each filesystem on the bay's drive under dir, one directory
// per partition, for a person to look through before a wipe.  The options
// are the peek's (no journal or log replay) plus noexec, nosuid and nodev,
// and the mounts are in the host's namespace so SFTP can reach them.  The
// drive must already be read-only in the kernel, as bayd leaves it after a
// read.  While anything is mounted, InUse says so and the wipe refuses.
// It returns a line per partition.
func (s *Sys) Browse(ctx context.Context, dir string) ([]string, error) {
	devs, err := s.Devices()
	if err != nil {
		return nil, err
	}
	if len(devs) != 1 {
		return nil, fmt.Errorf("the bay shows %d drives; browse wants exactly one", len(devs))
	}
	d := devs[0]
	if readStr(filepath.Join(s.sysBlock(), d.Name, "ro")) != "1" {
		return nil, fmt.Errorf("%s isn't read-only in the kernel yet.  Wait for the wall to show it PRESENT", d.Path)
	}
	t, err := s.Table(ctx, d)
	if err != nil {
		return nil, err
	}
	mounted := mountsUnder(readMountinfo(), dir)
	var out []string
	for _, p := range t.Parts {
		name, sys := "disk", filepath.Join(s.sysBlock(), d.Name)
		if p.Path != d.Path {
			base := filepath.Base(p.Path)
			sys = filepath.Join(sys, base)
			name = "part" + readStr(filepath.Join(sys, "partition"))
		}
		mp := filepath.Join(dir, name)
		line := fmt.Sprintf("%-6s %-8s %10s  ", name, cmp.Or(p.FSType, "-"), records.Size(p.Size))
		m, ok := mountOpts[p.FSType]
		switch {
		case p.FSType == "":
			out = append(out, line+"no filesystem")
			continue
		case !ok:
			out = append(out, line+"not mounted: I don't open "+p.FSType)
			continue
		case readStr(filepath.Join(sys, "ro")) != "1":
			out = append(out, line+"not mounted: the partition isn't read-only in the kernel")
			continue
		case mounted[mp]:
			out = append(out, line+mp+" (already)")
			continue
		}
		if err := os.MkdirAll(mp, 0o755); err != nil {
			return out, err
		}
		if res, _, err := run(ctx, "mount", "-t", m.fs, "-o", m.opts+",noexec,nosuid,nodev", p.Path, mp); err != nil {
			os.Remove(mp)
			out = append(out, line+fmt.Sprintf("couldn't mount: %v %s", err, firstLine(res)))
			continue
		}
		out = append(out, line+mp)
	}
	return out, nil
}

// Unbrowse unmounts everything Browse mounted under dir and removes the
// mount points.  It returns what it unmounted.
func Unbrowse(dir string) ([]string, error) {
	var done []string
	var errs []error
	for mp := range mountsUnder(readMountinfo(), dir) {
		if res, _, err := run(context.Background(), "umount", mp); err != nil {
			errs = append(errs, fmt.Errorf("umount %s: %v %s", mp, err, firstLine(res)))
			continue
		}
		done = append(done, mp)
	}
	sort.Strings(done)
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			os.Remove(filepath.Join(dir, e.Name())) // only empty ones go
		}
		os.Remove(dir)
	}
	return done, errors.Join(errs...)
}

func readMountinfo() string {
	b, _ := os.ReadFile("/proc/self/mountinfo")
	return string(b)
}

// mountsUnder returns the mount points in a mountinfo listing that sit
// below dir.
func mountsUnder(mountinfo, dir string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(mountinfo, "\n") {
		if f := strings.Fields(line); len(f) > 4 && strings.HasPrefix(f[4], dir+"/") {
			out[f[4]] = true
		}
	}
	return out
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
