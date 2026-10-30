// Package model holds the types bayd, zaweb and the records share.
package model

import (
	"time"

	"github.com/atmarx/zawiper/internal/smart"
)

type State string

const (
	Empty   State = "EMPTY"
	Reading State = "READING"
	Present State = "PRESENT"

	// A job, and how it ended (spec §5).  DONE, FAILED, INTERRUPTED and
	// ALARM hold until someone acknowledges them.
	Running     State = "RUNNING"
	Done        State = "DONE"
	Failed      State = "FAILED"
	Interrupted State = "INTERRUPTED"
	Alarm       State = "ALARM"

	// Offline is the hub's word for a bay whose link is down.  No bay
	// ever reports it about itself.
	Offline State = "OFFLINE"
)

// Surface-map cells, one byte each in Job.Map, as the wall draws them.
const (
	CellTodo     = '.' // not reached yet
	CellWriting  = 'W'
	CellWritten  = 'w'
	CellReading  = 'R'
	CellVerified = 'v'
	CellSlow     = 's' // marked when the job ends: slowness needs neighbours
	CellBad      = 'B'
)

// Job is a running or finished job, as the wall shows it.  bayd writes it to
// the bay's state file before every transition.
type Job struct {
	Kind    string    `json:"kind"`  // wipe
	Phase   string    `json:"phase"` // smart-before, write, verify, smart-after, record
	Serial  string    `json:"serial"`
	Session time.Time `json:"session"` // the insertion the confirm was drawn from
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`

	Map   string  `json:"map"`   // one Cell* byte per block
	Done  int64   `json:"done"`  // bytes through both passes, out of 2×Total
	Total int64   `json:"total"` // the drive's size
	MBps  float64 `json:"mbps"`
	ETA   int64   `json:"eta_s"`
	TempC int     `json:"temp_c,omitempty"`
	Bad   int64   `json:"bad"`
	// Each block's speed in MB/s, per pass, for the wall's throughput
	// graph.  Zero until the pass reaches the block.
	WriteMBps []uint16 `json:"write_mbps,omitempty"`
	ReadMBps  []uint16 `json:"read_mbps,omitempty"`

	// What the job costs the dock host, sampled every poll: bayd's own CPU
	// as a percentage of one core (top's way), the whole host's as a
	// percentage of all its cores, and the one-minute load average.
	CPU     float64 `json:"cpu,omitempty"`
	HostCPU float64 `json:"host_cpu,omitempty"`
	Cores   int     `json:"cores,omitempty"`
	Load    float64 `json:"load,omitempty"`

	// Evidence is what ended a FAILED or ALARM job: phase, offset, and what
	// the drive said.
	Evidence string `json:"evidence,omitempty"`
	RecordID string `json:"record_id,omitempty"`
}

// JobFile is /var/lib/zawiper/<bay>/job.json.  The disk side writes it
// before every transition.  When the disk side isn't running, the link reads
// it: a RUNNING job with nobody running it is INTERRUPTED.
type JobFile struct {
	State State `json:"state"`
	Job   Job   `json:"job"`
}

// Bridge is what the USB-to-SATA bridge says about itself.  Never identity:
// bridges ship with constant or blank serials.
type Bridge struct {
	VendorID     string `json:"vendor_id,omitempty"`
	ProductID    string `json:"product_id,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
	// Speed is the USB link as negotiated, in Mb/s: 480 for USB 2, 5000
	// for USB 3's first speed.  Zero when it isn't USB or wasn't read.
	Speed int `json:"speed_mbps,omitempty"`
}

// Part is a partition, or the whole disk when a filesystem sits on it bare.
type Part struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	FSType string `json:"fstype,omitempty"`
	Label  string `json:"label,omitempty"`
	UUID   string `json:"uuid,omitempty"`
	// Used is bytes in use, measured by a read-only mount.  -1 when unknown.
	Used int64 `json:"used"`
	// Peek says how Used was or wasn't measured.
	Peek string `json:"peek,omitempty"`
	// Backup is why this looks like backup media, if it does.
	Backup string `json:"backup,omitempty"`
}

