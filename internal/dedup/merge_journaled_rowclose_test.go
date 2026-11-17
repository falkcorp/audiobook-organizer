// file: internal/dedup/merge_journaled_rowclose_test.go
// version: 1.0.0
// guid: 3e6b9d24-8a51-4c7f-b0d2-9f4a1c7e5b68
// last-edited: 2026-11-17

package dedup

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// failUpdateStore fails every ModifyBook once armed: the shape of a store fault
// after MergeBooks has taken its lock and started writing.
type failUpdateStore struct {
	*database.PebbleStore
	armed bool
}

func (f *failUpdateStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if f.armed {
		return nil, errors.New("injected: write failed after the merge began")
	}
	return f.PebbleStore.ModifyBook(id, fn)
}

func engineOn(t *testing.T, f *vptest.Fixture, svcStore database.Store) (*Engine, *database.EmbeddingStore) {
	t.Helper()
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	es := database.NewEmbeddingStoreFromStore(f.S)
	engine, err := NewEngine(es, f.S, nil, nil, merge.NewService(svcStore), unified.DefaultScoreConfig())
	require.NoError(t, err)
	return engine, es
}

func pinnedAfterPrune(t *testing.T, s *database.PebbleStore, id string, keep int) []int64 {
	t.Helper()
	_, err := s.PruneBookSnapshots(id, keep)
	require.NoError(t, err)
	snaps, err := s.GetBookSnapshots(id, 0)
	require.NoError(t, err)
	var out []int64
	for _, sn := range snaps {
		out = append(out, sn.Timestamp.UnixNano())
	}
	return out
}

// A merge refused by MergeBooks' read-only phase wrote nothing, so the
// provisional rows written for it are CLOSED, which releases their pins and makes
// the undo say why it will not run. Forcing a soft-deleted book as the survivor is
// such a refusal.
func TestMergeBooksJournaled_RefusalBeforeAnyWriteClosesTheProvisionalRow(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	_, first, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)
	firstEntry, err := es.GetAutoMergeJournalEntry(first[0])
	require.NoError(t, err)

	editTitle(t, f.S, l, 3)                                               // the soft-deleted loser has snapshots a stale row could pin
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, l, "t") // l is soft-deleted: refused
	require.Error(t, err)
	var se *merge.SoftDeletedInputError
	require.ErrorAs(t, err, &se)
	require.Len(t, keys, 1)

	e, gerr := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, gerr)
	require.NotZero(t, e.AbortedAt, "the row for a merge that never wrote is closed")
	require.True(t, e.Provisional, "it stays provisional so a binary that predates AbortedAt stays conservative")
	require.Zero(t, e.UndoneAt)

	require.ErrorContains(t, engine.UnmergeAuto(keys[0]), "closed")

	// The finished first merge pins the loser's pre-merge snapshot; the closed row
	// adds nothing to it: what survives is that one and the newest.
	snaps := pinnedAfterPrune(t, f.S, l, 1)
	require.Len(t, snaps, 2, "the first merge's pin and the newest snapshot, and nothing for the closed row")
	require.Contains(t, snaps, firstEntry.LoserPreMergeTS)
}

// A failure that does NOT prove the merge wrote nothing keeps its row provisional
// and its pin: the merge may have written, and the one snapshot per book is what
// an operator needs to repair it by hand.
func TestMergeBooksJournaled_AmbiguousFailureKeepsTheProvisionalRowAndItsPin(t *testing.T) {
	f := vptest.New(t)
	fs := &failUpdateStore{PebbleStore: f.S}
	engine, es := engineOn(t, f, fs)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	editTitle(t, f.S, k, 2)
	fs.armed = true

	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.Error(t, err)
	require.ErrorContains(t, err, "injected")
	var refused *merge.RefusedBeforeWriteError
	require.False(t, errors.As(err, &refused), "this failure is after the merge began: it proves nothing about writes")
	require.Len(t, keys, 1)

	e, gerr := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, gerr)
	require.True(t, e.Provisional)
	require.Zero(t, e.AbortedAt, "an ambiguous failure must not close the row")

	fs.armed = false
	// Some write of the merge's (or this test's) lands above the baseline; the row pins the first one.
	editTitle(t, f.S, k, 3)
	want := engine.preMergeSnapshotNanos(k, e.WinnerBaselineTS)
	require.NotZero(t, want)
	require.Contains(t, pinnedAfterPrune(t, f.S, k, 1), want, "the kept row still pins the snapshot the patch would have named")
}

