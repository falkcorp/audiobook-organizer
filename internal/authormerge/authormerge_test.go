// file: internal/authormerge/authormerge_test.go
// version: 1.0.0
// guid: 8a1e4c62-9d37-4b05-b6f8-2e7a5c0d9f13
// last-edited: 2026-10-15

package authormerge

import (
	"encoding/json"
	"errors"
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

func TestMoveCredits_APrimaryOnlyMoveLeavesTheJunctionUnwritten(t *testing.T) {
	f := newFake()
	two := 2
	f.books["b"] = &database.Book{ID: "b", AuthorID: &two}
	j := []database.BookAuthor{credit("b", 5, 0)}
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
