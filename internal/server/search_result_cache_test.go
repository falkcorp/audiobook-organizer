// file: internal/server/search_result_cache_test.go
// version: 1.0.0
// guid: 220a3f36-7c10-426f-a8ee-c3fefa2ee20e
// last-edited: 2026-09-25

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	audiobookspkg "github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/searchcache"
)

type searchCacheFixture struct {
	srv     *Server
	authors []*database.Author
	series  []*database.Series
	bookIDs []string
}

var fixtureWords = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}

// newSearchCacheServer builds a Server over a real Pebble store and Bleve
// index with the index worker running and the result cache enabled, seeded
// with n books over a small vocabulary so queries overlap heavily.
func newSearchCacheServer(t testing.TB, n, ringSize int, cfg searchcache.Config) *searchCacheFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	idx, err := search.Open(filepath.Join(t.TempDir(), "bleve"))
	if err != nil {
		t.Fatalf("bleve: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	if err := idx.MarkRebuilt(); err != nil {
		t.Fatalf("MarkRebuilt: %v", err)
	}

	srv := NewServer(store)
	srv.setSearchIndex(idx)
	srv.audiobookService.SetSearchIndex(idx)
	srv.indexQueue = make(chan indexRequest, 4096)
	done := make(chan struct{})
	go func() { srv.runIndexWorker(); close(done) }()
	t.Cleanup(func() { srv.closeIndexQueue(); <-done })
	if ringSize > 0 {
		srv.searchChanges = searchcache.NewChangeLog(ringSize)
	}
	srv.enableSearchResultCache(cfg)

	fx := &searchCacheFixture{srv: srv}
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 12; i++ {
		a, err := srv.store.CreateAuthor(fmt.Sprintf("%s Writer%d", fixtureWords[i%len(fixtureWords)], i))
		if err != nil {
			t.Fatalf("author: %v", err)
		}
		fx.authors = append(fx.authors, a)
		s, err := srv.store.CreateSeries(fmt.Sprintf("%s Saga%d", fixtureWords[(i+3)%len(fixtureWords)], i), &a.ID)
		if err != nil {
			t.Fatalf("series: %v", err)
		}
		fx.series = append(fx.series, s)
	}
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("%s %s %d", fixtureWords[rng.Intn(len(fixtureWords))], fixtureWords[rng.Intn(len(fixtureWords))], i)
		a := fx.authors[rng.Intn(len(fx.authors))]
		b := &database.Book{
			ID: fmt.Sprintf("book%04d", i), Title: title, FilePath: fmt.Sprintf("/lib/%d.m4b", i), Format: "m4b",
			AuthorID: &a.ID,
		}
		if rng.Intn(3) == 0 {
			s := fx.series[rng.Intn(len(fx.series))]
			b.SeriesID = &s.ID
		}
		if rng.Intn(5) == 0 {
			f := false
			b.IsPrimaryVersion = &f
		}
		if _, err := srv.store.CreateBook(b); err != nil {
			t.Fatalf("book: %v", err)
		}
		fx.bookIDs = append(fx.bookIDs, b.ID)
	}
	drainTB(t, srv)
	return fx
}

func primaryOnly() audiobookspkg.ListFilters {
	t := true
	return audiobookspkg.ListFilters{IsPrimaryVersion: &t}
}

// list runs the list pipeline and returns the JSON of the parts a client
// reads (items and count), plus whether it was stale.
func (fx *searchCacheFixture) list(t testing.TB, q string, limit, offset int, f audiobookspkg.ListFilters) (string, bool) {
	t.Helper()
	resp, err := fx.srv.buildAudiobookListResponse(context.Background(), limit, offset, q, nil, nil, f, false)
	if err != nil {
		t.Fatalf("list %q: %v", q, err)
	}
	b, err := json.Marshal(gin.H{"items": resp["items"], "count": resp["count"]})
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := resp["stale"].(bool)
	return string(b), stale
}

