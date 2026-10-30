// zverify checks a wiped drive against its Za Wiper record, for a buyer who
// has no reason to trust us.  The record publishes the wipe's key, so
// zverify can regenerate exactly what the wipe wrote and hold the drive up
// against it.  It opens the drive read-only and changes nothing.
//
//	zverify -pubkey za-wiper.pub record.json /dev/sdX
//	zverify -pubkey https://…/za-wiper.pub https://…/record.json /dev/sdX
//	zverify -no-sig -full record.json /dev/sdX
//
// By default it samples the way IEEE 2883-2022 §7.3 suggests: the drive cut
// into 10,000 equal sections, two random 4 KiB blocks read from each, plus
// the first and last.  -sample N reads N random 1 MiB samples instead.
// -full reads every byte, which takes as long as reading the
// whole drive.  It exits 0 when everything matches, 1 when something
// doesn't, and 2 when it couldn't check at all.
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/sign"
	"github.com/atmarx/zawiper/internal/wipe"
)

const (
	mib    = 1 << 20
	sector = 512
	// sections is IEEE 2883 §7.3's suggested minimum number of subsections
	// for a drive addressed by LBA.
	sections = 10000
	block    = 4096
	// maxRanges is how many mismatched ranges get their own line.
	maxRanges = 20
)

// client fetches records and keys given as URLs.  Tests swap it.
var client = &http.Client{Timeout: time.Minute}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("zverify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pubkey := fs.String("pubkey", "", "the public key you trust: a file or an https URL, from the certificate")
	noSig := fs.Bool("no-sig", false, "don't check the signature (then anyone could have written the record)")
	samples := fs.Int("sample", 100, "check this many random 1 MiB samples, counting the first and last MiB, instead of IEEE 2883 §7.3 sampling")
	full := fs.Bool("full", false, "check every byte instead of samples")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: zverify [-pubkey file|-no-sig] [-sample N | -full] record.json /dev/sdX")
		fmt.Fprintln(stderr, "\nChecks a wiped drive against its Za Wiper record.  record.json may be a file or an")
		fmt.Fprintln(stderr, "https URL; its signature is read from the same place with .sig on the end.  The")
		fmt.Fprintln(stderr, "drive is opened read-only.")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case fs.NArg() != 2:
		fs.Usage()
		return 2
	case *pubkey == "" && !*noSig:
		fmt.Fprintln(stderr, "Say which key you trust with -pubkey (the published key on the certificate), or skip the signature with -no-sig.")
		return 2
	case *pubkey != "" && *noSig:
		fmt.Fprintln(stderr, "-pubkey and -no-sig contradict each other.  Pick one.")
		return 2
	case set["sample"] && *full:
		fmt.Fprintln(stderr, "-sample and -full contradict each other.  Pick one.")
		return 2
	case *samples < 1:
		fmt.Fprintln(stderr, "-sample needs at least 1.")
		return 2
	}
	v := &verifier{out: stdout, errw: stderr}
	return v.verify(fs.Arg(0), fs.Arg(1), *pubkey, *samples, *full, !set["sample"])
}

type verifier struct {
	out, errw io.Writer
}

func (v *verifier) say(f string, a ...any) { fmt.Fprintf(v.out, f+"\n", a...) }

