package bay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zawiper/internal/model"
	"zawiper/internal/smart"
)

const fixtureSize = 1000204886016

type fake struct {
	devs   []Device
	inUse  string
	roErr  error
	smart  []byte
	table  model.Table
	peeked []string
	ro     bool
	cap    *model.Capacity
}

func (f *fake) Devices() ([]Device, error)                         { return f.devs, nil }
func (f *fake) Bridge(Device) model.Bridge                         { return model.Bridge{VendorID: "174c"} }
func (f *fake) InUse(Device) (string, error)                       { return f.inUse, nil }
func (f *fake) SetRO(Device) error                                 { f.ro = f.roErr == nil; return f.roErr }
func (f *fake) Table(context.Context, Device) (model.Table, error) { return f.table, nil }
func (f *fake) SetRW(Device) error                                 { f.ro = false; return nil }
func (f *fake) Forget(Device) error                                { return nil }
func (f *fake) Eject(Device) error                                 { f.devs = nil; return nil }
func (f *fake) SMART(context.Context, Device) ([]byte, []byte, error) {
	return f.smart, []byte("text"), nil
}
func (f *fake) Capacity(context.Context, Device, string) *model.Capacity { return f.cap }
func (f *fake) Peek(_ context.Context, p *model.Part) {
	f.peeked = append(f.peeked, p.Path)
	p.Used = 312e9
}

