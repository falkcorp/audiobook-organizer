// file: internal/plugins/maintenance/retire_carry_resume_test.go
// version: 1.2.0
// guid: 0c7d4e2a-91b3-4f68-a5d0-3e8f1b72c946
// last-edited: 2026-10-23

package maintenance

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// journalCrashedCarry writes what JournalSteps committed for one carry of
// retired into survivor on the collection, as the list stood (cur): the
// survivor-side marker and the retired book's row, in one batch, without the
// list write that was to follow. It is the state a process that died between
// the journal commit and the list write leaves behind.
func (c *chainFixture) journalCrashedCarry(op, retired, survivor string, cur []string) (marker, row *database.OperationChange) {
	c.t.Helper()
	ids := make([]string, len(cur))
	for i, n := range cur {
		ids[i] = c.id[n]
	}
	_, rec, ok := undo.CarryMembership(ids, c.id[retired], c.id[survivor])
	require.True(c.t, ok)
	val, err := undo.EncodeMembership(rec)
	require.NoError(c.t, err)
	markerKind := undo.TookKind(undo.UserContentCollection)
	if rec.SurvivorWasMember {
		markerKind = undo.HeldKind(undo.UserContentCollection)
	}
	marker = &database.OperationChange{ID: ulid.Make().String(), OperationID: op, BookID: c.id[survivor],
		ChangeType: undo.ChangeTypeUserContentFollow, FieldName: undo.UserContentField(markerKind, c.col), OldValue: val, NewValue: c.id[retired]}
	row = &database.OperationChange{ID: ulid.Make().String(), OperationID: op, BookID: c.id[retired],
		ChangeType: undo.ChangeTypeUserContentFollow, FieldName: undo.UserContentField(undo.UserContentCollection, c.col), OldValue: val, NewValue: c.id[survivor]}
	require.NoError(c.t, c.f.s.CreateOperationChanges([]*database.OperationChange{marker, row}))
	return marker, row
}

// handInsertAfter lists book name right after book after, as the owner would.
func (c *chainFixture) handInsertAfter(name, after string) {
	c.t.Helper()
	col, err := c.f.s.GetCollection(c.col)
	require.NoError(c.t, err)
	at := slices.Index(col.BookIDs, c.id[after])
	require.GreaterOrEqual(c.t, at, 0)
	col.BookIDs = slices.Insert(col.BookIDs, at+1, c.id[name])
	require.NoError(c.t, c.f.s.UpdateCollection(col))
}

// A retire that died after its journal commit and before its list write is
// resumed under the same operation id. Between the two the owner listed the
// survivor by hand (at the end, or right beside the retired book, in its slot),
// so the resumed carry journals a second pair (a held marker and a row that
// recorded the survivor as already listed) for the one retire: two live rows for
// (op1, X, S, list). Reverting the operation must put the retired book back once
// and leave the list as the owner has it: the survivor the owner listed still
// listed, the older pair's marker named in the result (a later marker names the
// same retire), nothing released and nothing failed.
func TestRetireCarry_AResumedCarryWithTwoPairsRestoresOnceAndKeepsTheOwnersSurvivor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		hand func(c *chainFixture)
		afterCarry,
		want []string
	}{
		{"survivor listed at the end", func(c *chainFixture) { c.handAppend("S") }, []string{"a", "b", "S"}, []string{"a", "X", "b", "S"}},
		{"survivor listed in the retired book's slot", func(c *chainFixture) { c.handInsertAfter("S", "X") }, []string{"a", "S", "b"}, []string{"a", "X", "S", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newChainFixture(t, []string{"a", "X", "b", "S"}, []string{"a", "X", "b"})
			m1, r1 := c.journalCrashedCarry("op1", "X", "S", []string{"a", "X", "b"})
			require.Equal(t, []string{"a", "X", "b"}, c.members(), "the crash left the list as it was")
			tc.hand(c)
			c.carry("op1", "X", "S") // the resumed run: S is listed now, so X is just removed
			require.Equal(t, tc.afterCarry, c.members())

			rows, err := c.f.s.GetOperationChanges("op1")
			require.NoError(t, err)
			var live int
			for _, r := range rows {
				if !r.Voided && r.BookID == c.id["X"] && r.FieldName == undo.UserContentField(undo.UserContentCollection, c.col) {
					live++
				}
			}
			require.Equal(t, 2, live, "two live main rows for the one retire")

			rr, err := audiobooks.NewRevertService(c.st).RevertOperation("op1")
			require.NoError(t, err, "%+v", rr)
			require.Zero(t, rr.Failed, "%+v", rr)
			require.Equal(t, tc.want, c.members(), "X back beside a and b, the owner's S kept: %+v", rr)
			require.Equal(t, 1, rr.AlreadyRestored, "the older row finds X listed and writes nothing: %+v", rr)
			require.Zero(t, rr.SurvivorsReleased, "the owner's survivor is not released: %+v", rr)
			require.Zero(t, rr.Superseded, "%+v", rr)
			require.Equal(t, 1, rr.OrphanMarkers, "the older pair's marker is named, not silent: %+v", rr)
			require.Contains(t, strings.Join(rr.Kept, "\n"), m1.ID)
			require.Contains(t, strings.Join(rr.Kept, "\n"), "a later marker names the same retire")

			// Both rows end reverted, so a second revert has nothing to do.
			after, err := c.f.s.GetOperationChanges("op1")
			require.NoError(t, err)
			for _, r := range after {
				if r.ID == r1.ID {
					require.NotNil(t, r.RevertedAt, "the older row is stamped reverted")
				}
			}
			_, err = audiobooks.NewRevertService(c.st).RevertOperation("op1")
			require.ErrorContains(t, err, "already been reverted")
			require.Equal(t, tc.want, c.members())
		})
	}
}

