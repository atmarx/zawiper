package web

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/atmarx/zawiper/internal/hub"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
)

// The plain-text look (spec §6.5): the wall and each bay as a console of
// the CHKDSK era would have printed them, for curl, watch, an ssh session
// or a log.  Each opens on the command line that would have started what's
// running, under our own name and with switches that say which job it is.
// A bay is host\name, because in DOS a slash starts a switch.

const prompt = `C:\ZAWIPER> `

func dosName(b hub.Bay) string { return b.Host + `\` + b.Name }

// command is the line a bay's text opens on.
func command(b hub.Bay) string {
	c := prompt + "ZAWIPE " + dosName(b)
	if j := b.Snap.Job; j != nil && stateOf(b) != model.Offline {
		switch j.Kind {
		case "wipe", "":
			c += " /WIPE /V:FULL"
		default:
			c += " /" + strings.ToUpper(j.Kind)
		}
	}
	return c
}

// Text is one bay, as it stands.
func Text(b hub.Bay, now time.Time) string {
	var t strings.Builder
	t.WriteString(command(b) + "\n\n")
	lines, live := transcript(b)
	for _, l := range lines {
		t.WriteString(l + "\n")
	}
	if live != "" {
		t.WriteString(live + "\n")
	}
	if j := b.Snap.Job; j != nil && stateOf(b) == model.Running {
		l := LiveOf(j, b.Snap.Drive, now)
		fmt.Fprintf(&t, "    %s.  Elapsed %s at %s, %s to go.", l.Block, l.Elapsed, l.Rate, l.ETA)
		if l.Temp != "—" {
			fmt.Fprintf(&t, "  %s.", l.Temp)
		}
		t.WriteString("\n")
		if l.Errors != "None" {
			t.WriteString("    Errors: " + l.Errors + ".\n")
		}
	}
	return t.String()
}

// passes is the order a job's phases come in.  The write is third and the
// read-back fourth.
var passes = map[string]int{"": 0, "smart-before": 1, "erase": 2, "write": 3, "verify": 4, "smart-after": 5, "record": 6}

// transcript is a bay's text as lines that only ever grow while one job
// runs, and the one line that's still moving (live), so a follower can
// print what's new and nothing twice.
func transcript(b hub.Bay) (lines []string, live string) {
	s, st := b.Snap, stateOf(b)
	lines = append(lines, fmt.Sprintf("Za Wiper  %s on %s", b.Name, b.Host))
	switch st {
	case model.Offline:
		return append(lines, "", "The bay's link is down.  Last heard "+records.When(b.Seen)+"."), ""
	case model.Empty:
		return append(lines, "", "No drive in the bay."), ""
	}
	if d := s.Drive; d != nil {
		lines = append(lines, driveLine(d))
	}
	if s.Message != "" {
		lines = append(lines, s.Message)
	}
	j := s.Job
	if j == nil {
		switch st {
		case model.Reading:
			return append(lines, "", "Reading the drive..."), ""
		case model.Present:
			lines = append(lines, "")
			if d := s.Drive; d != nil {
				lines = append(lines, health(d)...)
				lines = append(lines, records.FoundLine(d))
				if why := d.LooksLikeBackup(); why != "" {
					lines = append(lines, "Looks like backup media: "+why+".")
				}
			}
			if len(s.Refusals) > 0 {
				lines = append(lines, "", "Za Wiper won't act on this drive:")
				for _, r := range s.Refusals {
					lines = append(lines, "    "+r)
				}
				return lines, ""
			}
			if ok, why := wipeable(b); ok {
				lines = append(lines, "", "Ready.  A wipe starts from the page, confirmed against the serial.")
			} else {
				lines = append(lines, "", why)
			}
		}
		return lines, ""
	}

	lines = append(lines, "")
	running := st == model.Running
	at := passes[j.Phase]
	pct := func(n int64) string {
		p := int64(0)
		if j.Total > 0 {
			p = min(100, max(0, n)*100/j.Total)
		}
		return fmt.Sprintf("%d percent completed.", p)
	}
	step := func(phase, text string) {
		if running && j.Phase == phase {
			live = text
		} else {
			lines = append(lines, text)
		}
	}
	if running && at < passes["write"] {
		step(j.Phase, PhaseWords(j.Phase)+"...")
	}
	if at >= passes["write"] || j.Done > 0 {
		step("write", PhaseWords("write")+"... "+pct(min(j.Done, j.Total)))
	}
	if at >= passes["verify"] {
		step("verify", PhaseWords("verify")+"... "+pct(j.Done-j.Total))
	}
	if running && at > passes["verify"] {
		step(j.Phase, PhaseWords(j.Phase)+"...")
	}
	if running {
		return lines, live
	}

	l := LiveOf(j, s.Drive, j.Ended)
	lines = append(lines, "")
	switch st {
	case model.Done:
		lines = append(lines, "Every block written, read back, and compared.")
	case model.Failed:
		lines = append(lines, "Failed during: "+PhaseWords(j.Phase)+".")
	case model.Interrupted:
		lines = append(lines, "Interrupted during: "+PhaseWords(j.Phase)+".  A run that didn't finish gets no record.")
	case model.Alarm:
		lines = append(lines, "Stopped by bayd during: "+PhaseWords(j.Phase)+".")
	}
	if j.Evidence != "" {
		lines = append(lines, "    "+j.Evidence)
	}
	if l.Errors != "None" {
		lines = append(lines, "Errors: "+l.Errors+".")
	}
	if l.Slow > 0 {
		lines = append(lines, fmt.Sprintf("%d slow block%s.", l.Slow, plural(l.Slow)))
	}
	if l.Took != "" {
		lines = append(lines, fmt.Sprintf("Took %s, %s on average.", l.Took, l.Average))
	}
	if d := s.Drive; d != nil {
		lines = append(lines, health(d)...)
	}
	if id := j.RecordID; id != "" {
		lines = append(lines, fmt.Sprintf("Record %s, filed under %s.  It names the level and the grade.", id, j.Serial))
	}
	return lines, ""
}

func driveLine(d *model.Drive) string {
	who := "Drive with no serial"
	if d.SMART != nil && d.SMART.Serial != "" {
		who = "Drive " + d.SMART.Serial
		if d.SMART.Model != "" {
			who += ", " + d.SMART.Model
		}
	}
	return fmt.Sprintf("%s, %s bytes (%s).", who, records.Commas(d.Size), records.Size(d.Size))
}

// health is the grade and its findings, fails first, as tag and value.
func health(d *model.Drive) []string {
	h := d.Health
	if h.Grade == "" {
		return nil
	}
	line := "Health: " + string(h.Grade)
	var tags []string
	for _, f := range pills(h.Findings, 0).Shown {
		tags = append(tags, strings.TrimSpace(f.Tag+" "+f.Value))
	}
	if len(tags) > 0 {
		line += " (" + strings.Join(tags, ", ") + ")"
	}
	return []string{line + "."}
}

// follow is what to print when a followed bay changes: the lines it hasn't
// printed yet, and the moving line if it moved.  When the transcript isn't
// an extension of the last one (a new drive, a new job), it starts again
// under a fresh command line.
func follow(was []string, wasLive string, b hub.Bay) (out []string, lines []string, live string) {
	lines, live = transcript(b)
	if len(lines) >= len(was) && slices.Equal(lines[:len(was)], was) {
		out = slices.Clone(lines[len(was):])
		if len(out) > 0 && out[0] == wasLive {
			out = out[1:]
		}
	} else {
		out = append([]string{"", command(b), ""}, lines...)
		wasLive = ""
	}
	if live != "" && live != wasLive {
		out = append(out, live)
	}
	return out, lines, live
}

// bayText serves one bay's text.  With ?follow it stays open and prints each
// change as it comes, the way a long CHKDSK scrolled.
func (s *Server) bayText(w http.ResponseWriter, r *http.Request) {
	b, ok := s.lookup(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, Text(b, time.Now()))
	fl, ok := w.(http.Flusher)
	if !r.URL.Query().Has("follow") || !ok {
		return
	}
	fl.Flush()
	ticks, cancel := s.Fleet.Subscribe()
	defer cancel()
	lines, live := transcript(b)
	for {
		select {
		case <-r.Context().Done():
			return
		case _, open := <-ticks:
			if !open {
				return
			}
			now, ok := s.Fleet.Bay(b.Key)
			if !ok {
				continue
			}
			var out []string
			out, lines, live = follow(lines, live, now)
			if len(out) == 0 {
				continue
			}
			io.WriteString(w, strings.Join(out, "\n")+"\n")
			fl.Flush()
		}
	}
}

// wallText is every bay, a line each, the way DIR listed a directory.
func (s *Server) wallText(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	bays := s.Fleet.Bays()
	var t strings.Builder
	t.WriteString(prompt + "ZAWIPE\n\n")
	width := 0
	for _, b := range bays {
		width = max(width, len(dosName(b)))
	}
	for _, b := range bays {
		st := stateOf(b)
		serial := "-"
		if d := b.Snap.Drive; d.Serial() != "" {
			serial = d.Serial()
		}
		what := ""
		if j := b.Snap.Job; j != nil && st == model.Running {
			if _, live := transcript(b); live != "" {
				what = live
			}
		}
		line := fmt.Sprintf("%-*s  %-11s  %-20s  %s", width, dosName(b), st, serial, what)
		t.WriteString(strings.TrimRight(line, " ") + "\n")
	}
	fmt.Fprintf(&t, "%12d bay%s\n", len(bays), plural(len(bays)))
	io.WriteString(w, t.String())
}
