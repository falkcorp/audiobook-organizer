// file: internal/reconcile/reconcile_parallel_test.go
// version: 1.1.0
// guid: 2c7f1a94-3e60-4d18-9b5a-8f0c6d2e1a37
// last-edited: 2026-07-16

package reconcile

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeReconcileStore is a minimal, concurrency-safe Store double. It embeds the
// full database.Store interface so it satisfies reconcile.Store (a subset);
// only the handful of methods the parallelized loops touch are implemented, and
// any other call would nil-panic (it never happens in these tests). The mutex
// guards the maps against the worker pools so `go test -race` exercises the
// concurrent access. `books` is read-only during a run.
type fakeReconcileStore struct {
	database.Store
	books   []database.BookCore
	files   map[string][]database.BookFile
	byID    map[string]*database.Book
	updated map[string]*database.Book
	mu      chan struct{} // 1-slot semaphore used as a mutex
}

func newFakeStore() *fakeReconcileStore {
	return &fakeReconcileStore{
		files:   map[string][]database.BookFile{},
		byID:    map[string]*database.Book{},
		updated: map[string]*database.Book{},
		mu:      make(chan struct{}, 1),
	}
}

func (f *fakeReconcileStore) lock()   { f.mu <- struct{}{} }
func (f *fakeReconcileStore) unlock() { <-f.mu }

func (f *fakeReconcileStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	return f.books, nil
}

func (f *fakeReconcileStore) GetAllImportPaths() ([]database.ImportPath, error) {
	return nil, nil
}

func (f *fakeReconcileStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	f.lock()
	defer f.unlock()
	return f.files[bookID], nil
}

func (f *fakeReconcileStore) GetBookByID(id string) (*database.Book, error) {
	f.lock()
	defer f.unlock()
	return f.byID[id], nil
}

func (f *fakeReconcileStore) UpdateBook(id string, book *database.Book) (*database.Book, error) {
	f.lock()
	defer f.unlock()
	f.updated[id] = book
	return book, nil
}

// TestFindBrokenSegmentBooks_ParallelOrderAndCounts verifies the parallelized
// FindBrokenSegmentBooks: Details must come out in book order (not scrambled by
// the worker pool), the counters must be exact, and the non-dry-run write path
// must mark exactly the broken books. Broken indices are deliberately
// non-contiguous {1,3,4,6} so a concurrency-scrambled fold would be detected.
// Run under -race to exercise the pool.
func TestFindBrokenSegmentBooks_ParallelOrderAndCounts(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = "" // disable the library-prefix skip; check all books
	defer func() { config.AppConfig.RootDir = prevRoot }()

	base := t.TempDir()
	const n = 8
	brokenSet := map[int]bool{1: true, 3: true, 4: true, 6: true}

	store := newFakeStore()
	var wantBrokenIDs []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("book-%02d", i)
		dir := filepath.Join(base, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Segment 0 always present on disk.
		seg0 := writeFile(t, dir, "seg0.m4b", "s0")
		var seg1 string
		if brokenSet[i] {
			seg1 = filepath.Join(dir, "seg1-missing.m4b") // never written → missing
			wantBrokenIDs = append(wantBrokenIDs, id)
		} else {
			seg1 = writeFile(t, dir, "seg1.m4b", "s1")
		}
		store.books = append(store.books, database.BookCore{ID: id, Title: id, FilePath: dir})
		store.files[id] = []database.BookFile{
			{FilePath: seg0},
			{FilePath: seg1},
		}
		store.byID[id] = &database.Book{ID: id, Title: id, FilePath: dir}
	}

	// --- dry run: no writes, but full detection + ordering + counts ---
	res, err := FindBrokenSegmentBooks(store, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if res.BooksChecked != n {
		t.Errorf("BooksChecked = %d, want %d", res.BooksChecked, n)
	}
	if res.BrokenBooks != len(wantBrokenIDs) {
		t.Errorf("BrokenBooks = %d, want %d", res.BrokenBooks, len(wantBrokenIDs))
	}
	if res.MarkedForReview != 0 {
		t.Errorf("MarkedForReview = %d, want 0 on dry run", res.MarkedForReview)
	}
	if len(store.updated) != 0 {
		t.Errorf("dry run wrote %d books, want 0", len(store.updated))
	}
	gotIDs := make([]string, len(res.Details))
	for i, d := range res.Details {
		gotIDs[i] = d.BookID
	}
	if !equalStrings(gotIDs, wantBrokenIDs) {
		t.Errorf("Details order = %v, want %v (must match book order, not pool order)", gotIDs, wantBrokenIDs)
	}

	// --- real run: same detection, plus each broken book marked needs_review ---
	store2 := newFakeStore()
	store2.books = store.books
	store2.files = store.files
	for _, b := range store.books {
		bk := *store.byID[b.ID]
		store2.byID[b.ID] = &bk
	}
	res2, err := FindBrokenSegmentBooks(store2, false)
	if err != nil {
		t.Fatalf("real run: %v", err)
	}
	if res2.MarkedForReview != len(wantBrokenIDs) {
		t.Errorf("MarkedForReview = %d, want %d", res2.MarkedForReview, len(wantBrokenIDs))
	}
	if len(store2.updated) != len(wantBrokenIDs) {
		t.Errorf("wrote %d books, want %d", len(store2.updated), len(wantBrokenIDs))
	}
	for _, id := range wantBrokenIDs {
		b, ok := store2.updated[id]
		if !ok {
			t.Errorf("broken book %s was not marked", id)
			continue
		}
		if b.LibraryState == nil || *b.LibraryState != "needs_review" {
			t.Errorf("book %s LibraryState = %v, want needs_review", id, b.LibraryState)
		}
		if b.MarkedForDeletion == nil || !*b.MarkedForDeletion {
			t.Errorf("book %s MarkedForDeletion not set", id)
		}
	}
}

// TestBuildReconcilePreview_ParallelBrokenOrder verifies the parallelized
// path-check loop in BuildReconcilePreviewWithProgress: BrokenRecords must list
// exactly the books whose FilePath is missing on disk, in book order, even
// though the stats run concurrently. With no import paths/root/iTunes dirs,
// FindUntrackedFiles returns nothing and the preview returns the broken records
// as UnmatchedBooks — enough to assert on the parallel fold. Broken indices
// {0,2,5} are non-contiguous.
func TestBuildReconcilePreview_ParallelBrokenOrder(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	prevITunes := config.AppConfig.ITunes.LibraryReadPath
	config.AppConfig.RootDir = ""
	config.AppConfig.ITunes.LibraryReadPath = ""
	defer func() {
		config.AppConfig.RootDir = prevRoot
		config.AppConfig.ITunes.LibraryReadPath = prevITunes
	}()

	base := t.TempDir()
	const n = 7
	brokenSet := map[int]bool{0: true, 2: true, 5: true}

	store := newFakeStore()
	var wantBrokenIDs []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("bk-%02d", i)
		var path string
		if brokenSet[i] {
			path = filepath.Join(base, id+"-missing.m4b") // never created → broken
			wantBrokenIDs = append(wantBrokenIDs, id)
		} else {
			path = writeFile(t, base, id+".m4b", "present")
		}
		store.books = append(store.books, database.BookCore{ID: id, Title: id, FilePath: path})
	}

	res, err := BuildReconcilePreview(store)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	gotIDs := make([]string, len(res.BrokenRecords))
	for i, r := range res.BrokenRecords {
		gotIDs[i] = r.BookID
	}
	if !equalStrings(gotIDs, wantBrokenIDs) {
		t.Errorf("BrokenRecords order = %v, want %v (book order, not pool order)", gotIDs, wantBrokenIDs)
	}
}

