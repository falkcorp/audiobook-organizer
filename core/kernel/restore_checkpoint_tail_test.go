// file: core/kernel/restore_checkpoint_tail_test.go
// version: 0.1.0
// guid: 8d3a6f15-2e47-4b90-b1c8-5a07e9d4f263
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"testing"

	"github.com/falkcorp/mediacore/backup/journal"
)

// TestRestoreAfterACheckpointReplaysOnlyTheTail (F4-08 B10k, 05-backup §5B.3.6
// "Coverage at restore"): a checkpoint's record carries the journal head its
// store had seen, taken through the kernel's storage binding (journalHeadSeq),
// and a restore of that checkpoint hands the hooks only the journal lines after
// it. Without the writer the record says 0 and the same restore replays the
// whole journal, which grows without bound.
func TestRestoreAfterACheckpointReplaysOnlyTheTail(t *testing.T) {
	// The journal: line 1 (key-spoiler revoked, committed) is in the checkpoint's
	// store; lines 2 and 3 came after it.
	run := func(t *testing.T, record func(g *batcave) uint64) (replayed int, boundary uint64) {
		t.Helper()
		g := newBatcave(t)
		cpSeq := record(g)
		cp := g.snapshot("checkpoint") // the store the checkpoint copied: head 1
		g.revokeKey("key-batmobile")   // line 2
		g.journalOnly(journal.Revocation{Kind: journal.APIKeyRevoked, Subject: rxKeyRef("key-redrobin")})
		g.restart()
		previous := g.snapshot("previous")
		g.swapIn(cp)
		replayed = -1
		count := func(hookEnv) ([]journal.RestoreHook, error) {
			return []journal.RestoreHook{func(_ context.Context, rc journal.RestoreContext) (map[string]int, error) {
				if rc.Revocations == nil {
					return nil, nil
				}
				replayed = 0
				for range rc.Revocations {
					replayed++
				}
				return nil, nil
			}}, nil
		}
		rb := &restoreBoot{ID: "r-checkpoint", Kind: journal.KindCheckpoint, PreSwapDir: previous, Steps: &rxSteps{}, SourceSeq: cpSeq}
		res, _, err := g.tryBoot(rb, nil, count)
		if err != nil {
			t.Fatal(err)
		}
		return replayed, res.RestoreBoundary
	}

	t.Run("the record carries the store's journal head", func(t *testing.T) {
		replayed, boundary := run(t, func(g *batcave) uint64 {
			seq, err := journalHeadSeq(g.ctx, g.bks())
			if err != nil {
				t.Fatal(err)
			}
			if seq != 1 {
				t.Fatalf("journalHeadSeq = %d, want the checkpoint's store's head 1", seq)
			}
			return seq
		})
		if boundary != 1 || replayed != 2 {
			t.Fatalf("boundary %d, %d line(s) replayed, want boundary 1 and only the 2 lines after the checkpoint", boundary, replayed)
		}
	})
	t.Run("a record without the writer replays everything", func(t *testing.T) {
		replayed, boundary := run(t, func(*batcave) uint64 { return 0 })
		if boundary != 0 || replayed != 3 {
			t.Fatalf("boundary %d, %d line(s) replayed, want boundary 0 and all 3 lines", boundary, replayed)
		}
	})
}

// TestStorageOptionsBindTheJournalHeadReader: every open of the store (P2's and the
// offline opens') carries the reader the pre-migration checkpoint's journal_seq
// comes from; without it storage records 0 and a restore of that checkpoint
// replays the whole journal.
func TestStorageOptionsBindTheJournalHeadReader(t *testing.T) {
	h := newSwapHost(t)
	k, err := offlineKernel(h.config(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.closeOffline(context.Background()) }()
	opts, err := k.storageOptions(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if opts.JournalHead == nil {
		t.Fatal("storage.OpenOptions.JournalHead is not bound: checkpoints would record journal_seq 0")
	}
}
