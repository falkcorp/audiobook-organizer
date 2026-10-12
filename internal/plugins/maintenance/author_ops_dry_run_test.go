// file: internal/plugins/maintenance/author_ops_dry_run_test.go
// version: 1.2.0
// guid: 5c1e4571-8bcf-4190-ac2d-ae141026d139
// last-edited: 2026-10-11

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// author-split-scan and resolve-production-authors ignored their params and
// wrote on every run until 2026-09-25. Both now default to a dry run.

func newDryRunSplitStore(t *testing.T) (*database.PebbleStore, int, string) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	st.WaitForWarmup()
	t.Cleanup(func() { _ = st.Close() })
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	b, err := st.CreateBook(&database.Book{
		Title:            "dry-run-split",
		FilePath:         "/dry-run-split/book.m4b",
		AuthorID:         new(composite.ID),
		IsPrimaryVersion: new(true),
	})
	require.NoError(t, err)
	require.NoError(t, st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: composite.ID, Role: "author"}}))
	return st, composite.ID, b.ID
}

func TestAuthorSplitScan_DefaultIsDryRun(t *testing.T) {
	for _, params := range []string{"", `{}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("params="+params, func(t *testing.T) {
			st, compositeID, bookID := newDryRunSplitStore(t)
			p := &Plugin{deps: &fakeDeps{store: st}}
			rep := &fakeReporter{}
			var raw json.RawMessage
			if params != "" {
				raw = json.RawMessage(params)
			}
			require.NoError(t, p.runAuthorSplitScan(context.Background(), raw, rep))

			still, err := st.GetAuthorByID(compositeID)
			require.NoError(t, err)
			require.NotNil(t, still, "dry run deleted the composite author")
			alice, err := st.GetAuthorByName("Alice Smith")
			require.NoError(t, err)
			require.Nil(t, alice, "dry run created a split author")
			credits, err := st.GetBookAuthors(bookID)
			require.NoError(t, err)
			require.Len(t, credits, 1)
			require.Equal(t, compositeID, credits[0].AuthorID, "dry run relinked the book")
			require.Contains(t, strings.Join(rep.logs, "\n"), "DRY RUN: would split 1 composite authors, create 2 authors, relink 1 books")
		})
	}
}

// The camelCase alias applies too, so an operator using either spelling gets
// the write they asked for.
func TestAuthorSplitScan_ExplicitApplyWrites(t *testing.T) {
	st, compositeID, _ := newDryRunSplitStore(t)
	p := &Plugin{deps: &fakeDeps{store: st}}
	require.NoError(t, p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dryRun":false}`), &fakeReporter{}))
	gone, err := st.GetAuthorByID(compositeID)
	require.NoError(t, err)
	require.Nil(t, gone, "apply should delete the composite")
}

func TestAuthorOpDryRun_DisagreeingKeysAreAnError(t *testing.T) {
	st, compositeID, _ := newDryRunSplitStore(t)
	p := &Plugin{deps: &fakeDeps{store: st}}
	err := p.runAuthorSplitScan(context.Background(), json.RawMessage(`{"dry_run":false,"dryRun":true}`), &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
	still, gerr := st.GetAuthorByID(compositeID)
	require.NoError(t, gerr)
	require.NotNil(t, still)
	err = p.runResolveProductionAuthors(context.Background(), json.RawMessage(`{"dry_run":true,"dryRun":false}`), &fakeReporter{})
	require.ErrorContains(t, err, "disagree")
}

// guardOpsStore stands in for OpsStore in the resolve-production-authors
// tests. It answers the three reads the op makes and nothing else: the embedded
// OpsStore is nil, so any other call -- every write -- panics, and the tests
// below recover that into a failure that names the offending call. Its maps are
// only read after construction, so the apply's workers may share it.
type guardOpsStore struct {
	OpsStore
	authors []database.Author
	books   map[int][]database.BookCore
	byID    map[int]*database.Author
}

func (s guardOpsStore) GetAllAuthors() ([]database.Author, error) { return s.authors, nil }
func (s guardOpsStore) GetBooksByAuthorIDWithRoleCore(id int) ([]database.BookCore, error) {
	return s.books[id], nil
}
func (s guardOpsStore) GetAuthorByID(id int) (*database.Author, error) { return s.byID[id], nil }

// resolveDeps drives runResolveProductionAuthors: a guard store, a scripted
// metadata fetch, a count of the dedup cache invalidations (the op's one
// non-store write) and a steerable scan stand-down gate. The apply calls it
// from a worker pool, so every recorded field is behind mu.
type resolveDeps struct {
	fakeDeps
	guard guardOpsStore
	wired bool
	fetch func(ctx context.Context, bookID string) (*int, error)

	// renewOK answers RenewScanStandDown; nil means always true.
	renewOK    func(call int) bool
	acquireErr error

	mu            sync.Mutex
	fetched       []string
	invalidations int
	acquires      []string // holder op ids
	releases      int
	renews        int
}

func (d *resolveDeps) OpsStore() OpsStore            { return d.guard }
func (d *resolveDeps) HasMetadataFetchService() bool { return d.wired }
func (d *resolveDeps) InvalidateDedupCache() {
	d.mu.Lock()
	d.invalidations++
	d.mu.Unlock()
}
func (d *resolveDeps) FetchMetadataByTitle(ctx context.Context, id string) (*int, error) {
	d.mu.Lock()
	d.fetched = append(d.fetched, id)
	d.mu.Unlock()
	return d.fetch(ctx, id)
}
func (d *resolveDeps) AcquireScanStandDown(_ context.Context, holder, _ string) (func(), error) {
	if d.acquireErr != nil {
		return nil, d.acquireErr
	}
	d.mu.Lock()
	d.acquires = append(d.acquires, holder)
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		d.releases++
		d.mu.Unlock()
	}, nil
}
func (d *resolveDeps) RenewScanStandDown(string) bool {
	d.mu.Lock()
	d.renews++
	n := d.renews
	d.mu.Unlock()
	return d.renewOK == nil || d.renewOK(n)
}

