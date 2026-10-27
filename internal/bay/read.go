// Package bay finds the drive in the bay and reads it without writing.
package bay

import (
	"context"
	"fmt"
	"strings"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/records"
	"zawiper/internal/smart"
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

	o.step("Setting it read-only")
	if err := src.SetRO(d); err != nil {
		refuse("Couldn't set %s read-only (%v), so I won't mount anything on it to look inside.", d.Path, err)
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
		if drv.Capacity.Checked() && drv.Capacity.Hidden() > 0 {
			refuse("The drive keeps %s sectors (%s) from the host behind an HPA or DCO.  A wipe wouldn't reach them, so it couldn't count as Clear.  Restore the full size with `hdparm -N p%d` (and `hdparm --dco-restore` for a DCO), power-cycle the drive, and re-read.",
				records.Commas(drv.Capacity.Hidden()), records.Size(drv.Capacity.Hidden()*512), drv.Capacity.Native)
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
