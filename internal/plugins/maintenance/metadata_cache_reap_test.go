// file: internal/plugins/maintenance/metadata_cache_reap_test.go
// version: 1.0.0
// guid: 2f7a5c81-9de3-4b06-8c14-a35f7e290bd6
// last-edited: 2026-08-20

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// reapFakeCache records every DeleteMetadataCache so a test can assert BOTH what
// was reaped and — the point of a delete op's test suite — that nothing else was.
type reapFakeCache struct {
	mu      sync.Mutex // Delete is called from RunItems' worker pool
	rows    []database.MetadataCacheSummary
	deleted []string
	listErr error
	delErr  map[string]error
}

func (f *reapFakeCache) ListMetadataCacheKeys() ([]database.MetadataCacheSummary, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rows, nil
}

func (f *reapFakeCache) DeleteMetadataCache(bookID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.delErr[bookID]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, bookID)
	return nil
}

func (f *reapFakeCache) GetMetadataCache(string) (*database.MetadataCandidateCache, error) {
	return nil, nil
}
func (f *reapFakeCache) PutMetadataCache(*database.MetadataCandidateCache) error { return nil }

var _ database.MetadataCacheStore = (*reapFakeCache)(nil)

func (f *reapFakeCache) deletedSorted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.deleted...)
	sort.Strings(out)
	return out
}

// reapFakeBooks resolves book IDs. present holds the books that exist; errs
// injects a store fault for specific IDs; reviveAfter names IDs that return
// (nil, nil) on the FIRST lookup and a real book on every later one — which is
// exactly the scan-then-recheck race the apply phase has to survive.
type reapFakeBooks struct {
	mu          sync.Mutex
	present     map[string]bool
	errs        map[string]error
	reviveAfter map[string]int
	calls       map[string]int
}

func (f *reapFakeBooks) GetBookByID(id string) (*database.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[id]++
	if err := f.errs[id]; err != nil {
		return nil, err
	}
	if after, ok := f.reviveAfter[id]; ok {
		if f.calls[id] > after {
			return &database.Book{ID: id}, nil
		}
		return nil, nil
	}
	if f.present[id] {
		return &database.Book{ID: id}, nil
	}
	// Genuinely absent: (nil, nil), not an error. The whole safety argument for
	// deleting rests on the store spelling absence this way.
	return nil, nil
}

// seedReap builds n cache rows with ids "b00".."bNN", handed over in REVERSE id
// order. That is deliberate: the real ListMetadataCacheKeys sorts by FetchedAt
// descending, which has nothing to do with book ID, so a fixture that arrives
// pre-sorted by ID would let the cap tests pass with the op's own sort deleted.
func seedReap(n int) *reapFakeCache {
	rows := make([]database.MetadataCacheSummary, 0, n)
	for i := n - 1; i >= 0; i-- {
		rows = append(rows, database.MetadataCacheSummary{
			BookID:         fmt.Sprintf("b%02d", i),
			FetchedAt:      time.Now().Add(-time.Duration(i) * time.Hour),
			CandidateCount: i + 1,
		})
	}
	return &reapFakeCache{rows: rows}
}

func runReapPlan(t *testing.T, cache *reapFakeCache, books *reapFakeBooks, params metadataCacheReapParams) reapPlan {
	t.Helper()
	plan, err := planMetadataCacheReap(context.Background(), cache, books, params, &fakeReporter{})
	require.NoError(t, err)
	// The identity that has to hold on every run, asserted on every run rather
	// than only in the test that is about it.
	require.True(t, plan.bucketsClose(),
		"buckets must partition the scanned set: %d+%d+%d+%d != %d",
		plan.Resolves, plan.Orphaned, plan.OrphanedCapped, plan.LookupErrs, plan.ScannedRows)
	return plan
}

// A dry run must classify everything and write nothing. This is the default
// mode, so a regression here silently turns a report into a purge.
func TestMetadataCacheReap_DryRunDeletesNothing(t *testing.T) {
	cache := seedReap(4)
	books := &reapFakeBooks{present: map[string]bool{"b00": true, "b02": true}}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: false})

	require.Equal(t, 4, plan.ScannedRows)
	require.Equal(t, 2, plan.Resolves)
	require.Equal(t, 2, plan.Orphaned, "b01 and b03 have no book")
	require.Equal(t, 0, plan.Reaped)
	require.Empty(t, cache.deletedSorted(), "a dry run must not call Delete at all")
}