func (d *resolveDeps) snapshot() (fetched []string, invalidations int, acquires []string, releases int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.fetched...), d.invalidations, append([]string(nil), d.acquires...), d.releases
}

// newResolveFixture: two production companies credited with b1+b2 and b2+b3
// (b2 is co-credited to both, so it must be fetched once), one real author the
// fetch can resolve to, and one non-production author who is not touched.
// Synthetic names throughout.
func newResolveFixture() *resolveDeps {
	realID := 30
	return &resolveDeps{
		guard: guardOpsStore{
			authors: []database.Author{
				{ID: 1, Name: "Tantor Media"}, {ID: 2, Name: "Audible Studios"}, {ID: 3, Name: "Jane Roe"},
			},
			books: map[int][]database.BookCore{
				1: {{ID: "b1"}, {ID: "b2"}},
				2: {{ID: "b2"}, {ID: "b3"}},
			},
			byID: map[int]*database.Author{
				realID: {ID: realID, Name: "John Roe"},
				1:      {ID: 1, Name: "Tantor Media"},
			},
		},
		wired: true,
		fetch: func(context.Context, string) (*int, error) { return &realID, nil },
	}
}

// withBooks replaces the fixture's credits with n books under company 1.
func (d *resolveDeps) withBooks(n int) *resolveDeps {
	books := make([]database.BookCore, n)
	for i := range books {
		books[i] = database.BookCore{ID: fmt.Sprintf("bk%02d", i)}
	}
	d.guard.books = map[int][]database.BookCore{1: books}
	return d
}

// runGuarded runs the op and turns a panic from the guard store (a write, or any
// read the op is not meant to make) into a test failure.
func runGuarded(t *testing.T, p *Plugin, ctx context.Context, raw json.RawMessage, rep sdk.Reporter) error {
	t.Helper()
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("resolve-production-authors touched the store beyond its three reads: %v", r)
			}
		}()
		err = p.runResolveProductionAuthors(ctx, raw, rep)
	}()
	return err
}

// An empty or mode-less param body is a preview: zero fetches (a fetch applies
// metadata), zero store writes, no dedup cache invalidation, and no scan
// stand-down (a daily scheduled preview must not park the scan). Bodies: what
// the scheduled resolve_production_authors task sends now ({}), what a row
// queued under scheduler.resolve-production-authors before 04-P4d carries
// ({"legacy_op_id":""}, schedulerExtraOpParams{}) and reaches this def through
// the alias, and the explicit spellings.
func TestResolveProductionAuthors_DefaultsToPreview(t *testing.T) {
	for _, body := range []string{"", `{}`, `null`, `{"legacy_op_id":""}`, `{"dry_run":true}`, `{"dryRun":true}`} {
		t.Run("body="+body, func(t *testing.T) {
			d := newResolveFixture()
			p := &Plugin{deps: d}
			var raw json.RawMessage
			if body != "" {
				raw = json.RawMessage(body)
			}
			rep := &opIDReporter{id: "op-preview"}
			require.NoError(t, runGuarded(t, p, context.Background(), raw, rep))
			fetched, inv, acquires, _ := d.snapshot()
			require.Empty(t, fetched, "preview fetched metadata, which applies it")
			require.Zero(t, inv, "preview invalidated the dedup cache")
			require.Empty(t, acquires, "preview took the scan stand-down")
			require.Contains(t, strings.Join(rep.logs, "\n"),
				"DRY RUN: would re-fetch metadata for 3 books across 2 production companies")
		})
	}
}

