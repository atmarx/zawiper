package web

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"zawiper/internal/hub"
	"zawiper/internal/model"
	"zawiper/internal/proto"
	"zawiper/internal/smart"
)

// DemoFleet is a pretend hub: four bays on two hosts, one of them mid-wipe
// with its map advancing.  `zaweb -demo` serves it, so the page can be
// designed and screenshot without a drive anywhere near it.  It touches
// nothing.  It answers the verbs the way bayd would, loosely: a wipe starts
// from the top, an ack goes back to the drive, an eject empties the bay, and
// an empty bay gets its drive back a little later.
type DemoFleet struct {
	mu   sync.Mutex
	bays map[string]*hub.Bay
	subs map[chan struct{}]struct{}
	// drives remembers what each bay had in it, for when it comes back.
	drives map[string]*model.Drive
	since  map[string]time.Time // when each bay entered its state
	stop   chan struct{}
}

const gib = 1 << 30

func NewDemoFleet() *DemoFleet {
	now := time.Now()
	f := &DemoFleet{
		bays:   map[string]*hub.Bay{},
		subs:   map[chan struct{}]struct{}{},
		drives: map[string]*model.Drive{},
		since:  map[string]time.Time{},
		stop:   make(chan struct{}),
	}
	caps := []string{"wipe"}

	seagate := demoDrive("ST31000528AS", "Seagate Barracuda 7200.12", "9VPAX9NA", 1000204886016, 7200, 11196, 41,
		smart.Health{Grade: smart.Notes, Notes: []string{"Airflow_Temperature_Cel failed in the past (it ran hot once)."}},
		model.Table{Type: "gpt", Parts: []model.Part{
			{Path: "/dev/zawiper/bay1-part1", Size: 600e9, FSType: "ext4", Label: "data", Used: 312e9},
			{Path: "/dev/zawiper/bay1-part2", Size: 400e9, FSType: "ntfs", Label: "Windows", Used: 88e9},
		}})
	wd := demoDrive("WDC WD20EFRX-68EUZN0", "Western Digital Red", "WD-WCC4M1KXJ5R7", 2000398934016, 5400, 31877, 34,
		smart.Health{Grade: smart.Pass},
		model.Table{Type: "gpt", Parts: []model.Part{
			{Path: "/dev/zawiper/bay2-part1", Size: 2000397868544, FSType: "ext4", Label: "backup-gamma", Used: 1312e9, Backup: `labelled "backup-gamma"`},
		}})
	toshiba := demoDrive("TOSHIBA DT01ACA050", "Toshiba DT01ACA", "Z3K8P2QAS", 500107862016, 7200, 20412, 38,
		smart.Health{Grade: smart.Pass}, model.Table{})

	running := &hub.Bay{Key: "lab-a/bay1", Host: "lab-a", Name: "bay1", Caps: caps, Version: "bayd demo", Online: true, Seen: now}
	running.Snap = model.Snapshot{State: model.Running, Bay: "bay1", Host: "lab-a", Session: now.Add(-3 * time.Hour), ReadAt: now.Add(-3 * time.Hour), Drive: seagate}
	running.Snap.Job = newDemoJob(seagate, running.Snap.Session, now.Add(-2*time.Hour))
	// Start partway into the write pass, so there's something to look at.
	advance(running.Snap.Job, float64(len(running.Snap.Job.Map))*0.55)

	present := &hub.Bay{Key: "lab-a/bay2", Host: "lab-a", Name: "bay2", Caps: caps, Version: "bayd demo", Online: true, Seen: now}
	present.Snap = model.Snapshot{State: model.Present, Bay: "bay2", Host: "lab-a", Session: now.Add(-4 * time.Minute), ReadAt: now.Add(-4 * time.Minute), Drive: wd, RecordID: "demo-inspect"}

	done := &hub.Bay{Key: "lab-b/dock", Host: "lab-b", Name: "dock", Caps: caps, Version: "bayd demo", Online: true, Seen: now}
	job := newDemoJob(toshiba, now.Add(-90*time.Minute), now.Add(-80*time.Minute))
	advance(job, float64(2*len(job.Map)+1))
	finish(job, now.Add(-12*time.Minute))
	done.Snap = model.Snapshot{State: model.Done, Bay: "dock", Host: "lab-b", Session: job.Session, ReadAt: job.Session, Drive: toshiba, Job: job, RecordID: job.RecordID}

	offline := &hub.Bay{Key: "lab-b/sas0", Host: "lab-b", Name: "sas0", Caps: []string{"wipe", "sas"}, Version: "bayd demo", Online: false, Seen: now.Add(-3*time.Hour - 12*time.Minute)}
	offline.Snap = model.Snapshot{State: model.Offline, Bay: "sas0", Host: "lab-b"}

	for _, b := range []*hub.Bay{running, present, done, offline} {
		f.bays[b.Key] = b
		f.drives[b.Key] = b.Snap.Drive
		f.since[b.Key] = now
	}
	go f.run()
	return f
}