func (fx *searchCacheFixture) ids(t testing.TB, q string, f audiobookspkg.ListFilters) []string {
	t.Helper()
	// Same filters buildAudiobookListResponse applies, so ids and list share
	// one cache entry.
	f.ExcludeQuarantined = true
	books, _, _, err := fx.srv.audiobookService.GetAudiobooksPage(context.Background(), 100000, 0, q, nil, nil, f)
	if err != nil {
		t.Fatalf("ids %q: %v", q, err)
	}
	out := make([]string, len(books))
	for i := range books {
		out[i] = books[i].ID
	}
	return out
}

func containsID(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// TestSearchResultCache_PagesMatchUncached is the property test: over random
// queries, filters, sorts and pages, a page sliced from the cache is
// byte-identical to the same request run without the cache.
func TestSearchResultCache_PagesMatchUncached(t *testing.T) {
	fx := newSearchCacheServer(t, 400, 0, searchcache.Config{})
	cache := fx.srv.searchResults
	rng := rand.New(rand.NewSource(42))
	sorts := []struct{ by, order string }{{"", ""}, {"title", "asc"}, {"title", "desc"}, {"author", "asc"}}
	for i := 0; i < 250; i++ {
		var q string
		switch rng.Intn(5) {
		case 0:
			q = fixtureWords[rng.Intn(len(fixtureWords))]
		case 1:
			q = fixtureWords[rng.Intn(len(fixtureWords))] + " " + fixtureWords[rng.Intn(len(fixtureWords))]
		case 2:
			q = fixtureWords[rng.Intn(len(fixtureWords))][:3] + "*"
		case 3:
			q = "  " + fixtureWords[rng.Intn(len(fixtureWords))] + "   Writer" + fmt.Sprint(rng.Intn(12))
		default:
			q = "Saga" + fmt.Sprint(rng.Intn(12))
		}
		f := audiobookspkg.ListFilters{}
		if rng.Intn(4) != 0 {
			f = primaryOnly()
		}
		f.ExcludeQuarantined = true
		s := sorts[rng.Intn(len(sorts))]
		f.SortBy, f.SortOrder = s.by, s.order
		limit := 1 + rng.Intn(60)
		offset := rng.Intn(120)

		fx.srv.audiobookService.SetSearchResultCache(nil)
		want, _ := fx.list(t, q, limit, offset, f)
		fx.srv.audiobookService.SetSearchResultCache(cache)
		got, stale := fx.list(t, q, limit, offset, f)
		if stale {
			t.Fatalf("query %q served stale with no writes", q)
		}
		if got != want {
			t.Fatalf("query %q limit=%d offset=%d sort=%v: cached page differs\n got: %.400s\nwant: %.400s", q, limit, offset, s, got, want)
		}
	}
	st := cache.Stats()
	if st.Hits == 0 || st.Misses == 0 {
		t.Fatalf("stats %+v: the property test never exercised both hits and misses", st)
	}
}

// TestSearchResultCache_CaseFoldSharesEntry pins that a plain query and its
// upper-case twin share one entry and return the same ranked list.
func TestSearchResultCache_CaseFoldSharesEntry(t *testing.T) {
	fx := newSearchCacheServer(t, 120, 0, searchcache.Config{})
	a := fx.ids(t, "Alpha  Bravo", primaryOnly())
	misses := fx.srv.searchResults.Stats().Misses
	b := fx.ids(t, "alpha bravo", primaryOnly())
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("folded queries differ: %v vs %v", a, b)
	}
	if fx.srv.searchResults.Stats().Misses != misses {
		t.Fatal("folded query did not share the cached entry")
	}
}

