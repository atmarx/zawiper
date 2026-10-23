package bay

import (
	"context"
	"os"

	"zawiper/internal/model"
)

// Dev is a pretend bay for working on Za Wiper without hardware: a file is
// the drive (present while it exists), and SMART comes from a fixture.
// `truncate -s 1000204886016 bay.img` matches the testdata drive's size.
type Dev struct {
	File  string
	Smart string // path to `smartctl -j` output
}

func (v *Dev) Devices() ([]Device, error) {
	fi, err := os.Stat(v.File)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []Device{{Name: "dev", Path: v.File, Size: fi.Size(), Logical: 512, Physical: 512}}, nil
}

func (v *Dev) Bridge(Device) model.Bridge {
	return model.Bridge{Manufacturer: "Za Wiper", Product: "dev bay (a file)"}
}

func (v *Dev) InUse(Device) (string, error) { return "", nil }
func (v *Dev) SetRO(Device) error           { return nil }

func (v *Dev) SMART(context.Context, Device) ([]byte, []byte, error) {
	js, err := os.ReadFile(v.Smart)
	return js, []byte("dev mode: SMART is the fixture " + v.Smart + "\n"), err
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
