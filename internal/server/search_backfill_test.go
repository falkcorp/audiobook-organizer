// file: internal/server/search_backfill_test.go
// version: 1.0.0
// guid: 867fced7-71af-4a7e-80b6-6c40617df44e
// last-edited: 2026-09-11
//
// Regression tests for the search-index bulk backfill (SQ-01 / TASK-321).
//
// The pre-fix backfill was a sequential `for range books` loop that did
// three point reads per book (author, series, tags) — an N+1 over the
// whole library with no worker pool. These tests pin the two properties
// the fix introduced:
//
//   - store traffic is O(pages + chunks), not O(books), and the per-book
//     read methods are never called at all;
//   - chunks are indexed concurrently — a barrier that needs `workers`
//     simultaneous arrivals is passed, which sequential code cannot do.

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// backfillFakeStore is a database.MockStore wired to serve n books with
// keyset paging and to count every read the backfill makes, split into
// the per-book trio (must be zero) and the batch trio (must be O(chunks)).
type backfillFakeStore struct {
	*database.MockStore
	books []database.Book

	pages, batchAuthors, batchSeries, batchTags atomic.Int64
	perBookAuthor, perBookSeries, perBookTags   atomic.Int64
}

func newBackfillFakeStore(t *testing.T, n int) *backfillFakeStore {
	t.Helper()
	f := &backfillFakeStore{MockStore: &database.MockStore{}}
	authorID, seriesID := 7, 3
	for i := range n {
		desc := fmt.Sprintf("book %d", i)
		f.books = append(f.books, database.Book{
			ID:          fmt.Sprintf("bk-%06d", i),
			Title:       fmt.Sprintf("Backfill Title %d", i),
			FilePath:    fmt.Sprintf("/tmp/bk%06d", i),
			Format:      "m4b",
			AuthorID:    &authorID,
			SeriesID:    &seriesID,
			Description: &desc,
		})
	}
	f.GetAllBooksFullFromFunc = func(afterID string, limit int) ([]database.Book, error) {
		f.pages.Add(1)
		start := 0
		if afterID != "" {
			for i := range f.books {
				if f.books[i].ID == afterID {
					start = i + 1
					break
				}
			}
		}
		end := min(start+limit, len(f.books))
		if start >= end {
			return nil, nil
		}
		return f.books[start:end:end], nil
	}
	f.GetAuthorsByIDsFunc = func(ids []int) (map[int]*database.Author, error) {
		f.batchAuthors.Add(1)
		out := map[int]*database.Author{}
		for _, id := range ids {
			out[id] = &database.Author{ID: id, Name: "Batchauthor Person"}
		}
		return out, nil
	}
	f.GetSeriesByIDsFunc = func(ids []int) (map[int]*database.Series, error) {
		f.batchSeries.Add(1)
		out := map[int]*database.Series{}
		for _, id := range ids {
			out[id] = &database.Series{ID: id, Name: "Batchseries Saga"}
		}
		return out, nil
	}
	f.GetBookTagsByBookIDsFunc = func(bookIDs []string) (map[string][]string, error) {
		f.batchTags.Add(1)
		out := map[string][]string{}
		for _, id := range bookIDs {
			out[id] = []string{"batchtag"}
		}
		return out, nil
	}
	f.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		f.perBookAuthor.Add(1)
		return &database.Author{ID: id, Name: "Point Read"}, nil
	}
	f.GetSeriesByIDFunc = func(id int) (*database.Series, error) {
		f.perBookSeries.Add(1)
		return &database.Series{ID: id, Name: "Point Read"}, nil
	}
	f.GetBookTagsFunc = func(bookID string) ([]string, error) {
		f.perBookTags.Add(1)
		return []string{"pointtag"}, nil
	}
	return f
}