func drive(t *testing.T) *fake {
	js, err := os.ReadFile("../smart/testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	return &fake{devs: []Device{{Name: "sdc", Path: "/dev/sdc", Size: fixtureSize}}, smart: js}
}

func TestReadClean(t *testing.T) {
	f := drive(t)
	f.table = model.Table{Type: "gpt", Parts: []model.Part{{Path: "/dev/sdc1", FSType: "ext4", Label: "data"}}}
	s := Read(context.Background(), f, f.devs)
	if s.State != model.Present || len(s.Refusals) != 0 {
		t.Fatalf("state %s refusals %v", s.State, s.Refusals)
	}
	if !f.ro || !s.Drive.ReadOnly {
		t.Error("read without setting read-only")
	}
	if s.Drive.Serial() != "9VPAX9NA" || s.Drive.Health.Grade != smart.Notes {
		t.Errorf("serial %q grade %q", s.Drive.Serial(), s.Drive.Health.Grade)
	}
	if used, all := s.Drive.UsedBytes(); used != 312e9 || !all {
		t.Errorf("used %d complete %v", used, all)
	}
}

func TestReadRefusals(t *testing.T) {
	cases := map[string]struct {
		mod  func(*fake)
		want string
		peek bool
	}{
		"two drives": {func(f *fake) { f.devs = append(f.devs, Device{Name: "sdd", Path: "/dev/sdd", Size: 1}) }, "one at a time", false},
		"in use":     {func(f *fake) { f.inUse = "sdc1 is mounted at /" }, "host is using", false},
		"no ro":      {func(f *fake) { f.roErr = errors.New("nope") }, "read-only", false},
		"no smart":   {func(f *fake) { f.smart = []byte(`{"smartctl":{"exit_status":2}}`) }, "SMART can't be read", true},
		// Val saw slot 0 report 4.14 GB for two different drives.
		"placeholder size": {func(f *fake) { f.devs[0].Size = 4140000000 }, "placeholder", true},
		"HPA": {func(f *fake) {
			f.cap = &model.Capacity{Method: "hdparm -N", Visible: fixtureSize/512 - 2048, Native: fixtureSize / 512}
		}, "keeps 2,048 sectors (1.05 MB) from the host", true},
	}
	for name, c := range cases {
		f := drive(t)
		f.table = model.Table{Parts: []model.Part{{Path: "/dev/sdc1", FSType: "ext4"}}}
		c.mod(f)
		s := Read(context.Background(), f, f.devs)
		if !strings.Contains(strings.Join(s.Refusals, " "), c.want) {
			t.Errorf("%s: refusals %v", name, s.Refusals)
		}
		if got := len(f.peeked) > 0; got != c.peek {
			t.Errorf("%s: peeked=%v", name, got)
		}
	}
}

func TestBackupSignatures(t *testing.T) {
	f := drive(t)
	f.table = model.Table{Parts: []model.Part{
		{Path: "/dev/sdc1", FSType: "ext4", Label: "backup-gamma"},
		{Path: "/dev/sdc2", FSType: "LVM2_member"},
		{Path: "/dev/sdc3", FSType: "vfat", Label: "EFI"},
	}}
	s := Read(context.Background(), f, f.devs)
	p := s.Drive.Table.Parts
	if p[0].Backup == "" || p[1].Backup == "" || p[2].Backup != "" {
		t.Errorf("backup flags: %q %q %q", p[0].Backup, p[1].Backup, p[2].Backup)
	}
	if s.Drive.LooksLikeBackup() == "" {
		t.Error("drive not flagged")
	}
}

func TestPollFilesOnceAndOnChange(t *testing.T) {
	f := drive(t)
	reads := 0
	b := &Bay{Name: "test", Src: f, OnRead: func(*model.Snapshot) (string, error) { reads++; return "rec", nil }}
	ctx := context.Background()
	b.Poll(ctx, false)
	b.Poll(ctx, false)
	if reads != 1 || b.Status().RecordID != "rec" {
		t.Fatalf("reads %d, status %+v", reads, b.Status())
	}
	session := b.Status().Session
	b.Poll(ctx, true) // Re-read: same insertion, same session
	if reads != 2 || !b.Status().Session.Equal(session) {
		t.Errorf("re-read: reads %d, session changed %v", reads, !b.Status().Session.Equal(session))
	}
	f.devs = nil
	b.Poll(ctx, false)
	if b.Status().State != model.Empty {
		t.Errorf("after removal: %s", b.Status().State)
	}
}

// The trap in `ark stamp`: inside a VM, the VM's own disks are sd* too.
// Only a path through the bay's USB port counts.
func TestSysDevicesOnlyThePort(t *testing.T) {
	root := t.TempDir()
	devices := filepath.Join(root, "devices")
	block := filepath.Join(root, "block")
	mk := func(name, path string, sectors string) {
		dir := filepath.Join(devices, path, "block", name)
		os.MkdirAll(filepath.Join(dir, "queue"), 0o755)
		os.WriteFile(filepath.Join(dir, "size"), []byte(sectors), 0o644)
		os.WriteFile(filepath.Join(dir, "queue/logical_block_size"), []byte("512"), 0o644)
		os.MkdirAll(block, 0o755)
		os.Symlink(dir, filepath.Join(block, name))
	}
	// The real paths on dock-host, 2026-10-23.
	mk("sda", "pci0000:00/0000:00:1e.0/0000:05:04.0/0000:09:0a.0/virtio4/host1/target1:0:0/1:0:0:9", "16777216")
	mk("sdb", "pci0000:00/0000:00:1e.0/0000:05:04.0/0000:09:01.0/virtio3/host0/target0:0:0/0:0:0:0", "134217728")
	mk("sdc", "pci0000:00/0000:00:1e.0/0000:05:02.0/0000:07:1b.0/usb10/10-1/10-1:1.0/host8/target8:0:0/8:0:0:0", "1953525168")
	// A second USB device on another port must not count either.
	mk("sdd", "pci0000:00/0000:00:1e.0/0000:05:02.0/0000:07:1b.0/usb10/10-2/10-2:1.0/host9/target9:0:0/9:0:0:0", "1000")

	s := &Sys{Port: "10-1", SysBlock: block}
	devs, err := s.Devices()
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Name != "sdc" || devs[0].Size != fixtureSize {
		t.Fatalf("devices %+v", devs)
	}
	if (&Sys{SysBlock: block}).usbDir("x") != "" {
		t.Error("empty port matched")
	}
	if _, err := (&Sys{SysBlock: block}).Devices(); err == nil {
		t.Error("no port configured, but Devices didn't refuse")
	}
}
