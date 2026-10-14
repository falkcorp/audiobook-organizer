// file: internal/server/author_merge_undo_test.go
// version: 1.0.1
// guid: d1a47c63-8e29-4f50-b6a3-90e5c2f1d784
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// An author merge is undoable: merging, then reverting its operation, brings the
// merged-away author back under its own id and name with its aliases, puts every
// book's credits (order, co-authors, roles) and primary AuthorID back, and drops
// the redirect. Both callers of the shared merge loop are covered.

// opReporter is the relink reporter with an operation id, so the journal rows have
// an operation to be reverted by.
type opReporter struct {
	relinkOpReporter
	id string
}

func (r *opReporter) OpID() string { return r.id }

// undoWorld is a keep author, a merge-away author with an alias, and three books
// credited to it in different shapes: primary only, behind a co-author, trashed.
type undoWorld struct {
	st                 *database.PebbleStore
	keep, from, coauth *database.Author
	soloBook, coBook   string
	trashedBook        string
	before             map[string][]database.BookAuthor
}

func newUndoWorld(t *testing.T) *undoWorld {
	t.Helper()
	st := newRelinkOpStore(t)
	w := &undoWorld{st: st}
	var err error
	w.keep, err = st.CreateAuthor("Undo Keep")
	require.NoError(t, err)
	w.from, err = st.CreateAuthor("Undo From")
	require.NoError(t, err)
	w.coauth, err = st.CreateAuthor("Undo Coauthor")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(w.from.ID, "U. From", "pen_name")
	require.NoError(t, err)
	w.soloBook = mkRelinkOpBook(t, st, "undo-solo", w.from.ID, false)
	w.coBook = mkRelinkOpBook(t, st, "undo-co", w.from.ID, false)
	w.trashedBook = mkRelinkOpBook(t, st, "undo-trashed", w.from.ID, true)
	require.NoError(t, st.SetBookAuthors(w.coBook, []database.BookAuthor{
		{BookID: w.coBook, AuthorID: w.coauth.ID, Role: "author", Position: 0},
		{BookID: w.coBook, AuthorID: w.from.ID, Role: "narrator", Position: 1},
	}))
	w.before = map[string][]database.BookAuthor{}
	for _, id := range []string{w.soloBook, w.coBook, w.trashedBook} {
		w.before[id], err = st.GetBookAuthors(id)
		require.NoError(t, err)
	}
	return w
}

func (w *undoWorld) requireRestored(t *testing.T) {
	t.Helper()
	back, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.NotNil(t, back)
	require.Equal(t, w.from.ID, back.ID, "the author is back under its own id")
	require.Equal(t, w.from.Name, back.Name)
	byName, err := w.st.GetAuthorByName(w.from.Name)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, byName.ID, "the name index points at it again")
	tomb, err := w.st.GetAuthorTombstone(w.from.ID)
	require.NoError(t, err)
	require.Zero(t, tomb, "the redirect is gone")
	aliases, err := w.st.GetAuthorAliases(w.from.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 1)
	require.Equal(t, "U. From", aliases[0].AliasName)
	for id, want := range w.before {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, want, got, "book %s credits", id)
		book, berr := w.st.GetBookByID(id)
		require.NoError(t, berr)
		require.NotNil(t, book.AuthorID)
		require.Equal(t, w.from.ID, *book.AuthorID, "book %s primary author", id)
	}
}

func revertOp(t *testing.T, st *database.PebbleStore, opID string) (*audiobooks.RevertResult, error) {
	t.Helper()
	return audiobooks.NewRevertService(st).RevertOperation(opID)
}

