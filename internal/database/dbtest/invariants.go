// file: internal/database/dbtest/invariants.go
// version: 1.0.0
// guid: b1d2f3a4-5c6e-7a8b-9c0d-invariantsdbt1
// last-edited: 2026-07-13

// Package dbtest holds cross-package TEST-ONLY helpers for asserting
// data-loss / store-consistency invariants against a real database.Store.
//
// It lives in its own package (not a _test.go file) for exactly one reason:
// the store-invariant assertion must be callable from BOTH the database
// package's white-box tests and the merge package's tests. A white-box
// (`package database`) test file cannot import a helper that imports
// `database` without creating an import cycle, so the shared, PUBLIC-API-only
// portion of the invariant check lives here where any test package can reach
// it. Nothing in the production build imports this package.
//
// The checks here use ONLY the exported database.Store surface, so they are
// portable but cannot see raw secondary-index rows. Two limitations follow from
// the public surface and are covered instead by the database package's own
// white-box helper (assertStoreInvariants in store_invariants_test.go):
//   - ListBookIDs (memdb-backed) omits soft-deleted books, so invariant (a)
//     ("live primary AND MarkedForDeletion") is effectively vacuous here — a
//     book in that contradictory state would not be enumerated. The white-box
//     helper scans raw primary rows and checks it properly.
//   - The "no dangling index row" check needs the unexported *PebbleStore.db.
//
// What this helper DOES robustly verify (via GetBookByID, which returns
// soft-deleted rows too): index listings never surface a soft-deleted or
// non-existent book as live, and every live book is discoverable via its
// own indexes.
package dbtest

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// AssertStoreInvariants scans every book reachable via the public Store API
// and asserts the store-consistency invariants that guard the data-loss bug
// classes fixed on main:
//
//	(a) No book is BOTH a live primary version AND MarkedForDeletion — a merge
//	    that half-applied could leave a row in this contradictory state.
//	(b) Every secondary-index-backed listing (work, version-group, file-path,
//	    iTunes persistent ID) resolves to books that actually exist and whose
//	    soft/hard-delete state matches — i.e. a soft-deleted or hard-deleted
//	    book must never surface as live in an index listing, and a live book
//	    must be discoverable through its own indexes.
//
// It is intentionally cheap (a ListBookIDs + point reads) so it can run inline
// at the end of merge/combine/delete tests without meaningfully slowing them.
func AssertStoreInvariants(tb testing.TB, store database.Store) {
	tb.Helper()

	ids, err := store.ListBookIDs()
	if err != nil {
		tb.Fatalf("invariant: ListBookIDs: %v", err)
	}

	live := map[string]*database.Book{}
	all := map[string]*database.Book{}
	for _, id := range ids {
		b, err := store.GetBookByID(id)
		if err != nil {
			tb.Fatalf("invariant: GetBookByID(%s): %v", id, err)
		}
		if b == nil {
			// ListBookIDs returned an ID that no longer resolves — a dangling
			// primary listing. That is itself a consistency violation.
			tb.Errorf("invariant (b): ListBookIDs returned %s but GetBookByID is nil (dangling primary)", id)
			continue
		}
		all[id] = b
		marked := b.MarkedForDeletion != nil && *b.MarkedForDeletion

		// (a) live-primary && marked-for-deletion is contradictory.
		if marked && b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			tb.Errorf("invariant (a): book %s is a live primary version AND MarkedForDeletion", id)
		}
		if !marked {
			live[id] = b
		}
	}

	// (b) Nothing an index listing returns may be soft-deleted or non-existent,
	// and any book it returns must resolve. Check the work + version-group
	// listings, which historically embedded stale Book snapshots.
	seenWork := map[string]bool{}
	seenVG := map[string]bool{}
	for _, b := range all {
		if b.WorkID != nil && *b.WorkID != "" && !seenWork[*b.WorkID] {
			seenWork[*b.WorkID] = true
			got, err := store.GetBooksByWorkID(*b.WorkID)
			if err != nil {
				tb.Fatalf("invariant: GetBooksByWorkID(%s): %v", *b.WorkID, err)
			}
			assertListingLive(tb, store, "work "+*b.WorkID, got)
		}
		if b.VersionGroupID != nil && *b.VersionGroupID != "" && !seenVG[*b.VersionGroupID] {
			seenVG[*b.VersionGroupID] = true
			got, err := store.GetBooksByVersionGroup(*b.VersionGroupID)
			if err != nil {
				tb.Fatalf("invariant: GetBooksByVersionGroup(%s): %v", *b.VersionGroupID, err)
			}
			assertListingLive(tb, store, "versiongroup "+*b.VersionGroupID, got)
		}
	}

	// (b) A LIVE book must be discoverable by its own work/version-group listing.
	for id, b := range live {
		if b.WorkID != nil && *b.WorkID != "" {
			got, _ := store.GetBooksByWorkID(*b.WorkID)
			if !containsID(got, id) {
				tb.Errorf("invariant (b): live book %s absent from its own work listing %s", id, *b.WorkID)
			}
		}
		if b.VersionGroupID != nil && *b.VersionGroupID != "" {
			got, _ := store.GetBooksByVersionGroup(*b.VersionGroupID)
			if !containsID(got, id) {
				tb.Errorf("invariant (b): live book %s absent from its own version-group listing %s", id, *b.VersionGroupID)
			}
		}
		// (b) The file-path index must resolve to a book carrying that exact path.
		if b.FilePath != "" {
			byPath, err := store.GetBookByFilePath(b.FilePath)
			if err != nil {
				tb.Fatalf("invariant: GetBookByFilePath(%s): %v", b.FilePath, err)
			}
			if byPath == nil {
				tb.Errorf("invariant (b): live book %s path %q not resolvable via path index", id, b.FilePath)
			} else if byPath.FilePath != b.FilePath {
				tb.Errorf("invariant (b): path index for %q resolved to a book with path %q", b.FilePath, byPath.FilePath)
			}
		}
		// (b) The iTunes-persistent-ID lookup must resolve to an existing book.
		if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
			byPID, err := store.GetBookByITunesPersistentID(*b.ITunesPersistentID)
			if err != nil {
				tb.Fatalf("invariant: GetBookByITunesPersistentID(%s): %v", *b.ITunesPersistentID, err)
			}
			if byPID == nil {
				tb.Errorf("invariant (b): live book %s iTunes PID %q not resolvable", id, *b.ITunesPersistentID)
			}
		}
	}
}

// assertListingLive asserts every book an index listing returned still exists
// and is not soft-deleted. Existence is re-checked via GetBookByID (which
// returns soft-deleted rows too) rather than the ListBookIDs-derived set, since
// ListBookIDs omits soft-deleted books and would misclassify them.
func assertListingLive(tb testing.TB, store database.Store, label string, got []database.Book) {
	tb.Helper()
	for i := range got {
		b := &got[i]
		stored, err := store.GetBookByID(b.ID)
		if err != nil {
			tb.Fatalf("invariant: GetBookByID(%s): %v", b.ID, err)
		}
		if stored == nil {
			tb.Errorf("invariant (b): listing %s returned hard-deleted/nonexistent book %s as live", label, b.ID)
			continue
		}
		if stored.MarkedForDeletion != nil && *stored.MarkedForDeletion {
			tb.Errorf("invariant (b): listing %s returned soft-deleted book %s as live", label, b.ID)
		}
	}
}

func containsID(books []database.Book, id string) bool {
	for i := range books {
		if books[i].ID == id {
			return true
		}
	}
	return false
}