func demoDrive(mdl, family, serial string, size int64, rpm, hours, temp int, h smart.Health, t model.Table) *model.Drive {
	passed := true
	return &model.Drive{
		Dev: "/dev/zawiper/demo", Size: size, LogicalSector: 512, PhysicalSector: 4096,
		Bridge: model.Bridge{VendorID: "174c", ProductID: "55aa", Manufacturer: "ASMedia", Product: "ASM1153E", Speed: 5000},
		SMART: &smart.Info{Model: mdl, Family: family, Serial: serial, CapacityBytes: size, RotationRate: rpm,
			Link: "6.0 Gb/s", LinkBps: 6_000_000_000,
			Passed: &passed, PowerOnHours: hours, PowerCycles: hours / 37, TempC: temp},
		Health: h, Table: t,
	}
}

func newDemoJob(d *model.Drive, session, started time.Time) *model.Job {
	blocks := int((d.Size + gib - 1) / gib)
	return &model.Job{
		Kind: "wipe", Phase: "write", Serial: d.Serial(), Session: session, Started: started,
		Map: strings.Repeat(string(rune(model.CellTodo)), blocks), Total: d.Size, MBps: 148, TempC: 40,
	}
}

// advance moves a job on by n blocks: the write pass, then the read-back.
func advance(j *model.Job, n float64) {
	j.Done = min(2*j.Total, j.Done+int64(n*gib))
	blocks := len(j.Map)
	if len(j.WriteMBps) != blocks {
		j.WriteMBps, j.ReadMBps = make([]uint16, blocks), make([]uint16, blocks)
	}
	// A spinning drive slows toward the inner tracks; so does the demo,
	// with a little wobble, and the read-back runs a touch faster.
	speed := func(i int, read bool) uint16 {
		pos := float64(i) / float64(blocks)
		v := 172 - 80*pos + 6*math.Sin(float64(i)*0.7) + 3*math.Sin(float64(i)*2.3)
		if read {
			v += 9
		}
		return uint16(v)
	}
	m := make([]byte, blocks)
	wrote, read := min(j.Done, j.Total), max(0, j.Done-j.Total)
	for i := range m {
		from, to := int64(i)*gib, min(int64(i+1)*gib, j.Total)
		switch {
		case read >= to:
			m[i] = model.CellVerified
			j.WriteMBps[i], j.ReadMBps[i] = speed(i, false), speed(i, true)
		case wrote >= to && wrote == j.Total && read >= from:
			m[i] = model.CellReading
			j.WriteMBps[i] = speed(i, false)
		case wrote >= to:
			m[i] = model.CellWritten
			j.WriteMBps[i] = speed(i, false)
		case wrote >= from:
			m[i] = model.CellWriting
		default:
			m[i] = model.CellTodo
		}
	}
	j.Map = string(m)
	switch {
	case j.Done >= 2*j.Total:
		j.Phase = "smart-after"
	case j.Done >= j.Total:
		j.Phase = "verify"
	default:
		j.Phase = "write"
	}
	cur := strings.IndexAny(j.Map, "WR")
	if cur < 0 {
		cur = blocks - 1
	}
	j.MBps = float64(speed(cur, j.Phase == "verify"))
	j.TempC = 40 + int(4*float64(j.Done)/float64(2*j.Total))
	// About what one bayd costs on a desktop CPU: a fifth of a core for AES-CTR.
	j.CPU, j.HostCPU, j.Cores = 19+2*math.Sin(float64(cur)), 6+math.Sin(float64(cur)*0.3), 8
	j.Load = 0.9 + 0.2*math.Sin(float64(cur)*0.1)
	if j.MBps > 0 {
		j.ETA = int64(float64(2*j.Total-j.Done) / (j.MBps * 1e6))
	}
}