func TestAuthorMergeOp_UndoRestoresTheAuthorItsCreditsAndPrimary(t *testing.T) {
	w := newUndoWorld(t)
	def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-merge"}))
	requireSourceDeleted(t, w.st, w.from.ID)

	// The merged author's alias now belongs to the keep author, so the name it
	// resolved by still resolves (to the author that absorbed it).
	viaAlias, err := w.st.FindAuthorByAlias("U. From")
	require.NoError(t, err)
	require.NotNil(t, viaAlias)
	require.Equal(t, w.keep.ID, viaAlias.ID)

	// Every row the merge wrote is one the revert can reverse.
	rows, err := w.st.GetOperationChanges("op-undo-merge")
	require.NoError(t, err)
	require.Len(t, rows, 4, "three credit rows and the delete row")
	for _, r := range rows {
		require.Empty(t, undo.NotRestorableLabel(r), "%s", r.ChangeType)
	}
	report, err := undo.PreflightUndoConflicts(w.st, "op-undo-merge")
	require.NoError(t, err)
	require.Zero(t, report.NotRestorable)

	res, err := revertOp(t, w.st, "op-undo-merge")
	require.NoError(t, err)
	require.Equal(t, 4, res.Restored)
	require.Zero(t, res.Failed)
	w.requireRestored(t)
	keepAliases, err := w.st.GetAuthorAliases(w.keep.ID)
	require.NoError(t, err)
	require.Empty(t, keepAliases, "the keep author gives the carried alias back")
	viaAlias, err = w.st.FindAuthorByAlias("U. From")
	require.NoError(t, err)
	require.NotNil(t, viaAlias)
	require.Equal(t, w.from.ID, viaAlias.ID, "and the alias names the restored author again")

	// And a second revert has nothing left to do.
	_, err = revertOp(t, w.st, "op-undo-merge")
	require.Error(t, err)
	require.Contains(t, err.Error(), "already been reverted")
}

func TestAIScanApplyOp_UndoRestoresTheMergedAuthor(t *testing.T) {
	w := newUndoWorld(t)
	f := newApplyFixture(t)
	f.st, f.s = w.st, newRelinkOpServer(w.st)
	f.s.aiScanStore = f.scanStore
	f.s.pipelineManager = aiscanManagerFor(f)
	id := f.result(database.ScanSuggestion{CanonicalName: w.keep.Name, KeepID: w.keep.ID, AuthorIDs: []int{w.keep.ID, w.from.ID}, Source: "groups_scan"})

	def := relinkOpDef(t, f.s.RegisterAIScanApplyOp, handlers.AIScanApplyDefID)
	raw, err := json.Marshal(handlers.AIScanApplyOpParams{ScanID: f.scan.ID, ResultIDs: []int{id}, DryRun: live()})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-ai"}))
	requireSourceDeleted(t, w.st, w.from.ID)
	require.True(t, f.applied(id))

	// The preflight counts the same rows the revert will restore: the note that
	// ties the result to the operation restores nothing and is in neither.
	report, err := undo.PreflightUndoConflicts(w.st, "op-undo-ai")
	require.NoError(t, err)
	require.Equal(t, 4, report.TotalChanges)
	require.Zero(t, report.NotRestorable)

	// The server's revert is the one that also marks the result pending again.
	res, err := f.s.revertOperation("op-undo-ai")
	require.NoError(t, err)
	require.Equal(t, 4, res.Restored)
	require.Equal(t, 4, res.Total)
	w.requireRestored(t)
	require.False(t, f.applied(id), "undone means the review list offers the suggestion again")

	// And it can be applied again.
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-ai-2"}))
	requireSourceDeleted(t, w.st, w.from.ID)
	require.True(t, f.applied(id))
}

