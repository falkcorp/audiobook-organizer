// file: internal/audiobooks/revert_opswriter_test.go
// version: 1.0.0
// guid: 12b59df3-10e9-4775-b61e-88a103e7c9ff
// last-edited: 2026-10-30

package audiobooks

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// owFx is a real store, one book with one file row, and a live v3 Writer
// journaling under opID, so every row a revert test reverts was written by the
// code that writes it in production.
type owFx struct {
	st   *database.PebbleStore
	book *database.Book
	file *database.BookFile
	w    *opswriter.Writer
	opID string
}

func newOWFx(t *testing.T) *owFx {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	dir := t.TempDir()
	book, err := st.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: filepath.Join(dir, "year-one")})
	require.NoError(t, err)
	f := &database.BookFile{BookID: book.ID, FilePath: filepath.Join(dir, "year-one", "01.mp3"),
		FileHash: "hash-year-one", TrackNumber: 1, Title: "Gotham Returns", Format: "mp3"}
	require.NoError(t, st.CreateBookFile(f))
	opID := "op-ow-revert"
	w := opswriter.New(opswriter.Config{
		OpID: opID, Mode: ops.Live, Files: st, Authors: st, Journal: st,
		History: opswriter.NewBookHistory(st, "ow-test", "metadata_update", opswriter.BatchPrefix(opID), nil),
	})
	return &owFx{st: st, book: book, file: f, w: w, opID: opID}
}

func (fx *owFx) row(t *testing.T) *database.BookFile {
	t.Helper()
	f, err := fx.st.GetBookFileByID(fx.book.ID, fx.file.ID)
	require.NoError(t, err)
	require.NotNil(t, f)
	return f
}

func (fx *owFx) modifyFile(t *testing.T, fn func(*database.BookFile)) {
	t.Helper()
	_, err := fx.w.ModifyBookFile(context.Background(), fx.book.ID, fx.file.ID, func(f *database.BookFile) error {
		fn(f)
		return nil
	})
	require.NoError(t, err)
}

// assertAllMarkedReverted: the revert journaled every row of the operation.
func (fx *owFx) assertAllMarkedReverted(t *testing.T) {
	t.Helper()
	rows, err := fx.st.GetOperationChanges(fx.opID)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, c := range rows {
		assert.NotNil(t, c.RevertedAt, "row %s (%s %s) is marked reverted", c.ID, c.ChangeType, c.FieldName)
	}
}

func TestRevertBookFileModify_RestoresEveryChangedField(t *testing.T) {
	fx := newOWFx(t)
	fx.modifyFile(t, func(f *database.BookFile) { f.Title = "The Dark Knight Returns"; f.TrackNumber = 7 })
	require.Equal(t, 7, fx.row(t).TrackNumber)

	rows, err := fx.st.GetOperationChanges(fx.opID)
	require.NoError(t, err)
	require.Len(t, rows, 2, "one row per changed field")
	for _, c := range rows {
		assert.Equal(t, database.ChangeTypeBookFileModify, c.ChangeType)
		assert.True(t, undo.IsRestorable(c), "the revert engine reverses %s", c.FieldName)
	}

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Restored)
	assert.Zero(t, res.Failed)
	got := fx.row(t)
	assert.Equal(t, "Gotham Returns", got.Title)
	assert.Equal(t, 1, got.TrackNumber)
	assert.Equal(t, "hash-year-one", got.FileHash, "a field the operation did not touch is untouched")
	fx.assertAllMarkedReverted(t)
}

// A field the Writer cleared (its JSON side is empty: omitted) and one it set
// from empty both come back.
func TestRevertBookFileModify_RestoresAbsentSides(t *testing.T) {
	fx := newOWFx(t)
	fx.modifyFile(t, func(f *database.BookFile) { f.Title = ""; f.Codec = "aac" })
	require.Empty(t, fx.row(t).Title)

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Restored)
	got := fx.row(t)
	assert.Equal(t, "Gotham Returns", got.Title)
	assert.Empty(t, got.Codec, "a field the Writer set from empty is cleared again")
}

