// file: internal/undo/credits_backfill_test.go
// version: 1.1.1
// guid: 13d57ee7-2dda-4c77-b02f-ecdf96215b72
// last-edited: 2026-10-27

package undo

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func cbIntPtr(v int) *int       { return &v }
func cbStrPtr(v string) *string { return &v }

// cbStates: before is a book whose join has the "A @0, B @0" shape and an
// empty narrator junction; after is the backfilled state.
func cbStates() (before, after CreditsBackfillState) {
	before = CreditsBackfillState{
		Authors: []database.BookAuthor{
			{BookID: "b1", AuthorID: 2, Role: "author", Position: 0},
			{BookID: "b1", AuthorID: 1, Role: "author", Position: 0},
		},
		AuthorID: cbIntPtr(1),
		Narrator: cbStrPtr("Oswald Cobblepot"),
	}
	after = CreditsBackfillState{
		Authors: []database.BookAuthor{
			{BookID: "b1", AuthorID: 1, Role: "author", Position: 0},
			{BookID: "b1", AuthorID: 2, Role: "author", Position: 1},
		},
		Narrators: []database.BookNarrator{{BookID: "b1", NarratorID: 7, Role: "narrator", Position: 0}},
		AuthorID:  cbIntPtr(1),
		Author:    &database.Author{ID: 1, Name: "Bruce Wayne"},
		Narrator:  cbStrPtr("Oswald Cobblepot"),
	}
	return before, after
}

func TestCheckCreditsBackfillCurrent_ThreeWay(t *testing.T) {
	before, after := cbStates()

	if err := CheckCreditsBackfillCurrent("b1", after, before, after); err != nil {
		t.Fatalf("current == after: %v, want nil", err)
	}
	// The pre-image comes back through Restored (stored order, 0..n-1), so a
	// book already reverted reads as before.Restored(), not as before and not
	// as before.Canonical() (which puts AuthorID first among tied rows).
	if err := CheckCreditsBackfillCurrent("b1", before.Restored(), before, after); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("current == restored before: %v, want ErrAlreadyRestored", err)
	}
	for name, mutate := range map[string]func(s *CreditsBackfillState){
		"author row added": func(s *CreditsBackfillState) {
			s.Authors = append(s.Authors, database.BookAuthor{AuthorID: 3, Role: "author", Position: 2})
		},
		"narrator removed": func(s *CreditsBackfillState) { s.Narrators = nil },
		"author_id moved":  func(s *CreditsBackfillState) { s.AuthorID = cbIntPtr(2) },
		"snapshot renamed": func(s *CreditsBackfillState) { s.Author = &database.Author{ID: 1, Name: "The Batman"} },
		"column edited":    func(s *CreditsBackfillState) { s.Narrator = cbStrPtr("Edward Nygma") },
		"json set":         func(s *CreditsBackfillState) { s.NarratorsJSON = cbStrPtr(`["Edward Nygma"]`) },
		"position swapped": func(s *CreditsBackfillState) { s.Authors[1].Position = 5 },
		"role changed":     func(s *CreditsBackfillState) { s.Narrators[0].Role = "co-narrator" },
	} {
		_, cur := cbStates()
		mutate(&cur)
		err := CheckCreditsBackfillCurrent("b1", cur, before, after)
		if err == nil || errors.Is(err, ErrAlreadyRestored) || RefusalReason(err) != ReasonChangedSince {
			t.Errorf("%s: %v, want a %s refusal", name, err, ReasonChangedSince)
		}
	}
}

// A journal row whose write never landed (the run died between the journal and
// ModifyBookCredits) leaves the book exactly at its raw pre-image. When the
// stored lists were not canonical (all-zero positions) that differs from
// before.Restored(), and it is already restored, not a changed-since refusal.
func TestCheckCreditsBackfillCurrent_RawPreImageIsAlreadyRestored(t *testing.T) {
	before, after := cbStates()
	if SameCreditsBackfillState(before, before.Restored()) {
		t.Fatal("fixture: the raw pre-image must differ from its Restored form (all-zero positions)")
	}
	if err := CheckCreditsBackfillCurrent("b1", before, before, after); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("current == raw pre-image: %v, want ErrAlreadyRestored", err)
	}
	// An edit on top of the raw pre-image is still a refusal.
	cur := before
	cur.Narrator = cbStrPtr("Edward Nygma")
	if err := CheckCreditsBackfillCurrent("b1", cur, before, after); errors.Is(err, ErrAlreadyRestored) || RefusalReason(err) != ReasonChangedSince {
		t.Fatalf("edited raw pre-image: %v, want a %s refusal", err, ReasonChangedSince)
	}
}

func TestSameCreditsBackfillState_IgnoresRowBookID(t *testing.T) {
	_, a := cbStates()
	_, b := cbStates()
	b.Authors[0].BookID, b.Narrators[0].BookID = "", "other"
	if !SameCreditsBackfillState(a, b) {
		t.Fatal("row book ids must not matter")
	}
	b.Author = nil
	if SameCreditsBackfillState(a, b) {
		t.Fatal("a nil snapshot against a set one compared equal")
	}
}

