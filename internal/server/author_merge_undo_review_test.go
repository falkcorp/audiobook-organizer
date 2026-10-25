// file: internal/server/author_merge_undo_review_test.go
// version: 1.8.1
// guid: 4f448d89-469a-44ec-b6b4-5fd001758a18
// last-edited: 2026-10-25

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

	pre, err := preflightUndoConflicts(w.st, "op-undo-collide")
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

	pre, err := preflightUndoConflicts(w.st, "op-undo-third")
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

	pre, err := preflightUndoConflicts(w.st, "op-undo-keep-took")
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

	pre, err := preflightUndoConflicts(w.st, "op-undo-squat")
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

// failModifyBookStore fails the primary-author write for one book (ModifyBook,
// and ModifyBookCredits, which writes a repointed primary with its credits).
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

// ModifyBookCredits fails the same book: authormerge.MoveCredits repoints a
// primary through it, in one write with the credits, so neither moves.
func (f failModifyBookStore) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if id == f.failBook {
		return nil, database.BookCreditsEdit{}, errors.New("injected primary write failure")
	}
	return f.PebbleStore.ModifyBookCredits(id, fn)
}

// A book whose primary author could not be repointed. On Pebble the credits and
// the primary are one write (authormerge.MoveCredits through ModifyBookCredits),
// so a failed write moves neither and its row is voided; the merge reports the
// failure, keeps the author, and the undo of what did move is clean. (A store
// without that capability moves the credits and then the primary as two writes;
// authormerge's TestMoveCredits_AFailedPrimaryRewritesTheRowAsUnchanged covers
// the row it journals when only the second fails.)
func TestAuthorMergeOp_APrimaryThatWasNotRepointedDoesNotBlockTheUndo(t *testing.T) {
	w := newUndoWorld(t)
	s := newRelinkOpServer(failModifyBookStore{PebbleStore: w.st, failBook: w.soloBook})
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)
	require.Error(t, def.Run(context.Background(), raw, &opReporter{id: "op-primary-stuck"}))
	unmoved, err := w.st.GetBookAuthors(w.soloBook)
	require.NoError(t, err)
	require.Equal(t, w.before[w.soloBook], unmoved, "the credit did not move without its primary")
	book, err := w.st.GetBookByID(w.soloBook)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, *book.AuthorID, "the primary did not move")
	rows, err := w.st.GetOperationChanges("op-primary-stuck")
	require.NoError(t, err)
	for _, r := range rows {
		if r.BookID == w.soloBook {
			require.True(t, r.Voided, "the row of the failed write is voided")
		}
	}

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

// An operation-runner child's registry knows only the op the child runs. Its
// wiring must not clear, or judge, the claims the parent's runs hold.
func TestWireScanApplyClaims_AChildProcessLeavesTheParentsClaimsAlone(t *testing.T) {
	old := isChildProcess
	isChildProcess = func() bool { return true }
	t.Cleanup(func() { isChildProcess = old })
	f := newApplyFixture(t)
	require.NoError(t, f.scanStore.ClaimScanForApply(f.scan.ID, "op-running-in-the-parent"))
	reg := opsregistry.New(dbmocks.NewMockStore(t), slog.New(slog.DiscardHandler), 1, nil)

	wireScanApplyClaims(f.scanStore, reg)

	got, err := f.scanStore.GetScan(f.scan.ID)
	require.NoError(t, err)
	require.Equal(t, "op-running-in-the-parent", got.ApplyingOpID, "not cleared")
	require.Error(t, f.scanStore.ClaimScanForApply(f.scan.ID, "op-other"), "and it still holds the scan")
}

// noMergeDeleteStore is a Store without the AuthorMergeDeleter capability:
// embedding the interface hides *PebbleStore's MergeDeleteAuthor, and it does
// not unwrap to the store underneath.
type noMergeDeleteStore struct{ database.Store }

// The merge-delete is a store capability, not part of Store. A store without it
// makes the Authors-view merge refuse before any book moves: there is no
// DeleteAuthor fallback, which would delete the merged author's aliases.
func TestAuthorMergeOp_AStoreWithoutTheMergeDeleteCapabilityRefusesTheMerge(t *testing.T) {
	w := newUndoWorld(t)
	def := relinkOpDef(t, newRelinkOpServer(noMergeDeleteStore{Store: w.st}).RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: w.keep.ID, MergeIDs: []int{w.from.ID}, KeepName: w.keep.Name})
	require.NoError(t, err)

	err = def.Run(context.Background(), raw, &opReporter{id: "op-capless"})
	require.ErrorContains(t, err, "AuthorMergeDeleter")

	still, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, still.ID, "the author was not deleted")
	for id, want := range w.before {
		got, gerr := w.st.GetBookAuthors(id)
		require.NoError(t, gerr)
		require.Equal(t, want, got, "book %s was not moved", id)
	}
	alias, err := w.st.FindAuthorByAlias("U. From")
	require.NoError(t, err)
	require.Equal(t, w.from.ID, alias.ID, "the alias was not touched")
}