// A partly undone merge leaves its result applied: the library still holds part of
// what the result did, and the list must not claim otherwise.
func TestAIScanApplyOp_APartlyUndoneApplyKeepsItsResultApplied(t *testing.T) {
	w := newUndoWorld(t)
	f := newApplyFixture(t)
	f.st, f.s = w.st, newRelinkOpServer(w.st)
	f.s.aiScanStore = f.scanStore
	f.s.pipelineManager = aiscanManagerFor(f)
	id := f.result(database.ScanSuggestion{CanonicalName: w.keep.Name, KeepID: w.keep.ID, AuthorIDs: []int{w.keep.ID, w.from.ID}, Source: "groups_scan"})
	def := relinkOpDef(t, f.s.RegisterAIScanApplyOp, handlers.AIScanApplyDefID)
	raw, err := json.Marshal(handlers.AIScanApplyOpParams{ScanID: f.scan.ID, ResultIDs: []int{id}, DryRun: live()})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-ai-partial"}))

	extra, err := w.st.CreateAuthor("Undo Later Coauthor")
	require.NoError(t, err)
	cur, err := w.st.GetBookAuthors(w.coBook)
	require.NoError(t, err)
	require.NoError(t, w.st.SetBookAuthors(w.coBook, append(append([]database.BookAuthor{}, cur...),
		database.BookAuthor{BookID: w.coBook, AuthorID: extra.ID, Role: "author", Position: len(cur)})))

	res, err := f.s.revertOperation("op-undo-ai-partial")
	require.Error(t, err)
	require.Equal(t, 1, res.Failed)
	require.True(t, f.applied(id), "part of the merge is still in the library")
}

// After the merge, somebody edits one of the books. The revert puts back every
// book it can prove nobody touched, and refuses (not overwrites) the one that
// changed, saying so.
func TestAuthorMergeOp_UndoLeavesABookEditedSinceAndRestoresTheRest(t *testing.T) {
	w := newUndoWorld(t)
	def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-edit"}))

	// A later edit: a second co-author is added to the co-authored book.
	extra, err := w.st.CreateAuthor("Undo Later Coauthor")
	require.NoError(t, err)
	cur, err := w.st.GetBookAuthors(w.coBook)
	require.NoError(t, err)
	edited := append(append([]database.BookAuthor{}, cur...), database.BookAuthor{BookID: w.coBook, AuthorID: extra.ID, Role: "author", Position: len(cur)})
	require.NoError(t, w.st.SetBookAuthors(w.coBook, edited))

	res, err := revertOp(t, w.st, "op-undo-edit")
	require.Error(t, err, "a row that cannot be reverted is reported, not hidden")
	require.Equal(t, 1, res.Failed)
	require.Equal(t, 3, res.Restored)
	require.Equal(t, 1, res.ChangedSince)

	got, gerr := w.st.GetBookAuthors(w.coBook)
	require.NoError(t, gerr)
	require.Equal(t, edited, got, "the later edit is not overwritten")
	for _, id := range []string{w.soloBook, w.trashedBook} {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, w.before[id], got)
	}
	back, aerr := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, aerr)
	require.Equal(t, w.from.ID, back.ID, "the author itself is back")
}

// The merged-away name was taken by a new author meanwhile. The author cannot be
// brought back, and so none of its credits are put back onto a row that does not exist.
func TestAuthorMergeOp_UndoRefusesWhenTheNameIsTakenAndRestoresNothingDangling(t *testing.T) {
	w := newUndoWorld(t)
	def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-taken"}))
	squatter, err := w.st.CreateAuthor(w.from.Name)
	require.NoError(t, err)
	require.NotEqual(t, w.from.ID, squatter.ID)

	res, err := revertOp(t, w.st, "op-undo-taken")

	require.Error(t, err)
	require.Equal(t, 4, res.Failed)
	require.Zero(t, res.Restored)
	for id := range w.before {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, []int{w.keep.ID}, func() []int {
			var ids []int
			for _, ba := range got {
				if ba.AuthorID != w.coauth.ID {
					ids = append(ids, ba.AuthorID)
				}
			}
			return ids
		}(), "book %s still on the keep author, nothing dangling", id)
	}
}

// A journal row that cannot be written means the merge does not happen for that
// book, and the author that book still credits is not deleted.
type failJournalStore struct {
	*database.PebbleStore
	failType string
}

func (f failJournalStore) CreateOperationChange(c *database.OperationChange) error {
	if c.ChangeType == f.failType {
		return errors.New("injected journal failure")
	}
	return f.PebbleStore.CreateOperationChange(c)
}

