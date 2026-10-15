// file: internal/plugins/maintenance/author_merge_undo_test.go
// version: 1.0.0
// guid: 4b8e2f17-a9c3-4d65-8e01-3c7f5a9d2b64
// last-edited: 2026-10-14

package maintenance

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Every maintenance author merge journals the rows the revert engine reverses
// (authormerge), so undoing the op brings back the merged author, its pen name,
// and every book's credits and primary exactly as they were. Each test runs one
// op for real against Pebble, preflights and reverts it, and checks all three.

// mergeUndoWorld: from (with a pen name) is merged into into. from is the
// primary of one book and the second credit of another, whose primary is co.
type mergeUndoWorld struct {
	st             *database.PebbleStore
	from, into, co database.Author
	alias          string
	books          []string
	credits        map[string][]database.BookAuthor
	primaries      map[string]int
}

// newMergeUndoWorld creates from under fromName and into under intoName. into
// also gets intoBooks books of its own, so a merge that keeps the author with
// the most books keeps into.
func newMergeUndoWorld(t *testing.T, fromName, intoName string, intoBooks int) *mergeUndoWorld {
	t.Helper()
	st := newRelinkTrashedStore(t)
	w := &mergeUndoWorld{st: st, alias: "U. M. Pen", credits: map[string][]database.BookAuthor{}, primaries: map[string]int{}}
	w.from = *mustAuthorNamed(t, st, fromName)
	w.into = *mustAuthorNamed(t, st, intoName)
	co, err := st.CreateAuthor("Undo Merge Co Author")
	require.NoError(t, err)
	w.co = *co
	_, err = st.CreateAuthorAlias(w.from.ID, w.alias, "pen_name")
	require.NoError(t, err)

	w.books = append(w.books,
		mkRelinkTrashedBook(t, st, "undo-merge-primary", w.from.ID, false),
		mkRelinkTrashedBook(t, st, "undo-merge-second", w.co.ID, false, w.from.ID))
	for i := range intoBooks {
		mkRelinkTrashedBook(t, st, "undo-merge-into-"+string(rune('a'+i)), w.into.ID, false)
	}
	for _, id := range w.books {
		credits, err := st.GetBookAuthors(id)
		require.NoError(t, err)
		w.credits[id] = credits
		b, err := st.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.AuthorID)
		w.primaries[id] = *b.AuthorID
	}
	return w
}

// mustAuthorNamed creates an author and then names it, so a name the creation
// gate would refuse (the "& Name" and numbered rows these ops repair) can be
// set up the way they arrived in a library.
func mustAuthorNamed(t *testing.T, st *database.PebbleStore, name string) *database.Author {
	t.Helper()
	a, err := st.CreateAuthor("undo merge placeholder " + name)
	require.NoError(t, err)
	require.NoError(t, st.UpdateAuthorName(a.ID, name))
	a.Name = name
	return a
}

func (w *mergeUndoWorld) requireMerged(t *testing.T) {
	t.Helper()
	via, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.NotNil(t, via)
	require.Equal(t, w.into.ID, via.ID, "the merged author's id redirects to the survivor")
	pen, err := w.st.FindAuthorByAlias(w.alias)
	require.NoError(t, err)
	require.NotNil(t, pen)
	require.Equal(t, w.into.ID, pen.ID, "the merged author's pen name moved to the survivor")
	for _, id := range w.books {
		credits, err := w.st.GetBookAuthors(id)
		require.NoError(t, err)
		for _, c := range credits {
			require.NotEqual(t, w.from.ID, c.AuthorID, "book %s still credits the merged author", id)
		}
	}
}

