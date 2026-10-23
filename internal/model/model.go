// Package model holds the types bayd, zaweb and the records share.
package model

import (
	"time"

	"zawiper/internal/smart"
)

type State string

const (
	Empty   State = "EMPTY"
	Reading State = "READING"
	Present State = "PRESENT"
)

// Bridge is what the USB-to-SATA bridge says about itself.  Never identity:
// bridges ship with constant or blank serials.
type Bridge struct {
	VendorID     string `json:"vendor_id,omitempty"`
	ProductID    string `json:"product_id,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Serial       string `json:"serial,omitempty"`
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
}

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

	// Raw smartctl output, kept for the record and not sent over the socket.
	SmartJSON []byte `json:"-"`
	SmartText []byte `json:"-"`
}
