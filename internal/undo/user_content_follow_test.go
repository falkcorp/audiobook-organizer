// file: internal/undo/user_content_follow_test.go
// version: 1.9.1
// guid: 6a1d9e43-2c7b-4f80-b5a3-d9e4f1c70a28
// last-edited: 2026-10-23

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
			back, _, err := RestoreMember(next, "r", "s", rec, MemberRole{Stand: SurvivorDefault}, nil)
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

func TestRestoreMember_Placement(t *testing.T) {
	t.Parallel()

	t.Run("the retired book is listed again: already restored", func(t *testing.T) {
		t.Parallel()
		_, _, err := RestoreMember([]string{"r", "s"}, "r", "s", MembershipRecord{}, MemberRole{Stand: SurvivorDefault}, nil)
		if !errors.Is(err, ErrAlreadyRestored) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("the survivor was taken out of the slot: refused, never guessed", func(t *testing.T) {
		t.Parallel()
		_, _, err := RestoreMember([]string{"a", "b"}, "r", "s", MembershipRecord{Index: 1}, MemberRole{Stand: SurvivorDefault}, nil)
		if RefusalReason(err) != ReasonChangedSince {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("survivor was already a member: the book goes back at its index", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMember([]string{"s", "b"}, "r", "s", MembershipRecord{Index: 1, SurvivorWasMember: true}, MemberRole{Stand: SurvivorDefault}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"s", "r", "b"})
	})

	t.Run("the index is clamped to a list that shrank", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMember([]string{"s"}, "r", "s", MembershipRecord{Index: 9, SurvivorWasMember: true}, MemberRole{Stand: SurvivorDefault}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"s", "r"})
	})

	t.Run("an unrelated edit of the list does not block the restore", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMember([]string{"x", "s", "y", "z"}, "r", "s", MembershipRecord{Index: 1}, MemberRole{Stand: SurvivorDefault}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"x", "r", "y", "z"})
	})

	t.Run("SurvivorStays: another retire stands in the slot, so the survivor stays", func(t *testing.T) {
		t.Parallel()
		// Op1 replaced r by s at index 0; op2 removed b because s was listed.
		got, _, err := RestoreMember([]string{"s"}, "r", "s", MembershipRecord{Index: 0}, MemberRole{Stand: SurvivorStays}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"r", "s"})
	})

	t.Run("SurvivorLeaves: a removal puts the book back and takes the survivor out", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMember([]string{"x", "s", "y"}, "r", "s", MembershipRecord{Index: 1, SurvivorWasMember: true}, MemberRole{Stand: SurvivorLeaves}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"x", "r", "y"})
	})

	t.Run("SurvivorLeaves with the survivor already gone just puts the book back", func(t *testing.T) {
		t.Parallel()
		got, _, err := RestoreMember([]string{"y"}, "r", "s", MembershipRecord{Index: 0, SurvivorWasMember: true}, MemberRole{Stand: SurvivorLeaves}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ucEq(t, got, []string{"r", "y"})
	})

	t.Run("SurvivorStays with the survivor gone is still a refusal", func(t *testing.T) {
		t.Parallel()
		_, _, err := RestoreMember([]string{"a"}, "r", "s", MembershipRecord{Index: 0}, MemberRole{Stand: SurvivorStays}, nil)
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
		rec.Anchored = true // a carry always records its anchors; DecodeMembership refuses a record without
		v, _ := EncodeMembership(rec)
		return &database.OperationChange{BookID: "r", NewValue: "s", OldValue: v}
	}
	if err := CheckMembershipFollow([]string{"a", "s"}, rowOf(MembershipRecord{Index: 1}), UserContentCollection, "L", nil); err != nil {
		t.Fatalf("restorable: %v", err)
	}
	if err := CheckMembershipFollow([]string{"a", "r"}, rowOf(MembershipRecord{Index: 1}), UserContentCollection, "L", nil); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("already: %v", err)
	}
	if RefusalReason(CheckMembershipFollow([]string{"a"}, rowOf(MembershipRecord{Index: 1}), UserContentCollection, "L", nil)) != ReasonChangedSince {
		t.Fatal("a slot the owner emptied must refuse")
	}
	if RefusalReason(CheckMembershipFollow(nil, &database.OperationChange{BookID: "r", NewValue: "s", OldValue: "x"}, UserContentCollection, "L", nil)) != ReasonOldValueUnparsable {
		t.Fatal("a malformed record must refuse")
	}
}

