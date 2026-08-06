// file: internal/plugins/maintenance/batch_delete_rows_test.go
// version: 1.0.0
// guid: 6c1f9b2e-7a04-4d38-95e6-1b8d3f0a2c57
// last-edited: 2026-08-06

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ─────────────────────────────────────────────────────────────────────────────
// dedupe-book-file-rows: salvage must still commit BEFORE its donors are deleted
// ─────────────────────────────────────────────────────────────────────────────

// salvageFailingStore fails UpdateBookFile for one specific row and delegates
// everything else to the real store underneath.
//
// It exists to exercise the one ordering rule that batching could plausibly have
// broken: rescued keeper fields are written in their OWN commit, before the
// donors they were rescued from are deleted. If the salvage write fails, the
// group must be abandoned with the donors untouched, so the next run can try
// again from the same evidence.
type salvageFailingStore struct {
	database.Store
	failForFileID string
	attempts      int
}

func (s *salvageFailingStore) UpdateBookFile(id string, f *database.BookFile) error {
	if id == s.failForFileID {
		s.attempts++
		return fmt.Errorf("simulated salvage write failure for %s", id)
	}
	return s.Store.UpdateBookFile(id, f)
}

// seedSalvageBook creates one book with two rows at the SAME path, split so that
// each row holds evidence the other lacks:
//
//	row 0: has a fingerprint, no duration  ← ranks first, becomes the keeper
//	row 1: has a duration, no fingerprint  ← the donor the keeper needs
//
// That split is what forces mergeMissingFields to report changed=true and makes
// the op attempt a salvage write, which is the code path under test.
func seedSalvageBook(t *testing.T, s *database.PebbleStore, title, path string) (bookID, keeperID, donorID string) {
	t.Helper()
	bk, err := s.CreateBook(&database.Book{Title: title})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	keeper := &database.BookFile{
		BookID:              bk.ID,
		FilePath:            path,
		AcoustIDFingerprint: []byte{0x01, 0x02, 0x03, 0x04},
	}
	if err := s.CreateBookFile(keeper); err != nil {
		t.Fatalf("CreateBookFile(keeper): %v", err)
	}
	donor := &database.BookFile{
		BookID:   bk.ID,
		FilePath: path,
		Duration: 3600,
		FileSize: 58000000,
	}
	if err := s.CreateBookFile(donor); err != nil {
		t.Fatalf("CreateBookFile(donor): %v", err)
	}
	return bk.ID, keeper.ID, donor.ID
}

// 🔴 THE ORDERING RULE, ASSERTED DIRECTLY.
//
// Batching the deletes must not tempt anyone into folding the salvage
// UpdateBookFile into the same atomic batch. Doing so silently removes the "if
// the salvage write fails, skip this group" escape: the group would commit both
// or neither, and "neither" is indistinguishable from "nothing to do" on the next
// run — so a keeper whose rescue failed could never be repaired from its twins
// again. This repo's dominant incident class is exactly that (the
// AcoustIDFingerprint and Author/Series write-back wipes).
//
// So: when the salvage write fails, the donor must still be there afterwards.
func TestDedupeBookFileRows_FailedSalvageLeavesDonorsIntact(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()

	// The book whose salvage will fail, and a control book that must still be
	// collapsed — otherwise a bug that skips EVERY book would pass this test.
	failBook, failKeeper, failDonor := seedSalvageBook(t, s, "Salvage Fails", "/lib/fail/track.m4b")
	okBook, _, _ := seedSalvageBook(t, s, "Salvage Succeeds", "/lib/ok/track.m4b")

	// PASS 1 of the op reads GetAllBookFilesCore, which is served from memdb.
	s.WaitForWarmup()

	wrapped := &salvageFailingStore{Store: s, failForFileID: failKeeper}
	p := &Plugin{deps: fakeDeps{store: wrapped}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})

	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}

	if wrapped.attempts == 0 {
		t.Fatal("the salvage write was never attempted — the fixture no longer exercises " +
			"the path under test, so this test proves nothing")
	}

	// THE ASSERTION: both rows survive. The donor still carries the duration the
	// keeper failed to receive, so the next run can rescue it.
	left, err := s.GetBookFiles(failBook)
	if err != nil {
		t.Fatalf("GetBookFiles(failBook): %v", err)
	}
	if len(left) != 2 {
		t.Fatalf("%d rows survived after a FAILED salvage, want 2 — the donor was deleted "+
			"even though the data it held was never rescued", len(left))
	}
	foundDonor := false
	for i := range left {
		if left[i].ID == failDonor {
			foundDonor = true
			if left[i].Duration != 3600 {
				t.Fatalf("donor duration = %d, want 3600 — the only surviving copy was damaged",
					left[i].Duration)
			}
		}
	}
	if !foundDonor {
		t.Fatalf("the donor row %s is gone; its duration was never salvaged onto the keeper",
			failDonor)
	}

	// The control book must still have collapsed — proving the skip is scoped to
	// the group whose salvage failed, not to the whole run.
	okLeft, err := s.GetBookFiles(okBook)
	if err != nil {
		t.Fatalf("GetBookFiles(okBook): %v", err)
	}
	if len(okLeft) != 1 {
		t.Fatalf("control book has %d rows, want 1 — one failing group aborted unrelated work",
			len(okLeft))
	}
	if okLeft[0].Duration != 3600 || len(okLeft[0].AcoustIDFingerprint) == 0 {
		t.Fatalf("control keeper lost salvaged evidence: duration=%d fingerprint_len=%d, "+
			"want 3600 and non-empty", okLeft[0].Duration, len(okLeft[0].AcoustIDFingerprint))
	}
}

