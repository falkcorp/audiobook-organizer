// file: internal/dedup/merge_journaled_participants_test.go
// version: 1.1.0
// guid: 7d1e5b93-0c42-4a8f-9e36-b2f40a6d8c15
// last-edited: 2026-11-17

package dedup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// fourBooks seeds a four-book cluster. Only alfred holds the primary flag, so
// the election the journaled merge PREDICTS names alfred; flipMispredict swaps the
// flag to nightwing before the first journal write, so the election MergeBooks
// makes names nightwing instead. That is a mispredicted winner in a merge of four.
func fourBooks(t *testing.T, f *vptest.Fixture) (ids []string, flipMispredict func()) {
	t.Helper()
	a := f.Book(t, vptest.Spec{ID: "alfred", Group: "G1", Primary: "true"})
	b := f.Book(t, vptest.Spec{ID: "batgirl", Group: "G2", Primary: "false"})
	c := f.Book(t, vptest.Spec{ID: "catwoman", Group: "G3", Primary: "false"})
	d := f.Book(t, vptest.Spec{ID: "nightwing", Group: "G4", Primary: "false"})
	flip := func() {
		for id, primary := range map[string]bool{a: false, d: true} {
			_, err := f.S.ModifyBook(id, func(row *database.Book) error {
				row.IsPrimaryVersion = &primary
				return nil
			})
			require.NoError(t, err)
		}
	}
	return []string{a, b, c, d}, flip
}

// newestBefore is each book's newest snapshot right now: the baseline a merge that
// starts now records. The expected pre-merge snapshot is worked out from it and the
// store (firstSnapshotAbove), never from the code under test.
func newestBefore(t *testing.T, s *database.PebbleStore, ids []string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, id := range ids {
		snaps, err := s.GetBookSnapshots(id, 1)
		require.NoError(t, err)
		require.NotEmpty(t, snaps, "the fixture left a snapshot for %s", id)
		out[id] = snaps[0].Timestamp.UnixNano()
	}
	return out
}

// firstSnapshotAbove is the earliest snapshot of id strictly newer than floor.
func firstSnapshotAbove(t *testing.T, s *database.PebbleStore, id string, floor int64) int64 {
	t.Helper()
	snaps, err := s.GetBookSnapshots(id, 0)
	require.NoError(t, err)
	var best int64
	for _, sn := range snaps {
		ts := sn.Timestamp.UnixNano()
		if ts > floor && (best == 0 || ts < best) {
			best = ts
		}
	}
	require.NotZero(t, best, "the merge wrote a snapshot of %s", id)
	return best
}

// In a merge of four books whose winner was mispredicted, the provisional rows are
// (alfred,batgirl) (alfred,catwoman) (alfred,nightwing) and the real losers are
// alfred, batgirl and catwoman. The post-merge patch used to overwrite those rows
// by INDEX against the actual loser list, so one failed write could leave a real
// loser in no row at all, unpinned and free to be pruned. Whichever single patch
// write fails, every participant's pre-merge snapshot must survive a prune.
func TestMergeBooksJournaled_MispredictedWinnerAndAFailedPatchWriteLeaveNoParticipantUnpinned(t *testing.T) {
	// 0 is every patch write failing, so every row stays as it was first written.
	for _, failPatch := range []int{1, 2, 3, 0} {
		t.Run(fmt.Sprintf("failing patch write %d", failPatch), func(t *testing.T) {
			f := vptest.New(t)
			engine, es := engineOn(t, f, f.S)
			ids, flip := fourBooks(t, f)
			baseline := newestBefore(t, f.S, ids)

			flipped, patches := false, 0
			engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
				if e.Provisional && !flipped {
					flipped = true
					flip()
				}
				if !e.Provisional && e.AbortedAt == 0 {
					patches++
					if failPatch == 0 || patches == failPatch {
						return "", errors.New("injected: patch write failed")
					}
				}
				return es.PutAutoMergeJournalEntry(e)
			}
			res, keys, err := engine.MergeBooksJournaled(0, ids, "", "t")
			require.NoError(t, err)
			require.Len(t, keys, 3)
			require.Equal(t, "nightwing", res.PrimaryID, "precondition: the winner was mispredicted")
			require.Equal(t, 3, patches, "precondition: all three patch writes were attempted")

			want := map[string]int64{}
			for _, id := range ids {
				want[id] = firstSnapshotAbove(t, f.S, id, baseline[id])
				editTitle(t, f.S, id, 3) // newer snapshots, so keep=1 would drop the older ones
			}
			for _, id := range ids {
				require.Contains(t, pinnedAfterPrune(t, f.S, id, 1), want[id],
					"%s lost the snapshot a merge undo or a hand repair needs", id)
			}
		})
	}
}

// Every actual loser is the LoserID of exactly one row, however the winner was
// predicted: the patch matches rows to losers by book id, and the row whose loser
// is the actual winner is the one that is handed on to the predicted winner.
func TestMergeBooksJournaled_PatchMatchesRowsToLosersByBookID(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	ids, flip := fourBooks(t, f)
	flipped := false
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		if e.Provisional && !flipped {
			flipped = true
			flip()
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	res, keys, err := engine.MergeBooksJournaled(0, ids, "", "t")
	require.NoError(t, err)
	require.Equal(t, "nightwing", res.PrimaryID)
	require.Len(t, keys, 3)

	seen := map[string]int{}
	for _, k := range keys {
		e, gerr := es.GetAutoMergeJournalEntry(k)
		require.NoError(t, gerr)
		require.False(t, e.Provisional, "every row was finalized")
		require.Equal(t, res.PrimaryID, e.WinnerID)
		seen[e.LoserID]++
		require.NotZero(t, e.LoserPreMergeTS)
		require.NotZero(t, e.WinnerPreMergeTS)
	}
	require.Equal(t, map[string]int{"alfred": 1, "batgirl": 1, "catwoman": 1}, seen)
}

// Each provisional row records EVERY participant of the merge with the baseline it
// has for it, so a row that survives a failed patch still covers the whole merge,
// and it still writes the winner and loser baselines an older binary reads. The
// rows are held provisional here by failing every patch write.
func TestMergeBooksJournaled_ProvisionalRowsListEveryParticipantWithItsBaseline(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	ids, _ := fourBooks(t, f)
	baseline := newestBefore(t, f.S, ids)
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		if !e.Provisional {
			return "", errors.New("injected: patch write failed")
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	_, keys, err := engine.MergeBooksJournaled(0, ids, "", "t")
	require.NoError(t, err)
	require.Len(t, keys, 3)

	want := make([]database.AutoMergeJournalParticipant, len(ids))
	for i, id := range ids {
		want[i] = database.AutoMergeJournalParticipant{BookID: id, BaselineTS: baseline[id]}
	}
	for _, k := range keys {
		e, gerr := es.GetAutoMergeJournalEntry(k)
		require.NoError(t, gerr)
		require.True(t, e.Provisional)
		require.Equal(t, want, e.Participants, "every participant, in the order the merge was asked for")
		require.NotEmpty(t, e.RowID, "each row carries the identity of the write that created it")
		require.True(t, e.BaselinesRecorded)
		require.Equal(t, baseline[e.WinnerID], e.WinnerBaselineTS, "the older fields are still written")
		require.Equal(t, baseline[e.LoserID], e.LoserBaselineTS)
	}
}
