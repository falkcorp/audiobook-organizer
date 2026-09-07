// file: internal/metabatch/fetch_ops_index_test.go
// version: 1.1.0
// guid: 5b7e0a34-9c26-4d18-a0f7-3e9b2c41d685
// last-edited: 2026-09-07

package metabatch_test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
)

// fetchIndexStore answers both listings and nothing else.
type fetchIndexStore struct {
	database.MockStore
	ops    []database.Operation
	opsErr error
	v2Rows []database.OperationV2Row
	v2Err  error

	v2ByID    map[string]*database.OperationV2Row
	v1ByID    map[string]*database.Operation
	v1Params  map[string][]byte
	v2Calls   int
	v1Calls   int
	paramCall int
}

func (s *fetchIndexStore) GetRecentOperations(limit int) ([]database.Operation, error) {
	s.v1Calls++
	return s.ops, s.opsErr
}

func (s *fetchIndexStore) ListOperationsV2Since(since time.Time, limit int) ([]database.OperationV2Row, error) {
	s.v2Calls++
	return s.v2Rows, s.v2Err
}

func (s *fetchIndexStore) GetOperationV2(id string) (*database.OperationV2Row, error) {
	if s.v2ByID == nil {
		return nil, nil
	}
	return s.v2ByID[id], nil
}

func (s *fetchIndexStore) GetOperationParams(opID string) ([]byte, error) {
	s.paramCall++
	if s.v1Params == nil {
		return nil, nil
	}
	return s.v1Params[opID], nil
}

// ---------------------------------------------------------------------------
// RemainingBooksToFetch — the resume guard
// ---------------------------------------------------------------------------

// The regression this exists for: ResumePolicy=ResumeRestart re-enters Run with
// the ORIGINAL book list, so a filter that does not actually remove fetched
// books turns every restart into a full re-fetch.
func TestRemainingBooksToFetch_SkipsBooksThatAlreadyHaveResults(t *testing.T) {
	existing := []database.OperationResult{
		{BookID: "b1"}, {BookID: "b2"},
	}
	got := metabatch.RemainingBooksToFetch(existing, []string{"b1", "b2", "b3", "b4"})

	want := []string{"b3", "b4"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v (order preserved), got %v", want, got)
		}
	}
}

func TestRemainingBooksToFetch_AllDoneYieldsEmpty(t *testing.T) {
	existing := []database.OperationResult{{BookID: "b1"}, {BookID: "b2"}}
	if got := metabatch.RemainingBooksToFetch(existing, []string{"b1", "b2"}); len(got) != 0 {
		t.Fatalf("expected nothing left to fetch, got %v", got)
	}
}

// A fresh run has no result rows and must fetch the whole list — the filter
// must not silently drop work on the common path.
func TestRemainingBooksToFetch_NoResultsFetchesEverything(t *testing.T) {
	want := []string{"b1", "b2", "b3"}
	got := metabatch.RemainingBooksToFetch(nil, want)
	if len(got) != len(want) {
		t.Fatalf("a fresh run must fetch all %d books, got %v", len(want), got)
	}
}

