// file: internal/plugins/maintenance/author_merge_undo_test.go
// version: 1.5.0
// guid: 4b8e2f17-a9c3-4d65-8e01-3c7f5a9d2b64
// last-edited: 2026-10-15

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
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
	require.Empty(t, report.CheckFailed, "the preflight refuses a row the revert restores")

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

// conjunction-repair renames an "& Name" row with no twin in place; the rename
// is journaled (author_rename) and undone with the op.
func TestAuthorConjunctionRepair_UndoRenamesTheSoloRowBack(t *testing.T) {
	st := newRelinkTrashedStore(t)
	solo := mustAuthorNamed(t, st, "& Undo Solo Author")
	p := New(&fakeDeps{store: st})
	require.NoError(t, p.runAuthorConjunctionRepair(context.Background(), conjRepairParams(false), &opIDReporter{id: "op-undo-conj-rename"}))
	renamed, err := st.GetAuthorByID(solo.ID)
	require.NoError(t, err)
	require.Equal(t, "Undo Solo Author", renamed.Name)

	rows, err := st.GetOperationChanges("op-undo-conj-rename")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, undo.ChangeTypeAuthorRename, rows[0].ChangeType)
	report, err := undo.PreflightUndoConflicts(st, "op-undo-conj-rename")
	require.NoError(t, err)
	require.Zero(t, report.NotRestorable)
	require.Equal(t, 1, report.TotalChanges)
	res, err := audiobooks.NewRevertService(st).RevertOperation("op-undo-conj-rename")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	require.Equal(t, 1, res.Restored)
	back, err := st.GetAuthorByID(solo.ID)
	require.NoError(t, err)
	require.Equal(t, "& Undo Solo Author", back.Name)
}

// A mixed author-id-repair run (dangling scalars AND a duplicate merge) undoes
// completely: the dangling primary goes back (author_id_repoint) and the merge
// is undone (author_merge_*).
func TestAuthorIDRepair_UndoOfAMixedRunRestoresBothPhases(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Mixed Author", "UNDO MIXED AUTHOR", 0)
	const dangling = 987654
	b, err := w.st.CreateBook(&database.Book{Title: "undo-mixed-dangling", FilePath: "/relink-trashed/undo-mixed-dangling",
		AuthorID: new(dangling), IsPrimaryVersion: new(true)})
	require.NoError(t, err)
	require.NoError(t, w.st.SetBookAuthors(b.ID, []database.BookAuthor{{BookID: b.ID, AuthorID: w.co.ID, Role: "author"}}))

	res := runRepair(t, New(&fakeDeps{store: w.st}), `{"dry_run":false}`)
	require.Equal(t, 1, res.DuplicateOutcomes[authorIDMergeMerged], "%+v", res.DuplicateChanges)
	require.Equal(t, 1, res.DanglingOutcomes["repointed"], "%+v", res.DanglingChanges)
	moved, err := w.st.GetBookByID(b.ID)
	require.NoError(t, err)
	require.Equal(t, w.co.ID, *moved.AuthorID)
	w.requireMerged(t)

	w.requireUndone(t, "op-id-repair")
	back, err := w.st.GetBookByID(b.ID)
	require.NoError(t, err)
	require.NotNil(t, back.AuthorID)
	require.Equal(t, dangling, *back.AuthorID, "the repair's repoint is undone")
}

// primaryRecordingStore records the denormalized Author each ModifyBook write
// leaves on a book, and can list every relink book twice (a book carrying two
// role rows for the author).
type primaryRecordingStore struct {
	*database.PebbleStore
	authors map[string]*database.Author
	twice   bool
}

func (s primaryRecordingStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	return s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
		err := fn(b)
		if err == nil {
			s.authors[id] = b.Author
		}
		return err
	})
}

func (s primaryRecordingStore) GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error) {
	books, err := s.PebbleStore.GetBooksByAuthorIDForRelinkCore(authorID)
	if s.twice {
		books = append(books, books...)
	}
	return books, err
}

// author-id-repair repoints a merged book's denormalized Author along with its
// AuthorID (CreditMove.Into), not just the id.
func TestAuthorIDRepair_RepointsTheDenormalizedAuthorToo(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Denorm Author", "UNDO DENORM AUTHOR", 0)
	st := primaryRecordingStore{PebbleStore: w.st, authors: map[string]*database.Author{}}
	p := New(&fakeDeps{store: st})
	ch, _, _ := p.authorIDMergeOne(st, false, "op-denorm", "undo denorm author", w.from, w.into, slog.Default())
	require.Equal(t, authorIDMergeMerged, ch.Outcome, "%+v", ch)
	got := st.authors[w.books[0]]
	require.NotNil(t, got, "the primary book's Author was not written")
	require.Equal(t, w.into.ID, got.ID)
	require.Equal(t, w.into.Name, got.Name)
}

