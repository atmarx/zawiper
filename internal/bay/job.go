package bay

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zawiper/internal/grade"
	"zawiper/internal/model"
	"zawiper/internal/records"
	"zawiper/internal/smart"
	"zawiper/internal/wipe"
)

// job is the one wipe a bay runs at a time.  Its live model.Job sits in the
// bay's snapshot; it and everything here are guarded by the bay's lock.
type job struct {
	dev    Device
	read   model.Snapshot // the read the confirm was drawn from
	block  int64
	cells  []byte
	pass   string // the pass and block in hand, for evidence
	i      int
	t0     time.Time // when the write pass began, for the ETA
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// ending is why a job stopped short: the evidence a person reads first, and
// whether it's a failure or an alarm.  It travels as the job context's cause.
type ending struct {
	state    model.State
	evidence string
}

func (e *ending) Error() string { return e.evidence }

func failed(f string, a ...any) *ending { return &ending{model.Failed, fmt.Sprintf(f, a...)} }
func alarm(f string, a ...any) *ending  { return &ending{model.Alarm, fmt.Sprintf(f, a...)} }

// held states wait for a person to acknowledge them.
func held(s model.State) bool {
	switch s {
	case model.Done, model.Failed, model.Interrupted, model.Alarm:
		return true
	}
	return false
}

const interruptedNote = "The job was running when bayd or its host went down.  I don't know what state this drive is in, and I won't pretend I do."

// Wipe starts a wipe of the drive in the bay, if it's the drive and the
// insertion the confirm was drawn from.  Every refusal says what to do.
func (b *Bay) Wipe(serial string, session time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if err := b.mayWipe(serial, session); err != nil {
		return err
	}
	j := &job{dev: b.dev, read: b.snap, block: cmp.Or(b.Block, wipe.Block), done: make(chan struct{})}
	j.cells = bytes.Repeat([]byte{model.CellTodo}, int((j.dev.Size+j.block-1)/j.block))
	live := &model.Job{Kind: "wipe", Phase: "smart-before", Serial: serial, Session: b.snap.Session,
		Started: time.Now().UTC(), Map: string(j.cells), Total: j.dev.Size, TempC: b.snap.Drive.SMART.TempC}
	if err := b.saveBefore(j); err != nil {
		return fmt.Errorf("Couldn't write the job's state (%v), so a power cut would leave no trace of the job.  Not starting.", err)
	}
	if err := b.saveState(model.Running, live); err != nil {
		return fmt.Errorf("Couldn't write the job's state file (%v), so a power cut would leave no trace of the job.  Not starting.", err)
	}
	base := b.base
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancelCause(base)
	j.cancel = cancel
	b.job = j
	s := b.snap
	s.State, s.Message, s.Job = model.Running, "Wiping.  Do not pull the drive.", live
	b.set(s)
	go b.run(ctx, j)
	return nil
}

// mayWipe is every reason not to start.  The caller holds b.mu.
func (b *Bay) mayWipe(serial string, session time.Time) error {
	s := b.snap
	switch {
	case s.State == model.Running:
		return errors.New("This bay is already running a job.  One at a time: wait for it to end.")
	case held(s.State):
		return fmt.Errorf("The last job ended %s and hasn't been acknowledged.  Acknowledge it, let the bay re-read the drive, and confirm again.", s.State)
	case s.State != model.Present || s.Drive == nil:
		return fmt.Errorf("The bay is %s, not holding a drive it has read.  Wait for the read to finish, then confirm again.", strings.ToLower(string(s.State)))
	case len(s.Refusals) > 0:
		return fmt.Errorf("The bay refused this drive: %s  Deal with that and re-read before wiping.", strings.Join(s.Refusals, "  "))
	case serial == "":
		return errors.New("The confirm didn't say which drive.  Confirm again from the bay's page.")
	case s.Drive.Serial() != serial:
		return fmt.Errorf("The bay holds %s, not %s.  Look at the drive again before confirming.", s.Drive.Serial(), serial)
	case !s.Session.Equal(session):
		return errors.New("That confirm was drawn from an earlier insertion of this drive.  Re-read it and confirm again.")
	case b.Spool == nil:
		return errors.New("This bay has no record spool, so a wipe would leave no record.  Not starting.")
	}
	devs, err := b.Src.Devices()
	if err != nil || len(devs) != 1 || devs[0] != b.dev {
		return errors.New("The bay's port doesn't show the drive the confirm was drawn from any more.  Re-read it and confirm again.")
	}
	why, err := b.Src.InUse(b.dev)
	if err != nil {
		return fmt.Errorf("Couldn't check whether the host is using %s (%v).  Not wiping it.", b.dev.Path, err)
	}
	if why != "" {
		return fmt.Errorf("The host is using %s now (%s).  Not wiping it.", b.dev.Path, why)
	}
	return nil
}

// run is the job, start to end: smart-before (the read's), write, verify,
// smart-after, record.
func (b *Bay) run(ctx context.Context, j *job) {
	defer close(j.done)
	defer j.cancel(nil)
	if err := b.Src.SetRW(j.dev); err != nil {
		b.finish(j, failed("Couldn't clear the read-only flag on %s, so nothing was written: %v", j.dev.Path, err), nil)
		return
	}
	w := &wipe.Job{Path: j.dev.Path, Size: j.dev.Size, Direct: b.Direct,
		Block: j.block, Buf: b.Buf, CheckEvery: b.CheckEvery,
		OnStart: func(pass string, i, n int) { b.blockStart(j, pass, i) },
		OnCell:  func(pass string, i, n int, c wipe.Cell) { b.blockDone(j, pass, i, c) },
		Check:   func(off int64) error { return b.checkIdentity(ctx, j, off) },
	}
	res, err := w.Run(ctx)
	if err := b.Src.Forget(j.dev); err != nil {
		log.Printf("job: forgetting the partition table: %v", err)
	}
	if err := b.Src.SetRO(j.dev); err != nil {
		log.Printf("job: setting read-only again: %v", err)
	}
	var end *ending
	if ctx.Err() != nil && !errors.As(context.Cause(ctx), &end) {
		// bayd is stopping.  The state file still says RUNNING, so the
		// next start reports INTERRUPTED, the same as a power cut.
		log.Printf("job: stopped by shutdown; it will come back INTERRUPTED")
		return
	}
	if end == nil && err != nil {
		end = failed("Couldn't open %s to write, so nothing was written: %v", j.dev.Path, err)
	}
	b.finish(j, end, res)
}

// finish reads SMART after a wipe that ran to the end, grades, files the
// record, and ends the job.  end is nil when nothing stopped it early.
func (b *Bay) finish(j *job, end *ending, res *wipe.Result) {
	serial := j.read.Drive.Serial()
	before := j.read.Drive.SMART
	raw := map[string][]byte{}
	keep(raw, "smart-before.json", j.read.SmartJSON)
	keep(raw, "smart-before.txt", j.read.SmartText)

	var after *smart.Info
	if end == nil {
		b.setPhase(j, "smart-after")
		var js, text []byte
		after, js, text = b.smartAfter(j)
		keep(raw, "smart.json", js)
		keep(raw, "smart.txt", text)
		if after != nil && after.Serial != serial {
			end = alarm("After the wipe the drive answers as serial %s, not %s.", after.Serial, serial)
			after = nil // not this drive's readings, so nothing to grade it on
		}
	}
	if end == nil && !res.OK() {
		end = failed("%s", damage(res))
	}

	b.setPhase(j, "record")
	health := grade.Wipe(before, after, res)
	if end != nil {
		health.Notes = append([]string{fmt.Sprintf("The job ended %s: %s", end.state, end.evidence)}, health.Notes...)
	}
	drive := *j.read.Drive
	if after != nil {
		drive.SMART = after
	}
	drive.Health, drive.ReadOnly = health, true
	rec := &records.Record{
		Serial: serial, Started: j.read.Session, Finished: time.Now().UTC(),
		Host: b.Host, Bay: b.Name, Software: b.Software,
		Drive: drive, Health: health, Wrote: res != nil && res.Written > 0, Wipe: res, Before: before,
	}
	if err := b.Spool.SaveWipe(rec, raw); err != nil {
		log.Printf("job: record: %v", err)
		rec.ID = ""
		if end == nil {
			end = failed("The wipe ran, but its record didn't save: %v", err)
		} else {
			end.evidence += fmt.Sprintf("  The record didn't save either: %v", err)
		}
	}
	b.end(j, end, rec.ID, &drive, res)
}

func keep(raw map[string][]byte, name string, b []byte) {
	if len(b) > 0 {
		raw[name] = b
	}
}

// smartAfter reads SMART once the wipe is done, from a context nothing can
// cancel: a finished wipe still gets its readings.
func (b *Bay) smartAfter(j *job) (*smart.Info, []byte, []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	js, text, err := b.Src.SMART(ctx, j.dev)
	if err != nil {
		log.Printf("job: SMART after the wipe: %v", err)
		return nil, js, text
	}
	info, err := smart.Parse(js)
	if err != nil {
		log.Printf("job: SMART after the wipe: %v", err)
	}
	return info, js, text
}

// damage is the evidence for a wipe that ran but didn't prove itself: the
// first bad block, and how many there were.
func damage(res *wipe.Result) string {
	n, first := 0, -1
	for i, c := range res.Cells {
		if c.Bad > 0 || c.Err != "" {
			n++
			if first < 0 {
				first = i
			}
		}
	}
	if first < 0 {
		return fmt.Sprintf("Only %s of %s bytes read back as written.", records.Commas(res.Checked), records.Commas(res.Size))
	}
	c := res.Cells[first]
	s := fmt.Sprintf("Block #%d, from byte %s: %s bytes didn't read back as written", first+1, records.Commas(int64(first)*res.Block), records.Commas(c.Bad))
	if c.Err != "" {
		s += ", and the drive said: " + c.Err
	}
	s += "."
	if n > 1 {
		s += fmt.Sprintf("  %d blocks were bad in all.", n)
	}
	return s
}

// end settles the job into DONE, FAILED or ALARM: state file first, then
// the snapshot.
func (b *Bay) end(j *job, e *ending, recID string, drive *model.Drive, res *wipe.Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	live := *b.snap.Job
	live.Ended, live.RecordID, live.Map = time.Now().UTC(), recID, string(finalMap(j.cells, res))
	state, msg := model.Done, fmt.Sprintf("Wiped and verified.  Grade: %s.", drive.Health.Grade)
	if e != nil {
		state, msg, live.Evidence = e.state, e.evidence, e.evidence
	}
	if err := b.saveState(state, &live); err != nil {
		log.Printf("job: state file: %v", err)
	}
	s := b.snap
	s.State, s.Message, s.Job, s.Drive = state, msg, &live, drive
	b.job = nil
	b.set(s)
}

// finalMap settles the live map: a block caught mid-pass is written but not
// verified, and slow blocks show once their neighbours are known.
func finalMap(cells []byte, res *wipe.Result) []byte {
	m := bytes.Clone(cells)
	for i, c := range m {
		if c == model.CellWriting || c == model.CellReading {
			m[i] = model.CellWritten
		}
	}
	if res == nil {
		return m
	}
	for _, i := range append(res.Slow(false), res.Slow(true)...) {
		if i < len(m) && m[i] != model.CellBad {
			m[i] = model.CellSlow
		}
	}
	return m
}

func (b *Bay) setPhase(j *job, phase string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.snap.Job.Phase == phase {
		return
	}
	b.snap.Job.Phase = phase
	if err := b.saveState(model.Running, b.snap.Job); err != nil {
		log.Printf("job: state file: %v", err)
	}
}

func (b *Bay) blockStart(j *job, pass string, i int) {
	b.setPhase(j, pass)
	b.mu.Lock()
	defer b.mu.Unlock()
	j.pass, j.i = pass, i
	if pass == "write" && i == 0 {
		j.t0 = time.Now()
	}
	j.cells[i] = model.CellWriting
	if pass == "verify" {
		j.cells[i] = model.CellReading
	}
	b.snap.Job.Map = string(j.cells)
}

func (b *Bay) blockDone(j *job, pass string, i int, c wipe.Cell) {
	b.mu.Lock()
	live, size := b.snap.Job, j.dev.Size
	from, to := int64(i)*j.block, min(int64(i+1)*j.block, size)
	secs := c.Write
	j.cells[i], live.Done = model.CellWritten, to
	if pass == "verify" {
		secs = c.Read
		j.cells[i], live.Done = model.CellVerified, size+to
		live.Bad += c.Bad
	}
	if c.Bad > 0 || c.Err != "" {
		j.cells[i] = model.CellBad
	}
	if secs > 0 {
		live.MBps = float64(to-from) / secs / 1e6
	}
	if el := time.Since(j.t0).Seconds(); el > 0 && live.Done > 0 {
		live.ETA = int64(float64(2*size-live.Done) / (float64(live.Done) / el))
	}
	live.Map = string(j.cells)
	if err := b.saveState(model.Running, live); err != nil {
		log.Printf("job: state file: %v", err)
	}
	hook := b.hook
	b.mu.Unlock()
	if hook != nil {
		hook(pass, i)
	}
}

// where says where the job is, for evidence: at a byte when the engine
// knows it, or in a block when the poll loop noticed.
func (b *Bay) where(j *job, off int64) string {
	b.mu.Lock()
	pass, i := j.pass, j.i
	b.mu.Unlock()
	switch {
	case pass == "":
		return "before the write pass"
	case off >= 0:
		return fmt.Sprintf("during the %s pass, at byte %s", pass, records.Commas(off))
	}
	return fmt.Sprintf("during the %s pass, in block #%d (from byte %s)", pass, i+1, records.Commas(int64(i)*j.block))
}

// checkIdentity is the engine's Check: before each pass and every 100 GiB,
// re-read the serial, WWN and size through Src (spec §4.1).  A drive that
// has left is the poll loop's FAILED, not an alarm.
func (b *Bay) checkIdentity(ctx context.Context, j *job, off int64) error {
	where := b.where(j, off)
	raise := func(e *ending) error { j.cancel(e); return e }
	devs, err := b.Src.Devices()
	if err != nil {
		return raise(alarm("Couldn't look at the bay's port to check the drive %s (%v).  I won't write to a drive I can't name.", where, err))
	}
	d := find(devs, j.dev.Name)
	if d == nil {
		j.cancel(failed("The drive left the bay %s.", where))
		return nil // the cancel stops the run before its next write
	}
	if d.Size != j.dev.Size {
		return raise(alarm("The bay now reports %s for the drive, not %s, %s.", records.Commas(d.Size), records.Commas(j.dev.Size), where))
	}
	js, _, err := b.Src.SMART(ctx, *d)
	info, perr := smart.Parse(js)
	if err = cmp.Or(err, perr); err != nil {
		return raise(alarm("Couldn't re-read the drive's identity %s (%v).  I won't write to a drive I can't name.", where, err))
	}
	want := j.read.Drive.SMART
	switch {
	case info.Serial != want.Serial:
		return raise(alarm("The drive now answers as serial %s, not %s, %s.", info.Serial, want.Serial, where))
	case want.WWN != "" && info.WWN != want.WWN:
		return raise(alarm("The drive's WWN is now %s, not %s, %s.", info.WWN, want.WWN, where))
	case info.CapacityBytes != want.CapacityBytes:
		return raise(alarm("The drive now says it holds %s bytes, not %s, %s.", records.Commas(info.CapacityBytes), records.Commas(want.CapacityBytes), where))
	}
	b.mu.Lock()
	b.snap.Job.TempC = info.TempC
	b.mu.Unlock()
	return nil
}

// watch is the poll loop during a job: no reads and no SetRO, only a look
// at the port.  Anything wrong cancels the job with its reason.
func (b *Bay) watch(j *job, devs []Device) {
	d := find(devs, j.dev.Name)
	switch {
	case d == nil:
		j.cancel(failed("The drive left the bay %s.", b.where(j, -1)))
	case d.Size != j.dev.Size:
		j.cancel(alarm("The bay now reports %s bytes for the drive, not %s, %s.", records.Commas(d.Size), records.Commas(j.dev.Size), b.where(j, -1)))
	case len(devs) > 1:
		j.cancel(alarm("Another drive appeared on the bay's port %s.  With two there I can't be sure which one I'm writing, so I stopped.", b.where(j, -1)))
	}
}

// Wait blocks until no job is running.  Once the bay's context is
// cancelled, that's as soon as the wipe lets go of the drive.
func (b *Bay) Wait() {
	b.mu.Lock()
	j := b.job
	b.mu.Unlock()
	if j != nil {
		<-j.done
	}
}

// Ack clears DONE, FAILED, ALARM or INTERRUPTED, and sends the bay back to
// a fresh read with a fresh session.  An interrupted job gets its record
// first.
func (b *Bay) Ack() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	switch s := b.snap.State; {
	case s == model.Running:
		return errors.New("The job is still running.  There's nothing to acknowledge until it ends.")
	case !held(s):
		return errors.New("There's nothing to acknowledge.")
	}
	if b.snap.State == model.Interrupted {
		if err := b.saveInterrupted(); err != nil {
			return fmt.Errorf("Couldn't file the record of the interrupted job (%v).  Nothing was cleared; acknowledge again once that's fixed.", err)
		}
	}
	if err := b.clearState(); err != nil {
		return fmt.Errorf("Couldn't clear the job's state file (%v).  Acknowledge again once that's fixed.", err)
	}
	b.sig = "\x00acked" // no port looks like this, so the next poll is a fresh read with a fresh session
	b.set(model.Snapshot{State: model.Reading, Message: "Reading the drive again after the job."})
	b.nudge()
	return nil
}

