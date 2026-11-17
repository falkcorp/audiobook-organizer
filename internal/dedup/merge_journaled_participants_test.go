// file: internal/dedup/merge_journaled_participants_test.go
// version: 1.1.0
// guid: 7d1e5b93-0c42-4a8f-9e36-b2f40a6d8c15
// last-edited: 2026-11-17

package dedup

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
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

// byLoser reads the journal rows a merge returned the keys of, by their LoserID.
func byLoser(t *testing.T, es *database.EmbeddingStore, keys []string) map[string]*database.AutoMergeJournalEntry {
	t.Helper()
	out := map[string]*database.AutoMergeJournalEntry{}
	for _, k := range keys {
		e, err := es.GetAutoMergeJournalEntry(k)
		require.NoError(t, err)
		require.NotContains(t, out, e.LoserID, "two rows name %s as their loser", e.LoserID)
		out[e.LoserID] = e
	}
	return out
}

// The by-id row matching is observable in the rows themselves: after a mispredicted
// winner and one failed patch write, every real loser is the LoserID of exactly one
// row, and the one row left provisional names the loser whose write failed. By
// INDEX the failed write would be some other loser's, so one loser would name no
// row and another would name two, which the duplicate-LoserID and missing-row
// assertions below catch. The purge hold is not what fails here: the participant
// lists of the provisional rows keep every book held either way.
//
// Rows are (alfred,batgirl) (alfred,catwoman) (alfred,nightwing), the real losers
// are alfred, batgirl, catwoman, and the patch writes them in that order, so the
// failing second write is batgirl's.
func TestMergeBooksJournaled_AFailedPatchWriteLeavesTheRowOfThatLoserProvisionalAndHeld(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	ids, flip := fourBooks(t, f)

	flipped, patches := false, 0
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		if e.Provisional && !flipped {
			flipped = true
			flip()
		}
		if !e.Provisional && e.AbortedAt == 0 {
			patches++
			if patches == 2 {
				return "", errors.New("injected: patch write failed")
			}
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	res, keys, err := engine.MergeBooksJournaled(0, ids, "", "t")
	require.NoError(t, err)
	require.Equal(t, "nightwing", res.PrimaryID, "precondition: the winner was mispredicted")
	require.Equal(t, 3, patches, "precondition: all three patch writes were attempted")
	require.Len(t, keys, 3)

	rows := byLoser(t, es, keys)
	require.Len(t, rows, 3)
	for _, id := range []string{"alfred", "batgirl", "catwoman"} {
		require.Contains(t, rows, id, "%s is a real loser and names a row", id)
	}
	require.NotContains(t, rows, "nightwing", "the winner is the loser of no row")
	require.False(t, rows["alfred"].Provisional)
	require.False(t, rows["catwoman"].Provisional)
	require.True(t, rows["batgirl"].Provisional, "the row whose write failed is batgirl's")

	open, err := es.OpenAutoMergeLosers()
	require.NoError(t, err)
	for _, id := range []string{"alfred", "batgirl", "catwoman"} {
		require.Equal(t, rows[id].Key, open.Losers[id], "%s is held by the row that names it", id)
	}
}

// A two-book merge whose winner was mispredicted: the one row is (alfred,nightwing),
// the real loser is alfred, and the row is the spare that takes its place. When the
// patch cannot finalize it, the row stays provisional and names nightwing, the
// WINNER, as its loser; alfred, the real loser and soft-deleted, is named by no row
// and only the participant list can hold it from the trash purge. The file-hash
// auto-merge reaches this shape (it passes no keeper).
func TestMergeBooksJournaled_ATwoBookMergeWithAMispredictedWinnerHoldsItsRealLoserWhenThePatchDoesNotRun(t *testing.T) {
	for _, how := range []string{"write fails", "winner snapshots unreadable", "loser snapshots unreadable"} {
		t.Run(how, func(t *testing.T) {
			f := vptest.New(t)
			engine, es := engineOn(t, f, f.S)
			p := f.Book(t, vptest.Spec{ID: "alfred", Group: "G1", Primary: "true"})
			w := f.Book(t, vptest.Spec{ID: "nightwing", Group: "G2", Primary: "false"})
			store := &armedSnapshotListStore{Store: f.S}
			engine.bookStore = store
			flipped := false
			engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
				if e.Provisional && !flipped {
					flipped = true
					for id, primary := range map[string]bool{p: false, w: true} {
						_, err := f.S.ModifyBook(id, func(row *database.Book) error {
							row.IsPrimaryVersion = &primary
							return nil
						})
						require.NoError(t, err)
					}
					switch how {
					case "winner snapshots unreadable":
						store.failID, store.armed = w, true
					case "loser snapshots unreadable":
						store.failID, store.armed = p, true
					}
				}
				if how == "write fails" && !e.Provisional && e.AbortedAt == 0 {
					return "", errors.New("injected: patch write failed")
				}
				return es.PutAutoMergeJournalEntry(e)
			}

			res, keys, err := engine.MergeBooksJournaled(0, []string{p, w}, "", "t")
			require.NoError(t, err)
			require.Equal(t, w, res.PrimaryID, "precondition: the winner was mispredicted")
			require.Len(t, keys, 1)
			row, gerr := es.GetAutoMergeJournalEntry(keys[0])
			require.NoError(t, gerr)
			require.True(t, row.Provisional, "precondition: the row could not be finalized")
			require.Equal(t, w, row.LoserID, "precondition: the spare row names the actual winner as its loser")
			pb, gerr := f.S.GetBookByID(p)
			require.NoError(t, gerr)
			require.True(t, pb.IsSoftDeleted(), "precondition: alfred was the real loser")

			open, err := es.OpenAutoMergeLosers()
			require.NoError(t, err)
			require.Equal(t, row.Key, open.Losers[p], "the real loser is held from the trash purge")

			// The undo refusal must name the real loser, not only the row's
			// LoserID (which here is the live winner).
			uerr := engine.UnmergeAuto(row.Key)
			require.Error(t, uerr)
			require.Contains(t, uerr.Error(), p, "the refusal names the real loser, which is the book in the trash")
		})
	}
}

// A winner whose snapshots cannot be read leaves every row of the merge provisional,
// and the one log line that says so names every row, instead of one identical line
// per row. Alongside it, each participant of the merge is held from the purge.
func TestMergeBooksJournaled_AWinnerReadErrorHoldsEveryParticipantOfEveryRow(t *testing.T) {
	logs := logtest.Capture(t, slog.LevelError)
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	ids, flip := fourBooks(t, f)
	store := &armedSnapshotListStore{Store: f.S, failID: "nightwing"}
	engine.bookStore = store
	flipped := false
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		if e.Provisional && !flipped {
			flipped = true
			flip()
			store.armed = true
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	res, keys, err := engine.MergeBooksJournaled(0, ids, "", "t")
	require.NoError(t, err)
	require.Equal(t, "nightwing", res.PrimaryID)
	require.Len(t, keys, 3)

	open, err := es.OpenAutoMergeLosers()
	require.NoError(t, err)
	for _, id := range ids {
		require.Contains(t, open.Losers, id, "%s is a participant of a live provisional row", id)
	}

	lines := logs.Lines("cannot read the snapshots of winner")
	require.Len(t, lines, 1, "one line for the merge, not one per row")
	for _, k := range keys {
		require.True(t, strings.Contains(lines[0], k), "the line names row %s: %s", k, lines[0])
	}
}
