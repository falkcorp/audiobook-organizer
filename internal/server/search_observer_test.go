// file: internal/server/search_observer_test.go
// version: 1.0.0
// guid: d7e3b1a4-6f28-4c95-b0a7-8e2f5c1d9a36
// last-edited: 2026-10-13

package server

import (
	"fmt"
	"maps"
	"path/filepath"
	"sort"
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

// newObserverServer builds a Server over a real in-memory Pebble store with the
// server's own change observer installed (NewServer does that) and a RECORDING
// index queue: no worker runs, so the channel holds every enqueue and a test
// reads exactly what the observer asked the index to do.
func newObserverServer(t *testing.T) (*Server, *database.PebbleStore) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := newTestServer(t, store)
	return srv, store
}

// The server hands the registry, the services and the scanner the store it was
// given: no decorator sits between them and the observer's hooks, and a plain
// type assertion on it succeeds.
func TestNewServer_StoreIsTheRawObservedStore(t *testing.T) {
	srv, store := newObserverServer(t)
	if srv.store != database.Store(store) {
		t.Fatalf("server store %T is not the store NewServer was given", srv.store)
	}
	if !isRawStore(srv.store) {
		t.Fatalf("server store %T is a decorator", srv.store)
	}
	if _, ok := srv.store.(*database.PebbleStore); !ok {
		t.Fatal("a bare *PebbleStore assertion on the server store failed")
	}
	if srv.searchChanges == nil {
		t.Fatal("NewServer did not create the search change log")
	}
}

// One test per write path the indexedStore decorator used to intercept, plus
// the paths it never saw. Each runs its write against a real store with the
// server's observer installed and a recording queue, and asserts the write
// asked for a re-index of exactly the book it touched: at least one request
// (the worker de-duplicates a batch by book ID, so several for one book are
// fine), and none for the untouched control book.
func TestSearchObserver_EveryWritePathReachesTheIndexQueue(t *testing.T) {
	const target, control = "target-book", "control-book"
	paths := []struct {
		name string
		do   func(t *testing.T, s *database.PebbleStore)
	}{
		{"CreateBook", func(t *testing.T, s *database.PebbleStore) {
			must(t, createBook(s, "created-book", "Fresh Title"))
		}},
		{"UpdateBook", func(t *testing.T, s *database.PebbleStore) {
			b, err := s.GetBookByID(target)
			must(t, err)
			b.Title = "Updated Title"
			_, err = s.UpdateBook(target, b)
			must(t, err)
		}},
		{"ModifyBook", func(t *testing.T, s *database.PebbleStore) {
			_, err := s.ModifyBook(target, func(b *database.Book) error { b.Title = "Modified Title"; return nil })
			must(t, err)
		}},
		{"FillBookMediaInfo", func(t *testing.T, s *database.PebbleStore) {
			secs := 3600
			updated, err := s.FillBookMediaInfo(target, database.BookMediaInfoPatch{Duration: &secs})
			must(t, err)
			if updated == nil || updated.Duration == nil || *updated.Duration != secs {
				t.Fatalf("the patch did not apply (the test would prove nothing): %+v", updated)
			}
		}},
		{"DeleteBook", func(t *testing.T, s *database.PebbleStore) {
			must(t, s.DeleteBook(target))
		}},
		{"AddBookTag (tag write)", func(t *testing.T, s *database.PebbleStore) {
			must(t, s.AddBookTag(target, "synthetic-tag"))
		}},
		{"SetBookAuthors (credit junction)", func(t *testing.T, s *database.PebbleStore) {
			a, err := s.CreateAuthor("Synthetic Writer")
			must(t, err)
			must(t, s.SetBookAuthors(target, []database.BookAuthor{{BookID: target, AuthorID: a.ID, Role: "author", Position: 0}}))
		}},
		{"SetBookNarrators (credit junction)", func(t *testing.T, s *database.PebbleStore) {
			n, err := s.CreateNarrator("Synthetic Reader")
			must(t, err)
			must(t, s.SetBookNarrators(target, []database.BookNarrator{{BookID: target, NarratorID: n.ID, Role: "narrator", Position: 0}}))
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			srv, store := newObserverServer(t)
			must(t, createBook(store, target, "Original Title"))
			must(t, createBook(store, control, "Control Title"))

			// Writes made before the queue exists are not indexed: Start has
			// not opened the index yet (see installSearchObserver).
			if srv.indexQueue != nil {
				t.Fatal("index queue exists before Start")
			}
			srv.indexQueue = make(chan indexRequest, 256)
			t.Cleanup(srv.closeIndexQueue)

			p.do(t, store)

			ids := queuedIDs(srv)
			if len(ids) == 0 {
				t.Fatalf("%s: no index request was enqueued", p.name)
			}
			want := target
			if p.name == "CreateBook" {
				want = "created-book"
			}
			for _, id := range ids {
				if id != want {
					t.Fatalf("%s: enqueued %q, want only %q (all: %v)", p.name, id, want, ids)
				}
			}
		})
	}
}

