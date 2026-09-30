// file: internal/batch/edit_history_test.go
// version: 1.0.0
// guid: 8d3f1a62-4b7e-4c09-9e25-71c0b6a4d3f8
// last-edited: 2026-09-30

package batch

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func historyFields(rows []database.MetadataChangeRecord) map[string]database.MetadataChangeRecord {
	out := map[string]database.MetadataChangeRecord{}
	for _, r := range rows {
		out[r.Field] = r
	}
	return out
}

// The web bulk edit records a "manual" history row for every field it
// changed -- including one no lock covers (version_notes) -- so the book's
// history shows it and a queued apply sees it as a later edit.
func TestUpdateAudiobooks_RecordsHistoryForEveryChangedField(t *testing.T) {
	store := NewMockBookStore()
	store.books["b1"] = &database.Book{ID: "b1", Title: "Old", Narrator: new("Reader"), VersionNotes: new("v1")}
	bs := NewBatchService(store)

	resp := bs.UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{"b1"}, Updates: map[string]any{
		"title": "New", "version_notes": "v2",
	}})
	if resp.Success != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	got := historyFields(store.history)
	for _, f := range []string{"title", "version_notes"} {
		r, ok := got[f]
		if !ok {
			t.Fatalf("no history row for %s: %+v", f, store.history)
		}
		if r.ChangeType != database.ChangeTypeManual || r.BookID != "b1" {
			t.Errorf("%s row = %+v", f, r)
		}
	}
	if _, ok := got["narrator"]; ok {
		t.Errorf("recorded an unchanged field: %+v", got["narrator"])
	}
	if *got["title"].PreviousValue != `"Old"` || *got["title"].NewValue != `"New"` {
		t.Errorf("title row values = %s -> %s", *got["title"].PreviousValue, *got["title"].NewValue)
	}
}

// A bulk CLEAR (narrator set to empty) records a row, although it records no
// lock: the old value is gone and the user did it.
func TestUpdateAudiobooks_ClearRecordsHistory(t *testing.T) {
	store := NewMockBookStore()
	store.books["b1"] = &database.Book{ID: "b1", Title: "T", Narrator: new("Reader")}
	bs := NewBatchService(store)

	resp := bs.UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{"b1"}, Updates: map[string]any{"narrator": ""}})
	if resp.Success != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	r, ok := historyFields(store.history)["narrator"]
	if !ok {
		t.Fatalf("a cleared narrator recorded no history: %+v", store.history)
	}
	if *r.PreviousValue != `"Reader"` || *r.NewValue != `""` {
		t.Errorf("narrator row = %s -> %s", *r.PreviousValue, *r.NewValue)
	}
}

// Setting a field to the value it already has records nothing.
func TestUpdateAudiobooks_NoActualChangeRecordsNoHistory(t *testing.T) {
	store := NewMockBookStore()
	store.books["b1"] = &database.Book{ID: "b1", Title: "Same", Narrator: new("Reader")}
	bs := NewBatchService(store)

	resp := bs.UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{"b1"}, Updates: map[string]any{
		"title": "Same", "narrator": "Reader",
	}})
	if resp.Success != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if len(store.history) != 0 {
		t.Fatalf("recorded %d history row(s) for a no-op edit: %+v", len(store.history), store.history)
	}
}

// A history write that fails is reported on the book, not swallowed: a
// missing row would let a queued apply overwrite the edit.
func TestUpdateAudiobooks_HistoryFailureIsReported(t *testing.T) {
	store := NewMockBookStore()
	store.books["b1"] = &database.Book{ID: "b1", Title: "Old"}
	store.histErr = errors.New("history store down")
	bs := NewBatchService(store)

	resp := bs.UpdateAudiobooks(&BatchUpdateRequest{IDs: []string{"b1"}, Updates: map[string]any{"title": "New"}})
	if resp.Failed != 1 || len(resp.Results) != 1 || resp.Results[0].Error == "" {
		t.Fatalf("resp = %+v", resp)
	}
}
