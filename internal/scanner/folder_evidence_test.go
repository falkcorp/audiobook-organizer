// file: internal/scanner/folder_evidence_test.go
// version: 1.1.0
// guid: 477ad146-9156-4328-bc92-4bad0d35ece0
// last-edited: 2026-10-06

package scanner

import (
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func resetFolderEvidenceCache(t *testing.T) {
	t.Helper()
	c := &folderEvidenceCache
	c.mu.Lock()
	c.store, c.snap, c.at, c.loading = nil, nil, time.Time{}, nil
	c.mu.Unlock()
}

// Many workers parse folders at once, and the snapshot expires while they
// do: every caller gets an answer, the lists are read with the mutex
// released, and -race sees no unguarded state.
func TestFolderNameEvidence_ConcurrentCallersAndReload(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })
	resetFolderEvidenceCache(t)
	t.Cleanup(func() { resetFolderEvidenceCache(t) })

	sanderson, err := store.CreateAuthor("Brandon Sanderson")
	if err != nil {
		t.Fatal(err)
	}
	junk, err := store.CreateSeries("Brandon Sanderson", &sanderson.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBook(&database.Book{Title: "Elantris", AuthorID: &sanderson.ID, SeriesID: &junk.ID,
		FilePath: "/srv/library/elantris.m4b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBook(&database.Book{Title: "Warbreaker", AuthorID: &sanderson.ID,
		FilePath: "/srv/library/warbreaker.m4b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSeries("Star Wars", nil); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 40; j++ {
				if j%10 == 0 && i%4 == 0 {
					// Expire the snapshot mid-run so callers race a reload.
					c := &folderEvidenceCache
					c.mu.Lock()
					c.at = time.Time{}
					c.mu.Unlock()
				}
				ev := FolderNameEvidence()
				if ev.IsKnownSeries == nil {
					continue // first load still in flight elsewhere and failed: no evidence
				}
				if ev.IsKnownSeries("Brandon Sanderson") {
					t.Error("junk series row read as a series")
					return
				}
				if !ev.IsKnownSeries("Star Wars") {
					t.Error("Star Wars series row not seen")
					return
				}
				_ = ev.IsAuthorRow("Brandon Sanderson")
				_ = ev.IsKnownAuthor("Brandon Sanderson")
			}
		}(i)
	}
	wg.Wait()
}

// The series paths refuse a series named after the book's own author: the
// "Author - Title" split read as "Series - Title".
func TestResolveSeriesID_RefusesTheAuthorsName(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	id, _, err := resolveSeriesID("Brandon Sanderson", "Brandon Sanderson", nil)
	if err != nil || id != nil {
		t.Fatalf("resolveSeriesID(author's own name) = (%v, %v), want no series", id, err)
	}
	id, _, err = resolveSeriesID("Good Omens", "Neil Gaiman, Terry Pratchett", nil)
	if err != nil || id == nil {
		t.Fatalf("resolveSeriesID(real series) = (%v, %v), want a series", id, err)
	}
	id, _, err = resolveSeriesID("Terry Pratchett", "Neil Gaiman, Terry Pratchett", nil)
	if err != nil || id != nil {
		t.Fatalf("resolveSeriesID(one co-author's name) = (%v, %v), want no series", id, err)
	}
	all, err := store.GetAllSeries()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "Good Omens" {
		t.Fatalf("series rows %+v, want only Good Omens", all)
	}
}

// countingSeriesStore counts the series-list reads a snapshot load makes.
type countingSeriesStore struct {
	*database.PebbleStore
	mu    sync.Mutex
	loads int
}

func (s *countingSeriesStore) GetAllSeries() ([]database.Series, error) {
	s.mu.Lock()
	s.loads++
	s.mu.Unlock()
	return s.PebbleStore.GetAllSeries()
}

// The first books of a scan all arrive at once with no snapshot yet: one of
// them reads the lists and the others wait for it.
func TestFolderNameEvidence_FirstLoadIsShared(t *testing.T) {
	pebble, cleanup := setupPebbleStore(t)
	defer cleanup()
	store := &countingSeriesStore{PebbleStore: pebble}
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })
	resetFolderEvidenceCache(t)
	t.Cleanup(func() { resetFolderEvidenceCache(t) })

	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if FolderNameEvidence().IsKnownSeries == nil {
				t.Error("a waiting caller got no snapshot")
			}
		}()
	}
	close(start)
	wg.Wait()
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.loads != 1 {
		t.Fatalf("series list read %d times by 16 concurrent first callers, want 1", store.loads)
	}
}