// A book listed once per role row for the merged author is moved and journaled
// once: one credit row per distinct book, plus the delete row.
func TestMergeAuthorInto_ABookListedTwiceIsJournaledOnce(t *testing.T) {
	w := newMergeUndoWorld(t, "Undo Twice From", "Undo Twice Into", 0)
	st := primaryRecordingStore{PebbleStore: w.st, authors: map[string]*database.Author{}, twice: true}
	p := New(&fakeDeps{store: st})
	n, rows, err := p.mergeAuthorInto(context.Background(), w.from, w.into, false, "op-twice", slog.Default())
	require.NoError(t, err)
	require.Equal(t, len(w.books), n, "each distinct book is relinked once")
	require.Equal(t, len(w.books)+1, rows, "one credit row per distinct book and the delete row")
	ledger, err := w.st.GetOperationChanges("op-twice")
	require.NoError(t, err)
	require.Len(t, ledger, len(w.books)+1)
}

// A ghost book's credit move (no book row: author-id-repair moves its credit
// with no primary) reverts junction-only, and a second revert finds nothing
// left to fail.
func TestAuthorIDRepair_UndoRestoresAGhostBooksCredit(t *testing.T) {
	s := newRepairPebble(t)
	x := repairAuthor(t, s, "Jane Doe")
	y := repairAuthor(t, s, "jane twin")
	require.NoError(t, s.UpdateAuthorName(y.ID, "JANE DOE"))
	repairBook(t, s, "real", &x.ID, x.ID)
	require.NoError(t, s.SetBookAuthors("ghost", []database.BookAuthor{{BookID: "ghost", AuthorID: x.ID, Role: "author"}}))
	before, err := s.GetBookAuthors("ghost")
	require.NoError(t, err)
	res := runRepair(t, New(&fakeDeps{store: s}), `{"dry_run":false,"skip_dangling":true}`)
	require.Equal(t, 1, res.DuplicateOutcomes[authorIDMergeMerged])

	report, err := undo.PreflightUndoConflicts(s, "op-id-repair")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "the ghost row is restorable")
	for range 2 {
		rv, err := audiobooks.NewRevertService(s).RevertOperation("op-id-repair")
		if err != nil {
			require.Contains(t, err.Error(), "already been reverted")
			continue
		}
		require.Zero(t, rv.Failed, "%+v", rv)
	}
	after, err := s.GetBookAuthors("ghost")
	require.NoError(t, err)
	require.True(t, undo.SameJunction(before, after), "ghost credits: want %+v got %+v", before, after)
	back, err := s.GetAuthorByID(x.ID)
	require.NoError(t, err)
	require.Equal(t, x.ID, back.ID)
}

// Undoing an author_id_repoint puts back the book's denormalized Author exactly
// as the repair found it, not just the id: the repair writes the successor's
// object, and a revert that left it would keep the book displaying the
// successor's name under the restored id. Both pre-states are covered: no
// object at all (nil), and a stale object for the dangling id.
func TestAuthorIDRepair_UndoRestoresTheDenormalizedAuthorExactly(t *testing.T) {
	for _, tc := range []struct {
		name string
		pre  *database.Author
	}{
		{"nil", nil},
		{"stale object", &database.Author{ID: 424242, Name: "Synthetic Gone Writer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newRepairPebble(t)
			real := repairAuthor(t, s, "Real Person")
			dangling := 424242
			repairBook(t, s, "b1", &dangling, real.ID)
			_, err := s.ModifyBook("b1", func(b *database.Book) error {
				b.Author = tc.pre
				return nil
			})
			require.NoError(t, err)
			before, err := s.GetBookByID("b1")
			require.NoError(t, err)
			require.Equal(t, tc.pre, before.Author, "fixture")

			runRepair(t, New(&fakeDeps{store: s}), `{"dry_run":false,"skip_duplicates":true}`)
			moved, err := s.GetBookByID("b1")
			require.NoError(t, err)
			require.Equal(t, real.ID, *moved.AuthorID)
			require.NotNil(t, moved.Author)
			require.Equal(t, real.ID, moved.Author.ID, "the repair names the successor")

			report, err := undo.PreflightUndoConflicts(s, "op-id-repair")
			require.NoError(t, err)
			require.Empty(t, report.CheckFailed)
			rv, err := audiobooks.NewRevertService(s).RevertOperation("op-id-repair")
			require.NoError(t, err)
			require.Zero(t, rv.Failed, "%+v", rv)
			require.Equal(t, 1, rv.Restored, "%+v", rv)

			back, err := s.GetBookByID("b1")
			require.NoError(t, err)
			require.NotNil(t, back.AuthorID)
			require.Equal(t, dangling, *back.AuthorID)
			require.Equal(t, before.Author, back.Author, "the Author object is back exactly as it was")
		})
	}
}

