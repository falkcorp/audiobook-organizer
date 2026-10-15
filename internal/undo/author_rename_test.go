// file: internal/undo/author_rename_test.go
// version: 1.1.0
// guid: 6f3d9b25-8c4a-4e71-a02b-9d4f7a1c3e86
// last-edited: 2026-10-15

package undo

import (
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestAuthorRenameRow_RoundTripsAndChecks(t *testing.T) {
	row, err := NewAuthorRenameRow(7, "& Old", "Old")
	if err != nil {
		t.Fatal(err)
	}
	id, from, to, err := DecodeAuthorRename(row)
	if err != nil || id != 7 || from != "& Old" || to != "Old" {
		t.Fatalf("decode: %d %q %q %v", id, from, to, err)
	}
	if NotRestorableLabel(row) != "" {
		t.Fatalf("a well-formed rename row must be restorable: %q", NotRestorableLabel(row))
	}
	cases := []struct {
		name    string
		cur     *database.Author
		ok      bool
		already bool
	}{
		{"still renamed", &database.Author{ID: 7, Name: "Old"}, true, false},
		{"already back", &database.Author{ID: 7, Name: "& Old"}, false, true},
		{"renamed since", &database.Author{ID: 7, Name: "Other"}, false, false},
		{"merged away", &database.Author{ID: 8, Name: "Old"}, false, false},
		{"gone", nil, false, false},
	}
	for _, tc := range cases {
		err := CheckAuthorRename(tc.cur, id, from, to)
		switch {
		case tc.ok && err != nil, tc.already && !errors.Is(err, ErrAlreadyRestored), !tc.ok && !tc.already && (err == nil || errors.Is(err, ErrAlreadyRestored)):
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	bad := &database.OperationChange{ChangeType: ChangeTypeAuthorRename, OldValue: "x", NewValue: "y"}
	if NotRestorableLabel(bad) == "" {
		t.Error("an unparsable rename row must not be restorable")
	}
}

func TestAuthorIDRepointRow_RoundTripsAndChecks(t *testing.T) {
	row, err := NewAuthorIDRepointRow("b1", 99, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	old, to, err := DecodeAuthorIDRepoint(row)
	if err != nil || old.AuthorID != 99 || old.Author != nil || to != 3 {
		t.Fatalf("decode: %+v %d %v", old, to, err)
	}
	from := old.AuthorID
	// A non-nil Author object round-trips as itself, so the revert can put
	// back exactly what the repair overwrote.
	stale := &database.Author{ID: 99, Name: "Synthetic Gone Writer"}
	withAuthor, err := NewAuthorIDRepointRow("b1", 99, stale, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, _, err := DecodeAuthorIDRepoint(withAuthor); err != nil || got.Author == nil ||
		got.Author.ID != stale.ID || got.Author.Name != stale.Name {
		t.Fatalf("decode with author: %+v %v", got.Author, err)
	}
	if NotRestorableLabel(row) != "" {
		t.Fatalf("a well-formed repoint row must be restorable: %q", NotRestorableLabel(row))
	}
	three, ninetyNine, other := 3, 99, 5
	if err := CheckAuthorIDRepoint("b1", &three, from, to); err != nil {
		t.Errorf("still repointed: %v", err)
	}
	if err := CheckAuthorIDRepoint("b1", &ninetyNine, from, to); !errors.Is(err, ErrAlreadyRestored) {
		t.Errorf("already back: %v", err)
	}
	for _, cur := range []*int{&other, nil} {
		if err := CheckAuthorIDRepoint("b1", cur, from, to); err == nil || errors.Is(err, ErrAlreadyRestored) {
			t.Errorf("changed since must refuse: %v", err)
		}
	}
	for _, bad := range []*database.OperationChange{
		{ChangeType: ChangeTypeAuthorIDRepoint, OldValue: "x", NewValue: `{"author_id":3}`, BookID: "b1"},
		{ChangeType: ChangeTypeAuthorIDRepoint, OldValue: `{"author_id":99}`, NewValue: `{"author_id":0}`, BookID: "b1"},
		{ChangeType: ChangeTypeAuthorIDRepoint, OldValue: `{"author_id":99}`, NewValue: `{"author_id":3}`},
	} {
		if NotRestorableLabel(bad) == "" {
			t.Errorf("an unparsable repoint row must not be restorable: %+v", bad)
		}
	}
}
