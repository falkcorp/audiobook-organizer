// file: internal/database/change_type_class_test.go
// version: 1.0.0
// guid: c3a96f18-47d2-4e5b-9a60-1f8e2b7d54c9
// last-edited: 2026-10-30

package database

import (
	"errors"
	"testing"
)

type recordingJournal struct {
	rows    []*OperationChange
	batches int
}

func (r *recordingJournal) CreateOperationChange(c *OperationChange) error {
	r.rows = append(r.rows, c)
	return nil
}

func (r *recordingJournal) CreateOperationChanges(cs []*OperationChange) error {
	r.batches++
	r.rows = append(r.rows, cs...)
	return nil
}

// TestRecordOperationChange_RefusesAnUnclassifiedType: a type nobody classified
// is refused with ErrUnclassifiedChangeType and nothing is written; a
// classified one is written unchanged.
func TestRecordOperationChange_RefusesAnUnclassifiedType(t *testing.T) {
	j := &recordingJournal{}
	err := RecordOperationChange(j, &OperationChange{ID: "c1", ChangeType: "batman_gotham_unclassified"})
	if !errors.Is(err, ErrUnclassifiedChangeType) {
		t.Fatalf("err = %v, want ErrUnclassifiedChangeType", err)
	}
	if len(j.rows) != 0 {
		t.Fatalf("an unclassified type was written: %+v", j.rows)
	}
	row := &OperationChange{ID: "c2", ChangeType: "metadata_update", FieldName: "title", OldValue: "a", NewValue: "b"}
	if err := RecordOperationChange(j, row); err != nil {
		t.Fatal(err)
	}
	if len(j.rows) != 1 || j.rows[0] != row {
		t.Fatalf("the classified row was not stored as given: %+v", j.rows)
	}
}

// TestRecordOperationChanges_ChecksEveryRowBeforeWritingAny: one unclassified
// row in a batch refuses the whole batch.
func TestRecordOperationChanges_ChecksEveryRowBeforeWritingAny(t *testing.T) {
	j := &recordingJournal{}
	err := RecordOperationChanges(j, []*OperationChange{
		{ID: "a", ChangeType: "metadata_update"},
		{ID: "b", ChangeType: "batman_gotham_unclassified"},
	})
	if !errors.Is(err, ErrUnclassifiedChangeType) {
		t.Fatalf("err = %v, want ErrUnclassifiedChangeType", err)
	}
	if j.batches != 0 || len(j.rows) != 0 {
		t.Fatalf("part of a refused batch was written: %+v", j.rows)
	}
	if err := RecordOperationChanges(j, []*OperationChange{{ID: "a", ChangeType: "metadata_update"}}); err != nil || j.batches != 1 {
		t.Fatalf("classified batch: err=%v batches=%d", err, j.batches)
	}
}

// TestDirectWriterChangeTypesAreClassifiedAsLibraryData pins the literal types
// the journal's direct writers record (the organize ledger, the narrator and
// author maintenance ops, the book_file repoint/delete ops, the series prune
// ledger): each is known, and none is one user's data. Changing an answer here
// changes who may read those rows on the journal routes.
func TestDirectWriterChangeTypesAreClassifiedAsLibraryData(t *testing.T) {
	for _, ct := range []string{
		"metadata_update", "series_merge", "series_delete", "series_prune_summary",
		"author_link", "author_delete", "narrator_split", "narrator_orphan_unlink", "narrator_delete",
		"book_file_repoint", "book_file_delete", "file_path_update", "book_create",
		"organize", "organize_summary", "organize_skipped", "organize_failed", "organize_rename",
		ChangeTypeBookFileRepoint,
	} {
		userData, known := ClassifyChangeType(ct)
		if !known || userData {
			t.Errorf("%s: known=%v userData=%v, want known library data", ct, known, userData)
		}
	}
	if _, known := ClassifyChangeType(""); known {
		t.Error("the empty change type must not be classified")
	}
}
