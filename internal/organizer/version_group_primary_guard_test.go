// file: internal/organizer/version_group_primary_guard_test.go
// version: 1.0.0
// guid: 7c41b8d6-2e59-4a03-b18f-6d0a3e95c247
// last-edited: 2026-08-13

// Regression tests for the surplus-primary defect found 2026-08-13.
//
// CreateOrganizedVersion marked every organized copy primary and demoted only
// its own source row. When a book joined a version group that ALREADY had an
// organized primary — routine, because the scanner hash-matches a newly
// downloaded copy into the existing book's group — the group ended up with two
// primaries. Production held 10,780 such groups and 10,798 surplus primary
// rows. See
// docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md

package organizer

import (
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// countPrimariesInGroup re-derives the answer from stored rows rather than from
// anything CreateOrganizedVersion reported, so the assertion cannot be
// satisfied by the code under test merely claiming success.
func countPrimariesInGroup(t *testing.T, store *database.PebbleStore, gid string) (primaries int, members int) {
	t.Helper()
	rows, err := store.GetBooksByVersionGroup(gid)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup(%q): %v", gid, err)
	}
	for i := range rows {
		members++
		if rows[i].IsPrimaryVersion != nil && *rows[i].IsPrimaryVersion {
			primaries++
		}
	}
	return primaries, members
}

// TestCreateOrganizedVersion_DoesNotAddASecondPrimary is the headline case: a
// group that already elects an organized primary must still elect exactly one
// after a second book in that group is organized.
//
// The incumbent must stay primary and the newcomer must yield. That direction
// matters in production: the incumbent has been through metadata enrichment and
// sits under a real author directory, while the newcomer is frequently still
// "Unknown Author" because organize can run before enrichment completes.
func TestCreateOrganizedVersion_DoesNotAddASecondPrimary(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	org := &Organizer{config: &config.Config{}}

	gid := "test-vg-existing-primary"
	yes, no := true, false
	organized, organizedSource := "organized", "imported"

	// The incumbent: an already-organized primary sitting in the group.
	incumbent, err := store.CreateBook(&database.Book{
		Title:            "Incumbent Organized Copy",
		FilePath:         filepath.Join(t.TempDir(), "real-author", "incumbent.m4b"),
		VersionGroupID:   &gid,
		IsPrimaryVersion: &yes,
		LibraryState:     &organized,
	})
	if err != nil {
		t.Fatalf("CreateBook(incumbent): %v", err)
	}

	// The newcomer: a freshly scanned copy hash-matched into the same group.
	newcomer, err := store.CreateBook(&database.Book{
		Title:            "Newcomer Source Copy",
		FilePath:         filepath.Join(t.TempDir(), "newbooks", "newcomer.m4b"),
		VersionGroupID:   &gid,
		IsPrimaryVersion: &no,
		LibraryState:     &organizedSource,
	})
	if err != nil {
		t.Fatalf("CreateBook(newcomer): %v", err)
	}

	// Precondition: exactly one primary before we organize anything. If this is
	// ever 2, the fixture is already broken and the assertion below is vacuous.
	if p, m := countPrimariesInGroup(t, store, gid); p != 1 || m != 2 {
		t.Fatalf("precondition: group has %d primaries / %d members, want 1/2", p, m)
	}

	newPath := filepath.Join(t.TempDir(), "organized-newcomer.m4b")
	created, err := svc.CreateOrganizedVersion(org, newcomer, newPath, false, "", &noopLogger{})
	if err != nil {
		t.Fatalf("CreateOrganizedVersion: %v", err)
	}

	primaries, members := countPrimariesInGroup(t, store, gid)
	if members != 3 {
		t.Errorf("group members = %d, want 3 (incumbent + newcomer + organized copy)", members)
	}
	if primaries != 1 {
		t.Errorf("group has %d primaries, want exactly 1 — a version group must never elect more than one", primaries)
	}

	// The incumbent specifically must be the survivor.
	got, err := store.GetBookByID(incumbent.ID)
	if err != nil || got == nil {
		t.Fatalf("GetBookByID(incumbent): %v", err)
	}
	if got.IsPrimaryVersion == nil || !*got.IsPrimaryVersion {
		t.Errorf("incumbent was demoted; it must remain primary because it is the enriched, correctly-named copy")
	}

	// ...and the newly organized record must have yielded.
	newRow, err := store.GetBookByID(created.ID)
	if err != nil || newRow == nil {
		t.Fatalf("GetBookByID(new organized copy): %v", err)
	}
	if newRow.IsPrimaryVersion != nil && *newRow.IsPrimaryVersion {
		t.Errorf("newly organized copy claimed primary while the group already had one")
	}
}

