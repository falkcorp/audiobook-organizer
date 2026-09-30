// file: internal/database/book_edit_history_test.go
// version: 1.0.0
// guid: 4c7e1b93-8a2d-4f06-b5e9-0d3a6f8c2e17
// last-edited: 2026-09-30

package database

import (
	"testing"
	"time"
)

type recStore struct{ rows []MetadataChangeRecord }

func (r *recStore) RecordMetadataChange(m *MetadataChangeRecord) error {
	r.rows = append(r.rows, *m)
	return nil
}
func (r *recStore) GetAuthorByID(int) (*Author, error) { return nil, nil }
func (r *recStore) GetSeriesByID(int) (*Series, error) { return nil, nil }

// nil and a pointer to "" store differently but render the same: no row.
// A real change beside it still records.
func TestRecordBookEditHistory_NilVersusEmptyIsNotAChange(t *testing.T) {
	empty := ""
	before := &Book{ID: "b1", Title: "Old", Narrator: nil}
	after := &Book{ID: "b1", Title: "New", Narrator: &empty}
	s := &recStore{}
	n, err := RecordBookEditHistory(s, before, after, ChangeTypeManual, "manual", time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(s.rows) != 1 || s.rows[0].Field != "title" {
		t.Fatalf("want exactly the title row, got %d: %+v", n, s.rows)
	}
}

// A clear (value -> nothing) is still a row.
func TestRecordBookEditHistory_ClearIsAChange(t *testing.T) {
	v := "Reader"
	s := &recStore{}
	if _, err := RecordBookEditHistory(s, &Book{ID: "b1", Narrator: &v}, &Book{ID: "b1"},
		ChangeTypeManual, "manual", time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if len(s.rows) != 1 || s.rows[0].Field != "narrator" || *s.rows[0].NewValue != `""` {
		t.Fatalf("want one narrator clear row, got %+v", s.rows)
	}
}
