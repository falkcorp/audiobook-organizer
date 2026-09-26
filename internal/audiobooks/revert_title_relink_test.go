// file: internal/audiobooks/revert_title_relink_test.go
// version: 1.0.0
// guid: 6dda78bf-049e-4911-b7d4-eea7bb31d0b7
// last-edited: 2026-09-26

package audiobooks

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// relinkStore is a MockStore holding one book's junction and primary, a set
// of authors, and the op's journal rows.
type relinkStore struct {
	*database.MockStore
	credits map[string][]database.BookAuthor
	primary map[string]*int
	authors map[int]database.Author
	deleted []int
	marked  []string
}

func newRelinkStore(rows []*database.OperationChange) *relinkStore {
	s := &relinkStore{
		MockStore: &database.MockStore{},
		credits:   map[string][]database.BookAuthor{},
		primary:   map[string]*int{},
		authors:   map[int]database.Author{},
	}
	s.GetOperationChangesFunc = func(string) ([]*database.OperationChange, error) { return rows, nil }
	s.MarkOperationChangesRevertedFunc = func(_ string, ids []string) error {
		s.marked = append(s.marked, ids...)
		return nil
	}
	s.GetBookByIDFunc = func(id string) (*database.Book, error) {
		if _, ok := s.credits[id]; !ok {
			return nil, nil
		}
		return &database.Book{ID: id, Title: "T", AuthorID: s.primary[id]}, nil
	}
	s.ModifyBookFunc = func(id string, fn func(*database.Book) error) (*database.Book, error) {
		b, _ := s.GetBookByIDFunc(id)
		if b == nil {
			return nil, nil
		}
		if err := fn(b); err != nil {
			if err == database.ErrSkipBookWrite {
				return b, nil
			}
			return nil, err
		}
		s.primary[id] = b.AuthorID
		return b, nil
	}
	s.ModifyBookAuthorsFunc = func(id string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) ([]database.BookAuthor, error) {
		out, err := fn(append([]database.BookAuthor(nil), s.credits[id]...))
		if err != nil {
			return nil, err
		}
		s.credits[id] = out
		return out, nil
	}
	s.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
		if a, ok := s.authors[id]; ok {
			return &a, nil
		}
		return nil, nil
	}
	s.GetAuthorByNameFunc = func(name string) (*database.Author, error) {
		for _, a := range s.authors {
			if a.Name == name {
				c := a
				return &c, nil
			}
		}
		return nil, nil
	}
	s.GetBooksByAuthorIDForRelinkFunc = func(authorID int) ([]database.BookCore, error) {
		var out []database.BookCore
		for id, cs := range s.credits {
			for _, ba := range cs {
				if ba.AuthorID == authorID {
					out = append(out, database.BookCore{ID: id})
					break
				}
			}
		}
		return out, nil
	}
	s.DeleteAuthorFunc = func(id int) error {
		s.deleted = append(s.deleted, id)
		delete(s.authors, id)
		return nil
	}
	return s
}

func creditsRow(id, bookID string, snap undo.TitleRelinkCreditsSnapshot, from, into int) *database.OperationChange {
	oldJSON, _ := json.Marshal(snap)
	newJSON, _ := json.Marshal(undo.TitleRelinkCreditsMove{FromAuthorID: from, IntoAuthorID: into})
	return &database.OperationChange{ID: id, OperationID: "op", BookID: bookID, ChangeType: undo.ChangeTypeTitleRelinkCredits,
		FieldName: "book_authors", OldValue: string(oldJSON), NewValue: string(newJSON)}
}

func createRow(id, bookID, name string) *database.OperationChange {
	return &database.OperationChange{ID: id, OperationID: "op", BookID: bookID, ChangeType: undo.ChangeTypeTitleRelinkAuthorCreate,
		FieldName: "author_name", NewValue: name}
}

var arcaneOriginal = []database.BookAuthor{
	{BookID: "b1", AuthorID: 100, Role: "author"},
	{BookID: "b1", AuthorID: 400, Role: "co-author", Position: 1},
}

