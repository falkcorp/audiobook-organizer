// file: internal/metafetch/openlibrary_close_recovery_test.go
// version: 1.1.1
// guid: 5e1c7a39-84d2-4b06-9f3a-c2d8e60b1a47
// last-edited: 2026-10-11

package metafetch

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
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

// afterStragglerReleased gives the service the production drain timeout back
// once the straggler is gone. The 50 ms used to force a timeout is shorter than
// a real fsync on a loaded disk, which would fail a close that has nothing left
// to wait for.
func afterStragglerReleased(svc *OpenLibraryService) {
	svc.Mu.Lock()
	svc.CloseTimeout = 0
	svc.Mu.Unlock()
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
	svc := &OpenLibraryService{CloseTimeout: 50 * time.Millisecond}
	svc.setStoreLocked(store)

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
	afterStragglerReleased(svc)
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
	svc.CloseTimeout = 0
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

// The metadata lookup path must follow the store through a timed-out close and
// its recovery. Consumers used to be handed the store once at start-up (and
// not at all when there was none then), and the memoized source chain kept
// that pointer: after the old store was closed and a new one opened, every
// local lookup still went to the old, refusing store and fell back to the
// network until the process restarted.
func TestOpenLibraryService_MetadataLookupFollowsRecoveredStore(t *testing.T) {
	dir := t.TempDir()
	store, held := openStoreWithHeldIterator(t, dir)
	svc := &OpenLibraryService{CloseTimeout: 50 * time.Millisecond}
	svc.setStoreLocked(store)

	mfs := &Service{}
	mfs.wireOpenLibrary(svc)
	// A client the way buildSourceChainFromConfig builds it. Its base URL is
	// unroutable, so a lookup that is not answered locally fails.
	client := metadata.NewOpenLibraryClientWithBaseURL("http://127.0.0.1:1")
	client.SetOLStoreSource(mfs.liveOLStore)

	if mfs.liveOLStore() != store {
		t.Fatal("the metadata service is not looking at the Open Library service's store")
	}

	svc.Mu.Lock()
	err := svc.CloseStoreLocked()
	svc.Mu.Unlock()
	if err == nil {
		t.Fatal("close with a lookup in flight returned nil")
	}
	if mfs.liveOLStore() != nil {
		t.Fatal("lookups are still sent to a store that is closing")
	}

	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	afterStragglerReleased(svc)
	if err := svc.EnsureStore(dir); err != nil {
		t.Fatalf("EnsureStore after the straggler released: %v", err)
	}
	t.Cleanup(svc.Close)
	fresh := mfs.liveOLStore()
	if fresh == nil || fresh == store {
		t.Fatalf("lookups were not pointed at the reopened store (got %p, old %p)", fresh, store)
	}

	dump := filepath.Join(dir, "editions.txt.gz")
	f, err := os.Create(dump)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	line := "/type/edition\t/books/OL1M\t5\t2024-01-01\t{\"key\":\"/books/OL1M\",\"title\":\"Recovered Book\",\"isbn_13\":[\"9780000000001\"]}\n"
	if _, err := gz.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := svc.OLStore.ImportDump("editions", dump, nil); err != nil {
		t.Fatalf("import into the reopened store: %v", err)
	}

	meta, err := client.GetBookByISBN(context.Background(), "9780000000001")
	if err != nil {
		t.Fatalf("lookup through the metadata client after recovery: %v", err)
	}
	if meta == nil || meta.Title != "Recovered Book" {
		t.Fatalf("lookup returned %+v; want the edition from the reopened store", meta)
	}
}