type Table struct {
	Type  string `json:"type,omitempty"` // gpt, dos, or "" for none
	Parts []Part `json:"parts"`
	Note  string `json:"note,omitempty"`
}

type Drive struct {
	Dev            string       `json:"dev"`
	Size           int64        `json:"size"`
	LogicalSector  int          `json:"logical_sector"`
	PhysicalSector int          `json:"physical_sector"`
	Bridge         Bridge       `json:"bridge"`
	SMART          *smart.Info  `json:"smart,omitempty"`
	Health         smart.Health `json:"health"`
	Table          Table        `json:"table"`
	ReadOnly       bool         `json:"read_only"` // the kernel's ro flag is set
	Capacity       *Capacity    `json:"capacity,omitempty"`
}

// Capacity is whether the drive keeps sectors from the host.  IEEE 2883
// §8.4.2.2 a) wants a host protected area (HPA) or a device configuration
// overlay (DCO) out of the way before an overwrite of an ATA drive counts
// as Clear.  Sectors are 512 bytes, as hdparm counts them.
type Capacity struct {
	Method  string `json:"method"`            // how it was checked
	Visible int64  `json:"visible,omitempty"` // sectors the host can address
	Native  int64  `json:"native,omitempty"`  // sectors the drive has, past any HPA (hdparm -N)
	// DCO is the drive's factory size from DEVICE CONFIGURATION IDENTIFY,
	// set only when it's more than Native: a DCO holding sectors back,
	// which -N can't give back.  Records from before 2026-10-30 folded it
	// into Native.
	DCO  int64  `json:"dco,omitempty"`
	Note string `json:"note,omitempty"` // why it couldn't be checked, when it couldn't
}

// HPA is how many sectors a host protected area keeps back: what
// hdparm -N p<Native> restores.
func (c *Capacity) HPA() int64 { return c.Native - c.Visible }

// DCOHidden is how many more a DCO keeps back, past the HPA.
func (c *Capacity) DCOHidden() int64 { return max(0, c.DCO-c.Native) }

// Checked says the drive answered.
func (c *Capacity) Checked() bool { return c != nil && c.Note == "" && c.Native > 0 && c.Visible > 0 }

// Hidden is how many sectors the host can't reach.
func (c *Capacity) Hidden() int64 { return c.HPA() + c.DCOHidden() }

func (d *Drive) Serial() string {
	if d == nil || d.SMART == nil {
		return ""
	}
	return d.SMART.Serial
}

// LooksLikeBackup is the first reason any filesystem looks like backup media.
func (d *Drive) LooksLikeBackup() string {
	for _, p := range d.Table.Parts {
		if p.Backup != "" {
			return p.Backup
		}
	}
	return ""
}

// UsedBytes totals what the peek measured, and says whether any part was
// left unmeasured.
func (d *Drive) UsedBytes() (total int64, complete bool) {
	complete = true
	for _, p := range d.Table.Parts {
		if p.Used < 0 {
			complete = false
			continue
		}
		total += p.Used
	}
	return total, complete
}

// Snapshot is the bay as bayd sees it right now.
type Snapshot struct {
	State    State     `json:"state"`
	Message  string    `json:"message,omitempty"`
	Bay      string    `json:"bay"`
	Session  time.Time `json:"session"` // when this drive was inserted
	ReadAt   time.Time `json:"read_at"`
	Drive    *Drive    `json:"drive,omitempty"`
	Devices  []string  `json:"devices,omitempty"` // when there is more than one
	Refusals []string  `json:"refusals,omitempty"`
	RecordID string    `json:"record_id,omitempty"`
	Host     string    `json:"host,omitempty"`
	Job      *Job      `json:"job,omitempty"` // RUNNING and the four endings

	// Raw smartctl output, kept for the record and not sent over the socket.
	SmartJSON []byte `json:"-"`
	SmartText []byte `json:"-"`
}
