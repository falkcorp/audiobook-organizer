// file: internal/database/dedup_label_test.go
// version: 1.0.3
// guid: 28cfcafd-ac95-4175-8fe7-b0fc46bd05bb
// last-edited: 2026-10-03

package database

import (
	"testing"
)

func newTestLabelStore(t *testing.T) *EmbeddingStore {
	t.Helper()
	ps, err := NewPebbleStoreInMemory("labels")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	return NewEmbeddingStore(ps.DB())
}

func TestLabeledExample_RoundTripAndFilter(t *testing.T) {
	es := newTestLabelStore(t)

	ex := LabeledExample{
		CandidateID:       42,
		EntityAID:         "a",
		EntityBID:         "b",
		Layer:             "exact",
		Label:             "not_dup",
		LabelSource:       "rule",
		LabelReason:       "duration ratio 0.02 — part vs whole",
		FolderRelation:    "sibling_parts", // planned value, not yet produced by the builder; exercises round-trip storage of an arbitrary valid string
		SignatureRelation: "unknown",
	}
	if err := es.UpsertLabeledExample(ex); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := es.GetLabeledExample(42)
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.LabelReason != ex.LabelReason || got.Label != "not_dup" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	list, err := es.ListLabeledExamples(LabeledExampleFilter{Label: "not_dup", Limit: 10})
	if err != nil || len(list) != 1 {
		t.Fatalf("list by label: err=%v len=%d", err, len(list))
	}
	n, err := es.CountLabeledExamples(LabeledExampleFilter{LabelSource: "rule"})
	if err != nil || n != 1 {
		t.Fatalf("count by source: err=%v n=%d", err, n)
	}
}

func TestLabeledExample_AbsentAndOffset(t *testing.T) {
	es := newTestLabelStore(t)

	// Absent key returns (nil, nil) — no error.
	got, err := es.GetLabeledExample(99999)
	if err != nil {
		t.Fatalf("get absent: unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("get absent: expected nil, got %+v", got)
	}

	// Upsert two examples with the same label so offset paging can be tested.
	for _, id := range []int64{1, 2} {
		ex := LabeledExample{
			CandidateID: id,
			EntityAID:   "a",
			EntityBID:   "b",
			Layer:       "exact",
			Label:       "not_dup",
			LabelSource: "rule",
		}
		if err := es.UpsertLabeledExample(ex); err != nil {
			t.Fatalf("upsert id=%d: %v", id, err)
		}
	}

	// Offset=1 on two matching rows must return exactly 1 row.
	page, err := es.ListLabeledExamples(LabeledExampleFilter{Label: "not_dup", Offset: 1, Limit: 10})
	if err != nil {
		t.Fatalf("list with offset: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("list with offset: expected 1 row, got %d", len(page))
	}
}