// verify returns the exit status.
func (v *verifier) verify(recordSrc, dev, pubSrc string, samples int, full, stratified bool) int {
	js, err := load(recordSrc)
	if err != nil {
		fmt.Fprintf(v.errw, "Couldn't read the record: %v\n", err)
		return 2
	}

	// The signature first: an unsigned record's claims are just claims.
	if pubSrc == "" {
		v.say("Signature: not checked (-no-sig).  Anyone could have written this record.")
	} else {
		line, err := load(pubSrc)
		if err != nil {
			fmt.Fprintf(v.errw, "Couldn't read the key: %v\n", err)
			return 2
		}
		pub, err := sign.ParseAuthorizedKey(line)
		if err != nil {
			fmt.Fprintf(v.errw, "%s: %v\n", pubSrc, err)
			return 2
		}
		sig, err := load(recordSrc + ".sig")
		if err != nil {
			v.say("Signature: missing.  There's no %s.sig to check (%v).", recordSrc, err)
			v.say("Stopping here: an unsigned record proves nothing.  Use -no-sig to check the drive anyway.")
			return 1
		}
		if err := sign.Verify(pub, js, sig); err != nil {
			v.say("Signature: BAD.  %s.", upper(err.Error()))
			v.say("Stopping here: a record that fails its signature proves nothing.")
			return 1
		}
		v.say("Signature: good.  The record was signed by %s, the key in %s.", sign.Fingerprint(pub), pubSrc)
	}

	var r records.Record
	if err := json.Unmarshal(js, &r); err != nil {
		fmt.Fprintf(v.errw, "The record isn't valid JSON: %v\n", err)
		return 2
	}
	x := r.Wipe
	if r.Kind != "wipe" || x == nil {
		v.say("Record: this is a%s %q record, not a wipe.  There's nothing to check the drive against.", an(r.Kind), r.Kind)
		return 1
	}
	model := "an unknown model"
	if r.Drive.SMART != nil {
		model = r.Drive.SMART.Model
	}
	v.say("Record: a wipe of %s, serial %s, %s (%s bytes), finished %s.  Grade: %s.",
		model, r.Serial, records.Size(x.Size), records.Commas(x.Size), x.Verified.UTC().Format("2006-01-02 15:04 UTC"), r.Health.Grade)
	switch {
	case x.Aborted != "":
		v.say("The record says the wipe was %s, so there's no whole-drive wipe to check.", x.Aborted)
		return 1
	case !x.OK():
		v.say("The record says the wipe didn't read back clean: %s of %s bytes matched, %s bad.", records.Commas(x.Checked), records.Commas(x.Size), records.Commas(x.Bad))
		return 1
	}
	v.say("The record says every byte was written and read back as written.")
	key, kerr := hex.DecodeString(x.Key)
	iv, ierr := hex.DecodeString(x.IV)
	if kerr != nil || ierr != nil || len(key) != 32 || len(iv) != 16 {
		v.say("This record doesn't publish its keystream key, so the drive can't be checked against it.")
		return 1
	}

	f, err := os.Open(dev) // read-only, always
	if err != nil {
		fmt.Fprintf(v.errw, "Couldn't open %s read-only: %v\n", dev, err)
		return 2
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Fprintf(v.errw, "Couldn't tell how big %s is: %v\n", dev, err)
		return 2
	}
	if size != x.Size {
		v.say("Device: %s holds %s bytes, but the record is for %s.  This isn't the drive the record is for, or something has changed its size.", dev, records.Commas(size), records.Commas(x.Size))
		return 1
	}
	v.say("Device: %s holds %s bytes, the same as the record.", dev, records.Commas(size))

	var spans [][2]int64 // offset, length
	if !full && (stratified && size < 2*sections*block || !stratified && int64(samples)*mib >= size) {
		v.say("The samples would cover the whole drive, so checking every byte instead.")
		full = true
	}
	if full {
		v.say("Checking every byte.  That takes as long as reading the whole drive.")
		spans = [][2]int64{{0, size}}
	} else if stratified {
		spans = stratifiedSpans(size)
		v.say("Checking %s blocks of 4 KiB as IEEE 2883 §7.3 suggests: two at random in each of %s equal sections, plus the first and the last.  -full checks every byte.",
			records.Commas(int64(len(spans))), records.Commas(sections))
	} else {
		spans = sampleSpans(size, samples)
		v.say("Checking %d samples of up to 1 MiB, including the first and the last.", len(spans))
	}
	res, err := v.compare(f, key, iv, spans, full)
	if err != nil {
		fmt.Fprintf(v.errw, "%v\n", err)
		return 2
	}
	return v.report(res, &r, spans, full)
}

// stratifiedSpans is IEEE 2883 §7.3's second option: the drive cut into
// sections, two distinct random blocks from each, and the first and last
// blocks.  The caller makes sure every section holds at least two blocks.
func stratifiedSpans(size int64) [][2]int64 {
	blocks := size / block
	offs := []int64{0, size - block}
	for i := int64(0); i < sections; i++ {
		lo, hi := blocks*i/sections, blocks*(i+1)/sections
		a := lo + rand.Int64N(hi-lo)
		b := lo + rand.Int64N(hi-lo-1)
		if b >= a {
			b++
		}
		offs = append(offs, a*block, b*block)
	}
	slices.Sort(offs)
	offs = slices.Compact(offs)
	out := make([][2]int64, len(offs))
	for i, o := range offs {
		out[i] = [2]int64{o, block}
	}
	return out
}

// sampleSpans picks n spans of 1 MiB: the first, the last, and the rest at
// random 4 KiB-aligned offsets, sorted so the drive reads front to back.
func sampleSpans(size int64, n int) [][2]int64 {
	length := min(int64(mib), size)
	last := size - length
	offs := []int64{0}
	if last > 0 && n > 1 {
		offs = append(offs, last)
	}
	// A small drive has fewer distinct offsets than samples asked for, so
	// give up on duplicates after a while.
	for tries := 0; len(offs) < n && tries < 10*n; tries++ {
		o := rand.Int64N(last/4096+1) * 4096
		if !slices.Contains(offs, o) {
			offs = append(offs, o)
		}
	}
	slices.Sort(offs)
	out := make([][2]int64, len(offs))
	for i, o := range offs {
		out[i] = [2]int64{o, length}
	}
	return out
}

