package link

import (
	"testing"

	"github.com/atmarx/zawiper/internal/model"
)

// A disk side from before versions came over the socket gets a stand-in, so
// the page can tell it from a disk side that isn't running at all.
func TestUnversioned(t *testing.T) {
	if s := unversioned(&model.Snapshot{}); s.Software != OldDisk {
		t.Errorf("old bayd: %q", s.Software)
	}
	if s := unversioned(&model.Snapshot{Software: "bayd 0.2.0 (842215c)"}); s.Software != "bayd 0.2.0 (842215c)" {
		t.Errorf("new bayd: %q", s.Software)
	}
	if unversioned(nil) != nil {
		t.Error("nil")
	}
}
