// file: internal/server/ai_merge_apply_undo_test.go
// version: 1.2.0
// guid: 9c4a7e21-3b58-4f06-8d1e-6a2f0b7c5e93
// last-edited: 2026-10-15

package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// ai.author-merge-apply journals its merges the way entities.author-merge does
// (authormerge), so undoing the apply brings back the merged author, its alias,
// and every book's credits and primary. Both actions that delete an author are
// covered: merge, and alias (which also adds the variant's name to the kept
// author as a pen name; that alias is not journaled and stays).
func TestAIAuthorMergeApplyOp_UndoRestoresTheMergedAuthor(t *testing.T) {
	for _, action := range []string{"merge", "alias"} {
		t.Run(action, func(t *testing.T) {
			w := newUndoWorld(t)
			opID := "op-undo-ai-apply-" + action
			def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
			raw, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{{
				GroupIndex: 0, Action: action, CanonicalName: w.keep.Name, KeepID: w.keep.ID, MergeIDs: []int{w.from.ID},
			}}})
			require.NoError(t, err)
			require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: opID}))
			requireSourceDeleted(t, w.st, w.from.ID)
			for id := range w.before {
				book, err := w.st.GetBookByID(id)
				require.NoError(t, err)
				require.NotNil(t, book.AuthorID)
				require.Equal(t, w.keep.ID, *book.AuthorID, "book %s primary moved to the kept author", id)
			}

			rows, err := w.st.GetOperationChanges(opID)
			require.NoError(t, err)
			require.Len(t, rows, 4, "three credit rows and the delete row")
			for _, r := range rows {
				require.Empty(t, undo.NotRestorableLabel(r), "%s", r.ChangeType)
			}
			report, err := undo.PreflightUndoConflicts(w.st, opID)
			require.NoError(t, err)
			require.Zero(t, report.NotRestorable)
			require.Equal(t, 4, report.TotalChanges)

			res, err := revertOp(t, w.st, opID)
			require.NoError(t, err)
			require.Zero(t, res.Failed, "%+v", res)
			require.Equal(t, 4, res.Restored)
			w.requireRestored(t)

			keepAliases, err := w.st.GetAuthorAliases(w.keep.ID)
			require.NoError(t, err)
			var names []string
			for _, a := range keepAliases {
				names = append(names, a.AliasName)
			}
			if action == "alias" {
				require.Equal(t, []string{w.from.Name}, names, "the pen name the alias action added is not journaled, so it stays")
			} else {
				require.Empty(t, names, "the kept author gives the carried alias back")
			}
		})
	}
}

// An apply with no operation id is refused before anything is written: its rows
// would belong to no operation a revert can name.
func TestAIAuthorMergeApplyOp_RefusesARunWithNoOperationID(t *testing.T) {
	w := newUndoWorld(t)
	def := relinkOpDef(t, newRelinkOpServer(w.st).RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
	raw, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{{
		Action: "merge", CanonicalName: w.keep.Name, KeepID: w.keep.ID, MergeIDs: []int{w.from.ID},
	}}})
	require.NoError(t, err)
	err = def.Run(context.Background(), raw, &opReporter{id: ""})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nothing applied")
	still, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, still.ID, "the author was not deleted")
	for id, want := range w.before {
		got, err := w.st.GetBookAuthors(id)
		require.NoError(t, err)
		require.Equal(t, want, got, "book %s was moved", id)
	}
}

// failPrimaryStore fails ModifyBook for one book: its primary cannot be
// repointed.
type failPrimaryStore struct {
	*database.PebbleStore
	failBook string
}

func (f failPrimaryStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == f.failBook {
		return nil, errors.New("injected ModifyBook failure")
	}
	return f.PebbleStore.ModifyBook(id, fn)
}

// ModifyBookCredits fails the same book: authormerge.MoveCredits repoints a
// primary through it, in one write with the credits.
func (f failPrimaryStore) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if id == f.failBook {
		return nil, database.BookCreditsEdit{}, errors.New("injected ModifyBook failure")
	}
	return f.PebbleStore.ModifyBookCredits(id, fn)
}

// A book whose primary could not be repointed still names the variant, so the
// variant is not deleted, and the run says why.
func TestAIAuthorMergeApplyOp_AFailedPrimaryRepointRefusesTheDelete(t *testing.T) {
	w := newUndoWorld(t)
	def := relinkOpDef(t, newRelinkOpServer(failPrimaryStore{PebbleStore: w.st, failBook: w.soloBook}).RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
	raw, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{{
		Action: "merge", CanonicalName: w.keep.Name, KeepID: w.keep.ID, MergeIDs: []int{w.from.ID},
	}}})
	require.NoError(t, err)
	rep := &opReporter{id: "op-ai-primary-fail"}
	require.NoError(t, def.Run(context.Background(), raw, rep))
	still, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, still.ID, "the variant is not deleted while a book names it as primary")
	logs := strings.Join(rep.logs, "\n")
	require.Contains(t, logs, "move credits and primary author: injected ModifyBook failure",
		"the failed repoint is reported")
	unmoved, err := w.st.GetBookAuthors(w.soloBook)
	require.NoError(t, err)
	require.Equal(t, w.before[w.soloBook], unmoved, "the credits and the primary are one write: neither moved")
	// Held back by the reassign's own failure count, before the delete's
	// re-check is reached.
	require.Contains(t, logs, "NOT deleted: 1 book(s) could not be reassigned")
}

// lateCreditStore credits mergeID on a new book just before the delete's
// re-check (the second relink listing of mergeID), as a write landing between
// the reassign and the delete would.
type lateCreditStore struct {
	*database.PebbleStore
	t        *testing.T
	mergeID  int
	calls    *int
	lateBook *string
}

func (l lateCreditStore) GetBooksByAuthorIDForRelinkCore(authorID int) ([]database.BookCore, error) {
	if authorID == l.mergeID {
		*l.calls++
		if *l.calls == 2 {
			*l.lateBook = mkRelinkOpBook(l.t, l.PebbleStore, "late-credit", l.mergeID, false)
		}
	}
	return l.PebbleStore.GetBooksByAuthorIDForRelinkCore(authorID)
}

// A book credited to the variant after the reassign holds the delete back
// (VerifyAuthorUnlinked): deleting would strip that late credit.
func TestAIAuthorMergeApplyOp_ACreditAddedAfterTheReassignRefusesTheDelete(t *testing.T) {
	w := newUndoWorld(t)
	calls, late := 0, ""
	st := lateCreditStore{PebbleStore: w.st, t: t, mergeID: w.from.ID, calls: &calls, lateBook: &late}
	def := relinkOpDef(t, newRelinkOpServer(st).RegisterAIAuthorMergeApplyOp, handlers.AIAuthorMergeApplyDefID)
	raw, err := json.Marshal(handlers.AIMergeApplyOpParams{Suggestions: []handlers.AIMergeApplySuggestion{{
		Action: "merge", CanonicalName: w.keep.Name, KeepID: w.keep.ID, MergeIDs: []int{w.from.ID},
	}}})
	require.NoError(t, err)
	require.NoError(t, def.Run(context.Background(), raw, &opReporter{id: "op-ai-late-credit"}))
	require.NotEmpty(t, late, "fixture: the late credit was added")
	still, err := w.st.GetAuthorByID(w.from.ID)
	require.NoError(t, err)
	require.Equal(t, w.from.ID, still.ID, "the variant is not deleted while a late book credits it")
	credits, err := w.st.GetBookAuthors(late)
	require.NoError(t, err)
	require.Len(t, credits, 1)
	require.Equal(t, w.from.ID, credits[0].AuthorID, "the late credit is intact")
}
