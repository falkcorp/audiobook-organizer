// file: internal/server/search_observer_test.go
// version: 1.0.0
// guid: d7e3b1a4-6f28-4c95-b0a7-8e2f5c1d9a36
// last-edited: 2026-10-13

package server

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// Credit junction rows are not in the search document (BookDocument reads
// AuthorID and Narrator off the book row), so rewriting them must invalidate
// cached results and enqueue NO re-index; a credit edit that also writes the
// book row still enqueues exactly that book, once.
func TestSearchObserver_CreditJunctionWritesRecordButDoNotEnqueue(t *testing.T) {
	srv, store := newObserverServer(t)
	const bookID = "credited-book"
	must(t, createBook(store, bookID, "Credited Title"))
	a, err := store.CreateAuthor("Synthetic Writer")
	must(t, err)
	n, err := store.CreateNarrator("Synthetic Reader")
	must(t, err)
	srv.indexQueue = make(chan indexRequest, 64)
	t.Cleanup(srv.closeIndexQueue)

	gen := srv.searchChanges.Generation()
	must(t, store.SetBookAuthors(bookID, []database.BookAuthor{{BookID: bookID, AuthorID: a.ID, Role: "author", Position: 0}}))
	must(t, store.SetBookNarrators(bookID, []database.BookNarrator{{BookID: bookID, NarratorID: n.ID, Role: "narrator", Position: 0}}))
	if ids := queuedIDs(srv); len(ids) != 0 {
		t.Fatalf("junction-only writes enqueued %v; the document does not read the junction", ids)
	}
	changed, _, ok := srv.searchChanges.ChangedSince(gen, 100)
	if !ok || !slices.Contains(changed, bookID) {
		t.Fatalf("junction writes did not reach the result-cache change log (ok=%v, changed=%v)", ok, changed)
	}

	// ModifyBookCredits writes the row: one request for the book, not four.
	_, _, err = store.ModifyBookCredits(bookID, func(b *database.Book, c *database.BookCreditsEdit) error {
		b.Title = "Credited Again"
		c.Authors = []database.BookAuthor{{AuthorID: a.ID, Role: "author", Position: 0}}
		c.Narrators = []database.BookNarrator{{NarratorID: n.ID, Role: "narrator", Position: 0}}
		return nil
	})
	must(t, err)
	if got := len(srv.indexQueue); got != 1 {
		t.Fatalf("ModifyBookCredits left %d requests on the queue, want exactly 1", got)
	}
	if ids := queuedIDs(srv); len(ids) != 1 || ids[0] != bookID {
		t.Fatalf("ModifyBookCredits enqueued %v, want [%s]", ids, bookID)
	}
}

// RecomputeBookAggregates reports the book once (the ModifyBook inside it), not
// twice, so a bulk backfill does not double its queue load.
func TestSearchObserver_RecomputeAggregatesQueuesTheBookOnce(t *testing.T) {
	srv, store := newObserverServer(t)
	const bookID = "aggregated-book"
	must(t, createBook(store, bookID, "Aggregated Title"))
	srv.indexQueue = make(chan indexRequest, 64)
	t.Cleanup(srv.closeIndexQueue)

	must(t, store.CreateBookFile(&database.BookFile{ID: "agg-1", BookID: bookID, FilePath: "/lib/agg.m4b", Format: "m4b", Duration: 600}))
	if got := len(srv.indexQueue); got != 1 {
		t.Fatalf("a recompute that wrote left %d requests on the queue, want exactly 1", got)
	}
}

// A book already queued and not yet popped is not queued again, however many
// writers hit it at once; once the worker pops it a new write queues afresh.
// Run with -race.
func TestEnqueueIndex_CoalescesPendingBooksUnderConcurrentWrites(t *testing.T) {
	srv := &Server{indexQueue: make(chan indexRequest, 1024)}
	const books, writers, rounds = 20, 8, 50

	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range rounds {
				for i := range books {
					srv.enqueueIndex(fmt.Sprintf("book-%02d", i))
				}
			}
		})
	}
	wg.Wait()
	if got := len(srv.indexQueue); got != books {
		t.Fatalf("queue holds %d requests after %d writes of %d books, want %d (one per book)", got, writers*rounds*books, books, books)
	}
	if got := atomic.LoadInt32(&srv.indexWorkerBusy); got != books {
		t.Fatalf("indexWorkerBusy = %d, want %d: a coalesced write must not count as outstanding work", got, books)
	}

	// The worker pops book-00 (lifting its mark) and later writes queue again.
	first := <-srv.indexQueue
	srv.indexRequestPopped(first.bookID)
	srv.enqueueIndex(first.bookID)
	srv.enqueueIndex(first.bookID)
	if got := len(srv.indexQueue); got != books {
		t.Fatalf("after a pop and a re-write the queue holds %d, want %d (the popped book queued once more)", got, books)
	}
}

