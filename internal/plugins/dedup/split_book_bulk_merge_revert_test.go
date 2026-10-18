// file: internal/plugins/dedup/split_book_bulk_merge_revert_test.go
// version: 1.0.0
// guid: 0b7e4c19-5a62-4f38-9d81-e2c6a7f30d95
// last-edited: 2026-10-18

package dedup

import (
	"context"
	"encoding/json"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/dbtest"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// opIDReporter is a reporter that knows its operation id, as the registry's
// does (opsregistry.ReporterOpID).
type opIDReporter struct {
	fakeReporter
	id string
}

func (r *opIDReporter) OpID() string { return r.id }

// dedup.split-book-bulk-merge journals every merge's undo pointer under its
// own operation id, so reverting that one operation (the engine POST
// /operations/:id/revert runs) undoes every merge of the batch.
func TestSplitBookBulkMerge_UndoneByRevertingTheOp(t *testing.T) {
	store := dbtest.NewStore(t)
	type pair struct{ keep, src, srcFile string }
	var pairs []pair
	var items []dedupengine.BulkSplitBookMergeItem
	for i := 0; i < 2; i++ {
		p := pair{keep: ulid.Make().String(), src: ulid.Make().String(), srcFile: ulid.Make().String()}
		for _, id := range []string{p.keep, p.src} {
			_, err := store.CreateBook(&database.Book{ID: id, Title: "Chapter " + id, Format: "mp3", FilePath: "/tmp/bulk-revert/" + id})
			require.NoError(t, err)
		}
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: ulid.Make().String(), BookID: p.keep, FilePath: "/tmp/bulk-revert/" + p.keep + "/01.mp3", Format: "mp3", TrackNumber: 1, Duration: 30}))
		require.NoError(t, store.CreateBookFile(&database.BookFile{ID: p.srcFile, BookID: p.src, FilePath: "/tmp/bulk-revert/" + p.src + "/02.mp3", Format: "mp3", TrackNumber: 1, Duration: 30}))
		pairs = append(pairs, p)
		items = append(items, dedupengine.BulkSplitBookMergeItem{CandidateID: "cand-" + p.keep, BookIDs: []string{p.keep, p.src}, KeepID: p.keep})
	}

	p := New(nil, store, database.NewEmbeddingStore(store.DB()))
	raw, err := json.Marshal(map[string]any{"items": items, "dry_run": false})
	require.NoError(t, err)
	require.NoError(t, p.runSplitBookBulkMerge(context.Background(), raw, &opIDReporter{id: "op-bulk-split"}))

	changes, err := store.GetOperationChanges("op-bulk-split")
	require.NoError(t, err)
	pointers := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeMergeCombineJournal {
			pointers++
		}
	}
	require.Equal(t, 2, pointers, "one pointer per merge, under the op")
	for _, pr := range pairs {
		b, err := store.GetBookByID(pr.src)
		require.NoError(t, err)
		require.True(t, b.IsSoftDeleted(), "merged")
	}

	res, err := audiobooks.NewRevertService(store).RevertOperation("op-bulk-split")
	require.NoError(t, err)
	require.Equal(t, 2, res.RestoredTypes[undo.ChangeTypeMergeCombineJournal])
	for _, pr := range pairs {
		b, err := store.GetBookByID(pr.src)
		require.NoError(t, err)
		require.False(t, b.IsSoftDeleted(), "%s restored", pr.src)
		f, err := store.GetBookFileByID(pr.src, pr.srcFile)
		require.NoError(t, err)
		require.NotNil(t, f, "its file row is back on it")
	}
}
