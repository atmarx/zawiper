package bay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/atmarx/zawiper/internal/model"
)

// Dev is a pretend bay for working on Za Wiper without hardware: a file is
// the drive (present while it exists), and SMART comes from a fixture.  The
// fixture's capacity is reported as the file's size, so any file a whole
// number of 512-byte sectors, 1 MiB or more, reads clean and can be wiped:
//
//	truncate -s 64M bay.img
type Dev struct {
	File  string
	Smart string // path to `smartctl -j` output

	mu      sync.Mutex
	ejected string // the file as it stood when ejected; it's back once it changes
}

// Bounds: a file can't lie about its size the way a bridge can, and dev
// mode wants small ones.
func (v *Dev) Bounds() (int64, int64) { return 1 << 20, MaxSize }

func (v *Dev) Devices() ([]Device, error) {
	fi, err := os.Stat(v.File)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.ejected != "" {
		if v.ejected == stamp(fi) {
			return nil, nil
		}
		v.ejected = ""
	}
	return []Device{{Name: "dev", Path: v.File, Size: fi.Size(), Logical: 512, Physical: 512}}, nil
}

func stamp(fi os.FileInfo) string { return fmt.Sprint(fi.Size(), fi.ModTime().UnixNano()) }

func (v *Dev) Bridge(Device) model.Bridge {
	return model.Bridge{Manufacturer: "Za Wiper", Product: "dev bay (a file)"}
}

func (v *Dev) InUse(Device) (string, error) { return "", nil }
func (v *Dev) SetRO(Device) error           { return nil }
func (v *Dev) SetRW(Device) error           { return nil }
func (v *Dev) Forget(Device) error          { return nil }

// Eject hides the file until it changes: `touch bay.img` puts it back.
func (v *Dev) Eject(Device) error {
	fi, err := os.Stat(v.File)
	if err != nil {
		return err
	}
	v.mu.Lock()
	v.ejected = stamp(fi)
	v.mu.Unlock()
	return nil
}

func (v *Dev) SMART(context.Context, Device) ([]byte, []byte, error) {
	js, err := os.ReadFile(v.Smart)
	if err != nil {
		return nil, nil, err
	}
	if fi, err := os.Stat(v.File); err == nil {
		js = withCapacity(js, fi.Size())
	}
	return js, []byte("dev mode: SMART is the fixture " + v.Smart + "\n"), nil
}

// withCapacity rewrites the fixture's user_capacity to the file's size.
// Anything it can't parse goes through untouched, for smart.Parse to judge.
func withCapacity(js []byte, size int64) []byte {
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return js
	}
	m["user_capacity"] = map[string]any{"bytes": size}
	out, err := json.Marshal(m)
	if err != nil {
		return js
	}
	return out
}

func (v *Dev) Table(ctx context.Context, d Device) (model.Table, error) {
	t := model.Table{Parts: []model.Part{}}
	b, err := blkid(ctx, d.Path)
	if err != nil {
		return t, err
	}
	t.Type = b["PTTYPE"]
	if b["TYPE"] != "" {
		t.Parts = append(t.Parts, model.Part{Path: d.Path, Size: d.Size, FSType: b["TYPE"], Label: b["LABEL"], UUID: b["UUID"]})
	}
	return t, nil
}

func (v *Dev) Peek(_ context.Context, p *model.Part) { p.Peek = "not mounted: dev mode" }