// A book_file edit reaches the index through the aggregate recompute the store
// runs after it (duration and size are in the search document). The admin debug
// API used to nudge the decorator with a skipped ModifyBook after such an edit;
// with no decorator that nudge is gone, and this is the proof it was not needed.
func TestSearchObserver_BookFileEditReachesTheIndexQueue(t *testing.T) {
	srv, store := newObserverServer(t)
	const bookID = "filed-book"
	must(t, createBook(store, bookID, "Filed Title"))
	must(t, store.CreateBookFile(&database.BookFile{ID: "file-1", BookID: bookID, FilePath: "/lib/filed-book.m4b", Format: "m4b", Duration: 100}))
	before, err := store.GetBookByID(bookID)
	must(t, err)

	srv.indexQueue = make(chan indexRequest, 64)
	t.Cleanup(srv.closeIndexQueue)

	_, err = store.ModifyBookFile(bookID, "file-1", func(f *database.BookFile) error { f.Duration = 7200; return nil })
	must(t, err)
	after, err := store.GetBookByID(bookID)
	must(t, err)
	if before.Duration != nil && after.Duration != nil && *before.Duration == *after.Duration {
		t.Fatalf("the edit did not change the book aggregate (duration %d); the test proves nothing", *after.Duration)
	}
	ids := queuedIDs(srv)
	if len(ids) == 0 {
		t.Fatal("a book_file edit that changed the book's aggregates enqueued no index request")
	}
	for _, id := range ids {
		if id != bookID {
			t.Fatalf("enqueued %q, want only %q (all: %v)", id, bookID, ids)
		}
	}
}

// A failed write reports nothing: the observer fires only for rows the store
// committed.
func TestSearchObserver_RejectedWriteEnqueuesNothing(t *testing.T) {
	srv, store := newObserverServer(t)
	srv.indexQueue = make(chan indexRequest, 8)
	t.Cleanup(srv.closeIndexQueue)

	if err := createBook(store, "bad:id", "Colon"); err == nil {
		t.Fatal("a colon in the book id was accepted; the test proves nothing")
	}
	if _, err := store.ModifyBook("missing-book", func(*database.Book) error { return nil }); err != nil {
		t.Fatalf("ModifyBook of a missing book: %v", err)
	}
	if ids := queuedIDs(srv); len(ids) != 0 {
		t.Fatalf("writes that committed nothing enqueued %v", ids)
	}
}

// Concurrent writers over a live index worker: the index must end up agreeing
// with the store, book for book. Each owned book is written by one goroutine
// (so its final title is known) and one hot book is rewritten by every
// goroutine through ModifyBook (so the worker must read the LATEST row once the
// last notification lands). Deleted books must be gone from the index. Runs the
// writers on a fixed pool of 8, and is meant for -race.
func TestSearchObserver_ConcurrentWritesLeaveIndexMatchingStore(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })

	srv := newTestServer(t, store)
	srv.setSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 1024)
	done := make(chan struct{})
	go func() { srv.runIndexWorker(); close(done) }()
	t.Cleanup(func() { srv.closeIndexQueue(); <-done })

	const workers, perWorker, rounds = 8, 6, 5
	must(t, createBook(store, "hot-book", "Hot Start"))

	var g errgroup.Group
	g.SetLimit(workers)
	for w := range workers {
		g.Go(func() error {
			for i := range perWorker {
				id := fmt.Sprintf("w%d-b%d", w, i)
				if err := createBook(store, id, "Seed "+id); err != nil {
					return err
				}
				for r := range rounds {
					title := fmt.Sprintf("Round%d Of %s", r, id)
					if _, err := store.ModifyBook(id, func(b *database.Book) error { b.Title = title; return nil }); err != nil {
						return err
					}
					if _, err := store.ModifyBook("hot-book", func(b *database.Book) error {
						b.Title = fmt.Sprintf("Hot Writer%d Round%d", w, r)
						return nil
					}); err != nil {
						return err
					}
				}
				// Every third owned book is deleted after its last rewrite.
				if i%3 == 0 {
					if err := store.DeleteBook(id); err != nil {
						return err
					}
				}
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("writer: %v", err)
	}
	drainQueue(t, srv)

	// The index and the store agree, book for book: every live owned book is
	// indexed under its final title, and every deleted one is gone.
	live, err := store.ListBookIDs()
	must(t, err)
	wantIndexed := map[string]bool{}
	for _, id := range live {
		if id != "hot-book" {
			wantIndexed[id] = true
		}
	}
	hits, _, err := idx.Search(fmt.Sprintf("title:round%d", rounds-1), 0, 1000)
	must(t, err)
	gotIndexed := map[string]bool{}
	for _, h := range hits {
		if h.BookID != "hot-book" {
			gotIndexed[h.BookID] = true
		}
	}
	if len(wantIndexed) != workers*perWorker-workers*(perWorker/3) {
		t.Fatalf("store holds %d owned books; the delete schedule changed under the test", len(wantIndexed))
	}
	if !maps.Equal(gotIndexed, wantIndexed) {
		t.Fatalf("index and store disagree\nindex: %v\nstore: %v", sortedKeys(gotIndexed), sortedKeys(wantIndexed))
	}

	// The hot book's document carries the title the store ended with.
	final, err := store.GetBookByID("hot-book")
	must(t, err)
	hot, _, err := idx.Search(fmt.Sprintf("title:%q", final.Title), 0, 10)
	must(t, err)
	if len(hot) != 1 || hot[0].BookID != "hot-book" {
		t.Fatalf("hot-book's index document does not carry the store's final title %q; hits = %v", final.Title, hot)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func createBook(s *database.PebbleStore, id, title string) error {
	_, err := s.CreateBook(&database.Book{ID: id, Title: title, FilePath: "/lib/" + id + ".m4b", Format: "m4b"})
	return err
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