// A result row for a book that was never requested must not shrink the list.
func TestRemainingBooksToFetch_IgnoresUnrelatedResults(t *testing.T) {
	existing := []database.OperationResult{{BookID: "other"}}
	got := metabatch.RemainingBooksToFetch(existing, []string{"b1", "b2"})
	if len(got) != 2 {
		t.Fatalf("unrelated result must not skip requested books, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// CandidateFetchOps — the v2 listing
// ---------------------------------------------------------------------------

func TestCandidateFetchOps_ListsV2NewestFirstAndFiltersOtherDefs(t *testing.T) {
	now := time.Now()
	store := &fetchIndexStore{
		v2Rows: []database.OperationV2Row{
			{ID: "new", DefID: metabatch.CandidateFetchDefID, Status: "running", QueuedAt: now},
			{ID: "old", DefID: metabatch.CandidateFetchDefID, Status: "completed", QueuedAt: now.Add(-time.Hour)},
			{ID: "other-def", DefID: "library.scan", Status: "running", QueuedAt: now},
		},
	}

	got := metabatch.CandidateFetchOps(store, 100)
	if len(got) != 2 {
		t.Fatalf("expected exactly the 2 candidate fetches, got %d: %+v", len(got), got)
	}
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Fatalf("expected newest-first [new old], got [%s %s]", got[0].ID, got[1].ID)
	}
}

func TestCandidateFetchOps_DoesNotDoubleCountAnID(t *testing.T) {
	now := time.Now()
	store := &fetchIndexStore{
		v2Rows: []database.OperationV2Row{
			{ID: "dup", DefID: metabatch.CandidateFetchDefID, Status: "completed", QueuedAt: now},
			{ID: "dup", DefID: metabatch.CandidateFetchDefID, Status: "running", QueuedAt: now},
		},
	}
	got := metabatch.CandidateFetchOps(store, 100)
	if len(got) != 1 {
		t.Fatalf("a repeated id must appear once, got %d", len(got))
	}
}

// A listing error degrades to an empty list rather than a panic or a partial
// answer presented as complete.
func TestCandidateFetchOps_ListingErrorYieldsNothing(t *testing.T) {
	store := &fetchIndexStore{v2Err: fmt.Errorf("v2 down")}
	if got := metabatch.CandidateFetchOps(store, 100); len(got) != 0 {
		t.Fatalf("a failed listing must yield nothing, got %+v", got)
	}
}

// The v2 listing must actually be consulted — a version that queried nothing
// would pass several tests above by accident.
//
// This asserted BOTH keyspaces until 2026-09-07. The v1 pass is gone, and with
// it v1Calls: nothing has minted a v1 row since the minter was retired, so that
// listing could only ever return pre-2026-08-23 history.
func TestCandidateFetchOps_QueriesTheV2Keyspace(t *testing.T) {
	store := &fetchIndexStore{}
	metabatch.CandidateFetchOps(store, 100)
	if store.v2Calls != 1 {
		t.Errorf("expected 1 v2 listing call, got %d", store.v2Calls)
	}
	if store.v1Calls != 0 {
		t.Errorf("the v1 keyspace must no longer be consulted, got %d call(s)", store.v1Calls)
	}
}

// ---------------------------------------------------------------------------
// CandidateFetchBookIDs — two params shapes
// ---------------------------------------------------------------------------

func TestCandidateFetchBookIDs_ReadsV2ParamsShape(t *testing.T) {
	store := &fetchIndexStore{
		v2ByID: map[string]*database.OperationV2Row{
			"op1": {ID: "op1", Params: `{"book_ids":["b1","b2"],"total_books":2}`},
		},
	}
	got := metabatch.CandidateFetchBookIDs(store, metabatch.CandidateFetchOp{ID: "op1"})
	if len(got) != 2 || got[0] != "b1" || got[1] != "b2" {
		t.Fatalf("expected [b1 b2] from v2 params, got %v", got)
	}
	if store.paramCall != 0 {
		t.Error("a v2 op must not read the v1 params blob")
	}
}

// TestCandidateFetchBookIDs_ReadsLegacyBareArrayShape was deleted on 2026-09-07
// with the v1 params branch it covered. SaveOperationParams had no production
// writer left, and the v1 listing pass that produced the only ops the branch
// applied to is gone, so the bare-[]string shape is unreachable.

func TestCandidateFetchBookIDs_MalformedParamsYieldNothing(t *testing.T) {
	store := &fetchIndexStore{
		v2ByID: map[string]*database.OperationV2Row{"op1": {ID: "op1", Params: `not json`}},
	}
	if got := metabatch.CandidateFetchBookIDs(store, metabatch.CandidateFetchOp{ID: "op1"}); got != nil {
		t.Errorf("malformed v2 params must yield nothing, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// IsActiveFetchStatus — two status vocabularies
// ---------------------------------------------------------------------------

// v1 queues at "pending", v2 at "queued". A guard that knew only one spelling
// would let a second fetch re-request every book the first had queued.
func TestIsActiveFetchStatus_SpansBothVocabularies(t *testing.T) {
	for _, s := range []string{"pending", "queued", "running"} {
		if !metabatch.IsActiveFetchStatus(s) {
			t.Errorf("%q must count as active", s)
		}
	}
	for _, s := range []string{"completed", "failed", "canceled", "interrupted", ""} {
		if metabatch.IsActiveFetchStatus(s) {
			t.Errorf("%q must not count as active", s)
		}
	}
}

// ---------------------------------------------------------------------------
// ResolveCandidateFetch — an id from either era must resolve
// ---------------------------------------------------------------------------

func (s *fetchIndexStore) GetOperationByID(id string) (*database.Operation, error) {
	if s.v1ByID == nil {
		return nil, nil
	}
	return s.v1ByID[id], nil
}

// The #2747 shape: a run keyed in v2 must not 404 at an endpoint that used to
// look ids up in v1 only.
func TestResolveCandidateFetch_FindsV2KeyedRun(t *testing.T) {
	now := time.Now()
	store := &fetchIndexStore{
		v2ByID: map[string]*database.OperationV2Row{
			"v2op": {
				ID: "v2op", DefID: metabatch.CandidateFetchDefID, Status: "running",
				ProgressCurrent: 3, ProgressTotal: 10, ProgressMessage: "fetched 3/10",
				QueuedAt: now,
			},
		},
	}
	op := metabatch.ResolveCandidateFetch(store, "v2op")
	if op == nil {
		t.Fatal("a v2-keyed run must resolve, not 404")
	}
	if op.Progress != 3 || op.Total != 10 || op.Message != "fetched 3/10" {
		t.Errorf("progress must carry across the shape mapping, got %d/%d %q",
			op.Progress, op.Total, op.Message)
	}
	if op.Status != "running" {
		t.Errorf("expected status running, got %q", op.Status)
	}
}

// History keyed in v1 must keep resolving too.
func TestResolveCandidateFetch_StillFindsLegacyRun(t *testing.T) {
	store := &fetchIndexStore{
		v1ByID: map[string]*database.Operation{
			"v1op": {ID: "v1op", Type: "metadata_candidate_fetch", Status: "completed", Progress: 5, Total: 5},
		},
	}
	op := metabatch.ResolveCandidateFetch(store, "v1op")
	if op == nil || op.ID != "v1op" || op.Progress != 5 {
		t.Fatalf("a v1-keyed run must still resolve, got %+v", op)
	}
}

func TestResolveCandidateFetch_UnknownIDResolvesToNil(t *testing.T) {
	store := &fetchIndexStore{}
	if op := metabatch.ResolveCandidateFetch(store, "nope"); op != nil {
		t.Fatalf("an unknown id must resolve to nil so the caller 404s, got %+v", op)
	}
}

// ---------------------------------------------------------------------------
// The limit must bound the ANSWER, not the store scan
// ---------------------------------------------------------------------------

// listCapStore models ListOperationsV2Since faithfully on the point that
// matters: it sorts StartedAt DESC NULLS LAST and truncates to `limit` BEFORE
// the caller can filter by DefID. A queued op has StartedAt == nil, so it sorts
// last and is the first row dropped.
type listCapStore struct {
	database.MockStore
	rows       []database.OperationV2Row
	gotV2Limit int
}

func (s *listCapStore) GetRecentOperations(int) ([]database.Operation, error) { return nil, nil }

func (s *listCapStore) ListOperationsV2Since(_ time.Time, limit int) ([]database.OperationV2Row, error) {
	s.gotV2Limit = limit
	all := append([]database.OperationV2Row(nil), s.rows...)
	sort.SliceStable(all, func(i, j int) bool {
		si, sj := all[i].StartedAt, all[j].StartedAt
		if si == nil && sj == nil {
			return all[i].QueuedAt.After(all[j].QueuedAt)
		}
		if si == nil {
			return false
		}
		if sj == nil {
			return true
		}
		return si.After(*sj)
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// THE BUG: passing the caller's small limit straight through meant that once
// enough unrelated ops had STARTED, a just-queued candidate fetch fell off the
// bottom of the store's sort and the per-book dedup guard stopped seeing it —
// so a second request re-requested every book the first had queued. That is the
// precise failure the guard exists to prevent.
func TestCandidateFetchOps_QueuedRunSurvivesACrowdedOpsTable(t *testing.T) {
	now := time.Now()
	started := now.Add(-time.Minute)

	var rows []database.OperationV2Row
	// 300 unrelated ops that have already STARTED, so they sort above anything queued.
	for i := range 300 {
		rows = append(rows, database.OperationV2Row{
			ID: fmt.Sprintf("other-%d", i), DefID: "library.scan",
			Status: "running", QueuedAt: now, StartedAt: &started,
		})
	}
	// One candidate fetch that is QUEUED — StartedAt nil, so it sorts dead last.
	rows = append(rows, database.OperationV2Row{
		ID: "queued-fetch", DefID: metabatch.CandidateFetchDefID,
		Status: "queued", QueuedAt: now,
	})

	store := &listCapStore{rows: rows}
	got := metabatch.CandidateFetchOps(store, 200)

	if len(got) != 1 || got[0].ID != "queued-fetch" {
		t.Fatalf("a queued fetch must survive a crowded ops table; got %+v (store was asked for limit=%d)",
			got, store.gotV2Limit)
	}
	if !metabatch.IsActiveFetchStatus(got[0].Status) {
		t.Errorf("the queued fetch must read as active, got %q", got[0].Status)
	}
}

// The caller's limit still bounds what comes back — it just applies to candidate
// fetches rather than to the raw scan.
func TestCandidateFetchOps_LimitBoundsTheResult(t *testing.T) {
	now := time.Now()
	var rows []database.OperationV2Row
	for i := range 10 {
		rows = append(rows, database.OperationV2Row{
			ID: fmt.Sprintf("f-%d", i), DefID: metabatch.CandidateFetchDefID,
			Status: "completed", QueuedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	got := metabatch.CandidateFetchOps(&listCapStore{rows: rows}, 3)
	if len(got) != 3 {
		t.Fatalf("limit must bound the returned fetches, got %d", len(got))
	}
	if got[0].ID != "f-0" {
		t.Errorf("the limit must keep the NEWEST, got %s first", got[0].ID)
	}
}

// A non-fetch op id must not resolve as a fetch. GET /operations/:id/results is
// a generic route, so without a DefID guard a library.scan id would come back
// 200 with an empty result set and a fabricated type.
func TestResolveCandidateFetch_RejectsANonFetchV2Op(t *testing.T) {
	store := &fetchIndexStore{
		v2ByID: map[string]*database.OperationV2Row{
			"scan-op": {ID: "scan-op", DefID: "library.scan", Status: "running"},
		},
	}
	if op := metabatch.ResolveCandidateFetch(store, "scan-op"); op != nil {
		t.Fatalf("a library.scan id must not resolve as a candidate fetch, got %+v", op)
	}
}
