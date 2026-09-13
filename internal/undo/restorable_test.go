// file: internal/undo/restorable_test.go
// version: 1.7.0
// guid: b83d2f5e-1a64-4c09-8e7d-5f0a9c2b6e14
// last-edited: 2026-09-12

package undo

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestNotRestorableLabel(t *testing.T) {
	cases := []struct{ changeType, field, want string }{
		{"file_move", "file_path", ""},
		{"organize_rename", "", ""},
		{"tag_write", "TITLE", ""},
		{"organize_summary", "", ""},
		{"metadata_update", "title", ""},
		{"metadata_update", "author_id", "metadata_update:author_id"},
		{"metadata_update", "series_id", ""},
		// Record-only on purpose; see the file comment in restorable.go.
		{"metadata_update", "version_group_id", "metadata_update:version_group_id"},
		{"metadata_update", "series_name", "metadata_update:series_name"},
		{"series_merge", "series_id", "series_merge"},
		{"metadata_update", "", "metadata_update:(no field)"},
		{"author_delete", "author", "author_delete"},
		{"narrator_delete", "narrator", "narrator_delete"},
		// The revert endpoint has no case for db_update.
		{"db_update", "title", "db_update"},
		// maintenance.fs-regroup-xml rows.
		{ChangeTypeBookFileReassign, "book_file:f1", ""},
		{ChangeTypeBookFileReassign, "", "book_file_reassign:(no book_file id)"},
		{ChangeTypeBookFileTrack, "book_file:f1", ""},
		{ChangeTypeBookFileTrack, "book_file:", "book_file_track:(no book_file id)"},
		{ChangeTypeBookPathUpdate, "file_path", ""},
		{ChangeTypeBookSoftDelete, "marked_for_deletion", ""},
		{ChangeTypeBookPrimaryDemote, "is_primary_version", ""},
		{ChangeTypeExternalIDReassign, "external_id:itunes/PID-1", ""},
		{ChangeTypeExternalIDReassign, "external_ids", "external_id_reassign:(no external id)"},
		// Reversing this would delete a book_file row.
		{ChangeTypeBookFileCreate, "book_file:f1", "book_file_create"},
	}
	for _, tc := range cases {
		c := &database.OperationChange{ChangeType: tc.changeType, FieldName: tc.field}
		if got := NotRestorableLabel(c); got != tc.want {
			t.Errorf("NotRestorableLabel(%s/%s) = %q, want %q", tc.changeType, tc.field, got, tc.want)
		}
		if IsRestorable(c) != (tc.want == "") {
			t.Errorf("IsRestorable(%s/%s) = %v, want %v", tc.changeType, tc.field, IsRestorable(c), tc.want == "")
		}
	}
}

// Every revertable field must name a string, *string or *int field of Book —
// the three shapes RestoreBookField writes. A bad entry would be classified
// restorable but fail at revert time (the string-only writer this replaced
// panicked on an *int: SetString on an int Value), so the preflight would
// promise a row the revert then counts as Failed.
func TestRevertableBookFields_NameRestorableFieldsOfBook(t *testing.T) {
	bt := reflect.TypeOf(database.Book{})
	for field, name := range revertableBookFields {
		f, ok := bt.FieldByName(name)
		if !ok {
			t.Errorf("%s -> %s: no such database.Book field", field, name)
			continue
		}
		ft := f.Type
		switch {
		case ft.Kind() == reflect.String:
		case ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.String:
		case ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Int:
		default:
			t.Errorf("%s -> %s: type %s, want string, *string or *int", field, name, ft)
		}
	}
}

