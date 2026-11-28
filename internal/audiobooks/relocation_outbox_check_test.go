// file: internal/audiobooks/relocation_outbox_check_test.go
// version: 0.1.0
// guid: 8e2a47c1-5d93-4f06-b1a8-3c7f90d2e614
// last-edited: 2026-11-28

package audiobooks

import (
	"strings"
	"testing"
)

// relocationProbeStore is a store that reports whether a relocation outbox is
// installed; mutes is a store that cannot say.
type relocationProbeStore bool

func (s relocationProbeStore) RelocationOutboxInstalled() bool { return bool(s) }

type relocationMuteStore struct{}

// TestRequireRelocationOutbox (F6-03 item 8): with the follow-up wired, a store
// with no relocation outbox, or one that cannot report it, stops the boot; an
// organized copy on such a store would stage nothing and no torrent would move.
func TestRequireRelocationOutbox(t *testing.T) {
	if err := RequireRelocationOutbox(relocationProbeStore(true)); err != nil {
		t.Fatalf("a store with the outbox installed was refused: %v", err)
	}
	err := RequireRelocationOutbox(relocationProbeStore(false))
	if err == nil || !strings.Contains(err.Error(), "no relocation outbox installed") {
		t.Fatalf("a store with no outbox = %v, want the refusal naming it", err)
	}
	err = RequireRelocationOutbox(relocationMuteStore{})
	if err == nil || !strings.Contains(err.Error(), "RelocationOutboxInstalled") {
		t.Fatalf("a store that cannot report = %v, want the refusal naming the method", err)
	}
}