// setupRenameTarget gives one primary book a unique title word, author and
// series, so each rename below moves exactly that book between two queries.
func setupRenameTarget(t *testing.T, fx *searchCacheFixture) (bookID string, authorID, seriesID int) {
	t.Helper()
	a, err := fx.srv.store.CreateAuthor("Zuluauthor Person")
	if err != nil {
		t.Fatal(err)
	}
	s, err := fx.srv.store.CreateSeries("Zuluseries Cycle", &a.ID)
	if err != nil {
		t.Fatal(err)
	}
	bookID = "target"
	if _, err := fx.srv.store.CreateBook(&database.Book{
		ID: bookID, Title: "Zulutitle Story", FilePath: "/lib/target.m4b", Format: "m4b",
		AuthorID: &a.ID, SeriesID: &s.ID,
	}); err != nil {
		t.Fatal(err)
	}
	drainQueue(t, fx.srv)
	return bookID, a.ID, s.ID
}

func TestSearchResultCache_RenamesAreReflected(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		for _, kind := range []string{"title", "author", "series"} {
			name := kind + map[bool]string{false: "/incremental", true: "/ring-overflow"}[overflow]
			t.Run(name, func(t *testing.T) {
				ring := 0
				if overflow {
					ring = 4
				}
				fx := newSearchCacheServer(t, 60, ring, searchcache.Config{})
				// newSearchCacheServer records its seeding into the small ring
				// too; the entry built below starts after it.
				id, authorID, seriesID := setupRenameTarget(t, fx)
				before, after := "zulu"+kind, "yankee"+kind
				if !containsID(fx.ids(t, before, primaryOnly()), id) {
					t.Fatalf("%q does not match the target before the rename", before)
				}
				if containsID(fx.ids(t, after, primaryOnly()), id) {
					t.Fatalf("%q matches the target before the rename", after)
				}
				patchesBefore := fx.srv.searchResults.Stats().Patches

				switch kind {
				case "title":
					if _, err := fx.srv.store.ModifyBook(id, func(b *database.Book) error { b.Title = "Yankeetitle Story"; return nil }); err != nil {
						t.Fatal(err)
					}
				case "author":
					if err := fx.srv.store.UpdateAuthorName(authorID, "Yankeeauthor Person"); err != nil {
						t.Fatal(err)
					}
				case "series":
					if err := fx.srv.store.UpdateSeriesName(seriesID, "Yankeeseries Cycle"); err != nil {
						t.Fatal(err)
					}
				}
				if overflow {
					// Unrelated writes push the rename's records out of the ring.
					for i := 0; i < 8; i++ {
						bid := fx.bookIDs[i]
						if _, err := fx.srv.store.ModifyBook(bid, func(b *database.Book) error { b.Title += " x"; return nil }); err != nil {
							t.Fatal(err)
						}
					}
				}
				drainQueue(t, fx.srv)

				if overflow {
					_, stale := fx.list(t, before, 50, 0, primaryOnly())
					if !stale {
						t.Fatalf("ring overflow did not serve the entry stale: gen=%d stats=%+v", fx.srv.searchChanges.Generation(), fx.srv.searchResults.Stats())
					}
					for _, q := range []string{before, after} {
						waitUntil(t, func() bool {
							_, stale := fx.list(t, q, 50, 0, primaryOnly())
							return !stale
						})
					}
				}
				if containsID(fx.ids(t, before, primaryOnly()), id) {
					t.Fatalf("%q still matches the target after the %s rename", before, kind)
				}
				// The "after" entry was built (warm) before the rename, so this
				// is a patched or rebuilt entry, not a fresh miss.
				if !containsID(fx.ids(t, after, primaryOnly()), id) {
					t.Fatalf("%q does not match the target after the %s rename", after, kind)
				}
				if !overflow && fx.srv.searchResults.Stats().Patches <= patchesBefore {
					t.Fatalf("the %s rename was not applied by a patch", kind)
				}
				// Byte-identity after the change: the patched entry agrees with
				// an uncached run.
				cache := fx.srv.searchResults
				got, _ := fx.list(t, "story", 50, 0, primaryOnly())
				fx.srv.audiobookService.SetSearchResultCache(nil)
				want, _ := fx.list(t, "story", 50, 0, primaryOnly())
				fx.srv.audiobookService.SetSearchResultCache(cache)
				if got != want {
					t.Fatalf("after the %s rename the cached page differs from uncached\n got: %.300s\nwant: %.300s", kind, got, want)
				}
			})
		}
	}
}