type result struct {
	checked    int64      // bytes compared
	bad        int64      // bytes that differ
	unreadable int64      // bytes that couldn't be read
	ranges     [][3]int64 // offset, length, bytes differing (-1: unreadable)
	badSpans   int        // spans with any damage
}

// compare reads each span and holds it against the keystream, a sector at
// a time, so damage is reported as sector ranges rather than as the odd
// byte of noise that happens to match.
func (v *verifier) compare(f *os.File, key, iv []byte, spans [][2]int64, progress bool) (*result, error) {
	const chunk = 8 * mib
	got, want := make([]byte, chunk), make([]byte, chunk)
	res := &result{}
	var total int64
	for _, s := range spans {
		total += s[1]
	}
	t0, tick := time.Now(), time.Now()
	for _, s := range spans {
		spanBad := false
		for off := s[0]; off < s[0]+s[1]; off += chunk {
			if progress && time.Since(tick) > 15*time.Second {
				tick = time.Now()
				done := res.checked + res.unreadable
				fmt.Fprintf(v.errw, "  %3.0f%%  %s of %s  %.0f MB/s\n", 100*float64(done)/float64(total),
					records.Size(done), records.Size(total), float64(done)/time.Since(t0).Seconds()/1e6)
			}
			n := min(chunk, s[0]+s[1]-off)
			if err := wipe.Keystream(key, iv, off, want[:n]); err != nil {
				return nil, err
			}
			if _, err := f.ReadAt(got[:n], off); err != nil {
				res.unreadable += n
				res.add(off, n, -1)
				spanBad = true
				continue
			}
			res.checked += n
			if bytes.Equal(got[:n], want[:n]) {
				continue
			}
			for p := int64(0); p < n; p += sector {
				q := min(p+sector, n)
				if bytes.Equal(got[p:q], want[p:q]) {
					continue
				}
				var d int64
				for k := p; k < q; k++ {
					if got[k] != want[k] {
						d++
					}
				}
				res.bad += d
				res.add(off+p, q-p, d)
				spanBad = true
			}
		}
		if spanBad {
			res.badSpans++
		}
	}
	return res, nil
}

// add records a damaged range, joining it to the last one if they touch.
func (r *result) add(off, n, diff int64) {
	if k := len(r.ranges); k > 0 {
		l := &r.ranges[k-1]
		if l[0]+l[1] == off && (l[2] < 0) == (diff < 0) {
			l[1] += n
			if diff > 0 {
				l[2] += diff
			}
			return
		}
	}
	r.ranges = append(r.ranges, [3]int64{off, n, diff})
}

func (v *verifier) report(res *result, r *records.Record, spans [][2]int64, full bool) int {
	ok := res.bad == 0 && res.unreadable == 0
	switch {
	case ok && full:
		v.say("Every byte matches the keystream: all %s of them.", records.Commas(res.checked))
		v.say("This drive holds exactly what the wipe wrote, and nothing else.")
	case ok:
		v.say("All %d samples match the keystream: %s bytes, spread from the first MiB to the last.", len(spans), records.Commas(res.checked))
		v.say("Everywhere we looked, this drive holds exactly what the wipe wrote.  Samples can't prove every byte; -full reads them all.")
	default:
		what := records.Commas(res.bad) + " bytes differ from the keystream"
		if res.unreadable > 0 {
			what += " and " + records.Commas(res.unreadable) + " couldn't be read"
		}
		if full {
			v.say("MISMATCH.  %s, out of %s.", what, records.Commas(res.checked+res.unreadable))
		} else {
			v.say("MISMATCH.  %d of %d samples don't match: %s.", res.badSpans, len(spans), what)
		}
		for i, g := range res.ranges {
			if i == maxRanges {
				v.say("  …and %d more ranges.", len(res.ranges)-maxRanges)
				break
			}
			if g[2] < 0 {
				v.say("  Bytes %s to %s (%s bytes) couldn't be read.", records.Commas(g[0]), records.Commas(g[0]+g[1]-1), records.Commas(g[1]))
			} else {
				v.say("  Bytes %s to %s (%s bytes) don't match; %s of them differ.", records.Commas(g[0]), records.Commas(g[0]+g[1]-1), records.Commas(g[1]), records.Commas(g[2]))
			}
		}
		v.say("Something has written to this drive since the wipe, or it isn't the drive this record is for.  A partition table or a format is enough to do it.")
	}
	v.say("zverify can't read the drive's serial number.  Check that the label on the drive says %s.", r.Serial)
	if ok {
		return 0
	}
	return 1
}

// load reads a file, or fetches an http(s) URL.
func load(src string) ([]byte, error) {
	if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
		return os.ReadFile(src)
	}
	resp, err := client.Get(src)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", src, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func an(s string) string {
	if s != "" && strings.ContainsRune("aeiou", rune(s[0])) {
		return "n"
	}
	return ""
}
