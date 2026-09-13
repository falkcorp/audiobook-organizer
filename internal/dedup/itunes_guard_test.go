// file: internal/dedup/itunes_guard_test.go
// version: 1.1.0
// guid: 3c9e1f47-2b8a-4d6e-9f15-7a0c4e2b8d61
// last-edited: 2026-09-13

package dedup

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const guardTestMediaRoot = "/srv/media/Audiobooks"

func withGuardMediaRoot(t *testing.T) {
	t.Helper()
	prev := config.Snapshot().ITunes
	config.Mutate(func(c *config.Config) { c.ITunes.MediaRoot = guardTestMediaRoot })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.ITunes = prev }) })
}

// dedupWriteTrap forwards reads to a real store and fails on any write.
type dedupWriteTrap struct {
	database.Store
	t *testing.T
}

func (w *dedupWriteTrap) trip(op string) error {
	w.t.Errorf("%s reached the store after an iTunes refusal", op)
	return fmt.Errorf("write trap: %s", op)
}
func (w *dedupWriteTrap) UpdateBook(string, *database.Book) (*database.Book, error) {
	return nil, w.trip("UpdateBook")
}
func (w *dedupWriteTrap) ModifyBook(string, func(*database.Book) error) (*database.Book, error) {
	return nil, w.trip("ModifyBook")
}
func (w *dedupWriteTrap) DeleteBook(string) error { return w.trip("DeleteBook") }
func (w *dedupWriteTrap) MoveBookFilesToBook([]string, string, string) error {
	return w.trip("MoveBookFilesToBook")
}
func (w *dedupWriteTrap) SetBookAuthors(string, []database.BookAuthor) error {
	return w.trip("SetBookAuthors")
}

func seedGuardPair(t *testing.T, store database.Store) (itunesID, managedID string) {
	t.Helper()
	d := 3600
	for id, path := range map[string]string{
		"ITB": guardTestMediaRoot + "/Author/Book.m4b",
		"MGB": "/srv/managed/Author/Book.m4b",
	} {
		_, err := store.CreateBook(&database.Book{ID: id, Title: "Guard Dup", Format: "m4b", FilePath: path, Duration: &d})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: "F" + id, BookID: id, FilePath: path, Format: "m4b"}))
	}
	return "ITB", "MGB"
}

func requireLive(t *testing.T, store database.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b, "book %s must still exist", id)
		assert.False(t, b.IsSoftDeleted(), "book %s must not be merged away", id)
	}
}

func TestDedupMergeBooks_RefusesITunesBookWithoutWriting(t *testing.T) {
	withGuardMediaRoot(t)
	store := newConcurrentTestStore(t)
	itunesID, managedID := seedGuardPair(t, store)

	_, err := MergeBooks(context.Background(), &dedupWriteTrap{Store: store, t: t}, "", managedID, []string{itunesID}, nil)
	require.ErrorIs(t, err, merge.ErrITunesProtected)
	assert.True(t, merge.IsRefusal(err))
	requireLive(t, store, itunesID, managedID)
}

func TestMergeSplitBookCluster_RefusesITunesBookWithoutWriting(t *testing.T) {
	withGuardMediaRoot(t)
	store := newConcurrentTestStore(t)
	itunesID, managedID := seedGuardPair(t, store)

	res, err := MergeSplitBookCluster(&dedupWriteTrap{Store: store, t: t}, managedID, []string{itunesID}, "")
	require.ErrorIs(t, err, merge.ErrITunesProtected)
	assert.Nil(t, res)
	requireLive(t, store, itunesID, managedID)
}

func TestAutoResolveCertain_CountsITunesRefusal(t *testing.T) {
	withGuardMediaRoot(t)
	engine, store, es := setupRealStoreEngine(t)
	prev := config.AppConfig.Dedup.AutoResolveEnabled
	config.AppConfig.Dedup.AutoResolveEnabled = true
	t.Cleanup(func() { config.AppConfig.Dedup.AutoResolveEnabled = prev })

	itunesID, managedID := seedGuardPair(t, store)
	mustSeed(t, es, itunesID, managedID, unified.BandCertain, unified.SigExactFile, unified.SigISBNASIN)

	res, err := engine.AutoResolveCertain(context.Background(), true, 10, 50)
	require.NoError(t, err)
	require.Equal(t, 1, res.Eligible, "the pair must reach the apply path for this test to mean anything")
	assert.Equal(t, 0, res.Merged)
	assert.Equal(t, 1, res.RefusedITunes)

	entries, err := es.ListAutoMergeJournalEntries(0)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused merge must not leave journal entries")
	pending, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 10})
	require.NoError(t, err)
	assert.Len(t, pending, 1, "the candidate must stay pending, not be marked merged")
	requireLive(t, store, itunesID, managedID)
}

func TestApplyVerdicts_ITunesRefusalMergesNothing(t *testing.T) {
	withGuardMediaRoot(t)
	engine, store, es := setupRealStoreEngine(t)
	prev := config.AppConfig.Dedup.LLMAutoMergeHighConfidence
	config.AppConfig.Dedup.LLMAutoMergeHighConfidence = true
	t.Cleanup(func() { config.AppConfig.Dedup.LLMAutoMergeHighConfidence = prev })

	itunesID, managedID := seedGuardPair(t, store)
	require.NoError(t, es.UpsertCandidate(database.DedupCandidate{
		EntityType: "book", EntityAID: itunesID, EntityBID: managedID,
		Layer: "llm", Status: "pending", FormulaVersion: "test",
	}))
	cands, _, err := es.ListCandidates(database.CandidateFilter{EntityType: "book", Status: "pending", Limit: 10})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	engine.ApplyVerdicts(
		[]ai.DedupPairVerdict{{Index: 0, IsDuplicate: true, Confidence: "high", Reason: "same book"}},
		map[int]database.DedupCandidate{0: cands[0]},
	)

	entries, err := es.ListAutoMergeJournalEntries(0)
	require.NoError(t, err)
	assert.Empty(t, entries, "a refused LLM auto-merge must not leave journal entries")
	requireLive(t, store, itunesID, managedID)
}
