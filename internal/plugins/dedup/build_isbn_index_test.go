// file: internal/plugins/dedup/build_isbn_index_test.go
// version: 1.0.0
// guid: f1e2d3c4-b5a6-7890-fedc-ba0987654321
// last-edited: 2026-06-14

// End-to-end tests for the build-isbn-index op.
//
// These tests use a real PebbleStore (via a temp dir) to exercise the
// complete flag-write chain: op sets the flag via the store's typed method;
// IsISBNIndexBuilt() reads the same key back.  They exist specifically to
// catch BUG 1 (flag-key mismatch) and BUG 2 (flag set on partial backfill).
package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// isbnIndexStoreAdapter wraps a *PebbleStore so it satisfies database.Store
// (via embedding) while also satisfying ISBNIndexStore (WriteISBNIndexForBook
// + SetISBNIndexBuilt + IsISBNIndexBuilt) and providing GetAllBooks.
// This is the minimal surface the build-isbn-index op needs.
type isbnIndexStoreAdapter struct {
	database.Store // embed for all methods we don't override
	pebble         *database.PebbleStore
}

// WriteISBNIndexForBook satisfies ISBNIndexStore — routes to PebbleStore.
func (a *isbnIndexStoreAdapter) WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error {
	return a.pebble.WriteISBNIndexForBook(bookID, isbn10, isbn13, asin)
}

// SetISBNIndexBuilt satisfies ISBNIndexFlagWriter — routes to PebbleStore.
// This is the method the fixed op must call instead of SetSetting directly.
func (a *isbnIndexStoreAdapter) SetISBNIndexBuilt() error {
	return a.pebble.SetISBNIndexBuilt()
}

// IsISBNIndexBuilt routes to PebbleStore.
func (a *isbnIndexStoreAdapter) IsISBNIndexBuilt() bool {
	return a.pebble.IsISBNIndexBuilt()
}

// GetAllBooks routes to PebbleStore (already embedded, but PebbleStore is
// concrete, not an interface — the embed dispatches correctly).
func (a *isbnIndexStoreAdapter) GetAllBooks(limit, offset int) ([]database.Book, error) {
	return a.pebble.GetAllBooks(limit, offset)
}

// GetSetting / SetSetting route to PebbleStore (used by isFlagSet inside the op).
func (a *isbnIndexStoreAdapter) GetSetting(key string) (*database.Setting, error) {
	return a.pebble.GetSetting(key)
}
func (a *isbnIndexStoreAdapter) SetSetting(key, value, dataType string, internal bool) error {
	return a.pebble.SetSetting(key, value, dataType, internal)
}

