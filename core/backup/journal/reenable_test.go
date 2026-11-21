// file: core/backup/journal/reenable_test.go
// version: 0.1.0
// guid: 6b1e3f0a-8d24-4c57-b9a1-2f7c5e04d813
// last-edited: 2026-11-20

package journal

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// TestUserEnabled_IsAKnownCountedKind: this build applies user.enabled lines
// (KnownKind), so a re-apply never queues one as a newer build's kind, and a
// coverage comparison counts it (IsRevocation), as it counts any line that
// changes a credential's standing.
func TestUserEnabled_IsAKnownCountedKind(t *testing.T) {
	if !KnownKind(UserEnabled) {
		t.Error("KnownKind(user.enabled) = false: a re-apply would queue it as a newer build's line")
	}
	if !IsRevocation(UserEnabled) {
		t.Error("IsRevocation(user.enabled) = false: coverage would skip the line")
	}
}

// TestUserEnabled_OutOfOrderLineBreaksTheChain (owner cell 4, F4-08 Q6b): a
// user.enabled line moved ahead of the disable it follows breaks the chain at
// its seq (BreakSeq), so no replay reads the two lines in the wrong order. A
// broken chain is §5B.5's "chain broken" condition: the file refuses to open
// (ErrCorrupt), coverage is closed (a restore takes the no-coverage path), and
// the boot reports revocation_journal_broken; the app itself stays open.
func TestUserEnabled_OutOfOrderLineBreaksTheChain(t *testing.T) {
	fx := newFixture(t)
	j := fx.open(t)
	fx.commitRevoke(t, j, "harvey-dent")
	b := fx.auth.NewBatch()
	must(t, b.Set([]byte("user/harvey-dent"), []byte("active")))
	r := Revocation{At: time.Date(2026, 11, 10, 4, 0, 0, 0, time.UTC), Kind: UserEnabled, Epoch: 2,
		Subject: entity.Ref{Module: "core", Kind: "user", ID: "harvey-dent"}}
	must(t, j.Append(context.Background(), b, r))
	must(t, b.Commit(context.Background(), storage.Sync))
	must(t, j.Close())

	lines := fx.lines(t)
	intact := fx.chain(t)
	if intact.Break != nil || len(intact.Lines) != 2 || intact.Lines[1].Kind != UserEnabled {
		t.Fatalf("control: break %v, %d lines", intact.Break, len(intact.Lines))
	}
	fx.write(t, slices.Concat(lines[:1], []string{lines[2], lines[1]})...)
	c := fx.chain(t)
	if c.Break == nil || c.Break.Reason != BreakSeq {
		t.Fatalf("an enable moved ahead of its disable: break %v, want %s", c.Break, BreakSeq)
	}
	if cov := c.Cover(installID, 0, Head{}, false); cov.OK || cov.Lines != nil {
		t.Errorf("Cover = %+v, want no coverage over the reordered chain", cov)
	}
	fx.openBroken(t, "reordered enable", ErrCorrupt)
}
