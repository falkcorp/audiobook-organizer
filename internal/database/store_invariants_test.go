// file: internal/database/store_invariants_test.go
// version: 1.1.0
// guid: c2e3f4a5-6b7c-8d9e-0f1a-storeinvar0001
// last-edited: 2026-07-13

package database

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// assertStoreInvariants is the white-box store-consistency checker. It lives in
// `package database` (not internal/database/dbtest) so it can reach the
// unexported *PebbleStore.db for the raw checks that the public helper cannot
// do. It is deliberately NOT importing dbtest — a white-box test importing a
// package that imports `database` would be an import cycle.
//
// Invariants:
//
//	(a) No book is both a live primary version AND MarkedForDeletion.
//	(b) Every work/version-group/path listing resolves to existing, non-soft-
//	    deleted books (a soft/hard-deleted book must never surface as live in a
//	    listing), and every live book is discoverable via its own indexes.
//	(c) No dangling index row: every book:path:, book:work:, book:versiongroup:,
//	    book:isbn10:/isbn13:/asin:, and tag_idx: row references a book that
//	    PHYSICALLY exists (a soft-deleted book still exists and legitimately
//	    keeps its rows; only a HARD-deleted book must have none).
//
// Physical existence is taken from a raw scan of the primary book:<id> rows
// (which includes soft-deleted books), NOT from ListBookIDs — the memdb-backed
// ListBookIDs omits soft-deleted books, so using it here would false-positive a
// soft-deleted book's still-valid index rows as "dangling".
func assertStoreInvariants(t *testing.T, store Store) {
	t.Helper()
	ps, ok := store.(*PebbleStore)
	if !ok {
		t.Fatalf("invariant: store is not *PebbleStore: %T", store)
	}

	// Physical book set (includes soft-deleted) via raw primary-row scan.
	all := map[string]*Book{}
	{
		iter := mustIter(t, ps, "book:0", "book:;")
		defer iter.Close()
		for iter.First(); iter.Valid(); iter.Next() {
			key := string(iter.Key())
			// Primary key form is exactly "book:<ulid>" — one colon, colon-free id.
			rest := strings.TrimPrefix(key, "book:")
			if strings.IndexByte(rest, ':') >= 0 {
				continue // secondary-index key (book:path:, book:work:, …)
			}
			var b Book
			if err := json.Unmarshal(iter.Value(), &b); err != nil {
				t.Fatalf("invariant: unmarshal %q: %v", key, err)
			}
			all[rest] = &b
		}
	}

	live := map[string]*Book{}
	for id, b := range all {
		marked := b.MarkedForDeletion != nil && *b.MarkedForDeletion
		if marked && b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			t.Errorf("invariant (a): book %s is a live primary version AND MarkedForDeletion", id)
		}
		if !marked {
			live[id] = b
		}
	}

	// (b) index listings return only existing, non-soft-deleted books; live
	// books appear in their own listings.
	seenWork, seenVG := map[string]bool{}, map[string]bool{}
	for _, b := range all {
		if b.WorkID != nil && *b.WorkID != "" && !seenWork[*b.WorkID] {
			seenWork[*b.WorkID] = true
			got, err := store.GetBooksByWorkID(*b.WorkID)
			if err != nil {
				t.Fatalf("invariant: GetBooksByWorkID: %v", err)
			}
			assertListingLiveWB(t, "work "+*b.WorkID, got, all)
		}
		if b.VersionGroupID != nil && *b.VersionGroupID != "" && !seenVG[*b.VersionGroupID] {
			seenVG[*b.VersionGroupID] = true
			got, err := store.GetBooksByVersionGroup(*b.VersionGroupID)
			if err != nil {
				t.Fatalf("invariant: GetBooksByVersionGroup: %v", err)
			}
			assertListingLiveWB(t, "versiongroup "+*b.VersionGroupID, got, all)
		}
	}
	for id, b := range live {
		if b.WorkID != nil && *b.WorkID != "" {
			got, _ := store.GetBooksByWorkID(*b.WorkID)
			if !containsIDWB(got, id) {
				t.Errorf("invariant (b): live book %s absent from work listing %s", id, *b.WorkID)
			}
		}
		if b.FilePath != "" {
			byPath, err := store.GetBookByFilePath(b.FilePath)
			if err != nil {
				t.Fatalf("invariant: GetBookByFilePath: %v", err)
			}
			if byPath == nil || byPath.ID != id {
				t.Errorf("invariant (b): path index for %q did not resolve back to book %s (got %v)", b.FilePath, id, byPath)
			}
		}
	}

	// (c) No dangling secondary-index rows. Book must physically exist in `all`.
	exists := func(id string) bool { return all[id] != nil }
	scanValueIsID(t, ps, "book:path:", exists)
	for _, prefix := range []string{
		"book:work:",
		"book:versiongroup:",
		"book:isbn10:",
		"book:isbn13:",
		"book:asin:",
		"tag_idx:",
	} {
		scanTrailingKeyIsID(t, ps, prefix, exists)
	}
}

