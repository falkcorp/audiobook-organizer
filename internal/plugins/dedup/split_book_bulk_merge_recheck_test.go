// file: internal/plugins/dedup/split_book_bulk_merge_recheck_test.go
// version: 1.0.1
// guid: 4b9e7c13-a5d2-4e68-9f40-c1d8e3a72b56
// last-edited: 2026-10-21

package dedup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
)

// logReporter is a reporter whose logger writes to buf.
type logReporter struct {
	fakeReporter
	buf *bytes.Buffer
}

func (r *logReporter) Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(r.buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// seedRecheckCluster stores three single-file books in one folder, titled by
// titleOf, each file fileSecs long, and returns their ids (ULID-ascending).
func seedRecheckCluster(t *testing.T, store database.Store, titleOf func(i int) string, fileSecs int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= 3; i++ {
		id := ulid.Make().String()
		ids = append(ids, id)
		path := fmt.Sprintf("/tmp/bulk-recheck/Sample Author/Sample Work/%02d.mp3", i)
		_, err := store.CreateBook(&database.Book{ID: id, Title: titleOf(i), Format: "mp3", FilePath: path})
		require.NoError(t, err)
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: id, FilePath: path, Format: "mp3", TrackNumber: 1, Duration: fileSecs}))
	}
	return ids
}

func chapterTitle(i int) string { return fmt.Sprintf("Sample Work - Chapter %d", i) }

// A stored candidate snapshot that the CURRENT rows no longer support is
// refused by the bulk-merge op: no book is retired, no file moves, and the
// candidate stays stored for review. The titles below carry distinct Book
// numbers (the V2 veto), as the snapshot's did not when it was stored.
func TestSplitBookBulkMerge_RefusesAStoredCandidateThatIsNowVetoed(t *testing.T) {
	store := dbtest.NewStore(t)
	ids := seedRecheckCluster(t, store, func(i int) string { return fmt.Sprintf("Sample Saga Book %d", i) }, 600)
	emb := database.NewEmbeddingStore(store.DB())
	cands := dedupengine.NewSplitBookStore(emb.PebbleDB())
	require.NoError(t, cands.SaveAll([]dedupengine.SplitBookCandidate{{ID: "stale", BookIDs: ids, ParentFolder: "/tmp/bulk-recheck/Sample Author/Sample Work", Shape: "parent", SuggestedTitle: "Sample Saga"}}))

	p := New(nil, store, emb)
	raw, err := json.Marshal(map[string]any{
		"items":   []dedupengine.BulkSplitBookMergeItem{{CandidateID: "stale", BookIDs: ids, KeepID: ids[0], SuggestedTitle: "Sample Saga"}},
		"dry_run": false,
	})
	require.NoError(t, err)
	var logs bytes.Buffer
	require.NoError(t, p.runSplitBookBulkMerge(context.Background(), raw, &logReporter{buf: &logs}))

	for _, id := range ids {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted(), "%s must not be retired", id)
		files, err := store.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1, "%s keeps its own file row", id)
	}
	kept, err := cands.Get("stale")
	require.NoError(t, err)
	require.NotNil(t, kept, "a refused candidate stays stored")
	require.Contains(t, logs.String(), "no longer shown to be chapters")
	require.Contains(t, logs.String(), "V2")
	// The refusal text once: the merge adds no second "split-book merge refused:".
	require.Contains(t, logs.String(), `error="split-book merge refused: the members are not shown to be chapters of one work: V2 distinct Book/Vol numbers [1, 2, 3] in titles"`)
	require.NotContains(t, logs.String(), "refused: split-book merge refused")
}

// The same op merges a stored candidate whose rows still pass, and a file whose
// duration is unknown (0) is held: the Repairs fixer's rule.
func TestSplitBookBulkMerge_MergesOnlyWhatStillPasses(t *testing.T) {
	store := dbtest.NewStore(t)
	good := seedRecheckCluster(t, store, chapterTitle, 600)
	unknown := seedRecheckCluster(t, store, chapterTitle, 0)
	emb := database.NewEmbeddingStore(store.DB())
	p := New(nil, store, emb)
	raw, err := json.Marshal(map[string]any{
		"items": []dedupengine.BulkSplitBookMergeItem{
			{CandidateID: "good", BookIDs: good, KeepID: good[0], SuggestedTitle: "Sample Work"},
			{CandidateID: "unknown", BookIDs: unknown, KeepID: unknown[0], SuggestedTitle: "Sample Work"},
		},
		"dry_run": false,
	})
	require.NoError(t, err)
	require.NoError(t, p.runSplitBookBulkMerge(context.Background(), raw, &opIDReporter{id: "op-recheck"}))

	for _, id := range good[1:] {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "%s merged into the keep", id)
	}
	for _, id := range unknown {
		b, err := store.GetBookByID(id)
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted(), "%s held: its file has no recorded duration", id)
	}
}

// A dry run says what the real run will do: it reports the refusal.
func TestSplitBookBulkMerge_DryRunReportsTheRefusal(t *testing.T) {
	store := dbtest.NewStore(t)
	ids := seedRecheckCluster(t, store, chapterTitle, 4*3600)
	p := New(nil, store, database.NewEmbeddingStore(store.DB()))
	raw, err := json.Marshal(map[string]any{
		"items":   []dedupengine.BulkSplitBookMergeItem{{CandidateID: "long", BookIDs: ids, KeepID: ids[0]}},
		"dry_run": true,
	})
	require.NoError(t, err)
	var logs bytes.Buffer
	require.NoError(t, p.runSplitBookBulkMerge(context.Background(), raw, &logReporter{buf: &logs}))
	require.Contains(t, logs.String(), "the real run would refuse")
	b, err := store.GetBookByID(ids[1])
	require.NoError(t, err)
	require.False(t, b.IsSoftDeleted(), "a dry run writes nothing")
}
