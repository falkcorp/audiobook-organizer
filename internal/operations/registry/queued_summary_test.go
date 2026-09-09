// file: internal/operations/registry/queued_summary_test.go
// version: 1.0.0
// guid: 9d3c0e57-b41a-4f62-8e05-6a7c1b9042fd
// last-edited: 2026-09-09

// OperationDef.SummarizeQueued lets a run that has not started say how much
// work it is holding, so a pending batch apply reads as "1,204 books to apply"
// instead of a blank row.
//
// Every test here drives the REAL EnqueueOp and reads back through
// GetOperationV2. A test that called SummarizeQueued directly would prove only
// that the closure can count, which was never in doubt — the defect was that
// nothing invoked it.
package registry_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// summarizeBookIDs is the shape all three production hooks have: count the
// selection, say so.
func summarizeBookIDs(params json.RawMessage) (int, int, string) {
	var p dedupeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return 0, 0, ""
	}
	if len(p.BookIDs) == 0 {
		return 0, 0, ""
	}
	return 0, len(p.BookIDs), "counted"
}

func makeSummarizingDef(t *testing.T, id string) registry.OperationDef {
	t.Helper()
	def := makeDedupeDef(t, id, id)
	def.SummarizeQueued = summarizeBookIDs
	def.MergeQueuedParams = func(existing, incoming json.RawMessage) (json.RawMessage, bool, error) {
		var oldParams, newParams dedupeParams
		if err := json.Unmarshal(existing, &oldParams); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(incoming, &newParams); err != nil {
			return nil, false, err
		}
		seen := map[string]struct{}{}
		merged := dedupeParams{}
		for _, bookID := range append(oldParams.BookIDs, newParams.BookIDs...) {
			if _, ok := seen[bookID]; ok {
				continue
			}
			seen[bookID] = struct{}{}
			merged.BookIDs = append(merged.BookIDs, bookID)
		}
		out, err := json.Marshal(merged)
		return out, err == nil, err
	}
	return def
}

// A freshly enqueued row must already carry its size. Before SummarizeQueued
// the progress columns stayed zero until Run's first tick, which for a
// four-hour ConcurrencyKey-serialized op could be hours after the user asked.
func TestEnqueueOp_QueuedRowReportsItsSizeBeforeItStarts(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeSummarizingDef(t, "test.summarize-enqueue")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	opID, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}

	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2(%s): row=%v err=%v", opID, row, err)
	}
	if row.Status != "queued" {
		t.Fatalf("precondition: expected a queued row, got %q", row.Status)
	}
	if row.ProgressTotal != 3 {
		t.Errorf("queued row must advertise its size: ProgressTotal=%d, want 3", row.ProgressTotal)
	}
	if row.ProgressMessage != "counted" {
		t.Errorf("queued row must carry the summary message, got %q", row.ProgressMessage)
	}
}

// The one the user actually asked for: the queue merger keeps unioning newly
// approved books into a row that is already waiting, so the count GROWS while
// pending. The row must restate its size at that moment — nothing else revisits
// it before it runs.
func TestEnqueueOp_MergedQueuedRowRestatesItsGrownSize(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeSummarizingDef(t, "test.summarize-merge")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	second, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"b", "c", "d"}})
	if err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}
	if second != first {
		t.Fatalf("precondition: expected the second request to merge into %s, got %s", first, second)
	}

	row, err := store.GetOperationV2(first)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2(%s): row=%v err=%v", first, row, err)
	}
	// a, b, c, d — the union, not either request on its own. Asserting 4 rules
	// out both "the merge did not re-summarize" (2) and "the summary describes
	// only the incoming request" (3).
	if row.ProgressTotal != 4 {
		t.Errorf("merged queued row must report the union: ProgressTotal=%d, want 4", row.ProgressTotal)
	}
}

// A def with no hook keeps the old behavior exactly. This is the guard against
// "fix" turning into "every queued op now claims a size it cannot know".
func TestEnqueueOp_DefWithoutSummarizeQueuedLeavesProgressAlone(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeDedupeDef(t, "test.summarize-absent", "test.summarize-absent")
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	opID, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("EnqueueOp: %v", err)
	}
	row, err := store.GetOperationV2(opID)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2(%s): row=%v err=%v", opID, row, err)
	}
	if row.ProgressTotal != 0 || row.ProgressMessage != "" {
		t.Errorf("a def with no SummarizeQueued must write nothing: total=%d message=%q",
			row.ProgressTotal, row.ProgressMessage)
	}
}

// A hook that declines (zero counts, no message) must not blank a row. The
// registry treats an empty answer as "I have nothing to say", not as "write
// zeroes", so a later decode failure cannot erase a summary already displayed.
func TestEnqueueOp_SilentSummarizeQueuedDoesNotBlankTheRow(t *testing.T) {
	r, store := newTestRegistry(t)
	def := makeSummarizingDef(t, "test.summarize-silent")
	// Counts on the enqueue that creates the row, then declines on the merge —
	// standing in for a hook that can no longer decode the params it is handed.
	// A def cannot be re-registered, so the change of heart lives in the hook.
	calls := 0
	def.SummarizeQueued = func(params json.RawMessage) (int, int, string) {
		calls++
		if calls > 1 {
			return 0, 0, ""
		}
		return summarizeBookIDs(params)
	}
	if err := r.RegisterOp(def); err != nil {
		t.Fatalf("RegisterOp: %v", err)
	}

	first, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("first EnqueueOp: %v", err)
	}
	if _, err := r.EnqueueOp(context.Background(), def.ID, dedupeParams{BookIDs: []string{"c"}}); err != nil {
		t.Fatalf("second EnqueueOp: %v", err)
	}
	if calls < 2 {
		t.Fatalf("precondition: the merge must have consulted the hook, calls=%d", calls)
	}

	row, err := store.GetOperationV2(first)
	if err != nil || row == nil {
		t.Fatalf("GetOperationV2(%s): row=%v err=%v", first, row, err)
	}
	if row.ProgressMessage != "counted" || row.ProgressTotal != 2 {
		t.Errorf("a declining hook must leave the existing summary intact: total=%d message=%q",
			row.ProgressTotal, row.ProgressMessage)
	}
}
