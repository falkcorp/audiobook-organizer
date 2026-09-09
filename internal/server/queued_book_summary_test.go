// file: internal/server/queued_book_summary_test.go
// version: 1.0.0
// guid: 0e58a3b6-2d17-4c90-b8f4-51c9e7a26d38
// last-edited: 2026-09-09

// The three OperationDef.SummarizeQueued hooks for the ops that hold a growing
// list of book IDs while queued.
//
// Each test feeds the summarizer the JSON a real queued row would hold — i.e.
// json.Marshal of the op's own params struct — rather than a hand-written
// literal. A literal would keep passing after a field was renamed, which is the
// exact failure the hooks must not have: a decode miss makes an op report
// nothing, and nothing is what it reported before this existed.
package server

import (
	"encoding/json"
	"testing"
)

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return raw
}

func TestSummarizeBatchApplyQueued(t *testing.T) {
	tests := []struct {
		name        string
		params      batchApplyOpParams
		wantDone    int
		wantTotal   int
		wantMessage string
	}{
		{
			name:        "a fresh batch reports its whole selection",
			params:      batchApplyOpParams{BookIDs: []string{"a", "b", "c"}},
			wantTotal:   3,
			wantMessage: "3 books to apply",
		},
		{
			// The counts must match what Run reports on its first tick
			// (UpdateProgress(priorDone, priorDone+len(BookIDs))), so the numbers
			// do not jump the moment the op starts.
			name: "a resumed batch counts prior work in the total and calls it out",
			params: batchApplyOpParams{
				BookIDs:       []string{"d", "e"},
				OriginalTotal: 10,
			},
			wantDone:    8,
			wantTotal:   10,
			wantMessage: "10 books to apply (8 already done)",
		},
		{
			name:        "one book is singular",
			params:      batchApplyOpParams{BookIDs: []string{"a"}},
			wantTotal:   1,
			wantMessage: "1 book to apply",
		},
		{
			// Nothing to say beats saying "0 books": an empty summary leaves any
			// text already on the row alone rather than blanking it.
			name:   "an empty selection says nothing",
			params: batchApplyOpParams{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			done, total, msg := summarizeBatchApplyQueued(mustMarshal(t, tt.params))
			if done != tt.wantDone || total != tt.wantTotal || msg != tt.wantMessage {
				t.Errorf("summarizeBatchApplyQueued = (%d, %d, %q), want (%d, %d, %q)",
					done, total, msg, tt.wantDone, tt.wantTotal, tt.wantMessage)
			}
		})
	}
}

func TestSummarizeBatchSaveQueued(t *testing.T) {
	done, total, msg := summarizeBatchSaveQueued(
		mustMarshal(t, batchSaveOpParams{BookIDs: []string{"a", "b"}, Organize: true}))
	if done != 0 || total != 2 || msg != "2 books to save" {
		t.Errorf("summarizeBatchSaveQueued = (%d, %d, %q), want (0, 2, %q)",
			done, total, msg, "2 books to save")
	}
}

func TestSummarizeBulkWriteBackQueued(t *testing.T) {
	done, total, msg := summarizeBulkWriteBackQueued(
		mustMarshal(t, bulkWriteBackOpParams{BookIDs: []string{"a", "b", "c", "d"}, Rename: true}))
	if done != 0 || total != 4 || msg != "4 books to write back" {
		t.Errorf("summarizeBulkWriteBackQueued = (%d, %d, %q), want (0, 4, %q)",
			done, total, msg, "4 books to write back")
	}
}

// Params that do not decode must produce silence, not a confident zero. A
// queued row already showing "1,204 books to apply" must not be rewritten to
// "0 books" because a future params change broke the decode.
func TestSummarizeQueued_UndecodableParamsSayNothing(t *testing.T) {
	garbage := json.RawMessage(`{"book_ids": "not-an-array"}`)
	for name, fn := range map[string]func(json.RawMessage) (int, int, string){
		"batch-apply":     summarizeBatchApplyQueued,
		"batch-save":      summarizeBatchSaveQueued,
		"bulk-write-back": summarizeBulkWriteBackQueued,
	} {
		t.Run(name, func(t *testing.T) {
			done, total, msg := fn(garbage)
			if done != 0 || total != 0 || msg != "" {
				t.Errorf("undecodable params must yield no summary, got (%d, %d, %q)", done, total, msg)
			}
		})
	}
}

// The merge that grows a queued row and the summary that describes it must
// agree. This drives the REAL merger and then the REAL summarizer, which is
// what the registry does on every merge — a test that only counted a
// hand-built list would not catch the two drifting apart.
func TestSummarizeBatchApplyQueued_AgreesWithTheQueueMerger(t *testing.T) {
	existing := mustMarshal(t, batchApplyOpParams{BookIDs: []string{"a", "b"}, OriginalTotal: 5})
	incoming := mustMarshal(t, batchApplyOpParams{BookIDs: []string{"b", "c"}, OriginalTotal: 2})

	merged, ok, err := mergeBatchApplyQueuedParams(existing, incoming)
	if err != nil || !ok {
		t.Fatalf("mergeBatchApplyQueuedParams: ok=%v err=%v", ok, err)
	}

	// The existing run had already finished 3 of its 5 (5 - 2 remaining), and
	// the union of remaining work is a, b, c. So 3 done out of 6.
	done, total, msg := summarizeBatchApplyQueued(merged)
	if done != 3 || total != 6 {
		t.Errorf("summary of merged params = (%d done, %d total), want (3, 6)", done, total)
	}
	if msg != "6 books to apply (3 already done)" {
		t.Errorf("message = %q", msg)
	}
}
