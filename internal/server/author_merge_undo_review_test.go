// file: internal/server/author_merge_undo_review_test.go
// version: 1.0.0
// guid: 4f448d89-469a-44ec-b6b4-5fd001758a18
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/entities"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// runUndoWorldMerge merges w.from into w.keep under opID through the
// entities.author-merge op.
func runUndoWorldMerge(t *testing.T, w *undoWorld, opID string) {
	t.Helper()
	def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: opID}))
	gone, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, gone.ID, "merged")
}

// requireCreditsBack checks every book of the world is back on w.from exactly as
// it was, primary included, and the author row is live under its own id.
func requireCreditsBack(t *testing.T, w *undoWorld) {
	t.Helper()
	back, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.NotNil(t, back)
	require.Equal(t, w.from.ID, back.ID, "the author is back under its own id")
	tomb, err := w.st.GetAuthorTombstone(w.from.ID)
	require.NoError(t, err)
	require.Zero(t, tomb, "the redirect is gone")
	aliases, err := w.st.GetAuthorAliases(w.from.ID)
	require.NoError(t, err)
	require.Len(t, aliases, 1, "its alias is back")
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

// Two authors whose names collide on the normalized name index (a case variant,
// the pair a dedup merge exists to fold together) share ONE entry, and here the
// keep author holds it. The merge must be undoable, and the preflight must say so:
// the restore used to read the keep author's hold on the entry as "the name was
// taken since", refuse the author, and so refuse every credit after it, while the
// preflight counted every row safe.
func TestAuthorMergeOp_UndoOfACollidingNameMergeRestoresEverything(t *testing.T) {
	w := newUndoWorld(t)
	require.NoError(t, w.st.UpdateAuthorName(w.from.ID, "undo keep"))
	w.from.Name = "undo keep"
	require.NoError(t, w.st.UpdateAuthorName(w.keep.ID, "Undo Keep"))
	byName, err := w.st.GetAuthorByName("undo keep")
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, byName.ID, "the keep author holds the shared entry")

	runUndoWorldMerge(t, w, "op-undo-collide")

	pre, err := undo.PreflightUndoConflicts(w.st, "op-undo-collide")
	require.NoError(t, err)
	require.Equal(t, pre.TotalChanges, pre.Safe, "every row predicted restorable: %+v", pre)

	res, err := revertOp(t, w.st, "op-undo-collide")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	requireCreditsBack(t, w)
	byName, err = w.st.GetAuthorByName("undo keep")
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, byName.ID, "the keep author still holds the shared entry")
}

// The shared entry is held by a THIRD author (not the keep author): the merged
// author never resolved by name, so its undo neither takes the entry nor reads
// the third author's hold as "the name was taken since".
func TestAuthorMergeOp_UndoOfAMergeWhoseNameAThirdAuthorHoldsRestoresEverything(t *testing.T) {
	w := newUndoWorld(t)
	third, err := w.st.CreateAuthor("Undo Third")
	require.NoError(t, err)
	require.NoError(t, w.st.UpdateAuthorName(w.from.ID, "undo third"))
	w.from.Name = "undo third"
	require.NoError(t, w.st.UpdateAuthorName(third.ID, "Undo Third")) // re-takes the entry
	byName, err := w.st.GetAuthorByName("undo third")
	require.NoError(t, err)
	require.Equal(t, third.ID, byName.ID, "the third author holds the shared entry")

	runUndoWorldMerge(t, w, "op-undo-third")

	pre, err := undo.PreflightUndoConflicts(w.st, "op-undo-third")
	require.NoError(t, err)
	require.Equal(t, pre.TotalChanges, pre.Safe, "every row predicted restorable: %+v", pre)

	res, err := revertOp(t, w.st, "op-undo-third")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	requireCreditsBack(t, w)
	byName, err = w.st.GetAuthorByName("undo third")
	require.NoError(t, err)
	require.Equal(t, third.ID, byName.ID, "the third author still holds the shared entry")
}

// The merged author held its own entry, and the keep author was renamed onto that
// name after the merge (the usual "keep the better spelling" follow-up). The keep
// author keeps the entry; the merged author still comes back, with its credits.
func TestAuthorMergeOp_UndoAfterTheKeepAuthorTookTheNameRestoresWithoutTheEntry(t *testing.T) {
	w := newUndoWorld(t)
	runUndoWorldMerge(t, w, "op-undo-keep-took")
	require.NoError(t, w.st.UpdateAuthorName(w.keep.ID, w.from.Name))

	pre, err := undo.PreflightUndoConflicts(w.st, "op-undo-keep-took")
	require.NoError(t, err)
	require.Equal(t, pre.TotalChanges, pre.Safe, "every row predicted restorable: %+v", pre)

	res, err := revertOp(t, w.st, "op-undo-keep-took")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	requireCreditsBack(t, w)
	byName, err := w.st.GetAuthorByName(w.from.Name)
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, byName.ID)
}

