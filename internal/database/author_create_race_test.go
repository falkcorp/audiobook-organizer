// file: internal/database/author_create_race_test.go
// version: 1.0.1
// guid: 7a1c0f2e-9b64-4d3a-8c51-2f8e6d4b7a09
// last-edited: 2026-09-02

package database

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestCreateAuthorIsAtomicUnderConcurrency pins the invariant that one author
// name yields exactly one author row.
//
// CreateAuthor was check-then-create: GetAuthorByName, and on a miss, mint a row.
// The two steps were not atomic, and the window is not narrow -- measured before
// the fix, 24 concurrent calls with an identical name produced 24 DISTINCT rows,
// reproducibly. The dedup check essentially never observed a concurrent write.
//
// A SERIAL test cannot observe this at all, which is presumably how it survived:
// call CreateAuthor twice in sequence and the second one correctly returns the
// first one's row. Only concurrency shows it, so the concurrency IS the test.
//
// Why it matters more than "some duplicate rows": the author:name:<normalized>
// index maps one name to exactly ONE id, so every duplicate beyond the indexed
// one is UNREACHABLE by name lookup. Any code that resolves a name to an id is
// then silently operating on the wrong row -- and books hang off the unreachable
// ones. The scanner resolves authors from inside its worker pool, once per book,
// so an import that first meets an author across several books at once mints a
// row per worker.
func TestCreateAuthorIsAtomicUnderConcurrency(t *testing.T) {
	const workers = 24
	const name = "Terry Pratchett"

	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	defer store.Close()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ids     = map[int]int{} // author id -> how many callers got it
		errs    []error
		startCh = make(chan struct{})
	)

	for range workers {
		wg.Go(func() {
			<-startCh // release all workers at once to widen the window
			a, err := store.CreateAuthor(name)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids[a.ID]++
		})
	}
	close(startCh)
	wg.Wait()

	for _, e := range errs {
		t.Errorf("CreateAuthor returned an error: %v", e)
	}
	if len(ids) != 1 {
		t.Fatalf("%d concurrent CreateAuthor(%q) calls produced %d DISTINCT author rows, want exactly 1: %v\n"+
			"the author:name index maps one name to one id, so every row beyond the indexed one is "+
			"unreachable by name lookup and any books attached to it are silently orphaned",
			workers, name, len(ids), ids)
	}

	// And the surviving row must be the one the name index actually resolves to,
	// otherwise "exactly one row" is true while every name lookup still misses it.
	byName, err := store.GetAuthorByName(name)
	if err != nil {
		t.Fatalf("GetAuthorByName: %v", err)
	}
	if byName == nil {
		t.Fatal("the name index resolves to nothing after creating the author")
	}
	for id := range ids {
		if byName.ID != id {
			t.Fatalf("callers were handed author id %d but the name index resolves to %d; "+
				"the row everyone is using is not the row lookups will find", id, byName.ID)
		}
	}
}
