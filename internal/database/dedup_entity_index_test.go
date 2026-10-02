// file: internal/database/dedup_entity_index_test.go
// version: 1.1.0
// guid: 42d58947-e661-49b8-bdef-32b171b2b68d
// last-edited: 2026-10-01

package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// legacyCandidate writes a candidate the way rows stored before 2026-06-22
// look: the record, pair and status rows, and NO "dedup:e:" rows.
func legacyCandidate(t *testing.T, s *EmbeddingStore, a, b, status string) int64 {
	t.Helper()
	id, _, err := s.UpsertCandidateNew(DedupCandidate{EntityType: "book", EntityAID: a, EntityBID: b, Layer: "embedding", Status: status})
	require.NoError(t, err)
	require.NoError(t, s.db.Delete(dedupEntityKey("book", a, id), pebble.Sync))
	require.NoError(t, s.db.Delete(dedupEntityKey("book", b, id), pebble.Sync))
	got, err := s.ListCandidatesForEntityStrict("book", a, "")
	require.NoError(t, err)
	require.Empty(t, got, "the legacy row is invisible by entity")
	return id
}

// TestEntityIndex_StatusChangeIndexesALegacyCandidate: a verdict recorded on
// a pre-index candidate is found by entity afterwards.
func TestEntityIndex_StatusChangeIndexesALegacyCandidate(t *testing.T) {
	s := newTestEmbeddingStore(t)
	id := legacyCandidate(t, s, "bA", "bB", "pending")
	require.NoError(t, s.UpdateCandidateStatus(id, "dismissed"))
	for _, side := range []string{"bA", "bB"} {
		got, err := s.ListCandidatesForEntityStrict("book", side, "")
		require.NoError(t, err)
		require.Len(t, got, 1, side)
		require.Equal(t, "dismissed", got[0].Status)
	}
}

// TestEntityIndex_MarkMergedIndexesALegacyCandidate: the bulk merged writer
// does the same.
func TestEntityIndex_MarkMergedIndexesALegacyCandidate(t *testing.T) {
	s := newTestEmbeddingStore(t)
	legacyCandidate(t, s, "bA", "bB", "pending")
	n, err := s.MarkCandidatesAsMergedForEntity("book", "bA")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := s.ListCandidatesForEntityStrict("book", "bB", "merged")
	require.NoError(t, err)
	require.Len(t, got, 1)
}

// TestMigration064_BackfillsBothEntityIndexes: a legacy candidate no write
// touched since, and a label stored before the label index, are both found
// by entity after migration 64; a corrupt label is counted, not fatal.
func TestMigration064_BackfillsBothEntityIndexes(t *testing.T) {
	ps, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	s := NewEmbeddingStore(ps.DB())
	legacyCandidate(t, s, "bA", "bB", "dismissed")
	require.NoError(t, s.UpsertLabeledExample(LabeledExample{CandidateID: 7, EntityAID: "bC", EntityBID: "bD", Label: "not_dup"}))
	require.NoError(t, s.db.Delete(dedupLabelEntityKey("bC", 7), pebble.Sync))
	require.NoError(t, s.db.Delete(dedupLabelEntityKey("bD", 7), pebble.Sync))
	require.NoError(t, s.db.Set(dedupLabelKey(9), []byte("{corrupt"), pebble.Sync))
	labels, err := s.ListLabeledExamplesForEntitiesStrict([]string{"bC"}, LabeledExampleFilter{})
	require.NoError(t, err)
	require.Empty(t, labels, "the legacy label is invisible by entity")

	require.NoError(t, migration064Up(ps))

	got, err := s.ListCandidatesForEntityStrict("book", "bA", "dismissed")
	require.NoError(t, err)
	require.Len(t, got, 1)
	labels, err = s.ListLabeledExamplesForEntitiesStrict([]string{"bD"}, LabeledExampleFilter{Label: "not_dup"})
	require.NoError(t, err)
	require.Len(t, labels, 1)
	cands, lbl, err := ps.BackfillDedupEntityIndexes()
	require.NoError(t, err)
	require.Equal(t, 1, cands.Indexed)
	require.Equal(t, LabelIndexBackfill{Indexed: 1, Unreadable: 1}, lbl)
}

