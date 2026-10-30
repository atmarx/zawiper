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
	s := Read(context.Background(), f, f.devs, ReadOpts{})
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
		// A drive that doesn't answer, or disagrees about its size, gets no
		// further reads: no peek.
		"no smart": {func(f *fake) { f.smart = []byte(`{"smartctl":{"exit_status":2}}`) }, "didn't answer SMART", false},
		// A two-slot ASM1156 dock has reported 4.14 GB in both slots.
		"placeholder size": {func(f *fake) { f.devs[0].Size = 4140000000 }, "placeholder", false},
		"HPA": {func(f *fake) {
			f.cap = &model.Capacity{Method: "hdparm -N", Visible: fixtureSize/512 - 2048, Native: fixtureSize / 512}
		}, "keeps 2,048 sectors (1.05 MB) from the host behind a host protected area.  A wipe wouldn't reach them, so it couldn't count as Clear.  Restore the full size with `hdparm -N p1953525168`", true},
		// -N can't lift a DCO, so the refusal mustn't send anyone there.
		"DCO": {func(f *fake) {
			f.cap = &model.Capacity{Method: "hdparm -N, hdparm --dco-identify", Visible: fixtureSize / 512, Native: fixtureSize / 512, DCO: fixtureSize/512 + 32}
		}, "overlay says it has 1,953,525,200 sectors, 32 more than it admits to (16.38 KB).  A wipe wouldn't reach them, so it couldn't count as Clear.  hdparm -N can't give them back: `hdparm --yes-i-know-what-i-am-doing --dco-restore`", true},
		// The EDGE SE847: no HPA, and a DCO one sector over (2026-10-29).
		"DCO off by one": {func(f *fake) {
			f.cap = &model.Capacity{Method: "hdparm -N, hdparm --dco-identify", Visible: fixtureSize / 512, Native: fixtureSize / 512, DCO: fixtureSize/512 + 1}
		}, "1 more than it admits to (512 B).  A wipe wouldn't reach them, so it couldn't count as Clear.  hdparm -N can't give them back: `hdparm --yes-i-know-what-i-am-doing --dco-restore` can, then power-cycle the drive and re-read.  A gap of exactly one", true},
	}
	for name, c := range cases {
		f := drive(t)
		f.table = model.Table{Parts: []model.Part{{Path: "/dev/sdc1", FSType: "ext4"}}}
		c.mod(f)
		s := Read(context.Background(), f, f.devs, ReadOpts{})
		if !strings.Contains(strings.Join(s.Refusals, " "), c.want) {
			t.Errorf("%s: refusals %v", name, s.Refusals)
		}
		if got := len(f.peeked) > 0; got != c.peek {
			t.Errorf("%s: peeked=%v", name, got)
		}
	}
}

// A drive that doesn't answer says so on the wall as it goes, and its
// refusal warns about the slot next door.
func TestReadStepsAndShares(t *testing.T) {
	f := drive(t)
	f.table = model.Table{Parts: []model.Part{{Path: "/dev/sdc1", FSType: "ext4"}}}
	var steps []string
	s := Read(context.Background(), f, f.devs, ReadOpts{Step: func(m string) { steps = append(steps, m) }, Shares: []string{"bay1"}})
	if len(s.Refusals) != 0 {
		t.Fatalf("a good drive: %v", s.Refusals)
	}
	if len(steps) != 5 || !strings.HasPrefix(steps[1], "Asking SMART (smartctl), since ") || !strings.HasPrefix(steps[4], "Looking inside /dev/sdc1 read-only, since ") {
		t.Errorf("steps %q", steps)
	}

	f = drive(t)
	f.smart = []byte(`{"smartctl":{"exit_status":2}}`)
	s = Read(context.Background(), f, f.devs, ReadOpts{Shares: []string{"bay1"}})
	if r := strings.Join(s.Refusals, " "); !strings.Contains(r, "shares a bridge with bay1.  Take this drive out, or it can stall bay1.") {
		t.Errorf("refusals %v", s.Refusals)
	}
	f = drive(t)
	f.cap = &model.Capacity{Method: "hdparm -N", Visible: fixtureSize/512 - 2048, Native: fixtureSize / 512}
	s = Read(context.Background(), f, f.devs, ReadOpts{Shares: []string{"bay1"}})
	if r := strings.Join(s.Refusals, " "); strings.Contains(r, "shares a bridge") {
		t.Errorf("an HPA doesn't stall the bridge: %v", s.Refusals)
	}
}

func TestBackupSignatures(t *testing.T) {
	f := drive(t)
	f.table = model.Table{Parts: []model.Part{
		{Path: "/dev/sdc1", FSType: "ext4", Label: "backup-gamma"},
		{Path: "/dev/sdc2", FSType: "LVM2_member"},
		{Path: "/dev/sdc3", FSType: "vfat", Label: "EFI"},
	}}
	s := Read(context.Background(), f, f.devs, ReadOpts{})
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

// filtered is a drive the device filter won't let bayd open.
type filtered struct {
	*fake
	restarted bool
}

func (f *filtered) Reach(context.Context, Device) error { return ErrFiltered }
func (f *filtered) Restarted(Device) bool {
	was := f.restarted
	f.restarted = true
	return was
}

// A stale filter restarts bayd once per insertion.  If the rebuilt filter
// still refuses, the bay blames the filter, not the bridge, and stays up.
func TestPollStaleFilter(t *testing.T) {
	f := &filtered{fake: drive(t)}
	restarts := 0
	b := &Bay{Name: "test", Src: f, Restart: func() { restarts++ }}
	ctx := context.Background()
	b.Poll(ctx, false)
	if restarts != 1 || f.ro {
		t.Fatalf("first refusal: restarts %d, read anyway %v", restarts, f.ro)
	}
	b.Poll(ctx, true)
	s := b.Status()
	if restarts != 1 {
		t.Errorf("restarted again for the same drive: %d", restarts)
	}
	if s.State != model.Present || len(s.Refusals) != 1 || !strings.Contains(s.Refusals[0], "device filter") || strings.Contains(s.Refusals[0], "bridge can't") {
		t.Errorf("second refusal: %s %q", s.State, s.Refusals)
	}
	if f.ro {
		t.Error("went on to set read-only through a filter that refuses")
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
	// The real paths on lab-a, 2026-10-23.
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
