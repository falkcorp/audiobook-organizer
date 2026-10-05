// file: internal/dedup/unmerge_auto_followups_test.go
// version: 1.0.0
// guid: 5d2a9e64-1c7b-4f03-b8e6-0a4f3c9d7b12
// last-edited: 2026-10-05

package dedup

import (
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// vpEngine is an Engine over a vptest fixture store, whose books have real
// files under the library root, so the primary hand-offs can crown them.
func vpEngine(t *testing.T) (*Engine, *vptest.Fixture, *merge.Service, *database.EmbeddingStore) {
	t.Helper()
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	edb, err := pebble.Open(t.TempDir(), &pebble.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = edb.Close() })
	es := database.NewEmbeddingStore(edb)
	ms := merge.NewService(f.S)
	engine, err := NewEngine(es, f.S, nil, nil, ms, unified.DefaultScoreConfig())
	require.NoError(t, err)
	return engine, f, ms, es
}

// S4: two losers from different groups, journaled one entry each. Undoing
// L1's entry puts back only L1's sibling and leaves the journal applied;
// undoing L2's entry puts back L2's and finishes the journal.
func TestUnmergeAuto_TwoLosersInDifferentGroups(t *testing.T) {
	engine, f, ms, es := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l1 := f.Book(t, vptest.Spec{ID: "l1", Group: "G1", Primary: "true"})
	s1 := f.Book(t, vptest.Spec{ID: "s1", Group: "G1", Primary: "false"})
	l2 := f.Book(t, vptest.Spec{ID: "l2", Group: "G2", Primary: "true"})
	s2 := f.Book(t, vptest.Spec{ID: "s2", Group: "G2", Primary: "false"})

	res, keys, err := engine.MergeBooksJournaled(0, []string{l1, l2, k}, k, "dedup:merge-source:test")
	require.NoError(t, err)
	require.Len(t, keys, 2)
	keyOf := map[string]string{}
	for _, key := range keys {
		e, err := es.GetAutoMergeJournalEntry(key)
		require.NoError(t, err)
		keyOf[e.LoserID] = key
		require.Equal(t, res.SiblingJournalID, e.SiblingJournalID)
	}

	require.NoError(t, engine.UnmergeAuto(keyOf[l1]))
	require.Equal(t, "G1", f.GroupOf(t, s1))
	require.Equal(t, "G1", f.GroupOf(t, l1))
	require.Equal(t, "H", f.GroupOf(t, s2), "the other loser's sibling stays")
	j, err := ms.GetSiblingMoveJournal(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, merge.SiblingJournalApplied, j.Status)

	require.NoError(t, engine.UnmergeAuto(keyOf[l2]))
	require.Equal(t, "G2", f.GroupOf(t, s2))
	j, err = ms.GetSiblingMoveJournal(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, merge.SiblingJournalUndone, j.Status)
	f.RequireSinglePrimary(t, "H", k)
	f.RequireSinglePrimary(t, "G1", "")
	f.RequireSinglePrimary(t, "G2", "")
}

// R1 through UnmergeAuto: undo, re-merge, repeat undo of the first entry is
// refused and changes nothing.
func TestUnmergeAuto_RepeatAfterReMergeIsRefused(t *testing.T) {
	engine, f, _, _ := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})

	_, keys1, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)
	require.NoError(t, engine.UnmergeAuto(keys1[0]))
	require.Equal(t, "G", f.GroupOf(t, ls))

	_, _, err = engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)
	require.Equal(t, "H", f.GroupOf(t, ls))
	before := map[string]string{}
	for _, id := range []string{k, l, ls} {
		before[id] = f.GroupOf(t, id) + "/" + f.Flag(t, id)
	}

	err = engine.UnmergeAuto(keys1[0])
	require.ErrorContains(t, err, "already undone")
	for _, id := range []string{k, l, ls} {
		require.Equal(t, before[id], f.GroupOf(t, id)+"/"+f.Flag(t, id), "book %s", id)
	}
}

// S4: when the post-merge patch cannot be written the entry stays
// provisional, and UnmergeAuto refuses it with nothing written, instead of
// reverting a loser while leaving its siblings in the merge's group.
func TestUnmergeAuto_RefusesProvisionalEntry(t *testing.T) {
	engine, f, ms, es := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})
	calls := 0
	engine.journalPut = func(e database.AutoMergeJournalEntry) (string, error) {
		calls++
		if calls > 1 {
			return "", fmt.Errorf("injected patch failure")
		}
		return es.PutAutoMergeJournalEntry(e)
	}
	res, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err, "a failed patch does not fail a completed merge")
	require.Len(t, keys, 1)
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.True(t, entry.Provisional)
	require.Empty(t, entry.Siblings)

	type st struct{ group, flag string }
	snap := func() map[string]st {
		out := map[string]st{}
		for _, id := range []string{k, l, ls} {
			out[id] = st{f.GroupOf(t, id), f.Flag(t, id)}
		}
		return out
	}
	before := snap()
	err = engine.UnmergeAuto(keys[0])
	require.ErrorContains(t, err, "never finalized")
	require.Equal(t, before, snap(), "a refused unmerge writes nothing")

	// The siblings are still reachable through the sibling-move journal.
	_, err = ms.UndoSiblingMove(res.SiblingJournalID)
	require.NoError(t, err)
	require.Equal(t, "G", f.GroupOf(t, ls))
}

// S4: UnmergeAuto hands off the groups it touched even when no sibling moved.
// Here the merge demoted the reused group's primary p (k was named primary);
// reverting k to its pre-merge non-primary row would leave H with none.
func TestUnmergeAuto_HandsOffWithoutSiblings(t *testing.T) {
	engine, f, _, _ := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "false"})
	p := f.Book(t, vptest.Spec{ID: "p", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})

	res, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)
	require.Empty(t, res.SiblingJournalID)
	f.RequireSinglePrimary(t, "H", k)
	require.Equal(t, "false", f.Flag(t, p))

	require.NoError(t, engine.UnmergeAuto(keys[0]))
	f.RequireSinglePrimary(t, "H", "")
	f.RequireSinglePrimary(t, "G", l)
}

// revertFailStore fails RevertBookToVersion for one book until cleared.
type revertFailStore struct {
	Store
	failFor string
}

func (s *revertFailStore) RevertBookToVersion(id string, ts time.Time) (*database.Book, error) {
	if id == s.failFor {
		return nil, fmt.Errorf("injected revert failure")
	}
	return s.Store.RevertBookToVersion(id, ts)
}

// A failed revert leaves the entry retryable: it is not marked undone, and a
// second UnmergeAuto finishes the job.
func TestUnmergeAuto_FailedRevertIsRetryable(t *testing.T) {
	engine, f, _, es := vpEngine(t)
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	_, keys, err := engine.MergeBooksJournaled(0, []string{l, k}, k, "t")
	require.NoError(t, err)

	fs := &revertFailStore{Store: f.S, failFor: l}
	engine.bookStore = fs
	require.ErrorContains(t, engine.UnmergeAuto(keys[0]), "injected revert failure")
	entry, err := es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.Zero(t, entry.UndoneAt)

	fs.failFor = ""
	require.NoError(t, engine.UnmergeAuto(keys[0]))
	require.Equal(t, "G", f.GroupOf(t, l))
	entry, err = es.GetAutoMergeJournalEntry(keys[0])
	require.NoError(t, err)
	require.NotZero(t, entry.UndoneAt)
}
