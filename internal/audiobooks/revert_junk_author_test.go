// file: internal/audiobooks/revert_junk_author_test.go
// version: 1.1.0
// guid: 6f61a9d0-210f-4702-ae53-7ec0a671e4ff
// last-edited: 2026-09-29

package audiobooks

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// after is a journaled post-write junction (never nil: an empty junction is
// the unlink of a book's only credit).
func after(cs ...database.BookAuthor) *[]database.BookAuthor {
	if cs == nil {
		cs = []database.BookAuthor{}
	}
	return &cs
}

func junkRow(id, bookID string, snap undo.TitleRelinkCreditsSnapshot, move undo.JunkAuthorCreditsMove) *database.OperationChange {
	oldJSON, _ := json.Marshal(snap)
	newJSON, _ := json.Marshal(move)
	return &database.OperationChange{ID: id, OperationID: "op", BookID: bookID, ChangeType: undo.ChangeTypeJunkAuthorCredits,
		FieldName: "book_authors", OldValue: string(oldJSON), NewValue: string(newJSON)}
}

// relinkMove is the 100 ("Demon Cycle") -> 300 relink that also moved the
// primary.
func relinkMove() undo.JunkAuthorCreditsMove {
	return undo.JunkAuthorCreditsMove{FromAuthorID: 100, IntoAuthorID: 300, PrimaryAfter: intp(300), PrimaryChanged: true,
		CreditsAfter: after(database.BookAuthor{BookID: "b1", AuthorID: 300, Role: "author"})}
}

// A relink that also moved the primary is put back exactly: junction order
// and roles, and the primary.
func TestRevertJunkAuthor_RestoresRelink(t *testing.T) {
	orig := []database.BookAuthor{{BookID: "b1", AuthorID: 100, Role: "author"}}
	rows := []*database.OperationChange{junkRow("c1", "b1",
		undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: orig}, relinkMove())}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "Demon Cycle"}
	s.authors[300] = database.Author{ID: 300, Name: "Peter V. Brett"}
	s.credits["b1"] = []database.BookAuthor{{BookID: "b1", AuthorID: 300, Role: "author"}}
	s.primary["b1"] = intp(300)

	res, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if res.Restored != 1 || res.Failed != 0 || res.NotRestorable != 0 {
		t.Errorf("result = %+v, want restored 1", res)
	}
	if !reflect.DeepEqual(s.credits["b1"], orig) {
		t.Errorf("credits = %+v, want %+v", s.credits["b1"], orig)
	}
	if p := s.primary["b1"]; p == nil || *p != 100 {
		t.Errorf("primary = %v, want 100", p)
	}
	if len(s.deleted) != 0 {
		t.Errorf("a junk-author revert deleted authors: %v", s.deleted)
	}
}

// An UNLINK (no real author found: into 0) is reverted too: the title-relink
// type could not express it.
func TestRevertJunkAuthor_RestoresUnlink(t *testing.T) {
	orig := []database.BookAuthor{
		{BookID: "b1", AuthorID: 100, Role: "author"},
		{BookID: "b1", AuthorID: 400, Role: "co-author", Position: 1},
	}
	written := database.BookAuthor{BookID: "b1", AuthorID: 400, Role: "co-author", Position: 0}
	rows := []*database.OperationChange{junkRow("c1", "b1",
		undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: orig},
		undo.JunkAuthorCreditsMove{FromAuthorID: 100, IntoAuthorID: 0, PrimaryAfter: intp(400), PrimaryChanged: true,
			CreditsAfter: after(written)})}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "Unknown"}
	s.authors[400] = database.Author{ID: 400, Name: "Co Writer"}
	s.credits["b1"] = []database.BookAuthor{written}
	s.primary["b1"] = intp(400)

	res, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if res.Restored != 1 {
		t.Errorf("result = %+v, want restored 1", res)
	}
	if !reflect.DeepEqual(s.credits["b1"], orig) {
		t.Errorf("credits = %+v, want %+v", s.credits["b1"], orig)
	}
	if p := s.primary["b1"]; p == nil || *p != 100 {
		t.Errorf("primary = %v, want 100", p)
	}
}

// Any junction other than exactly the repair's is a later change: refused,
// nothing written -- the junction and the primary both stay as they are.
func TestRevertJunkAuthor_RefusesLaterChange(t *testing.T) {
	orig := []database.BookAuthor{{BookID: "b1", AuthorID: 100, Role: "author"}}
	for name, later := range map[string][]database.BookAuthor{
		"junk re-added":     {{BookID: "b1", AuthorID: 300, Role: "author"}, {BookID: "b1", AuthorID: 100, Role: "author", Position: 1}},
		"real author gone":  {{BookID: "b1", AuthorID: 555, Role: "author"}},
		"credits emptied":   {},
		"other real author": {{BookID: "b1", AuthorID: 556, Role: "author"}},
		"co-author added":   {{BookID: "b1", AuthorID: 300, Role: "author"}, {BookID: "b1", AuthorID: 557, Role: "author", Position: 1}},
		"role changed":      {{BookID: "b1", AuthorID: 300, Role: "narrator"}},
	} {
		t.Run(name, func(t *testing.T) {
			rows := []*database.OperationChange{junkRow("c1", "b1",
				undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: orig}, relinkMove())}
			s := newRelinkStore(rows)
			s.authors[100] = database.Author{ID: 100, Name: "Demon Cycle"}
			s.credits["b1"] = append([]database.BookAuthor{}, later...)
			s.primary["b1"] = intp(300)
			res, _ := NewRevertService(s).RevertOperation("op")
			if res == nil || res.Failed != 1 || res.Restored != 0 {
				t.Fatalf("result = %+v, want failed 1", res)
			}
			if !reflect.DeepEqual(s.credits["b1"], append([]database.BookAuthor{}, later...)) {
				t.Errorf("credits overwritten: %+v, want %+v", s.credits["b1"], later)
			}
			if p := s.primary["b1"]; p == nil || *p != 300 {
				t.Errorf("primary overwritten: %v", p)
			}
		})
	}
}