// The batched path must still collapse a plain duplicate group end to end. This
// is the "did rerouting the caller break the op" guard.
func TestDedupeBookFileRows_BatchedDeleteStillCollapses(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a real PebbleStore; skipped in -short")
	}
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer s.Close()
	s.WaitForWarmup()

	bookIDs := seedDupBooks(t, s, 3, 5)

	p := &Plugin{deps: fakeDeps{store: s}}
	raw, _ := json.Marshal(DedupeBookFileRowsParams{Apply: true})
	if err := p.runDedupeBookFileRows(context.Background(), raw, &concurrentReporter{}); err != nil {
		t.Fatalf("runDedupeBookFileRows: %v", err)
	}

	for i, id := range bookIDs {
		files, ferr := s.GetBookFiles(id)
		if ferr != nil {
			t.Fatalf("GetBookFiles(%s): %v", id, ferr)
		}
		if len(files) != 1 {
			t.Fatalf("book %d: %d rows survived, want 1", i, len(files))
		}
		// Aggregates must reflect the single survivor, not the 5 original rows.
		bk, berr := s.GetBookByID(id)
		if berr != nil {
			t.Fatalf("GetBookByID(%s): %v", id, berr)
		}
		if bk.Duration == nil || *bk.Duration != 3600 {
			t.Fatalf("book %d duration = %v, want 3600 (one surviving row, not 5)", i, bk.Duration)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// orphan-book-files-cleanup: must route through the notifying batch method
// ─────────────────────────────────────────────────────────────────────────────

// ⚠️ THIS PATH HAS NO TRAILING RecomputeBookAggregates OF ITS OWN, unlike
// dedupe-book-file-rows. Its aggregate freshness comes entirely from
// DeleteBookFilesByIDs notifying once per affected book, so the thing worth
// asserting at this layer is that the op goes through that method and not through
// some non-notifying shortcut. (That the method itself recomputes is asserted
// directly in internal/database/delete_book_files_by_ids_test.go.)
//
// For a GENUINE orphan the owning book is gone by definition, so the recompute
// finds nothing and returns early — but the notification still has to be issued,
// because orphanhood is decided from a snapshot and a book that turns out to
// exist after all must have its totals corrected rather than left counting rows
// that no longer exist.
func TestOrphanBookFilesCleanup_DeletesViaNotifyingBatchMethod(t *testing.T) {
	books := []database.Book{{ID: "book-alive", Title: "Alive"}}
	files := []database.BookFileCore{
		{ID: "f1", BookID: "book-alive", FilePath: "/lib/keep.m4b"},
		{ID: "f2", BookID: "book-ghost", FilePath: "/lib/orphan-1.m4b"},
		{ID: "f3", BookID: "book-ghost", FilePath: "/lib/orphan-2.m4b"},
	}

	var batchCalls [][]string
	var perRowCalls []string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		DeleteBookFilesByIDsFunc: func(ids []string) error {
			batchCalls = append(batchCalls, append([]string(nil), ids...))
			return nil
		},
		DeleteBookFileFunc: func(id string) error {
			perRowCalls = append(perRowCalls, id)
			return nil
		},
	}

	p := &Plugin{deps: fakeDeps{store: store}}
	raw, _ := json.Marshal(OrphanBookFilesCleanupParams{Delete: true})
	if err := p.runOrphanBookFilesCleanup(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runOrphanBookFilesCleanup: %v", err)
	}

	if len(perRowCalls) != 0 {
		t.Fatalf("the per-row DeleteBookFile was called %d times (%v) — the op is still paying "+
			"~1.35s of fixed overhead per row", len(perRowCalls), perRowCalls)
	}
	if len(batchCalls) != 1 {
		t.Fatalf("DeleteBookFilesByIDs called %d times for 2 orphans, want 1", len(batchCalls))
	}
	got := map[string]bool{}
	for _, id := range batchCalls[0] {
		got[id] = true
	}
	if len(got) != 2 || !got["f2"] || !got["f3"] {
		t.Fatalf("batched ids = %v, want exactly f2 and f3", batchCalls[0])
	}
}

// Chunking exists so that one unresolvable ID defers only its own chunk rather
// than the whole sweep — and so the per-row cancellation check and progress tick
// the old loop gave us for free survive. Both depend on the list actually being
// split, so assert the split rather than trusting the constant.
func TestOrphanBookFilesCleanup_ChunksLargeOrphanSets(t *testing.T) {
	const orphanCount = 1200 // > 2 chunks at 500

	books := []database.Book{{ID: "book-alive", Title: "Alive"}}
	files := make([]database.BookFileCore, 0, orphanCount)
	for i := 0; i < orphanCount; i++ {
		files = append(files, database.BookFileCore{
			ID:       fmt.Sprintf("orphan-%04d", i),
			BookID:   "book-ghost",
			FilePath: fmt.Sprintf("/lib/orphan-%04d.m4b", i),
		})
	}

	var chunkSizes []int
	seen := map[string]int{}
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{books[0].Core()}, nil
		},
		DeleteBookFilesByIDsFunc: func(ids []string) error {
			chunkSizes = append(chunkSizes, len(ids))
			for _, id := range ids {
				seen[id]++
			}
			return nil
		},
	}

	p := &Plugin{deps: fakeDeps{store: store}}
	raw, _ := json.Marshal(OrphanBookFilesCleanupParams{Delete: true})
	if err := p.runOrphanBookFilesCleanup(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runOrphanBookFilesCleanup: %v", err)
	}

	if len(chunkSizes) != 3 {
		t.Fatalf("chunk count = %d (sizes %v), want 3 for %d orphans at 500/chunk — "+
			"an unchunked call would make one stale id abort the entire sweep",
			len(chunkSizes), chunkSizes, orphanCount)
	}
	if len(seen) != orphanCount {
		t.Fatalf("%d distinct ids deleted, want %d — chunking dropped or duplicated rows",
			len(seen), orphanCount)
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("id %s was submitted %d times, want exactly 1", id, n)
		}
	}
}