func TestSurvivorStandFor(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	tp := func(sec int) *time.Time { v := at(sec); return &v }
	took := UserContentField(UserContentCollectionTook, "c1")
	held := UserContentField(UserContentCollectionHeld, "c1")
	mk := func(op, retired, field string, created int, reverted *time.Time) *database.OperationChange {
		return &database.OperationChange{ID: op + "/" + retired, ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: "s",
			FieldName: field, NewValue: retired, CreatedAt: at(created), RevertedAt: reverted}
	}
	row := func(op, retired string) *database.OperationChange {
		return &database.OperationChange{ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: retired, NewValue: "s"}
	}
	cases := []struct {
		name string
		rows []*database.OperationChange
		row  *database.OperationChange
		want SurvivorStand
	}{
		{"a took row with a borrowing marker standing: the survivor stays",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", held, 2, nil)}, row("op0", "a"), SurvivorStays},
		{"the last took row: the survivor swaps back out",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", held, 2, tp(5))}, row("op0", "a"), SurvivorDefault},
		{"the last held row that borrows: the survivor leaves",
			[]*database.OperationChange{mk("op0", "a", took, 1, tp(4)), mk("op1", "b", held, 2, nil)}, row("op1", "b"), SurvivorLeaves},
		{"a held row with a sibling still standing: stays",
			[]*database.OperationChange{mk("op0", "a", took, 1, tp(4)), mk("op1", "b", held, 2, nil), mk("op1", "c", held, 3, nil)}, row("op1", "b"), SurvivorStays},
		{"a held row whose siblings were already restored: leaves",
			[]*database.OperationChange{mk("op0", "a", took, 1, tp(4)), mk("op1", "b", held, 2, nil), mk("op1", "c", held, 3, tp(5))}, row("op1", "b"), SurvivorLeaves},
		{"held borrows through another held (transitive), took reverted since: the later one stays while the earlier stands",
			[]*database.OperationChange{mk("op0", "a", took, 1, tp(6)), mk("op1", "b", held, 2, nil), mk("op2", "c", held, 3, nil)}, row("op1", "b"), SurvivorStays},
		{"held borrows through another held (transitive): the last of them leaves",
			[]*database.OperationChange{mk("op0", "a", took, 1, tp(6)), mk("op1", "b", held, 2, tp(7)), mk("op2", "c", held, 3, nil)}, row("op2", "c"), SurvivorLeaves},
		{"a held marker written when the survivor was an original member does not borrow",
			[]*database.OperationChange{mk("op0", "a", held, 1, nil)}, row("op0", "a"), SurvivorDefault},
		{"a held marker written after the took one was reverted does not borrow",
			[]*database.OperationChange{mk("op0", "a", took, 1, tp(3)), mk("op1", "b", held, 5, nil)}, row("op1", "b"), SurvivorDefault},
		{"a voided marker does not count",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), {ID: "v", ChangeType: ChangeTypeUserContentFollow, OperationID: "op1", BookID: "s", FieldName: held, NewValue: "b", CreatedAt: at(2), Voided: true}}, row("op0", "a"), SurvivorDefault},
		{"another list's marker does not count",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", UserContentField(UserContentCollectionHeld, "c9"), 2, nil)}, row("op0", "a"), SurvivorDefault},
		{"a playlist marker does not count for a collection",
			[]*database.OperationChange{mk("op0", "a", took, 1, nil), mk("op1", "b", UserContentField(UserContentPlaylistHeld, "c1"), 2, nil)}, row("op0", "a"), SurvivorDefault},
		{"no marker for the row (its carry's marker was voided)",
			[]*database.OperationChange{mk("op1", "b", took, 1, nil)}, row("op0", "a"), SurvivorDefault},
		{"no rows", nil, row("op0", "a"), SurvivorDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, _ := SurvivorStandFor(pairRows(tc.rows), tc.row, UserContentCollection, "c1"); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMarkerOf(t *testing.T) {
	t.Parallel()
	held := UserContentField(UserContentCollectionHeld, "c1")
	m := &database.OperationChange{ID: "m", ChangeType: ChangeTypeUserContentFollow, OperationID: "op1", BookID: "s", FieldName: held, NewValue: "r"}
	other := &database.OperationChange{ID: "o", ChangeType: ChangeTypeUserContentFollow, OperationID: "op2", BookID: "s", FieldName: held, NewValue: "r"}
	row := &database.OperationChange{OperationID: "op1", BookID: "r", NewValue: "s"}
	if got := MarkerOf([]*database.OperationChange{other, m}, row, UserContentCollection, "c1"); got != m {
		t.Fatalf("got %+v", got)
	}
	if MarkerOf([]*database.OperationChange{other}, row, UserContentCollection, "c1") != nil {
		t.Fatal("another operation's marker is not this row's")
	}
	if MarkerOf([]*database.OperationChange{m}, row, UserContentCollection, "c2") != nil {
		t.Fatal("another list's marker is not this row's")
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
	rec, _ := EncodeMembership(MembershipRecord{Index: 2, Anchored: true})
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
		{FieldName: "collection:c1", OldValue: `{"index":2}`, NewValue: "s"}, // no anchors: not a carry's record
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

func TestPairedRow(t *testing.T) {
	mk := func(id, op, book, field, nv string) *database.OperationChange {
		return &database.OperationChange{ID: id, OperationID: op, BookID: book, ChangeType: ChangeTypeUserContentFollow, FieldName: field, NewValue: nv}
	}
	row := mk("r1", "op1", "retired", UserContentField(UserContentCollection, "c1"), "survivor")
	otherList := mk("r2", "op1", "retired", UserContentField(UserContentCollection, "c2"), "survivor")
	otherOp := mk("r3", "op2", "retired", UserContentField(UserContentCollection, "c1"), "survivor")
	otherBook := mk("r4", "op1", "other", UserContentField(UserContentCollection, "c1"), "survivor")
	pl := mk("r5", "op1", "retired", UserContentField(UserContentPlaylist, "c1"), "survivor")
	all := []*database.OperationChange{otherList, otherOp, otherBook, pl, row}

	m := mk("m1", "op1", "survivor", UserContentField(UserContentCollectionHeld, "c1"), "retired")
	if got := PairedRow(all, m); got != row {
		t.Fatalf("held collection marker paired with %v, want r1", got)
	}
	m.FieldName = UserContentField(UserContentCollectionTook, "c1")
	if got := PairedRow(all, m); got != row {
		t.Fatalf("took collection marker paired with %v, want r1", got)
	}
	m.FieldName = UserContentField(UserContentPlaylistTook, "c1")
	if got := PairedRow(all, m); got != pl {
		t.Fatalf("playlist marker paired with %v, want r5", got)
	}
	m.FieldName = UserContentField(UserContentCollectionHeld, "zz")
	if got := PairedRow(all, m); got != nil {
		t.Fatalf("marker of an unknown list paired with %v, want nil", got)
	}
	m.FieldName = "bogus"
	if got := PairedRow(all, m); got != nil {
		t.Fatalf("unparsable marker paired with %v, want nil", got)
	}
}

// SurvivorStandFor says whether the row borrowed the survivor: a took marker,
// or a held marker written while a borrowing marker stood. A row without a
// marker, or with a non-borrowing held marker, borrowed nothing.
func TestSurvivorStandFor_ReportsBorrowing(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	took := UserContentField(UserContentCollectionTook, "c1")
	held := UserContentField(UserContentCollectionHeld, "c1")
	mk := func(op, retired, field string, created int) *database.OperationChange {
		return &database.OperationChange{ID: op + "/" + retired, ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: "s",
			FieldName: field, NewValue: retired, CreatedAt: at(created)}
	}
	row := func(op, retired string) *database.OperationChange {
		return &database.OperationChange{ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: retired, NewValue: "s"}
	}
	cases := []struct {
		name         string
		rows         []*database.OperationChange
		row          *database.OperationChange
		want         SurvivorStand
		wantBorrowed bool
	}{
		{"a took row borrows the survivor and, alone, swaps it back", []*database.OperationChange{mk("op0", "a", took, 1)}, row("op0", "a"), SurvivorDefault, true},
		{"a took row with another borrower standing keeps it", []*database.OperationChange{mk("op0", "a", took, 1), mk("op1", "b", held, 2)}, row("op0", "a"), SurvivorStays, true},
		{"a held row borrows when a took marker stood", []*database.OperationChange{mk("op0", "a", took, 1), mk("op1", "b", held, 2)}, row("op1", "b"), SurvivorStays, true},
		{"a held row of an original member borrows nothing", []*database.OperationChange{mk("op0", "a", held, 1)}, row("op0", "a"), SurvivorDefault, false},
		{"a row with no marker borrows nothing", nil, row("op0", "a"), SurvivorDefault, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stand, borrowed := SurvivorStandFor(pairRows(tc.rows), tc.row, UserContentCollection, "c1")
			if stand != tc.want || borrowed != tc.wantBorrowed {
				t.Fatalf("got stand %v borrowed %v, want %v %v", stand, borrowed, tc.want, tc.wantBorrowed)
			}
		})
	}
}

// RestoreMember is the placement of a revert plus the repair of a revert that wrote the
// list and died before stamping: the book is already listed, the row borrowed
// the survivor, nothing else stands on it, so the survivor is taken out.
func TestRestoreMember(t *testing.T) {
	t.Parallel()
	tookRec := MembershipRecord{Index: 0, SurvivorWasMember: false}
	heldRec := MembershipRecord{Index: 0, SurvivorWasMember: true}
	cases := []struct {
		name         string
		cur          []string
		rec          MembershipRecord
		stand        SurvivorStand
		borrowed     bool
		want         []string
		wantReleased bool
		wantAlready  bool
	}{
		{"book listed again, borrowed survivor still listed: released", []string{"r", "s", "x"}, tookRec, SurvivorDefault, true, []string{"r", "x"}, true, false},
		{"a held row's borrowed survivor is released too", []string{"r", "s", "x"}, heldRec, SurvivorLeaves, true, []string{"r", "x"}, true, false},
		{"something else stands on the survivor: kept", []string{"r", "s", "x"}, tookRec, SurvivorStays, true, nil, false, true},
		{"the row borrowed nothing: kept", []string{"r", "s", "x"}, heldRec, SurvivorDefault, false, nil, false, true},
		{"the survivor is not listed: nothing to release", []string{"r", "x"}, tookRec, SurvivorDefault, true, nil, false, true},
		{"book not listed: the ordinary restore, no release", []string{"s", "x"}, tookRec, SurvivorDefault, true, []string{"r", "x"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next, res, err := RestoreMember(tc.cur, "r", "s", tc.rec, MemberRole{Stand: tc.stand, Borrowed: tc.borrowed}, nil)
			released := res.Released
			if tc.wantAlready {
				if !errors.Is(err, ErrAlreadyRestored) || released {
					t.Fatalf("got released=%v err=%v, want ErrAlreadyRestored", released, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if released != tc.wantReleased {
				t.Fatalf("released=%v, want %v", released, tc.wantReleased)
			}
			ucEq(t, next, tc.want)
		})
	}
}

// RestoreMember with a retired book that stays out (MemberRole.RetiredStaysOut):
// the book is not put back, the survivor plays its part as for any row, and the
// result says so. A book already listed is still ErrAlreadyRestored.
func TestRestoreMember_RetiredBookStaysOut(t *testing.T) {
	t.Parallel()
	tookRec := MembershipRecord{Index: 1, SurvivorWasMember: false}
	heldRec := MembershipRecord{Index: 1, SurvivorWasMember: true}
	cases := []struct {
		name        string
		cur         []string
		rec         MembershipRecord
		stand       SurvivorStand
		want        []string
		wantAlready bool
	}{
		{"the survivor took the slot and nothing stands on it: it leaves, the book stays out", []string{"x", "s", "y"}, tookRec, SurvivorDefault, []string{"x", "y"}, false},
		{"the survivor took the slot and another retire stands on it: it stays", []string{"x", "s", "y"}, tookRec, SurvivorStays, []string{"x", "s", "y"}, false},
		{"a removal whose survivor was left for it: the survivor leaves", []string{"x", "s", "y"}, heldRec, SurvivorLeaves, []string{"x", "y"}, false},
		{"a removal whose survivor was an original member: nothing changes", []string{"x", "s", "y"}, heldRec, SurvivorDefault, []string{"x", "s", "y"}, false},
		{"the survivor is not listed: nothing to take out", []string{"x", "y"}, tookRec, SurvivorDefault, []string{"x", "y"}, false},
		{"the book is listed after all: already restored", []string{"x", "r", "y"}, heldRec, SurvivorDefault, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			next, res, err := RestoreMember(tc.cur, "r", "s", tc.rec, MemberRole{Stand: tc.stand, Borrowed: true, RetiredStaysOut: true}, nil)
			if tc.wantAlready {
				if !errors.Is(err, ErrAlreadyRestored) || res.LeftOut {
					t.Fatalf("got leftOut=%v err=%v, want ErrAlreadyRestored", res.LeftOut, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !res.LeftOut {
				t.Fatal("the result does not say the book was left out")
			}
			ucEq(t, next, tc.want)
		})
	}
}

// RetiredStaysOut: a retired book that was itself a survivor, listed only on
// other retires' account, comes back only while one of them still stands.
func TestRetiredStaysOut(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	at := func(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }
	reverted := func(c *database.OperationChange, sec int) *database.OperationChange {
		t := at(sec)
		c.RevertedAt = &t
		return c
	}
	// r is the survivor of these markers (BookID r).
	mk := func(op, retired string, took bool, created int) *database.OperationChange {
		kind := UserContentCollectionHeld
		if took {
			kind = UserContentCollectionTook
		}
		return &database.OperationChange{ID: op + "/" + retired, ChangeType: ChangeTypeUserContentFollow, OperationID: op, BookID: "r",
			FieldName: UserContentField(kind, "c1"), NewValue: retired, CreatedAt: at(created)}
	}
	// The row that retired r into another survivor, at second 10.
	row := &database.OperationChange{ID: "op9/r", ChangeType: ChangeTypeUserContentFollow, OperationID: "op9", BookID: "r", NewValue: "s3",
		FieldName: UserContentField(UserContentCollection, "c1"), CreatedAt: at(10)}
	cases := []struct {
		name string
		rows []*database.OperationChange
		want bool
	}{
		{"an original member of the list (no markers of its own): comes back", nil, false},
		{"an original member that held a book for another retire: comes back", []*database.OperationChange{mk("op0", "a", false, 1)}, false},
		{"listed on a took marker that still stands: comes back", []*database.OperationChange{mk("op0", "a", true, 1)}, false},
		{"listed on a took marker and a held one, only the held one stands: comes back", []*database.OperationChange{reverted(mk("op0", "a", true, 1), 5), mk("op1", "b", false, 2)}, false},
		{"listed on borrowed account, every retire reverted since the book was retired: stays out", []*database.OperationChange{reverted(mk("op0", "a", true, 1), 12), reverted(mk("op1", "b", false, 2), 13)}, true},
		{"the same, one reverted before the book was retired and one after: stays out", []*database.OperationChange{reverted(mk("op0", "a", true, 1), 5), reverted(mk("op1", "b", false, 2), 11)}, true},
		{"borrowed account ended before the book was retired: it was an original member by then", []*database.OperationChange{reverted(mk("op0", "a", true, 1), 5)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RetiredStaysOut(pairRows(tc.rows), row, UserContentCollection, "c1"); got != tc.want {
				t.Fatalf("RetiredStaysOut = %v, want %v", got, tc.want)
			}
		})
	}
}
