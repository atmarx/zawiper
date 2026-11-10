package smart

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/st31000528as.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The first drive found in a bay: healthy, except airflow temperature
// crossed its threshold once, which is why smartctl exits 32.
func TestParseRealDrive(t *testing.T) {
	i, err := Parse(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if i.Serial != "9VPAX9NA" || i.Model != "ST31000528AS" {
		t.Errorf("identity: %q %q", i.Serial, i.Model)
	}
	if i.CapacityBytes != 1000204886016 {
		t.Errorf("capacity %d", i.CapacityBytes)
	}
	if i.WWN != "5 000c50 02d635ade" {
		t.Errorf("wwn %q", i.WWN)
	}
	if i.Passed == nil || !*i.Passed || i.PowerOnHours != 11196 || i.ExitStatus != 32 {
		t.Errorf("health fields: %+v", i)
	}
	if a, ok := i.Attr(190); !ok || a.WhenFailed != "past" {
		t.Errorf("attr 190: %+v", a)
	}
	// SATA 2.6 reports only its maximum link speed.
	if i.Link != "3.0 Gb/s" || i.LinkBps != 3_000_000_000 {
		t.Errorf("link %q %d", i.Link, i.LinkBps)
	}
}

func TestParseCurrentLink(t *testing.T) {
	i, err := Parse([]byte(`{"serial_number":"X1","interface_speed":{
		"max":{"string":"6.0 Gb/s","units_per_second":60,"bits_per_unit":100000000},
		"current":{"string":"3.0 Gb/s","units_per_second":30,"bits_per_unit":100000000}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if i.Link != "3.0 Gb/s" || i.LinkBps != 3_000_000_000 {
		t.Errorf("the current link should win: %q %d", i.Link, i.LinkBps)
	}
}

func TestAssessRealDrive(t *testing.T) {
	i, _ := Parse(fixture(t))
	h := Assess(i)
	if h.Grade != Notes {
		t.Fatalf("grade %q, notes %v", h.Grade, h.Notes)
	}
	if len(h.Notes) != 1 || !strings.Contains(h.Notes[0], "Airflow_Temperature_Cel") {
		t.Errorf("notes %v", h.Notes)
	}
}

func TestAssess(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		info Info
		want Grade
	}{
		{"clean", Info{Passed: &yes}, Pass},
		{"failing", Info{Passed: &no}, Fail},
		{"failing bit", Info{Passed: &yes, ExitStatus: ExitFailing}, Fail},
		{"attr now", Info{Passed: &yes, Attrs: []Attr{{ID: 5, Name: "Reallocated_Sector_Ct", WhenFailed: "now"}}}, Fail},
		{"pending", Info{Passed: &yes, Attrs: []Attr{{ID: 197, Raw: 8}}}, Notes},
		{"crc", Info{Passed: &yes, Attrs: []Attr{{ID: 199, Raw: 2}}}, Notes},
		{"hot", Info{Passed: &yes, TempC: 55}, Notes},
		{"no status", Info{}, Notes},
		{"fail beats notes", Info{Passed: &no, TempC: 55}, Fail},
	}
	for _, c := range cases {
		if got := Assess(&c.info).Grade; got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for name, js := range map[string]string{
		"open failed": `{"smartctl":{"exit_status":2},"serial_number":"X"}`,
		"no serial":   `{"smartctl":{"exit_status":0}}`,
		"zero serial": `{"smartctl":{"exit_status":0},"serial_number":"00000000"}`,
		"garbage":     `not json`,
	} {
		if _, err := Parse([]byte(js)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestCeiling(t *testing.T) {
	for name, c := range map[string]struct {
		i    *Info
		want int
	}{
		"nothing":    {nil, CeilingSpinning},
		"spinning":   {&Info{RotationRate: 7200}, CeilingSpinning},
		"flash":      {&Info{RotationRate: 0}, CeilingFlash},
		"SCT lower":  {&Info{RotationRate: 7200, MaxTempC: 50}, 50},
		"SCT higher": {&Info{RotationRate: 5400, MaxTempC: 80}, CeilingSpinning},
		"SSD higher": {&Info{RotationRate: 0, MaxTempC: 70}, CeilingFlash},
		"NVMe":       {&Info{MaxTempC: 60, WarnTempC: 80}, 80},
		"nonsense":   {&Info{RotationRate: 7200, MaxTempC: 255}, CeilingSpinning},
		"nonsense 2": {&Info{RotationRate: 7200, MaxTempC: 1}, CeilingSpinning},
	} {
		if got, why := Ceiling(c.i); got != c.want || why == "" {
			t.Errorf("%s: %d (%s), want %d", name, got, why, c.want)
		}
	}
}

// The temperature blocks a real ST1000LM044 gave through a USB bridge, from
// -a -l scttempsts -j and from -A -j.  The serial and rotation rate are filled in.
func TestParseTemperatureFromARealDrive(t *testing.T) {
	js := []byte(`{"smartctl":{"exit_status":0},"serial_number":"X1","rotation_rate":5400,"temperature":{"current":29,"power_cycle_min":22,"power_cycle_max":29,"lifetime_min":18,"lifetime_max":53,"op_limit_max":80}}`)
	i, err := Parse(js)
	if err != nil || i.TempC != 29 || i.MaxTempC != 80 {
		t.Fatalf("%+v %v", i, err)
	}
	if c, _ := Ceiling(i); c != CeilingSpinning {
		t.Errorf("ceiling %d", c)
	}
	if c, err := ParseTemp([]byte(`{"smartctl":{"exit_status":0},"temperature":{"current":29}}`)); err != nil || c != 29 {
		t.Errorf("ParseTemp: %d %v", c, err)
	}
}

func TestParseTemperatureLimits(t *testing.T) {
	js := []byte(`{"smartctl":{"exit_status":0},"serial_number":"X1","temperature":{"current":40,"op_limit_max":60},"nvme_composite_temperature_threshold":{"warning":80,"critical":85}}`)
	i, err := Parse(js)
	if err != nil || i.MaxTempC != 60 || i.WarnTempC != 80 || i.TempC != 40 {
		t.Errorf("%+v, %v", i, err)
	}
}

// Shaped from smartmontools' nvmeprint.cpp.  It isn't a capture from a real drive.
func nvmeFixture(t *testing.T) *Info {
	t.Helper()
	js, err := os.ReadFile("testdata/nvme_usb.json")
	if err != nil {
		t.Fatal(err)
	}
	i, err := Parse(js)
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func TestParseNVMe(t *testing.T) {
	i := nvmeFixture(t)
	if i.Protocol != NVMe || i.TempC != 51 || i.WarnTempC != 84 || i.RotationRate != 0 || i.CapacityBytes != 500107862016 || i.PercentUsed != 3 || i.NVMeHidden() != 0 {
		t.Fatalf("%+v", i)
	}
	if c, _ := Ceiling(i); c != 84 {
		t.Errorf("ceiling %d", c)
	}
	// 51 °C is an ordinary day for NVMe, and its log entries aren't findings.
	if h := Assess(i); h.Grade != Pass {
		t.Errorf("%+v", h)
	}
}

func TestNVMeCeilingFromOpLimitAlone(t *testing.T) {
	i, err := Parse([]byte(`{"smartctl":{"exit_status":0},"device":{"protocol":"NVMe"},"serial_number":"X1","temperature":{"current":40,"op_limit_max":75}}`))
	if err != nil || i.WarnTempC != 75 {
		t.Fatalf("%+v %v", i, err)
	}
}

func TestNVMeHealth(t *testing.T) {
	parse := func(crit, media int, used int) *Info {
		js := fmt.Sprintf(`{"smartctl":{"exit_status":%d},"device":{"protocol":"NVMe"},"serial_number":"X1","smart_status":{"passed":%v},"temperature":{"current":40},"nvme_smart_health_information_log":{"critical_warning":%d,"media_errors":%d,"percentage_used":%d}}`,
			map[bool]int{true: ExitFailing, false: 0}[crit != 0], crit == 0, crit, media, used)
		i, err := Parse([]byte(js))
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	if h := Assess(parse(nvmeWarnTemperature, 0, 0)); h.Grade != Pass {
		t.Errorf("hot right now isn't failing: %+v", h)
	}
	if h := Assess(parse(0x04, 0, 0)); h.Grade != Fail {
		t.Errorf("reliability degraded: %+v", h)
	}
	if h := Assess(parse(nvmeWarnTemperature|0x01, 0, 0)); h.Grade != Fail {
		t.Errorf("spare low and hot: %+v", h)
	}
	if h := Assess(parse(0, 3, 100)); h.Grade != Notes || len(h.Findings) != 2 {
		t.Errorf("media and wear: %+v", h)
	}
	if f := Rising(parse(0, 1, 0), parse(0, 2, 0)); len(f) != 1 || !f[0].Fail || f[0].Tag != "MEDIA" {
		t.Errorf("rising: %+v", f)
	}
}

func TestNVMeHidden(t *testing.T) {
	for _, c := range []struct {
		i    Info
		want int64
	}{
		{Info{CapacityBytes: 500}, 0}, // no namespace management
		{Info{CapacityBytes: 500, NVMeTotal: 500}, 0},
		{Info{CapacityBytes: 400, NVMeTotal: 500}, 100},
		{Info{CapacityBytes: 400, NVMeTotal: 500, NVMeUnallocated: 100}, 100},
		{Info{CapacityBytes: 500, NVMeUnallocated: 20}, 20},
	} {
		if got := c.i.NVMeHidden(); got != c.want {
			t.Errorf("%+v: %d", c.i, got)
		}
	}
}