func TestMetadataCacheReap_ApplyDeletesOnlyOrphans(t *testing.T) {
	cache := seedReap(4)
	books := &reapFakeBooks{present: map[string]bool{"b00": true, "b02": true}}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: true})

	require.Equal(t, 2, plan.Reaped)
	require.Equal(t, 0, plan.Revived)
	require.Equal(t, 0, plan.DeleteErrs)
	require.Equal(t, []string{"b01", "b03"}, cache.deletedSorted(),
		"only the rows whose book does not resolve")
}

// THE safety property. A store fault is not an absence. If a lookup error were
// folded into the orphan bucket, one bad day for Pebble would read as thousands
// of books to forget — so an unresolvable row is counted, reported, and skipped.
func TestMetadataCacheReap_LookupErrorIsNeverDeleted(t *testing.T) {
	cache := seedReap(3)
	books := &reapFakeBooks{
		present: map[string]bool{"b00": true},
		errs:    map[string]error{"b01": errors.New("pebble: io error")},
	}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: true})

	require.Equal(t, 1, plan.LookupErrs)
	require.Equal(t, 1, plan.Resolves)
	require.Equal(t, 1, plan.Orphaned, "only b02 is genuinely absent")
	require.Equal(t, []string{"b02"}, cache.deletedSorted(),
		"the row whose book could not be resolved must survive")

	// And it must be reported as its own cause, not silently absorbed.
	var found bool
	for _, d := range plan.all {
		if d.BookID == "b01" {
			require.Equal(t, "lookup-error", d.Bucket)
			require.Contains(t, d.Reason, "pebble: io error")
			found = true
		}
	}
	require.True(t, found, "the unresolvable row must appear in the report")
}

// The scan is a snapshot; the store is live. A book restored or re-imported
// between the scan and the write is no longer an orphan, and its cached
// candidates are worth keeping. The re-check at delete time is what turns that
// race from data loss into a spared row.
func TestMetadataCacheReap_RevivedBookIsSparedAtWriteTime(t *testing.T) {
	cache := seedReap(3)
	books := &reapFakeBooks{
		present: map[string]bool{"b00": true},
		// b01 looks absent during the scan and resolves on the re-check.
		reviveAfter: map[string]int{"b01": 1},
	}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: true})

	require.Equal(t, 2, plan.Orphaned, "the scan saw two orphans")
	require.Equal(t, 1, plan.Revived, "one of them came back before the write")
	require.Equal(t, 1, plan.Reaped)
	require.Equal(t, []string{"b02"}, cache.deletedSorted(),
		"the revived book's cache row must survive")
}

// A re-check that FAILS is not permission to delete either — same reasoning as
// the scan-phase lookup error, at the other end of the run.
func TestMetadataCacheReap_RecheckErrorSparesTheRow(t *testing.T) {
	cache := seedReap(2)
	books := &reapFakeBooks{}
	// Both rows look orphaned in the scan; b00's re-check then faults.
	plan, err := planMetadataCacheReap(context.Background(), cache,
		&recheckFailBooks{inner: books, failAfter: map[string]int{"b00": 1}},
		metadataCacheReapParams{Apply: true}, &fakeReporter{})
	require.NoError(t, err)

	require.Equal(t, 2, plan.Orphaned)
	require.Equal(t, 1, plan.DeleteErrs, "the faulting re-check is counted, not swallowed")
	require.Equal(t, 1, plan.Reaped)
	require.Equal(t, []string{"b01"}, cache.deletedSorted())
}

// recheckFailBooks resolves normally until a given call count, then errors —
// letting a test fault the SECOND lookup (the re-check) while the first (the
// scan) succeeds.
type recheckFailBooks struct {
	inner     *reapFakeBooks
	failAfter map[string]int
	mu        sync.Mutex
	calls     map[string]int
}

func (f *recheckFailBooks) GetBookByID(id string) (*database.Book, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[id]++
	n := f.calls[id]
	f.mu.Unlock()
	if after, ok := f.failAfter[id]; ok && n > after {
		return nil, errors.New("pebble: io error on recheck")
	}
	return f.inner.GetBookByID(id)
}