// The relink created author 901 and moved b1's credit 100 -> 901 (keeping the
// co-author 400). Undo restores b1's junction exactly (order, roles,
// co-author) and its primary, then deletes 901, which nothing credits any
// more.
func TestRevertTitleRelink_RestoresCreditsAndDeletesUncreditedAuthor(t *testing.T) {
	rows := []*database.OperationChange{
		createRow("c1", "b1", "Brand New Writer"),
		creditsRow("c2", "b1", undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: arcaneOriginal}, 100, 901),
	}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "Arcane Chef 2"}
	s.authors[400] = database.Author{ID: 400, Name: "Co Writer"}
	s.authors[901] = database.Author{ID: 901, Name: "Brand New Writer"}
	s.credits["b1"] = []database.BookAuthor{
		{BookID: "b1", AuthorID: 400, Role: "co-author", Position: 1},
		{BookID: "b1", AuthorID: 901, Role: "author", Position: 1},
	}
	s.primary["b1"] = intp(901)

	res, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if res.Restored != 2 || res.Failed != 0 || res.NotRestorable != 0 {
		t.Errorf("result = %+v, want restored 2", res)
	}
	if !reflect.DeepEqual(s.credits["b1"], arcaneOriginal) {
		t.Errorf("credits = %+v, want %+v", s.credits["b1"], arcaneOriginal)
	}
	if p := s.primary["b1"]; p == nil || *p != 100 {
		t.Errorf("primary = %v, want 100", p)
	}
	if !reflect.DeepEqual(s.deleted, []int{901}) {
		t.Errorf("deleted = %v, want [901]", s.deleted)
	}
}

// A created author still credited elsewhere (here: b2, whose credit is not
// part of this revert) is KEPT; the row still counts restored.
func TestRevertTitleRelink_KeepsCreatedAuthorStillCredited(t *testing.T) {
	rows := []*database.OperationChange{
		createRow("c1", "b1", "Brand New Writer"),
		creditsRow("c2", "b1", undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: arcaneOriginal[:1]}, 100, 901),
	}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "Arcane Chef 2"}
	s.authors[901] = database.Author{ID: 901, Name: "Brand New Writer"}
	s.credits["b1"] = []database.BookAuthor{{BookID: "b1", AuthorID: 901, Role: "author"}}
	s.primary["b1"] = intp(901)
	s.credits["b2"] = []database.BookAuthor{{BookID: "b2", AuthorID: 901, Role: "author"}}

	res, err := NewRevertService(s).RevertOperation("op")
	if err != nil {
		t.Fatalf("RevertOperation: %v (%+v)", err, res)
	}
	if len(s.deleted) != 0 {
		t.Errorf("author still credited on b2 was deleted: %v", s.deleted)
	}
	if res.Restored != 2 {
		t.Errorf("result = %+v, want restored 2", res)
	}
}

// A book whose credits changed after the relink (the real author was since
// removed) is refused as changed-since, and nothing is written.
func TestRevertTitleRelink_RefusesChangedCredits(t *testing.T) {
	rows := []*database.OperationChange{
		creditsRow("c1", "b1", undo.TitleRelinkCreditsSnapshot{AuthorID: intp(100), Credits: arcaneOriginal}, 100, 300),
	}
	s := newRelinkStore(rows)
	s.authors[100] = database.Author{ID: 100, Name: "Arcane Chef 2"}
	later := []database.BookAuthor{{BookID: "b1", AuthorID: 555, Role: "author"}}
	s.credits["b1"] = later
	s.primary["b1"] = intp(555)

	res, _ := NewRevertService(s).RevertOperation("op")
	if res == nil || res.Failed != 1 || res.ChangedSince != 1 || res.Restored != 0 {
		t.Fatalf("result = %+v, want failed 1 changed-since 1", res)
	}
	if !reflect.DeepEqual(s.credits["b1"], later) || *s.primary["b1"] != 555 {
		t.Errorf("a later change was overwritten: credits=%+v primary=%v", s.credits["b1"], *s.primary["b1"])
	}
}

// Both change types are restorable; a credits row whose old value does not
// parse is not, and says why.
func TestNotRestorableLabel_TitleRelink(t *testing.T) {
	ok := creditsRow("c1", "b1", undo.TitleRelinkCreditsSnapshot{Credits: arcaneOriginal}, 100, 300)
	if l := undo.NotRestorableLabel(ok); l != "" {
		t.Errorf("credits row label = %q, want restorable", l)
	}
	if l := undo.NotRestorableLabel(createRow("c2", "b1", "X")); l != "" {
		t.Errorf("create row label = %q, want restorable", l)
	}
	bad := &database.OperationChange{ID: "c3", ChangeType: undo.ChangeTypeTitleRelinkCredits, OldValue: "{", NewValue: "{}"}
	if l := undo.NotRestorableLabel(bad); l != undo.ChangeTypeTitleRelinkCredits+":(unparsable)" {
		t.Errorf("bad row label = %q", l)
	}
	if l := undo.NotRestorableLabel(createRow("c4", "b1", "")); l == "" {
		t.Errorf("create row with no name must not be restorable")
	}
}
