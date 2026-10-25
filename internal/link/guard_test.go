package link

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"zawiper/internal/proto"
)

// The hub can drop a record only by naming one this link sent.
func TestStoredNeedsAPendingRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "drives", "X", "2026-10-24T09-15-40Z-wipe")
	os.MkdirAll(dir, 0o755)
	s := &session{l: &Link{}, pending: map[string]string{}}
	for _, id := range []string{"X/2026-10-24T09-15-40Z-wipe", "../..", dir} {
		s.stored(id)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("a stored the link didn't ask for removed %s", dir)
	}
}

// A verb outside the contract is refused before anything is dialled.
func TestUnknownVerbRefused(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "bayd.sock")
	os.WriteFile(sock, nil, 0o600) // would fail to dial anyway; never reached
	l := &Link{Bay: "a", Socket: sock}
	for _, v := range []proto.Verb{"load", "abort", "", "run"} {
		if rep := l.command(context.Background(), proto.Request{Verb: v}); rep.OK || rep.Error == "" {
			t.Errorf("%q: %+v", v, rep)
		}
	}
}