// Eject flushes the drive and lets it go.  From DONE it clears the job too;
// the other endings want acknowledging first, so nobody misses them.
func (b *Bay) Eject() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	state := b.snap.State
	switch state {
	case model.Running:
		return errors.New("A job is running, and nothing stops a run from here.  Wait for it to end.")
	case model.Reading:
		return errors.New("The bay is reading the drive.  Wait for the read to finish, then eject.")
	case model.Failed, model.Alarm, model.Interrupted:
		return fmt.Errorf("The job ended %s.  Acknowledge it first, then eject.", state)
	}
	devs, err := b.Src.Devices()
	switch {
	case err != nil:
		return fmt.Errorf("Can't see the bay's port (%v), so there's nothing I can safely eject.", err)
	case len(devs) == 0:
		return errors.New("There's nothing in the bay to eject.")
	case len(devs) > 1:
		return fmt.Errorf("There are %d drives on the bay's port.  I won't guess which one to eject; take them out by hand.", len(devs))
	}
	d := devs[0]
	why, err := b.Src.InUse(d)
	if err != nil || why != "" {
		return fmt.Errorf("The host is using %s (%s).  Not ejecting it.", d.Path, cmp.Or(why, fmt.Sprint(err)))
	}
	if err := b.Src.Eject(d); err != nil {
		return fmt.Errorf("Couldn't eject %s: %v.  It's still attached.", d.Path, err)
	}
	if state == model.Done {
		if err := b.clearState(); err != nil {
			log.Printf("eject: state file: %v", err)
		}
	}
	b.sig = ""
	b.set(model.Snapshot{State: model.Empty, Message: "Ejected.  It's safe to pull the drive."})
	return nil
}

