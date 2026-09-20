// file: internal/plugins/maintenance/bookfile_batch_write_test.go
// version: 1.0.0
// guid: 6c1d80fe-4a52-47b3-9d8e-0b2f5a9c7314
// last-edited: 2026-09-19

package maintenance

// The three missing-file ops rewrite many rows of the SAME book. Per-row
// UpdateBookFile recomputed the book's aggregates after every row, each
// recompute re-reading all of its rows — the O(n^2) shape that got
// duration-reextract killed as stuck on 2026-09-19 (#3480). These tests run on
// a real PebbleStore because the per-row recompute lives inside the store,
// where a mock cannot see it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/stretchr/testify/require"
)

// batchTestRows is deliberately larger than 1: with one row per book a per-row
// loop and a batched write are indistinguishable, which is exactly the blind
// fixture this file exists to avoid.
const batchTestRows = 8

// seedBatchBook creates one book with n files, calling path(i) for each row's
// FilePath, and returns the book ID.
func seedBatchBook(t *testing.T, s *database.PebbleStore, n int, path func(i int) string, missing bool, size int64) string {
	t.Helper()
	book, err := s.CreateBook(&database.Book{Title: "Batched", FilePath: "/lib/Batched"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	for i := range n {
		if err := s.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: path(i), FileSize: size, Duration: 60, Missing: missing,
		}); err != nil {
			t.Fatalf("CreateBookFile: %v", err)
		}
	}
	return book.ID
}

// mark-missing-files: one recompute for the whole book, not one per flipped row.
func TestMarkMissingFiles_RecomputesBookOnce(t *testing.T) {
	dir := t.TempDir()
	s := newRepairPebble(t)
	// Bytes present on disk but the rows are flagged Missing: the clear-stale
	// direction, so every row flips.
	bookID := seedBatchBook(t, s, batchTestRows, func(i int) string {
		p := filepath.Join(dir, fmt.Sprintf("part%02d.mp3", i))
		writeFile(t, p, 1000)
		return p
	}, true, 1000)

	logs := aggtest.Capture(t)
	plan, err := planMarkMissingFiles(context.Background(), s, nil,
		markMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, batchTestRows, plan.ClearedStale, "every row must still be written")
	require.Equal(t, 0, plan.UpdateErrs)

	got := aggtest.CountInvocations(logs(), bookID)
	require.LessOrEqual(t, got, 1,
		"one recompute for the book, not one per row (got %d for %d rows)", got, batchTestRows)
}

// missing-file-repoint: one recompute for the whole book, not one per repointed
// row. The fixture reuses the op's own track-slash shape rule: a row pointing at
// ".../Stem - N/35.mp3" is derived to ".../Stem - 0N.mp3".
func TestMissingFileRepoint_RecomputesBookOnce(t *testing.T) {
	dir := t.TempDir()
	s := newRepairPebble(t)
	bookID := seedBatchBook(t, s, batchTestRows, func(i int) string {
		writeFile(t, filepath.Join(dir, fmt.Sprintf("Stem - %02d.mp3", i+1)), 1000)
		return filepath.Join(dir, fmt.Sprintf("Stem - %d", i+1), "35.mp3")
	}, false, 1000)

	logs := aggtest.Capture(t)
	plan, err := planMissingFileRepoint(context.Background(), s, nil,
		missingFileRepointParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, batchTestRows, plan.Repointed, "every row must still be written")
	require.Equal(t, 0, plan.UpdateErrs)

	got := aggtest.CountInvocations(logs(), bookID)
	require.LessOrEqual(t, got, 1,
		"one recompute for the book, not one per row (got %d for %d rows)", got, batchTestRows)
}

// recover-missing-files: one recompute for the whole book, not one per recovered
// row. Each row's size is unique so each matches exactly one unclaimed file.
func TestRecoverMissingFiles_RecomputesBookOnce(t *testing.T) {
	root := t.TempDir()
	s := newRepairPebble(t)
	book, err := s.CreateBook(&database.Book{Title: "Batched", FilePath: "/lib/Batched"})
	require.NoError(t, err)
	for i := range batchTestRows {
		size := 4000 + i // unique per row, so the size match is unambiguous
		writeFile(t, filepath.Join(root, "Author", "Book", fmt.Sprintf("renamed%02d.mp3", i)), size)
		require.NoError(t, s.CreateBookFile(&database.BookFile{
			BookID:   book.ID,
			FilePath: filepath.Join(root, "Author", "Book", fmt.Sprintf("gone%02d.mp3", i)),
			FileSize: int64(size), Duration: 60,
		}))
	}

	logs := aggtest.Capture(t)
	plan, err := planRecoverMissingFiles(context.Background(), s, nil, root,
		recoverMissingParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)
	require.Equal(t, batchTestRows, plan.Repointed, "every row must still be written")
	require.Equal(t, 0, plan.UpdateErrs)

	got := aggtest.CountInvocations(logs(), book.ID)
	require.LessOrEqual(t, got, 1,
		"one recompute for the book, not one per row (got %d for %d rows)", got, batchTestRows)
}