// TestCreateOrganizedVersion_StillClaimsPrimaryWhenGroupHasNone guards the
// other direction. The guard must not overcorrect into never electing a
// primary — that is precisely the defect repaired by
// reconcile.ElectMissingPrimaries, and re-introducing it here would hide the
// book from the web UI instead of duplicating it.
func TestCreateOrganizedVersion_StillClaimsPrimaryWhenGroupHasNone(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	org := &Organizer{config: &config.Config{}}

	gid := "test-vg-no-primary"
	no := false
	imported := "imported"

	source, err := store.CreateBook(&database.Book{
		Title:            "Lonely Source",
		FilePath:         filepath.Join(t.TempDir(), "newbooks", "lonely.m4b"),
		VersionGroupID:   &gid,
		IsPrimaryVersion: &no,
		LibraryState:     &imported,
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	if p, _ := countPrimariesInGroup(t, store, gid); p != 0 {
		t.Fatalf("precondition: group already has %d primaries, want 0", p)
	}

	newPath := filepath.Join(t.TempDir(), "organized-lonely.m4b")
	created, err := svc.CreateOrganizedVersion(org, source, newPath, false, "", &noopLogger{})
	if err != nil {
		t.Fatalf("CreateOrganizedVersion: %v", err)
	}

	newRow, err := store.GetBookByID(created.ID)
	if err != nil || newRow == nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if newRow.IsPrimaryVersion == nil || !*newRow.IsPrimaryVersion {
		t.Errorf("organized copy did not claim primary in a group that had none — the book would be invisible in the web UI")
	}
	if p, _ := countPrimariesInGroup(t, store, gid); p != 1 {
		t.Errorf("group has %d primaries after organize, want exactly 1", p)
	}
}

// TestCreateOrganizedVersion_NewGroupStillClaimsPrimary covers the untouched
// path: a book with no version group at all gets a fresh group, and its
// organized copy is that group's primary.
func TestCreateOrganizedVersion_NewGroupStillClaimsPrimary(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	store.WaitForWarmup()
	t.Cleanup(func() { _ = store.Close() })

	svc := NewService(store)
	org := &Organizer{config: &config.Config{}}

	source, err := store.CreateBook(&database.Book{
		Title:    "Ungrouped Book",
		FilePath: filepath.Join(t.TempDir(), "newbooks", "ungrouped.m4b"),
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	newPath := filepath.Join(t.TempDir(), "organized-ungrouped.m4b")
	created, err := svc.CreateOrganizedVersion(org, source, newPath, false, "", &noopLogger{})
	if err != nil {
		t.Fatalf("CreateOrganizedVersion: %v", err)
	}

	newRow, err := store.GetBookByID(created.ID)
	if err != nil || newRow == nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if newRow.VersionGroupID == nil || *newRow.VersionGroupID == "" {
		t.Fatalf("no version group assigned")
	}
	if newRow.IsPrimaryVersion == nil || !*newRow.IsPrimaryVersion {
		t.Errorf("organized copy of an ungrouped book must be its new group's primary")
	}
	if p, _ := countPrimariesInGroup(t, store, *newRow.VersionGroupID); p != 1 {
		t.Errorf("new group has %d primaries, want exactly 1", p)
	}
}