// The apply is reachable only by an explicit dry_run=false (either spelling),
// fetches every book credited to a production company exactly once (b2 is
// credited to both), counts only the books that landed on a non-production
// author, holds the scan stand-down under the op's own id for the whole pass
// and invalidates the dedup cache once.
func TestResolveProductionAuthors_ApplyFetchesAndCounts(t *testing.T) {
	for _, body := range []string{`{"dry_run":false}`, `{"dryRun":false}`} {
		t.Run(body, func(t *testing.T) {
			d := newResolveFixture()
			stillProd := 1
			d.fetch = func(_ context.Context, id string) (*int, error) {
				switch id {
				case "b1":
					realID := 30
					return &realID, nil // resolved
				case "b2":
					return &stillProd, nil // still the production company
				default:
					return nil, errors.New("no match") // b3: fetch failed
				}
			}
			p := &Plugin{deps: d}
			rep := &opIDReporter{id: "op-apply"}
			require.NoError(t, runGuarded(t, p, context.Background(), json.RawMessage(body), rep))
			fetched, inv, acquires, releases := d.snapshot()
			require.ElementsMatch(t, []string{"b1", "b2", "b3"}, fetched)
			require.Equal(t, 1, inv)
			require.Equal(t, []string{"op-apply"}, acquires, "the apply must hold the scan stand-down under its op id")
			require.Equal(t, 1, releases, "the stand-down must be released when the apply ends")
			require.Contains(t, strings.Join(rep.logs, "\n"),
				"Resolved 1 books across 2 production companies (1 fetches failed")
		})
	}
}

// Without the fetch service the apply would resolve nothing and still report
// success; it refuses up front instead, before taking the stand-down. The
// preview does not need the service.
func TestResolveProductionAuthors_ApplyRefusesWithoutFetchService(t *testing.T) {
	d := newResolveFixture()
	d.wired = false
	p := &Plugin{deps: d}
	err := runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":false}`), &opIDReporter{id: "op-x"})
	require.ErrorContains(t, err, "metadata fetch service not initialized")
	fetched, inv, acquires, _ := d.snapshot()
	require.Empty(t, fetched)
	require.Zero(t, inv)
	require.Empty(t, acquires)

	require.NoError(t, runGuarded(t, p, context.Background(), nil, &fakeReporter{}), "preview must not need the fetch service")
}

// Metadata is never applied during a library scan: when the scan does not park,
// the apply fails before its first fetch.
func TestResolveProductionAuthors_ApplyRefusesWhenScanDoesNotStandDown(t *testing.T) {
	d := newResolveFixture()
	d.acquireErr = errors.New("scan did not park within 5m0s")
	p := &Plugin{deps: d}
	err := runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":false}`), &opIDReporter{id: "op-x"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "a library scan is running")
	require.Contains(t, err.Error(), "maintenance.resolve-production-authors apply")
	fetched, inv, _, _ := d.snapshot()
	require.Empty(t, fetched, "no fetch may run when the scan did not stand down")
	require.Zero(t, inv)
}

// A lapsed lease means the scanner may be running again: the pool stops before
// the next fetch. Only the first renewal succeeds, so exactly one book is
// fetched; the dedup cache is still invalidated for that one.
func TestResolveProductionAuthors_ApplyStopsWhenLeaseIsLost(t *testing.T) {
	d := newResolveFixture().withBooks(12)
	d.renewOK = func(call int) bool { return call == 1 }
	p := &Plugin{deps: d}
	err := runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":false}`), &opIDReporter{id: "op-x"})
	require.ErrorIs(t, err, registry.ErrScanStandDownLost)
	fetched, inv, _, releases := d.snapshot()
	require.Len(t, fetched, 1, "no fetch may start after the lease is lost")
	require.Equal(t, 1, inv, "the book fetched before the loss changed credits; the cache must be dropped")
	require.Equal(t, 1, releases)
}

