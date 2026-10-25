// file: internal/dedup/unmerge_auto_retired_shell_test.go
// version: 1.1.0
// guid: 3e9b5c71-a204-4d68-b1f7-82c0d6e4a915
// last-edited: 2026-10-25

package dedup

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// lockProbeStore reports, for every RevertBookToVersion, whether another holder
// of the merge lock got in while the call was in flight.
type lockProbeStore struct {
	Store
	inner      database.Store
	calls      atomic.Int32
	rivalInHit atomic.Bool
}

// Unwrap lets database.AsCapability reach the retired-row reader the shell
// check needs (a decorator without it fails closed).
func (s *lockProbeStore) Unwrap() database.Store { return s.inner }

func (s *lockProbeStore) RevertBookToVersion(id string, ts time.Time) (*database.Book, error) {
	s.calls.Add(1)
	var rival atomic.Bool
	go func() {
		merge.LockMergeRMW()
		rival.Store(true)
		merge.UnlockMergeRMW()
	}()
	time.Sleep(150 * time.Millisecond)
	if rival.Load() {
		s.rivalInHit.Store(true)
	}
	return s.Store.RevertBookToVersion(id, ts)
}

// UnmergeAuto's whole-row reverts run under the merge lock a retire holds (review
// S1 of #4144), around the store call only: the rest of UnmergeAuto (the sibling
// undo, the redirect clear, the hand-off) takes the lock itself, so a lock held
// across it would deadlock; this test finishing proves it is not.
func TestUnmergeAuto_HoldsTheMergeLockAroundEachRevert(t *testing.T) {
	engine, store, _ := setupRealStoreEngine(t)
	yes, no := true, false
	seedGrouped(t, store, "K", "H", &yes)
	seedGrouped(t, store, "L", "G", &no)
	seedGrouped(t, store, "LS", "G", &yes)
	_, keys, err := engine.MergeBooksJournaled(0, []string{"L", "K"}, "K", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 1)

	probe := &lockProbeStore{Store: engine.bookStore, inner: store}
	engine.bookStore = probe
	require.NoError(t, engine.UnmergeAuto(keys[0]))
	require.EqualValues(t, 2, probe.calls.Load(), "the loser and the winner are each reverted")
	require.False(t, probe.rivalInHit.Load(), "another merge-lock holder ran while a revert was in flight")
}

// UnmergeAuto refuses, before any write, an entry whose loser has since been
// retired as a shell: the whole-row revert would un-trash it and clear its
// merged-into link while its files stay retired under the winner. The sibling
// undo (its first write) is not run either.
func TestUnmergeAuto_RefusesARetiredShellLoser(t *testing.T) {
	engine, store, es := setupRealStoreEngine(t)
	yes, no := true, false
	seedGrouped(t, store, "K", "H", &yes)
	seedGrouped(t, store, "L", "G", &no)
	seedGrouped(t, store, "LS", "G", &yes)

	_, keys, err := engine.MergeBooksJournaled(0, []string{"L", "K"}, "K", "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 1)

	// The retire repair then moved L's file row onto the winner K.
	ps := store.(*database.PebbleStore)
	require.NoError(t, ps.CreateBookFile(&database.BookFile{ID: "lf", BookID: "L", FilePath: "/lib/L/a.mp3"}))
	at := time.Now().UTC().Truncate(time.Microsecond)
	_, err = ps.CreateBookVersion(&database.BookVersion{ID: "rv-L", BookID: "K", Status: database.BookVersionStatusRetired,
		Source: "retired_book", RetiredFromBookID: "L", RetireOpID: "op-x", RetiredAt: &at})
	require.NoError(t, err)
	require.NoError(t, ps.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{"lf"}, SourceBookID: "L"}}, "K", "rv-L"))

	err = engine.UnmergeAuto(keys[0])
	require.ErrorIs(t, err, database.ErrBookIsRetiredShell)
	require.ErrorContains(t, err, "nothing reverted")

	g, _, deleted := groupAndFlag(t, store, "L")
	require.True(t, deleted, "the loser stays in the trash")
	require.Equal(t, "H", g, "the loser keeps its merged group")
	g, _, _ = groupAndFlag(t, store, "LS")
	require.Equal(t, "H", g, "the sibling undo did not run")
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.Zero(t, entry.UndoneAt, "the entry is not marked undone")
}
