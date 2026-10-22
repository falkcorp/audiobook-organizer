// file: internal/undo/user_content_follow_test.go
// version: 1.2.0
// guid: 6a1d9e43-2c7b-4f80-b5a3-d9e4f1c70a28
// last-edited: 2026-10-21

package undo

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func ucF(v float64) *float64 { return &v }
func ucS(v string) *string   { return &v }

func ucEq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCarryMembership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		cur     []string
		want    []string
		wantRec MembershipRecord
	}{
		{"replaced in place", []string{"a", "r", "b"}, []string{"a", "s", "b"}, MembershipRecord{Index: 1, Prev: "a", Next: "b"}},
		{"survivor already listed before", []string{"s", "r", "b"}, []string{"s", "b"}, MembershipRecord{Index: 1, SurvivorWasMember: true, Prev: "s", Next: "b"}},
		{"survivor already listed after", []string{"r", "b", "s"}, []string{"b", "s"}, MembershipRecord{Index: 0, SurvivorWasMember: true, Next: "b"}},
		{"first and only", []string{"r"}, []string{"s"}, MembershipRecord{Index: 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			orig := append([]string(nil), tc.cur...)
			next, rec, ok := CarryMembership(tc.cur, "r", "s")
			if !ok {
				t.Fatal("not carried")
			}
			ucEq(t, next, tc.want)
			tc.wantRec.Anchored = true
			if rec != tc.wantRec {
				t.Fatalf("record %+v, want %+v", rec, tc.wantRec)
			}
			ucEq(t, tc.cur, orig) // the input is never written
			back, _, err := RestoreMembership(next, "r", "s", rec, SurvivorDefault, nil)
			if err != nil {
				t.Fatal(err)
			}
			ucEq(t, back, orig)
		})
	}
	t.Run("not listed: nothing to carry", func(t *testing.T) {
		t.Parallel()
		cur := []string{"a"}
		next, _, ok := CarryMembership(cur, "r", "s")
		if ok {
			t.Fatal("carried a book that is not listed")
		}
		ucEq(t, next, cur)
	})
}

