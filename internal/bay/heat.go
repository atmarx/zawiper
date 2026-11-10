package bay

import (
	"context"
	"log"
	"time"

	"github.com/atmarx/zawiper/internal/records"
	"github.com/atmarx/zawiper/internal/smart"
)

const (
	heatEvery = time.Minute // how often a working job reads the temperature
	coolEvery = time.Minute // and a waiting one
	// heatBackoff is the longest it goes between reads while they're failing:
	// a dock has reset under SMART traffic before (spec §6.6).
	heatBackoff = 10 * time.Minute
	// coolMargin is how far under the ceiling a drive cools before it works
	// again, so a job doesn't stutter at the line.
	coolMargin = 5
	// heatBlind is how long a pause waits on a temperature it can't read.  A
	// job stuck forever on a broken sensor helps nobody, so it carries on and
	// the record says so.
	heatBlind = 10 * time.Minute
)

// heat is a job's temperature.  Only the job's goroutine touches it, until the
// job is over.
type heat struct {
	ceiling int
	why     string
	next    time.Time
	every   time.Duration
	peak    int
	pauses  int
	waited  time.Duration
	curve   []int
	lost    bool
}

func (b *Bay) newHeat(j *job) *heat {
	var info *smart.Info
	if j.read.Drive != nil {
		info = j.read.Drive.SMART
	}
	c, why := smart.Ceiling(info)
	if b.TempCeiling > 0 && b.TempCeiling < c {
		c, why = b.TempCeiling, "this bay's temp_ceiling"
	}
	every := b.HeatEvery
	if every <= 0 {
		every = heatEvery
	}
	return &heat{ceiling: c, why: why, every: every, next: time.Now().Add(every)}
}

func (h *heat) record() *records.Heat {
	return &records.Heat{CeilingC: h.ceiling, Why: h.why, PeakC: h.peak, Pauses: h.pauses,
		WaitedS: int64(h.waited.Seconds()), Curve: h.curve, Lost: h.lost}
}

// coolOff looks at the temperature when one is due, and if the drive is over
// its ceiling, holds the job here until it's cooled.  It returns how long it
// held.  A pause is only ever a wait: nothing on the page starts or stops one.
func (b *Bay) coolOff(ctx context.Context, j *job) time.Duration {
	h := j.heat
	if h == nil || time.Now().Before(h.next) {
		return 0
	}
	t, ok := b.temperature(ctx, j)
	if !ok || t <= h.ceiling {
		return 0
	}
	start := time.Now()
	resume := h.ceiling - coolMargin
	h.pauses++
	log.Printf("job: the drive is at %d °C, over its %d °C ceiling (%s); waiting for %d °C", t, h.ceiling, h.why, resume)
	b.setPaused(j, true, resume, 0)
	wait := b.CoolEvery
	if wait <= 0 {
		wait = coolEvery
	}
	var blind time.Time
	for t > resume {
		select {
		case <-ctx.Done():
			t = resume // the job is ending; the job says why
			continue
		case <-time.After(wait):
		}
		got, ok := b.temperature(ctx, j)
		switch {
		case ok:
			t, blind = got, time.Time{}
		case blind.IsZero():
			blind = time.Now()
		case time.Since(blind) > heatBlind:
			log.Printf("job: the temperature hasn't read for %s; carrying on", heatBlind)
			h.lost = true
			t = resume
		}
	}
	d := time.Since(start)
	h.waited += d
	b.setPaused(j, false, 0, h.waited)
	log.Printf("job: carrying on after %s", records.Took(d))
	return d
}

// temperature reads SMART, and keeps the reading.  A failed read backs off,
// since the reads themselves may be what's upsetting the dock.
func (b *Bay) temperature(ctx context.Context, j *job) (int, bool) {
	h := j.heat
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	t, err := b.readTemp(rctx, j)
	base := b.HeatEvery
	if base <= 0 {
		base = heatEvery
	}
	if err != nil || t <= 0 {
		h.every = min(h.every*2, heatBackoff)
		h.next = time.Now().Add(h.every)
		return 0, false
	}
	h.every = base
	h.next = time.Now().Add(h.every)
	h.curve = append(h.curve, t)
	h.peak = max(h.peak, t)
	b.mu.Lock()
	if live := b.snap.Job; live != nil {
		live.TempC, live.PeakC = t, h.peak
	}
	b.mu.Unlock()
	return t, true
}

// thermometerSource is a Source that can read just the temperature, which is far
// less traffic through a bridge than a full SMART read.  Optional, so fakes
// needn't.
type thermometerSource interface {
	Temperature(ctx context.Context, d Device) (int, error)
}

func (b *Bay) readTemp(ctx context.Context, j *job) (int, error) {
	if s, ok := b.Src.(thermometerSource); ok {
		return s.Temperature(ctx, j.dev)
	}
	js, _, err := b.Src.SMART(ctx, j.dev)
	if err != nil {
		return 0, err
	}
	info, err := smart.Parse(js)
	if err != nil {
		return 0, err
	}
	return info.TempC, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (b *Bay) setPaused(j *job, paused bool, resume int, waited time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	live := b.snap.Job
	if live == nil {
		return
	}
	live.Paused, live.ResumeC = paused, resume
	if waited > 0 {
		live.WaitedS = int64(waited.Seconds())
	}
	if err := b.saveState(b.snap.State, live); err != nil {
		log.Printf("job: state file: %v", err)
	}
}
