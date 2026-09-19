// file: internal/plugins/maintenance/batch_delete_rows_test.go
// version: 1.2.0
// guid: 6c1f9b2e-7a04-4d38-95e6-1b8d3f0a2c57
// last-edited: 2026-09-19

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: wrapped}, root: t.TempDir()}}
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

	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: s}, root: t.TempDir()}}
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
// orphan-book-files-cleanup: report-only; the delete mode was removed
// ─────────────────────────────────────────────────────────────────────────────

// The op's {"delete": true} mode deleted book_file rows, which the standing
// rule forbids (repoint, never delete). It must now refuse loudly — an old
// caller must not get a silent report it mistakes for a cleanup — and must not
// touch a single row either way.
func TestOrphanBookFilesCleanup_DeleteModeIsRefusedAndDeletesNothing(t *testing.T) {
	files := []database.BookFileCore{
		{ID: "f1", BookID: "book-alive", FilePath: "/lib/keep.m4b"},
		{ID: "f2", BookID: "book-ghost", FilePath: "/lib/orphan-1.m4b"},
	}
	var deletes int
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return files, nil },
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{{ID: "book-alive"}}, nil
		},
		DeleteBookFilesByIDsFunc: func(ids []string) error { deletes += len(ids); return nil },
		DeleteBookFileFunc:       func(id string) error { deletes++; return nil },
	}
	p := &Plugin{deps: rootDirDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}

	raw, _ := json.Marshal(OrphanBookFilesCleanupParams{Delete: true})
	if err := p.runOrphanBookFilesCleanup(context.Background(), raw, &fakeReporter{}); err == nil {
		t.Fatal("delete mode was accepted; it must be refused")
	}
	if err := p.runOrphanBookFilesCleanup(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("report-only run: %v", err)
	}
	if deletes != 0 {
		t.Fatalf("%d book_file row(s) deleted", deletes)
	}
}

func containsID(ids []string, want string) bool {
	return slices.Contains(ids, want)
}
