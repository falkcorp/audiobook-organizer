// file: internal/server/metadata_ops_test.go
// version: 1.1.0
// guid: 9c1e4a77-5b2d-4f83-9a10-2e7c6b8d4f01
// last-edited: 2026-07-11

// Package server tests for TASK-05 (INIT-3-T3): the bulk metadata fetch outer
// loop now runs on a bounded errgroup pool with a per-provider semaphore. These
// tests pin the concurrency contract that is otherwise invisible to CI:
//
//   - the worker pool never exceeds its configured limit (and actually reaches it);
//   - the per-provider semaphore never allows more than perProviderFetchCap
//     in-flight calls to one source (and actually reaches it);
//   - two concurrent calls through a ProtectedSource are race-free;
//   - resume-skip, counter-exactness, and context cancellation still hold when
//     the outer loop is parallel.
//
// Metadata sources are disabled (config.AppConfig.MetadataSources = nil) for the
// end-to-end run so BuildSourceChain() is empty and no book makes a real network
// call — every book deterministically resolves to "not_found", which lets us
// assert exact counter/row behavior under concurrency.
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
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
)

// concurrentFakeSource records the maximum number of overlapping search calls it
// ever saw, so a test can assert a concurrency cap was respected AND reached.
type concurrentFakeSource struct {
	name     string
	delay    time.Duration
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *concurrentFakeSource) Name() string { return f.name }

func (f *concurrentFakeSource) observe() {
	cur := f.inFlight.Add(1)
	for {
		m := f.maxSeen.Load()
		if cur <= m || f.maxSeen.CompareAndSwap(m, cur) {
			break
		}
	}
	time.Sleep(f.delay)
	f.inFlight.Add(-1)
}

func (f *concurrentFakeSource) SearchByTitle(_ context.Context, _ string) ([]metadata.BookMetadata, error) {
	f.observe()
	return nil, nil
}

func (f *concurrentFakeSource) SearchByTitleAndAuthor(_ context.Context, _, _ string) ([]metadata.BookMetadata, error) {
	f.observe()
	return nil, nil
}

// TestProviderSemaphore_CapRespectedAndReached drives many goroutines through the
// real acquire/release around one source and asserts in-flight never exceeds the
// cap but does reach it (so an accidentally-serialized path can't vacuously pass).
func TestProviderSemaphore_CapRespectedAndReached(t *testing.T) {
	src := &concurrentFakeSource{name: "fake", delay: 15 * time.Millisecond}
	chain := []metadata.MetadataSource{src}
	sem := newProviderSemaphore(chain, perProviderFetchCap)

	const goroutines = 24
	var wg sync.WaitGroup
	ctx := context.Background()
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sem.acquire(ctx, src.name); err != nil {
				return
			}
			defer sem.release(src.name)
			_, _ = src.SearchByTitle(ctx, "t")
		}()
	}
	wg.Wait()

	if got := src.maxSeen.Load(); int(got) > perProviderFetchCap {
		t.Fatalf("per-provider cap exceeded: max in-flight %d > cap %d", got, perProviderFetchCap)
	}
	if got := src.maxSeen.Load(); int(got) < perProviderFetchCap {
		t.Fatalf("per-provider cap never reached: max in-flight %d < cap %d (pool may be serialized)", got, perProviderFetchCap)
	}
}

// TestRunBookFetchPool_WorkerCapRespectedAndReached asserts the errgroup pool
// honors its worker limit and actually saturates it.
func TestRunBookFetchPool_WorkerCapRespectedAndReached(t *testing.T) {
	const workers = 4
	var inFlight, maxSeen atomic.Int32
	err := runBookFetchPool(context.Background(), workers, 64, func(_ context.Context, _ int) error {
		cur := inFlight.Add(1)
		for {
			m := maxSeen.Load()
			if cur <= m || maxSeen.CompareAndSwap(m, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := maxSeen.Load(); int(got) > workers {
		t.Fatalf("worker cap exceeded: max in-flight %d > workers %d", got, workers)
	}
	if got := maxSeen.Load(); int(got) < workers {
		t.Fatalf("worker cap never reached: max in-flight %d < workers %d (pool may be serialized)", got, workers)
	}
}

// TestRunBookFetchPool_CtxCancelStopsPromptly asserts a mid-run cancellation
// propagates as ctx.Err() from g.Wait and stops processing further items.
func TestRunBookFetchPool_CtxCancelStopsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var processed atomic.Int32
	const total = 200
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := runBookFetchPool(ctx, 4, total, func(gctx context.Context, _ int) error {
		select {
		case <-gctx.Done():
			return gctx.Err()
		case <-time.After(10 * time.Millisecond):
			processed.Add(1)
			return nil
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := processed.Load(); int(got) >= total {
		t.Fatalf("expected cancellation to stop processing early, but processed all %d items", got)
	}
}

// TestProtectedSource_ConcurrentCallsRaceFree wraps a fake in the real
// ProtectedSource breaker and hits it with cap-many concurrent goroutines. Run
// under -race, this proves the breaker's mutable counters are safe at cap 2.
func TestProtectedSource_ConcurrentCallsRaceFree(t *testing.T) {
	ps := metadata.NewProtectedSource(&concurrentFakeSource{name: "fake", delay: 2 * time.Millisecond}, 5, 30*time.Second)
	var wg sync.WaitGroup
	for i := 0; i < perProviderFetchCap*8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = ps.SearchByTitle(context.Background(), "t")
			_, _ = ps.SearchByTitleAndAuthor(context.Background(), "t", "a")
		}()
	}
	wg.Wait()
}

// recordingStore embeds MockStore and captures CreateOperationResult rows plus a
// seedable GetOperationResults response so resume/counter behavior is observable.
type recordingStore struct {
	*database.MockStore
	mu       sync.Mutex
	created  []*database.OperationResult
	existing []database.OperationResult
}

func (r *recordingStore) CreateOperationResult(res *database.OperationResult) error {
	r.mu.Lock()
	r.created = append(r.created, res)
	r.mu.Unlock()
	return nil
}

func (r *recordingStore) GetOperationResults(_ string) ([]database.OperationResult, error) {
	return r.existing, nil
}

func (r *recordingStore) createdCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.created)
}

func newRecordingStore(books map[string]*database.Book) *recordingStore {
	mock := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := books[id]
			if !ok {
				return nil, nil
			}
			return b, nil
		},
	}
	return &recordingStore{MockStore: mock}
}