// Every mapped field round-trips through RestoreBookField: a non-empty old
// value lands exactly, and "" restores the empty state (nil for a pointer).
func TestRestoreBookField_RoundTripsEveryMappedField(t *testing.T) {
	for field, name := range revertableBookFields {
		book := &database.Book{}
		f := reflect.ValueOf(book).Elem().FieldByName(name)
		old := "Old value"
		if f.Kind() == reflect.Pointer && f.Type().Elem().Kind() == reflect.Int {
			old = "42"
		}
		if err := RestoreBookField(book, field, old); err != nil {
			t.Errorf("%s: RestoreBookField(%q): %v", field, old, err)
			continue
		}
		got := f
		if got.Kind() == reflect.Pointer {
			if got.IsNil() {
				t.Errorf("%s: restored %q as nil", field, old)
				continue
			}
			got = got.Elem()
		}
		if s := fmt.Sprint(got.Interface()); s != old {
			t.Errorf("%s: restored %q, want %q", field, s, old)
		}

		if err := RestoreBookField(book, field, ""); err != nil {
			t.Errorf("%s: RestoreBookField(\"\"): %v", field, err)
			continue
		}
		if f.Kind() == reflect.Pointer && !f.IsNil() {
			t.Errorf("%s: \"\" restored as non-nil %v, want nil", field, f.Elem().Interface())
		}
		if f.Kind() == reflect.String && f.String() != "" {
			t.Errorf("%s: \"\" restored as %q", field, f.String())
		}
	}
}

func TestRestoreBookField_SeriesID(t *testing.T) {
	nine := 9
	book := &database.Book{SeriesID: &nine}
	if err := RestoreBookField(book, "series_id", "4"); err != nil {
		t.Fatalf("restore 4: %v", err)
	}
	if book.SeriesID == nil || *book.SeriesID != 4 {
		t.Fatalf("SeriesID = %v, want 4", book.SeriesID)
	}
	if err := RestoreBookField(book, "series_id", "12 (Foo)"); err == nil {
		t.Error("unparsable old value: want an error")
	}
	if *book.SeriesID != 4 {
		t.Errorf("failed restore changed SeriesID to %d", *book.SeriesID)
	}
	if err := RestoreBookField(book, "series_id", ""); err != nil || book.SeriesID != nil {
		t.Errorf("restore \"\": err %v, SeriesID %v, want nil", err, book.SeriesID)
	}
}

type seriesMap map[int]*database.Series

func (m seriesMap) GetSeriesByID(id int) (*database.Series, error) { return m[id], nil }
func (m seriesMap) GetSeriesByName(name string, _ *int) (*database.Series, error) {
	for _, s := range m {
		if s.Name == name {
			return s, nil
		}
	}
	return nil, nil
}

// A series_rename row's referent check is a three-way compare-and-set on the
// series' current name: the recorded new name is restorable, the recorded old
// name is already restored, anything else was renamed since.
func TestCheckRestoreReferent_SeriesRenameThreeWay(t *testing.T) {
	id := 9
	row := &database.OperationChange{ChangeType: ChangeTypeSeriesRename, SeriesID: &id, FieldName: "series_name", OldValue: "Old", NewValue: "New"}

	if err := CheckRestoreReferent(seriesMap{9: {ID: 9, Name: "New"}}, row); err != nil {
		t.Errorf("current == new: %v, want nil", err)
	}
	if err := CheckRestoreReferent(seriesMap{9: {ID: 9, Name: "Old"}}, row); !errors.Is(err, ErrAlreadyRestored) {
		t.Errorf("current == old: %v, want ErrAlreadyRestored", err)
	}
	var ref *ReferentError
	if err := CheckRestoreReferent(seriesMap{9: {ID: 9, Name: "Manual"}}, row); !errors.As(err, &ref) || ref.Reason != ReasonSeriesRenamedSince {
		t.Errorf("current == other: %v, want ReferentError %q", err, ReasonSeriesRenamedSince)
	}
	// A degenerate row whose old and new names match is restorable, not
	// "already restored", when the series holds that name.
	same := *row
	same.OldValue = "New"
	if err := CheckRestoreReferent(seriesMap{9: {ID: 9, Name: "New"}}, &same); err != nil {
		t.Errorf("old == new == current: %v, want nil", err)
	}
}