func TestCreditsBackfillStateOf_DoesNotAlias(t *testing.T) {
	book := &database.Book{ID: "b1", AuthorID: cbIntPtr(1), Author: &database.Author{ID: 1, Name: "Bruce Wayne"},
		Narrator: cbStrPtr("Oswald Cobblepot"), NarratorsJSON: cbStrPtr(`["Oswald Cobblepot"]`)}
	rows := []database.BookAuthor{{BookID: "b1", AuthorID: 1, Role: "author"}}
	s := CreditsBackfillStateOf(book, rows, nil)
	*book.AuthorID, book.Author.Name, *book.Narrator, *book.NarratorsJSON = 9, "x", "x", "x"
	rows[0].AuthorID = 9
	if *s.AuthorID != 1 || s.Author.Name != "Bruce Wayne" || *s.Narrator != "Oswald Cobblepot" ||
		*s.NarratorsJSON != `["Oswald Cobblepot"]` || s.Authors[0].AuthorID != 1 {
		t.Fatalf("state aliases the book or rows: %+v", s)
	}

	var back database.Book
	s.ApplyFlatFields(&back)
	if !SameCreditsBackfillState(CreditsBackfillStateOf(&back, s.Authors, nil), s) {
		t.Fatal("ApplyFlatFields did not round-trip the flat fields")
	}
}

func TestDecodeCreditsBackfill(t *testing.T) {
	before, after := cbStates()
	old, _ := json.Marshal(before)
	good, _ := json.Marshal(CreditsBackfillWrite{After: after, Classes: []string{"author_positions_renumbered"}})
	noClasses, _ := json.Marshal(CreditsBackfillWrite{After: after})
	row := func(oldV, newV string) *database.OperationChange {
		return &database.OperationChange{ID: "c1", BookID: "b1", ChangeType: ChangeTypeCreditsBackfill, OldValue: oldV, NewValue: newV}
	}

	gotBefore, gotWrite, err := DecodeCreditsBackfill(row(string(old), string(good)))
	if err != nil || !SameCreditsBackfillState(gotBefore, before) || !SameCreditsBackfillState(gotWrite.After, after) {
		t.Fatalf("decode: err=%v", err)
	}
	if NotRestorableLabel(row(string(old), string(good))) != "" {
		t.Fatal("a well-formed row was labelled not restorable")
	}
	for name, c := range map[string]*database.OperationChange{
		"no classes":    row(string(old), string(noClasses)),
		"bad pre-image": row("{", string(good)),
		"bad write":     row(string(old), "["),
	} {
		if _, _, err := DecodeCreditsBackfill(c); RefusalReason(err) != ReasonOldValueUnparsable {
			t.Errorf("%s: %v, want %s", name, err, ReasonOldValueUnparsable)
		}
		if l := NotRestorableLabel(c); !strings.HasPrefix(l, ChangeTypeCreditsBackfill+":") {
			t.Errorf("%s: label %q", name, l)
		}
	}
}

// B4: the revert must give the organizer back the filing author it had. The
// organizer files under the first STORED row at the lowest position, so an
// all-zero pre-image [Selina@0, Bruce@0] with AuthorID Bruce restores as
// [Selina@0, Bruce@1] (stored order), not as Canonical's [Bruce@0, Selina@1]
// (the backfill's own AuthorID tie-break).
func TestRestored_KeepsStoredOrderWhereCanonicalBreaksTiesWithAuthorID(t *testing.T) {
	before, _ := cbStates()
	ids := func(rows []database.BookAuthor) []int {
		var out []int
		for _, r := range rows {
			out = append(out, r.AuthorID)
		}
		return out
	}
	if got := ids(before.Restored().Authors); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("Restored = %v, want stored order [2 1]", got)
	}
	if got := ids(before.Canonical().Authors); got[0] != 1 {
		t.Fatalf("Canonical = %v, the AuthorID tie-break should put 1 first (the forward write's rule)", got)
	}
	r := before.Restored()
	for i, row := range r.Authors {
		if row.Position != i {
			t.Fatalf("Restored positions = %+v, want 0..n-1", r.Authors)
		}
	}
	// Idempotent: the store normalising Restored's rows with AuthorID as the
	// tie-breaker (distinct positions, nothing to break) changes nothing.
	again := CreditsBackfillState{Authors: database.NormalizeBookAuthors(r.Authors, before.AuthorID), AuthorID: before.AuthorID}
	if !SameCreditsBackfillState(CreditsBackfillState{Authors: r.Authors, AuthorID: before.AuthorID}, again) {
		t.Fatalf("the store's AuthorID tie-breaker reorders a Restored list: %+v", again.Authors)
	}
	// The AuthorID-first form is NOT "already restored": a book holding it
	// when the revert runs holds the backfill's order, not the pre-image's, so
	// it is refused as changed-since rather than skipped as done.
	_, after := cbStates()
	err := CheckCreditsBackfillCurrent("b1", before.Canonical(), before, after)
	if errors.Is(err, ErrAlreadyRestored) || RefusalReason(err) != ReasonChangedSince {
		t.Fatalf("current == canonical before: %v, want a %s refusal", err, ReasonChangedSince)
	}
}