func makeBooks(n int) (map[string]*database.Book, []string) {
	books := make(map[string]*database.Book, n)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("book-%03d", i)
		books[id] = &database.Book{ID: id, Title: fmt.Sprintf("Title %03d", i)}
		ids = append(ids, id)
	}
	return books, ids
}

// TestRunBulkMetadataFetchForBookIDs_CountersExactUnderConcurrency runs the real
// parallel op with sources disabled: every book resolves to not_found and writes
// exactly one result row. Asserts the row count is exact (no double/under count).
func TestRunBulkMetadataFetchForBookIDs_CountersExactUnderConcurrency(t *testing.T) {
	disableMetadataSourcesForTest(t)

	const n = 250
	books, ids := makeBooks(n)
	store := newRecordingStore(books)
	srv := &Server{store: store.MockStore, metadataFetchService: metafetch.NewService(store)}

	err := srv.runBulkMetadataFetchForBookIDs(
		context.Background(), "op-counters", ids,
		operations.BulkMetadataFetchParams{}, store, fastpathNoopProgress{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := store.createdCount(); got != n {
		t.Fatalf("expected exactly %d result rows, got %d", n, got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, r := range store.created {
		if r.Status != "not_found" {
			t.Fatalf("expected not_found (sources disabled), got %q for %s", r.Status, r.BookID)
		}
	}
}

// TestRunBulkMetadataFetchForBookIDs_ResumeSkipExact seeds existing result rows
// and asserts those books are skipped (no new row) while the rest are processed.
func TestRunBulkMetadataFetchForBookIDs_ResumeSkipExact(t *testing.T) {
	disableMetadataSourcesForTest(t)

	const n = 120
	books, ids := makeBooks(n)
	store := newRecordingStore(books)
	// Mark the first 30 as already done.
	const alreadyDone = 30
	for i := 0; i < alreadyDone; i++ {
		store.existing = append(store.existing, database.OperationResult{OperationID: "op-resume", BookID: ids[i], Status: "cached"})
	}
	srv := &Server{store: store.MockStore, metadataFetchService: metafetch.NewService(store)}

	err := srv.runBulkMetadataFetchForBookIDs(
		context.Background(), "op-resume", ids,
		operations.BulkMetadataFetchParams{}, store, fastpathNoopProgress{},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got, want := store.createdCount(), n-alreadyDone; got != want {
		t.Fatalf("resume-skip wrong: expected %d new rows, got %d", want, got)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	seen := make(map[string]bool)
	for _, r := range store.created {
		seen[r.BookID] = true
	}
	for i := 0; i < alreadyDone; i++ {
		if seen[ids[i]] {
			t.Fatalf("book %s was already done but got re-processed", ids[i])
		}
	}
}

// TestRunBulkMetadataFetchForBookIDs_CtxCanceledReturnsErr asserts that a
// pre-canceled context makes the op return context.Canceled (exercising the
// post-Wait ctx check) without processing all books.
func TestRunBulkMetadataFetchForBookIDs_CtxCanceledReturnsErr(t *testing.T) {
	disableMetadataSourcesForTest(t)

	const n = 500
	books, ids := makeBooks(n)
	store := newRecordingStore(books)
	srv := &Server{store: store.MockStore, metadataFetchService: metafetch.NewService(store)}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := srv.runBulkMetadataFetchForBookIDs(
		ctx, "op-cancel", ids,
		operations.BulkMetadataFetchParams{}, store, fastpathNoopProgress{},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got := store.createdCount(); got >= n {
		t.Fatalf("expected cancellation to stop processing early, processed %d/%d", got, n)
	}
}

// NOTE: runBulkMetadataFetchAll is intentionally NOT covered by an end-to-end test
// here. Unlike the by-ID variant, when the source chain is empty it falls back to a
// real metadata.NewAudibleClient() (metadata_ops.go ~L239), so disabling sources
// does not make it hermetic — a naive end-to-end test issues live Audible calls
// (slow and flaky). The All rewrite reuses the SAME directly-tested primitives
// (runBookFetchPool, providerSemaphore) and the same processOne shape as the by-ID
// path (covered above); reviewers should line-check the All variant's work-building
// and source-chain wrapping against those tested paths.