// TestSearchResultCache_DisconnectStillCaches: a request whose client goes
// away mid-search still leaves the result in the cache, and the retry is a hit
// that runs no second search.
func TestSearchResultCache_DisconnectStillCaches(t *testing.T) {
	fx := newSearchCacheServer(t, 200, 0, searchcache.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := fx.srv.audiobookService.GetAudiobooksPage(ctx, 10, 0, "alpha", nil, nil, primaryOnly())
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	waitUntil(t, func() bool { return fx.srv.searchResults.Stats().Entries == 1 })
	before := fx.srv.searchResults.Stats()
	if _, _, meta, err := fx.srv.audiobookService.GetAudiobooksPage(context.Background(), 10, 0, "alpha", nil, nil, primaryOnly()); err != nil || !meta.Cached {
		t.Fatalf("retry: meta=%+v err=%v", meta, err)
	}
	after := fx.srv.searchResults.Stats()
	if after.Hits != before.Hits+1 || after.Rebuilds != before.Rebuilds {
		t.Fatalf("retry was not a hit: before %+v after %+v", before, after)
	}
}

// TestSearchResultCache_PendingReturns202 drives the list handler with a wait
// too short for any search, and polls the search ID to completion.
func TestSearchResultCache_PendingReturns202(t *testing.T) {
	fx := newSearchCacheServer(t, 200, 0, searchcache.Config{Wait: time.Nanosecond})
	_, _, _, err := fx.srv.audiobookService.GetAudiobooksPage(context.Background(), 10, 0, "bravo", nil, nil, primaryOnly())
	var pe *searchcache.PendingError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *searchcache.PendingError", err)
	}
	gin.SetMode(gin.TestMode)
	poll := func() map[string]any {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Params = gin.Params{{Key: "search_id", Value: pe.SearchID}}
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/search/"+pe.SearchID, nil)
		fx.srv.getSearchJob(c)
		if w.Code != http.StatusOK {
			t.Fatalf("poll status %d: %s", w.Code, w.Body.String())
		}
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if d, ok := body["data"].(map[string]any); ok {
			return d
		}
		return body
	}
	waitUntil(t, func() bool { return poll()["status"] == "done" })
	if _, _, meta, err := fx.srv.audiobookService.GetAudiobooksPage(context.Background(), 10, 0, "bravo", nil, nil, primaryOnly()); err != nil || !meta.Cached {
		t.Fatalf("after done: meta=%+v err=%v", meta, err)
	}
}

// TestSearchResultCache_ConcurrentReadsAndWrites runs searches while books are
// renamed, under -race, then checks every entry converges on the uncached truth.
func TestSearchResultCache_ConcurrentReadsAndWrites(t *testing.T) {
	fx := newSearchCacheServer(t, 150, 0, searchcache.Config{})
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if w%2 == 0 {
					id := fx.bookIDs[(i*5+w)%len(fx.bookIDs)]
					word := fixtureWords[(i+w)%len(fixtureWords)]
					if _, err := fx.srv.store.ModifyBook(id, func(b *database.Book) error { b.Title = word + " renamed"; return nil }); err != nil {
						t.Error(err)
						return
					}
					continue
				}
				q := fixtureWords[(i+w)%len(fixtureWords)]
				if _, _, _, err := fx.srv.audiobookService.GetAudiobooksPage(context.Background(), 20, i%3*20, q, nil, nil, primaryOnly()); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	drainQueue(t, fx.srv)
	cache := fx.srv.searchResults
	for _, q := range fixtureWords {
		waitUntil(t, func() bool {
			got, stale := fx.list(t, q, 100, 0, primaryOnly())
			fx.srv.audiobookService.SetSearchResultCache(nil)
			want, _ := fx.list(t, q, 100, 0, primaryOnly())
			fx.srv.audiobookService.SetSearchResultCache(cache)
			return !stale && got == want
		})
	}
}

