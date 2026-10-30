package web

import (
	"fmt"
	"math"
	"strings"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/records"
)

// The speed scale under the throughput graph: what kind of drive moves data
// at this rate, as a best guess.  Each band starts where the one below it
// ends, in MB/s of sustained sequential transfer.  The bands overlap in
// real life: a 2011 7200 rpm drive's outer edge runs no faster than a
// recent 5400's, and every disk's inner tracks run at about half its outer
// edge.  So the guess goes by the fastest blocks seen (the outer edge, where
// a wipe starts) and says what SMART claims beside it.
var bands = []struct {
	label string  // on the scale, four characters at most
	words string  // in the guess
	from  float64 // MB/s
}{
	{"slow", "slower than a 5,400 rpm disk", 0},
	{"5400", "a 5,400 rpm disk", 60},
	{"7200", "a 7,200 rpm disk", 120},
	{"10k", "a 10,000 rpm disk", 220},
	{"15k", "a 15,000 rpm disk", 260},
	{"SSD", "a SATA SSD", 330},
	{"NVMe", "an NVMe SSD", 700},
	{"fast", "faster than NVMe", 7500},
}

// bandTop is where the scale ends, for placing a mark in the last band.
const bandTop = 20000

// slot is how many characters each band takes on the scale.  Eight of them
// make 40, about as wide as the throughput graph.
const slot = 5

func bandOf(mbps float64) int {
	i := 0
	for k, b := range bands {
		if mbps >= b.from {
			i = k
		}
	}
	return i
}

// bandCol is where a rate falls on the scale, in characters: across its
// band on a log scale, since the bands widen as they go up.
func bandCol(mbps float64) int {
	if mbps <= 0 {
		return 0
	}
	k := bandOf(mbps)
	lo, hi := math.Max(1, bands[k].from), float64(bandTop)
	if k+1 < len(bands) {
		hi = bands[k+1].from
	}
	frac := math.Log(math.Max(mbps, lo)/lo) / math.Log(hi/lo)
	return min(len(bands)*slot-1, k*slot+int(math.Min(0.999, frac)*slot))
}

// A link's practical ceiling, below its signalling rate: protocol overhead
// and, for USB, the bridge.
type link struct {
	name string
	mbps float64
}

func usbLink(speed int) (link, bool) {
	switch {
	case speed <= 0:
		return link{}, false
	case speed <= 12:
		return link{"USB 1.1", 1}, true
	case speed <= 480:
		return link{"USB 2", 40}, true
	case speed <= 5000:
		return link{"USB 3 (5 Gb/s)", 450}, true
	case speed <= 10000:
		return link{"USB 3 (10 Gb/s)", 1000}, true
	case speed <= 20000:
		return link{"USB 3 (20 Gb/s)", 2000}, true
	}
	return link{fmt.Sprintf("USB4 (%d Gb/s)", speed/1000), 3500}, true
}

// sataLink: 8b/10b puts ten bits on the wire for each byte, and framing
// takes about another 8%.  6.0 Gb/s comes to about 550 MB/s.
func sataLink(name string, bps int64) (link, bool) {
	if bps <= 0 {
		return link{}, false
	}
	return link{"SATA " + name, float64(bps) / 10 / 1e6 * 0.92}, true
}

// links are the drive's links that could cap the rate, slowest first.
func links(d *model.Drive) []link {
	if d == nil {
		return nil
	}
	var ls []link
	if l, ok := usbLink(d.Bridge.Speed); ok {
		ls = append(ls, l)
	}
	if d.SMART != nil {
		if l, ok := sataLink(d.SMART.Link, d.SMART.LinkBps); ok {
			ls = append(ls, l)
		}
	}
	if len(ls) == 2 && ls[1].mbps < ls[0].mbps {
		ls[0], ls[1] = ls[1], ls[0]
	}
	return ls
}

func peak(rates ...[]uint16) float64 {
	p := 0
	for _, r := range rates {
		for _, v := range r {
			p = max(p, int(v))
		}
	}
	return float64(p)
}

// Band draws the scale, a mark at the rate right now, and a wall where the
// link tops out, with everything past it shaded:
//
//	slow 5400 7200  10k  15k  SSD NVMe fast
//	──────────▲──────────────────┤░░░░░░░░░
//
// and words for the guess and the link.  The scale is plain text, so the
// page can update it in place.
func Band(j *model.Job, d *model.Drive) (scale, guess, limit string) {
	var top strings.Builder
	for _, b := range bands {
		pad := slot - len(b.label)
		top.WriteString(strings.Repeat(" ", pad-pad/2) + b.label + strings.Repeat(" ", pad/2))
	}
	rule := []rune(strings.Repeat("─", len(bands)*slot))
	ls := links(d)
	if len(ls) > 0 {
		wall := bandCol(ls[0].mbps)
		rule[wall] = '┤'
		for i := wall + 1; i < len(rule); i++ {
			rule[i] = '░'
		}
	}
	if j.MBps > 0 {
		rule[bandCol(j.MBps)] = '▲'
	}
	scale = strings.TrimRight(top.String(), " ") + "\n" + string(rule)

	switch p := peak(j.WriteMBps, j.ReadMBps); {
	case p <= 0:
		guess = "Best guess: once the first block is through."
	case len(ls) > 0 && p >= 0.85*ls[0].mbps:
		guess = fmt.Sprintf("Best guess: can't tell.  The peak, %.0f MB/s, is %s's ceiling, so the link gives out before the drive does.", p, ls[0].name)
	default:
		guess = fmt.Sprintf("Best guess: %s, from a peak of %.0f MB/s.", bands[bandOf(p)].words, p)
	}
	if d != nil && d.SMART != nil {
		if r := d.SMART.RotationRate; r > 0 {
			guess += fmt.Sprintf("  SMART says %s rpm.", records.Commas(int64(r)))
		} else {
			guess += "  SMART says solid state."
		}
	}

	if len(ls) == 0 {
		return scale, guess, "Link: not reported, so no ceiling is marked."
	}
	var parts []string
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s, about %.0f MB/s", l.name, l.mbps))
	}
	mark := "  ┤ is where it tops out."
	if len(ls) > 1 {
		mark = "  ┤ is where the slower one tops out."
	}
	return scale, guess, "Link: " + strings.Join(parts, " · ") + "." + mark
}
