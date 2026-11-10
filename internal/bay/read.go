// Package bay finds the drive in the bay and reads it without writing.
package bay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/smart"
)

// Device is a block device on the bay's USB port.
type Device struct {
	Name     string // sdc
	Path     string // /dev/sdc
	Size     int64
	Logical  int
	Physical int
}

// Source is everything bayd asks of the machine.  The real one reads sysfs
// and runs smartctl; tests and dev mode supply their own.
type Source interface {
	// Devices lists block devices on the bay's port, and nothing else.
	Devices() ([]Device, error)
	Bridge(d Device) model.Bridge
	// InUse says why the host is using the device, or "" if it isn't.
	InUse(d Device) (string, error)
	// SetRO sets the kernel's read-only flag on the device.
	SetRO(d Device) error
	SMART(ctx context.Context, d Device) (js, text []byte, err error)
	Table(ctx context.Context, d Device) (model.Table, error)
	// Peek mounts a filesystem read-only, somewhere private, to measure it.
	Peek(ctx context.Context, p *model.Part)

	// The job's side.  SetRW clears the read-only flag for a wipe, Forget
	// drops the kernel's copy of a partition table the wipe destroyed, and
	// Eject flushes the drive and lets it go.
	SetRW(d Device) error
	Forget(d Device) error
	Eject(d Device) error
}

// Sanity bounds on size: anything outside is a bridge talking nonsense.
const (
	MinSize = 8 << 30
	MaxSize = 64 << 40
)

// bounded is a Source with its own idea of a sane size.  Only dev mode has
// one: its "drive" is a file, so a bridge can't be lying about it.
type bounded interface {
	Bounds() (min, max int64)
}

// capacity is a Source that can ask a drive whether it keeps sectors from
// the host.  Optional, so fakes needn't.
type capacity interface {
	Capacity(ctx context.Context, d Device, protocol string) *model.Capacity
}

// ErrFiltered is the kernel's device filter refusing bayd its own drive.
// The filter is built from /dev/zawiper/<bay> when bayd@ starts; a drive
// that comes up in stages (a zero-size disk, then a bridge reset, then the
// real one) can leave it holding numbers the drive no longer has.
var ErrFiltered = errors.New("the kernel's device filter refused it")

// filter is a Source that can tell the device filter apart from the drive.
// Optional, so fakes needn't.
type filter interface {
	// Reach opens the drive and closes it, waiting a moment for the
	// filter.  ErrFiltered means it never let bayd in.
	Reach(ctx context.Context, d Device) error
	// Restarted reports whether bayd already restarted for the drive as it
	// sits now, and notes that it has.
	Restarted(d Device) bool
}

func bounds(src Source) (int64, int64) {
	if b, ok := src.(bounded); ok {
		return b.Bounds()
	}
	return MinSize, MaxSize
}

// ReadOpts is what a read is told beyond the drive.
type ReadOpts struct {
	// Step says what the read is doing now, for the wall.  A drive that has
	// stopped answering shows up as one step that doesn't end.
	Step func(string)
	// Shares names the other bays behind the same bridge.  A drive that
	// doesn't answer stalls them too: the kernel's error handling stops the
	// whole SCSI host while it recovers.
	Shares []string
}

func (o ReadOpts) step(what string) {
	if o.Step != nil {
		o.Step(fmt.Sprintf("%s, since %s.", what, time.Now().Format("15:04:05")))
	}
}

