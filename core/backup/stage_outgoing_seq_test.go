// file: core/backup/stage_outgoing_seq_test.go
// version: 0.1.0
// guid: 0b8e4a97-3c52-4d1f-a6e0-71d9c2f58b34
// last-edited: 2026-11-21

package backup_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/backup"
)

// TestStagedRestoresCarryTheOutgoingJournalHead (F4-08 B10k, 05-backup §5B.3.4
// step 8, DATA2-25): an archive restore and a revert each stage an intent that
// carries the live store's committed journal head, which becomes the `staged`
// record's Seq, the recorded boundary of a later revert.
func TestStagedRestoresCarryTheOutgoingJournalHead(t *testing.T) {
	ctx := context.Background()
	t.Run("archive", func(t *testing.T) {
		e := newEnv(t)
		a := e.create("manual")
		e.markers.head = 41
		if _, err := e.svc.StageRestore(asOwner(ctx), a.ID, backup.RestoreOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := e.stager.staged[0].OutgoingSeq; got != 41 {
			t.Fatalf("archive intent OutgoingSeq = %d, want the live journal head 41", got)
		}
	})
	t.Run("revert", func(t *testing.T) {
		e := newEnv(t)
		revertFixture(t, e, "20261110T030000Z-cccccccc", "applied", true)
		e.markers.head = 58
		if _, err := e.svc.RevertLastRestore(asOwner(ctx), backup.RestoreOptions{}); err != nil {
			t.Fatal(err)
		}
		if got := e.stager.staged[0].OutgoingSeq; got != 58 {
			t.Fatalf("revert intent OutgoingSeq = %d, want the live journal head 58", got)
		}
	})
}

// TestStagingRefusesWhenTheJournalHeadCannotBeRead: a head that cannot be read is
// not recorded as 0; nothing is staged.
func TestStagingRefusesWhenTheJournalHeadCannotBeRead(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("joker scrambled the head")
	t.Run("archive", func(t *testing.T) {
		e := newEnv(t)
		a := e.create("manual")
		e.markers.failRead = boom
		if _, err := e.svc.StageRestore(asOwner(ctx), a.ID, backup.RestoreOptions{}); !errors.Is(err, boom) {
			t.Fatalf("StageRestore = %v, want the read error", err)
		}
		if len(e.stager.staged) != 0 {
			t.Fatalf("intents = %d, want none", len(e.stager.staged))
		}
	})
	t.Run("revert", func(t *testing.T) {
		e := newEnv(t)
		revertFixture(t, e, "20261110T030000Z-cccccccc", "applied", true)
		e.markers.failRead = boom
		if _, err := e.svc.RevertLastRestore(asOwner(ctx), backup.RestoreOptions{}); !errors.Is(err, boom) {
			t.Fatalf("RevertLastRestore = %v, want the read error", err)
		}
		if len(e.stager.staged) != 0 {
			t.Fatalf("intents = %d, want none", len(e.stager.staged))
		}
	})
}
