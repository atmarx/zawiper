// Package smart parses `smartctl -j` output and grades what it finds.
package smart

import (
	"encoding/json"
	"fmt"
	"strings"
)

// smartctl exit status bits (smartctl(8), "RETURN VALUES").  Bits 0 and 1
// mean smartctl couldn't talk to the drive at all; the rest are findings.
const (
	ExitCmdLine      = 1 << 0
	ExitOpenFailed   = 1 << 1
	ExitCmdFailed    = 1 << 2 // some SMART or ATA command failed
	ExitFailing      = 1 << 3 // overall health: FAILING
	ExitPrefailNow   = 1 << 4 // pre-fail attribute at or below threshold now
	ExitThreshPast   = 1 << 5 // an attribute was at or below threshold in the past
	ExitErrorLog     = 1 << 6 // the error log has entries
	ExitSelfTestErrs = 1 << 7 // the self-test log has errors
)

type Attr struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Value      int    `json:"value"`
	Worst      int    `json:"worst"`
	Thresh     int    `json:"thresh"`
	WhenFailed string `json:"when_failed"` // "", "now" or "past"
	Raw        int64  `json:"raw"`
	RawString  string `json:"raw_string"`
}

type Info struct {
	Model         string `json:"model"`
	Family        string `json:"family,omitempty"`
	Serial        string `json:"serial"`
	WWN           string `json:"wwn,omitempty"`
	Firmware      string `json:"firmware,omitempty"`
	CapacityBytes int64  `json:"capacity_bytes"`
	Protocol      string `json:"protocol,omitempty"` // ATA, SCSI or NVMe, as smartctl talked to it
	RotationRate  int    `json:"rotation_rate"`      // 0 is solid state
	// Link is the SATA link the drive reports, the current speed if it
	// says and its maximum if not: "3.0 Gb/s", and LinkBps in bits a second.
	Link          string `json:"link,omitempty"`
	LinkBps       int64  `json:"link_bps,omitempty"`
	Passed        *bool  `json:"passed"` // nil when the drive didn't say
	PowerOnHours  int    `json:"power_on_hours"`
	PowerCycles   int    `json:"power_cycles"`
	TempC         int    `json:"temp_c"`
	ErrorLogCount int    `json:"error_log_count"`
	Attrs         []Attr `json:"attrs"`
	ExitStatus    int    `json:"exit_status"`
	Version       string `json:"smartctl_version"`
}

