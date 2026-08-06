// file: internal/database/delete_book_files_by_ids_test.go
// version: 1.0.0
// guid: 4e7a1c92-3d58-4b6f-9a0e-2c8f5b1d7e43
// last-edited: 2026-08-06

package database

import (
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// countBookVersions counts the book_ver:<bookID>:<nanos> copy-on-write snapshots
// that exist for one book.
//
// WHY THIS IS THE MEASUREMENT. notifyBookFileChange is unexported and cannot be
// mocked, so "was this book notified once or N times?" has to be observed through
// something the notification leaves behind. Every notification that actually
// changes an aggregate runs UpdateBook, and every UpdateBook writes exactly one
// book_ver snapshot marshalling the whole old Book. So the snapshot delta across
// a call IS the number of effective notifications — and it is also the single
// most expensive artefact of the per-row path, which makes it the right thing to
// be counting rather than a proxy for it.
func countBookVersions(t *testing.T, s *PebbleStore, bookID string) int {
	t.Helper()
	prefix := fmt.Sprintf("book_ver:%s:", bookID)
	iter, err := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefix),
		UpperBound: []byte(prefix + "\xff"),
	})
	if err != nil {
		t.Fatalf("NewIter: %v", err)
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

// seedBookWithFiles creates one book holding len(durations) rows, each at its own
// path, and returns the book ID plus the row IDs in order.
func seedBookWithFiles(t *testing.T, s *PebbleStore, title string, durations []int) (string, []string) {
	t.Helper()
	bk, err := s.CreateBook(&Book{Title: title})
	if err != nil {
		t.Fatalf("CreateBook(%s): %v", title, err)
	}
	ids := make([]string, 0, len(durations))
	for i, d := range durations {
		f := &BookFile{
			BookID:   bk.ID,
			FilePath: fmt.Sprintf("/lib/%s/track%02d.m4b", bk.ID, i),
			Duration: d,
			FileSize: int64(d) * 16000,
		}
		if err := s.CreateBookFile(f); err != nil {
			t.Fatalf("CreateBookFile: %v", err)
		}
		ids = append(ids, f.ID)
	}
	return bk.ID, ids
}

func newBatchDeleteStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// 🔑 THIS IS THE FIX, AND THIS TEST IS THE PROOF OF IT.
//
// maintenance.dedupe-book-file-rows cost ~1.35s of FIXED overhead per deleted
// row — flat per-row deltas (1.85/1.94/1.42/1.66/1.54s) while the book's file
// count fell 65 → 34, which is what rules out an O(R²) walk and pins it on
// per-row work that should have been per-book. Over 2,901 redundant production
// rows that is ~1.3 hours.
//
// The single biggest component is the copy-on-write book_ver snapshot: one full
// re-marshal of the entire Book, per deleted row. So the assertion is not "it
// feels faster", it is a COUNT: three rows deleted across two books must produce
// exactly one snapshot per affected book, never one per row.
func TestDeleteBookFilesByIDs_NotifiesEachAffectedBookExactlyOnce(t *testing.T) {
	s := newBatchDeleteStore(t)

	// Durations are all distinct and non-zero so that removing rows genuinely
	// changes each book's summed Duration/FileSize. This matters: RecomputeBook-
	// Aggregates early-returns without writing when nothing changed, so a fixture
	// whose totals happen to stay put would record a delta of 0 and pass this test
	// vacuously — proving nothing about how many times it was called.
	bookA, filesA := seedBookWithFiles(t, s, "Book A", []int{100, 200, 300})
	bookB, filesB := seedBookWithFiles(t, s, "Book B", []int{400, 500})

	beforeA := countBookVersions(t, s, bookA)
	beforeB := countBookVersions(t, s, bookB)

	// Two rows from A and one from B — deliberately interleaved so the grouping
	// cannot be an accident of input ordering.
	ids := []string{filesA[1], filesB[1], filesA[2]}
	if err := s.DeleteBookFilesByIDs(ids); err != nil {
		t.Fatalf("DeleteBookFilesByIDs: %v", err)
	}

	if got := countBookVersions(t, s, bookA) - beforeA; got != 1 {
		t.Fatalf("book A: %d new book_ver snapshots after deleting 2 rows, want exactly 1 — "+
			"the per-row notify is still in the loop", got)
	}
	if got := countBookVersions(t, s, bookB) - beforeB; got != 1 {
		t.Fatalf("book B: %d new book_ver snapshots after deleting 1 row, want exactly 1", got)
	}

	// The rows must actually be gone, and the RIGHT ones.
	filesLeftA, err := s.GetBookFiles(bookA)
	if err != nil {
		t.Fatalf("GetBookFiles(A): %v", err)
	}
	if len(filesLeftA) != 1 || filesLeftA[0].ID != filesA[0] {
		t.Fatalf("book A: %d rows survived (%v), want exactly the untouched first row",
			len(filesLeftA), filesLeftA)
	}
	filesLeftB, err := s.GetBookFiles(bookB)
	if err != nil {
		t.Fatalf("GetBookFiles(B): %v", err)
	}
	if len(filesLeftB) != 1 || filesLeftB[0].ID != filesB[0] {
		t.Fatalf("book B: %d rows survived, want exactly the untouched first row", len(filesLeftB))
	}

	// Aggregates must reflect the survivors — batching must not cost correctness.
	bkA, err := s.GetBookByID(bookA)
	if err != nil {
		t.Fatalf("GetBookByID(A): %v", err)
	}
	if bkA.Duration == nil || *bkA.Duration != 100 {
		t.Fatalf("book A duration = %v, want 100 (the one surviving row)", bkA.Duration)
	}
}

// The counterpart to the test above: the OLD path, on the SAME fixture, must
// produce one snapshot PER ROW. Without this the "exactly 1" assertion above is
// unanchored — a reader cannot tell whether 1 is a win or simply what this
// fixture always produces.
func TestDeleteBookFile_PerRowPathStillNotifiesPerRow(t *testing.T) {
	s := newBatchDeleteStore(t)
	bookID, fileIDs := seedBookWithFiles(t, s, "Per-row Book", []int{100, 200, 300})

	before := countBookVersions(t, s, bookID)
	for _, id := range fileIDs[1:] { // delete 2 rows, one call each
		if err := s.DeleteBookFile(id); err != nil {
			t.Fatalf("DeleteBookFile(%s): %v", id, err)
		}
	}
	if got := countBookVersions(t, s, bookID) - before; got != 2 {
		t.Fatalf("per-row path produced %d snapshots for 2 deletes, want 2 — "+
			"DeleteBookFile's own behaviour must NOT have changed; other callers rely on it", got)
	}
}

// FAIL-CLOSED. An ID that does not resolve means the caller's view of the store
// disagrees with the store, and this is a DESTRUCTIVE operation. Nothing is
// deleted, and the error names the offender.
//
// This deliberately differs from DeleteBookFile, which treats an unresolvable ID
// as "already gone" and returns nil — defensible for one row because nothing else
// rides along, indefensible for a batch. Both callers re-read their row sets from
// the store on every run, so the deferred work simply happens on the next run.
func TestDeleteBookFilesByIDs_FailsClosedOnUnresolvableID(t *testing.T) {
	s := newBatchDeleteStore(t)
	bookID, fileIDs := seedBookWithFiles(t, s, "Fail-closed Book", []int{100, 200, 300})

	before := countBookVersions(t, s, bookID)

	err := s.DeleteBookFilesByIDs([]string{fileIDs[0], "no-such-book-file-id", fileIDs[1]})
	if err == nil {
		t.Fatal("DeleteBookFilesByIDs returned nil for an unresolvable id, want an error")
	}
	if !strings.Contains(err.Error(), "no-such-book-file-id") {
		t.Fatalf("error %q does not name the unresolvable id — an operator cannot act on it", err)
	}

	// NOTHING may have been deleted, including the two IDs that did resolve.
	left, gerr := s.GetBookFiles(bookID)
	if gerr != nil {
		t.Fatalf("GetBookFiles: %v", gerr)
	}
	if len(left) != 3 {
		t.Fatalf("%d rows survived a failed batch, want all 3 — fail-closed means "+
			"the resolvable rows are spared too", len(left))
	}
	if got := countBookVersions(t, s, bookID) - before; got != 0 {
		t.Fatalf("%d book_ver snapshots written by a batch that deleted nothing, want 0", got)
	}
}

// Duplicate IDs in the input must not double-delete or double-notify. Callers
// accumulate IDs across groups, so a duplicate is a plausible caller bug rather
// than a theoretical one.
func TestDeleteBookFilesByIDs_IgnoresDuplicateAndEmptyIDs(t *testing.T) {
	s := newBatchDeleteStore(t)
	bookID, fileIDs := seedBookWithFiles(t, s, "Dup-input Book", []int{100, 200, 300})

	before := countBookVersions(t, s, bookID)
	if err := s.DeleteBookFilesByIDs([]string{fileIDs[2], "", fileIDs[2]}); err != nil {
		t.Fatalf("DeleteBookFilesByIDs: %v", err)
	}
	if got := countBookVersions(t, s, bookID) - before; got != 1 {
		t.Fatalf("%d snapshots, want 1 — a repeated id was treated as a second row", got)
	}
	left, _ := s.GetBookFiles(bookID)
	if len(left) != 2 {
		t.Fatalf("%d rows survived, want 2", len(left))
	}
}

// An empty ID list is a no-op, not an error: callers that accumulate IDs may
// legitimately end a book with nothing to delete.
func TestDeleteBookFilesByIDs_EmptyInputIsNoOp(t *testing.T) {
	s := newBatchDeleteStore(t)
	bookID, _ := seedBookWithFiles(t, s, "Empty-input Book", []int{100})
	before := countBookVersions(t, s, bookID)

	if err := s.DeleteBookFilesByIDs(nil); err != nil {
		t.Fatalf("DeleteBookFilesByIDs(nil) = %v, want nil", err)
	}
	if got := countBookVersions(t, s, bookID) - before; got != 0 {
		t.Fatalf("%d snapshots written for an empty batch, want 0", got)
	}
}

// Secondary indexes must be torn down by the batch path exactly as they are by
// the per-row path. A row whose primary key is gone but whose book_file_id index
// entry survives is precisely the state that makes a LATER DeleteBookFilesByIDs
// "resolve" a row that no longer exists.
func TestDeleteBookFilesByIDs_RemovesSecondaryIndexes(t *testing.T) {
	s := newBatchDeleteStore(t)
	bk, err := s.CreateBook(&Book{Title: "Index Book"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	f := &BookFile{
		BookID:             bk.ID,
		FilePath:           "/lib/index/track01.m4b",
		Duration:           600,
		FileSize:           9600000,
		ITunesPersistentID: "DEADBEEF12345678",
		FileHash:           "sha256:indexhash",
	}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	if err := s.DeleteBookFilesByIDs([]string{f.ID}); err != nil {
		t.Fatalf("DeleteBookFilesByIDs: %v", err)
	}

	if got, _ := s.GetBookFileByPID("DEADBEEF12345678"); got != nil {
		t.Fatal("PID index entry survived the batch delete")
	}
	if got, _ := s.GetBookFileByPath("/lib/index/track01.m4b"); got != nil {
		t.Fatal("path index entry survived the batch delete")
	}
	// The ID index is what DeleteBookFilesByIDs itself resolves through, so a
	// stale entry here would let a second delete "find" a row that is gone.
	if err := s.DeleteBookFilesByIDs([]string{f.ID}); err == nil {
		t.Fatal("re-deleting an already-deleted id succeeded — the book_file_id " +
			"index entry is stale, so resolution is lying about what exists")
	}
}
