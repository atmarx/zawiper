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
	Link         string `json:"link,omitempty"`
	LinkBps      int64  `json:"link_bps,omitempty"`
	Passed       *bool  `json:"passed"` // nil when the drive didn't say
	PowerOnHours int    `json:"power_on_hours"`
	PowerCycles  int    `json:"power_cycles"`
	TempC        int    `json:"temp_c"`
	// MaxTempC is SCT's recommended maximum, and WarnTempC is NVMe's warning
	// threshold.  Zero when the drive doesn't say.
	MaxTempC  int `json:"max_temp_c,omitempty"`
	WarnTempC int `json:"warn_temp_c,omitempty"`
	// What only NVMe says.  Total and unallocated are the controller's, and
	// zero on a drive without namespace management.
	MediaErrors     int64  `json:"media_errors,omitempty"`
	PercentUsed     int    `json:"percent_used,omitempty"`
	NVMeTotal       int64  `json:"nvme_total,omitempty"`
	NVMeUnallocated int64  `json:"nvme_unallocated,omitempty"`
	ErrorLogCount   int    `json:"error_log_count"`
	Attrs           []Attr `json:"attrs"`
	ExitStatus      int    `json:"exit_status"`
	Version         string `json:"smartctl_version"`
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
	NVMeThreshold   struct {
		Warning int `json:"warning"`
	} `json:"nvme_composite_temperature_threshold"`
	// Floats, because smartctl prints these 128-bit counters as bare numbers,
	// and a bridge's nonsense in one shouldn't sink the whole read.
	NVMeTotal       float64 `json:"nvme_total_capacity"`
	NVMeUnallocated float64 `json:"nvme_unallocated_capacity"`
	NVMeHealth      struct {
		CriticalWarning int     `json:"critical_warning"`
		PercentageUsed  int     `json:"percentage_used"`
		MediaErrors     float64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	Temperature struct {
		OpLimitMax int `json:"op_limit_max"` // SCT's recommended maximum; NVMe's warning threshold
		Current    int `json:"current"`
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

// NVMe is the protocol smartctl names for an NVMe drive, bridged or not.
const NVMe = "NVMe"

// nvmeWarnTemperature is the temperature bit of NVMe's critical warning.
const nvmeWarnTemperature = 0x02

// NVMeHidden is how much of the controller's capacity this namespace doesn't
// show: unallocated, or in another namespace.  A wipe reaches neither.
func (i *Info) NVMeHidden() int64 {
	return max(i.NVMeUnallocated, i.NVMeTotal-i.CapacityBytes)
}

// Parse reads `smartctl -j` output.  It fails when smartctl couldn't reach
// the drive or the drive gave no serial, since a record is keyed on it.
func Parse(b []byte) (*Info, error) {
	var r rawJSON
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("smartctl output isn't JSON: %w", err)
	}
	if r.Smartctl.ExitStatus&(ExitCmdLine|ExitOpenFailed) != 0 {
		return nil, fmt.Errorf("smartctl couldn't open it or get it to identify itself (exit %d)", r.Smartctl.ExitStatus)
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
		MaxTempC:      r.Temperature.OpLimitMax,
		WarnTempC:     r.NVMeThreshold.Warning,
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
	if i.Protocol == NVMe {
		i.MediaErrors = int64(r.NVMeHealth.MediaErrors)
		i.PercentUsed = r.NVMeHealth.PercentageUsed
		i.NVMeTotal, i.NVMeUnallocated = int64(r.NVMeTotal), int64(r.NVMeUnallocated)
		if i.WarnTempC == 0 {
			i.WarnTempC = i.MaxTempC
		}
		// smartctl calls any critical warning a failed health check.  A
		// temperature warning alone only says the drive is hot right now,
		// which Assess reports as that.
		if r.NVMeHealth.CriticalWarning == nvmeWarnTemperature {
			passed := true
			i.Passed = &passed
			i.ExitStatus &^= ExitFailing
		}
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
	// Findings are the notes as tags, for the pills beside the grade (spec
	// §6.4).  Each one's Text is also in Notes, which records signed
	// before findings existed carry alone.
	Findings []Finding `json:"findings,omitempty"`
}

// About is whose a finding is.  A buyer reads a note against the drive
// differently from one against the cable, the bridge or the job.
type About string

const (
	Drive  About = "drive"
	Bridge About = "bridge"
	Job    About = "job"
)

// Finding is one note behind a grade.  Value is what was measured, never a
// score: "8", "52 °C", "0→6" for a count that rose during the job.
type Finding struct {
	Tag   string `json:"tag"`
	Value string `json:"value,omitempty"`
	Fail  bool   `json:"fail,omitempty"`
	About About  `json:"about"`
	Text  string `json:"text"`
}

// Add files a finding.  A failing one fails the grade; any other takes a
// PASS down to PASS WITH NOTES.
func (h *Health) Add(f Finding) {
	h.Findings = append(h.Findings, f)
	h.Notes = append(h.Notes, f.Text)
	switch {
	case f.Fail:
		h.Grade = Fail
	case h.Grade == Pass:
		h.Grade = Notes
	}
}

// Tagged gives notes from before findings existed (bayd before Oct 29) the
// tags they'd carry now, for the pills.  A note today's checks word the same
// way takes that check's tag; any other is a plain NOTE.  The words are the
// note's own either way.  Display only: the signed record keeps what it said.
func (h Health) Tagged(again []Finding) []Finding {
	if len(h.Findings) > 0 || len(h.Notes) == 0 {
		return h.Findings
	}
	byText := map[string]Finding{}
	for _, f := range again {
		byText[f.Text] = f
	}
	out := make([]Finding, 0, len(h.Notes))
	for _, n := range h.Notes {
		f, ok := byText[n]
		if !ok {
			f = Finding{Tag: "NOTE", Text: n}
		}
		out = append(out, f)
	}
	return out
}

// Lead puts a finding first, leaving the grade alone: how a job ended goes
// above what the grade found.
func (h *Health) Lead(f Finding) {
	h.Findings = append([]Finding{f}, h.Findings...)
	h.Notes = append([]string{f.Text}, h.Notes...)
}

// Counts that should be zero on a healthy drive.  Non-zero is a note, not a
// failure: an inspect can't tell a stable old scar from a drive that's dying.
// Only a wipe (before/after) can see a count rising.
var watched = []struct {
	id    int
	tag   string
	about About
	what  string
}{
	{5, "REALLOC", Drive, "reallocated sectors"},
	{197, "PENDING", Drive, "sectors pending reallocation"},
	{198, "OFFLINE-UNC", Drive, "offline-uncorrectable sectors"},
	{187, "UNCORR", Drive, "reported-uncorrectable errors"},
	{199, "CRC", Bridge, "interface CRC errors (usually the cable or the bridge, not the platters)"},
}

// HotC is where a drive in a closed caddy has started cooking.
const HotC = 50

// hotAt is where a drive counts as hot.  An NVMe drive idles past HotC, so it
// gets its own ceiling.
func hotAt(i *Info) int {
	if i.Protocol != NVMe {
		return HotC
	}
	c, _ := Ceiling(i)
	return c
}

// Assess grades a drive from SMART alone.
func Assess(i *Info) Health {
	h := Health{Grade: Pass}
	add := func(fail bool, about About, tag, value, s string, a ...any) {
		h.Add(Finding{Tag: tag, Value: value, Fail: fail, About: about, Text: fmt.Sprintf(s, a...)})
	}

	if (i.Passed != nil && !*i.Passed) || i.ExitStatus&ExitFailing != 0 {
		add(true, Drive, "FAILING", "", "The drive's own health check says FAILING.")
	}
	for _, a := range i.Attrs {
		switch a.WhenFailed {
		case "now":
			add(true, Drive, "THRESH", a.Name, "%s is at or below its failure threshold right now.", a.Name)
		case "past":
			add(false, Drive, "PAST", a.Name, "%s crossed its threshold in the past (worst %d, threshold %d).", a.Name, a.Worst, a.Thresh)
		}
	}
	for _, w := range watched {
		if a, ok := i.Attr(w.id); ok && a.Raw > 0 {
			add(false, w.about, w.tag, fmt.Sprint(a.Raw), "%d %s.", a.Raw, w.what)
		}
	}
	if i.TempC >= hotAt(i) {
		add(false, Drive, "HOT", fmt.Sprintf("%d °C", i.TempC), "Running hot: %d °C.", i.TempC)
	}
	if i.MediaErrors > 0 {
		add(false, Drive, "MEDIA", fmt.Sprint(i.MediaErrors), "%d media and data integrity errors.", i.MediaErrors)
	}
	if i.PercentUsed >= 100 {
		add(false, Drive, "WORN", fmt.Sprintf("%d%%", i.PercentUsed), "The drive has used %d%% of its rated endurance.", i.PercentUsed)
	}
	if i.ErrorLogCount > 0 {
		add(false, Drive, "ERRLOG", fmt.Sprint(i.ErrorLogCount), "%d entries in the drive's error log.", i.ErrorLogCount)
	}
	if i.ExitStatus&ExitSelfTestErrs != 0 {
		add(false, Drive, "SELFTEST", "log", "The self-test log records errors.")
	}
	if i.ExitStatus&ExitCmdFailed != 0 {
		add(false, Bridge, "BRIDGE", "", "Some SMART commands failed through this bridge, so the picture may be incomplete.")
	}
	if i.Passed == nil {
		add(false, Bridge, "NO-STATUS", "", "The drive didn't report an overall health status.")
	}
	return h
}

// Rising compares SMART from before and after a wipe.  A watched count that
// climbs while every sector is written and read back is a drive getting
// worse under load: that fails it, except for CRC errors, which belong to
// the cable or the bridge.
func Rising(before, after *Info) []Finding {
	var fs []Finding
	for _, w := range watched {
		a, ok1 := before.Attr(w.id)
		b, ok2 := after.Attr(w.id)
		if !ok1 || !ok2 || b.Raw <= a.Raw {
			continue
		}
		fs = append(fs, Finding{Tag: w.tag, Value: fmt.Sprintf("%d→%d", a.Raw, b.Raw), Fail: w.about == Drive, About: w.about,
			Text: fmt.Sprintf("%s rose during the wipe, %d → %d.", w.what, a.Raw, b.Raw)})
	}
	if after.MediaErrors > before.MediaErrors {
		fs = append(fs, Finding{Tag: "MEDIA", Value: fmt.Sprintf("%d→%d", before.MediaErrors, after.MediaErrors), Fail: true, About: Drive,
			Text: fmt.Sprintf("Media and data integrity errors rose during the wipe, %d → %d.", before.MediaErrors, after.MediaErrors)})
	}
	if after.ErrorLogCount > before.ErrorLogCount {
		fs = append(fs, Finding{Tag: "ERRLOG", Value: fmt.Sprintf("%d→%d", before.ErrorLogCount, after.ErrorLogCount), About: Drive,
			Text: fmt.Sprintf("The drive's error log grew during the wipe, %d → %d entries.", before.ErrorLogCount, after.ErrorLogCount)})
	}
	return fs
}

const (
	// CeilingSpinning and CeilingFlash are the job's ceilings for a drive that
	// doesn't say what it wants (spec §6.6).
	CeilingSpinning = 55
	CeilingFlash    = 65
)

// Ceiling is the temperature a job holds a drive under, and where it came
// from.  NVMe's warning threshold is set for that drive's hotter normal, so it
// stands.  SCT's maximum can only lower the default: a laptop drive has
// been seen to report 80 °C, which is no ceiling for a spinning disk.
func Ceiling(i *Info) (int, string) {
	sane := func(c int) bool { return c >= 30 && c <= 100 }
	if i == nil {
		return CeilingSpinning, "the default, since SMART couldn't be read"
	}
	if sane(i.WarnTempC) {
		return i.WarnTempC, "the drive's NVMe warning threshold"
	}
	c, why := CeilingSpinning, "the default for a spinning drive"
	if i.RotationRate == 0 {
		c, why = CeilingFlash, "the default for flash"
	}
	if sane(i.MaxTempC) && i.MaxTempC < c {
		return i.MaxTempC, "the drive's recommended maximum"
	}
	return c, why
}

// ParseTemp reads the temperature from `smartctl -A -j`, which has no serial
// for Parse to hold it to.
func ParseTemp(b []byte) (int, error) {
	var r struct {
		Temperature struct {
			Current int `json:"current"`
		} `json:"temperature"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, fmt.Errorf("smartctl output isn't JSON: %w", err)
	}
	return r.Temperature.Current, nil
}
