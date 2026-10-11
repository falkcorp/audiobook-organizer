// file: internal/metafetch/openlibrary_close_recovery_test.go
// version: 1.0.0
// guid: 5e1c7a39-84d2-4b06-9f3a-c2d8e60b1a47
// last-edited: 2026-10-11

package metafetch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
)

// openStoreWithHeldIterator opens an Open Library store at <dir>/oldb on an
// engine the test owns, and returns it with an iterator held on that engine:
// a lookup or import that has not finished, so the store's Close cannot
// drain.
func openStoreWithHeldIterator(t *testing.T, dir string) (*openlibrary.OLStore, *database.GatedIter) {
	t.Helper()
	db, err := pebble.Open(filepath.Join(dir, "oldb"), &pebble.Options{FormatMajorVersion: pebble.FormatNewest})
	if err != nil {
		t.Fatal(err)
	}
	gated, closeDB := database.NewOwnedGatedDB(db)
	if err := gated.Set([]byte("ol:work:held"), []byte(`{"key":"held"}`), pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	it, err := gated.NewIter(&pebble.IterOptions{})
	if err != nil {
		t.Fatal(err)
	}
	it.First()
	return openlibrary.NewOLStoreOnEngine(gated, closeDB), it
}

// A store whose Close times out refuses all work but still holds its engine
// and the directory's lock file. The service must keep it, so that a retried
// close (delete, factory reset) cannot report success while the engine is
// open, and so that the next EnsureStore can finish the close and reopen the
// store instead of failing with "lock held by current process" until the
// process restarts.
func TestOpenLibraryService_TimedOutCloseRecoversOnceTheStragglerReleases(t *testing.T) {
	dir := t.TempDir()
	store, held := openStoreWithHeldIterator(t, dir)
	svc := &OpenLibraryService{OLStore: store, CloseTimeout: 50 * time.Millisecond}

	svc.Mu.Lock()
	err := svc.CloseStoreLocked()
	svc.Mu.Unlock()
	if err == nil {
		t.Fatal("CloseStoreLocked with a lookup in flight returned nil")
	}
	if svc.OLStore != nil {
		t.Fatal("a store that refuses all work was left as the live store")
	}

	// Retrying while the straggler still holds the engine must fail again: a
	// nil here would let a delete go on to remove files under a live engine.
	svc.Mu.Lock()
	err = svc.CloseStoreLocked()
	svc.Mu.Unlock()
	if err == nil {
		t.Fatal("a retried close reported success while the engine was still held open")
	}

	// EnsureStore says why it cannot reopen, instead of surfacing the
	// engine's lock-file error.
	if err := svc.EnsureStore(dir); err == nil || !strings.Contains(err.Error(), "has not finished closing") {
		t.Fatalf("EnsureStore with the previous engine still open returned %v; want a has-not-finished-closing error", err)
	}
	if svc.OLStore != nil {
		t.Fatal("EnsureStore installed a store while the previous engine was still open")
	}

	if err := held.Close(); err != nil {
		t.Fatalf("releasing the straggler: %v", err)
	}
	if err := svc.EnsureStore(dir); err != nil {
		t.Fatalf("EnsureStore after the straggler released: %v", err)
	}
	if svc.OLStore == nil {
		t.Fatal("EnsureStore left no store")
	}
	if _, err := svc.OLStore.GetStatus(); err != nil {
		t.Fatalf("the reopened store is not usable: %v", err)
	}
	svc.Close()
	if svc.OLStore != nil || svc.pendingClose != nil {
		t.Fatal("Close left a store behind")
	}
}

// Once the straggler has released, a plain retried close (the delete handler's
// path) finishes the job and reports success.
func TestOpenLibraryService_RetriedCloseSucceedsAfterStragglerReleases(t *testing.T) {
	store, held := openStoreWithHeldIterator(t, t.TempDir())
	svc := &OpenLibraryService{OLStore: store, CloseTimeout: 50 * time.Millisecond}

	svc.Mu.Lock()
	defer svc.Mu.Unlock()
	if err := svc.CloseStoreLocked(); err == nil {
		t.Fatal("first close with a lookup in flight returned nil")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.CloseStoreLocked(); err != nil {
		t.Fatalf("retried close after the straggler released: %v", err)
	}
	if err := svc.CloseStoreLocked(); err != nil {
		t.Fatalf("close with nothing left to close: %v", err)
	}
}

// No store at all is not an error.
func TestOpenLibraryService_CloseStoreLockedWithNoStore(t *testing.T) {
	svc := &OpenLibraryService{}
	svc.Mu.Lock()
	defer svc.Mu.Unlock()
	if err := svc.CloseStoreLocked(); err != nil {
		t.Fatalf("CloseStoreLocked on an empty service: %v", err)
	}
}