// authorDriftStore changes a book's denormalized Author once, just before the
// first ModifyBook of that book: another writer landing between phase (a)'s
// read (which it journals) and its write.
type authorDriftStore struct {
	*database.PebbleStore
	drifted map[string]bool
}

func (s authorDriftStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if !s.drifted[id] {
		s.drifted[id] = true
		if _, err := s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
			b.Author = &database.Author{ID: 31337, Name: "Synthetic Drift Writer"}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// Phase (a) journals the Author object it read and writes only while the row
// still carries it: when another writer changed the object in between, the
// repoint is not made, its row is voided, and nothing is left for a revert.
func TestAuthorIDRepair_AnAuthorChangedSinceTheReadIsNotRepointed(t *testing.T) {
	s := newRepairPebble(t)
	real := repairAuthor(t, s, "Real Person")
	dangling := 424242
	repairBook(t, s, "b1", &dangling, real.ID)
	st := authorDriftStore{PebbleStore: s, drifted: map[string]bool{}}

	res := runRepair(t, New(&fakeDeps{store: st}), `{"dry_run":false,"skip_duplicates":true}`)
	require.Zero(t, res.DanglingOutcomes["repointed"], "%+v", res.DanglingChanges)
	require.Equal(t, 1, res.DanglingOutcomes["changed_since_scan"], "%+v", res.DanglingOutcomes)
	got, err := s.GetBookByID("b1")
	require.NoError(t, err)
	require.Equal(t, dangling, *got.AuthorID, "the book is left on its id")
	require.Equal(t, 31337, got.Author.ID, "the other writer's object stands")

	rows, err := s.GetOperationChanges("op-id-repair")
	require.NoError(t, err)
	seen := 0
	for _, r := range rows {
		if r.ChangeType == undo.ChangeTypeAuthorIDRepoint {
			seen++
			require.True(t, r.Voided, "the row of a repoint that did not land is voided: %+v", r)
		}
	}
	require.Equal(t, 1, seen, "the repoint was journaled before the write")
}

// author-id-repair's phase (a) repoints a dangling primary onto x, and its
// phase (b) then merges x away, moving the same book's primary again. The
// revert runs newest-first, so the merge row puts x back before the repoint
// row runs; the preflight must predict that rather than refuse the repoint
// row against the primary stored now.
func TestAuthorIDRepair_PreflightFollowsANewerMergeOfTheSameBook(t *testing.T) {
	s := newRepairPebble(t)
	x := repairAuthor(t, s, "Jane Doe")
	y := repairAuthor(t, s, "jane twin")
	require.NoError(t, s.UpdateAuthorName(y.ID, "JANE DOE"))
	dangling := 777777
	repairBook(t, s, "b1", &dangling, x.ID)

	res := runRepair(t, New(&fakeDeps{store: s}), `{"dry_run":false}`)
	require.Equal(t, 1, res.DanglingOutcomes["repointed"], "%+v", res.DanglingChanges)
	require.Equal(t, 1, res.DuplicateOutcomes[authorIDMergeMerged], "%+v", res.DuplicateChanges)
	rows, err := s.GetOperationChanges("op-id-repair")
	require.NoError(t, err)
	movedPrimary := false
	for _, r := range rows {
		if r.ChangeType == undo.ChangeTypeAuthorMergeCredits && r.BookID == "b1" {
			_, move, derr := undo.DecodeAuthorMergeCredits(r)
			require.NoError(t, derr)
			movedPrimary = move.PrimaryChanged
		}
	}
	require.True(t, movedPrimary, "fixture: the merge must move b1's primary a second time")

	report, err := undo.PreflightUndoConflicts(s, "op-id-repair")
	require.NoError(t, err)
	require.Empty(t, report.CheckFailed, "the repoint row is restorable once the newer merge row is")
	require.Equal(t, report.TotalChanges, report.Safe, "%+v", report)
	rv, err := audiobooks.NewRevertService(s).RevertOperation("op-id-repair")
	require.NoError(t, err)
	require.Zero(t, rv.Failed, "%+v", rv)
	back, err := s.GetBookByID("b1")
	require.NoError(t, err)
	require.NotNil(t, back.AuthorID)
	require.Equal(t, dangling, *back.AuthorID)
}
