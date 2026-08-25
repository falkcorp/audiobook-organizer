// file: internal/database/batch_upsert_aggregates_test.go
// version: 1.4.0
// guid: 9d41f6b2-70e8-4c35-b1a7-38f0c92e64d5
// last-edited: 2026-08-24

package database

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
)

// These three delegate to internal/database/aggtest, which owns the real
// implementations and the TerminalMarkers contract. They were defined here until
// internal/plugins/maintenance needed them too — a test helper in this package's
// _test files cannot cross a package boundary, and copying them would fork
// TerminalMarkers. Kept as local names so the ~27 call sites in this package did
// not have to churn; read aggtest's doc comments before using either counter.
func captureAggregateLogs(t *testing.T) func() string { return aggtest.Capture(t) }

func countAggregateInvocations(logs, bookID string) int {
	return aggtest.CountInvocations(logs, bookID)
}

func countAggregateWrites(logs, bookID string) int { return aggtest.CountWrites(logs, bookID) }

// sumStoredFileAggregates reads back what the store actually holds for bookID.
//
// The expected totals are derived from the STORED rows rather than from the
// values handed to BatchUpsertBookFiles, because the batch path runs each file
// through normalizeBookFileDuration (CONS-18) and may rewrite a duration on the
// way in. Comparing the book against its own files is also the invariant that
// actually matters: the parent's aggregates must equal the sum of its children —
// but ONLY while at least one child carries a duration. When none does, the
// partial-data rule deliberately preserves the book's previous value instead,
// and parent will not equal children. TestDeletingEveryFileKeepsTheBookDuration
// pins exactly that case, so do not use this helper to assert it.
//
// KNOWN BLIND SPOT: expectations come from the stored rows, so this cannot see
// row duplication — if a batch wrote the same file twice, both copies are summed
// on each side and the comparison still balances. That is the deliberate
// trade-off for surviving CONS-18, not an oversight; see the todo.d fragment on
// duplicate rows within a single batch.
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

	// TWO books is the load-bearing property, and it was measured: rerun with a
	// single-book fixture and the "recompute only the first book" and "only the
	// last book" mutants both survive. Multiplicity is what catches a partial
	// recompute.
	//
	// The differing file counts (5 vs 3) are NOT load-bearing — an earlier
	// version of this comment claimed they were. Rerun with 5 and 5 and every
	// mutant this fixture kills is still killed. What actually separates the two
	// books is their disjoint per-file durations (600+i vs 900+i), which is what
	// stops one book's total coincidentally matching the other's. Keep the
	// durations distinct; the counts are free to change.
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

// TestBatchUpsertBookFilesMultiRowRetargetStrict closes the gap between the two
// tests above: one is multi-row but every row is NEW, the other exercises the
// retarget branch but with a SINGLE row. Nothing covered a batch that both
// retargets a row and carries a second row for the same book.
//
// That combination is where a per-row recompute can hide. Two mutations survive
// the rest of this suite and are killed only here: appending the book id a
// second time inside the `existing != nil` branch (so an updated row costs an
// extra recompute), and keying the de-dup set on the PRE-match BookID while
// appending the POST-match one (so the set never suppresses anything for a
// retargeted row). Both reintroduce exactly the O(N^2) read amplification this
// change exists to delete, on the update path only.
func TestBatchUpsertBookFilesMultiRowRetargetStrict(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	owner, err := store.CreateBook(&Book{Title: "Owner", FilePath: "/lib/own"})
	require.NoError(t, err)
	other, err := store.CreateBook(&Book{Title: "Other", FilePath: "/lib/other"})
	require.NoError(t, err)

	const ownedPath = "/lib/own/t1.m4b"
	require.NoError(t, store.CreateBookFile(&BookFile{
		BookID:   owner.ID,
		FilePath: ownedPath,
		Duration: 100,
		FileSize: 1_000_000,
	}))

	readLogs := captureAggregateLogs(t)

	// Row 1 is submitted under `other` and retargets to `owner` by path.
	// Row 2 is a genuinely new row for `owner`. Both resolve to one book, so
	// one recompute must cover them.
	require.NoError(t, store.BatchUpsertBookFiles([]*BookFile{
		{BookID: other.ID, FilePath: ownedPath, Duration: 250, FileSize: 2_000_000},
		{BookID: owner.ID, FilePath: "/lib/own/t2.m4b", Duration: 300, FileSize: 3_000_000},
	}))

	require.Equal(t, 1, countAggregateInvocations(readLogs(), owner.ID),
		"a batch resolving to ONE book must recompute it exactly once, "+
			"however many rows resolve to it and whether they are new or updated")
	require.Zero(t, countAggregateInvocations(readLogs(), other.ID),
		"the book named on the request but owning no row must not be recomputed")

	wantDur, wantSize := sumStoredFileAggregates(t, store, owner.ID)
	require.Positive(t, wantDur, "fixture must produce a non-zero duration or the assertion is vacuous")

	ownerBook, err := store.GetBookByID(owner.ID)
	require.NoError(t, err)
	require.NotNil(t, ownerBook.Duration)
	require.Equal(t, wantDur, *ownerBook.Duration)
	require.NotNil(t, ownerBook.FileSize)
	require.Equal(t, wantSize, *ownerBook.FileSize)
}

// TestBatchUpsertBookFilesAggregateLogInstrumentIsLive is a known-good twin for
// the counting helper, and it exists because the instrument can die silently.
//
// Every coalescing assertion in this file is counted through log lines emitted
// by RecomputeBookAggregates in a DIFFERENT file. Renaming those lines fails
// loudly — plenty of tests break. But DELETING the "no change needed" Debug
// block (or raising its level, or changing its Enabled guard so it stops
// emitting) leaves this whole suite green, because a redundant recompute then
// produces no line at all and simply is not counted. With that block gone, a
// mutant that drops the per-book de-duplication and recomputes once per row
// survives a full `go test ./internal/database/` run. Measured, not assumed.
//
// So that Debug line is load-bearing for tests in another file, and nothing at
// the line says so. This test is the control: it asserts the redundant path is
// still observable, so the instrument's death fails here rather than quietly
// blinding everything else.
func TestBatchUpsertBookFilesAggregateLogInstrumentIsLive(t *testing.T) {
	store, cleanup := setupTestDB(t)
	defer cleanup()

	book, err := store.CreateBook(&Book{Title: "Instrument", FilePath: "/lib/instr"})
	require.NoError(t, err)

	readLogs := captureAggregateLogs(t)

	require.NoError(t, store.BatchUpsertBookFiles([]*BookFile{{
		BookID:   book.ID,
		FilePath: "/lib/instr/t1.m4b",
		Duration: 600,
		FileSize: 6_000_000,
	}}))
	require.Equal(t, 1, countAggregateInvocations(readLogs(), book.ID))
	require.Equal(t, 1, countAggregateWrites(readLogs(), book.ID))

	// Deliberately redundant: the sums are unchanged, so this takes the
	// no-change early return and emits ONLY the Debug line.
	require.NoError(t, store.RecomputeBookAggregates(book.ID))

	require.Equal(t, 2, countAggregateInvocations(readLogs(), book.ID),
		"a redundant no-change recompute must still be COUNTED — if this reads 1, "+
			"the 'no change needed' Debug line is gone and every coalescing "+
			"assertion in this file has silently stopped being able to see a "+
			"per-row regression")
	require.Equal(t, 1, countAggregateWrites(readLogs(), book.ID),
		"a no-change recompute must not be counted as a write")
}