// A primary that changed since the repair is refused BEFORE the junction is
// touched: no half-revert.
func TestRevertJunkAuthor_RefusesChangedPrimary(t *testing.T) {
	orig := []database.BookAuthor{{BookID: "b1", AuthorID: 100, Role: "author"}}
	rows := []*database.OperationChange{junkRow("c1", "b1",
		undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: orig}, relinkMove())}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "Demon Cycle"}
	s.credits["b1"] = []database.BookAuthor{{BookID: "b1", AuthorID: 300, Role: "author"}}
	s.primary["b1"] = intp(777)
	res, _ := NewRevertService(s).RevertOperation("op")
	if res == nil || res.Failed != 1 || res.ChangedSince != 1 {
		t.Fatalf("result = %+v, want failed 1 changed-since 1", res)
	}
	if *s.primary["b1"] != 777 {
		t.Errorf("primary overwritten: %v", *s.primary["b1"])
	}
	if want := []database.BookAuthor{{BookID: "b1", AuthorID: 300, Role: "author"}}; !reflect.DeepEqual(s.credits["b1"], want) {
		t.Errorf("junction half-reverted: %+v", s.credits["b1"])
	}
}

// An unlink whose book the owner then fixed by hand (a real author credited,
// the primary set) is refused with nothing written: the junk credit is not
// put back over the owner's fix.
func TestRevertJunkAuthor_RefusesAfterManualFix(t *testing.T) {
	orig := []database.BookAuthor{{BookID: "b1", AuthorID: 100, Role: "author"}}
	rows := []*database.OperationChange{junkRow("c1", "b1",
		undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: orig},
		undo.JunkAuthorCreditsMove{FromAuthorID: 100, IntoAuthorID: 0, PrimaryChanged: true, CreditsAfter: after()})}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "GraphicAudio"}
	fixed := []database.BookAuthor{{BookID: "b1", AuthorID: 900, Role: "author"}}
	s.credits["b1"] = fixed
	s.primary["b1"] = intp(900)
	res, _ := NewRevertService(s).RevertOperation("op")
	if res == nil || res.Failed != 1 || res.ChangedSince != 1 {
		t.Fatalf("result = %+v, want failed 1 changed-since 1", res)
	}
	if !reflect.DeepEqual(s.credits["b1"], fixed) || *s.primary["b1"] != 900 {
		t.Errorf("owner's fix overwritten: credits=%+v primary=%v", s.credits["b1"], *s.primary["b1"])
	}
}

// The created-author revert deletes the row it journaled by ID, and only
// that row: a same-named row someone else made is kept.
func TestRevertJunkAuthorCreate_DeletesOnlyTheJournaledID(t *testing.T) {
	mk := func(id int) *database.OperationChange {
		v, _ := json.Marshal(undo.JunkAuthorCreate{AuthorID: id, Name: "Robin Hobb"})
		return &database.OperationChange{ID: "c", OperationID: "op", BookID: "b1",
			ChangeType: undo.ChangeTypeJunkAuthorCreate, FieldName: "author", NewValue: string(v)}
	}
	s := newRelinkStore([]*database.OperationChange{mk(501)})
	s.authors[501] = database.Author{ID: 501, Name: "Robin Hobb"}
	res, err := NewRevertService(s).RevertOperation("op")
	if err != nil || res.Restored != 1 {
		t.Fatalf("revert: %v %+v", err, res)
	}
	if !reflect.DeepEqual(s.deleted, []int{501}) {
		t.Errorf("deleted %v, want [501]", s.deleted)
	}

	// The journaled row is gone; a same-named row with another ID is not ours.
	s = newRelinkStore([]*database.OperationChange{mk(501)})
	s.authors[777] = database.Author{ID: 777, Name: "Robin Hobb"}
	res, err = NewRevertService(s).RevertOperation("op")
	if err != nil || res.Restored != 1 {
		t.Fatalf("revert: %v %+v", err, res)
	}
	if len(s.deleted) != 0 {
		t.Errorf("deleted a row this op never made: %v", s.deleted)
	}
}

func TestDecodeJunkAuthorCredits_RefusesBadRows(t *testing.T) {
	for name, c := range map[string]*database.OperationChange{
		"bad old":        {ID: "x", OldValue: "{", NewValue: `{"from_author_id":1,"credits_after":[]}`},
		"bad new":        {ID: "x", OldValue: `{}`, NewValue: "{"},
		"no from":        {ID: "x", OldValue: `{}`, NewValue: `{"into_author_id":3,"credits_after":[]}`},
		"negative in":    {ID: "x", OldValue: `{}`, NewValue: `{"from_author_id":1,"into_author_id":-1,"credits_after":[]}`},
		"no after state": {ID: "x", OldValue: `{}`, NewValue: `{"from_author_id":1}`},
	} {
		if _, _, err := undo.DecodeJunkAuthorCredits(c); err == nil {
			t.Errorf("%s: decoded", name)
		}
	}
	c := &database.OperationChange{ID: "x", ChangeType: undo.ChangeTypeJunkAuthorCredits, OldValue: `{}`, NewValue: `{"from_author_id":1,"credits_after":[]}`}
	if _, _, err := undo.DecodeJunkAuthorCredits(c); err != nil {
		t.Errorf("unlink row refused: %v", err)
	}
}