// TestFindBrokenSegmentBooks_RealStoreConcurrent drives the non-dry-run write
// path against a REAL PebbleStore (not the mutex-guarded fake) so `go test
// -race` exercises concurrent GetBookByID / GetBookFiles / UpdateBook —
// including the memdb write-through — across the worker pool. This is the
// check that actually proves the production behavior change is safe: a
// concurrent unlocked-map access in the store would surface here as a race or a
// fatal panic. Every book is broken, so every worker performs a hydrate+update,
// maximizing write concurrency.
func TestFindBrokenSegmentBooks_RealStoreConcurrent(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("new pebble store: %v", err)
	}

	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = "" // disable the library-prefix skip; check all books
	defer func() { config.AppConfig.RootDir = prevRoot }()

	base := t.TempDir()
	const n = 40 // enough books to keep NumCPU workers genuinely contending
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		dir := filepath.Join(base, fmt.Sprintf("book-%02d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		seg0 := writeFile(t, dir, "seg0.m4b", "s0")             // present
		seg1 := filepath.Join(dir, "seg1-missing.m4b")         // never written → missing
		book, err := ps.CreateBook(&database.Book{Title: fmt.Sprintf("book %d", i), FilePath: dir})
		if err != nil {
			t.Fatalf("create book: %v", err)
		}
		ids = append(ids, book.ID)
		for _, fp := range []string{seg0, seg1} {
			if err := ps.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: fp}); err != nil {
				t.Fatalf("create book file: %v", err)
			}
		}
	}

	res, err := FindBrokenSegmentBooks(ps, false)
	if err != nil {
		t.Fatalf("FindBrokenSegmentBooks: %v", err)
	}
	if res.BooksChecked != n {
		t.Errorf("BooksChecked = %d, want %d", res.BooksChecked, n)
	}
	if res.BrokenBooks != n {
		t.Errorf("BrokenBooks = %d, want %d", res.BrokenBooks, n)
	}
	if res.MarkedForReview != n {
		t.Errorf("MarkedForReview = %d, want %d", res.MarkedForReview, n)
	}
	// Confirm the writes actually persisted through the store (not just counted).
	for _, id := range ids {
		b, err := ps.GetBookByID(id)
		if err != nil || b == nil {
			t.Fatalf("re-read %s: %v", id, err)
		}
		if b.LibraryState == nil || *b.LibraryState != "needs_review" {
			t.Errorf("book %s LibraryState = %v, want needs_review", id, b.LibraryState)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