// finish ends a job the way a clean wipe ends, with a few slow blocks noted.
func finish(j *model.Job, ended time.Time) {
	m := []byte(j.Map)
	for _, i := range []int{7, 8, len(m) / 2, len(m) - 3} {
		if i >= 0 && i < len(m) {
			m[i] = model.CellSlow
		}
	}
	j.Map, j.Phase, j.Ended, j.ETA = string(m), "record", ended, 0
	j.Done = 2 * j.Total
	j.RecordID = ended.UTC().Format("2006-01-02T15-04-05Z") + "-wipe"
}

func (f *DemoFleet) run() {
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			return
		case now := <-t.C:
			f.mu.Lock()
			changed := false
			for key, b := range f.bays {
				if f.step(key, b, now) {
					changed = true
				}
			}
			f.mu.Unlock()
			if changed {
				f.tick()
			}
		}
	}
}

// step moves one bay along.  It reports whether anything changed.
func (f *DemoFleet) step(key string, b *hub.Bay, now time.Time) bool {
	s := &b.Snap
	if b.Online {
		b.Seen = now
	}
	switch s.State {
	case model.Running:
		j := s.Job
		advance(j, 0.35)
		if j.Phase == "smart-after" {
			finish(j, now)
			s.State, s.RecordID = model.Done, j.RecordID
			f.since[key] = now
		}
		return true
	case model.Empty:
		if now.Sub(f.since[key]) > 12*time.Second && f.drives[key] != nil {
			s.State, s.Message = model.Reading, "smartctl -x -d sat /dev/zawiper/"+b.Name
			f.since[key] = now
			return true
		}
	case model.Reading:
		if now.Sub(f.since[key]) > 3*time.Second {
			s.State, s.Message, s.Drive = model.Present, "", f.drives[key]
			s.Session, s.ReadAt = now, now
			f.since[key] = now
			return true
		}
	}
	return false
}

func (f *DemoFleet) Close() { close(f.stop) }

func (f *DemoFleet) tick() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (f *DemoFleet) Bays() []hub.Bay {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []hub.Bay
	for _, b := range f.bays {
		out = append(out, copyBay(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (f *DemoFleet) Bay(key string) (hub.Bay, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.bays[key]
	if !ok {
		return hub.Bay{}, false
	}
	return copyBay(b), true
}

// copyBay copies the job, the one part step changes in place.
func copyBay(b *hub.Bay) hub.Bay {
	c := *b
	if b.Snap.Job != nil {
		j := *b.Snap.Job
		c.Snap.Job = &j
	}
	return c
}

func (f *DemoFleet) Call(_ context.Context, key string, req proto.Request) (*model.Snapshot, error) {
	f.mu.Lock()
	b, ok := f.bays[key]
	if !ok {
		f.mu.Unlock()
		return nil, errors.New("no such bay")
	}
	if !b.Online {
		f.mu.Unlock()
		return nil, errors.New("this bay's link is down")
	}
	s := &b.Snap
	now := time.Now()
	var err error
	switch req.Verb {
	case proto.Status:
	case proto.Reread:
		if s.State == model.Present {
			s.ReadAt = now
		}
	case proto.Wipe:
		switch {
		case s.State != model.Present || s.Drive == nil:
			err = errors.New("there's no drive ready to wipe")
		case req.Serial != s.Drive.Serial() || !req.Session.Equal(s.Session):
			err = errors.New("that isn't the drive in the bay now; look again")
		default:
			s.State, s.Job = model.Running, newDemoJob(s.Drive, s.Session, now)
			s.Job.Phase = "smart-before"
		}
	case proto.Ack:
		switch s.State {
		case model.Done, model.Failed, model.Interrupted, model.Alarm:
			s.State, s.Job = model.Present, nil
		default:
			err = errors.New("there's nothing to acknowledge")
		}
	case proto.Eject:
		switch s.State {
		case model.Present, model.Done, model.Failed, model.Interrupted:
			s.State, s.Job, s.Drive, s.RecordID = model.Empty, nil, nil, ""
		default:
			err = errors.New("not now")
		}
	default:
		err = errors.New("unknown verb")
	}
	f.since[key] = now
	snap := copyBay(b).Snap
	f.mu.Unlock()
	f.tick()
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

func (f *DemoFleet) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	return ch, func() {
		f.mu.Lock()
		delete(f.subs, ch)
		f.mu.Unlock()
	}
}

var _ hub.Fleet = (*DemoFleet)(nil)
