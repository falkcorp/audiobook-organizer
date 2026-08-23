// file: internal/metabatch/fetch_ops_index_test.go
// version: 1.0.0
// guid: 5b7e0a34-9c26-4d18-a0f7-3e9b2c41d685
// last-edited: 2026-08-22

package metabatch_test

import (
	"fmt"
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
// CandidateFetchOps — the keyspace union
// ---------------------------------------------------------------------------

func TestCandidateFetchOps_UnionsBothKeyspacesNewestFirst(t *testing.T) {
	now := time.Now()
	store := &fetchIndexStore{
		v2Rows: []database.OperationV2Row{
			{ID: "new", DefID: metabatch.CandidateFetchDefID, Status: "running", QueuedAt: now},
			{ID: "other-def", DefID: "library.scan", Status: "running", QueuedAt: now},
		},
		ops: []database.Operation{
			{ID: "old", Type: "metadata_candidate_fetch", CreatedAt: now.Add(-time.Hour)},
			{ID: "other-type", Type: "library_scan", CreatedAt: now},
		},
	}

	got := metabatch.CandidateFetchOps(store, 100)
	if len(got) != 2 {
		t.Fatalf("expected exactly the 2 candidate fetches, got %d: %+v", len(got), got)
	}
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Fatalf("expected newest-first [new old], got [%s %s]", got[0].ID, got[1].ID)
	}
	if got[0].Legacy {
		t.Error("v2 row must not be flagged Legacy")
	}
	if !got[1].Legacy {
		t.Error("v1 row must be flagged Legacy")
	}
}

// The whole point of the union: history keyed under a v1 id stays visible after
// new runs stop writing v1 rows. Losing this reintroduces the "results are
// invisible" bug the Resume Review picker was written to fix.
func TestCandidateFetchOps_KeepsLegacyHistoryVisible(t *testing.T) {
	store := &fetchIndexStore{
		ops: []database.Operation{
			{ID: "historical", Type: "metadata_candidate_fetch", CreatedAt: time.Now()},
		},
	}
	got := metabatch.CandidateFetchOps(store, 100)
	if len(got) != 1 || got[0].ID != "historical" {
		t.Fatalf("legacy-only history must still be listed, got %+v", got)
	}
}

func TestCandidateFetchOps_DoesNotDoubleCountAnID(t *testing.T) {
	now := time.Now()
	store := &fetchIndexStore{
		v2Rows: []database.OperationV2Row{
			{ID: "dup", DefID: metabatch.CandidateFetchDefID, Status: "completed", QueuedAt: now},
		},
		ops: []database.Operation{
			{ID: "dup", Type: "metadata_candidate_fetch", CreatedAt: now},
		},
	}
	got := metabatch.CandidateFetchOps(store, 100)
	if len(got) != 1 {
		t.Fatalf("an id present in both keyspaces must appear once, got %d", len(got))
	}
	if got[0].Legacy {
		t.Error("the v2 view should win for an id present in both")
	}
}

// One keyspace failing must not blank the other — both directions.
func TestCandidateFetchOps_OneKeyspaceErrorDoesNotHideTheOther(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name  string
		store *fetchIndexStore
		want  string
	}{
		{
			name: "v1 errors",
			store: &fetchIndexStore{
				opsErr: fmt.Errorf("v1 down"),
				v2Rows: []database.OperationV2Row{
					{ID: "v2op", DefID: metabatch.CandidateFetchDefID, QueuedAt: now},
				},
			},
			want: "v2op",
		},
		{
			name: "v2 errors",
			store: &fetchIndexStore{
				v2Err: fmt.Errorf("v2 down"),
				ops: []database.Operation{
					{ID: "v1op", Type: "metadata_candidate_fetch", CreatedAt: now},
				},
			},
			want: "v1op",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := metabatch.CandidateFetchOps(tc.store, 100)
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("expected %s to survive, got %+v", tc.want, got)
			}
		})
	}
}

// Both listings must actually be consulted. A version that queried only one
// would pass several tests above by accident.
func TestCandidateFetchOps_QueriesBothKeyspaces(t *testing.T) {
	store := &fetchIndexStore{}
	metabatch.CandidateFetchOps(store, 100)
	if store.v2Calls != 1 {
		t.Errorf("expected 1 v2 listing call, got %d", store.v2Calls)
	}
	if store.v1Calls != 1 {
		t.Errorf("expected 1 v1 listing call, got %d", store.v1Calls)
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

// The v1 blob is a BARE []string, not FetchOpParams — decoding it with the
// wrong shape yields nothing, which would silently disable the dedup guard for
// every still-running legacy fetch.
func TestCandidateFetchBookIDs_ReadsLegacyBareArrayShape(t *testing.T) {
	store := &fetchIndexStore{
		v1Params: map[string][]byte{"op1": []byte(`["b1","b2","b3"]`)},
	}
	got := metabatch.CandidateFetchBookIDs(store, metabatch.CandidateFetchOp{ID: "op1", Legacy: true})
	if len(got) != 3 {
		t.Fatalf("expected 3 book ids from the legacy bare-array blob, got %v", got)
	}
}

func TestCandidateFetchBookIDs_MalformedParamsYieldNothing(t *testing.T) {
	store := &fetchIndexStore{
		v2ByID:   map[string]*database.OperationV2Row{"op1": {ID: "op1", Params: `not json`}},
		v1Params: map[string][]byte{"op2": []byte(`not json`)},
	}
	if got := metabatch.CandidateFetchBookIDs(store, metabatch.CandidateFetchOp{ID: "op1"}); got != nil {
		t.Errorf("malformed v2 params must yield nothing, got %v", got)
	}
	if got := metabatch.CandidateFetchBookIDs(store, metabatch.CandidateFetchOp{ID: "op2", Legacy: true}); got != nil {
		t.Errorf("malformed v1 params must yield nothing, got %v", got)
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
