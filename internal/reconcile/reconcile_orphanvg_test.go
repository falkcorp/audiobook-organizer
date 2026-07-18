// file: internal/reconcile/reconcile_orphanvg_test.go
// version: 1.0.0
// guid: 7a1c9e2d-4f6b-4a3e-9d0c-5b8e2f1a6c3d
// last-edited: 2026-07-18

package reconcile

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// TestAssignOrphanVGs_PoolCorrectness drives AssignOrphanVGs across a worker
// pool of orphan books (no VersionGroupID, in the library dir) plus a
// not-in-library book and an already-grouped book, and verifies: every orphan
// gets a unique VersionGroupID + IsPrimaryVersion=true + LibraryState set,
// the counters are exact, and nothing outside the intended candidate set was
// touched. Run with -race to exercise the pool.
func TestAssignOrphanVGs_PoolCorrectness(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	defer func() { config.AppConfig.RootDir = prevRoot }()

	const libRoot = "/lib"
	const n = 40 // enough to keep NumCPU workers genuinely contending

	store := newFakeStore()
	var orphanIDs []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("orphan-%02d", i)
		orphanIDs = append(orphanIDs, id)
		path := fmt.Sprintf("%s/%s.m4b", libRoot, id)
		store.books = append(store.books, database.BookCore{ID: id, Title: id, FilePath: path})
		store.byID[id] = &database.Book{ID: id, Title: id, FilePath: path}
	}

	// Already-grouped book: caught by the serial pre-filter, never hydrated.
	grouped := "grouped-book"
	existingVG := "vg-existing"
	store.books = append(store.books, database.BookCore{ID: grouped, Title: grouped, FilePath: libRoot + "/grouped.m4b", VersionGroupID: &existingVG})
	store.byID[grouped] = &database.Book{ID: grouped, Title: grouped, FilePath: libRoot + "/grouped.m4b", VersionGroupID: &existingVG}

	// Outside the library root entirely.
	outside := "outside-book"
	store.books = append(store.books, database.BookCore{ID: outside, Title: outside, FilePath: "/other/outside.m4b"})
	store.byID[outside] = &database.Book{ID: outside, Title: outside, FilePath: "/other/outside.m4b"}

	config.AppConfig.RootDir = libRoot

	res, err := AssignOrphanVGs(store, libRoot)
	if err != nil {
		t.Fatalf("AssignOrphanVGs: %v", err)
	}

	wantTotal := n + 2
	if res.TotalChecked != wantTotal {
		t.Errorf("TotalChecked = %d, want %d", res.TotalChecked, wantTotal)
	}
	if res.Assigned != n {
		t.Errorf("Assigned = %d, want %d", res.Assigned, n)
	}
	if res.AlreadyHasVG != 1 {
		t.Errorf("AlreadyHasVG = %d, want 1", res.AlreadyHasVG)
	}
	if res.NotInLibrary != 1 {
		t.Errorf("NotInLibrary = %d, want 1", res.NotInLibrary)
	}
	if res.Errors != 0 {
		t.Errorf("Errors = %d, want 0", res.Errors)
	}
	if res.SkippedConcurrentAssignment != 0 {
		t.Errorf("SkippedConcurrentAssignment = %d, want 0", res.SkippedConcurrentAssignment)
	}

	// Every orphan must have been written exactly once with a unique VG.
	seenVG := make(map[string]bool, n)
	if len(store.updated) != n {
		t.Fatalf("wrote %d books, want %d", len(store.updated), n)
	}
	for _, id := range orphanIDs {
		b, ok := store.updated[id]
		if !ok {
			t.Errorf("orphan %s was not written", id)
			continue
		}
		if b.VersionGroupID == nil || *b.VersionGroupID == "" {
			t.Errorf("orphan %s VersionGroupID not set", id)
			continue
		}
		if seenVG[*b.VersionGroupID] {
			t.Errorf("orphan %s VersionGroupID %s collides with another book", id, *b.VersionGroupID)
		}
		seenVG[*b.VersionGroupID] = true
		if b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
			t.Errorf("orphan %s IsPrimaryVersion not set true", id)
		}
		if b.LibraryState == nil || *b.LibraryState != "organized" {
			t.Errorf("orphan %s LibraryState = %v, want organized", id, b.LibraryState)
		}
	}

	// The already-grouped and outside-library books must never be written.
	if _, ok := store.updated[grouped]; ok {
		t.Errorf("already-grouped book %s was written, want untouched", grouped)
	}
	if _, ok := store.updated[outside]; ok {
		t.Errorf("outside-library book %s was written, want untouched", outside)
	}
}