// The state file, and the read the job started from beside it.

func (b *Bay) saveState(s model.State, j *model.Job) error {
	if b.StatePath == "" {
		return nil
	}
	js, err := json.MarshalIndent(model.JobFile{State: s, Job: *j}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(b.StatePath, append(js, '\n'))
}

// before is what the state file can't hold: the read the job started from,
// so an interrupted job's record can still say what the drive was.
type before struct {
	Drive     model.Drive `json:"drive"`
	Block     int64       `json:"block"`
	SmartJSON []byte      `json:"smart_json"`
	SmartText []byte      `json:"smart_text"`
}

func (b *Bay) beforePath() string {
	return strings.TrimSuffix(b.StatePath, ".json") + "-before.json"
}

func (b *Bay) saveBefore(j *job) error {
	if b.StatePath == "" {
		return nil
	}
	js, err := json.Marshal(before{Drive: *j.read.Drive, Block: j.block, SmartJSON: j.read.SmartJSON, SmartText: j.read.SmartText})
	if err != nil {
		return err
	}
	return writeAtomic(b.beforePath(), js)
}

func (b *Bay) loadBefore() (*before, error) {
	js, err := os.ReadFile(b.beforePath())
	if err != nil {
		return nil, err
	}
	var bf before
	return &bf, json.Unmarshal(js, &bf)
}

func (b *Bay) clearState() error {
	if b.StatePath == "" {
		return nil
	}
	for _, p := range []string{b.StatePath, b.beforePath()} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// restore picks up the state file a previous bayd left.  A job it says was
// RUNNING had nobody running it: that's INTERRUPTED.  Endings hold as they
// were, until someone acknowledges them.
func (b *Bay) restore() {
	if b.StatePath == "" {
		return
	}
	js, err := os.ReadFile(b.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var jf model.JobFile
	if err == nil {
		err = json.Unmarshal(js, &jf)
	}
	if err != nil {
		jf = model.JobFile{State: model.Interrupted, Job: model.Job{Kind: "wipe",
			Evidence: fmt.Sprintf("The job's state file couldn't be read (%v), so I can't say what was running.", err)}}
	}
	switch jf.State {
	case model.Running:
		jf.State = model.Interrupted
		jf.Job.Evidence = interruptedNote
	case model.Done, model.Failed, model.Alarm, model.Interrupted:
	default:
		log.Printf("state: %s says %q, which isn't a job; ignoring it", b.StatePath, jf.State)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if err := b.saveState(jf.State, &jf.Job); err != nil {
		log.Printf("state: %v", err)
	}
	s := model.Snapshot{State: jf.State, Session: jf.Job.Session, Job: &jf.Job, Message: cmp.Or(jf.Job.Evidence, "The last job ended "+string(jf.State)+".")}
	if bf, err := b.loadBefore(); err == nil {
		s.Drive = &bf.Drive
	}
	b.set(s)
}

// saveInterrupted files what's known of a job nobody saw end.  The caller
// holds b.mu.
func (b *Bay) saveInterrupted() error {
	live := b.snap.Job
	if live == nil || live.Serial == "" {
		log.Printf("ack: the interrupted job named no drive, so there's no record to file")
		return nil
	}
	if b.Spool == nil {
		return errors.New("this bay has no record spool")
	}
	rec := &records.Record{
		Serial: live.Serial, Started: live.Session, Finished: time.Now().UTC(),
		Host: b.Host, Bay: b.Name, Software: b.Software,
		Drive: model.Drive{Size: live.Total},
	}
	raw := map[string][]byte{}
	block := int64(0)
	if bf, err := b.loadBefore(); err == nil {
		rec.Drive, rec.Before, block = bf.Drive, bf.Drive.SMART, bf.Block
		keep(raw, "smart-before.json", bf.SmartJSON)
		keep(raw, "smart-before.txt", bf.SmartText)
	} else {
		log.Printf("ack: no record of the read the job started from: %v", err)
	}
	rec.Wipe = partial(live, block)
	rec.Health = grade.Wipe(rec.Before, nil, rec.Wipe)
	rec.Health.Notes = append([]string{interruptedNote}, rec.Health.Notes...)
	rec.Drive.Health = rec.Health
	return b.Spool.SaveInterrupted(rec, raw)
}

// partial rebuilds what the state file knew of a wipe: how far it got, and
// which blocks were bad.  The key died with the process.
func partial(j *model.Job, block int64) *wipe.Result {
	if block == 0 && len(j.Map) > 0 {
		block = (j.Total + int64(len(j.Map)) - 1) / int64(len(j.Map))
	}
	r := &wipe.Result{Method: wipe.Method, Size: j.Total, Block: block, Started: j.Started, Bad: j.Bad,
		Written: min(j.Done, j.Total), Checked: max(0, j.Done-j.Total-j.Bad),
		Aborted: fmt.Sprintf("stopped when bayd or its host went down, during the %s phase", cmp.Or(j.Phase, "first"))}
	for _, c := range []byte(j.Map) {
		var cell wipe.Cell
		if c == model.CellBad {
			cell.Err = "bad before the interruption"
		}
		r.Cells = append(r.Cells, cell)
	}
	return r
}

func writeAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