func TestAuthorMergeOp_AFailedJournalWriteMovesNothingAndDeletesNothing(t *testing.T) {
	for _, failType := range []string{undo.ChangeTypeAuthorMergeCredits, undo.ChangeTypeAuthorMergeDelete} {
		t.Run(failType, func(t *testing.T) {
			w := newUndoWorld(t)
			s := newRelinkOpServer(failJournalStore{PebbleStore: w.st, failType: failType})
			def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
			raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
			require.NoError(t, err)

			err = def.Run(context.Background(), raw, &opReporter{id: "op-nojournal-" + failType})

			require.Error(t, err)
			require.Contains(t, err.Error(), "undo row not recorded")
			back, gerr := w.st.GetAuthorByID(w.from.ID)
			require.NoError(t, gerr)
			require.Equal(t, w.from.ID, back.ID, "the author is not deleted")
			if failType == undo.ChangeTypeAuthorMergeCredits {
				for id, want := range w.before {
					got, gerr := w.st.GetBookAuthors(id)
					require.NoError(t, gerr)
					require.Equal(t, want, got, "an unjournaled merge never happens")
				}
			}
		})
	}
}

// A refused junction write voids the row it journaled, and the book's primary
// author is not repointed (the junction still credits the merged author).
func TestAuthorMergeOp_ARefusedWriteVoidsItsRowAndLeavesThePrimaryAlone(t *testing.T) {
	w := newUndoWorld(t)
	s := newRelinkOpServer(failSetBookAuthorsStore{PebbleStore: w.st, failBook: w.soloBook})
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)

	err = def.Run(context.Background(), raw, &opReporter{id: "op-void"})
	require.Error(t, err)

	book, gerr := w.st.GetBookByID(w.soloBook)
	require.NoError(t, gerr)
	require.Equal(t, w.from.ID, *book.AuthorID, "a book whose junction write failed still names its author as primary, not the keep")
	got, gerr := w.st.GetBookAuthors(w.soloBook)
	require.NoError(t, gerr)
	require.Equal(t, w.before[w.soloBook], got)

	rows, gerr := w.st.GetOperationChanges("op-void")
	require.NoError(t, gerr)
	voided := 0
	for _, r := range rows {
		if r.BookID == w.soloBook {
			require.True(t, r.Voided, "the row of the refused write is voided")
			voided++
		}
	}
	require.Equal(t, 1, voided)

	// The other books merged, the author was kept (the solo book still credits it),
	// and reverting restores them without tripping on the voided row.
	res, rerr := revertOp(t, w.st, "op-void")
	require.NoError(t, rerr)
	require.Zero(t, res.Failed)
	for _, id := range []string{w.coBook, w.trashedBook} {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, w.before[id], got)
	}
}