// TestAssignOrphanVGs_ConcurrentAssignmentSkip proves the clobber guard: a
// book that looked like an orphan at the initial Core scan (GetAllBooksCore)
// but has since had a VersionGroupID assigned by someone else (a sibling
// worker, a concurrent regroup apply, or a merge) — visible only once this
// worker re-fetches the full record via GetBookByID — must be skipped, not
// overwritten. Before this fix AssignOrphanVGs used the Core snapshot's
// VersionGroupID check as the only guard and would clobber this race.
func TestAssignOrphanVGs_ConcurrentAssignmentSkip(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	defer func() { config.AppConfig.RootDir = prevRoot }()

	const libRoot = "/lib"
	config.AppConfig.RootDir = libRoot

	store := newFakeStore()

	// The Core snapshot (what the initial scan sees) has no VG — it looks
	// like a legitimate orphan and passes the pre-filter.
	raced := "raced-book"
	path := libRoot + "/raced.m4b"
	store.books = append(store.books, database.BookCore{ID: raced, Title: raced, FilePath: path})

	// But the full record (what GetBookByID returns, simulating a concurrent
	// writer that landed between the scan and this hydrate) already has one.
	concurrentVG := "vg-assigned-by-someone-else"
	concurrentIsPrimary := true
	store.byID[raced] = &database.Book{
		ID: raced, Title: raced, FilePath: path,
		VersionGroupID:   &concurrentVG,
		IsPrimaryVersion: &concurrentIsPrimary,
	}

	// A genuine orphan alongside it so the run does real work too.
	clean := "clean-book"
	cleanPath := libRoot + "/clean.m4b"
	store.books = append(store.books, database.BookCore{ID: clean, Title: clean, FilePath: cleanPath})
	store.byID[clean] = &database.Book{ID: clean, Title: clean, FilePath: cleanPath}

	res, err := AssignOrphanVGs(store, libRoot)
	if err != nil {
		t.Fatalf("AssignOrphanVGs: %v", err)
	}

	if res.SkippedConcurrentAssignment != 1 {
		t.Errorf("SkippedConcurrentAssignment = %d, want 1", res.SkippedConcurrentAssignment)
	}
	if res.Assigned != 1 {
		t.Errorf("Assigned = %d, want 1", res.Assigned)
	}
	if res.Errors != 0 {
		t.Errorf("Errors = %d, want 0", res.Errors)
	}

	// The raced book must never have been written, and its VG must be
	// unchanged from the concurrent assignment.
	if _, ok := store.updated[raced]; ok {
		t.Errorf("raced book %s was written, want left untouched by the clobber guard", raced)
	}
	if got := store.byID[raced].VersionGroupID; got == nil || *got != concurrentVG {
		t.Errorf("raced book VersionGroupID = %v, want unchanged %q", got, concurrentVG)
	}

	// The clean book must have been assigned normally.
	b, ok := store.updated[clean]
	if !ok {
		t.Fatalf("clean book %s was not written", clean)
	}
	if b.VersionGroupID == nil || *b.VersionGroupID == "" {
		t.Errorf("clean book VersionGroupID not set")
	}
}

// TestAssignOrphanVGs_RealStoreConcurrent drives AssignOrphanVGs against a
// real PebbleStore (not the mutex-guarded fake) so `go test -race` exercises
// concurrent GetBookByID / UpdateBook — including the memdb write-through —
// across the worker pool, and confirms the writes persisted.
func TestAssignOrphanVGs_RealStoreConcurrent(t *testing.T) {
	ps, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("new pebble store: %v", err)
	}

	const libRoot = "/lib"
	const n = 40

	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("%s/book-%02d.m4b", libRoot, i)
		book, err := ps.CreateBook(&database.Book{Title: fmt.Sprintf("book %d", i), FilePath: path})
		if err != nil {
			t.Fatalf("create book: %v", err)
		}
		ids = append(ids, book.ID)
	}

	res, err := AssignOrphanVGs(ps, libRoot)
	if err != nil {
		t.Fatalf("AssignOrphanVGs: %v", err)
	}
	if res.Assigned != n {
		t.Errorf("Assigned = %d, want %d", res.Assigned, n)
	}
	if res.Errors != 0 {
		t.Errorf("Errors = %d, want 0", res.Errors)
	}

	seenVG := make(map[string]bool, n)
	for _, id := range ids {
		b, err := ps.GetBookByID(id)
		if err != nil || b == nil {
			t.Fatalf("re-read %s: %v", id, err)
		}
		if b.VersionGroupID == nil || *b.VersionGroupID == "" {
			t.Errorf("book %s VersionGroupID not set", id)
			continue
		}
		if seenVG[*b.VersionGroupID] {
			t.Errorf("book %s VersionGroupID %s collides", id, *b.VersionGroupID)
		}
		seenVG[*b.VersionGroupID] = true
		if b.IsPrimaryVersion == nil || !*b.IsPrimaryVersion {
			t.Errorf("book %s IsPrimaryVersion not set true", id)
		}
	}
}