// One field written since is left alone and reported; the other still reverts.
func TestRevertBookFileModify_DriftRefusesOnlyThatField(t *testing.T) {
	fx := newOWFx(t)
	fx.modifyFile(t, func(f *database.BookFile) { f.Title = "The Dark Knight Returns"; f.TrackNumber = 7 })
	_, err := fx.st.ModifyBookFile(fx.book.ID, fx.file.ID, func(f *database.BookFile) error {
		f.Title = "Knightfall"
		return nil
	})
	require.NoError(t, err)

	report, err := preflightUndoConflicts(fx.st, fx.opID)
	require.NoError(t, err)
	require.Len(t, report.CheckFailed, 1, "the preflight predicts the refusal: %+v", report)
	assert.Equal(t, undo.ReasonChangedSince, report.CheckFailed[0].Reason)

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.Error(t, err, "a refused row fails the revert")
	require.NotNil(t, res)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 1, res.ChangedSince)
	assert.Equal(t, 1, res.Restored)
	got := fx.row(t)
	assert.Equal(t, "Knightfall", got.Title, "the later write is kept")
	assert.Equal(t, 1, got.TrackNumber, "the untouched field reverted")

	rows, err := fx.st.GetOperationChanges(fx.opID)
	require.NoError(t, err)
	for _, c := range rows {
		if c.FieldName == "file.title:"+fx.file.ID {
			assert.Nil(t, c.RevertedAt, "the refused row stays unmarked")
		} else {
			assert.NotNil(t, c.RevertedAt)
		}
	}
}

// Two writes of one field: the newer row reverts first, so the field ends at
// the oldest value. Rows of one write share a timestamp, so only two writes
// prove the order.
func TestRevertBookFileModify_SameFieldTwiceRevertsNewestFirst(t *testing.T) {
	fx := newOWFx(t)
	fx.modifyFile(t, func(f *database.BookFile) { f.Title = "Hush" })
	fx.modifyFile(t, func(f *database.BookFile) { f.Title = "Knightfall" })

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.NoError(t, err)
	assert.Equal(t, 2, res.Restored)
	assert.Zero(t, res.Failed)
	assert.Equal(t, "Gotham Returns", fx.row(t).Title)
	fx.assertAllMarkedReverted(t)
}

func TestRevertBookFileModify_AlreadyRestoredWritesNothing(t *testing.T) {
	fx := newOWFx(t)
	c := &database.OperationChange{ID: "chg-ow-already", OperationID: "op-ow-already", BookID: fx.book.ID,
		ChangeType: database.ChangeTypeBookFileModify, FieldName: "file.title:" + fx.file.ID,
		OldValue: `"Gotham Returns"`, NewValue: `"Hush"`}
	require.NoError(t, fx.st.CreateOperationChange(c))
	err := NewRevertService(fx.st).revertBookFileModify(c)
	require.ErrorIs(t, err, undo.ErrAlreadyRestored)
}

func (fx *owFx) authors(t *testing.T) []database.BookAuthor {
	t.Helper()
	got, err := fx.st.GetBookAuthors(fx.book.ID)
	require.NoError(t, err)
	return got
}

func newOWAuthors(t *testing.T, st *database.PebbleStore, names ...string) []*database.Author {
	t.Helper()
	var out []*database.Author
	for _, n := range names {
		a, err := st.CreateAuthor(n)
		require.NoError(t, err)
		out = append(out, a)
	}
	return out
}

func TestRevertBookAuthorsModify_RestoresCreditListInOrder(t *testing.T) {
	fx := newOWFx(t)
	as := newOWAuthors(t, fx.st, "Bruce Wayne", "Alfred Pennyworth", "Selina Kyle")
	start := []database.BookAuthor{
		{AuthorID: as[0].ID, Role: "author", Position: 0},
		{AuthorID: as[1].ID, Role: "co-author", Position: 1},
	}
	require.NoError(t, fx.st.SetBookAuthors(fx.book.ID, start))
	want := fx.authors(t)
	require.Len(t, want, 2)

	_, err := fx.w.ModifyBookAuthors(context.Background(), fx.book.ID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		// Selina first, Alfred dropped, Bruce demoted: the order changes too.
		return []database.BookAuthor{
			{AuthorID: as[2].ID, Role: "author", Position: 0},
			{AuthorID: as[0].ID, Role: "co-author", Position: 1},
		}, nil
	})
	require.NoError(t, err)
	require.NotEqual(t, want, fx.authors(t))

	rows, err := fx.st.GetOperationChanges(fx.opID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, undo.IsRestorable(rows[0]))

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Restored)
	assert.Equal(t, want, fx.authors(t), "the credit list is back exactly, order and roles included")
	fx.assertAllMarkedReverted(t)

	// No author row was deleted: Selina Kyle now has no credit and is left for
	// maintenance.purge-empty-authors.
	got, err := fx.st.GetAuthorByID(as[2].ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, as[2].ID, got.ID)
}

