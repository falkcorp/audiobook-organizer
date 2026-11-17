// file: internal/dedup/merge_journaled_row_identity_test.go
// version: 1.0.0
// guid: b4e8d1a7-3c92-4f05-8a6b-1d7c0e9f2a58
// last-edited: 2026-11-17

package dedup

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// onFirstReadStore runs a hook the first time MergeBooks reads a book through it.
// MergeBooks' first act is to load its participants, which happens after the
// journaled merge wrote its provisional rows and before it can refuse, so the hook
// fires exactly between those two points.
type onFirstReadStore struct {
	database.Store
	hook func()
	done bool
}

func (o *onFirstReadStore) Unwrap() database.Store { return o.Store }
func (o *onFirstReadStore) GetBookByID(id string) (*database.Book, error) {
	if !o.done {
		o.done = true
		o.hook()
	}
	return o.Store.GetBookByID(id)
}

// A refused merge closes only the row it wrote. A different merge's row at the same
// key, finalized since, is left exactly as it is: merge A writes K, another merge
// B overwrites K and finalizes it, A is then refused. B's row must survive.
func TestMergeBooksJournaled_RefusalDoesNotCloseARowAnotherMergeNowHolds(t *testing.T) {
	f := vptest.New(t)
	svc := &onFirstReadStore{Store: f.S, done: true} // armed below, after the first merge
	engine, es := engineOn(t, f, svc)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	_, _, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t") // l is now soft-deleted
	require.NoError(t, err)

	var foreign database.AutoMergeJournalEntry
	svc.done = false
	svc.hook = func() {
		rows, lerr := es.ListAutoMergeJournalEntries(0)
		require.NoError(t, lerr)
		var a *database.AutoMergeJournalEntry
		for i := range rows {
			if rows[i].Provisional && rows[i].AbortedAt == 0 {
				a = &rows[i]
			}
		}
		require.NotNil(t, a, "merge A's provisional row exists when its merge starts")
		// Merge B takes over A's key and finalizes it.
		foreign = database.AutoMergeJournalEntry{
			CandidateID: 99, WinnerID: "alfred", LoserID: "catwoman", Tag: "B",
			WinnerPreMergeTS: 5, LoserPreMergeTS: 6, MergedAt: a.MergedAt,
		}
		_, perr := es.PutAutoMergeJournalEntry(foreign)
		require.NoError(t, perr)
	}
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, l, "t") // l is soft-deleted: refused
	var refused *merge.RefusedBeforeWriteError
	require.ErrorAs(t, err, &refused, "precondition: merge A was refused before it wrote")
	require.Len(t, keys, 1)

	got, gerr := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, gerr)
	require.False(t, got.Provisional, "B's finalized row is intact")
	require.Zero(t, got.AbortedAt, "A did not close B's row")
	require.Equal(t, "B", got.Tag)
	require.Equal(t, int64(5), got.WinnerPreMergeTS)
	require.Equal(t, int64(6), got.LoserPreMergeTS)
}

// A merge whose key is taken takes a new one and leaves the other row alone. The
// clock is fixed so both rows ask for the same nanosecond, and the second key
// (which the first retry would land on) is taken too.
func TestMergeBooksJournaled_TakenKeyIsNeverOverwrittenAndNewKeyIsChosen(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	const t0 = int64(1_700_000_000_000_000_000)
	engine.journalNow = func() int64 { return t0 } // the clock repeats itself
	var foreignKeys []string
	for _, at := range []int64{t0, t0 + 1} {
		key, err := es.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{
			CandidateID: 7, WinnerID: "alfred", LoserID: "catwoman", Tag: "other", MergedAt: at, WinnerPreMergeTS: 1, LoserPreMergeTS: 2,
		})
		require.NoError(t, err)
		foreignKeys = append(foreignKeys, key)
	}

	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotContains(t, foreignKeys, keys[0], "the merge's row is on a key nobody held")
	for _, fk := range foreignKeys {
		got, gerr := es.GetAutoMergeJournalEntry(fk)
		require.NoError(t, gerr)
		require.Equal(t, "other", got.Tag, "the row that held %s is untouched", fk)
	}
	mine, gerr := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, gerr)
	require.False(t, mine.Provisional, "the merge finalized its own row")
	require.Equal(t, l, mine.LoserID)
	require.Equal(t, t0+2, mine.MergedAt, "each retry moves strictly above the last key tried, even when the clock repeats")
}

// If every key the merge tries is taken it stops, before MergeBooks, and says so.
func TestMergeBooksJournaled_GivesUpWhenEveryKeyIsTaken(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	const t0 = int64(1_700_000_000_000_000_000)
	engine.journalNow = func() int64 { return t0 }
	for i := range int64(provisionalKeyAttempts + 2) {
		_, err := es.PutAutoMergeJournalEntry(database.AutoMergeJournalEntry{CandidateID: 7, Tag: "other", MergedAt: t0 + i})
		require.NoError(t, err)
	}
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.ErrorContains(t, err, "merge skipped")
	require.Empty(t, keys)
	lb, gerr := f.S.GetBookByID(l)
	require.NoError(t, gerr)
	require.False(t, lb.IsSoftDeleted(), "no merge happened")
	for i := range int64(provisionalKeyAttempts + 2) {
		got, gerr := es.GetAutoMergeJournalEntry(fmt.Sprintf("dedup:automerge:%016x", uint64(t0+i)))
		require.NoError(t, gerr)
		require.Equal(t, "other", got.Tag)
	}
}
