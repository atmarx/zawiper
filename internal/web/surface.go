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

	"github.com/atmarx/zawiper/internal/hub"
	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
)

// The DEFRAG glyph for each cell byte, and the class that colors it.
// zawiper.js holds the same table; keep the two in step.
var glyphs = map[byte]struct{ glyph, class string }{
	model.CellTodo:     {"░", "todo"},
	model.CellWriting:  {"W", "cur"},
	model.CellReading:  {"r", "cur"},
	model.CellWritten:  {"■", "written"},
	model.CellVerified: {"■", "verified"},
	model.CellSlow:     {"▒", "slow"}, // not ■: light yellow next to white is too close on blue
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

// Group shrinks a map so that each cell stands for per blocks, the way a
// big drive's map still fits on a page.
func Group(m string, per int) string {
	if per <= 1 {
		return m
	}
	out := make([]byte, 0, (len(m)+per-1)/per)
	for i := 0; i < len(m); i += per {
		out = append(out, summarize(m[i:min(i+per, len(m))]))
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
	Map   string `json:"map"`
	Mini  string `json:"mini"`
	Scale string `json:"scale"`
	// Fine is the map grouped less, for a wide layout on a big screen,
	// and FineScale its scale.  Empty when it would be the same as Map.
	Fine      string `json:"fine,omitempty"`
	FineScale string `json:"finescale,omitempty"`
	Phase     string `json:"phase"`
	Block     string `json:"block"`
	Pct       string `json:"pct"`
	Bar       string `json:"bar"`
	Rate      string `json:"rate"`
	ETA       string `json:"eta"`
	// Left is the ETA in words, for a tile: "about 6 h 10 min left".
	Left    string `json:"left"`
	Elapsed string `json:"elapsed"`
	Temp    string `json:"temp"`
	Errors  string `json:"errors"`
	Written string `json:"written"`
	Checked string `json:"checked"`
	// Trace is the throughput graph, as TraceRows reads it, and Axis the
	// labels beside it, a line per row.
	Trace string `json:"trace"`
	Axis  string `json:"axis"`
	// Band is the speed scale and its mark, Guess and Limit the words under
	// it (band.go).  CPU is bayd's own, Host the dock host's.
	Band  string `json:"band"`
	Guess string `json:"guess"`
	Limit string `json:"limit"`
	CPU   string `json:"cpu"`
	Host  string `json:"host"`

	// Headline numbers, for a job that's over.
	Took     string `json:"-"`
	Average  string `json:"-"`
	ReadBack string `json:"-"`
	Slow     int    `json:"-"`
}

const (
	miniCells = 64
	// mapCells is about as many cells as the big map shows before it
	// groups blocks: 1,000 GiB fits one to a cell, 4 TB goes three.
	mapCells = 1600
	// fineCells is the wide layout's limit: a 4 TB drive one block a cell.
	fineCells = 6400
	// traceCols is the graph's width in characters, narrow enough for a
	// phone.
	traceCols = 32
)

// LiveOf words a job.  d is the drive it runs on, for the links that cap
// its rate; nil leaves the ceiling off the scale.
func LiveOf(j *model.Job, d *model.Drive, now time.Time) *Live {
	per := (len(j.Map) + mapCells - 1) / mapCells
	l := &Live{Map: Group(j.Map, per), Mini: Mini(j.Map, miniCells), Phase: PhaseWords(j.Phase),
		Scale: fmt.Sprintf("1 block = %d GiB", max(1, per))}
	if fine := (len(j.Map) + fineCells - 1) / fineCells; fine < per {
		l.Fine, l.FineScale = Group(j.Map, fine), fmt.Sprintf("1 block = %d GiB", max(1, fine))
	}
	l.Written = gb(min(j.Done, j.Total))
	l.Checked = gb(max(0, j.Done-j.Total))
	l.Trace, l.Axis = trace(j.WriteMBps, j.ReadMBps, traceCols)
	l.Band, l.Guess, l.Limit = Band(j, d)
	l.CPU, l.Host = "—", "—"
	if j.CPU > 0 {
		l.CPU = fmt.Sprintf("%.0f%% of a core", j.CPU)
	}
	if j.Cores > 0 {
		l.Host = fmt.Sprintf("%.0f%% of %d cores · load %.2f", j.HostCPU, j.Cores, j.Load)
	}

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
		l.Left = left(time.Duration(j.ETA) * time.Second)
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

// gb counts bytes to ten megabytes, so it moves every second a drive is
// busy.
func gb(n int64) string {
	if n >= 1e12 {
		return fmt.Sprintf("%.3f TB", float64(n)/1e12)
	}
	return fmt.Sprintf("%.2f GB", float64(n)/1e9)
}

// trace boils the per-block speeds down to cols columns, outer edge first.
// Each column is two characters: the write pass's height and the read-back's,
// in half rows from '0' up (see traceRows), or '-' for nothing yet.  The
// read-back wins where it has been, so the white catches up with the cyan.
// axis labels the rows: the top speed, half of it, and zero.
func trace(write, read []uint16, cols int) (enc, axis string) {
	n := max(len(write), len(read))
	if n == 0 {
		return "", traceAxis(0)
	}
	cols = min(cols, n)
	type col struct{ w, r float64 }
	cs := make([]col, cols)
	peak := 0.0
	avg := func(v []uint16, from, to int) float64 {
		sum, k := 0.0, 0
		for i := from; i < to && i < len(v); i++ {
			if v[i] > 0 {
				sum += float64(v[i])
				k++
			}
		}
		if k == 0 {
			return 0
		}
		return sum / float64(k)
	}
	for c := range cs {
		from, to := c*n/cols, (c+1)*n/cols
		cs[c] = col{avg(write, from, to), avg(read, from, to)}
		peak = math.Max(peak, math.Max(cs[c].w, cs[c].r))
	}
	if peak == 0 {
		return "", traceAxis(0)
	}
	scale := math.Ceil(peak/50) * 50
	level := func(v float64) byte {
		if v <= 0 {
			return '-'
		}
		return '0' + byte(max(1, min(2*traceRows, int(math.Round(v/scale*2*traceRows)))))
	}
	b := make([]byte, 0, 2*cols)
	for _, c := range cs {
		b = append(b, level(c.w), level(c.r))
	}
	return string(b), traceAxis(scale)
}

// traceRows is how many text rows the graph stands; each holds two levels,
// a lower half block and a full one.
const traceRows = 6

func traceAxis(top float64) string {
	rows := make([]string, traceRows)
	for i := range rows {
		rows[i] = "   ┤"
	}
	if top > 0 {
		rows[0] = fmt.Sprintf("%3.0f┤", top)
		rows[traceRows/2] = fmt.Sprintf("%3.0f┤", top/2)
	}
	rows[traceRows-1] = "  0┤"
	return strings.Join(rows, "\n")
}

// TraceRows draws the graph as rows of half and full blocks, top row first,
// colored by pass.  zawiper.js draws the same thing from the same string;
// keep the two in step.
func TraceRows(enc string) template.HTML {
	var b strings.Builder
	for row := traceRows - 1; row >= 0; row-- {
		run := ""
		for c := 0; c+1 < len(enc); c += 2 {
			lv, class := enc[c+1], "t-r"
			if lv == '-' {
				lv, class = enc[c], "t-w"
			}
			g := " "
			if lv >= '0' && lv <= '0'+2*traceRows {
				switch h := int(lv - '0'); {
				case h >= 2*row+2:
					g = "█"
				case h == 2*row+1:
					g = "▄"
				}
			}
			if class != run {
				if run != "" {
					b.WriteString("</span>")
				}
				fmt.Fprintf(&b, `<span class="%s">`, class)
				run = class
			}
			b.WriteString(g)
		}
		if run != "" {
			b.WriteString("</span>")
		}
		if row > 0 {
			b.WriteString("\n")
		}
	}
	return template.HTML(b.String())
}

// clock is h:mm:ss, the way DEFRAG counted.
func clock(d time.Duration) string {
	d = d.Round(time.Second)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

// left is time to go, as a tile says it: to the minute, never to the second.
func left(d time.Duration) string {
	m := int(d.Round(time.Minute).Minutes())
	switch {
	case m < 1:
		return "under a minute left"
	case m < 60:
		return fmt.Sprintf("about %d min left", m)
	case m%60 == 0:
		return fmt.Sprintf("about %d h left", m/60)
	}
	return fmt.Sprintf("about %d h %d min left", m/60, m%60)
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
		j.CPU, j.HostCPU, j.Cores, j.Load = 0, 0, 0, 0
		j.WriteMBps, j.ReadMBps = nil, nil
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
		Builds   [2]string
	}{b.Online, b.Caps, s.State, s.Message, s.Session, s.ReadAt, s.Drive, s.Devices, s.Refusals, s.RecordID, job, [2]string{s.Software, b.Version}})
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:8])
}
