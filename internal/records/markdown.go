package records

import (
	"fmt"
	"strings"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/wipe"
)

// Viper heads every human copy.  Mandatory, per the spec.
const Viper = `     ___
    / o \___    Z A   W I P E R
    \___   /    aka Za Viper
     ___) (     ssss
    (_____/`

// Markdown is the record a person reads: in a terminal, in a browser, or on
// paper twenty years from now.  It is rendered from the record, never edited.
func Markdown(r *Record) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	d := r.Drive
	model, fw, family := "unknown", "", ""
	if d.SMART != nil {
		model, fw, family = d.SMART.Model, d.SMART.Firmware, d.SMART.Family
	}

	w("```\n%s\n```\n\n", Viper)
	w("# %s\n\n", r.Serial)
	w("%s, %s.  %s.\n\n", model, Size(d.Size), kindLine(r))

	w("| | |\n|---|---|\n")
	w("| Serial | `%s` |\n", r.Serial)
	w("| Model | %s |\n", model)
	if family != "" {
		w("| Family | %s |\n", family)
	}
	if d.SMART != nil && d.SMART.WWN != "" {
		w("| WWN | `%s` |\n", d.SMART.WWN)
	}
	if fw != "" {
		w("| Firmware | %s |\n", fw)
	}
	w("| Capacity | %s (%d bytes) |\n", Size(d.Size), d.Size)
	w("| Sectors | %d logical / %d physical bytes |\n", d.LogicalSector, d.PhysicalSector)
	if d.SMART != nil {
		w("| Type | %s |\n", Rotation(d.SMART.RotationRate))
	}
	w("| Bridge | %s |\n", BridgeLine(d.Bridge))
	w("| Record | `%s` |\n", r.ID)
	w("| Read | %s |\n", When(r.Finished))
	w("| Bay | %s on %s |\n", r.Bay, r.Host)
	w("| Za Wiper | %s |\n\n", r.Software)

	w("## Health: %s\n\n", r.Health.Grade)
	if s := d.SMART; s != nil {
		status := "didn't say"
		if s.Passed != nil && *s.Passed {
			status = "PASSED"
		} else if s.Passed != nil {
			status = "FAILING"
		}
		w("The drive's own verdict: %s.  Power-on %s hours, %s power cycles, %d °C.\n\n",
			status, Commas(int64(s.PowerOnHours)), Commas(int64(s.PowerCycles)), s.TempC)
	}
	for _, n := range r.Health.Notes {
		w("- %s\n", n)
	}
	if len(r.Health.Notes) > 0 {
		w("\n")
	}
	if s := d.SMART; s != nil && len(s.Attrs) > 0 {
		w("| ID | Attribute | Value | Worst | Thresh | Failed | Raw |\n|--:|---|--:|--:|--:|---|---|\n")
		for _, a := range s.Attrs {
			w("| %d | %s | %d | %d | %d | %s | %s |\n", a.ID, a.Name, a.Value, a.Worst, a.Thresh, a.WhenFailed, a.RawString)
		}
		w("\n")
	}

	if r.Wipe != nil {
		wipeSection(w, r)
		w("## Found before the wipe\n\n%s\n\n", FoundLine(&d))
	} else {
		w("## Found on it\n\n%s\n\n", FoundLine(&d))
	}
	for _, p := range d.Table.Parts {
		w("- `%s` %s", p.Path, Size(p.Size))
		if p.FSType != "" {
			w(" · %s", p.FSType)
		}
		if p.Label != "" {
			w(" `%s`", p.Label)
		}
		if p.Used >= 0 {
			w(" · %s used", Size(p.Used))
		}
		if p.Peek != "" {
			w(" · _%s_", p.Peek)
		}
		if p.Backup != "" {
			w(" · **looks like a backup: %s**", p.Backup)
		}
		w("\n")
	}
	if d.Table.Note != "" {
		w("\n%s\n", d.Table.Note)
	}

	if len(r.Refusals) > 0 {
		w("\n## Refused\n\n")
		for _, x := range r.Refusals {
			w("- %s\n", x)
		}
	}

	w("\n---\n\n")
	if !r.Wrote {
		w("Nothing was written to this drive.  ")
	}
	if r.Wipe != nil && r.Wipe.OK() {
		w("Every byte of this drive was overwritten with a keystream from a key that existed only in memory, then read back and compared.  ")
	}
	w("This record states what Za Wiper observed on the date shown, and nothing more.  smartctl's raw output is kept beside it as `smart.txt` and `smart.json`")
	if r.Before != nil {
		w(", with the pre-wipe readings in `smart-before.txt` and `smart-before.json`")
	}
	w(".\n")
	return b.String()
}

func kindLine(r *Record) string {
	switch r.Kind {
	case "inspect":
		return "Inspected, read only"
	case "wipe":
		if r.Wipe != nil {
			return "Wiped, " + WipeVerdict(r.Wipe)
		}
	}
	return r.Kind
}