// The subset of smartctl's JSON we read.
type rawJSON struct {
	Smartctl struct {
		Version    []int `json:"version"`
		ExitStatus int   `json:"exit_status"`
	} `json:"smartctl"`
	ModelFamily string `json:"model_family"`
	ModelName   string `json:"model_name"`
	Device      struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	SerialNumber string `json:"serial_number"`
	WWN          *struct {
		NAA int   `json:"naa"`
		OUI int64 `json:"oui"`
		ID  int64 `json:"id"`
	} `json:"wwn"`
	FirmwareVersion string `json:"firmware_version"`
	UserCapacity    struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	RotationRate   *int `json:"rotation_rate"`
	InterfaceSpeed struct {
		Max     *linkSpeed `json:"max"`
		Current *linkSpeed `json:"current"`
	} `json:"interface_speed"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	PowerOnTime struct {
		Hours int `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount int `json:"power_cycle_count"`
	Temperature     struct {
		Current int `json:"current"`
	} `json:"temperature"`
	Attributes struct {
		Table []struct {
			ID         int    `json:"id"`
			Name       string `json:"name"`
			Value      int    `json:"value"`
			Worst      int    `json:"worst"`
			Thresh     int    `json:"thresh"`
			WhenFailed string `json:"when_failed"`
			Raw        struct {
				Value  int64  `json:"value"`
				String string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	ErrorLog struct {
		Summary struct {
			Count int `json:"count"`
		} `json:"summary"`
	} `json:"ata_smart_error_log"`
}

type linkSpeed struct {
	String         string `json:"string"`
	UnitsPerSecond int64  `json:"units_per_second"`
	BitsPerUnit    int64  `json:"bits_per_unit"`
}

// Parse reads `smartctl -j` output.  It fails when smartctl couldn't reach
// the drive or the drive gave no serial, since a record is keyed on it.
func Parse(b []byte) (*Info, error) {
	var r rawJSON
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("smartctl output isn't JSON: %w", err)
	}
	if r.Smartctl.ExitStatus&(ExitCmdLine|ExitOpenFailed) != 0 {
		return nil, fmt.Errorf("smartctl couldn't open the drive (exit %d)", r.Smartctl.ExitStatus)
	}
	serial := strings.TrimSpace(r.SerialNumber)
	if serial == "" || strings.Trim(serial, "0") == "" {
		return nil, fmt.Errorf("the drive reported no serial number")
	}
	i := &Info{
		Model:         strings.TrimSpace(r.ModelName),
		Family:        r.ModelFamily,
		Protocol:      r.Device.Protocol,
		Serial:        serial,
		Firmware:      r.FirmwareVersion,
		CapacityBytes: r.UserCapacity.Bytes,
		PowerOnHours:  r.PowerOnTime.Hours,
		PowerCycles:   r.PowerCycleCount,
		TempC:         r.Temperature.Current,
		ErrorLogCount: r.ErrorLog.Summary.Count,
		ExitStatus:    r.Smartctl.ExitStatus,
	}
	if len(r.Smartctl.Version) >= 2 {
		i.Version = fmt.Sprintf("%d.%d", r.Smartctl.Version[0], r.Smartctl.Version[1])
	}
	if r.WWN != nil {
		i.WWN = fmt.Sprintf("%x %06x %09x", r.WWN.NAA, r.WWN.OUI, r.WWN.ID)
	}
	if r.RotationRate != nil {
		i.RotationRate = *r.RotationRate
	}
	for _, l := range []*linkSpeed{r.InterfaceSpeed.Current, r.InterfaceSpeed.Max} {
		if l != nil && l.UnitsPerSecond > 0 {
			i.Link, i.LinkBps = l.String, l.UnitsPerSecond*l.BitsPerUnit
			break
		}
	}
	if r.SmartStatus != nil {
		p := r.SmartStatus.Passed
		i.Passed = &p
	}
	for _, a := range r.Attributes.Table {
		i.Attrs = append(i.Attrs, Attr{
			ID: a.ID, Name: a.Name, Value: a.Value, Worst: a.Worst, Thresh: a.Thresh,
			WhenFailed: a.WhenFailed, Raw: a.Raw.Value, RawString: a.Raw.String,
		})
	}
	return i, nil
}

// Attr returns the attribute with this ID, if the drive reports it.
func (i *Info) Attr(id int) (Attr, bool) {
	for _, a := range i.Attrs {
		if a.ID == id {
			return a, true
		}
	}
	return Attr{}, false
}

type Grade string

const (
	Pass  Grade = "PASS"
	Notes Grade = "PASS WITH NOTES"
	Fail  Grade = "FAIL — DO NOT USE"
)

type Health struct {
	Grade Grade    `json:"grade"`
	Notes []string `json:"notes"`
}

// Counts that should be zero on a healthy drive.  Non-zero is a note, not a
// failure: an inspect can't tell a stable old scar from a drive that's dying.
// Only a wipe (before/after) can see a count rising.
var watched = []struct {
	id   int
	what string
}{
	{5, "reallocated sectors"},
	{197, "sectors pending reallocation"},
	{198, "offline-uncorrectable sectors"},
	{187, "reported-uncorrectable errors"},
	{199, "interface CRC errors (usually the cable or the bridge, not the platters)"},
}

// HotC is where a drive in a closed caddy has started cooking.
const HotC = 50

// Assess grades a drive from SMART alone.
func Assess(i *Info) Health {
	h := Health{Grade: Pass}
	note := func(s string, a ...any) {
		h.Notes = append(h.Notes, fmt.Sprintf(s, a...))
		if h.Grade == Pass {
			h.Grade = Notes
		}
	}
	fail := func(s string, a ...any) {
		h.Notes = append(h.Notes, fmt.Sprintf(s, a...))
		h.Grade = Fail
	}

	if (i.Passed != nil && !*i.Passed) || i.ExitStatus&ExitFailing != 0 {
		fail("The drive's own health check says FAILING.")
	}
	for _, a := range i.Attrs {
		switch a.WhenFailed {
		case "now":
			fail("%s is at or below its failure threshold right now.", a.Name)
		case "past":
			note("%s crossed its threshold in the past (worst %d, threshold %d).", a.Name, a.Worst, a.Thresh)
		}
	}
	for _, w := range watched {
		if a, ok := i.Attr(w.id); ok && a.Raw > 0 {
			note("%d %s.", a.Raw, w.what)
		}
	}
	if i.TempC >= HotC {
		note("Running hot: %d °C.", i.TempC)
	}
	if i.ErrorLogCount > 0 {
		note("%d entries in the drive's error log.", i.ErrorLogCount)
	}
	if i.ExitStatus&ExitSelfTestErrs != 0 {
		note("The self-test log records errors.")
	}
	if i.ExitStatus&ExitCmdFailed != 0 {
		note("Some SMART commands failed through this bridge, so the picture may be incomplete.")
	}
	if i.Passed == nil {
		note("The drive didn't report an overall health status.")
	}
	return h
}

// Rising compares SMART from before and after a wipe.  A watched count that
// climbs while every sector is written and read back is a drive getting
// worse under load: that fails it, except for CRC errors, which belong to
// the cable or the bridge.
func Rising(before, after *Info) (fails, notes []string) {
	for _, w := range watched {
		a, ok1 := before.Attr(w.id)
		b, ok2 := after.Attr(w.id)
		if !ok1 || !ok2 || b.Raw <= a.Raw {
			continue
		}
		msg := fmt.Sprintf("%s rose during the wipe, %d → %d.", w.what, a.Raw, b.Raw)
		if w.id == 199 {
			notes = append(notes, msg)
		} else {
			fails = append(fails, msg)
		}
	}
	if after.ErrorLogCount > before.ErrorLogCount {
		notes = append(notes, fmt.Sprintf("The drive's error log grew during the wipe, %d → %d entries.", before.ErrorLogCount, after.ErrorLogCount))
	}
	return fails, notes
}
