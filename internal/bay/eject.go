package bay

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Eject flushes the drive and has the kernel let it go.  Deleting the SCSI
// device makes the sd driver sync the drive's cache, and stop the unit where
// the kernel manages start and stop, which is the spin-down.  The drive is
// gone from the host until it's pulled and reinserted.
func (s *Sys) Eject(d Device) error {
	if out, _, err := run(context.Background(), "blockdev", "--flushbufs", d.Path); err != nil {
		return fmt.Errorf("blockdev --flushbufs %s: %v %s", d.Path, err, bytes.TrimSpace(out))
	}
	// O_WRONLY without O_CREATE: if the node isn't there, fail rather than
	// leave a stray file behind.
	f, err := os.OpenFile(filepath.Join(s.sysBlock(), d.Name, "device", "delete"), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte("1")); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
