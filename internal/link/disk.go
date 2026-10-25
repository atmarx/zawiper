package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"zawiper/internal/model"
	"zawiper/internal/proto"
)

func (l *Link) disk() *proto.Client { return &proto.Client{Socket: l.Socket} }

// absent says the disk side isn't running: no socket, or a socket nobody is
// listening on.  The disk unit only runs while a drive is in the bay.
// Anything else (a timeout, a garbled reply) means it's there but unwell.
func absent(err error) bool {
	return errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

// snapshot is the bay right now: the disk side's word when it's running,
// and the job state file's when it isn't.
func (l *Link) snapshot(ctx context.Context) (*model.Snapshot, error) {
	snap, err := l.disk().Call(ctx, proto.Status)
	if err == nil {
		return snap, nil
	}
	if absent(err) {
		s := l.idle()
		return &s, nil
	}
	return nil, err
}

// idle is the bay with no disk side running.  A RUNNING job with nobody
// running it is INTERRUPTED; a held ending stays what it was until someone
// acknowledges it; anything else is an empty bay.
func (l *Link) idle() model.Snapshot {
	s := model.Snapshot{State: model.Empty, Bay: l.Bay, Host: l.Host}
	jf, err := l.jobFile()
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s
	case err != nil:
		// A state file we can't read might have been a running job.
		// Don't guess it away: hold it as INTERRUPTED until someone looks.
		s.State = model.Interrupted
		s.Message = fmt.Sprintf("The job state file can't be read, so this bay can't say how its last job ended: %v", err)
		return s
	}
	switch jf.State {
	case model.Running, model.Interrupted:
		s.State = model.Interrupted
	case model.Done, model.Failed, model.Alarm:
		s.State = jf.State
	default:
		return s
	}
	job := jf.Job
	s.Job, s.Session, s.RecordID = &job, job.Session, job.RecordID
	return s
}

func (l *Link) jobFile() (*model.JobFile, error) {
	b, err := os.ReadFile(l.State)
	if err != nil {
		return nil, err
	}
	var jf model.JobFile
	if err := json.Unmarshal(b, &jf); err != nil {
		return nil, err
	}
	return &jf, nil
}

// command passes a hub command to the disk side.  When the disk side isn't
// running, the link answers status itself, acknowledges an interrupted job
// by archiving its state file, and refuses the rest.
func (l *Link) command(ctx context.Context, req proto.Request) proto.Reply {
	switch req.Verb {
	case proto.Status, proto.Reread, proto.Wipe, proto.Ack, proto.Eject:
	default:
		return proto.Reply{Error: fmt.Sprintf("bay %s doesn't take the command %q", l.Bay, req.Verb)}
	}
	snap, err := l.disk().Do(ctx, req)
	if err == nil {
		return proto.Reply{OK: true, Snapshot: snap}
	}
	if !absent(err) {
		return proto.Reply{Error: err.Error()}
	}
	switch req.Verb {
	case proto.Status:
	case proto.Ack:
		if err := l.archive(); err != nil {
			return l.refuse(err.Error())
		}
	default:
		return l.refuse(fmt.Sprintf("bay %s has no drive in it (its disk side isn't running), so there's nothing to %s", l.Bay, req.Verb))
	}
	s := l.idle()
	return proto.Reply{OK: true, Snapshot: &s}
}

func (l *Link) refuse(msg string) proto.Reply {
	s := l.idle()
	return proto.Reply{Error: msg, Snapshot: &s}
}

// archive acknowledges an interrupted job: the state file moves aside to
// job.acked-<time>.json, and the bay reads empty.  The held endings (DONE,
// FAILED, ALARM) are the disk side's to acknowledge, with the drive back in.
func (l *Link) archive() error {
	s := l.idle()
	if s.State == model.Empty {
		return fmt.Errorf("bay %s has nothing to acknowledge", l.Bay)
	}
	if s.State != model.Interrupted {
		return fmt.Errorf("bay %s holds a %s job, and only its disk side can acknowledge that; put the drive back in", l.Bay, s.State)
	}
	base := strings.TrimSuffix(filepath.Base(l.State), ".json")
	stamp := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	dst := filepath.Join(filepath.Dir(l.State), base+".acked-"+stamp+".json")
	if err := os.Rename(l.State, dst); err != nil {
		return fmt.Errorf("couldn't set the interrupted job aside: %w", err)
	}
	return nil
}
