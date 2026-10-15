// file: internal/server/ai_merge_apply_undo_test.go
// version: 1.0.0
// guid: 9c4a7e21-3b58-4f06-8d1e-6a2f0b7c5e93
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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