// Rows stored before OperationChange.SeriesID existed decode with it nil, and
// the book-scoped series_name rows among them stay record-only.
func TestOperationChange_LegacyRowsDecodeAndStayRecordOnly(t *testing.T) {
	legacy := `{"id":"c1","operation_id":"op","book_id":"","change_type":"metadata_update",` +
		`"field_name":"series_name","old_value":"Old","new_value":"New","created_at":"2026-08-01T00:00:00Z"}`
	var c database.OperationChange
	if err := json.Unmarshal([]byte(legacy), &c); err != nil {
		t.Fatalf("decode legacy row: %v", err)
	}
	if c.SeriesID != nil || c.OldValue != "Old" || c.NewValue != "New" {
		t.Fatalf("decoded %+v, want SeriesID nil and the names intact", c)
	}
	if got := NotRestorableLabel(&c); got != "metadata_update:series_name" {
		t.Errorf("label = %q, want metadata_update:series_name", got)
	}

	// And the new shape round-trips its series id.
	id := 10
	data, err := json.Marshal(database.OperationChange{ChangeType: ChangeTypeSeriesRename, SeriesID: &id})
	if err != nil {
		t.Fatal(err)
	}
	var back database.OperationChange
	if err := json.Unmarshal(data, &back); err != nil || back.SeriesID == nil || *back.SeriesID != 10 {
		t.Fatalf("round trip: %+v, err %v", back, err)
	}
	if !IsRestorable(&back) {
		t.Error("series_rename with a series id: want restorable")
	}
}

func TestCheckRestoreReferent(t *testing.T) {
	store := seriesMap{4: {ID: 4}}
	row := func(field, old string) *database.OperationChange {
		return &database.OperationChange{ChangeType: "metadata_update", FieldName: field, OldValue: old}
	}
	if err := CheckRestoreReferent(store, row("series_id", "4")); err != nil {
		t.Errorf("live series: %v", err)
	}
	if err := CheckRestoreReferent(store, row("series_id", "5")); err == nil {
		t.Error("deleted series: want an error")
	}
	if err := CheckRestoreReferent(store, row("series_id", "")); err != nil {
		t.Errorf("restore to no series: %v", err)
	}
	if err := CheckRestoreReferent(store, row("title", "5")); err != nil {
		t.Errorf("other field: %v", err)
	}
}

// A metadata_update row's compare-and-set is the same three-way model as a
// series_rename row's: the recorded new value is restorable, the recorded old
// value is already restored (ErrAlreadyRestored), anything else changed since.
func TestCheckBookFieldCurrent_ThreeWay(t *testing.T) {
	row := &database.OperationChange{ChangeType: "metadata_update", BookID: "b1", FieldName: "title", OldValue: "Old", NewValue: "New"}

	if err := CheckBookFieldCurrent(&database.Book{Title: "New"}, row); err != nil {
		t.Errorf("current == new: %v, want nil", err)
	}
	if err := CheckBookFieldCurrent(&database.Book{Title: "Old"}, row); !errors.Is(err, ErrAlreadyRestored) {
		t.Errorf("current == old: %v, want ErrAlreadyRestored", err)
	}
	if err := CheckBookFieldCurrent(&database.Book{Title: "Manual"}, row); RefusalReason(err) != ReasonChangedSince {
		t.Errorf("current == other: %v, want ReferentError %q", err, ReasonChangedSince)
	}
	// A row whose old and new values match wrote nothing: it is restorable,
	// never "already restored", as for series_rename.
	same := *row
	same.OldValue = "New"
	if err := CheckBookFieldCurrent(&database.Book{Title: "New"}, &same); err != nil {
		t.Errorf("old == new == current: %v, want nil", err)
	}
	if err := CheckBookFieldCurrent(&database.Book{Title: "Manual"}, &same); RefusalReason(err) != ReasonChangedSince {
		t.Errorf("old == new != current: %v, want ReferentError %q", err, ReasonChangedSince)
	}
	bad := *row
	bad.FieldName = "not_a_field"
	if err := CheckBookFieldCurrent(&database.Book{}, &bad); RefusalReason(err) != ReasonFieldUnreadable {
		t.Errorf("unknown field: %v, want ReferentError %q", err, ReasonFieldUnreadable)
	}
}
