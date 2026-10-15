// file: internal/authormerge/authormerge_test.go
// version: 1.2.0
// guid: 8a1e4c62-9d37-4b05-b6f8-2e7a5c0d9f13
// last-edited: 2026-10-15

package authormerge

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// fakeStore records ledger rows (by id, latest write wins, as the store does)
// and fails the writes a test names.
type fakeStore struct {
	rows      map[string]database.OperationChange
	order     []string
	junction  map[string][]database.BookAuthor
	books     map[string]*database.Book
	setErr    error
	modifyErr error
	deleteErr error
	sets      int
	aliases   []database.AuthorAlias
}

func newFake() *fakeStore {
	return &fakeStore{rows: map[string]database.OperationChange{}, junction: map[string][]database.BookAuthor{}, books: map[string]*database.Book{}}
}

func (f *fakeStore) CreateOperationChange(c *database.OperationChange) error {
	if _, ok := f.rows[c.ID]; !ok {
		f.order = append(f.order, c.ID)
	}
	f.rows[c.ID] = *c
	return nil
}
func (f *fakeStore) GetBookAuthors(id string) ([]database.BookAuthor, error) {
	return f.junction[id], nil
}
func (f *fakeStore) SetBookAuthors(id string, a []database.BookAuthor) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.sets++
	f.junction[id] = a
	return nil
}
func (f *fakeStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if f.modifyErr != nil {
		return nil, f.modifyErr
	}
	b := f.books[id]
	if b == nil {
		return nil, nil
	}
	if err := fn(b); err != nil {
		if errors.Is(err, database.ErrSkipBookWrite) {
			return b, nil
		}
		return nil, err
	}
	return b, nil
}
func (f *fakeStore) GetAuthorAliases(int) ([]database.AuthorAlias, error) { return f.aliases, nil }
func (f *fakeStore) AuthorNameHolder(string) (int, error)                 { return 0, nil }
func (f *fakeStore) MergeDeleteAuthor(fromID, intoID int) (*database.AuthorMergeDeleteResult, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	return &database.AuthorMergeDeleteResult{}, nil
}

// creditsFake is a fakeStore with the BookCreditsModifier capability: the row
// and the junction are written together, or neither is.
type creditsFake struct {
	*fakeStore
	creditsErr error
	calls      int
}

func (f *creditsFake) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	f.calls++
	if f.creditsErr != nil {
		return nil, database.BookCreditsEdit{}, f.creditsErr
	}
	b := f.books[id]
	if b == nil {
		return nil, database.BookCreditsEdit{}, nil
	}
	cp := *b
	edit := database.BookCreditsEdit{Authors: slices.Clone(f.junction[id])}
	if err := fn(&cp, &edit); err != nil {
		return nil, database.BookCreditsEdit{}, err
	}
	*b = cp
	f.junction[id] = edit.Authors
	return b, edit, nil
}

func (f *fakeStore) only(t *testing.T) database.OperationChange {
	t.Helper()
	if len(f.order) != 1 {
		t.Fatalf("want exactly one ledger row, got %d", len(f.order))
	}
	return f.rows[f.order[0]]
}

func credit(book string, author, pos int) database.BookAuthor {
	return database.BookAuthor{BookID: book, AuthorID: author, Role: "author", Position: pos}
}

func TestStep_RefusesAnEmptyOperationID(t *testing.T) {
	f := newFake()
	if _, err := Step(f, "", &database.OperationChange{}); err == nil {
		t.Fatal("a row under no operation id must be refused")
	}
	if len(f.order) != 0 {
		t.Fatal("nothing may be written")
	}
}

func TestStep_VoidMarksTheRowVoided(t *testing.T) {
	f := newFake()
	void, err := Step(f, "op", &database.OperationChange{ChangeType: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := void(); err != nil {
		t.Fatal(err)
	}
	row := f.only(t)
	if !row.Voided || row.RevertedAt == nil || row.OperationID != "op" {
		t.Fatalf("voided row: %+v", row)
	}
}

func TestMoveCredits_ARefusedJunctionWriteVoidsItsRow(t *testing.T) {
	f := newFake()
	f.setErr = errors.New("refused")
	_, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1,
		Before: []database.BookAuthor{credit("b", 2, 0)}, After: []database.BookAuthor{credit("b", 1, 0)}})
	if err == nil {
		t.Fatal("want the set error")
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a refused write must be voided")
	}
}

func TestMoveCredits_APrimaryOnlyMoveCreditsTheKeepAuthor(t *testing.T) {
	f := &creditsFake{fakeStore: newFake()}
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	j := []database.BookAuthor{credit("b", 5, 0)}
	res, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: j, After: j, PrimaryBefore: &two})
	if err != nil || !res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	want := []database.BookAuthor{credit("b", 5, 0), credit("b", 1, 1)}
	if !undo.SameJunction(f.junction["b"], want) {
		t.Fatalf("the keep author must be credited with the primary: %+v", f.junction["b"])
	}
	var move undo.AuthorCreditsMove
	if err := json.Unmarshal([]byte(f.only(t).NewValue), &move); err != nil {
		t.Fatal(err)
	}
	if move.CreditsAfter == nil || !undo.SameJunction(*move.CreditsAfter, want) {
		t.Fatalf("the row must journal the credits written: %+v", move.CreditsAfter)
	}
}

func TestMoveCredits_APrimaryOnlyMoveLeavesTheJunctionUnwritten(t *testing.T) {
	f := newFake()
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	j := []database.BookAuthor{credit("b", 1, 0)}
	res, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: j, After: j, PrimaryBefore: &two})
	if err != nil || !res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	if f.sets != 0 {
		t.Fatal("an unchanged junction must not be rewritten")
	}
	if *f.books["b"].AuthorID != 1 {
		t.Fatal("primary not repointed")
	}
}

