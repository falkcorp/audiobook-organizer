// file: internal/undo/restorable_test.go
// version: 1.0.0
// guid: b83d2f5e-1a64-4c09-8e7d-5f0a9c2b6e14
// last-edited: 2026-09-12

package undo

import (
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
		{"metadata_update", "", "metadata_update:(no field)"},
		{"author_delete", "author", "author_delete"},
		{"narrator_delete", "narrator", "narrator_delete"},
		// Reversed by RunUndoOperation only, not by the revert endpoint.
		{"db_update", "title", "db_update"},
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

// Every revertable field must name a string or *string field of Book. A bad
// entry would be classified restorable but fail the reflection write at revert
// time, so the preflight would promise a row the revert then counts as Failed.
func TestRevertableBookFields_NameStringFieldsOfBook(t *testing.T) {
	bt := reflect.TypeOf(database.Book{})
	for field, name := range revertableBookFields {
		f, ok := bt.FieldByName(name)
		if !ok {
			t.Errorf("%s -> %s: no such database.Book field", field, name)
			continue
		}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if ft.Kind() != reflect.String {
			t.Errorf("%s -> %s: kind %s, want string or *string", field, name, ft.Kind())
		}
	}
}
