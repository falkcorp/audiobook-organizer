// file: internal/merge/plan_virtual_holders_test.go
// version: 1.0.0
// guid: 2dc80dae-a233-411b-ba47-721acf3d06b6
// last-edited: 2026-10-25

package merge

import (
	"errors"
	"testing"

	ulid "github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pickBooks builds a bookOf for pickRowAtPath out of a map of fixed answers, and
// records which books were asked about.
type pickBooks struct {
	books map[string]*database.Book
	err   map[string]error
	asked []string
}

func (p *pickBooks) bookOf(id string) (*database.Book, error) {
	p.asked = append(p.asked, id)
	if e := p.err[id]; e != nil {
		return nil, e
	}
	return p.books[id], nil
}

func liveBook(id string) *database.Book { return &database.Book{ID: id} }

func trashedBook(id string) *database.Book {
	t := true
	return &database.Book{ID: id, MarkedForDeletion: &t}
}

// pickRowAtPath was liveRowAtPath, which took the smallest (BookID, ID) of the
// live rows, so with several live holders it moved the oldest book's row to the
// target and left that book, which was not part of the combine, without a file
// row. Its table changed with it: the smallest-ID rule survives only among
// trashed holders, where nobody is using the copy.
func TestPickRowAtPath(t *testing.T) {
	boom := errors.New("injected book read error")
	rows := func(rs ...database.BookFile) []database.BookFile { return rs }
	tests := []struct {
		name     string
		rows     []database.BookFile
		arriving map[string]bool
		books    pickBooks
		wantRow  string // row id picked
		arrives  bool
		warning  bool
		wantErr  error
		asked    []string // books bookOf must have been asked about, in order
	}{
		{name: "no rows", rows: nil},
		{
			name:    "the target's own row wins over everything, and no book is read",
			rows:    rows(database.BookFile{ID: "r0", BookID: "b1"}, database.BookFile{ID: "r1", BookID: "tgt"}),
			wantRow: "r1",
		},
		{
			name:     "a row of a book the combine moves anyway: nothing is planned",
			rows:     rows(database.BookFile{ID: "r0", BookID: "b1"}, database.BookFile{ID: "r1", BookID: "abs"}),
			arriving: map[string]bool{"abs": true},
			arrives:  true,
		},
		{
			name:     "the target's own row still beats an arriving one",
			rows:     rows(database.BookFile{ID: "r0", BookID: "abs"}, database.BookFile{ID: "r1", BookID: "tgt"}),
			arriving: map[string]bool{"abs": true},
			wantRow:  "r1",
		},
		{
			name:    "a retired row is never picked",
			rows:    rows(database.BookFile{ID: "r0", BookID: "b1", Retired: true}),
			wantRow: "",
		},
		{
			name:    "one live holder: its row is the one to move (#1549)",
			rows:    rows(database.BookFile{ID: "r0", BookID: "b1"}),
			books:   pickBooks{books: map[string]*database.Book{"b1": liveBook("b1")}},
			wantRow: "r0",
			asked:   []string{"b1"},
		},
		{
			name: "a trashed holder is preferred over a live one even when the live one is older",
			rows: rows(database.BookFile{ID: "r0", BookID: "b1"}, database.BookFile{ID: "r1", BookID: "b2"}),
			books: pickBooks{books: map[string]*database.Book{
				"b1": liveBook("b1"), "b2": trashedBook("b2"),
			}},
			wantRow: "r1",
		},
		{
			name: "several trashed holders: the smallest (BookID, ID), whatever the order of the rows",
			rows: rows(database.BookFile{ID: "r3", BookID: "b2"}, database.BookFile{ID: "r2", BookID: "b2"}, database.BookFile{ID: "r9", BookID: "b3"}),
			books: pickBooks{books: map[string]*database.Book{
				"b2": trashedBook("b2"), "b3": trashedBook("b3"),
			}},
			wantRow: "r2",
		},
		{
			name: "two live holders: nothing is planned, and a warning says why",
			rows: rows(database.BookFile{ID: "r0", BookID: "b1"}, database.BookFile{ID: "r1", BookID: "b2"}),
			books: pickBooks{books: map[string]*database.Book{
				"b1": liveBook("b1"), "b2": liveBook("b2"),
			}},
			warning: true,
		},
		{
			name: "two rows of ONE live book are still two rows: nothing is planned",
			rows: rows(database.BookFile{ID: "r0", BookID: "b1"}, database.BookFile{ID: "r1", BookID: "b1"}),
			books: pickBooks{books: map[string]*database.Book{
				"b1": liveBook("b1"),
			}},
			warning: true,
			asked:   []string{"b1"},
		},
		{
			name:    "a row whose book is gone is not moved (undo could not move it back) and not duplicated",
			rows:    rows(database.BookFile{ID: "r0", BookID: "gone"}),
			books:   pickBooks{books: map[string]*database.Book{}},
			warning: true,
		},
		{
			name:    "a book read error is an error, never 'not trashed'",
			rows:    rows(database.BookFile{ID: "r0", BookID: "b1"}),
			books:   pickBooks{err: map[string]error{"b1": boom}},
			wantErr: boom,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pick, err := pickRowAtPath(tc.rows, "tgt", tc.arriving, tc.books.bookOf)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.arrives, pick.arrives)
			assert.Equal(t, tc.warning, pick.warning != "", "warning: %q", pick.warning)
			if tc.wantRow == "" {
				assert.Nil(t, pick.row)
			} else {
				require.NotNil(t, pick.row)
				assert.Equal(t, tc.wantRow, pick.row.ID)
			}
			if tc.asked != nil {
				assert.Equal(t, tc.asked, tc.books.asked)
			}
			if tc.name == "the target's own row wins over everything, and no book is read" {
				assert.Empty(t, tc.books.asked)
			}
		})
	}
}

