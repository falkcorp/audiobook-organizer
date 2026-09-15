// file: internal/reconcile/reconcile_lost_update_test.go
// version: 1.0.0
// guid: ea0798e4-b9ae-402f-9662-c7ec718821b3
// last-edited: 2026-09-15

package reconcile

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// reconcileLostUpdateStore wraps fakeReconcileStore with the OTHER writer: it
// commits Duration=4242 on the stored row right after every raw GetBookByID
// (which here returns a copy, as the real store does) and right before every
// ModifyBook takes the row lock. A site that reads the row and writes the
// WHOLE row back reverts it; ModifyBook keeps it. Same shape as
// electLostUpdateStore, over the shared fake the whole-library passes use.
type reconcileLostUpdateStore struct {
	*fakeReconcileStore
}

func (s reconcileLostUpdateStore) landDuration(id string) {
	s.lock()
	defer s.unlock()
	if b := s.byID[id]; b != nil {
		d := 4242
		b.Duration = &d
	}
}

func (s reconcileLostUpdateStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.fakeReconcileStore.GetBookByID(id)
	if err != nil || b == nil {
		return b, err
	}
	cp := *b
	s.landDuration(id)
	return &cp, nil
}

func (s reconcileLostUpdateStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	s.landDuration(id)
	return s.fakeReconcileStore.ModifyBook(id, fn)
}

// TestAssignOrphanVGs_DoesNotRevertConcurrentColumns pins the lost-update fix
// on the orphan-VG assignment write (audit A1#15), the pass that touches every
// ungrouped library book: it must set only VersionGroupID / IsPrimaryVersion /
// LibraryState, so a Duration another writer commits between its read and its
// write survives. Against the old GetBookByID -> UpdateBook(whole row) it fails
// with "Duration reverted".
func TestAssignOrphanVGs_DoesNotRevertConcurrentColumns(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	defer func() { config.AppConfig.RootDir = prevRoot }()
	const libRoot = "/lib"
	config.AppConfig.RootDir = libRoot

	inner := newFakeStore()
	const id = "orphan"
	path := libRoot + "/orphan.m4b"
	inner.books = append(inner.books, database.BookCore{ID: id, Title: id, FilePath: path})
	inner.byID[id] = &database.Book{ID: id, Title: id, FilePath: path}
	store := reconcileLostUpdateStore{inner}

	res, err := AssignOrphanVGs(store, libRoot)
	if err != nil {
		t.Fatalf("AssignOrphanVGs: %v", err)
	}
	if res.Assigned != 1 || res.Errors != 0 || res.SkippedConcurrentAssignment != 0 {
		t.Fatalf("Assigned=%d Errors=%d SkippedConcurrent=%d, want 1/0/0", res.Assigned, res.Errors, res.SkippedConcurrentAssignment)
	}
	inner.lock()
	got := inner.updated[id]
	inner.unlock()
	if got == nil || got.VersionGroupID == nil || *got.VersionGroupID == "" {
		t.Fatalf("orphan not written with a version group: %+v", got)
	}
	if got.IsPrimaryVersion == nil || !*got.IsPrimaryVersion {
		t.Fatalf("orphan not written as primary: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the version-group assignment write: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}

// TestFindBrokenSegmentBooks_DoesNotRevertConcurrentColumns pins the same fix
// on the broken-segment mark, the other whole-library pass that writes: it must
// set only LibraryState / MarkedForDeletion / MarkedForDeletionAt. Against the
// old hydrate -> UpdateBook(whole row) it fails with "Duration reverted".
func TestFindBrokenSegmentBooks_DoesNotRevertConcurrentColumns(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = "" // disable the library-prefix skip; check all books
	defer func() { config.AppConfig.RootDir = prevRoot }()

	dir := t.TempDir()
	seg0 := writeFile(t, dir, "seg0.m4b", "s0")
	seg1 := dir + "/seg1-missing.m4b" // never written -> missing

	inner := newFakeStore()
	const id = "broken"
	inner.books = append(inner.books, database.BookCore{ID: id, Title: id, FilePath: dir})
	inner.files[id] = []database.BookFile{{FilePath: seg0}, {FilePath: seg1}}
	inner.byID[id] = &database.Book{ID: id, Title: id, FilePath: dir}
	store := reconcileLostUpdateStore{inner}

	res, err := FindBrokenSegmentBooks(store, false)
	if err != nil {
		t.Fatalf("FindBrokenSegmentBooks: %v", err)
	}
	if res.BrokenBooks != 1 || res.MarkedForReview != 1 {
		t.Fatalf("BrokenBooks=%d MarkedForReview=%d, want 1/1", res.BrokenBooks, res.MarkedForReview)
	}
	inner.lock()
	got := inner.updated[id]
	inner.unlock()
	if got == nil || got.LibraryState == nil || *got.LibraryState != "needs_review" {
		t.Fatalf("broken book not written as needs_review: %+v", got)
	}
	if got.MarkedForDeletion == nil || !*got.MarkedForDeletion {
		t.Fatalf("broken book not marked for deletion: %+v", got)
	}
	if got.Duration == nil || *got.Duration != 4242 {
		t.Fatalf("Duration reverted by the broken-segment mark: got %v, want 4242 (the concurrent writer's value)", got.Duration)
	}
}