// ⚠️ THE REGRESSION THIS GUARDS AGAINST IS SUBTLE AND WAS ALMOST SHIPPED.
//
// Fail-closed is self-healing for dedupe-book-file-rows, which re-reads its rows
// from Pebble every run. It is NOT self-healing here: findOrphanBookFiles gets its
// list from GetAllBookFilesCore, which is served from MEMDB in production. memdb
// can hold a row Pebble no longer has, and that phantom yields an ID that will
// never resolve — so a naive fail-closed chunk would abort the SAME 500 rows on
// every nightly 02:15 run until the process restarts and memdb rebuilds. The
// per-row loop this replaced skipped the phantom and deleted the other 499.
//
// Hence the degraded path: a rejected chunk is retried one row at a time through
// DeleteBookFile, which tolerates "already gone". Assert that the survivors of a
// failed chunk actually get deleted — not merely that the run continues.
func TestOrphanBookFilesCleanup_RejectedChunkFallsBackToPerRowDeletes(t *testing.T) {
	const orphanCount = 1200

	files := make([]database.BookFileCore, 0, orphanCount)
	for i := 0; i < orphanCount; i++ {
		files = append(files, database.BookFileCore{
			ID:       fmt.Sprintf("orphan-%04d", i),
			BookID:   "book-ghost",
			FilePath: fmt.Sprintf("/lib/orphan-%04d.m4b", i),
		})
	}

	batchCalls := 0
	var perRow []string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{{ID: "book-alive"}}, nil
		},
		DeleteBookFilesByIDsFunc: func(ids []string) error {
			batchCalls++
			if batchCalls == 1 {
				// Exactly the shape DeleteBookFilesByIDs produces for a phantom row.
				return fmt.Errorf("DeleteBookFilesByIDs: 1 of %d book_file id(s) did not "+
					"resolve, nothing deleted: orphan-0007", len(ids))
			}
			return nil
		},
		DeleteBookFileFunc: func(id string) error {
			perRow = append(perRow, id)
			if id == "orphan-0007" {
				return fmt.Errorf("simulated: phantom row, not in pebble")
			}
			return nil
		},
	}

	p := &Plugin{deps: fakeDeps{store: store}}
	raw, _ := json.Marshal(OrphanBookFilesCleanupParams{Delete: true})
	if err := p.runOrphanBookFilesCleanup(context.Background(), raw, &fakeReporter{}); err != nil {
		t.Fatalf("runOrphanBookFilesCleanup returned %v; a failing chunk is recovered and "+
			"logged, not propagated", err)
	}

	if batchCalls != 3 {
		t.Fatalf("DeleteBookFilesByIDs called %d times, want 3 — a failing chunk aborted "+
			"the chunks after it", batchCalls)
	}
	// THE ASSERTION: the rejected chunk's 500 rows were retried individually, so
	// the 499 real orphans still got deleted rather than being stuck forever.
	if len(perRow) != 500 {
		t.Fatalf("per-row fallback attempted %d rows, want 500 — the rejected chunk's "+
			"rows would be abandoned on every run until memdb rebuilds", len(perRow))
	}
	if perRow[0] != "orphan-0000" || perRow[499] != "orphan-0499" {
		t.Fatalf("fallback covered the wrong rows: first=%s last=%s", perRow[0], perRow[499])
	}
	// Only the phantom is left behind; the other 499 in that chunk succeeded.
	if !containsID(perRow, "orphan-0007") {
		t.Fatal("the phantom row was never retried individually")
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}
