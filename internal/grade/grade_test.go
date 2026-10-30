package grade

import (
	"strings"
	"testing"

	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

func healthy(realloc, crc int64) *smart.Info {
	ok := true
	return &smart.Info{Serial: "9VPAX9NA", Passed: &ok, TempC: 30, Attrs: []smart.Attr{
		{ID: 5, Name: "Reallocated_Sector_Ct", Raw: realloc},
		{ID: 199, Name: "UDMA_CRC_Error_Count", Raw: crc},
	}}
}

// A wipe where every byte came back, on a drive with nothing to say.
func clean() *wipe.Result {
	r := &wipe.Result{Size: 40, Written: 40, Checked: 40}
	for i := 0; i < 40; i++ {
		r.Cells = append(r.Cells, wipe.Cell{Write: 7, Read: 7})
	}
	return r
}

func TestWipe(t *testing.T) {
	slow := clean()
	slow.Cells[20].Write = 30
	bad := clean()
	bad.Bad, bad.Checked = 4096, 40-4096
	aborted := clean()
	aborted.Aborted = "stopped during the verify pass"
	alarm := clean()
	alarm.Aborted, alarm.Alarm = "stopped during the write pass", "the drive now says serial Z1D2"

	cases := map[string]struct {
		before, after *smart.Info
		res           *wipe.Result
		grade         smart.Grade
		note          string
		pill          string // tag and value of the finding behind the note
		about         smart.About
	}{
		"clean":          {healthy(0, 0), healthy(0, 0), clean(), smart.Pass, "", "", ""},
		"stable scar":    {healthy(8, 0), healthy(8, 0), clean(), smart.Notes, "8 reallocated sectors", "REALLOC 8", smart.Drive},
		"rising":         {healthy(8, 0), healthy(40, 0), clean(), smart.Fail, "rose during the wipe, 8 → 40", "REALLOC 8→40", smart.Drive},
		"rising crc":     {healthy(0, 1), healthy(0, 3), clean(), smart.Notes, "interface CRC", "CRC 1→3", smart.Bridge},
		"bad bytes":      {healthy(0, 0), healthy(0, 0), bad, smart.Fail, "4,096 bytes didn't read back", "BAD 4.10 KB", smart.Drive},
		"aborted":        {healthy(0, 0), healthy(0, 0), aborted, smart.Notes, "proves nothing", "PARTIAL stopped during the verify pass", smart.Job},
		"alarm":          {healthy(0, 0), healthy(0, 0), alarm, smart.Fail, "identity changed mid-job: the drive now says serial Z1D2", "IDENTITY", smart.Job},
		"slow block":     {healthy(0, 0), healthy(0, 0), slow, smart.Notes, "Slow blocks: #21.", "SLOW 1", smart.Drive},
		"no smart after": {healthy(0, 0), nil, clean(), smart.Notes, "couldn't be read after the wipe", "NO-SMART after", smart.Bridge},
		"never started":  {healthy(0, 0), healthy(0, 0), nil, smart.Notes, "never started", "PARTIAL not started", smart.Job},
	}
	for name, c := range cases {
		h := Wipe(c.before, c.after, c.res)
		notes := strings.Join(h.Notes, " | ")
		if h.Grade != c.grade || !strings.Contains(notes, c.note) {
			t.Errorf("%s: %s, notes %q", name, h.Grade, notes)
		}
		// Every sentence has its pill, in the same order.
		if len(h.Findings) != len(h.Notes) {
			t.Fatalf("%s: %d findings for %d notes", name, len(h.Findings), len(h.Notes))
		}
		found := c.pill == ""
		for i, f := range h.Findings {
			if f.Text != h.Notes[i] {
				t.Errorf("%s: finding %d says %q, note says %q", name, i, f.Text, h.Notes[i])
			}
			if strings.TrimSpace(f.Tag+" "+f.Value) == c.pill && strings.Contains(f.Text, c.note) && f.About == c.about {
				found = true
				if f.Fail != (c.grade == smart.Fail && f.About != smart.Bridge && f.Tag != "PARTIAL") {
					t.Errorf("%s: %s fail=%v under %s", name, c.pill, f.Fail, c.grade)
				}
			}
		}
		if !found {
			t.Errorf("%s: no pill %q (%s) among %+v", name, c.pill, c.about, h.Findings)
		}
	}
}

// Missing readings must never panic, whichever side is missing.
func TestWipeNoSMART(t *testing.T) {
	if h := Wipe(nil, nil, clean()); h.Grade != smart.Notes {
		t.Errorf("no SMART: %s", h.Grade)
	}
	if h := Wipe(nil, healthy(0, 0), clean()); h.Grade != smart.Pass {
		t.Errorf("no SMART before: %s %v", h.Grade, h.Notes)
	}
}
