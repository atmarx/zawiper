package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"math"
	"strings"
	"time"

	"zawiper/internal/hub"
	"zawiper/internal/model"
	"zawiper/internal/records"
)

// The DEFRAG glyph for each cell byte, and the class that colors it.
// zawiper.js holds the same table; keep the two in step.
var glyphs = map[byte]struct{ glyph, class string }{
	model.CellTodo:     {"░", "todo"},
	model.CellWriting:  {"W", "cur"},
	model.CellReading:  {"R", "cur"},
	model.CellWritten:  {"▓", "written"},
	model.CellVerified: {"█", "verified"},
	model.CellSlow:     {"▒", "slow"},
	model.CellBad:      {"B", "bad"},
}

// Cells draws a map as runs of colored glyphs.  Every byte becomes a glyph
// from the table above or a "?", so nothing from the bay reaches the HTML
// as itself.
func Cells(m string) template.HTML {
	var b strings.Builder
	run := ""
	for i := 0; i < len(m); i++ {
		g, ok := glyphs[m[i]]
		if !ok {
			g.glyph, g.class = "?", "unk"
		}
		if g.class != run {
			if run != "" {
				b.WriteString("</span>")
			}
			fmt.Fprintf(&b, `<span class="c-%s">`, g.class)
			run = g.class
		}
		b.WriteString(g.glyph)
	}
	if run != "" {
		b.WriteString("</span>")
	}
	return template.HTML(b.String())
}

// Mini shrinks a map to n cells for a tile on the wall.  A bad block wins,
// then whatever is happening right now, then the least progress, so a mini
// cell only goes solid once everything under it has.
func Mini(m string, n int) string {
	if len(m) <= n {
		return m
	}
	out := make([]byte, n)
	for i := range n {
		out[i] = summarize(m[i*len(m)/n : (i+1)*len(m)/n])
	}
	return string(out)
}

func summarize(s string) byte {
	for _, c := range []byte{model.CellBad, model.CellWriting, model.CellReading} {
		if strings.IndexByte(s, c) >= 0 {
			return c
		}
	}
	rank := map[byte]int{model.CellTodo: 0, model.CellWritten: 1, model.CellVerified: 2, model.CellSlow: 2}
	low := byte(model.CellVerified)
	for i := 0; i < len(s); i++ {
		r, ok := rank[s[i]]
		if !ok {
			return s[i]
		}
		if r < rank[low] {
			low = s[i]
		}
	}
	if low == model.CellVerified && strings.IndexByte(s, model.CellSlow) >= 0 {
		return model.CellSlow
	}
	return low
}

// PhaseWords says what a job phase is doing, for people.
func PhaseWords(p string) string {
	switch p {
	case "smart-before":
		return "Reading SMART before the wipe"
	case "erase":
		return "Asking the drive to erase itself"
	case "write":
		return "Writing the keystream"
	case "verify":
		return "Reading it back and comparing"
	case "smart-after":
		return "Reading SMART after the wipe"
	case "record":
		return "Writing the record"
	case "":
		return "Starting"
	}
	return p
}

// Live is a job's surface map and the numbers in its status box, already
// worded.  The page draws it once, and /events sends it on every tick.
type Live struct {
	Map     string `json:"map"`
	Mini    string `json:"mini"`
	Phase   string `json:"phase"`
	Block   string `json:"block"`
	Pct     string `json:"pct"`
	Bar     string `json:"bar"`
	Rate    string `json:"rate"`
	ETA     string `json:"eta"`
	Elapsed string `json:"elapsed"`
	Temp    string `json:"temp"`
	Errors  string `json:"errors"`

	// Headline numbers, for a job that's over.
	Took     string `json:"-"`
	Average  string `json:"-"`
	ReadBack string `json:"-"`
	Slow     int    `json:"-"`
}

const miniCells = 64

func LiveOf(j *model.Job, now time.Time) *Live {
	l := &Live{Map: j.Map, Mini: Mini(j.Map, miniCells), Phase: PhaseWords(j.Phase)}

	frac := 0.0
	if j.Total > 0 {
		frac = math.Min(1, float64(j.Done)/float64(2*j.Total))
	}
	pct := math.Floor(frac*1000) / 10 // never rounds up to 100.0 before it's there
	l.Pct = fmt.Sprintf("%.1f%%", pct)
	const bar = 20
	filled := int(frac * bar)
	l.Bar = strings.Repeat("█", filled) + strings.Repeat("░", bar-filled)

	l.Block = fmt.Sprintf("%s blocks of 1 GiB", records.Commas(int64(len(j.Map))))
	if i := strings.IndexAny(j.Map, "WR"); i >= 0 {
		l.Block = fmt.Sprintf("Block %s of %s", records.Commas(int64(i+1)), records.Commas(int64(len(j.Map))))
	}

	l.Rate, l.ETA, l.Temp = "—", "—", "—"
	if j.MBps > 0 {
		l.Rate = fmt.Sprintf("%.0f MB/s", j.MBps)
	}
	if j.ETA > 0 {
		l.ETA = clock(time.Duration(j.ETA) * time.Second)
	}
	if j.TempC > 0 {
		l.Temp = fmt.Sprintf("%d °C", j.TempC)
	}
	end := now
	if !j.Ended.IsZero() {
		end = j.Ended
	}
	if !j.Started.IsZero() {
		l.Elapsed = clock(end.Sub(j.Started))
	} else {
		l.Elapsed = "—"
	}
	l.Errors = "None"
	switch bad := strings.Count(j.Map, string(rune(model.CellBad))); {
	case j.Bad > 0 && bad > 0:
		l.Errors = fmt.Sprintf("%s bytes in %d block%s", records.Commas(j.Bad), bad, plural(bad))
	case j.Bad > 0:
		l.Errors = fmt.Sprintf("%s bytes bad", records.Commas(j.Bad))
	case bad > 0:
		l.Errors = fmt.Sprintf("%d bad block%s", bad, plural(bad))
	}

	l.Slow = strings.Count(j.Map, string(rune(model.CellSlow)))
	l.ReadBack = records.Size(min(j.Total, max(0, j.Done-j.Total)))
	if !j.Ended.IsZero() && !j.Started.IsZero() {
		d := j.Ended.Sub(j.Started)
		l.Took = clock(d)
		if d > 0 {
			l.Average = fmt.Sprintf("%.0f MB/s", float64(j.Done)/d.Seconds()/1e6)
		}
	}
	return l
}

// clock is h:mm:ss, the way DEFRAG counted.
func clock(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Sig fingerprints what a page has to redraw for: everything about a bay
// except a running job's progress, which the page updates in place.  When
// it changes under an open confirm, the confirm goes (spec §4.2).
func Sig(b hub.Bay) string {
	s := b.Snap
	var job *model.Job
	if s.Job != nil {
		j := *s.Job
		j.Phase, j.Map, j.Done, j.MBps, j.ETA, j.TempC, j.Bad = "", "", 0, 0, 0, 0, 0
		job = &j
	}
	blob, _ := json.Marshal(struct {
		Online   bool
		Caps     []string
		State    model.State
		Message  string
		Session  time.Time
		ReadAt   time.Time
		Drive    *model.Drive
		Devices  []string
		Refusals []string
		RecordID string
		Job      *model.Job
	}{b.Online, b.Caps, s.State, s.Message, s.Session, s.ReadAt, s.Drive, s.Devices, s.Refusals, s.RecordID, job})
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:8])
}