func TestMoveCredits_AFailedPrimaryRewritesTheRowAsUnchanged(t *testing.T) {
	f := newFake()
	two := 2
	f.modifyErr = errors.New("locked")
	res, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1,
		Before: []database.BookAuthor{credit("b", 2, 0)}, After: []database.BookAuthor{credit("b", 1, 0)}, PrimaryBefore: &two})
	if err != nil || res.PrimaryErr == nil {
		t.Fatalf("res %+v err %v", res, err)
	}
	var move undo.AuthorCreditsMove
	if err := json.Unmarshal([]byte(f.only(t).NewValue), &move); err != nil {
		t.Fatal(err)
	}
	if move.PrimaryChanged || move.PrimaryAfter != nil {
		t.Fatalf("the row still claims the primary moved: %+v", move)
	}
}

func TestMergeDelete_ARefusedDeleteVoidsItsRow(t *testing.T) {
	f := newFake()
	f.deleteErr = errors.New("refused")
	if _, err := MergeDelete(f, "op", database.Author{ID: 2, Name: "B"}, database.Author{ID: 1, Name: "A"}); err == nil {
		t.Fatal("want the delete error")
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a refused delete must be voided")
	}
}

func TestMergeDelete_JournalsTheAuthorAndItsAliases(t *testing.T) {
	f := newFake()
	f.aliases = []database.AuthorAlias{{ID: 9, AuthorID: 2, AliasName: "B. Pen", AliasType: "pen_name"}}
	if _, err := MergeDelete(f, "op", database.Author{ID: 2, Name: "B"}, database.Author{ID: 1, Name: "A"}); err != nil {
		t.Fatal(err)
	}
	row := f.only(t)
	if row.ChangeType != undo.ChangeTypeAuthorMergeDelete {
		t.Fatalf("row %+v", row)
	}
	var snap undo.AuthorSnapshot
	if err := json.Unmarshal([]byte(row.OldValue), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.ID != 2 || snap.Name != "B" {
		t.Fatalf("snapshot %+v", snap)
	}
}

// P5: the keep author is credited only when the primary repoint lands. A book
// another writer moved off FromID since the caller read it keeps its junction:
// crediting IntoID there would name an author its primary never moved to.
func TestMoveCredits_ABookWhosePrimaryMovedIsNotCreditedToTheKeepAuthor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store func(*fakeStore) CreditsStore
	}{
		{"atomic", func(f *fakeStore) CreditsStore { return &creditsFake{fakeStore: f} }},
		{"no capability", func(f *fakeStore) CreditsStore { return f }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			two, nine := 2, 9
			f.books["b"] = &database.Book{ID: "b", AuthorID: &nine}
			j := []database.BookAuthor{credit("b", 5, 0)}
			f.junction["b"] = slices.Clone(j)
			res, err := MoveCredits(tc.store(f), "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: j, After: j, PrimaryBefore: &two})
			if err != nil || res.PrimaryRepointed {
				t.Fatalf("res %+v err %v", res, err)
			}
			if slices.ContainsFunc(f.junction["b"], func(ba database.BookAuthor) bool { return ba.AuthorID == 1 }) {
				t.Fatalf("the keep author was credited on a book whose primary never moved to it: %+v", f.junction["b"])
			}
			if *f.books["b"].AuthorID != 9 {
				t.Fatal("the other writer's primary must stand")
			}
			var move undo.AuthorCreditsMove
			if err := json.Unmarshal([]byte(f.only(t).NewValue), &move); err != nil {
				t.Fatal(err)
			}
			if move.PrimaryChanged || move.CreditsAfter == nil || !undo.SameJunction(*move.CreditsAfter, j) {
				t.Fatalf("the row must journal what was stored and no repoint: %+v", move)
			}
		})
	}
}

// Without the capability the keep author is not appended at all (no unsafe
// two-step): the primary is still repointed, the junction left as the caller
// asked.
func TestMoveCredits_WithoutTheCapabilityTheKeepAuthorIsNotAppended(t *testing.T) {
	f := newFake()
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	j := []database.BookAuthor{credit("b", 5, 0)}
	f.junction["b"] = slices.Clone(j)
	res, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: j, After: j, PrimaryBefore: &two})
	if err != nil || !res.PrimaryRepointed {
		t.Fatalf("res %+v err %v", res, err)
	}
	if !undo.SameJunction(f.junction["b"], j) || f.sets != 0 {
		t.Fatalf("the junction must be left as asked: %+v (sets %d)", f.junction["b"], f.sets)
	}
	if *f.books["b"].AuthorID != 1 {
		t.Fatal("primary not repointed")
	}
}

// The atomic write is one write: refused, nothing moved, and its row is voided.
func TestMoveCredits_ARefusedAtomicWriteVoidsItsRow(t *testing.T) {
	f := &creditsFake{fakeStore: newFake(), creditsErr: errors.New("refused")}
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	j := []database.BookAuthor{credit("b", 2, 0)}
	f.junction["b"] = slices.Clone(j)
	if _, err := MoveCredits(f, "op", CreditMove{BookID: "b", FromID: 2, IntoID: 1, Before: j,
		After: []database.BookAuthor{credit("b", 1, 0)}, PrimaryBefore: &two}); err == nil {
		t.Fatal("want the write error")
	}
	if !f.only(t).Voided {
		t.Fatal("the row of a refused write must be voided")
	}
	if f.sets != 0 || !undo.SameJunction(f.junction["b"], j) || *f.books["b"].AuthorID != 2 {
		t.Fatalf("nothing may move: junction %+v primary %d", f.junction["b"], *f.books["b"].AuthorID)
	}
}
