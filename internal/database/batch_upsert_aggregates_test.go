// file: internal/database/batch_upsert_aggregates_test.go
// version: 1.2.0
// guid: 9d41f6b2-70e8-4c35-b1a7-38f0c92e64d5
// last-edited: 2026-08-24

package database

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureAggregateLogs redirects the default logger into a buffer for the
// duration of one test and returns a reader for it.
//
// Counting log lines is the point, not a convenience. RecomputeBookAggregates is
// reached through notifyBookFileChange, which is unexported and has no return
// value, so there is no seam to inject a counter into. Its log lines are the only
// observable record of how many times it ran.
//
// A test that only checked the final Duration/FileSize would pass whether the
// recompute ran once or once per row — and "once per row" is precisely the O(N^2)
// this exists to prevent. The final values cannot distinguish those two cases.
//
// The handler is pinned at LevelDebug because that distinction lives entirely in
// the Debug-level "no change needed" line: a redundant recompute produces no
// Info-level output at all. At the default level this capture would be blind to
// exactly the regression it exists to detect. See countAggregateInvocations.
//
// slog.SetDefault is process-global, so a test using this must NOT call
// t.Parallel.
func captureAggregateLogs(t *testing.T) func() string {
	t.Helper()

	var mu sync.Mutex
	var buf bytes.Buffer

	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(
		&syncWriter{mu: &mu, buf: &buf},
		&slog.HandlerOptions{Level: slog.LevelDebug},
	)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// syncWriter serialises writes from the logger, which RecomputeBookAggregates may
// reach from more than one goroutine in other suites.
type syncWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// countAggregateInvocations returns how many times RecomputeBookAggregates RAN
// for bookID, whether or not it changed anything.
//
// Invocations, not writes, are the quantity that matters, and the difference is
// the whole point of this helper. Every invocation calls GetBookFiles and reads
// the book's ENTIRE file set; that read is the O(N^2) cost. But only the first
// invocation of a run finds anything to change — the rest compute the same sums,
// hit the "no change needed" early return, and never emit the "updated" line.
//
// Counting "updated" therefore reports 1 whether the recompute ran once or once
// per row. A mutant that dropped the per-book de-duplication and recomputed N
// times passed a version of this suite that counted writes. Every message this
// function matches begins with "RecomputeBookAggregates", so matching the prefix
// catches the updated / no-change / not-found / failed variants alike.
func countAggregateInvocations(logs, bookID string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "RecomputeBookAggregates") &&
			strings.Contains(line, "book_id="+bookID) {
			n++
		}
	}
	return n
}

// countAggregateWrites returns how many times an aggregate WRITE was logged for
// bookID — i.e. how often the sums actually changed. Distinct from
// countAggregateInvocations; see the note there before using either.
func countAggregateWrites(logs, bookID string) int {
	n := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "RecomputeBookAggregates updated") &&
			strings.Contains(line, "book_id="+bookID) {
			n++
		}
	}
	return n
}

// sumStoredFileAggregates reads back what the store actually holds for bookID.
//
// The expected totals are derived from the STORED rows rather than from the
// values handed to BatchUpsertBookFiles, because the batch path runs each file
// through normalizeBookFileDuration (CONS-18) and may rewrite a duration on the
// way in. Comparing the book against its own files is also the invariant that
// actually matters: the parent's aggregates must equal the sum of its children.
func sumStoredFileAggregates(t *testing.T, store Store, bookID string) (int, int64) {
	t.Helper()
	files, err := store.GetBookFiles(bookID)
	require.NoError(t, err)

	var dur int
	var size int64
	for _, f := range files {
		dur += f.Duration
		size += f.FileSize
	}
	return dur, size
}

// TestBatchUpsertBookFilesRecomputesAggregatesOncePerBook pins both halves of the
// bug this method had.
//
// BEFORE: BatchUpsertBookFiles called notifyBookFileChange nowhere, so a batch
// write left Book.Duration and Book.FileSize exactly as they were — nil on a
// freshly created book. Every other BookFile mutator recomputed; this one did
// not, and nothing on the write path reported it. On the old code the aggregate
// assertions below fail against a nil Duration.
//
// AFTER: exactly ONE recompute per affected book, regardless of how many rows the
// batch carries. The per-book write count is asserted to be 1 rather than merely
// non-zero, which is what separates the fix from the per-row loop it replaces.
func TestBatchUpsertBookFilesRecomputesAggregatesOncePerBook(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	readLogs := captureAggregateLogs(t)

	mkBook := func(title, path string) string {
		b, err := store.CreateBook(&Book{Title: title, FilePath: path})
		require.NoError(t, err)
		require.Nil(t, b.Duration, "a freshly created book has no aggregate yet")
		return b.ID
	}

	// Two books of DIFFERENT file counts, so a per-book total cannot
	// coincidentally match the other book's.
	bookA := mkBook("Batch Book A", "/lib/batch/a")
	bookB := mkBook("Batch Book B", "/lib/batch/b")

	files := []*BookFile{}
	for i := range 5 {
		files = append(files, &BookFile{
			BookID:      bookA,
			FilePath:    "/lib/batch/a/track" + string(rune('1'+i)) + ".m4b",
			TrackNumber: i + 1,
			Duration:    600 + i,
			FileSize:    int64(20_000_000 + i),
		})
	}
	for i := range 3 {
		files = append(files, &BookFile{
			BookID:      bookB,
			FilePath:    "/lib/batch/b/track" + string(rune('1'+i)) + ".m4b",
			TrackNumber: i + 1,
			Duration:    900 + i,
			FileSize:    int64(30_000_000 + i),
		})
	}

	require.NoError(t, store.BatchUpsertBookFiles(files))

	// --- the correctness half: the parent now matches the sum of its children ---
	for _, tc := range []struct {
		name   string
		bookID string
		want   int
	}{
		{"book A", bookA, 5},
		{"book B", bookB, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantDur, wantSize := sumStoredFileAggregates(t, store, tc.bookID)
			require.Positive(t, wantDur, "fixture must produce a non-zero duration or the assertion is vacuous")

			b, err := store.GetBookByID(tc.bookID)
			require.NoError(t, err)
			require.NotNil(t, b)
			require.NotNil(t, b.Duration,
				"Duration is still nil — BatchUpsertBookFiles did not recompute aggregates")
			require.NotNil(t, b.FileSize)

			require.Equal(t, wantDur, *b.Duration,
				"book Duration must equal the sum of its stored files")
			require.Equal(t, wantSize, *b.FileSize,
				"book FileSize must equal the sum of its stored files")

			// --- the coalescing half: ONE recompute, not one per row ---
			//
			// Asserted on INVOCATIONS, not writes. Only the first recompute of a
			// batch finds anything to change, so a write count reads 1 even when
			// the recompute ran once per row — which is the exact regression this
			// assertion exists to catch.
			logs := readLogs()
			require.Equal(t, 1, countAggregateInvocations(logs, tc.bookID),
				"expected exactly ONE recompute for this book across a %d-row batch; "+
					"more means it is running per row again, and each run re-reads "+
					"every file of the book", tc.want)
			require.Equal(t, 1, countAggregateWrites(logs, tc.bookID),
				"the single recompute should have written the aggregates exactly once")
		})
	}
}

