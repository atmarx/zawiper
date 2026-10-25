// Package grade turns a wipe into PASS, PASS WITH NOTES, or FAIL (spec §6.4):
// SMART after the job, what rose during it, and what the read-back found.
package grade

import (
	"fmt"

	"zawiper/internal/records"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

// Wipe grades a drive from SMART before and after a wipe, and the wipe's
// result.  after is nil when SMART couldn't be read afterwards; res is nil
// when the wipe never started.
func Wipe(before, after *smart.Info, res *wipe.Result) smart.Health {
	h := fromSMART(before, after)
	note := func(s string) {
		h.Notes = append(h.Notes, s)
		if h.Grade == smart.Pass {
			h.Grade = smart.Notes
		}
	}
	fail := func(s string) {
		h.Notes = append(h.Notes, s)
		h.Grade = smart.Fail
	}
	if res == nil {
		note("The wipe never started, so this says nothing about the drive's surface.")
		return h
	}
	if res.Bad > 0 {
		fail(fmt.Sprintf("%s bytes didn't read back as written.", records.Commas(res.Bad)))
	}
	switch {
	case res.Alarm != "":
		fail("The drive's identity changed mid-job: " + res.Alarm)
	case res.Aborted != "":
		note("The wipe was " + res.Aborted + ", so it proves nothing about the rest of the drive.")
	}
	if slow := records.SlowLine(res); slow != "none" {
		note("Slow blocks: " + slow + ".")
	}
	return h
}

// fromSMART is Assess on the drive as it stands after the job, plus any
// count that rose during it.
func fromSMART(before, after *smart.Info) smart.Health {
	switch {
	case before == nil && after == nil:
		return smart.Health{Grade: smart.Notes, Notes: []string{"No SMART readings at all, so the drive's own view is missing."}}
	case after == nil:
		h := smart.Assess(before)
		h.Notes = append(h.Notes, "SMART couldn't be read after the wipe, so nothing could be compared.  This grade is from before it.")
		if h.Grade == smart.Pass {
			h.Grade = smart.Notes
		}
		return h
	case before == nil:
		before = after
	}
	h := smart.Assess(after)
	fails, notes := smart.Rising(before, after)
	h.Notes = append(h.Notes, notes...)
	if len(notes) > 0 && h.Grade == smart.Pass {
		h.Grade = smart.Notes
	}
	if len(fails) > 0 {
		h.Notes = append(h.Notes, fails...)
		h.Grade = smart.Fail
	}
	return h
}
