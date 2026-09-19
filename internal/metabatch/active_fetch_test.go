// file: internal/metabatch/active_fetch_test.go
// version: 1.0.0
// guid: d7d00f50-68d2-4055-8d44-19e45731c3d6
// last-edited: 2026-09-19

package metabatch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

type activeOpsStub struct {
	rows []database.OperationV2Row
	err  error
}

func (s activeOpsStub) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return s.rows, s.err
}

// TestActiveCandidateFetchBookIDs_ReadsLiveWorkOnly: the in-flight guard must
// see a running fetch's books however much history has piled up after it, and
// must ignore other defs and finished rows.
func TestActiveCandidateFetchBookIDs_ReadsLiveWorkOnly(t *testing.T) {
	got, err := ActiveCandidateFetchBookIDs(activeOpsStub{rows: []database.OperationV2Row{
		{ID: "run", DefID: CandidateFetchDefID, Status: "running", Params: `{"book_ids":["a","b"],"total_books":2}`},
		{ID: "queued", DefID: CandidateFetchDefID, Status: "queued", Params: `{"book_ids":["c"]}`},
		{ID: "other", DefID: "library.scan", Status: "running", Params: `{"book_ids":["x"]}`},
		{ID: "done", DefID: CandidateFetchDefID, Status: "completed", Params: `{"book_ids":["y"]}`},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !got[id] {
			t.Errorf("book %s of a live fetch is missing from the guard set", id)
		}
	}
	for _, id := range []string{"x", "y"} {
		if got[id] {
			t.Errorf("book %s is not in a live candidate fetch but was guarded", id)
		}
	}
}

// TestActiveCandidateFetchBookIDs_ErrorIsNotEmpty: "could not look" must not
// read as "nothing is running", or the handler would enqueue a duplicate run.
func TestActiveCandidateFetchBookIDs_ErrorIsNotEmpty(t *testing.T) {
	if _, err := ActiveCandidateFetchBookIDs(activeOpsStub{err: errors.New("store closed")}); err == nil {
		t.Fatal("a failed listing returned no error")
	}
	if _, err := ActiveCandidateFetchBookIDs(activeOpsStub{rows: []database.OperationV2Row{
		{ID: "bad", DefID: CandidateFetchDefID, Status: "running", Params: "{not json"},
	}}); err == nil {
		t.Fatal("undecodable params of a live fetch returned no error")
	}
}