// A flagged narrator the AI apply reclassifies is journaled too, and the revert
// brings the author back, its credits, and removes the narrator link it added.
func TestAIScanApplyOp_UndoRestoresAReclassifiedNarrator(t *testing.T) {
	f := newApplyFixture(t)
	keep, variant, voice := f.author("Rev Keep"), f.author("Rev Variant"), f.author("Rev Voice")
	_, err := f.st.CreateAuthorAlias(voice.ID, "V.", "pen_name")
	require.NoError(t, err)
	mkRelinkOpBook(t, f.st, "rev-variant", variant.ID, false)
	voiceBook := mkRelinkOpBook(t, f.st, "rev-voice", voice.ID, false)
	beforeCredits, err := f.st.GetBookAuthors(voiceBook)
	require.NoError(t, err)
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": voice.Name, "ids": []int{voice.ID}}})
	require.NoError(t, err)
	id := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, variant.ID, voice.ID},
		Roles: roles, Source: "full_scan",
	})
	def := relinkOpDef(t, f.s.RegisterAIScanApplyOp, handlers.AIScanApplyDefID)
	raw, err := json.Marshal(handlers.AIScanApplyOpParams{ScanID: f.scan.ID, ResultIDs: []int{id}, DryRun: live()})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-undo-narr"}))
	narrators, nerr := f.st.GetBookNarrators(voiceBook)
	require.NoError(t, nerr)
	require.Len(t, narrators, 1)

	res, err := revertOp(t, f.st, "op-undo-narr")

	require.NoError(t, err)
	require.Zero(t, res.Failed)
	back, gerr := f.st.GetAuthorByID(voice.ID)
	require.NoError(t, gerr)
	require.Equal(t, voice.ID, back.ID)
	aliases, gerr := f.st.GetAuthorAliases(voice.ID)
	require.NoError(t, gerr)
	require.Len(t, aliases, 1)
	got, gerr := f.st.GetBookAuthors(voiceBook)
	require.NoError(t, gerr)
	require.Equal(t, beforeCredits, got)
	book, gerr := f.st.GetBookByID(voiceBook)
	require.NoError(t, gerr)
	require.Equal(t, voice.ID, *book.AuthorID)
	narrators, nerr = f.st.GetBookNarrators(voiceBook)
	require.NoError(t, nerr)
	require.Empty(t, narrators, "the narrator link the reclassify added is removed")
	// The variant merge was undone with it.
	require.Equal(t, strconv.Itoa(variant.ID), strconv.Itoa(func() int { a, _ := f.st.GetAuthorByID(variant.ID); return a.ID }()))
}

// failLocksStore makes every lock read fail.
type failLocksStore struct{ *database.PebbleStore }

func (failLocksStore) GetMetadataFieldStates(string) ([]database.MetadataFieldState, error) {
	return nil, errors.New("injected lock read failure")
}

// A lock that cannot be read is not "unlocked": neither a live merge nor a preview
// treats the author as mergeable. A preview that said "merged" would have the apply
// refuse what the preview promised.
func TestMergeAuthorsInto_AnUnreadableLockHoldsTheAuthorBackInLiveAndPreview(t *testing.T) {
	for _, dry := range []bool{true, false} {
		w := newUndoWorld(t)
		st := failLocksStore{PebbleStore: w.st}

		out, _ := mergeAuthorsInto(context.Background(), st, "op-unreadable",
			authorMergeSpec{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, HonorLocks: true, DryRun: dry}, nil, nil)

		require.Zero(t, out.Merged, "dry=%v: an author held back by an unreadable lock is not merged", dry)
		require.False(t, out.MergedIDs[w.from.ID], "dry=%v", dry)
		require.NotEmpty(t, out.Errors, "dry=%v", dry)
		require.Contains(t, strings.Join(out.Errors, "\n"), "injected lock read failure")
		back, err := w.st.GetAuthorByID(w.from.ID)
		require.NoError(t, err)
		require.Equal(t, w.from.ID, back.ID, "dry=%v: nothing deleted", dry)
		for id, want := range w.before {
			got, gerr := w.st.GetBookAuthors(id)
			require.NoError(t, gerr)
			require.Equal(t, want, got, "dry=%v: no book moved", dry)
		}
	}
}

// An id that never existed is "not found", and is not mistaken for one already
// merged into the keep author.
func TestAuthorMergeOp_AnUnknownIDIsNotFoundAndNotCountedMerged(t *testing.T) {
	w := newUndoWorld(t)

	out, _ := mergeAuthorsInto(context.Background(), w.st, "op-unknown",
		authorMergeSpec{KeepID: w.keep.ID, MergeIDs: []int{987654}}, nil, nil)

	require.Zero(t, out.Merged)
	require.False(t, out.MergedIDs[987654], "an author that never existed is not 'already merged'")
	require.Len(t, out.Errors, 1)
	require.Contains(t, out.Errors[0], "author 987654 was not found")

	// Through the op, the failure is the op's.
	def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{987654}, KeepName: w.keep.Name})
	require.NoError(t, err)
	err = def.Run(context.Background(), raw, &opReporter{id: "op-unknown-2"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "author 987654 was not found")
}