func assertListingLiveWB(t *testing.T, label string, got []Book, all map[string]*Book) {
	t.Helper()
	for i := range got {
		b := &got[i]
		stored, ok := all[b.ID]
		if !ok {
			t.Errorf("invariant (b): listing %s returned unknown/hard-deleted book %s", label, b.ID)
			continue
		}
		if stored.MarkedForDeletion != nil && *stored.MarkedForDeletion {
			t.Errorf("invariant (b): listing %s returned soft-deleted book %s as live", label, b.ID)
		}
	}
}

func containsIDWB(books []Book, id string) bool {
	for i := range books {
		if books[i].ID == id {
			return true
		}
	}
	return false
}

func scanValueIsID(t *testing.T, ps *PebbleStore, prefix string, exists func(string) bool) {
	t.Helper()
	iter := mustIterPrefix(t, ps, prefix)
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		id := string(iter.Value())
		if id != "" && !exists(id) {
			t.Errorf("invariant (c): dangling %s row %q → nonexistent book %q", prefix, string(iter.Key()), id)
		}
	}
}

func scanTrailingKeyIsID(t *testing.T, ps *PebbleStore, prefix string, exists func(string) bool) {
	t.Helper()
	iter := mustIterPrefix(t, ps, prefix)
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		idx := strings.LastIndex(key, ":")
		if idx < 0 || idx+1 >= len(key) {
			continue
		}
		id := key[idx+1:]
		if !exists(id) {
			t.Errorf("invariant (c): dangling %s row %q → nonexistent book %q", prefix, key, id)
		}
	}
}

func mustIterPrefix(t *testing.T, ps *PebbleStore, prefix string) *pebble.Iterator {
	t.Helper()
	ub := append([]byte(nil), []byte(prefix)...)
	ub[len(ub)-1]++ // prefix end
	return mustIter(t, ps, prefix, string(ub))
}

func mustIter(t *testing.T, ps *PebbleStore, lower, upper string) *pebble.Iterator {
	t.Helper()
	iter, err := ps.db.NewIter(&pebble.IterOptions{LowerBound: []byte(lower), UpperBound: []byte(upper)})
	if err != nil {
		t.Fatalf("invariant: NewIter(%q,%q): %v", lower, upper, err)
	}
	return iter
}

// TestStoreInvariants_CleanStore proves the helper passes on a normal store
// with live books, a shared work ID, and tags — a baseline so a later failure
// means a real inconsistency, not a broken assertion.
func TestStoreInvariants_CleanStore(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	wid := "WORK0000000000000000000AAA"
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		created, err := store.CreateBook(&Book{Title: title, FilePath: "/lib/" + title + ".m4b", WorkID: strPtr(wid)})
		if err != nil {
			t.Fatalf("CreateBook: %v", err)
		}
		if err := store.SetBookTags(created.ID, []string{"genre:fantasy", "favorite"}); err != nil {
			t.Fatalf("SetBookTags: %v", err)
		}
	}
	assertStoreInvariants(t, store)
}

// TestStoreInvariants_SoftThenHardDelete exercises the delete/soft-delete paths.
// A soft-deleted book still physically exists and keeps its index rows (must NOT
// be flagged); a hard-deleted book must have its row + all index rows gone.
func TestStoreInvariants_SoftThenHardDelete(t *testing.T) {
	store, cleanup := setupPebbleTestDB(t)
	defer cleanup()

	wid := "WORK0000000000000000000BBB"
	a, err := store.CreateBook(&Book{Title: "Keeper", FilePath: "/lib/keeper.m4b", WorkID: strPtr(wid)})
	if err != nil {
		t.Fatalf("CreateBook a: %v", err)
	}
	b, err := store.CreateBook(&Book{Title: "Doomed", FilePath: "/lib/doomed.m4b", WorkID: strPtr(wid)})
	if err != nil {
		t.Fatalf("CreateBook b: %v", err)
	}

	// Soft-delete b via UpdateBook (mirrors merge.SoftDeleteBook). b still exists.
	upd := *b
	tru := true
	upd.MarkedForDeletion = &tru
	if _, err := store.UpdateBook(b.ID, &upd); err != nil {
		t.Fatalf("soft-delete UpdateBook: %v", err)
	}
	assertStoreInvariants(t, store)
	if got, _ := store.GetBooksByWorkID(wid); containsIDWB(got, b.ID) {
		t.Errorf("soft-deleted book %s still live in work listing", b.ID)
	}

	// Hard-delete b: row + index rows must be gone (no dangling rows).
	if err := store.DeleteBook(b.ID); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	assertStoreInvariants(t, store)
	if got, err := store.GetBookByID(a.ID); err != nil || got == nil {
		t.Fatalf("survivor a lost: err=%v got=%v", err, got)
	}
}
