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
}

// Sanity bounds on size: anything outside is a bridge talking nonsense.
const (
	MinSize = 8 << 30
	MaxSize = 64 << 40
)

// Read looks at whatever is on the port.  It writes nothing to the drive.
func Read(ctx context.Context, src Source, devs []Device) model.Snapshot {
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

	if err := src.SetRO(d); err != nil {
		refuse("Couldn't set %s read-only (%v), so I won't mount anything on it to look inside.", d.Path, err)
	} else {
		drv.ReadOnly = true
	}

	js, text, err := src.SMART(ctx, d)
	s.SmartJSON, s.SmartText = js, text
	info, perr := smart.Parse(js)
	switch {
	case err != nil:
		refuse("SMART can't be read through this bridge (%v).  Without the drive's own serial there's nothing to file a record under.", err)
	case perr != nil:
		refuse("SMART can't be read through this bridge: %v.  Without the drive's own serial there's nothing to file a record under.", perr)
	default:
		drv.SMART = info
		drv.Health = smart.Assess(info)
		if info.CapacityBytes != d.Size {
			refuse("The bridge says %s but the drive says %s.  The bay's slot 0 has reported placeholder sizes before.  Reseat the drive and re-read.",
				records.Size(d.Size), records.Size(info.CapacityBytes))
		}
	}
	if d.Size < MinSize || d.Size > MaxSize {
		refuse("%s is outside what a real drive in this bay should be (8 GiB to 64 TiB).", records.Size(d.Size))
	}

	t, err := src.Table(ctx, d)
	if err != nil {
		t.Note = fmt.Sprintf("Couldn't read the partition table: %v", err)
	}
	for i := range t.Parts {
		p := &t.Parts[i]
		p.Used = -1
		if drv.ReadOnly {
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