// requireUndone preflights and reverts opID, then checks the author, its alias
// and every book's credits and primary are back.
func (w *mergeUndoWorld) requireUndone(t *testing.T, opID string) {
	t.Helper()
	rows, err := w.st.GetOperationChanges(opID)
	require.NoError(t, err)
	require.NotEmpty(t, rows, "the merge journaled nothing")
	for _, r := range rows {
		require.Empty(t, undo.NotRestorableLabel(r), "%s row is not one the revert can reverse", r.ChangeType)
	}
	report, err := undo.PreflightUndoConflicts(w.st, opID)
	require.NoError(t, err)
	require.Zero(t, report.NotRestorable)
	require.Equal(t, len(rows), report.TotalChanges)

	res, err := audiobooks.NewRevertService(w.st).RevertOperation(opID)
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	require.Equal(t, len(rows), res.Restored)

	back, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.NotNil(t, back)
	require.Equal(t, w.from.ID, back.ID, "the merged author is back under its own id")
	require.Equal(t, w.from.Name, back.Name)
	pen, err := w.st.FindAuthorByAlias(w.alias)
	require.NoError(t, err)
	require.NotNil(t, pen)
	require.Equal(t, w.from.ID, pen.ID, "the pen name names the restored author again")
	intoAliases, err := w.st.GetAuthorAliases(w.into.ID)
	require.NoError(t, err)
	require.Empty(t, intoAliases, "the survivor gives the carried alias back")
	for _, id := range w.books {
		credits, err := w.st.GetBookAuthors(id)
		require.NoError(t, err)
		require.True(t, undo.SameJunction(w.credits[id], credits), "book %s credits: want %+v, got %+v", id, w.credits[id], credits)
		b, err := w.st.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.AuthorID)
		require.Equal(t, w.primaries[id], *b.AuthorID, "book %s primary", id)
	}
}

func TestAuthorDuplicateMerge_UndoRestoresTheMergedRow(t *testing.T) {
	// Case twins; into has the most books, so it is kept.
	w := newMergeUndoWorld(t, "Undo Twin Author", "UNDO TWIN AUTHOR", 3)
	p := New(&fakeDeps{store: w.st})
	raw := json.RawMessage(`{"names":["Undo Twin Author"],"dry_run":false}`)
	require.NoError(t, p.runAuthorDuplicateMerge(context.Background(), raw, &opIDReporter{id: "op-undo-dup"}))
	w.requireMerged(t)
	w.requireUndone(t, "op-undo-dup")
}

func TestAuthorConjunctionRepair_UndoRestoresTheMergedRow(t *testing.T) {
	w := newMergeUndoWorld(t, "& Undo Conj Author", "Undo Conj Author", 0)
	p := New(&fakeDeps{store: w.st})
	require.NoError(t, p.runAuthorConjunctionRepair(context.Background(), conjRepairParams(false), &opIDReporter{id: "op-undo-conj"}))
	w.requireMerged(t)
	w.requireUndone(t, "op-undo-conj")
}

func TestAuthorStripMerge_UndoRestoresTheMergedRow(t *testing.T) {
	w := newMergeUndoWorld(t, "001-147 Undo Strip Author", "Undo Strip Author", 0)
	p := New(&fakeDeps{store: w.st})
	rep := &summaryReporter{}
	require.NoError(t, p.runAuthorStripMerge(context.Background(), json.RawMessage(`{"apply":true}`), rep))
	w.requireMerged(t)
	w.requireUndone(t, rep.OpID())
}

func TestAuthorIDRepair_UndoRestoresTheMergedDuplicate(t *testing.T) {
	// The canonical row is the one the name index resolves: into, named last.
	w := newMergeUndoWorld(t, "Undo Index Author", "UNDO INDEX AUTHOR", 0)
	held, err := w.st.GetAuthorByName("undo index author")
	require.NoError(t, err)
	require.Equal(t, w.into.ID, held.ID, "fixture: the index must resolve into")
	p := New(&fakeDeps{store: w.st})
	res := runRepair(t, p, `{"dry_run":false,"skip_dangling":true}`)
	require.Equal(t, 1, res.DuplicateOutcomes[authorIDMergeMerged], "%+v", res.DuplicateChanges)
	w.requireMerged(t)
	w.requireUndone(t, "op-id-repair")
}

// An apply with no operation id merges nothing: its rows would belong to no
// operation a revert can name.
func TestMergeAuthorInto_RefusesAnApplyWithNoOperationID(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo NoOp From", "Undo NoOp Into", 0)
	p := New(&fakeDeps{store: w.st})
	n, rows, err := p.mergeAuthorInto(context.Background(), w.from, w.into, false, "", nil)
	require.Error(t, err)
	require.Zero(t, n)
	require.Zero(t, rows)
	still, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, still.ID, "the author was not deleted")
	for _, id := range w.books {
		credits, err := w.st.GetBookAuthors(id)
		require.NoError(t, err)
		require.True(t, undo.SameJunction(w.credits[id], credits), "book %s was moved", id)
	}
}