// FoundLine is the one-sentence answer to "what's on it?"
func FoundLine(d *model.Drive) string {
	t := d.Table
	if t.Type == "" && len(t.Parts) == 0 {
		return "No partition table, no filesystems.  Looks blank already."
	}
	var bits []string
	if t.Type != "" {
		bits = append(bits, strings.ToUpper(t.Type), plural(len(t.Parts), "partition", "partitions"))
	} else {
		bits = append(bits, "No partition table", "a filesystem on the bare disk")
	}
	used, complete := d.UsedBytes()
	switch {
	case used > 0 && complete:
		bits = append(bits, Size(used)+" of files")
	case used > 0:
		bits = append(bits, "at least "+Size(used)+" of files")
	case !complete:
		bits = append(bits, "contents unmeasured")
	default:
		bits = append(bits, "no file data")
	}
	return strings.Join(bits, " · ") + "."
}

func BridgeLine(b model.Bridge) string {
	var parts []string
	if b.Manufacturer != "" || b.Product != "" {
		parts = append(parts, strings.TrimSpace(b.Manufacturer+" "+b.Product))
	}
	if b.VendorID != "" {
		parts = append(parts, b.VendorID+":"+b.ProductID)
	}
	if b.Serial != "" {
		parts = append(parts, "bridge serial "+b.Serial)
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " · ")
}

func Rotation(rpm int) string {
	if rpm == 0 {
		return "solid state"
	}
	return fmt.Sprintf("HDD, %d rpm", rpm)
}

// When is local time with UTC beside it, so nobody has to do arithmetic.
func When(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return fmt.Sprintf("%s (%s)", t.Local().Format("2006-01-02 15:04 MST"), t.UTC().Format("15:04 UTC"))
}

// Size is decimal, the way drives are sold.
func Size(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f, i := float64(n), 0
	for f >= unit && i < 5 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.2f %cB", f, " KMGTP"[i])
}

// Commas groups thousands: 11,196.
func Commas(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Surface draws a wipe one character a block, the way DEFRAG drew a disk.
func Surface(w *wipe.Result) string {
	if w == nil {
		return ""
	}
	slow := map[int]bool{}
	for _, i := range append(w.Slow(false), w.Slow(true)...) {
		slow[i] = true
	}
	var b strings.Builder
	for i, c := range w.Cells {
		if i > 0 && i%64 == 0 {
			b.WriteByte('\n')
		}
		switch {
		case c.Bad > 0 || c.Err != "":
			b.WriteString("B")
		case c.Read == 0:
			b.WriteString("░")
		case slow[i]:
			b.WriteString("▒")
		default:
			b.WriteString("█")
		}
	}
	return b.String()
}

// SurfaceLegend explains Surface.
func SurfaceLegend(w *wipe.Result) string {
	return fmt.Sprintf("█ written and verified · ▒ slow · B bad · ░ not reached · one block = %s", Size(w.Block))
}

// WipeVerdict is the wipe's result in a few words.
func WipeVerdict(w *wipe.Result) string {
	switch {
	case w.OK():
		return "verified"
	case w.Aborted != "":
		return "stopped, " + strings.TrimPrefix(w.Aborted, "stopped ")
	default:
		return "FAILED verification"
	}
}

// Pass is how long a pass took and how fast it went.
func Pass(w *wipe.Result, read bool) string {
	from, to := w.Started, w.Wrote
	if read {
		from, to = w.Wrote, w.Verified
	}
	if to.IsZero() {
		return "didn't finish"
	}
	d := to.Sub(from).Round(time.Second)
	return fmt.Sprintf("%d:%02d:%02d · %.0f MB/s", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, w.MBps(read))
}

// SlowLine names the slow blocks, numbered from 1 like a person would.
func SlowLine(w *wipe.Result) string {
	seen := map[int]bool{}
	var s []string
	for _, i := range append(w.Slow(false), w.Slow(true)...) {
		if !seen[i] {
			seen[i] = true
			s = append(s, fmt.Sprintf("#%d", i+1))
		}
	}
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}

func wipeSection(w func(string, ...any), r *Record) {
	x := r.Wipe
	w("## Wipe: %s\n\n%s.\n\n", WipeVerdict(x), x.Method)
	w("| | |\n|---|---|\n")
	w("| Started | %s |\n", When(x.Started))
	w("| Write pass | %s |\n", Pass(x, false))
	w("| Verify pass | %s |\n", Pass(x, true))
	w("| Read back as written | %s of %s bytes |\n", Commas(x.Checked), Commas(x.Size))
	if x.Bad > 0 {
		w("| Bad | %s bytes |\n", Commas(x.Bad))
	}
	w("| Slow blocks | %s |\n\n", SlowLine(x))
	w("```\n%s\n```\n\n%s\n\n", Surface(x), SurfaceLegend(x))
}