// TestSearchBackfill_BatchReadsNotPerBook proves the N+1 is gone: for
// 1000 books at page 500 / chunk 100 the store sees 3 page reads (two
// full pages plus the empty terminator) and 10 calls of each batch
// method — and zero calls of the per-book methods.
func TestSearchBackfill_BatchReadsNotPerBook(t *testing.T) {
	srv, _, idx := newDropOnlyServer(t)
	const n, pageSize, chunkSize = 1000, 500, 100
	fake := newBackfillFakeStore(t, n)

	indexed, err := srv.runSearchBackfill(context.Background(), fake, 4, pageSize, chunkSize)
	if err != nil {
		t.Fatalf("runSearchBackfill: %v", err)
	}
	if indexed != n {
		t.Fatalf("indexed %d, want %d", indexed, n)
	}
	if d, _ := idx.DocCount(); d != n {
		t.Fatalf("DocCount %d, want %d", d, n)
	}

	const wantChunks = n / chunkSize
	if got := fake.pages.Load(); got != n/pageSize+1 {
		t.Errorf("GetAllBooksFullFrom calls = %d, want %d", got, n/pageSize+1)
	}
	for name, got := range map[string]int64{
		"GetAuthorsByIDs":      fake.batchAuthors.Load(),
		"GetSeriesByIDs":       fake.batchSeries.Load(),
		"GetBookTagsByBookIDs": fake.batchTags.Load(),
	} {
		if got != wantChunks {
			t.Errorf("%s calls = %d, want %d (one per chunk)", name, got, wantChunks)
		}
	}
	for name, got := range map[string]int64{
		"GetAuthorByID": fake.perBookAuthor.Load(),
		"GetSeriesByID": fake.perBookSeries.Load(),
		"GetBookTags":   fake.perBookTags.Load(),
	} {
		if got != 0 {
			t.Errorf("%s called %d times during the backfill; the per-book N+1 is back", name, got)
		}
	}

	// The batch-resolved relations must actually land in the documents:
	// every book carries the batch author, series and tag, none carries
	// the point-read stand-ins.
	for _, probe := range []struct {
		q    string
		want uint64
	}{
		{"author:batchauthor", n},
		{"series:batchseries", n},
		{"tags:batchtag", n},
		{"author:point", 0},
		{"tags:pointtag", 0},
	} {
		_, total, err := idx.Search(probe.q, 0, 1)
		if err != nil {
			t.Fatalf("search %q: %v", probe.q, err)
		}
		if total != probe.want {
			t.Errorf("search %q: %d hits, want %d", probe.q, total, probe.want)
		}
	}
}

// TestSearchBackfill_ChunksRunConcurrently uses a barrier inside the
// batch author read: it needs `workers` chunks to be inside a store call
// at the same moment before it lets any of them through. Sequential code
// enters one chunk, blocks, and the 2s watchdog trips the test.
func TestSearchBackfill_ChunksRunConcurrently(t *testing.T) {
	srv, _, idx := newDropOnlyServer(t)
	const workers, chunkSize = 4, 50
	const n = workers * chunkSize * 2 // 8 chunks, two rounds of 4
	fake := newBackfillFakeStore(t, n)

	var (
		mu        sync.Mutex
		waiting   int
		release   = make(chan struct{})
		released  bool
		barrierOK atomic.Bool
	)
	watchdog := time.AfterFunc(2*time.Second, func() {
		mu.Lock()
		defer mu.Unlock()
		if !released {
			released = true
			close(release) // let the test finish so it can report, not hang
		}
	})
	defer watchdog.Stop()

	inner := fake.GetAuthorsByIDsFunc
	fake.GetAuthorsByIDsFunc = func(ids []int) (map[int]*database.Author, error) {
		mu.Lock()
		if !released {
			waiting++
			if waiting == workers {
				barrierOK.Store(true)
				released = true
				close(release)
			}
		}
		mu.Unlock()
		<-release
		return inner(ids)
	}

	indexed, err := srv.runSearchBackfill(context.Background(), fake, workers, n, chunkSize)
	if err != nil {
		t.Fatalf("runSearchBackfill: %v", err)
	}
	if !barrierOK.Load() {
		t.Fatalf("only %d chunk(s) were inside the store concurrently within 2s; want %d — the backfill is running sequentially", waiting, workers)
	}
	if indexed != n {
		t.Fatalf("indexed %d, want %d", indexed, n)
	}
	if d, _ := idx.DocCount(); d != n {
		t.Fatalf("DocCount %d, want %d", d, n)
	}
}

// TestSearchBackfill_CancelStopsPaging: a canceled context ends the
// backfill without an infinite page loop and reports the cancellation.
func TestSearchBackfill_CancelStopsPaging(t *testing.T) {
	srv, _, _ := newDropOnlyServer(t)
	fake := newBackfillFakeStore(t, 300)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := srv.runSearchBackfill(ctx, fake, 2, 100, 50)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if got := fake.pages.Load(); got > 1 {
		t.Fatalf("kept paging after cancel: %d page reads", got)
	}
}