// A cancelled op stops the pool, and the fetches already in flight receive the
// cancelled ctx. The books fetched before the stop may have new credits, so the
// dedup cache is invalidated anyway.
func TestResolveProductionAuthors_ApplyStopsOnCancel(t *testing.T) {
	d := newResolveFixture().withBooks(40)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var ctxNeverCancelled atomic.Bool
	d.fetch = func(fctx context.Context, _ string) (*int, error) {
		cancel()
		select {
		case <-fctx.Done(): // the fetch must run under the op's ctx
			return nil, fctx.Err()
		case <-time.After(2 * time.Second):
			ctxNeverCancelled.Store(true)
			return nil, errors.New("fetch ctx was not the op's ctx")
		}
	}
	p := &Plugin{deps: d}
	err := runGuarded(t, p, ctx, json.RawMessage(`{"dry_run":false}`), &opIDReporter{id: "op-x"})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, ctxNeverCancelled.Load(), "the in-flight fetch did not see the cancel: it is not running under the op's ctx")
	fetched, inv, _, _ := d.snapshot()
	require.NotEmpty(t, fetched)
	require.Less(t, len(fetched), 40, "the pool must stop dispatching after the cancel")
	require.Equal(t, 1, inv)
}

// CLAUDE.md concurrency rule: the apply is one provider round-trip per book over
// a set that can be library-scale, so it runs on a bounded pool. Each fetch
// blocks until a second one is in flight; with a sequential loop the second
// never starts and the wait times out. Run under -race: the counters, the Label
// closure and the fakes are all shared across workers.
func TestResolveProductionAuthors_ApplyFetchesConcurrently(t *testing.T) {
	d := newResolveFixture().withBooks(16)
	var inFlight, peak atomic.Int64
	both := make(chan struct{})
	var closeOnce sync.Once
	realID := 30
	d.fetch = func(context.Context, string) (*int, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 2 {
			closeOnce.Do(func() { close(both) })
		}
		select {
		case <-both:
		case <-time.After(2 * time.Second):
		}
		return &realID, nil
	}
	p := &Plugin{deps: d}
	rep := &opIDReporter{id: "op-x"}
	require.NoError(t, runGuarded(t, p, context.Background(), json.RawMessage(`{"dry_run":false}`), rep))
	fetched, inv, _, _ := d.snapshot()
	require.Len(t, fetched, 16)
	require.Equal(t, 1, inv)
	require.GreaterOrEqual(t, peak.Load(), int64(2), "the apply fetched one book at a time")
	require.LessOrEqual(t, peak.Load(), int64(resolveProductionAuthorsMaxWorkers), "the pool must stay bounded")
	require.Contains(t, strings.Join(rep.logs, "\n"), "Resolved 16 books across 2 production companies")
}

// The pool size follows metadata_scoring.bulk_fetch_workers (the bulk metadata
// fetch's setting), defaults to 4 when unset, and is capped at 8.
func TestResolveProductionAuthorsWorkers(t *testing.T) {
	prev := config.AppConfig.MetadataScoring.BulkFetchWorkers
	t.Cleanup(func() { config.AppConfig.MetadataScoring.BulkFetchWorkers = prev })
	for _, tc := range []struct{ set, want int }{{0, 4}, {-1, 4}, {2, 2}, {8, 8}, {50, 8}} {
		config.AppConfig.MetadataScoring.BulkFetchWorkers = tc.set
		require.Equal(t, tc.want, resolveProductionAuthorsWorkers(), "bulk_fetch_workers=%d", tc.set)
	}
}

// The def carries the merged contract (04-P4d): the loser ID as a FormerID, the
// loser's gate, timeout and resume policy, and the network capabilities of the
// fetch path it runs.
func TestResolveProductionAuthorsDef_MergeContract(t *testing.T) {
	def := New(fakeDeps{}).resolveProductionAuthorsDef()
	require.Equal(t, []string{"scheduler.resolve-production-authors"}, def.FormerIDs)
	require.Equal(t, []auth.Permission{auth.PermSettingsManage}, def.Permissions)
	require.GreaterOrEqual(t, def.Timeout, 2*time.Hour)
	require.Equal(t, sdk.ResumeDrop, def.ResumePolicy)
	require.Subset(t, def.Capabilities, []sdk.Capability{
		sdk.CapLibraryWrite, sdk.CapNetworkAudible, sdk.CapNetworkGeneric, sdk.CapNetworkOpenAI,
	})
}