// With a live worker, a flood of writes to a few books still ends with the
// index reading each book's LATEST row: coalescing must not swallow the write
// that lands after a pop.
func TestEnqueueIndex_CoalescingKeepsIndexCurrentUnderLiveWorker(t *testing.T) {
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

	const books, writers, rounds = 4, 6, 40
	for i := range books {
		must(t, createBook(store, fmt.Sprintf("flood-%d", i), "Seed"))
	}
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			for r := range rounds {
				for i := range books {
					id := fmt.Sprintf("flood-%d", i)
					title := fmt.Sprintf("Flood Book%d Writer%d Round%d", i, w, r)
					_, _ = store.ModifyBook(id, func(b *database.Book) error { b.Title = title; return nil })
				}
			}
		})
	}
	wg.Wait()
	drainQueue(t, srv)
	for i := range books {
		id := fmt.Sprintf("flood-%d", i)
		final, err := store.GetBookByID(id)
		must(t, err)
		hits, _, err := idx.Search(fmt.Sprintf("title:%q", final.Title), 0, 10)
		must(t, err)
		if len(hits) != 1 || hits[0].BookID != id {
			t.Fatalf("%s: index does not carry the store's final title %q; hits = %v", id, final.Title, hits)
		}
	}
}

func TestDropLogGate_LogsOncePerIntervalAndCountsTheRest(t *testing.T) {
	var g dropLogGate
	t0 := time.Unix(1000, 0)
	if ok, n := g.allow(t0); !ok || n != 0 {
		t.Fatalf("first drop: allow=%v swallowed=%d, want true/0", ok, n)
	}
	for i := 1; i <= 5; i++ {
		if ok, _ := g.allow(t0.Add(time.Duration(i) * time.Second)); ok {
			t.Fatalf("drop %d inside the interval was logged", i)
		}
	}
	if ok, n := g.allow(t0.Add(dropLogInterval)); !ok || n != 5 {
		t.Fatalf("after the interval: allow=%v swallowed=%d, want true/5", ok, n)
	}
}

// A write between NewServer and Start has no queue to land on; it is recorded
// in the durable dirty set so the reconciler Start launches closes the gap.
func TestEnqueueIndex_BeforeStartMarksTheBookDirty(t *testing.T) {
	srv, store := newObserverServer(t)
	if srv.indexQueue != nil {
		t.Fatal("index queue exists before Start")
	}
	must(t, createBook(store, "early-book", "Early Title"))
	ds := database.AsSearchIndexDirtyStore(store)
	if ds == nil {
		t.Fatal("store has no dirty set")
	}
	ids, err := ds.ListSearchIndexDirty(0)
	must(t, err)
	if !slices.Equal(ids, []string{"early-book"}) {
		t.Fatalf("dirty set = %v, want [early-book]", ids)
	}
}

// Shutdown removes this server's observer and authority provider by identity:
// an older server shutting down leaves a newer server's observer on the store.
func TestReleaseSearchAndAuthority_IsByIdentity(t *testing.T) {
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	older := newTestServer(t, store)
	newer := newTestServer(t, store) // installs its observer over older's

	older.indexQueue = make(chan indexRequest, 8)
	newer.indexQueue = make(chan indexRequest, 8)
	t.Cleanup(older.closeIndexQueue)
	t.Cleanup(newer.closeIndexQueue)

	older.releaseSearchAndAuthority()
	must(t, createBook(store, "still-observed", "Title"))
	if got := len(newer.indexQueue); got != 1 {
		t.Fatalf("the newer server's observer was removed by the older server's release (queue %d)", got)
	}
	newer.releaseSearchAndAuthority()
	must(t, createBook(store, "unobserved", "Title"))
	if got := len(newer.indexQueue) + len(older.indexQueue); got != 1 {
		t.Fatalf("a released observer still enqueued (total queued %d)", got)
	}
}

// Shutdown must detach the observer and the authority provider only after every
// writer this server owns has stopped: a still-running op resolves credits and
// writes books until then. Pinned by source order, because Shutdown cannot be
// driven to a half-way state in a unit test.
func TestShutdown_ReleasesObserverAndProviderAfterWritersStop(t *testing.T) {
	raw, err := os.ReadFile("server_lifecycle.go")
	must(t, err)
	src := string(raw)
	release := strings.Index(src, "s.releaseSearchAndAuthority()")
	if release < 0 {
		t.Fatal("Shutdown no longer calls releaseSearchAndAuthority")
	}
	for _, earlier := range []string{"s.opRegistry.Shutdown(", "s.bgCancel()", "s.bgWG.Wait()", "p.Stop()", "s.pluginRegistry.ShutdownAll("} {
		i := strings.Index(src, earlier)
		if i < 0 {
			t.Fatalf("Shutdown no longer contains %q; update this test", earlier)
		}
		if i > release {
			t.Fatalf("releaseSearchAndAuthority runs before %q: ops still running would resolve credits against no authority lists", earlier)
		}
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
