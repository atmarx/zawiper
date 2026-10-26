package bay

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"zawiper/internal/config"
)

// fakeSys builds a sysfs tree and /dev/disk/by-path under a temp dir:
//
//	sda  the host's own disk, on SATA
//	sdc  a drive in a USB dock on port 10-1, with one partition
//	sdd  a USB drive on bus 11, port 11-1 (not "1-1", whatever a glob says)
//	sde  SAS, HBA 0000:03:00.0, phy 0, LUN 0
//	sdf  SAS, same target, LUN 1 (a second LUN the bay must see)
//	sdg  SAS, same HBA, phy 1: a different bay
type fakeTree struct {
	root, sysBlock, byPath string
}

func newFakeTree(t *testing.T) *fakeTree {
	t.Helper()
	root := t.TempDir()
	f := &fakeTree{root: root, sysBlock: filepath.Join(root, "sys/block"), byPath: filepath.Join(root, "dev/disk/by-path")}
	for _, d := range []string{f.sysBlock, f.byPath} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	usb := "sys/devices/pci0000:00/0000:00:14.0/usb10/10-1"
	f.file(t, usb+"/idVendor", "174c")
	f.file(t, usb+"/idProduct", "1156")
	f.file(t, usb+"/manufacturer", "ASMedia")
	f.file(t, usb+"/product", "ASM1156-PM")
	f.file(t, usb+"/serial", "000000000000")
	hba := "sys/devices/pci0000:00/0000:00:01.0/0000:03:00.0"
	f.file(t, hba+"/vendor", "0x1000")
	f.file(t, hba+"/device", "0x0097")

	f.disk(t, "sda", "sys/devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", 500107862016)
	f.disk(t, "sdc", usb+"/10-1:1.0/host6/target6:0:0/6:0:0:0/block/sdc", 1000204886016)
	f.file(t, usb+"/10-1:1.0/host6/target6:0:0/6:0:0:0/block/sdc/sdc1/partition", "1")
	f.file(t, usb+"/10-1:1.0/host6/target6:0:0/6:0:0:0/block/sdc/sdc1/size", "2048")
	f.disk(t, "sdd", "sys/devices/pci0000:00/0000:00:14.0/usb11/11-1/11-1:1.0/host7/target7:0:0/7:0:0:0/block/sdd", 16e9)
	sas := hba + "/host2/port-2:0/end_device-2:0/target2:0:0"
	f.disk(t, "sde", sas+"/2:0:0:0/block/sde", 4000787030016)
	f.disk(t, "sdg", hba+"/host2/port-2:1/end_device-2:1/target2:0:1/2:0:1:0/block/sdg", 4000787030016)
	f.byPathLink(t, "pci-0000:03:00.0-sas-phy0-lun-0", "sde")
	f.byPathLink(t, "pci-0000:03:00.0-sas-phy1-lun-0", "sdg")
	f.file(t, "dev/sde1", "")
	f.byPathLink(t, "pci-0000:03:00.0-sas-phy0-lun-0-part1", "sde1")
	return f
}

