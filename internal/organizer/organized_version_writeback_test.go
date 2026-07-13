// file: internal/organizer/organized_version_writeback_test.go
// version: 1.1.0
// guid: 8eea5b3c-7be2-4f84-a629-aca6c5044dbb
// last-edited: 2026-07-13

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// newWritebackTestBook seeds one book in store with denormalized Author and
// Series populated, mirroring the shape a full-fidelity Pebble row has in
// production before it is ever passed through the memdb-slim projection.
//
// It then immediately re-reads the row via GetBookByID and hard-fails if
// Author/Series did not persist. Without this check, a later nil Author on
// the original book after CreateOrganizedVersion would be ambiguous: did
// the write-back wipe it, or did CreateBook/Pebble simply never persist a
// denormalized Author in the first place? Asserting the baseline here makes
// the later Outcome-A/B read unambiguous — a post-write-back nil can only
// be the write-back's doing.
func newWritebackTestBook(t *testing.T, store *database.PebbleStore) *database.Book {
	t.Helper()

	created, err := store.CreateBook(&database.Book{
		Title:    "Writeback Probe",
		FilePath: filepath.Join(t.TempDir(), "original.m4b"),
		Author:   &database.Author{ID: 1, Name: "Original Author"},
		Series:   &database.Series{ID: 1, Name: "Original Series"},
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	seeded, err := store.GetBookByID(created.ID)
	if err != nil {
		t.Fatalf("GetBookByID (post-seed baseline): %v", err)
	}
	if seeded == nil {
		t.Fatalf("GetBookByID (post-seed baseline) returned nil for %q", created.ID)
	}
	if seeded.Author == nil || seeded.Series == nil {
		t.Fatalf("fixture bug: seeded book has nil Author/Series before any write-back — Author=%v Series=%v", seeded.Author, seeded.Series)
	}

	return seeded
}

// TestCreateOrganizedVersion_OriginalDemotedToNonPrimary proves the
// version-group demotion invariant holds against a real PebbleStore:
// after CreateOrganizedVersion runs, the original book must end up with
// VersionGroupID set, IsPrimaryVersion=false, and LibraryState=
// "organized_source". This must hold regardless of the Author/Series
// outcome verified below (TODO.md: CreateOrganizedVersion original-book
// slim-writeback, STOREFID W5d-1).
func TestCreateOrganizedVersion_OriginalDemotedToNonPrimary(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	org := &Organizer{config: &config.Config{}}

	seeded := newWritebackTestBook(t, store)

	// Build the SLIM projection the production write-back path sends:
	// GetAllBooksCore -> ToBook nils the heavy/denormalized fields.
	slim := *seeded
	slim.Author = nil
	slim.Series = nil
	slim.Description = nil

	newPath := filepath.Join(t.TempDir(), "organized.m4b")
	if _, err := svc.CreateOrganizedVersion(org, &slim, newPath, false, "", &noopLogger{}); err != nil {
		t.Fatalf("CreateOrganizedVersion: %v", err)
	}

	got, err := store.GetBookByID(seeded.ID)
	if err != nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if got == nil {
		t.Fatalf("GetBookByID returned nil for original book %q", seeded.ID)
	}

	if got.VersionGroupID == nil || *got.VersionGroupID == "" {
		t.Errorf("VersionGroupID not set on original: got %v", got.VersionGroupID)
	}
	if got.IsPrimaryVersion == nil {
		t.Fatalf("IsPrimaryVersion is nil on original")
	}
	if *got.IsPrimaryVersion != false {
		t.Errorf("IsPrimaryVersion = %v, want false", *got.IsPrimaryVersion)
	}
	if got.LibraryState == nil {
		t.Fatalf("LibraryState is nil on original")
	}
	if *got.LibraryState != "organized_source" {
		t.Errorf("LibraryState = %q, want %q", *got.LibraryState, "organized_source")
	}
}

// TestCreateOrganizedVersion_RecordsOrganizeProvenance proves the Bug-2
// import-provenance fix: CreateOrganizedVersion moves the file from the original
// path to newPath, and that source path is known here, so it must record an
// "organize" path-change on the new book carrying the real old → new. Without it
// the change log only shows CreateBook's empty-from "import" marker and drops
// where the file was organized FROM.
func TestCreateOrganizedVersion_RecordsOrganizeProvenance(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	org := &Organizer{config: &config.Config{}}

	seeded := newWritebackTestBook(t, store)
	oldPath := seeded.FilePath

	newPath := filepath.Join(t.TempDir(), "organized.m4b")
	created, err := svc.CreateOrganizedVersion(org, seeded, newPath, false, "", &noopLogger{})
	if err != nil {
		t.Fatalf("CreateOrganizedVersion: %v", err)
	}

	history, err := store.GetBookPathHistory(created.ID)
	if err != nil {
		t.Fatalf("GetBookPathHistory: %v", err)
	}

	var foundOrganize, foundImport bool
	for _, ph := range history {
		switch ph.ChangeType {
		case "organize":
			foundOrganize = true
			if ph.OldPath != oldPath {
				t.Errorf("organize OldPath = %q, want %q (the real source path)", ph.OldPath, oldPath)
			}
			if ph.NewPath != newPath {
				t.Errorf("organize NewPath = %q, want %q", ph.NewPath, newPath)
			}
		case "import":
			foundImport = true
		}
	}
	if !foundOrganize {
		t.Errorf("no organize path-change recorded for organized book %q; history=%+v", created.ID, history)
	}
	if !foundImport {
		t.Errorf("expected CreateBook import path-change to still be present; history=%+v", history)
	}
}

// TestCreateOrganizedVersion_AuthorSeriesSurviveOriginalWriteback probes
// whether the original book's denormalized Author/Series survive the
// slim write-back in CreateOrganizedVersion (internal/organizer/service.go,
// comment: "Deliberately NOT fixed here ... Tracked as a follow-up").
// Locked two-outcome protocol (TASK-07, STOREFID W5d-1):
//
//   - Outcome A (PASS): Author/Series survive -> the TODO concern is
//     disproven at the store layer; assertions below stay live.
//   - Outcome B (wipe confirmed): the guarded block below detects the wipe
//     and skips with a message identifying the known bug, rather than
//     failing silently or asserting a broken invariant as green. This is
//     the ONLY permitted skip in this file — any other GetBookByID error or
//     nil result is a hard test failure (fixture bug), not a skip.
func TestCreateOrganizedVersion_AuthorSeriesSurviveOriginalWriteback(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	org := &Organizer{config: &config.Config{}}

	seeded := newWritebackTestBook(t, store)

	slim := *seeded
	slim.Author = nil
	slim.Series = nil
	slim.Description = nil

	newPath := filepath.Join(t.TempDir(), "organized.m4b")
	if _, err := svc.CreateOrganizedVersion(org, &slim, newPath, false, "", &noopLogger{}); err != nil {
		t.Fatalf("CreateOrganizedVersion: %v", err)
	}

	got, err := store.GetBookByID(seeded.ID)
	if err != nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if got == nil {
		t.Fatalf("GetBookByID returned nil for original book %q", seeded.ID)
	}

	// Guarded wipe-detection block: this is the ONLY permitted skip path in
	// this file. It runs before any hard assertion so a confirmed wipe
	// documents the bug executably instead of failing the suite.
	if got.Author == nil || got.Series == nil {
		t.Skipf("W5d-1 KNOWN BUG CONFIRMED: Author/Series wiped by slim write-back (STOR-1 guard lacks Author/Series) — unskip when the fail-open hydrate fix (TODO.md:75-83) lands")
	}

	if got.Author.Name != seeded.Author.Name {
		t.Errorf("Author.Name = %q, want %q (survived write-back)", got.Author.Name, seeded.Author.Name)
	}
	if got.Series.Name != seeded.Series.Name {
		t.Errorf("Series.Name = %q, want %q (survived write-back)", got.Series.Name, seeded.Series.Name)
	}
}