// A list that had no credits comes back empty.
func TestRevertBookAuthorsModify_FromNoCredits(t *testing.T) {
	fx := newOWFx(t)
	as := newOWAuthors(t, fx.st, "Harvey Dent")
	_, err := fx.w.ModifyBookAuthors(context.Background(), fx.book.ID, func([]database.BookAuthor) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{AuthorID: as[0].ID, Role: "author"}}, nil
	})
	require.NoError(t, err)
	require.Len(t, fx.authors(t), 1)

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Restored)
	assert.Empty(t, fx.authors(t))
}

func TestRevertBookAuthorsModify_DriftIsRefused(t *testing.T) {
	fx := newOWFx(t)
	as := newOWAuthors(t, fx.st, "Bruce Wayne", "Dick Grayson")
	require.NoError(t, fx.st.SetBookAuthors(fx.book.ID, []database.BookAuthor{{AuthorID: as[0].ID, Role: "author"}}))
	_, err := fx.w.ModifyBookAuthors(context.Background(), fx.book.ID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		return append(cur, database.BookAuthor{AuthorID: as[1].ID, Role: "co-author"}), nil
	})
	require.NoError(t, err)
	// Someone credits a narrator-turned-author by hand since.
	extra := newOWAuthors(t, fx.st, "Barbara Gordon")
	_, err = fx.st.ModifyBookAuthors(fx.book.ID, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		return append(cur, database.BookAuthor{AuthorID: extra[0].ID, Role: "co-author"}), nil
	})
	require.NoError(t, err)
	drifted := fx.authors(t)

	report, err := preflightUndoConflicts(fx.st, fx.opID)
	require.NoError(t, err)
	require.Len(t, report.CheckFailed, 1, "%+v", report)

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.Error(t, err)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 1, res.ChangedSince)
	assert.Equal(t, drifted, fx.authors(t), "the hand edit is kept")
}

// An author the revert would credit again that was deleted since refuses the
// row, in the preflight and in the revert.
func TestRevertBookAuthorsModify_DeletedAuthorRefuses(t *testing.T) {
	fx := newOWFx(t)
	as := newOWAuthors(t, fx.st, "Oswald Cobblepot", "Edward Nygma")
	require.NoError(t, fx.st.SetBookAuthors(fx.book.ID, []database.BookAuthor{{AuthorID: as[0].ID, Role: "author"}}))
	_, err := fx.w.ModifyBookAuthors(context.Background(), fx.book.ID, func([]database.BookAuthor) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{AuthorID: as[1].ID, Role: "author"}}, nil
	})
	require.NoError(t, err)
	require.NoError(t, fx.st.DeleteAuthor(as[0].ID))
	after := fx.authors(t)

	report, err := preflightUndoConflicts(fx.st, fx.opID)
	require.NoError(t, err)
	require.Len(t, report.CheckFailed, 1, "%+v", report)

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.Error(t, err)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, after, fx.authors(t), "no credit is written for a deleted author")
}

// A repoint and field edits in one operation all revert.
func TestRevertMixedOperation_RepointAndModifyRows(t *testing.T) {
	fx := newOWFx(t)
	was := database.BookFileLocationOf(fx.row(t))
	to := database.BookFileLocation{Path: filepath.Join(filepath.Dir(fx.file.FilePath), "01-moved.mp3"), Missing: false}
	require.NoError(t, fx.w.RepointBookFile(context.Background(), fx.book.ID, fx.file.ID, was, to))
	fx.modifyFile(t, func(f *database.BookFile) { f.Title = "Arkham Asylum"; f.TrackNumber = 4 })
	as := newOWAuthors(t, fx.st, "Jason Todd")
	_, err := fx.w.ModifyBookAuthors(context.Background(), fx.book.ID, func([]database.BookAuthor) ([]database.BookAuthor, error) {
		return []database.BookAuthor{{AuthorID: as[0].ID, Role: "author"}}, nil
	})
	require.NoError(t, err)

	rows, err := fx.st.GetOperationChanges(fx.opID)
	require.NoError(t, err)
	require.Len(t, rows, 4, "repoint + two fields + credits")

	res, err := NewRevertService(fx.st).RevertOperation(fx.opID)
	require.NoError(t, err)
	assert.Equal(t, 4, res.Restored)
	assert.Zero(t, res.Failed)
	got := fx.row(t)
	assert.Equal(t, was.Path, got.FilePath)
	assert.Equal(t, "Gotham Returns", got.Title)
	assert.Equal(t, 1, got.TrackNumber)
	assert.Empty(t, fx.authors(t))
	fx.assertAllMarkedReverted(t)
}