// TestLabelEntityIndex_ReadsOnlyTheBooksAsked: the per-book read finds a
// label from either side, once; it is not failed by a corrupt label of an
// unrelated book (the whole-keyspace strict read is); it follows an
// overwrite that changes the pair and a delete; a corrupt label indexed
// under the book does fail it.
func TestLabelEntityIndex_ReadsOnlyTheBooksAsked(t *testing.T) {
	s := newTestEmbeddingStore(t)
	require.NoError(t, s.UpsertLabeledExample(LabeledExample{CandidateID: 1, EntityAID: "bA", EntityBID: "bB", Label: "not_dup"}))
	require.NoError(t, s.UpsertLabeledExample(LabeledExample{CandidateID: 2, EntityAID: "bA", EntityBID: "bC", Label: "true_dup"}))
	require.NoError(t, s.db.Set(dedupLabelKey(3), []byte("{corrupt"), pebble.Sync))

	_, err := s.ListLabeledExamplesStrict(LabeledExampleFilter{})
	require.Error(t, err, "the whole-keyspace read still fails on a corrupt label")

	got, err := s.ListLabeledExamplesForEntitiesStrict([]string{"bA", "bB"}, LabeledExampleFilter{Label: "not_dup"})
	require.NoError(t, err)
	require.Len(t, got, 1, "found from both sides, returned once")
	require.Equal(t, int64(1), got[0].CandidateID)

	require.NoError(t, s.UpsertLabeledExample(LabeledExample{CandidateID: 1, EntityAID: "bX", EntityBID: "bY", Label: "not_dup"}))
	got, err = s.ListLabeledExamplesForEntitiesStrict([]string{"bB"}, LabeledExampleFilter{})
	require.NoError(t, err)
	require.Empty(t, got, "the overwrite moved the label off bB")
	got, err = s.ListLabeledExamplesForEntitiesStrict([]string{"bY"}, LabeledExampleFilter{})
	require.NoError(t, err)
	require.Len(t, got, 1)

	require.NoError(t, s.DeleteLabeledExample(1))
	got, err = s.ListLabeledExamplesForEntitiesStrict([]string{"bX", "bY"}, LabeledExampleFilter{})
	require.NoError(t, err)
	require.Empty(t, got)
	_, closer, err := s.db.Get(dedupLabelEntityKey("bX", 1))
	require.ErrorIs(t, err, pebble.ErrNotFound, "the delete dropped its index rows")
	if closer != nil {
		_ = closer.Close()
	}

	n, err := s.DeleteLabeledExamplesBySource("")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, closer, err = s.db.Get(dedupLabelEntityKey("bC", 2))
	require.ErrorIs(t, err, pebble.ErrNotFound, "a by-source delete drops its index rows")
	if closer != nil {
		_ = closer.Close()
	}

	require.NoError(t, s.db.Set(dedupLabelEntityKey("bA", 3), nil, pebble.Sync))
	_, err = s.ListLabeledExamplesForEntitiesStrict([]string{"bA"}, LabeledExampleFilter{})
	require.Error(t, err, "a corrupt label indexed under the book may be a verdict on it")
}

// recordingLog captures Info and Warn lines.
type recordingLog struct {
	logger.Logger
	mu          sync.Mutex
	infos, warn []string
}

func (r *recordingLog) Info(msg string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.infos = append(r.infos, fmt.Sprintf(msg, args...))
}

func (r *recordingLog) Warn(msg string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warn = append(r.warn, fmt.Sprintf(msg, args...))
}

// TestMigration064_LogsProgressAndWarnsOnUnreadable: a long backfill reports
// progress every dedupBackfillProgressEvery batches, so boot is not silent,
// and unreadable rows (which could not be indexed) are a Warn, not buried in
// an Info line's counters.
func TestMigration064_LogsProgressAndWarnsOnUnreadable(t *testing.T) {
	ps, err := NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	s := NewEmbeddingStore(ps.DB())
	b := s.db.NewBatch()
	for i := 1; i <= labelBackfillBatch+1; i++ {
		v, err := json.Marshal(LabeledExample{CandidateID: int64(i), EntityAID: "bA", EntityBID: fmt.Sprintf("b%d", i), Label: "not_dup"})
		require.NoError(t, err)
		require.NoError(t, b.Set(dedupLabelKey(int64(i)), v, nil))
	}
	require.NoError(t, b.Set(dedupLabelKey(int64(labelBackfillBatch+5)), []byte("{corrupt"), nil))
	require.NoError(t, b.Commit(pebble.Sync))
	_ = b.Close()

	rec := &recordingLog{Logger: logger.New("test")}
	prevLog, prevEvery := dedupBackfillLog, dedupBackfillProgressEvery
	dedupBackfillLog, dedupBackfillProgressEvery = rec, 1
	t.Cleanup(func() { dedupBackfillLog, dedupBackfillProgressEvery = prevLog, prevEvery })

	require.NoError(t, migration064Up(ps))

	var progress int
	for _, l := range rec.infos {
		if strings.Contains(l, "labels backfill in progress") {
			progress++
			require.Contains(t, l, fmt.Sprintf("%d indexed", labelBackfillBatch))
		}
	}
	require.Equal(t, 1, progress, "one full batch, one progress line: %q", rec.infos)
	require.Len(t, rec.warn, 1, "%q", rec.warn)
	require.Contains(t, rec.warn[0], "1 labeled examples are unreadable")
}

// TestLabelEntityIndex_SkipsAStaleRowForTheOldBook: an index row left behind
// under a book the label no longer names (written by a path that did not
// maintain the index) is skipped, not returned as a verdict on that book.
func TestLabelEntityIndex_SkipsAStaleRowForTheOldBook(t *testing.T) {
	s := newTestEmbeddingStore(t)
	require.NoError(t, s.UpsertLabeledExample(LabeledExample{CandidateID: 1, EntityAID: "bA", EntityBID: "bB", Label: "not_dup"}))
	v, err := json.Marshal(LabeledExample{CandidateID: 1, EntityAID: "bX", EntityBID: "bY", Label: "not_dup"})
	require.NoError(t, err)
	require.NoError(t, s.db.Set(dedupLabelKey(1), v, pebble.Sync)) // bypasses index maintenance
	_, closer, err := s.db.Get(dedupLabelEntityKey("bA", 1))
	require.NoError(t, err, "the stale index row is still there")
	_ = closer.Close()

	got, err := s.ListLabeledExamplesForEntitiesStrict([]string{"bA"}, LabeledExampleFilter{})
	require.NoError(t, err)
	require.Empty(t, got, "the label names bX/bY now, not bA")
}