// A third author took the merged author's name since: the revert refuses the
// author and every credit after it, and the preflight predicts exactly that
// instead of counting the rows safe.
func TestAuthorMergeOp_PreflightRefusesTheCreditsOfAnAuthorItCannotRestore(t *testing.T) {
	w := newUndoWorld(t)
	runUndoWorldMerge(t, w, "op-undo-squat")
	_, err := w.st.CreateAuthor(w.from.Name)
	require.NoError(t, err)

	pre, err := undo.PreflightUndoConflicts(w.st, "op-undo-squat")
	require.NoError(t, err)
	require.Zero(t, pre.Safe, "nothing of this merge can be restored: %+v", pre)

	res, err := revertOp(t, w.st, "op-undo-squat")
	require.Error(t, err)
	require.Zero(t, res.Restored)
	require.Equal(t, pre.TotalChanges, res.Failed)
}

// A failed alias move-back fails the author row instead of counting it restored,
// and a retry finishes it: the steps after the row run on the already-restored
// path too.
func TestAuthorMergeOp_AnAliasThatDidNotComeBackFailsTheRowAndARetryFinishesIt(t *testing.T) {
	w := newUndoWorld(t)
	runUndoWorldMerge(t, w, "op-undo-alias")
	// Somebody moved the carried alias to a third author meanwhile.
	keepAliases, err := w.st.GetAuthorAliases(w.keep.ID)
	require.NoError(t, err)
	require.Len(t, keepAliases, 1, "the merge carried the alias to the keep author")
	require.NoError(t, w.st.DeleteAuthorAlias(keepAliases[0].ID))
	_, err = w.st.CreateAuthorAlias(w.coauth.ID, "U. From", "pen_name")
	require.NoError(t, err)

	res, err := revertOp(t, w.st, "op-undo-alias")
	require.Error(t, err, "the alias that cannot come back is reported")
	require.Equal(t, 1, res.Failed)
	require.Contains(t, err.Error(), "U. From")

	// The alias is freed; the retry finishes the author row.
	held, err := w.st.GetAuthorAliases(w.coauth.ID)
	require.NoError(t, err)
	require.Len(t, held, 1)
	require.NoError(t, w.st.DeleteAuthorAlias(held[0].ID))
	_, err = revertOp(t, w.st, "op-undo-alias")
	require.NoError(t, err)
	requireCreditsBack(t, w)
	got, err := w.st.FindAuthorByAlias("U. From")
	require.NoError(t, err)
	require.Equal(t, w.from.ID, got.ID)
}

// Every merge path keeps a merged author's pen name resolving, to the keep
// author: here the AI dedup apply (ai.author-merge-apply), both its merge and its
// alias action. It deleted, then tombstoned, and the delete cascaded the alias away.
func TestAIAuthorMergeApplyOp_PenNameOfAMergedAuthorResolvesToTheKeep(t *testing.T) {
	for _, action := range []string{"merge", "alias"} {
		t.Run(action, func(t *testing.T) {
			st := newRelinkOpStore(t)
			keep, err := st.CreateAuthor("Pen Keep")
			require.NoError(t, err)
			from, err := st.CreateAuthor("Pen From")
			require.NoError(t, err)
			_, err = st.CreateAuthorAlias(from.ID, "P. Pen", "pen_name")
			require.NoError(t, err)
			mkRelinkOpBook(t, st, "pen-book-"+action, from.ID, false)

			def := relinkOpDef(t, newRelinkOpServer(st).RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
			raw, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{
				{Action: action, KeepID: keep.ID, MergeIDs: []int{from.ID}, CanonicalName: keep.Name},
			}})
			require.NoError(t, err)
			require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-pen-" + action}))

			gone, err := st.GetAuthorByID(from.ID)
			require.NoError(t, err)
			require.Equal(t, keep.ID, gone.ID, "merged, with the redirect")
			got, err := st.FindAuthorByAlias("P. Pen")
			require.NoError(t, err)
			require.NotNil(t, got, "the pen name resolves to somebody")
			require.Equal(t, keep.ID, got.ID)
		})
	}
}

// failModifyBookStore fails the primary-author write for one book: the credits
// move, the primary does not.
type failModifyBookStore struct {
	*database.PebbleStore
	failBook string
}

func (f failModifyBookStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == f.failBook {
		return nil, errors.New("injected primary write failure")
	}
	return f.PebbleStore.ModifyBook(id, fn)
}