// TestDeletingEveryFileKeepsTheBookDuration pins what the partial-data rule
// actually does when a book loses all of its files, because the comment on
// DeleteBookFilesForBook claimed the opposite.
//
// That comment read: "The book likely has Duration=0 after deletion, which is
// correct — nothing to sum." But RecomputeBookAggregates' partial-data rule fires
// on exactly this input: when no file carries a duration and the book already has
// a non-zero one, the old value is PRESERVED and a warning is logged, precisely so
// a transient missing-file event cannot destroy hard-won duration data.
//
// So deleting every file leaves Duration untouched, not zeroed. Both statements
// cannot be true; this test settles which one the code implements. It is written
// against the shared recompute path rather than the delete method's own wording so
// it keeps holding if that call site moves.
func TestDeletingEveryFileKeepsTheBookDuration(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Loses Its Files", FilePath: "/lib/loses"})
	require.NoError(t, err)

	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:   book.ID,
		FilePath: "/lib/loses/track1.m4b",
		Duration: 1234,
		FileSize: 5_000_000,
	}))

	withFiles, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, withFiles.Duration)
	require.Equal(t, 1234, *withFiles.Duration)

	require.NoError(t, store.DeleteBookFilesForBook(book.ID))

	remaining, err := store.GetBookFiles(book.ID)
	require.NoError(t, err)
	require.Empty(t, remaining, "every file should be gone")

	after, err := store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, after.Duration,
		"the partial-data rule preserves a populated Duration when no file carries one")
	require.Equal(t, 1234, *after.Duration,
		"deleting every file must NOT zero the book's Duration — the partial-data "+
			"rule deliberately keeps the last known good value")
}

// TestBatchUpsertBookFilesAttributesTheAffectedBookNotTheRequestedOne guards the
// subtlety that decides WHERE the book id is collected.
//
// BatchUpsertBookFiles matches an existing row by iTunes PID or by FilePath and
// then adopts that row's identity, including its BookID (pebble_store_bookfiles.go
// reassigns file.BookID from the stored row). So when a caller submits a file
// under book B whose path is already owned by book A, the write lands on A.
//
// The recompute therefore has to follow the row, not the request. Collecting the
// id before the match branch would recompute B — which did not change — and leave
// A, which did, stale. That failure is invisible to any test that only submits
// non-colliding rows, which is why this case is written out explicitly.
func TestBatchUpsertBookFilesAttributesTheAffectedBookNotTheRequestedOne(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	owner, err := store.CreateBook(&Book{Title: "Owner", FilePath: "/lib/own"})
	require.NoError(t, err)
	other, err := store.CreateBook(&Book{Title: "Other", FilePath: "/lib/other"})
	require.NoError(t, err)

	const sharedPath = "/lib/own/track1.m4b"

	// The row is created under `owner`.
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:   owner.ID,
		FilePath: sharedPath,
		Duration: 100,
		FileSize: 1_000_000,
	}))

	readLogs := captureAggregateLogs(t)

	// The same path is now submitted under `other`. The match-by-path branch
	// retargets it back to `owner`.
	require.NoError(t, store.BatchUpsertBookFiles([]*BookFile{{
		BookID:   other.ID,
		FilePath: sharedPath,
		Duration: 250,
		FileSize: 2_000_000,
	}}))

	ownerFiles, err := store.GetBookFiles(owner.ID)
	require.NoError(t, err)
	require.Len(t, ownerFiles, 1, "the upsert must update the existing row, not add a second")

	ownerBook, err := store.GetBookByID(owner.ID)
	require.NoError(t, err)
	require.NotNil(t, ownerBook.Duration)
	require.Equal(t, ownerFiles[0].Duration, *ownerBook.Duration,
		"the book that actually owns the row must be the one recomputed")

	// `other` never owned a row, so it must not have been recomputed at all.
	//
	// Counted as invocations rather than writes on purpose: `other` has no files
	// and a nil Duration, so recomputing it would sum to zero, match what is
	// already stored, and return via the no-change path without ever logging a
	// write. A write-count assertion here would read 0 whether or not the
	// spurious recompute happened, and would prove nothing.
	require.Zero(t, countAggregateInvocations(readLogs(), other.ID),
		"the requested-but-not-affected book must not be recomputed")
}
