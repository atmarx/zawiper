// Package grade turns a wipe into PASS, PASS WITH NOTES, or FAIL (spec §6.4):
// SMART after the job, what rose during it, and what the read-back found.
package grade

import (
	"fmt"
	"strings"

	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/smart"
	"github.com/atmarx/zawiper/internal/wipe"
)

// Wipe grades a drive from SMART before and after a wipe, and the wipe's
// result.  after is nil when SMART couldn't be read afterwards; res is nil
// when the wipe never started.
func Wipe(before, after *smart.Info, res *wipe.Result) smart.Health {
	h := fromSMART(before, after)
	if res == nil {
		h.Add(smart.Finding{Tag: "PARTIAL", Value: "not started", About: smart.Job,
			Text: "The wipe never started, so this says nothing about the drive's surface."})
		return h
	}
	if res.Bad > 0 {
		h.Add(smart.Finding{Tag: "BAD", Value: records.Size(res.Bad), Fail: true, About: smart.Drive,
			Text: fmt.Sprintf("%s bytes didn't read back as written.", records.Commas(res.Bad))})
	}
	switch {
	case res.Alarm != "":
		h.Add(smart.Finding{Tag: "IDENTITY", Fail: true, About: smart.Job,
			Text: "The drive's identity changed mid-job: " + res.Alarm})
	case res.Aborted != "":
		h.Add(smart.Finding{Tag: "PARTIAL", Value: res.Aborted, About: smart.Job,
			Text: "The wipe was " + res.Aborted + ", so it proves nothing about the rest of the drive."})
	}
	if slow := records.SlowLine(res); slow != "none" {
		h.Add(smart.Finding{Tag: "SLOW", Value: fmt.Sprint(strings.Count(slow, "#")), About: smart.Drive,
			Text: "Slow blocks: " + slow + "."})
	}
	return h
}

// fromSMART is Assess on the drive as it stands after the job, plus any
// count that rose during it.
func fromSMART(before, after *smart.Info) smart.Health {
	switch {
	case before == nil && after == nil:
		h := smart.Health{Grade: smart.Pass}
		h.Add(smart.Finding{Tag: "NO-SMART", About: smart.Bridge, Text: "No SMART readings at all, so the drive's own view is missing."})
		return h
	case after == nil:
		h := smart.Assess(before)
		h.Add(smart.Finding{Tag: "NO-SMART", Value: "after", About: smart.Bridge,
			Text: "SMART couldn't be read after the wipe, so nothing could be compared.  This grade is from before it."})
		return h
	case before == nil:
		before = after
	}
	h := smart.Assess(after)
	// Notes before fails, as the sentences have always run.
	rising := smart.Rising(before, after)
	for _, fail := range []bool{false, true} {
		for _, f := range rising {
			if f.Fail == fail {
				h.Add(f)
			}
		}
	}
	return h
}
