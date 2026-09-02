// file: internal/database/metadata_field_locks_test.go
// version: 1.0.0
// guid: 041867f9-1429-4696-8eac-a3faef717c4d
// last-edited: 2026-09-02

package database

import (
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/metastate"
)

// Every Column in the vocabulary must be a real Book field, so the mapping
// cannot silently name a column that was renamed away.
func TestUserLockableFieldsNameRealBookColumns(t *testing.T) {
	bookType := reflect.TypeOf(Book{})
	seen := map[string]bool{}
	for _, f := range UserLockableFields {
		if f.Key == "" || f.Column == "" {
			t.Errorf("vocabulary entry %+v has an empty key or column", f)
		}
		if seen[f.Key] {
			t.Errorf("lock key %q listed twice", f.Key)
		}
		seen[f.Key] = true
		if _, ok := bookType.FieldByName(f.Column); !ok {
			t.Errorf("lock key %q maps to Book.%s, which does not exist", f.Key, f.Column)
		}
	}
	if got, want := len(UserLockableFieldKeys()), len(UserLockableFields); got != want {
		t.Errorf("UserLockableFieldKeys() has %d keys, vocabulary has %d", got, want)
	}
	for k := range AllUserLockableFieldsLocked() {
		if !seen[k] {
			t.Errorf("AllUserLockableFieldsLocked reports %q, not in the vocabulary", k)
		}
	}
	if len(AllUserLockableFieldsLocked()) != len(UserLockableFields) {
		t.Errorf("AllUserLockableFieldsLocked locks %d keys, vocabulary has %d",
			len(AllUserLockableFieldsLocked()), len(UserLockableFields))
	}
}

type lockReader struct {
	states    []MetadataFieldState
	statesErr error
	pref      *UserPreference
	prefErr   error
	prefKey   string
}

func (r *lockReader) GetMetadataFieldStates(string) ([]MetadataFieldState, error) {
	return r.states, r.statesErr
}

func (r *lockReader) GetUserPreference(key string) (*UserPreference, error) {
	r.prefKey = key
	return r.pref, r.prefErr
}

func TestLockedUserFields_RowsDecideWhenPresent(t *testing.T) {
	r := &lockReader{states: []MetadataFieldState{
		{Field: FieldKeyAuthorName, OverrideLocked: true},
		{Field: FieldKeyTitle, OverrideValue: new(`"Curated"`)},
		{Field: FieldKeyNarrator, FetchedValue: new(`"From Provider"`)},
		{Field: FieldKeySeriesName},
	}}
	locked, err := LockedUserFields(r, "b1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !locked[FieldKeyAuthorName] {
		t.Error("OverrideLocked row not reported locked")
	}
	if !locked[FieldKeyTitle] {
		t.Error("OverrideValue-only row not reported locked: an explicit value is the user's word")
	}
	if locked[FieldKeyNarrator] {
		t.Error("fetched-only row reported locked: a provider value is not a user act")
	}
	if locked[FieldKeySeriesName] {
		t.Error("empty row reported locked")
	}
	if r.prefKey != "" {
		t.Error("legacy blob consulted even though rows exist")
	}
}

func TestLockedUserFields_FallsBackToLegacyBlobWhenNoRows(t *testing.T) {
	blob := `{"series_position":{"override_value":3,"override_locked":true},` +
		`"title":{"override_value":"Curated"},` +
		`"narrator":{"fetched_value":"Prov"},` +
		`"language":{"override_value":null}}`
	r := &lockReader{pref: &UserPreference{Value: &blob}}
	locked, err := LockedUserFields(r, "b1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.prefKey != metastate.Key("b1") {
		t.Errorf("legacy blob read under %q, want %q", r.prefKey, metastate.Key("b1"))
	}
	if !locked[FieldKeySeriesPosition] || !locked[FieldKeyTitle] {
		t.Errorf("legacy locks missed: %v", locked)
	}
	if locked[FieldKeyNarrator] {
		t.Error("legacy fetched-only field reported locked")
	}
	if locked[FieldKeyLanguage] {
		t.Error("legacy null override reported locked")
	}
}

func TestLockedUserFields_NoStateMeansNothingLocked(t *testing.T) {
	locked, err := LockedUserFields(&lockReader{}, "b1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(locked) != 0 {
		t.Errorf("want no locks, got %v", locked)
	}
}

func TestLockedUserFields_FailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		reader MetadataFieldStateReader
		bookID string
	}{
		{"nil reader", nil, "b1"},
		{"empty id", &lockReader{}, ""},
		{"rows error", &lockReader{statesErr: errors.New("pebble: iterator closed")}, "b1"},
		{"legacy error", &lockReader{prefErr: errors.New("pebble: closed")}, "b1"},
		{"legacy garbage", &lockReader{pref: &UserPreference{Value: new("{not json")}}, "b1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locked, err := LockedUserFields(tc.reader, tc.bookID)
			if err == nil {
				t.Fatalf("want an error, got locks %v", locked)
			}
			if !errors.Is(err, ErrFieldLocksUnavailable) {
				t.Errorf("error %v does not wrap ErrFieldLocksUnavailable", err)
			}
			if locked != nil {
				t.Errorf("a failed read must not hand back a usable map (got %v): a caller "+
					"that ignores the error would treat it as 'nothing locked'", locked)
			}
		})
	}
}