// seedHolder creates a book and, if path is not empty, a live row of its own at
// path. Books are created in call order, so an earlier call has the smaller id.
func seedHolder(t *testing.T, store database.Store, title, path string) (*database.Book, string) {
	t.Helper()
	b, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: title, Format: "mp3", FilePath: "/tmp/pvh-own-" + ulid.Make().String() + ".mp3"})
	require.NoError(t, err)
	rowID := ulid.Make().String()
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: rowID, BookID: b.ID, FilePath: path, Format: "mp3"}))
	return b, rowID
}

// With a live bystander (the older book, so the smaller id) and a trashed copy of
// the file both holding the path, the plan takes the trashed copy's row. The old
// smallest-BookID rule took the bystander's, leaving a live book without its file.
func TestPlanVirtualFile_PrefersATrashedHoldersRowOverALiveBystanders(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)
	path := "/tmp/pvh-trashed-pref.mp3"
	bystander, _ := seedHolder(t, store, "Live bystander", path)
	trashed, trashedRow := seedHolder(t, store, "Trashed copy", path)
	require.NoError(t, SoftDeleteBook(store, trashed.ID))
	require.Less(t, bystander.ID, trashed.ID, "the bystander must be the older book, or this test cannot tell the rules apart")
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: path})
	require.NoError(t, err)

	plan, err := ms.planVirtualFile(target, target.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, plan.existing, "the trashed copy's row is reattached")
	assert.Equal(t, trashedRow, plan.existing.ID)
	assert.Equal(t, trashed.ID, plan.fm.FromBookID)
	assert.Nil(t, plan.create)
	assert.Empty(t, plan.warning)
}

// Two live books hold the path: the plan is empty, with a warning, and neither
// holder is touched or duplicated.
func TestPlanVirtualFile_TwoLiveHoldersPlansNothingAndWarns(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)
	path := "/tmp/pvh-two-live.mp3"
	seedHolder(t, store, "Holder one", path)
	seedHolder(t, store, "Holder two", path)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: path})
	require.NoError(t, err)

	plan, err := ms.planVirtualFile(target, target.ID, nil)
	require.NoError(t, err)
	assert.Nil(t, plan.existing, "no live bystander's row may be taken")
	assert.Nil(t, plan.create, "and no second row may be put on the file")
	assert.Empty(t, plan.fm.FileID)
	assert.False(t, plan.attached)
	assert.Contains(t, plan.warning, "path held by 2 rows")
	assert.Contains(t, plan.warning, target.ID)
}