// A failing delete must be counted, and must not be reported as reaped.
func TestMetadataCacheReap_DeleteErrorIsCountedNotReaped(t *testing.T) {
	cache := seedReap(3)
	cache.delErr = map[string]error{"b01": errors.New("pebble: write failed")}
	books := &reapFakeBooks{present: map[string]bool{"b00": true}}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: true})

	require.Equal(t, 2, plan.Orphaned)
	require.Equal(t, 1, plan.Reaped, "only b02 actually went away")
	require.Equal(t, 1, plan.DeleteErrs)
	require.Equal(t, []string{"b02"}, cache.deletedSorted())
}

// The cap must take a STABLE prefix, so a capped run resumed later continues
// through the set instead of re-sampling a different arbitrary slice — and the
// rows it left behind must still be accounted for.
func TestMetadataCacheReap_CapTakesDeterministicPrefix(t *testing.T) {
	cache := seedReap(5) // b00..b04, none present -> all orphaned
	books := &reapFakeBooks{}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: true, Max: 2})

	require.Equal(t, 2, plan.CappedAt)
	require.Equal(t, 2, plan.Orphaned)
	require.Equal(t, 3, plan.OrphanedCapped, "the rest are counted, not dropped")
	require.Equal(t, []string{"b00", "b01"}, cache.deletedSorted(),
		"the first N by book ID, so a re-run continues rather than re-samples")

	// Re-running against what would remain takes the NEXT prefix, not the same one.
	var remaining []database.MetadataCacheSummary
	for _, r := range cache.rows {
		if r.BookID != "b00" && r.BookID != "b01" {
			remaining = append(remaining, r)
		}
	}
	rest := &reapFakeCache{rows: remaining}
	plan2 := runReapPlan(t, rest, &reapFakeBooks{}, metadataCacheReapParams{Apply: true, Max: 2})
	require.Equal(t, []string{"b02", "b03"}, rest.deletedSorted())
	require.Equal(t, 1, plan2.OrphanedCapped)
}

// An empty book_id cannot be resolved AND cannot be deleted by key — a bare
// prefix delete would be a keyspace-wide hazard. It must land in lookup-error.
func TestMetadataCacheReap_EmptyBookIDIsNeverDeleted(t *testing.T) {
	cache := &reapFakeCache{rows: []database.MetadataCacheSummary{
		{BookID: "", CandidateCount: 3},
		{BookID: "b01", CandidateCount: 1},
	}}
	books := &reapFakeBooks{}

	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Apply: true})

	require.Equal(t, 1, plan.LookupErrs)
	require.Equal(t, 1, plan.Orphaned)
	require.Equal(t, []string{"b01"}, cache.deletedSorted(),
		"the empty key must never reach DeleteMetadataCache")
}

// The report is the only record of what a delete op destroyed. It must carry
// EVERY scanned row — the keep set is what makes the reap set believable.
func TestMetadataCacheReap_ReportListsEveryScannedRow(t *testing.T) {
	cache := seedReap(5)
	books := &reapFakeBooks{
		present: map[string]bool{"b00": true},
		errs:    map[string]error{"b01": errors.New("boom\twith\ttabs")},
	}
	plan := runReapPlan(t, cache, books, metadataCacheReapParams{Max: 2})

	path := filepath.Join(t.TempDir(), "nested", "reap.tsv")
	require.NoError(t, writeReapReport(path, plan.all))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	require.Len(t, lines, 1+plan.ScannedRows, "header plus one row per scanned entry")

	// Every data line must have exactly the header's column count. An error
	// message containing a tab is interpolated into `reason`, so this is a real
	// hazard, not a hypothetical one.
	cols := len(strings.Split(lines[0], "\t"))
	for _, ln := range lines[1:] {
		require.Len(t, strings.Split(ln, "\t"), cols, "column drift in row: %q", ln)
	}
	require.NotContains(t, string(raw), "boom\twith\ttabs")
	require.Contains(t, string(raw), "boom with tabs")
}

// A failure listing the cache must abort the run rather than conclude that the
// cache is empty and there is nothing to reap. "No rows found" and "could not
// look" must not produce the same outcome.
func TestMetadataCacheReap_ListFailureAborts(t *testing.T) {
	cache := &reapFakeCache{listErr: errors.New("pebble: iter failed")}
	_, err := planMetadataCacheReap(context.Background(), cache, &reapFakeBooks{},
		metadataCacheReapParams{Apply: true}, &fakeReporter{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "list metadata cache")
	require.Empty(t, cache.deletedSorted())
}