// A write failure of the PROVISIONAL row itself (the second of two) closes the
// first, which describes a merge that never started.
func TestMergeBooksJournaled_FailedSecondProvisionalWriteClosesTheFirst(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l1 := f.Book(t, vptest.Spec{ID: "robin", Group: "G1", Primary: "true"})
	l2 := f.Book(t, vptest.Spec{ID: "nightwing", Group: "G2", Primary: "true"})
	calls := 0
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		calls++
		if calls == 2 {
			return "", fmt.Errorf("injected: second provisional write")
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	_, keys, err := engine.MergeBooksJournaled(0, []string{l1, l2, k}, k, "t")
	require.ErrorContains(t, err, "merge skipped")
	require.Len(t, keys, 1)
	e, gerr := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, gerr)
	require.NotZero(t, e.AbortedAt)
	live, serr := f.S.GetBookByID(l1)
	require.NoError(t, serr)
	require.False(t, live.IsSoftDeleted(), "the merge never started")
}

// The undo reads both pre-merge snapshots before writing anything. A loser or a
// winner that is missing, or a read that fails, refuses the whole undo.
func TestUnmergeAuto_RefusesBeforeAnyWriteWhenABookIsMissingOrUnreadable(t *testing.T) {
	setup := func(t *testing.T) (*Engine, *vptest.Fixture, string, string, string) {
		engine, f, es := pinEngine(t)
		k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
		l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
		return engine, f, k, l, mergedPair(t, engine, es, l, k).Key
	}
	t.Run("loser missing", func(t *testing.T) {
		engine, f, k, l, key := setup(t)
		require.NoError(t, f.S.DeleteRaw("book:"+l))
		err := engine.UnmergeAuto(key)
		require.ErrorContains(t, err, "loser "+l+" no longer exists")
		require.ErrorContains(t, err, "nothing reverted")
		w, gerr := f.S.GetBookByID(k)
		require.NoError(t, gerr)
		require.False(t, w.IsSoftDeleted())
	})
	t.Run("winner missing", func(t *testing.T) {
		engine, f, k, l, key := setup(t)
		require.NoError(t, f.S.DeleteRaw("book:"+k))
		err := engine.UnmergeAuto(key)
		require.ErrorContains(t, err, "winner "+k+" no longer exists")
		lb, gerr := f.S.GetBookByID(l)
		require.NoError(t, gerr)
		require.True(t, lb.IsSoftDeleted(), "the loser stays in the trash")
	})
	t.Run("both missing", func(t *testing.T) {
		engine, f, k, l, key := setup(t)
		require.NoError(t, f.S.DeleteRaw("book:"+l))
		require.NoError(t, f.S.DeleteRaw("book:"+k))
		err := engine.UnmergeAuto(key)
		require.ErrorContains(t, err, "nothing reverted")
		require.ErrorContains(t, err, "loser "+l, "the first missing book is named")
	})
	t.Run("read error", func(t *testing.T) {
		engine, f, k, l, key := setup(t)
		engine.bookStore = &failGetBookStore{Store: f.S, failID: k}
		err := engine.UnmergeAuto(key)
		require.ErrorContains(t, err, "read winner "+k)
		require.ErrorContains(t, err, "injected")
		require.ErrorContains(t, err, "nothing reverted")
		lb, gerr := f.S.GetBookByID(l)
		require.NoError(t, gerr)
		require.True(t, lb.IsSoftDeleted())
	})
}

type failGetBookStore struct {
	database.Store
	failID string
}

// Unwrap lets database.AsCapability see through to the Pebble store, as every
// production decorator does.
func (f *failGetBookStore) Unwrap() database.Store { return f.Store }

func (f *failGetBookStore) GetBookByID(id string) (*database.Book, error) {
	if id == f.failID {
		return nil, errors.New("injected: sstable block not found")
	}
	return f.Store.GetBookByID(id)
}

// If closing the row fails, the row simply stays provisional: it keeps its pin
// (safe) and UnmergeAuto refuses it. The merge's own refusal is still returned.
func TestMergeBooksJournaled_FailureToCloseARefusedRowLeavesItProvisional(t *testing.T) {
	f := vptest.New(t)
	engine, es := engineOn(t, f, f.S)
	k := f.Book(t, vptest.Spec{ID: "batman", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "robin", Group: "G", Primary: "true"})
	_, _, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)

	calls := 0
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		calls++
		if calls > 1 { // the closing write
			return "", errors.New("injected: close failed")
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, l, "t")
	var se *merge.SoftDeletedInputError
	require.ErrorAs(t, err, &se, "the merge's refusal is what the caller sees")
	require.Len(t, keys, 1)
	e, gerr := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, gerr)
	require.True(t, e.Provisional)
	require.Zero(t, e.AbortedAt)
	require.ErrorContains(t, engine.UnmergeAuto(keys[0]), "never finalized")
}
