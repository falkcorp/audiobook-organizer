// file: internal/database/dedup_label_prior_test.go
// version: 1.1.0
// guid: 2c758413-c221-48ba-89b6-96100620b328
// last-edited: 2026-10-06

package database

import (
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

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
	// The record is consumed: a second restore finds nothing to undo and
	// leaves the restored verdict alone.
	if _, err = s.RestoreLabelAfterBulkRevert(7); !errors.Is(err, ErrNoBulkDismissRecord) {
		t.Fatalf("second restore: err=%v want ErrNoBulkDismissRecord", err)
	}
	if got, _ := s.GetLabeledExample(7); got == nil || got.Label != "true_dup" {
		t.Fatalf("restored label touched by a second restore: %+v", got)
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

// S2: the record is written for every dismissed row, even one with no label
// and no capture, so the revert is not keyed off the (best-effort) label.
func TestLabelPrior_RecordWrittenWithoutAnyLabel(t *testing.T) {
	s := newTestEmbeddingStore(t)
	if _, err := s.GetBulkDismissPrior(3); !errors.Is(err, ErrNoBulkDismissRecord) {
		t.Fatalf("before save: err=%v want ErrNoBulkDismissRecord", err)
	}
	if err := s.SaveLabelBeforeBulk(3); err != nil {
		t.Fatal(err)
	}
	rec, err := s.GetBulkDismissPrior(3)
	if err != nil || rec == nil || rec.Prior != nil {
		t.Fatalf("record=%+v err=%v, want a no-earlier-label record", rec, err)
	}
	// The capture failed: there is still no label. The revert still runs.
	if restored, err := s.RestoreLabelAfterBulkRevert(3); err != nil || restored {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	if _, err := s.GetBulkDismissPrior(3); !errors.Is(err, ErrNoBulkDismissRecord) {
		t.Fatalf("record survived the restore: %v", err)
	}
}

// N1: a bulk-dismiss label is never saved as the earlier verdict.
func TestLabelPrior_RefusesABulkLabelAsThePrior(t *testing.T) {
	s := newTestEmbeddingStore(t)
	bulk := LabeledExample{CandidateID: 4, EntityAID: "a", EntityBID: "b", Label: "not_dup", LabelSource: "human", LabelReason: LabelReasonUserBulkDismiss}
	if err := s.UpsertLabeledExample(bulk); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLabelBeforeBulk(4); !errors.Is(err, ErrPriorIsBulkDismiss) {
		t.Fatalf("err=%v want ErrPriorIsBulkDismiss", err)
	}
	if _, found, _ := s.readBulkPriorRaw(4); found {
		t.Fatal("a refused save wrote a record")
	}
}

// N1: with an unfinished earlier record, a second save keeps it instead of
// overwriting the real earlier verdict with the bulk label.
func TestLabelPrior_KeepsTheEarlierRecordOverABulkLabel(t *testing.T) {
	s := newTestEmbeddingStore(t)
	earlier := LabeledExample{CandidateID: 5, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "human", LabelReason: "user_merge"}
	if err := s.UpsertLabeledExample(earlier); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLabelBeforeBulk(5); err != nil {
		t.Fatal(err)
	}
	bulk := earlier
	bulk.Label, bulk.LabelReason = "not_dup", LabelReasonUserBulkDismiss
	if err := s.UpsertLabeledExample(bulk); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLabelBeforeBulk(5); err != nil {
		t.Fatalf("second save: %v", err)
	}
	if _, err := s.RestoreLabelAfterBulkRevert(5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetLabeledExample(5); got == nil || got.LabelReason != "user_merge" {
		t.Fatalf("earlier verdict lost: %+v", got)
	}
}

// A later verdict is not the bulk dismiss's to undo: refused, nothing touched,
// record kept.
func TestLabelPrior_RestoreRefusesALaterVerdict(t *testing.T) {
	s := newTestEmbeddingStore(t)
	if err := s.SaveLabelBeforeBulk(6); err != nil {
		t.Fatal(err)
	}
	later := LabeledExample{CandidateID: 6, EntityAID: "a", EntityBID: "b", Label: "true_dup", LabelSource: "human", LabelReason: "user_merge"}
	if err := s.UpsertLabeledExample(later); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreLabelAfterBulkRevert(6); !errors.Is(err, ErrLabelChangedSinceBulk) {
		t.Fatalf("err=%v want ErrLabelChangedSinceBulk", err)
	}
	if got, _ := s.GetLabeledExample(6); got == nil || got.LabelReason != "user_merge" {
		t.Fatalf("later verdict touched: %+v", got)
	}
	if _, found, _ := s.readBulkPriorRaw(6); !found {
		t.Fatal("record dropped by a refused restore")
	}
}

// N2: a restore that fails keeps the record, so a retry can finish it; and a
// restore is idempotent once the earlier label is back.
func TestLabelPrior_FailedRestoreKeepsTheRecordForARetry(t *testing.T) {
	s := newTestEmbeddingStore(t)
	if err := s.db.Set(dedupLabelPriorKey(8), []byte("{not json"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RestoreLabelAfterBulkRevert(8); err == nil {
		t.Fatal("restore of an unreadable record succeeded")
	}
	if _, found, _ := s.readBulkPriorRaw(8); found {
		t.Fatal("unreadable record decoded")
	}
	if _, _, err := s.db.Get(dedupLabelPriorKey(8)); err != nil {
		t.Fatalf("record deleted by a failed restore: %v", err)
	}

	earlier := LabeledExample{CandidateID: 10, EntityAID: "a", EntityBID: "b", Label: "unsure", LabelSource: "human", LabelReason: "reviewer_note"}
	if err := s.UpsertLabeledExample(earlier); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLabelBeforeBulk(10); err != nil {
		t.Fatal(err)
	}
	// The capture failed, so the current label is still the earlier one.
	if restored, err := s.RestoreLabelAfterBulkRevert(10); err != nil || !restored {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
}

// Rows #3783 dismissed: a bare-LabeledExample record still decodes, and a
// bulk label with no record reads as a no-earlier-label record.
func TestLabelPrior_LegacyRowsStayUndoable(t *testing.T) {
	s := newTestEmbeddingStore(t)
	old := []byte(`{"candidate_id":11,"entity_a_id":"a","entity_b_id":"b","label":"true_dup","label_source":"human","label_reason":"user_merge"}`)
	if err := s.db.Set(dedupLabelPriorKey(11), old, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	rec, err := s.GetBulkDismissPrior(11)
	if err != nil || rec.Prior == nil || rec.Prior.LabelReason != "user_merge" {
		t.Fatalf("legacy record=%+v err=%v", rec, err)
	}
	if err := s.UpsertLabeledExample(LabeledExample{CandidateID: 12, EntityAID: "a", EntityBID: "b", Label: "not_dup", LabelReason: LabelReasonUserBulkDismiss}); err != nil {
		t.Fatal(err)
	}
	if restored, err := s.RestoreLabelAfterBulkRevert(12); err != nil || restored {
		t.Fatalf("legacy no-record restore: restored=%v err=%v", restored, err)
	}
	if got, _ := s.GetLabeledExample(12); got != nil {
		t.Fatalf("legacy bulk label not removed: %+v", got)
	}
}