// failingListMark is a store whose collection list write fails (a commit, a
// persist or a version conflict: not a refusal) for its first fail calls.
type failingListMark struct {
	*database.PebbleStore
	fail, calls int
}

func (f *failingListMark) UpdateCollectionBooksAndMark(id string, fn func([]string) ([]string, error), mark database.OperationMark) (*database.Collection, error) {
	f.calls++
	if f.calls <= f.fail {
		return nil, errors.New("simulated commit failure")
	}
	return f.PebbleStore.UpdateCollectionBooksAndMark(id, fn, mark)
}

// The newest row of a resumed retire (two live pairs for one retire) fails to
// write its list for a reason that is not a refusal. The older row must not run
// on its own: its record (the survivor took the slot) would swap the survivor
// the owner listed in that slot out of the list, and stamp the newest pair's
// marker reverted while the newest row stays live. It is left standing and
// reported; once the failure clears, a retry puts the book back once beside the
// owner's survivor.
func TestRetireCarry_ARefusedNewestRowOfAResumedRetireLeavesTheOlderRowStanding(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "b", "S"}, []string{"a", "X", "b"})
	_, r1 := c.journalCrashedCarry("op1", "X", "S", []string{"a", "X", "b"})
	c.handInsertAfter("S", "X") // [a X S b]: the owner lists S in X's slot
	c.carry("op1", "X", "S")    // the resumed run journals the second pair and removes X
	require.Equal(t, []string{"a", "S", "b"}, c.members())

	failing := &failingListMark{PebbleStore: c.f.s, fail: 1}
	rr, err := audiobooks.NewRevertService(failing).RevertOperation("op1")
	require.Error(t, err, "the newest row failed: %+v", rr)
	require.NotNil(t, rr)
	require.Equal(t, 2, rr.Failed, "the failed newest row and the older row left standing: %+v", rr)
	require.Contains(t, err.Error(), "left standing, not applied")
	require.Equal(t, []string{"a", "S", "b"}, c.members(), "the owner's survivor is not swapped out of the slot")
	rows, gerr := c.f.s.GetOperationChanges("op1")
	require.NoError(t, gerr)
	for _, r := range rows {
		if _, _, ok := undo.UserContentFromField(r.FieldName); ok {
			require.Nil(t, r.RevertedAt, "no row or marker is stamped reverted while the newest row stands: %s %s", r.FieldName, r.ID)
		}
	}

	failing.fail = 0 // the failure clears
	rr, err = audiobooks.NewRevertService(failing).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, []string{"a", "X", "S", "b"}, c.members())
	rows, gerr = c.f.s.GetOperationChanges("op1")
	require.NoError(t, gerr)
	for _, r := range rows {
		if r.ID == r1.ID {
			require.NotNil(t, r.RevertedAt, "the older row is reverted after the retry")
		}
	}
}

// lockProbe is a store that, while it stamps rows reverted, tries to take the
// lock of one list from another goroutine and records whether it got it.
type lockProbe struct {
	*database.PebbleStore
	kind, listID, rowID string
	mu                  sync.Mutex
	probed, heldByMark  bool
}

func (l *lockProbe) MarkOperationChangesReverted(op string, ids []string) error {
	if slices.Contains(ids, l.rowID) {
		got := make(chan struct{})
		go func() {
			defer undo.LockList(l.kind, l.listID)()
			close(got)
		}()
		held := false
		select {
		case <-got:
		case <-time.After(300 * time.Millisecond):
			held = true
		}
		l.mu.Lock()
		l.probed, l.heldByMark = true, held
		l.mu.Unlock()
	}
	return l.PebbleStore.MarkOperationChangesReverted(op, ids)
}

// The end-of-revert stamp of a list row (one the list write did not stamp: its
// marker was reverted already) is made under the list's lock, like every other
// stamp of a list's rows: the journal clock orders a list's rows by their
// sequences only while every writer of them holds the list lock.
func TestRetireCarry_TheEndOfRevertStampOfAListRowHoldsTheListLock(t *testing.T) {
	t.Parallel()
	c := newChainFixture(t, []string{"a", "X", "b", "S"}, []string{"a", "X", "b"})
	c.carry("op1", "X", "S")
	rows, err := c.f.s.GetOperationChanges("op1")
	require.NoError(t, err)
	var row *database.OperationChange
	var markers []string
	for _, r := range rows {
		if kind, _, ok := undo.UserContentFromField(r.FieldName); ok && undo.IsMarker(kind) {
			markers = append(markers, r.ID)
		} else if ok && kind == undo.UserContentCollection {
			row = r
		}
	}
	require.NotNil(t, row)
	require.Len(t, markers, 1)
	// The marker is already reverted, so the row's revert has no mark to stamp
	// with it and the row is stamped by the end-of-revert call.
	require.NoError(t, c.f.s.MarkOperationChangesReverted("op1", markers))

	probe := &lockProbe{PebbleStore: c.f.s, kind: undo.UserContentCollection, listID: c.col, rowID: row.ID}
	rr, err := audiobooks.NewRevertService(probe).RevertOperation("op1")
	require.NoError(t, err, "%+v", rr)
	require.Zero(t, rr.Failed, "%+v", rr)
	require.Equal(t, []string{"a", "X", "b"}, c.members())
	probe.mu.Lock()
	defer probe.mu.Unlock()
	require.True(t, probe.probed, "the row was stamped by the end-of-revert call")
	require.True(t, probe.heldByMark, "the list lock was held while the row was stamped")
}