// drainTB is drainQueue for benchmarks as well as tests.
func drainTB(tb testing.TB, srv *Server) {
	tb.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&srv.indexWorkerBusy) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	tb.Fatalf("index queue did not drain")
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within 10s")
}

// TestSearchResultCache_Timing measures uncached vs cached pages on a ~100k
// synthetic library. Opt-in (SEARCH_CACHE_BENCH=1): it seeds 100k books.
func TestSearchResultCache_Timing(t *testing.T) {
	if os.Getenv("SEARCH_CACHE_BENCH") == "" {
		t.Skip("set SEARCH_CACHE_BENCH=1 to run the 100k timing")
	}
	start := time.Now()
	fx := newSearchCacheServer(t, 100000, 0, searchcache.Config{})
	t.Logf("seeded 100000 books + index in %v", time.Since(start).Round(time.Millisecond))
	cache := fx.srv.searchResults
	f := primaryOnly()
	f.ExcludeQuarantined = true
	timeIt := func(q string, offset int) time.Duration {
		t0 := time.Now()
		fx.list(t, q, 50, offset, primaryOnly())
		return time.Since(t0)
	}
	for _, q := range []string{"alpha", "alpha bravo", "writer3"} {
		fx.srv.audiobookService.SetSearchResultCache(nil)
		_, n, err := fx.srv.audiobookService.GetAudiobooksWithTotal(context.Background(), 1, 0, q, nil, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		deep := max(0, (n/50-1)*50)
		u1, uN := timeIt(q, 0), timeIt(q, deep)
		fx.srv.audiobookService.SetSearchResultCache(cache)
		c1miss := timeIt(q, 0)
		c1, cN := timeIt(q, 0), timeIt(q, deep)
		t.Logf("q=%-13q matches=%-6d uncached p1=%-10v pN(offset %d)=%-10v | cached first(miss)=%-10v p1 hit=%-10v pN hit=%v",
			q, n, u1.Round(time.Microsecond), deep, uN.Round(time.Microsecond), c1miss.Round(time.Microsecond), c1.Round(time.Microsecond), cN.Round(time.Microsecond))
	}
}

// TestSearchResultCache_IndexLagIsRepatched: a read that lands between a book
// write and its Bleve commit re-evaluates against the old document and is
// stamped current. The commit must record the book again so the next read
// re-patches it. Simulated by writing through the inner store (recorded at
// write time, not re-indexed) and re-indexing afterwards.
func TestSearchResultCache_IndexLagIsRepatched(t *testing.T) {
	fx := newSearchCacheServer(t, 60, 0, searchcache.Config{})
	id, _, _ := setupRenameTarget(t, fx)
	if !containsID(fx.ids(t, "zulutitle", primaryOnly()), id) {
		t.Fatal("target missing before the rename")
	}
	inner := fx.srv.store.(*indexedStore).Store
	if _, err := inner.ModifyBook(id, func(b *database.Book) error { b.Title = "Yankeetitle Story"; return nil }); err != nil {
		t.Fatal(err)
	}
	// Bleve still holds the old title: the patch keeps the target.
	if !containsID(fx.ids(t, "zulutitle", primaryOnly()), id) {
		t.Fatal("index was updated before the simulated commit")
	}
	fx.srv.enqueueIndex(id, false)
	drainQueue(t, fx.srv)
	if containsID(fx.ids(t, "zulutitle", primaryOnly()), id) {
		t.Fatal("the index commit did not re-patch an entry read during the lag")
	}
}