func (f *fakeTree) file(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// disk makes a block device's sysfs directory, its /sys/block link, and its
// /dev node (an empty file, so by-path links resolve).
func (f *fakeTree) disk(t *testing.T, name, rel string, size int64) {
	t.Helper()
	f.file(t, rel+"/size", strconv.FormatInt(size/512, 10))
	f.file(t, rel+"/queue/logical_block_size", "512")
	f.file(t, rel+"/queue/physical_block_size", "4096")
	if err := os.MkdirAll(filepath.Join(f.root, rel, "holders"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, rel), filepath.Join(f.sysBlock, name)); err != nil {
		t.Fatal(err)
	}
	f.file(t, "dev/"+name, "")
}

func (f *fakeTree) byPathLink(t *testing.T, name, dev string) {
	t.Helper()
	if err := os.Symlink("../../"+dev, filepath.Join(f.byPath, name)); err != nil {
		t.Fatal(err)
	}
}

func names(devs []Device) string {
	var n []string
	for _, d := range devs {
		n = append(n, d.Name)
	}
	sort.Strings(n)
	return strings.Join(n, ",")
}

func TestSysUSB(t *testing.T) {
	f := newFakeTree(t)
	s := &Sys{Port: "10-1", SysBlock: f.sysBlock, ByPath: f.byPath}
	devs, err := s.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if names(devs) != "sdc" {
		t.Fatalf("port 10-1: %s", names(devs))
	}
	d := devs[0]
	if d.Path != "/dev/sdc" || d.Size != 1000204886016 || d.Logical != 512 || d.Physical != 4096 {
		t.Errorf("%+v", d)
	}
	if b := s.Bridge(d); b.VendorID != "174c" || b.Product != "ASM1156-PM" {
		t.Errorf("bridge %+v", b)
	}
	if p := s.partitions(d); len(p) != 1 || p[0] != "sdc1" {
		t.Errorf("partitions %v", p)
	}

	// Port 1-1 is a different port from 11-1, and from 10-1.
	s.Port = "1-1"
	if devs, _ := s.Devices(); len(devs) != 0 {
		t.Errorf("port 1-1 matched %s", names(devs))
	}
	s.Port = "11-1"
	if devs, _ := s.Devices(); names(devs) != "sdd" {
		t.Errorf("port 11-1: %s", names(devs))
	}
}

// A two-slot dock is one USB device with a LUN per slot.  A bay on the whole
// port sees both drives (and refuses); a bay per LUN sees its own.
func TestSysUSBLUN(t *testing.T) {
	f := newFakeTree(t)
	usb := "sys/devices/pci0000:00/0000:00:14.0/usb10/10-1"
	f.disk(t, "sdf", usb+"/10-1:1.0/host6/target6:0:0/6:0:0:1/block/sdf", 1000204886016)
	f.disk(t, "sdh", usb+"/10-1:1.0/host6/target6:0:0/6:0:0:11/block/sdh", 1000204886016)
	s := &Sys{Port: "10-1", SysBlock: f.sysBlock, ByPath: f.byPath}
	if devs, _ := s.Devices(); names(devs) != "sdc,sdf,sdh" {
		t.Errorf("whole port: %s", names(devs))
	}
	for l, want := range map[string]string{"0": "sdc", "1": "sdf", "11": "sdh", "2": ""} {
		s.LUN = l
		if devs, _ := s.Devices(); names(devs) != want {
			t.Errorf("lun %s: %s, want %s", l, names(devs), want)
		}
	}
	// The LUN narrows the port; it doesn't stand alone.
	s.Port, s.LUN = "11-1", "1"
	if devs, _ := s.Devices(); len(devs) != 0 {
		t.Errorf("port 11-1 lun 1: %s", names(devs))
	}
}

func TestSysSAS(t *testing.T) {
	f := newFakeTree(t)
	s := &Sys{SASPath: "pci-0000:03:00.0-sas-phy0-lun-0", SysBlock: f.sysBlock, ByPath: f.byPath}
	devs, err := s.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if names(devs) != "sde" {
		t.Fatalf("phy0: %s", names(devs))
	}
	b := s.Bridge(devs[0])
	if b.VendorID != "1000" || b.ProductID != "0097" || b.Product != "SAS HBA at 0000:03:00.0" {
		t.Errorf("bridge %+v", b)
	}

	// A second LUN on the same target is on the bay too, so the one-LUN
	// refusal can see it.  The next phy over is not.
	f.disk(t, "sdf", "sys/devices/pci0000:00/0000:00:01.0/0000:03:00.0/host2/port-2:0/end_device-2:0/target2:0:0/2:0:0:1/block/sdf", 4000787030016)
	if devs, _ := s.Devices(); names(devs) != "sde,sdf" {
		t.Errorf("two LUNs: %s", names(devs))
	}
	s.SASPath = "pci-0000:03:00.0-sas-phy1-lun-0"
	if devs, _ := s.Devices(); names(devs) != "sdg" {
		t.Errorf("phy1: %s", names(devs))
	}
}

func TestSysSASEmptyAndWrong(t *testing.T) {
	f := newFakeTree(t)
	s := &Sys{SASPath: "pci-0000:03:00.0-sas-phy7-lun-0", SysBlock: f.sysBlock, ByPath: f.byPath}
	devs, err := s.Devices()
	if err != nil || len(devs) != 0 {
		t.Fatalf("an empty phy is an empty bay: %v %v", devs, err)
	}
	s.SASPath = "pci-0000:03:00.0-sas-phy0-lun-0-part1"
	if _, err := s.Devices(); err == nil || !strings.Contains(err.Error(), "isn't a disk") {
		t.Errorf("a partition's by-path: %v", err)
	}
}

func TestSysNoSelector(t *testing.T) {
	if _, err := (&Sys{}).Devices(); err == nil {
		t.Error("no selector should be an error")
	}
	if _, err := (&Sys{Port: "10-1", SASPath: "x-sas-y"}).Devices(); err == nil {
		t.Error("two selectors should be an error")
	}
}

func TestNewSys(t *testing.T) {
	s, err := NewSys(&config.Bay{Name: "dock-a", USBPort: "10-1", SmartType: "sat"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Port != "10-1" || s.LUN != "" || s.SASPath != "" || s.SmartType != "sat" || s.RunDir != "/run/zawiper/dock-a" {
		t.Errorf("%+v", s)
	}
	s, err = NewSys(&config.Bay{Name: "hba0", SASPath: "pci-0000:03:00.0-sas-phy2-lun-0", SmartType: "scsi"})
	if err != nil || s.SASPath == "" || s.Port != "" {
		t.Errorf("%+v %v", s, err)
	}
	for _, c := range []*config.Bay{
		nil,
		{Name: "b"},
		{Name: "b", USBPort: "10-1", SASPath: "pci-0000:03:00.0-sas-phy2-lun-0"},
		{Name: "../etc", USBPort: "10-1"},
	} {
		if _, err := NewSys(c); err == nil {
			t.Errorf("NewSys(%+v) should fail", c)
		}
	}
}

func TestOpenable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "node")
	os.WriteFile(p, nil, 0o644)
	if err := openable(context.Background(), p); err != nil {
		t.Error(err)
	}
	// Anything but a permission error fails at once.
	if err := openable(context.Background(), p+"-gone"); err == nil {
		t.Error("a missing node should fail")
	}
	if os.Geteuid() != 0 {
		// A node bayd may not open (here, by mode; under the unit, by the
		// device filter) fails after the wait, and says why.
		os.Chmod(p, 0)
		openWait = 0
		defer func() { openWait = 10 * time.Second }()
		if err := openable(context.Background(), p); err == nil || !strings.Contains(err.Error(), "can't say what's on it") {
			t.Errorf("unopenable node: %v", err)
		}
	}
}

// fakeHdparm puts an hdparm on PATH that prints what a drive would.
func fakeHdparm(t *testing.T, n, dco string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n-N) " + n + " ;;\n--dco-identify) " + dco + " ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "hdparm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestCapacity(t *testing.T) {
	ctx, d, s := context.Background(), Device{Path: "/dev/sdc"}, &Sys{}
	const whole = `printf '\\n/dev/sdc:\\n max sectors   = 1953525168/1953525168, HPA is disabled\\n'`
	const noDCO = `echo 'SG_IO: bad/missing sense data' >&2; exit 5`

	fakeHdparm(t, whole, noDCO)
	if c := s.Capacity(ctx, d, "ATA"); !c.Checked() || c.Hidden() != 0 || c.Method != "hdparm -N; no DCO" {
		t.Errorf("whole drive: %+v", c)
	}
	fakeHdparm(t, `echo ' max sectors   = 1953000000/1953525168, HPA is enabled'`, noDCO)
	if c := s.Capacity(ctx, d, "ATA"); !c.Checked() || c.Hidden() != 525168 {
		t.Errorf("HPA: %+v", c)
	}
	fakeHdparm(t, whole, `printf 'DCO Revision: 0x0001\\nReal max sectors: 1953525200\\n'`)
	if c := s.Capacity(ctx, d, "ATA"); !c.Checked() || c.Hidden() != 32 || !strings.Contains(c.Method, "dco-identify") {
		t.Errorf("DCO: %+v", c)
	}
	fakeHdparm(t, `echo 'SG_IO: questionable sense data' >&2; exit 5`, noDCO)
	if c := s.Capacity(ctx, d, "ATA"); c.Checked() || !strings.Contains(c.Note, "hdparm -N couldn't") {
		t.Errorf("bridge refused: %+v", c)
	}
	if c := s.Capacity(ctx, d, "SCSI"); c.Checked() || !strings.Contains(c.Note, "speaks SCSI") {
		t.Errorf("SAS: %+v", c)
	}
}

func TestMountsUnder(t *testing.T) {
	mi := `36 35 98:0 / /mnt rw - ext4 /dev/sda1 rw
37 36 8:17 / /mnt/zawiper/bay1/part1 ro,nosuid,nodev,noexec - ext4 /dev/sdb1 ro
38 36 8:22 / /mnt/zawiper/bay1/part6 ro,nosuid,nodev,noexec - ext4 /dev/sdb6 ro
39 36 8:34 / /mnt/zawiper/bay10/part2 ro - ext4 /dev/sdc2 ro
40 36 8:35 / /mnt/zawiper/bay1 rw - tmpfs tmpfs rw`
	got := mountsUnder(mi, "/mnt/zawiper/bay1")
	if len(got) != 2 || !got["/mnt/zawiper/bay1/part1"] || !got["/mnt/zawiper/bay1/part6"] {
		t.Errorf("%v", got)
	}
}