// The same refusal through a whole combine: it still succeeds, the warning is on
// the PERSISTED journal, and both holders keep their rows.
func TestCombineBooks_PathHeldByTwoLiveBooksIsJournaledAndLeavesThemTheirRows(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvh-combine-two-live.mp3"
	h1, row1 := seedHolder(t, store, "Holder one", path)
	h2, row2 := seedHolder(t, store, "Holder two", path)
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Survivor", Format: "mp3", FilePath: path})
	require.NoError(t, err)
	absorbed, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Absorbed", Format: "mp3", FilePath: "/tmp/pvh-combine-two-live-abs.mp3"})
	require.NoError(t, err)

	ms := NewService(store)
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	require.NoError(t, err)

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, CombineJournalApplied, j.Status)
	require.Len(t, j.Warnings, 1, "the ambiguity is recorded once, on the journal read back from the store")
	assert.Contains(t, j.Warnings[0], "path held by 2 rows")
	assert.Empty(t, j.SurvivorFiles, "no row was moved or created for the survivor's own file")

	assert.Contains(t, viewFiles(t, store, h1.ID), row1, "the first holder keeps its row")
	assert.Contains(t, viewFiles(t, store, h2.ID), row2, "the second holder keeps its row")
	for _, f := range viewFiles(t, store, survivor.ID) {
		assert.NotEqual(t, path, f.Path, "the survivor must not get a second row on the held file")
	}
}

// A rowless primary whose FilePath is the path of a row an ABSORBED book owns.
// The survivor's own attach used to run first and move that row, so the bulk move
// then could not find it under its source ("file not found") and stopped the
// combine after its journal and its first write: every such combine ended in a
// CombineIncompleteError telling the user to revert.
func TestCombineBooks_RowlessPrimaryAtAnAbsorbedBooksRowPathCombinesCleanly(t *testing.T) {
	store := setupTestStore(t)
	path := "/tmp/pvh-primary-at-absorbed-row.mp3"
	survivor, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Legacy record", Format: "mp3", FilePath: path})
	require.NoError(t, err)
	absorbed, rowID := seedHolder(t, store, "Record with the row", path)
	before := viewFiles(t, store, survivor.ID, absorbed.ID)
	require.Len(t, before, 1)

	ms := NewService(store)
	res, err := ms.CombineBooks([]string{survivor.ID, absorbed.ID}, survivor.ID, nil)
	var incomplete *CombineIncompleteError
	require.False(t, errors.As(err, &incomplete), "the combine must not stop after its journal: %v", err)
	require.NoError(t, err)
	assert.Equal(t, 1, res.FilesMoved, "the row is moved once, by the bulk move")

	files, err := store.GetBookFiles(survivor.ID)
	require.NoError(t, err)
	require.Len(t, files, 1, "the survivor owns the one row, with no second row on the file")
	assert.Equal(t, rowID, files[0].ID)
	assert.Equal(t, path, files[0].FilePath)
	left, err := store.GetBookFiles(absorbed.ID)
	require.NoError(t, err)
	assert.Empty(t, left)

	j, err := ms.GetCombineJournal(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, CombineJournalApplied, j.Status)
	assert.Empty(t, j.SurvivorFiles, "the survivor's own file arrives with the bulk move; nothing is journaled for it twice")
	require.Len(t, j.Absorbed, 1)
	require.Len(t, j.Absorbed[0].Files, 1)
	assert.Equal(t, rowID, j.Absorbed[0].Files[0].FileID)

	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err)
	assert.Equal(t, before, viewFiles(t, store, survivor.ID, absorbed.ID), "undo puts the row back on its book")
}

// A row of a book the combine is moving is not planned at all, so the survivor's
// own plan no longer races the bulk move for it.
func TestPlanVirtualFile_RowOfAnArrivingBookPlansNothing(t *testing.T) {
	store := setupTestStore(t)
	ms := NewService(store)
	path := "/tmp/pvh-arriving.mp3"
	arriving, _ := seedHolder(t, store, "Absorbed with a row", path)
	target, err := store.CreateBook(&database.Book{ID: ulid.Make().String(), Title: "Target", FilePath: path})
	require.NoError(t, err)

	plan, err := ms.planVirtualFile(target, target.ID, map[string]bool{arriving.ID: true})
	require.NoError(t, err)
	assert.Equal(t, virtualFilePlan{}, plan)

	plan, err = ms.planVirtualFile(target, target.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, plan.existing, "outside a combine the row is the target's to move, as before")
}
