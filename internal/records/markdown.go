package records

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/atmarx/zawiper/internal/model"
	"github.com/atmarx/zawiper/internal/qr"
	"github.com/atmarx/zawiper/internal/smart"
	"github.com/atmarx/zawiper/internal/wipe"
)

// Viper heads every human copy.  Mandatory, per the spec.
const Viper = `     ___
    / o \___    Z A   W I P E R
    \___   /    aka Za Viper
     ___) (     ssss
    (_____/`

// Markdown is the record a person reads: in a terminal, in a browser, or on
// paper twenty years from now.  It is rendered from the record, never edited.
func Markdown(r *Record) string { return MarkdownSealed(r, nil) }

// MarkdownSealed is Markdown with what the signature says, once there is
// one.  The hub renders record.md again after it signs record.json.
func MarkdownSealed(r *Record, seal *Seal) string {
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

	if r.Wipe != nil {
		certificateSection(w, r)
	}

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
	if fs := r.Health.Findings; len(fs) == len(r.Health.Notes) {
		for _, f := range fs {
			w("- `%s` %s\n", strings.TrimSpace(f.Tag+" "+f.Value), f.Text)
		}
	} else {
		for _, n := range r.Health.Notes {
			w("- %s\n", n)
		}
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
		checkSection(w, r, seal)
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
		w("%s  ", Overwritten(r.Wipe))
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
	glyph := map[byte]string{cellBad: "B", cellTodo: "░", cellSlow: "▒", cellOK: "█"}
	var b strings.Builder
	for i, c := range cells(w) {
		if i > 0 && i%mapCols == 0 {
			b.WriteByte('\n')
		}
		b.WriteString(glyph[c])
	}
	return b.String()
}

// The surface map is mapCols blocks a row, in the text and in the picture.
const mapCols = 64

const (
	cellOK   = 'v' // written and read back right
	cellSlow = 's'
	cellBad  = 'b'
	cellTodo = 't' // not reached
)

// cells classifies each block of a wipe, worst first.
func cells(w *wipe.Result) []byte {
	slow := map[int]bool{}
	for _, i := range append(w.Slow(false), w.Slow(true)...) {
		slow[i] = true
	}
	out := make([]byte, len(w.Cells))
	for i, c := range w.Cells {
		switch {
		case c.Bad > 0 || c.Err != "":
			out[i] = cellBad
		case c.Read == 0:
			out[i] = cellTodo
		case slow[i]:
			out[i] = cellSlow
		default:
			out[i] = cellOK
		}
	}
	return out
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

// Overwritten is the one sentence that says what the wipe did to every byte.
func Overwritten(x *wipe.Result) string {
	if x.Key == "" {
		return "Every byte of this drive was overwritten with a keystream from a key that existed only in memory, then read back and compared."
	}
	return "Every byte of this drive was overwritten with a keystream, then read back and compared.  The key is published in this record, so anyone can regenerate the stream and check the drive."
}

// Seal is what the certificate says about the record's signature.  The sign
// package fills it in; records only prints it.  A Seal with no Fingerprint
// is a record nobody has signed yet.
type Seal struct {
	Fingerprint string // SHA256:…, as `ssh-keygen -l` prints it
	PublicKey   string // ssh-ed25519 AAAA… za-wiper
	KeyURL      string // where the public key is published, if anywhere
	RecordURL   string // this record's page on the hub, if it has an address
	SSHKeygen   string // the commands that check the signature with OpenSSH
	Zverify     string // the command that checks the drive against the record
	Problem     string // why the signature on file doesn't hold, when it doesn't
}

// Signed says the record carries a signature that checks out.
func (s *Seal) Signed() bool { return s != nil && s.Fingerprint != "" && s.Problem == "" }

// Sanitization is what a wipe record can claim under NIST SP 800-88 Rev. 2.
// Level is "Clear" or empty.  It is never "Purge": that needs the drive's
// own sanitize command, and nothing in Za Wiper runs one yet.
type Sanitization struct {
	Level     string
	Why       string // why there's no claim, when Level is empty
	Technique string
	Standard  string
	Verify    string // how the wipe was verified
	Result    string // what verification found
	Limits    string // what this level can't reach
}

// StandardClear names what a Clear certificate follows.  Rev. 2 leaves the
// techniques themselves to IEEE 2883.
const StandardClear = "NIST SP 800-88 Rev. 2, Guidelines for Media Sanitization (September 2025), at the Clear level.  Technique from IEEE Std 2883-2022, IEEE Standard for Sanitizing Storage: §8.4.2.2, Clear for ATA, and §7.2, full verification."

// Sanitize reads the claim a record supports, and nothing more.
func Sanitize(r *Record) Sanitization {
	x := r.Wipe
	if x == nil {
		return Sanitization{Why: "This record is an inspection.  Nothing was written to the drive."}
	}
	s := Sanitization{
		Standard:  StandardClear,
		Technique: fmt.Sprintf("Overwrite.  One pass of an AES-256-CTR keystream over all %s bytes a host can address, from the first to the last.", Commas(x.Size)),
		Verify:    "Full verification (IEEE 2883 §7.2).  The keystream was regenerated from its key, and every byte was read back from the drive and compared.",
		Result:    fmt.Sprintf("%s of %s bytes read back as written.", Commas(x.Checked), Commas(x.Size)),
		Limits:    "An overwrite reaches every sector a host can address.  It can't reach sectors the drive has retired and remapped; that takes Purge, the drive's own sanitize command.",
	}
	if x.Bad > 0 {
		s.Result += fmt.Sprintf("  %s bytes didn't.", Commas(x.Bad))
	}
	c := r.Drive.Capacity
	if c.Checked() && c.Hidden() == 0 {
		s.Technique += fmt.Sprintf("  No HPA or DCO keeps any sectors from the host (%s: %s of %s).", c.Method, Commas(c.Visible), Commas(c.Native))
	}
	if r.Drive.SMART != nil {
		if a, ok := r.Drive.SMART.Attr(5); ok {
			s.Limits += fmt.Sprintf("  This drive reports %s reallocated sectors.", Commas(a.Raw))
		}
	}
	switch {
	case x.Aborted != "":
		s.Why = "The wipe was " + x.Aborted + ", so it covers only part of the drive."
	case x.Bad > 0:
		s.Why = fmt.Sprintf("%s bytes didn't read back as written.", Commas(x.Bad))
	case !x.OK():
		s.Why = "Not every byte was both written and read back."
	case x.Size != r.Drive.Size:
		s.Why = fmt.Sprintf("The wipe covered %s bytes of a %s-byte drive.", Commas(x.Size), Commas(r.Drive.Size))
	case r.Drive.SMART == nil:
		s.Why = "The drive's type couldn't be read, so no level can be claimed for it."
	case r.Drive.SMART.RotationRate == 0:
		s.Why = "This is a solid-state drive.  An overwrite proves nothing on flash, where the controller keeps cells a host can't address.  Flash needs the drive's own sanitize command."
	case c == nil:
		s.Why = "This record doesn't say whether the drive keeps sectors from the host behind an HPA or DCO.  IEEE 2883 §8.4.2.2 needs those out of the way before an overwrite counts as Clear."
	case !c.Checked():
		s.Why = "Za Wiper couldn't check whether the drive keeps sectors from the host behind an HPA or DCO, which IEEE 2883 §8.4.2.2 needs out of the way before an overwrite counts as Clear.  " + c.Note
	case c.Hidden() > 0:
		s.Why = fmt.Sprintf("The drive keeps %s sectors from the host behind an HPA or DCO, and the overwrite didn't reach them.", Commas(c.Hidden()))
	default:
		s.Level = "Clear"
	}
	return s
}

// Media is the media type, as the certificate names it.
func Media(d *model.Drive) string {
	switch {
	case d.SMART == nil:
		return "Unknown: SMART couldn't be read"
	case d.SMART.RotationRate == 0:
		return "Flash: solid-state drive"
	}
	return fmt.Sprintf("Magnetic: hard disk drive, %s rpm", Commas(int64(d.SMART.RotationRate)))
}

// HealthRow is one line of SMART, before the wipe and after it.
type HealthRow struct {
	What, Before, After string
	Worse               bool
}

// HealthRows compares the readings that matter to a buyer.  A wipe that
// writes and reads every sector is the hardest a drive gets used, so a
// count that rose during it is worth knowing.
func HealthRows(r *Record) []HealthRow {
	before, after := r.Before, r.Drive.SMART
	if after == nil {
		return nil
	}
	verdict := func(i *smart.Info) string {
		switch {
		case i == nil:
			return "—"
		case i.Passed == nil:
			return "didn't say"
		case *i.Passed:
			return "PASSED"
		}
		return "FAILING"
	}
	rows := []HealthRow{{What: "Drive's own verdict", Before: verdict(before), After: verdict(after),
		Worse: verdict(after) == "FAILING" && verdict(before) != "FAILING"}}
	add := func(what string, get func(*smart.Info) (int64, bool), worseUp bool) {
		a, ok := get(after)
		if !ok {
			return
		}
		row := HealthRow{What: what, Before: "—", After: Commas(a)}
		if before != nil {
			if b, ok := get(before); ok {
				row.Before = Commas(b)
				row.Worse = worseUp && a > b
			}
		}
		rows = append(rows, row)
	}
	for _, a := range []struct {
		id   int
		what string
	}{
		{5, "Reallocated sectors"},
		{197, "Pending sectors"},
		{198, "Offline uncorrectable"},
		{187, "Reported uncorrectable"},
		{199, "Interface CRC errors"},
	} {
		add(a.what, func(i *smart.Info) (int64, bool) {
			x, ok := i.Attr(a.id)
			return x.Raw, ok
		}, true)
	}
	add("Error log entries", func(i *smart.Info) (int64, bool) { return int64(i.ErrorLogCount), true }, true)
	add("Temperature, °C", func(i *smart.Info) (int64, bool) { return int64(i.TempC), i.TempC > 0 }, false)
	add("Power-on hours", func(i *smart.Info) (int64, bool) { return int64(i.PowerOnHours), true }, false)
	return rows
}

// KeyCheck is the whole-drive check with stock tools: OpenSSL makes the
// keystream from the published key, and cmp holds the drive against it.
func KeyCheck(x *wipe.Result, dev string) string {
	return fmt.Sprintf("openssl enc -aes-256-ctr -K %s -iv %s -in /dev/zero 2>/dev/null | cmp -n %d - %s && echo every byte matches", x.Key, x.IV, x.Size, dev)
}

// Certificate is a wipe record laid out as a certificate of sanitization,
// for the record page and the sheet on the bag.
type Certificate struct {
	R       *Record
	Seal    *Seal // never nil; a Seal with no Fingerprint is unsigned
	San     Sanitization
	ForSale bool
	Verdict string // WIPED · VERIFIED, or NOT FOR SALE
	Why     string // why not, when it isn't for sale
	Date    string // the day the wipe was verified
	Model   string
	Media   string
	Health  []HealthRow
	Map     template.HTML // the surface map, as SVG
	Legend  []LegendItem
	QR      template.HTML // the record's address, as SVG; empty without one
	Check   string        // KeyCheck on /dev/sdX; empty when no key was kept
	Zverify string
}

type LegendItem struct {
	Class, Label string
	Count        int
}

// NewCertificate lays out a wipe record.  It returns nil for anything else:
// an inspection certifies nothing.
func NewCertificate(r *Record, seal *Seal) *Certificate {
	if r == nil || r.Wipe == nil {
		return nil
	}
	if seal == nil {
		seal = &Seal{}
	}
	x := r.Wipe
	c := &Certificate{R: r, Seal: seal, San: Sanitize(r), Model: "unknown model", Media: Media(&r.Drive),
		Health: HealthRows(r), Zverify: seal.Zverify}
	if r.Drive.SMART != nil {
		c.Model = r.Drive.SMART.Model
	}
	c.ForSale = c.San.Level != "" && r.Health.Grade != smart.Fail
	switch {
	case c.ForSale:
		c.Verdict = "WIPED · VERIFIED"
	case c.San.Level == "":
		c.Verdict, c.Why = "NOT FOR SALE", c.San.Why
	default:
		c.Verdict, c.Why = "NOT FOR SALE", "Wiped and verified, but the drive's health failed."
		if len(r.Health.Notes) > 0 {
			c.Why += "  " + r.Health.Notes[len(r.Health.Notes)-1]
		}
	}
	day := x.Verified
	if day.IsZero() {
		day = r.Finished
	}
	c.Date = day.UTC().Format("2006-01-02")
	c.Map, c.Legend = SurfaceSVG(x)
	if seal.RecordURL != "" {
		if q, err := qr.Encode([]byte(seal.RecordURL)); err == nil {
			c.QR = template.HTML(q.SVG("qr"))
		}
	}
	if x.Key != "" {
		c.Check = KeyCheck(x, "/dev/sdX")
	}
	if c.Zverify == "" {
		c.Zverify = "zverify -no-sig record.json /dev/sdX"
	}
	return c
}

// SurfaceSVG draws the surface map for print: the same blocks as Surface,
// in DEFRAG's colours, one unit a block so CSS can size it to the space.
// Runs of one colour are one rectangle, and a pattern draws the grid, so a
// 4 TB map stays small.
func SurfaceSVG(w *wipe.Result) (template.HTML, []LegendItem) {
	cs := cells(w)
	cols := min(mapCols, max(len(cs), 1))
	rows := max((len(cs)+cols-1)/cols, 1)
	n := map[byte]int{}
	for _, c := range cs {
		n[c]++
	}
	legend := []LegendItem{
		{"m-v", "written and verified", n[cellOK]},
		{"m-s", "slow", n[cellSlow]},
		{"m-b", "bad", n[cellBad]},
		{"m-t", "not reached", n[cellTodo]},
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="map" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" preserveAspectRatio="xMinYMin meet" role="img" aria-label="Surface map: %d blocks of %s.  %d verified, %d slow, %d bad, %d not reached.">`,
		cols, rows, len(cs), Size(w.Block), n[cellOK], n[cellSlow], n[cellBad], n[cellTodo])
	b.WriteString(`<defs><pattern id="map-grid" width="1" height="1" patternUnits="userSpaceOnUse"><path class="m-grid" d="M1 0V1H0" fill="none" stroke-width=".3"/></pattern></defs>`)
	fmt.Fprintf(&b, `<rect class="m-field" width="%d" height="%d"/>`, cols, rows)
	for i := 0; i < len(cs); {
		x, y, k := i%cols, i/cols, 1
		for x+k < cols && i+k < len(cs) && cs[i+k] == cs[i] {
			k++
		}
		fmt.Fprintf(&b, `<rect class="m-%c" x="%d" y="%d" width="%d" height="1"/>`, cs[i], x, y, k)
		i += k
	}
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="url(#map-grid)"/></svg>`, cols, rows)
	return template.HTML(b.String()), legend
}

func certificateSection(w func(string, ...any), r *Record) {
	c := NewCertificate(r, nil)
	s := c.San
	w("## Certificate of sanitization\n\n")
	if s.Level == "" {
		w("**No sanitization is claimed.**  %s\n\n", s.Why)
	}
	w("| | |\n|---|---|\n")
	if s.Level != "" {
		w("| Level | **%s** |\n", s.Level)
	}
	w("| Technique | %s |\n", s.Technique)
	w("| Standard | %s |\n", s.Standard)
	w("| Media | %s |\n", c.Media)
	w("| Verification | %s |\n", s.Verify)
	w("| Result | %s |\n", s.Result)
	w("| Completed | %s |\n", When(r.Wipe.Verified))
	w("| Tool | %s |\n", r.Software)
	w("| Performed by | bay %s on %s, unattended |\n", r.Bay, r.Host)
	w("| Grade | %s |\n\n", r.Health.Grade)
	w("%s\n\n", s.Limits)
	if !c.ForSale {
		w("**NOT FOR SALE.**  %s\n\n", c.Why)
	}
	if rows := c.Health; len(rows) > 0 {
		w("### Health before and after\n\n| | Before | After |\n|---|--:|--:|\n")
		for _, h := range rows {
			mark := ""
			if h.Worse {
				mark = " ▲"
			}
			w("| %s | %s | %s%s |\n", h.What, h.Before, h.After, mark)
		}
		w("\n")
	}
}

func checkSection(w func(string, ...any), r *Record, seal *Seal) {
	x := r.Wipe
	c := NewCertificate(r, seal)
	w("## Check it yourself\n\n")
	if x.Key == "" {
		w("This wipe ran before Za Wiper published its keys, so the stream can't be regenerated.  The read-back above is the only check.\n\n")
	} else {
		w("The wipe's key is published on purpose.  It regenerates the noise that was written, so anyone can read the drive back against it without trusting us.  The old data is gone either way; the key only proves it.\n\n")
		w("| | |\n|---|---|\n")
		w("| Keystream | AES-256-CTR, the IV as the counter for the drive's first 16 bytes |\n")
		w("| Key | `%s` |\n", x.Key)
		w("| IV | `%s` |\n", x.IV)
		w("| Length | %s bytes |\n\n", Commas(x.Size))
		w("Every byte, with OpenSSL and cmp, for as long as reading the whole drive takes:\n\n    %s\n\n", c.Check)
		w("With zverify, which also checks the record and its signature.  It reads two random blocks from each ten-thousandth of the drive, as IEEE 2883 §7.3 suggests; add `-full` for every byte:\n\n    %s\n\n", c.Zverify)
		w("Anything written to the drive since the wipe, even a partition table, won't match.\n\n")
	}
	w("### Signature\n\n")
	switch {
	case c.Seal.Signed():
		w("`record.json` is signed with the hub's key: SSHSIG, Ed25519, namespace `za-wiper`.  The signature is `record.json.sig`, beside it.\n\n")
		w("| | |\n|---|---|\n")
		w("| Key | `%s` |\n", c.Seal.Fingerprint)
		if c.Seal.KeyURL != "" {
			w("| Published at | %s |\n", c.Seal.KeyURL)
		}
		if c.Seal.RecordURL != "" {
			w("| Record | %s |\n", c.Seal.RecordURL)
		}
		w("| Public key | `%s` |\n\n", c.Seal.PublicKey)
		w("With OpenSSH, in the folder holding `record.json` and `record.json.sig`:\n\n")
		for _, l := range strings.Split(c.Seal.SSHKeygen, "\n") {
			w("    %s\n", l)
		}
		w("\n")
	case c.Seal.Problem != "":
		w("**The signature on file doesn't hold.**  %s\n\n", c.Seal.Problem)
	default:
		w("Not signed yet.  An unsigned certificate is just a nice PDF.\n\n")
	}
}