func TestRestoreMembership(t *testing.T) {
	t.Parallel()

	t.Run("the retired book is listed again: already restored", func(t *testing.T) {
		t.Parallel()
		_, _, err := RestoreMembership([]string{"r", "s"}, "r", "s", MembershipRecord{}, SurvivorDefault, nil)
		if !errors.Is(err, ErrAlreadyRestored) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("the survivor was taken out of the slot: refused, never guessed", func(t *testing.T) {
		t.Parallel()
		_, _, err := RestoreMembership([]string{"a", "b"}, "r", "s", MembershipRecord{Index: 1}, SurvivorDefault, nil)
		if RefusalReason(err) != ReasonChangedSince {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("survivor was already a member: the book goes back at its index", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMembership([]string{"s", "b"}, "r", "s", MembershipRecord{Index: 1, SurvivorWasMember: true}, SurvivorDefault, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"s", "r", "b"})
	})

	t.Run("the index is clamped to a list that shrank", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMembership([]string{"s"}, "r", "s", MembershipRecord{Index: 9, SurvivorWasMember: true}, SurvivorDefault, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"s", "r"})
	})

	t.Run("an unrelated edit of the list does not block the restore", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMembership([]string{"x", "s", "y", "z"}, "r", "s", MembershipRecord{Index: 1}, SurvivorDefault, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"x", "r", "y", "z"})
	})

	t.Run("SurvivorStays: another retire stands in the slot, so the survivor stays", func(t *testing.T) {
		t.Parallel()
		// Op1 replaced r by s at index 0; op2 removed b because s was listed.
		got, _, err := RestoreMembership([]string{"s"}, "r", "s", MembershipRecord{Index: 0}, SurvivorStays, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"r", "s"})
	})

	t.Run("SurvivorLeaves: a removal puts the book back and takes the survivor out", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMembership([]string{"x", "s", "y"}, "r", "s", MembershipRecord{Index: 1, SurvivorWasMember: true}, SurvivorLeaves, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"x", "r", "y"})
	})

	t.Run("SurvivorLeaves with the survivor already gone just puts the book back", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMembership([]string{"y"}, "r", "s", MembershipRecord{Index: 0, SurvivorWasMember: true}, SurvivorLeaves, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"r", "y"})
	})

	t.Run("SurvivorStays with the survivor gone is still a refusal", func(t *testing.T) {
		t.Parallel()
		_, _, err := RestoreMembership([]string{"a"}, "r", "s", MembershipRecord{Index: 0}, SurvivorStays, nil)
		if RefusalReason(err) != ReasonChangedSince {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRestoreRating_And_CheckRatingFollowCurrent(t *testing.T) {
	t.Parallel()
	row := func(old, new RatingSnapshot) *database.OperationChange {
		o, _ := EncodeRating(old)
		n, _ := EncodeRating(new)
		return &database.OperationChange{BookID: "r", OldValue: o, NewValue: n}
	}
	own := RatingSnapshot{Story: ucF(3)}
	filled := RatingSnapshot{Story: ucF(3), Overall: ucF(4.5), Notes: ucS("loved it")}

	t.Run("the survivor still holds what the retire wrote: the filled fields go back", func(t *testing.T) {
		t.Parallel()
		got, err := RestoreRating(filled, row(own, filled))
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(own) {
			t.Fatalf("got %+v, want %+v", got, own)
		}
	})

	t.Run("already back to the old value: already restored", func(t *testing.T) {
		t.Parallel()
		if _, err := RestoreRating(own, row(own, filled)); !errors.Is(err, ErrAlreadyRestored) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a field the retire filled was rated since: the whole row is refused", func(t *testing.T) {
		t.Parallel()
		edited := RatingSnapshot{Story: ucF(3), Overall: ucF(1), Notes: ucS("loved it")}
		_, err := RestoreRating(edited, row(own, filled))
		if RefusalReason(err) != ReasonChangedSince {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a field the row did not touch may differ: it is kept, not blocking", func(t *testing.T) {
		t.Parallel()
		// Another operation filled performance after this one filled overall.
		later := RatingSnapshot{Story: ucF(3), Overall: ucF(4.5), Notes: ucS("loved it"), Performance: ucF(5)}
		got, err := RestoreRating(later, row(own, filled))
		if err != nil {
			t.Fatal(err)
		}
		want := RatingSnapshot{Story: ucF(3), Performance: ucF(5)}
		if !got.Equal(want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("a field the row did not touch may even be edited by the owner", func(t *testing.T) {
		t.Parallel()
		edited := RatingSnapshot{Story: ucF(1), Overall: ucF(4.5), Notes: ucS("loved it")}
		got, err := RestoreRating(edited, row(own, filled))
		if err != nil {
			t.Fatal(err)
		}
		if got.Story == nil || *got.Story != 1 || got.Overall != nil || got.Notes != nil {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("unparsable values are a referent refusal", func(t *testing.T) {
		t.Parallel()
		_, err := RestoreRating(own, &database.OperationChange{OldValue: "{", NewValue: "{}"})
		if RefusalReason(err) != ReasonOldValueUnparsable {
			t.Fatalf("err = %v", err)
		}
		_, err = RestoreRating(own, &database.OperationChange{OldValue: "{}", NewValue: "{"})
		if RefusalReason(err) != ReasonOldValueUnparsable {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("CheckRatingFollowCurrent is the same verdict from a book", func(t *testing.T) {
		t.Parallel()
		book := &database.Book{ID: "s", UserRatingStory: ucF(3), UserRatingOverall: ucF(4.5), UserRatingNotes: ucS("loved it")}
		c := row(own, filled)
		if err := CheckRatingFollowCurrent(book, c); err != nil {
			t.Fatalf("err = %v", err)
		}
		book.UserRatingOverall = ucF(2)
		if RefusalReason(CheckRatingFollowCurrent(book, c)) != ReasonChangedSince {
			t.Fatal("an edited field must refuse")
		}
		book.UserRatingOverall, book.UserRatingNotes = nil, nil
		if err := CheckRatingFollowCurrent(book, c); !errors.Is(err, ErrAlreadyRestored) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCheckMembershipFollow(t *testing.T) {
	t.Parallel()
	rowOf := func(rec MembershipRecord) *database.OperationChange {
		v, _ := EncodeMembership(rec)
		return &database.OperationChange{BookID: "r", NewValue: "s", OldValue: v}
	}
	if err := CheckMembershipFollow([]string{"a", "s"}, rowOf(MembershipRecord{Index: 1})); err != nil {
		t.Fatalf("restorable: %v", err)
	}
	if err := CheckMembershipFollow([]string{"a", "r"}, rowOf(MembershipRecord{Index: 1})); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("already: %v", err)
	}
	if RefusalReason(CheckMembershipFollow([]string{"a"}, rowOf(MembershipRecord{Index: 1}))) != ReasonChangedSince {
		t.Fatal("a slot the owner emptied must refuse")
	}
	if RefusalReason(CheckMembershipFollow(nil, &database.OperationChange{BookID: "r", NewValue: "s", OldValue: "x"})) != ReasonOldValueUnparsable {
		t.Fatal("a malformed record must refuse")
	}
}

func TestSurvivorStandsIn(t *testing.T) {
	t.Parallel()
	now := time.Now()
	marker := func(op, field string) *database.OperationChange {
		return &database.OperationChange{ChangeType: ChangeTypeUserContentFollow, OperationID: op, FieldName: field, NewValue: "b"}
	}
	field := UserContentField(UserContentCollectionHeld, "c1")
	cases := []struct {
		name string
		rows []*database.OperationChange
		want bool
	}{
		{"another op's standing marker", []*database.OperationChange{marker("op2", field)}, true},
		{"this op's own marker does not count", []*database.OperationChange{marker("op1", field)}, false},
		{"a reverted marker does not count", []*database.OperationChange{{ChangeType: ChangeTypeUserContentFollow, OperationID: "op2", FieldName: field, RevertedAt: &now}}, false},
		{"a voided marker does not count", []*database.OperationChange{{ChangeType: ChangeTypeUserContentFollow, OperationID: "op2", FieldName: field, Voided: true}}, false},
		{"another list's marker does not count", []*database.OperationChange{marker("op2", UserContentField(UserContentCollectionHeld, "c9"))}, false},
		{"a playlist marker does not count for a collection", []*database.OperationChange{marker("op2", UserContentField(UserContentPlaylistHeld, "c1"))}, false},
		{"another change type does not count", []*database.OperationChange{{ChangeType: "metadata_update", OperationID: "op2", FieldName: field}}, false},
		{"no rows", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SurvivorStandsIn(tc.rows, "op1", UserContentCollection, "c1"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSurvivorOnlyStandsByReplace(t *testing.T) {
	t.Parallel()
	base := time.Now()
	at := func(d time.Duration) time.Time { return base.Add(d) }
	tp := func(t time.Time) *time.Time { return &t }
	took := UserContentField(UserContentCollectionTook, "c1")
	held := UserContentField(UserContentCollectionHeld, "c1")
	// row is op2's removal, written at +10s.
	row := &database.OperationChange{ChangeType: ChangeTypeUserContentFollow, OperationID: "op2", CreatedAt: at(10 * time.Second)}
	mk := func(op, field string, created time.Time, reverted *time.Time) *database.OperationChange {
		return &database.OperationChange{ChangeType: ChangeTypeUserContentFollow, OperationID: op, FieldName: field, CreatedAt: created, RevertedAt: reverted}
	}
	cases := []struct {
		name string
		rows []*database.OperationChange
		want bool
	}{
		{"the replace was reverted after the removal: the survivor leaves", []*database.OperationChange{mk("op1", took, at(0), tp(at(20*time.Second)))}, true},
		{"the replace is still standing", []*database.OperationChange{mk("op1", took, at(0), nil)}, false},
		{"the replace was reverted BEFORE the removal was written (the owner added the survivor later)", []*database.OperationChange{mk("op1", took, at(0), tp(at(5*time.Second)))}, false},
		{"another removal still stands", []*database.OperationChange{mk("op1", took, at(0), tp(at(20*time.Second))), mk("op3", held, at(12*time.Second), nil)}, false},
		{"another removal was reverted too", []*database.OperationChange{mk("op1", took, at(0), tp(at(20*time.Second))), mk("op3", held, at(12*time.Second), tp(at(30*time.Second)))}, true},
		{"this op's own replace marker is not another op's", []*database.OperationChange{mk("op2", took, at(0), tp(at(20*time.Second)))}, false},
		{"a voided replace marker does not count", []*database.OperationChange{{ChangeType: ChangeTypeUserContentFollow, OperationID: "op1", FieldName: took, CreatedAt: at(0), RevertedAt: tp(at(20 * time.Second)), Voided: true}}, false},
		{"another list's marker does not count", []*database.OperationChange{mk("op1", UserContentField(UserContentCollectionTook, "c9"), at(0), tp(at(20*time.Second)))}, false},
		{"no rows", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SurvivorOnlyStandsByReplace(tc.rows, row, UserContentCollection, "c1"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsMarker(t *testing.T) {
	t.Parallel()
	for _, k := range []string{UserContentCollectionHeld, UserContentPlaylistHeld, UserContentCollectionTook, UserContentPlaylistTook} {
		if !IsMarker(k) {
			t.Fatalf("%s is a marker", k)
		}
	}
	for _, k := range []string{UserContentRating, UserContentCollection, UserContentPlaylist, "", "x"} {
		if IsMarker(k) {
			t.Fatalf("%s is not a marker", k)
		}
	}
}

func TestUserContentFollow_FieldsAndValidity(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{UserContentRating, UserContentCollection, UserContentPlaylist, UserContentCollectionHeld, UserContentPlaylistHeld, UserContentCollectionTook, UserContentPlaylistTook} {
		k, id, ok := UserContentFromField(UserContentField(kind, "x1"))
		if !ok || k != kind || id != "x1" {
			t.Fatalf("%s: %q %q %v", kind, k, id, ok)
		}
	}
	for _, bad := range []string{"", "rating", "rating:", "unknown:x", ":x"} {
		if _, _, ok := UserContentFromField(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
	rec, _ := EncodeMembership(MembershipRecord{Index: 2})
	good := []*database.OperationChange{
		{FieldName: "rating:s", OldValue: "{}", NewValue: `{"overall":4}`},
		{FieldName: "collection:c1", OldValue: rec, NewValue: "s"},
		{FieldName: "playlist:p1", OldValue: rec, NewValue: "s"},
		{FieldName: "collection-held:c1", OldValue: rec, NewValue: "r"},
		{FieldName: "playlist-held:p1", OldValue: rec, NewValue: "r"},
	}
	for _, c := range good {
		c.ChangeType = ChangeTypeUserContentFollow
		if label := NotRestorableLabel(c); label != "" {
			t.Fatalf("%s should be restorable, got %q", c.FieldName, label)
		}
	}
	for _, c := range []*database.OperationChange{
		{FieldName: "rating:s", OldValue: "{", NewValue: "{}"},
		{FieldName: "collection:c1", OldValue: "x", NewValue: "s"},
		{FieldName: "collection:c1", OldValue: rec, NewValue: ""},
		{FieldName: "collection-held:c1", OldValue: rec, NewValue: ""},
		{FieldName: "bogus:c1", OldValue: rec, NewValue: "s"},
	} {
		c.ChangeType = ChangeTypeUserContentFollow
		if NotRestorableLabel(c) == "" {
			t.Fatalf("%+v should not be restorable", c)
		}
	}
	if _, err := DecodeMembership(`{"index":-1}`); err == nil {
		t.Fatal("a negative index must not decode")
	}
}
