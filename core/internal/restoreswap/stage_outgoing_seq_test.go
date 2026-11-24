// file: core/internal/restoreswap/stage_outgoing_seq_test.go
// version: 0.1.0
// guid: c41f7e02-6d8b-4a35-9e17-0fb3a25d8c69
// last-edited: 2026-11-21

package restoreswap

import (
	"context"
	"os"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestStagedRecordCarriesTheOutgoingJournalHead (F4-08 B10k, 05-backup §5B.3.4
// step 8, DATA2-25): the `staged` journal record's Seq is the intent's
// OutgoingSeq, whether Stage wrote the record or P0a's ensureStaged did (the
// stager's create succeeded and its journal write did not), and only for the
// restore kinds.
func TestStagedRecordCarriesTheOutgoingJournalHead(t *testing.T) {
	h := newOSHost(t.TempDir())
	for n, kind := range []string{"archive", "revert", "checkpoint"} {
		t.Run(kind+" via Stage", func(t *testing.T) {
			s := h.swapper(t)
			in := h.intent(t, n, kind)
			in.OutgoingSeq = 40 + uint64(n)
			if err := s.Stage(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			recs, err := ReadJournal(vfs.Default, h.state, in.ID)
			if err != nil || len(recs) == 0 || recs[0].Step != StepStaged {
				t.Fatalf("journal = %+v, err %v, want a staged record first", recs, err)
			}
			if recs[0].Seq != in.OutgoingSeq {
				t.Fatalf("staged record Seq = %d, want the intent's OutgoingSeq %d", recs[0].Seq, in.OutgoingSeq)
			}
			// The slot is taken until P0a acts on it: free it for the next kind.
			if err := os.Remove(s.pendingPath()); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("ensureStaged", func(t *testing.T) {
		s := h.swapper(t)
		in := h.intent(t, 9, "archive")
		in.OutgoingSeq = 77
		recs, err := s.ensureStaged(context.Background(), in)
		if err != nil || len(recs) != 1 {
			t.Fatalf("ensureStaged = %+v, err %v", recs, err)
		}
		if recs[0].Seq != 77 {
			t.Fatalf("ensureStaged wrote Seq %d, want 77", recs[0].Seq)
		}
	})
	t.Run("a reset's staged record carries no seq", func(t *testing.T) {
		in := h.intent(t, 10, "store")
		in.OutgoingSeq = 5
		if in.Kind != restorejournal.KindReset {
			t.Fatalf("fixture kind = %q, want reset", in.Kind)
		}
		if got := stagedRecord(in); got.Seq != 0 {
			t.Fatalf("a reset's staged record Seq = %d, want 0", got.Seq)
		}
	})
}