// newPebbleForISBNIndexTest opens a fresh PebbleStore in a temp dir.
func newPebbleForISBNIndexTest(t *testing.T) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "isbn-idx-test"))
	if err != nil {
		t.Fatalf("open PebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// makePlugin creates a Plugin whose p.store is an isbnIndexStoreAdapter
// wrapping the given PebbleStore.  engine is nil — build-isbn-index does not
// use the engine.
func makePlugin(pebble *database.PebbleStore) *Plugin {
	adapter := &isbnIndexStoreAdapter{
		Store:  pebble, // embed satisfies the full Store interface
		pebble: pebble,
	}
	return &Plugin{store: adapter}
}

// strPtrDedup returns a pointer to s.
func strPtrDedup(s string) *string { return &s }

// createISBNBook inserts a book with the given ISBN13 and returns its ID.
func createISBNBook(t *testing.T, pebble *database.PebbleStore, title, isbn13 string) string {
	t.Helper()
	b := &database.Book{
		Title:    title,
		FilePath: "/audio/" + title + ".m4b",
		ISBN13:   strPtrDedup(isbn13),
	}
	created, err := pebble.CreateBook(b)
	if err != nil {
		t.Fatalf("CreateBook %q: %v", title, err)
	}
	return created.ID
}

// TestBuildISBNIndex_EndToEnd_FlagAligned is the regression test for BUG 1.
//
// It verifies that after the op runs with apply=true over a real PebbleStore,
// store.IsISBNIndexBuilt() returns true — i.e., the op writes the flag to the
// SAME key that IsISBNIndexBuilt reads.
func TestBuildISBNIndex_EndToEnd_FlagAligned(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)

	// Pre-condition: flag is false on a fresh store.
	if pebble.IsISBNIndexBuilt() {
		t.Fatal("precondition: IsISBNIndexBuilt should be false on a fresh store")
	}

	// Create 2 books sharing an ISBN13.
	const sharedISBN = "9781234567890"
	idA := createISBNBook(t, pebble, "book-a", sharedISBN)
	idB := createISBNBook(t, pebble, "book-b", sharedISBN)

	// Run the op with apply=true.
	p := makePlugin(pebble)
	params := `{"apply":true}`
	err := p.runBuildISBNIndex(context.Background(), json.RawMessage(params), &fakeReporter{})
	if err != nil {
		t.Fatalf("runBuildISBNIndex: %v", err)
	}

	// BUG 1 assertion: IsISBNIndexBuilt must now return true.
	if !pebble.IsISBNIndexBuilt() {
		t.Error("BUG 1: IsISBNIndexBuilt() returned false after op completed with apply=true; " +
			"the flag key written by the op does not match the key IsISBNIndexBuilt reads")
	}

	// Also verify both books are reachable by the index.
	ids, err := pebble.GetBookIDsByISBNASIN("", sharedISBN, "")
	if err != nil {
		t.Fatalf("GetBookIDsByISBNASIN: %v", err)
	}
	containsIDDedup := func(s []string, target string) bool {
		for _, v := range s {
			if v == target {
				return true
			}
		}
		return false
	}
	if !containsIDDedup(ids, idA) {
		t.Errorf("expected idA=%q in index, got %v", idA, ids)
	}
	if !containsIDDedup(ids, idB) {
		t.Errorf("expected idB=%q in index, got %v", idB, ids)
	}
}

// TestBuildISBNIndex_EndToEnd_DryRun verifies that dry-run (apply=false) does
// NOT set the completion flag.
func TestBuildISBNIndex_EndToEnd_DryRun(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)
	createISBNBook(t, pebble, "book-a", "9780000000001")

	p := makePlugin(pebble)
	err := p.runBuildISBNIndex(context.Background(), json.RawMessage(`{}`), &fakeReporter{})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}

	if pebble.IsISBNIndexBuilt() {
		t.Error("dry-run must not set the completion flag")
	}
}

// errWriteISBNStore wraps a PebbleStore but injects write errors for selected book IDs.
type errWriteISBNStore struct {
	database.Store
	pebble     *database.PebbleStore
	failBookID string
}

func (e *errWriteISBNStore) WriteISBNIndexForBook(bookID, isbn10, isbn13, asin string) error {
	if bookID == e.failBookID {
		return errors.New("injected write error")
	}
	return e.pebble.WriteISBNIndexForBook(bookID, isbn10, isbn13, asin)
}
func (e *errWriteISBNStore) SetISBNIndexBuilt() error {
	return e.pebble.SetISBNIndexBuilt()
}
func (e *errWriteISBNStore) IsISBNIndexBuilt() bool {
	return e.pebble.IsISBNIndexBuilt()
}
func (e *errWriteISBNStore) GetAllBooks(limit, offset int) ([]database.Book, error) {
	return e.pebble.GetAllBooks(limit, offset)
}
func (e *errWriteISBNStore) GetSetting(key string) (*database.Setting, error) {
	return e.pebble.GetSetting(key)
}
func (e *errWriteISBNStore) SetSetting(key, value, dataType string, internal bool) error {
	return e.pebble.SetSetting(key, value, dataType, internal)
}

// TestBuildISBNIndex_EndToEnd_PartialFailureDoesNotSetFlag is the regression
// test for BUG 2.
//
// It verifies that when any per-book write fails during apply=true, the
// completion flag is NOT set.  A partial index must not be treated as complete.
func TestBuildISBNIndex_EndToEnd_PartialFailureDoesNotSetFlag(t *testing.T) {
	pebble := newPebbleForISBNIndexTest(t)

	// Create two books; one will fail.
	idA := createISBNBook(t, pebble, "book-a", "9780000000001")
	createISBNBook(t, pebble, "book-b", "9780000000002")

	// Wire a store that fails writes for idA.
	adapter := &errWriteISBNStore{
		Store:      pebble,
		pebble:     pebble,
		failBookID: idA,
	}
	p := &Plugin{store: adapter}

	err := p.runBuildISBNIndex(context.Background(), json.RawMessage(`{"apply":true}`), &fakeReporter{})
	if err != nil {
		t.Fatalf("runBuildISBNIndex with partial failure: %v", err)
	}

	// BUG 2 assertion: flag must NOT be set when there were write failures.
	if pebble.IsISBNIndexBuilt() {
		t.Error("BUG 2: IsISBNIndexBuilt() returned true after a partial write failure; " +
			"the op must not mark the index complete when some books could not be written")
	}
}