// Read looks at whatever is on the port.  It writes nothing to the drive.
func Read(ctx context.Context, src Source, devs []Device, o ReadOpts) model.Snapshot {
	s := model.Snapshot{State: model.Present, ReadAt: time.Now().UTC()}
	refuse := func(f string, a ...any) { s.Refusals = append(s.Refusals, fmt.Sprintf(f, a...)) }

	if len(devs) > 1 {
		for _, d := range devs {
			s.Devices = append(s.Devices, fmt.Sprintf("%s (%s)", d.Path, records.Size(d.Size)))
		}
		refuse("There are %d drives on the bay's port.  Za Wiper handles one at a time, and won't guess which one you meant.  Take one out.", len(devs))
		return s
	}
	d := devs[0]
	if d.Size == 0 {
		s.State = model.Reading
		s.Message = "Waiting for the drive to report its size."
		return s
	}

	drv := &model.Drive{Dev: d.Path, Size: d.Size, LogicalSector: d.Logical, PhysicalSector: d.Physical, Bridge: src.Bridge(d)}
	s.Drive = drv

	why, err := src.InUse(d)
	if err != nil {
		refuse("Couldn't check whether the host is using %s: %v.  Not touching it.", d.Path, err)
		return s
	}
	if why != "" {
		refuse("The host is using %s (%s).  That makes it not the bay's drive.  Not touching it.", d.Path, why)
		return s
	}

	if f, ok := src.(filter); ok {
		if err := f.Reach(ctx, d); errors.Is(err, ErrFiltered) {
			refuse("The kernel won't let this bay open its own drive (%v).  That's the device filter systemd built when bayd started, not the drive or the bridge, and a restart that rebuilt it didn't help.  Compare `systemctl show bayd@<bay> -p DeviceAllow` with what's in /dev/zawiper/.", err)
			return s
		}
	}

	// Read-only, so peeking inside can't write.  A drive that just went in
	// can beat udev, which hasn't yet let this unit open its partitions:
	// wait for it.  If it still won't, nothing gets mounted, and that's a
	// note, not a refusal: the wipe doesn't need it.
	o.step("Setting it read-only")
	var notes []smart.Finding
	err = src.SetRO(d)
	for _, w := range roWaits {
		if err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(w)
		err = src.SetRO(d)
	}
	if err != nil {
		notes = append(notes, smart.Finding{Tag: "NO-PEEK", About: smart.Job,
			Text: fmt.Sprintf("Couldn't set %s read-only (%v), so nothing on it was mounted to look inside.  The partitions are still listed.", d.Path, err)})
	} else {
		drv.ReadOnly = true
	}

	// A drive that doesn't answer SMART, or can't agree on its own size,
	// gets no further reads.  It can't be filed or wiped anyway, and every
	// command it lets time out stalls the bridge.
	balky := false
	o.step("Asking SMART (smartctl)")
	asked := time.Now()
	js, text, err := src.SMART(ctx, d)
	took := time.Since(asked).Round(time.Second)
	s.SmartJSON, s.SmartText = js, text
	info, perr := smart.Parse(js)
	switch {
	case err != nil:
		refuse("smartctl didn't run (%v).  Without the drive's own serial there's nothing to file a record under.", err)
		balky = true
	case perr != nil:
		refuse("The drive didn't answer SMART: %v, after %s.  A failing drive does this, and so does one that hasn't come up to speed, or a bridge that can't pass SMART through.  Without the drive's own serial there's nothing to file a record under.", perr, took)
		balky = true
	default:
		drv.SMART = info
		drv.Health = smart.Assess(info)
		for _, f := range notes {
			drv.Health.Add(f)
		}
		if info.CapacityBytes != d.Size {
			refuse("The bridge says %s but the drive says %s.  A bridge falls back to a placeholder size when the drive doesn't identify itself properly as it comes up.  Reseat the drive and re-read, and if it holds, try it on another port.",
				records.Size(d.Size), records.Size(info.CapacityBytes))
			balky = true
		}
	}
	if lo, hi := bounds(src); d.Size < lo || d.Size > hi {
		refuse("%s is outside what a real drive in this bay should be (%s to %s).", records.Size(d.Size), binary(lo), binary(hi))
		balky = true
	}
	if balky {
		if len(o.Shares) > 0 {
			who := strings.Join(o.Shares, " and ")
			refuse("This slot shares a bridge with %s.  Take this drive out, or it can stall %s.", who, who)
		}
		return s
	}

	if c, ok := src.(capacity); ok {
		o.step("Checking for hidden sectors (hdparm)")
		drv.Capacity = c.Capacity(ctx, d, info.Protocol)
		if c := drv.Capacity; c.Checked() {
			// Hidden sectors don't stop a wipe.  It covers everything
			// else, and its record withholds Clear (records.Sanitize).
			if n := c.HPA(); n > 0 {
				drv.Health.Add(smart.Finding{Tag: "HPA", Value: sectors(n), About: smart.Drive,
					Text: fmt.Sprintf("The drive keeps %s sectors (%s) from the host behind a host protected area.  A wipe covers everything else but not those, so its record can't claim Clear.  To reach them: `hdparm -N p%d`, power-cycle the drive, and re-read.",
						records.Commas(n), records.Size(n*512), c.Native)})
			}
			if n := c.DCOHidden(); n > 0 {
				why := ""
				if n == 1 {
					why = "  A gap of exactly one is also what firmware reports when it fills in DCO IDENTIFY with a count where the standard asks for the last address.  If the restore goes through and the gap is still there, that's all this is."
				}
				drv.Health.Add(smart.Finding{Tag: "DCO", Value: sectors(n), About: smart.Drive,
					Text: fmt.Sprintf("The drive's device configuration overlay says it has %s sectors, %s more than it admits to (%s).  A wipe covers everything else but not those, so its record can't claim Clear.  hdparm -N can't give them back: `hdparm --yes-i-know-what-i-am-doing --dco-restore` can, on a bridge that passes it through, then power-cycle the drive and re-read.%s",
						records.Commas(c.DCO), records.Commas(n), records.Size(n*512), why)})
			}
		}
	}

	o.step("Reading the partition table (blkid)")
	t, err := src.Table(ctx, d)
	if err != nil {
		t.Note = fmt.Sprintf("Couldn't read the partition table: %v", err)
	}
	for i := range t.Parts {
		p := &t.Parts[i]
		p.Used = -1
		if drv.ReadOnly {
			o.step("Looking inside " + p.Path + " read-only")
			src.Peek(ctx, p)
		} else {
			p.Peek = "not mounted: the device isn't read-only"
		}
		if p.Backup == "" {
			p.Backup = backupBySignature(p)
		}
	}
	drv.Table = t
	return s
}

// binary names a bound the way it was set: 8 GiB, 64 TiB.
func binary(n int64) string {
	units := []string{"bytes", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for n >= 1024 && n%1024 == 0 && i < len(units)-1 {
		n /= 1024
		i++
	}
	return fmt.Sprintf("%d %s", n, units[i])
}

// Cheap backup tells that need no mount.  The peek adds restic and PBS.
func backupBySignature(p *model.Part) string {
	switch {
	case strings.HasPrefix(strings.ToLower(p.Label), "backup-"):
		return fmt.Sprintf("labelled %q, the backup rotation's naming", p.Label)
	case p.FSType == "LVM2_member", p.FSType == "zfs_member", p.FSType == "linux_raid_member":
		return fmt.Sprintf("a %s, which could be holding anything, backups included", p.FSType)
	case p.FSType == "crypto_LUKS":
		return "encrypted (LUKS), so I can't see what's inside"
	}
	return ""
}

// roWaits are the pauses before each retry of read-only, while udev catches
// up with a drive that just went in.
var roWaits = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

func sectors(n int64) string {
	if n == 1 {
		return "1 sector"
	}
	return records.Commas(n) + " sectors"
}