// The AI dedup apply refuses a run with a merge or alias suggestion the same way.
func TestAIAuthorMergeApplyOp_AStoreWithoutTheMergeDeleteCapabilityRefusesTheRun(t *testing.T) {
	for _, action := range []string{"merge", "alias"} {
		t.Run(action, func(t *testing.T) {
			st := newRelinkOpStore(t)
			keep, err := st.CreateAuthor("Capless Keep")
			require.NoError(t, err)
			from, err := st.CreateAuthor("Capless From")
			require.NoError(t, err)
			book := mkRelinkOpBook(t, st, "capless-"+action, from.ID, false)

			def := relinkOpDef(t, newRelinkOpServer(noMergeDeleteStore{Store: st}).RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
			raw, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{
				{Action: action, KeepID: keep.ID, MergeIDs: []int{from.ID}, CanonicalName: keep.Name},
			}})
			require.NoError(t, err)

			err = def.Run(context.Background(), raw, &opReporter{id: "op-capless-" + action})
			require.ErrorIs(t, err, database.ErrAuthorMergeDeleteUnsupported)

			still, err := st.GetAuthorByID(from.ID)
			require.NoError(t, err)
			require.Equal(t, from.ID, still.ID, "the author was not deleted")
			credits, err := st.GetBookAuthors(book)
			require.NoError(t, err)
			require.Len(t, credits, 1)
			require.Equal(t, from.ID, credits[0].AuthorID, "the book was not moved")
			aliases, err := st.GetAuthorAliases(keep.ID)
			require.NoError(t, err)
			require.Empty(t, aliases, "no alias was created")
		})
	}
}

// An author whose name fails today's creation gate (the library holds rows that
// predate it: "read by narrator", "Epigraph") is merged and reclassified like any
// other, so its undo must bring it back. The restore used to apply the creation
// gate: the preflight called every row safe and the revert failed all of them.
func TestUndo_AnAuthorWhoseNameFailsTheCreationGateComesBack(t *testing.T) {
	for _, junk := range []string{"read by narrator", "Epigraph", "14 BBY"} {
		t.Run("merge/"+junk, func(t *testing.T) {
			require.Error(t, database.CheckAuthorNameForCreation(junk))
			w := newUndoWorld(t)
			require.NoError(t, w.st.UpdateAuthorName(w.from.ID, junk))
			w.from.Name = junk
			runUndoWorldMerge(t, w, "op-gate-merge")

			pre, err := preflightUndoConflicts(w.st, "op-gate-merge")
			require.NoError(t, err)
			require.Equal(t, pre.TotalChanges, pre.Safe, "the preflight calls every row safe")
			res, err := revertOp(t, w.st, "op-gate-merge")
			require.NoError(t, err)
			require.Zero(t, res.Failed, "and the revert restores every row")

			back, err := w.st.GetAuthorByID(w.from.ID)
			require.NoError(t, err)
			require.Equal(t, w.from.ID, back.ID)
			require.Equal(t, junk, back.Name)
			for id, want := range w.before {
				got, gerr := w.st.GetBookAuthors(id)
				require.NoError(t, gerr)
				require.Equal(t, want, got, "book %s credits", id)
			}
		})
	}
	t.Run("reclassify", func(t *testing.T) {
		w := newUndoWorld(t)
		require.NoError(t, w.st.UpdateAuthorName(w.from.ID, "read by narrator"))
		_, err := entities.ReclassifyAuthorAsNarrator(w.st, w.from.ID, entities.ReclassifyOptions{
			Journal: &entities.ReclassifyJournal{OperationID: "op-gate-recl", Store: w.st},
		})
		require.NoError(t, err)

		pre, err := preflightUndoConflicts(w.st, "op-gate-recl")
		require.NoError(t, err)
		require.Equal(t, pre.TotalChanges, pre.Safe)
		res, err := revertOp(t, w.st, "op-gate-recl")
		require.NoError(t, err)
		require.Zero(t, res.Failed)
		back, err := w.st.GetAuthorByID(w.from.ID)
		require.NoError(t, err)
		require.Equal(t, w.from.ID, back.ID)
		require.Equal(t, "read by narrator", back.Name)
	})
}

