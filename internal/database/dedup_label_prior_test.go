// file: internal/database/dedup_label_prior_test.go
// version: 1.0.0
// guid: 2c758413-c221-48ba-89b6-96100620b328
// last-edited: 2026-10-06

package database

import "testing"

func TestLabelPrior_RestoresTheEarlierVerdict(t *testing.T) {
	s := newTestEmbeddingStore(t)
	earlier := LabeledExample{CandidateID: 7, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "human", LabelReason: "user_merge"}
	if err := s.UpsertLabeledExample(earlier); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLabelBeforeBulk(7); err != nil {
		t.Fatal(err)
	}
	bulk := earlier
	bulk.Label, bulk.LabelReason = "not_dup", "user_bulk_dismiss"
	if err := s.UpsertLabeledExample(bulk); err != nil {
		t.Fatal(err)
	}
	restored, err := s.RestoreLabelAfterBulkRevert(7)
	if err != nil || !restored {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	got, _ := s.GetLabeledExample(7)
	if got == nil || got.Label != "true_dup" || got.LabelReason != "user_merge" {
		t.Fatalf("earlier label not restored: %+v", got)
	}
	// The saved copy is consumed: a second restore removes the label.
	if restored, err = s.RestoreLabelAfterBulkRevert(7); err != nil || restored {
		t.Fatalf("second restore: restored=%v err=%v", restored, err)
	}
	if got, _ := s.GetLabeledExample(7); got != nil {
		t.Fatalf("label should be gone: %+v", got)
	}
}

func TestLabelPrior_NoEarlierLabelMeansRemove(t *testing.T) {
	s := newTestEmbeddingStore(t)
	if err := s.SaveLabelBeforeBulk(9); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertLabeledExample(LabeledExample{CandidateID: 9, EntityAID: "a", EntityBID: "b", Label: "not_dup", LabelReason: "user_bulk_dismiss"}); err != nil {
		t.Fatal(err)
	}
	if restored, err := s.RestoreLabelAfterBulkRevert(9); err != nil || restored {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	if got, _ := s.GetLabeledExample(9); got != nil {
		t.Fatalf("bulk label should be removed: %+v", got)
	}
	// A prior row never leaks into label scans.
	if all, err := s.ListLabeledExamples(LabeledExampleFilter{}); err != nil || len(all) != 0 {
		t.Fatalf("scan saw %d rows (%v)", len(all), err)
	}
}