// A cancelled context stops the batch: the rows already written stay written,
// the rest are never attempted, and the outcome says so. This is the behaviour
// the ops rely on to abort mid-book when the scan stand-down lease lapses.
func TestWriteBookFileBatch_CancelStopsMidBook(t *testing.T) {
	dir := t.TempDir()
	s := newRepairPebble(t)
	bookID := seedBatchBook(t, s, batchTestRows, func(i int) string {
		return filepath.Join(dir, fmt.Sprintf("part%02d.mp3", i))
	}, false, 1000)
	stored, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	require.Len(t, stored, batchTestRows)

	rows := make([]*database.BookFile, len(stored))
	for i := range stored {
		stored[i].FilePath = filepath.Join(dir, fmt.Sprintf("moved%02d.mp3", i))
		rows[i] = &stored[i]
	}

	seen := 0
	out := writeBookFileBatch(context.Background(), s, rows, bookFileBatchOpts{
		OnRow:    func(int, bool) { seen++ },
		Abort:    func() bool { return seen >= 3 },
		Reporter: &fakeReporter{},
	})
	require.True(t, out.Cancelled, "the batch must report that it stopped early")
	require.Equal(t, 3, out.Applied, "only the rows written before the abort")
	require.Equal(t, 0, out.RowErrs)
	require.Equal(t, 3, seen)

	// The rows that were written are really written; the rest are untouched.
	after, err := s.GetBookFiles(bookID)
	require.NoError(t, err)
	moved := 0
	for _, f := range after {
		if filepath.Base(f.FilePath) != "" && len(f.FilePath) > 0 &&
			filepath.Base(f.FilePath)[:5] == "moved" {
			moved++
		}
	}
	require.Equal(t, 3, moved, "a cancelled batch must not roll back committed rows")
}

// An abort with nothing written yet is a no-op, not a panic on an empty slice.
func TestWriteBookFileBatch_EmptyRows(t *testing.T) {
	s := newRepairPebble(t)
	out := writeBookFileBatch(context.Background(), s, nil, bookFileBatchOpts{})
	require.Equal(t, bookFileWriteOutcome{}, out)
}

// classifyBookFileWrites must keep a post-commit aggregate failure OUT of the
// row-error count: those rows are written, and reporting them as failed writes
// would send a person to re-run work that is already correct.
func TestClassifyBookFileWrites_SeparatesRecomputeFromRowErrors(t *testing.T) {
	err := errors.Join(
		fmt.Errorf("book file f1: %w", os.ErrPermission),
		fmt.Errorf("%w for book b1: %w", database.ErrBookAggregatesRecompute, os.ErrPermission),
		context.Canceled,
	)
	out := classifyBookFileWrites(5, err)
	require.Equal(t, 5, out.Applied)
	require.Equal(t, 1, out.RowErrs)
	require.Equal(t, 1, out.RecomputeErrs)
	require.True(t, out.Cancelled)
}

// groupItemsByBook must be deterministic: the ops sort their flat work list by
// file ID so a capped re-run takes a stable prefix, and map iteration order
// would have thrown that away.
func TestGroupItemsByBook_DeterministicOrder(t *testing.T) {
	type row struct{ book, file string }
	items := []row{{"b2", "f1"}, {"b1", "f2"}, {"b2", "f3"}, {"b1", "f4"}}
	groups := groupItemsByBook(items,
		func(r row) string { return r.book },
		func(r row) string { return r.file })
	require.Len(t, groups, 2)
	require.Equal(t, "b2", groups[0].BookID, "b2 owns the lowest file ID")
	require.Equal(t, []row{{"b2", "f1"}, {"b2", "f3"}}, groups[0].Items)
	require.Equal(t, "b1", groups[1].BookID)
	require.Equal(t, []row{{"b1", "f2"}, {"b1", "f4"}}, groups[1].Items)
}