// An author with no books comes back from an undo whose alias could not: the
// delete row fails (the alias is reported) and no restored type counts it. The
// author is still back, so the author caches must still be dropped.
func TestRevertOperation_APartlyRestoredAuthorWithNoBooksDropsTheAuthorCaches(t *testing.T) {
	st := newRelinkOpStore(t)
	keep, err := st.CreateAuthor("Bookless Keep")
	require.NoError(t, err)
	from, err := st.CreateAuthor("Bookless From")
	require.NoError(t, err)
	third, err := st.CreateAuthor("Bookless Third")
	require.NoError(t, err)
	_, err = st.CreateAuthorAlias(from.ID, "B. From", "pen_name")
	require.NoError(t, err)
	s := newRelinkOpServer(st)
	def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
	raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: keep.ID, MergeIDs: []int{from.ID}, KeepName: keep.Name})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-bookless"}))
	// The carried alias moves to a third author meanwhile: it cannot come back.
	carried, err := st.GetAuthorAliases(keep.ID)
	require.NoError(t, err)
	require.Len(t, carried, 1)
	require.NoError(t, st.DeleteAuthorAlias(carried[0].ID))
	_, err = st.CreateAuthorAlias(third.ID, "B. From", "pen_name")
	require.NoError(t, err)
	s.authorsCache.Set("authors-list", nil)
	s.dedupCache.Set("author-duplicates", gin.H{"stale": true})

	res, err := s.revertOperation("op-bookless")
	require.Error(t, err, "the alias is reported")
	require.Equal(t, 1, res.AuthorsPartlyRestored)
	back, gerr := st.GetAuthorByID(from.ID)
	require.NoError(t, gerr)
	require.Equal(t, from.ID, back.ID, "the author row is back")
	require.Zero(t, s.authorsCache.Len(), "author list cache dropped")
	require.Zero(t, s.dedupCache.Len(), "dedup cache dropped")
}

// A reclassify of an author whose normalized name entry a case variant holds
// records SharedNameIndex (handlers/entities/reclassify.go). Its undo brings the
// author back without taking the entry from the variant. Without the flag the
// restore treated the entry as the author's own, and the variant holding it read
// as "another author has taken the name": the undo was refused.
func TestReclassify_UndoOfAnAuthorWhoseNameACaseVariantHoldsLeavesTheVariantTheEntry(t *testing.T) {
	w := newUndoWorld(t)
	require.NoError(t, w.st.UpdateAuthorName(w.from.ID, "undo keep"))
	require.NoError(t, w.st.UpdateAuthorName(w.keep.ID, "Undo Keep"))
	h, err := w.st.GetAuthorByName("undo keep")
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, h.ID, "fixture: the case variant holds the shared entry")
	_, err = entities.ReclassifyAuthorAsNarrator(w.st, w.from.ID, entities.ReclassifyOptions{
		Journal: &entities.ReclassifyJournal{OperationID: "op-recl-variant", Store: w.st},
	})
	require.NoError(t, err)

	pre, err := preflightUndoConflicts(w.st, "op-recl-variant")
	require.NoError(t, err)
	require.Equal(t, pre.TotalChanges, pre.Safe)
	res, err := revertOp(t, w.st, "op-recl-variant")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	back, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, back.ID, "the author is back")
	h, err = w.st.GetAuthorByName("undo keep")
	require.NoError(t, err)
	require.Equal(t, w.keep.ID, h.ID, "and the variant still holds the entry")
}

// mergeFinish journals the delete row before the merge-delete and rewrites it
// with what the delete reported (authormerge.SameSnapshot). Only the delete knows the
// ids whose redirects pointed at the merged author, so only the rewritten row can
// point a flattened redirect back. C was merged into A, A into K, and redirect
// flattening repointed C at K: undoing A->K points C at A again.
func TestAuthorMergeOp_UndoPointsAFlattenedRedirectBackThroughTheRewrittenRow(t *testing.T) {
	st := newRelinkOpStore(t)
	c, err := st.CreateAuthor("Chain Oldest")
	require.NoError(t, err)
	a, err := st.CreateAuthor("Chain Middle")
	require.NoError(t, err)
	k, err := st.CreateAuthor("Chain Keep")
	require.NoError(t, err)
	mkRelinkOpBook(t, st, "chain-middle", a.ID, false)
	s := newRelinkOpServer(st)
	merge := func(opID string, keep *database.Author, from int) {
		t.Helper()
		def := relinkOpDef(t, s.RegisterAuthorMergeOp, "entities.author-merge")
		raw, err := json.Marshal(entities.AuthorMergeOpParams{KeepID: keep.ID, MergeIDs: []int{from}, KeepName: keep.Name})
		require.NoError(t, err)
		require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: opID}))
	}
	merge("op-chain-1", a, c.ID)
	merge("op-chain-2", k, a.ID)
	_, err = st.ResolveTombstoneChains()
	require.NoError(t, err)
	flat, err := st.GetAuthorTombstone(c.ID)
	require.NoError(t, err)
	require.Equal(t, k.ID, flat, "fixture: C's redirect was flattened onto K")

	res, err := revertOp(t, st, "op-chain-2")
	require.NoError(t, err)
	require.Zero(t, res.Failed)
	back, err := st.GetAuthorByID(a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, back.ID, "A is back")
	redirect, err := st.GetAuthorTombstone(c.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, redirect, "C redirects to A again")
	viaC, err := st.GetAuthorByID(c.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, viaC.ID)
}