// The credits of a book moved but its primary author could not be repointed. The
// row used to still claim the repoint, so the revert read the untouched primary as
// "already back", refused the junction as changed since, and the credits could
// never go back. It now journals what happened, and the undo puts them back.
func TestAuthorMergeOp_APrimaryThatWasNotRepointedDoesNotBlockTheUndo(t *testing.T) {
	w := newUndoWorld(t)
	s := newRelinkOpServer(failModifyBookStore{PebbleStore: w.st, failBook: w.soloBook})
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)
	require.Error(t, def.Run(context.Background(), raw, &opReporter{id: "op-primary-stuck"}))
	moved, err := w.st.GetBookAuthors(w.soloBook)
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, moved[0].AuthorID, "the credit moved")
	book, err := w.st.GetBookByID(w.soloBook)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, *book.AuthorID, "the primary did not")

	res, err := revertOp(t, w.st, "op-primary-stuck")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	for id, want := range w.before {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, want, got, "book %s credits", id)
	}
}

// The reclassify journals the same way when its primary write fails.
func TestReclassify_APrimaryThatWasNotRepointedDoesNotBlockTheUndo(t *testing.T) {
	w := newUndoWorld(t)
	store := failModifyBookStore{PebbleStore: w.st, failBook: w.soloBook}
	_, err := entities.ReclassifyAuthorAsNarrator(store, w.from.ID, entities.ReclassifyOptions{
		Journal: &entities.ReclassifyJournal{OperationID: "op-reclass-stuck", Store: store},
	})
	require.Error(t, err, "the author is kept: the solo book's primary still names it")

	res, err := revertOp(t, w.st, "op-reclass-stuck")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	for id, want := range w.before {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, want, got, "book %s credits", id)
	}
}

// newUndoApplyFixture is an AI scan whose one result merges w.from into w.keep,
// on w's store, applied under opID.
func newUndoApplyFixture(t *testing.T, w *undoWorld, opID string) (*applyFixture, int) {
	t.Helper()
	f := newApplyFixture(t)
	f.st, f.s = w.st, newRelinkOpServer(w.st)
	f.s.aiScanStore = f.scanStore
	f.s.pipelineManager = aiscanManagerFor(f)
	id := f.result(database.ScanSuggestion{CanonicalName: w.keep.Name, KeepID: w.keep.ID, AuthorIDs: []int{w.keep.ID, w.from.ID}, Source: "groups_scan"})
	def := relinkOpDef(t, f.s.RegisterAIScanApplyOp, handlers.AIScanApplyDefID)
	raw, err := json.Marshal(handlers.AIScanApplyOpParams{ScanID: f.scan.ID, ResultIDs: []int{id}, DryRun: live()})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: opID}))
	require.True(t, f.applied(id))
	return f, id
}

// A revert that finds every row reverted already still reopens the results: an
// earlier run may have marked the notes reverted and stopped before reopening.
func TestRevertOperation_ASecondRunReopensTheResultsTheFirstDidNot(t *testing.T) {
	w := newUndoWorld(t)
	f, id := newUndoApplyFixture(t, w, "op-reopen-second")
	// The first run: everything reverted, the results not reopened (the revert
	// service alone, standing in for a run that stopped before the reopen).
	_, err := revertOp(t, w.st, "op-reopen-second")
	require.NoError(t, err)
	require.True(t, f.applied(id))

	res, err := f.s.revertOperation("op-reopen-second")
	require.Error(t, err, "already reverted")
	require.Nil(t, res)
	require.False(t, f.applied(id), "the second run reopens the result")
}

// Undoing an author merge drops the author list and dedup caches, as the merge
// does, so the restored author is listed again at once.
func TestRevertOperation_AnUndoneAuthorMergeDropsTheAuthorCaches(t *testing.T) {
	w := newUndoWorld(t)
	f, _ := newUndoApplyFixture(t, w, "op-undo-caches")
	f.s.authorsCache.Set("authors-list", nil)
	f.s.dedupCache.Set("author-duplicates", gin.H{"stale": true})

	_, err := f.s.revertOperation("op-undo-caches")
	require.NoError(t, err)
	require.Zero(t, f.s.authorsCache.Len(), "author list cache dropped")
	require.Zero(t, f.s.dedupCache.Len(), "dedup cache dropped")
}

// The server wires the scan-apply claim to the operations registry: a claim left
// by a run that is not running (the previous process, a crashed run) is cleared at
// startup and does not hold the scan, where it used to refuse every apply of it
// for three hours.
func TestWireScanApplyClaims_AClaimOfARunThatIsNotRunningDoesNotHoldTheScan(t *testing.T) {
	f := newApplyFixture(t)
	require.NoError(t, f.scanStore.ClaimScanForApply(f.scan.ID, "op-previous-process"))
	m := dbmocks.NewMockStore(t)
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)

	wireScanApplyClaims(f.scanStore, reg)

	got, err := f.scanStore.GetScan(f.scan.ID)
	require.NoError(t, err)
	require.Empty(t, got.ApplyingOpID, "cleared at startup")
	require.NoError(t, f.scanStore.ClaimScanForApply(f.scan.ID, "op-crashed"))
	require.NoError(t, f.scanStore.ClaimScanForApply(f.scan.ID, "op-next"), "op-crashed is not running, so it holds nothing")
}
