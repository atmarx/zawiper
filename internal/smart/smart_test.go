package smart

import (
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

// The drive Val found in the bay: healthy, except airflow temperature
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
