// file: internal/server/handlers/diagnostics_primary_handoff_test.go
// version: 1.1.0
// guid: 6f2a8c35-9d41-4e7b-a0c6-3b5e7d1f9a24
// last-edited: 2026-10-05

package handlers_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// diagOpStore is a real PebbleStore that serves one canned diagnostics
// operation.
type diagOpStore struct {
	*database.PebbleStore
	op *database.OperationV2Row
}

func (s diagOpStore) GetOperationV2(string) (*database.OperationV2Row, error) { return s.op, nil }

// A delete_orphan suggestion that marks a group's primary deleted hands the
// flag on to the group's library copy.
func TestDiagnosticsHandler_ApplySuggestions_DeleteOrphanHandsPrimaryOn(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	inc := f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
	next := f.Book(t, vptest.Spec{ID: "next", Group: "g", Primary: "false"})

	store := diagOpStore{PebbleStore: f.S, op: diagSuggestionOp(`{"id":"s1","action":"delete_orphan","book_ids":["` + inc + `"]}`)}
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"applied":1`)
	f.RequireSinglePrimary(t, "g", next)
}

// A merge_versions suggestion carries the loser's version sibling into the
// survivor's group. The diagnostics apply writes no dedup journal and drops
// merge.Result, so the sibling-move journal merge.Service writes is the undo:
// it puts the sibling back in its group as that group's primary.
func TestDiagnosticsHandler_ApplySuggestions_MergeVersionsSiblingMoveIsUndoable(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true"})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})

	store := diagOpStore{PebbleStore: f.S, op: diagSuggestionOp(`{"id":"s1","action":"merge_versions","book_ids":["` + l + `","` + k + `"],"primary_id":"` + k + `"}`)}
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil)
	c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
		`{"operation_id":"op-1","approved_suggestion_ids":["s1"]}`, nil)
	h.ApplySuggestions(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), `"applied":1`)
	require.Equal(t, "H", f.GroupOf(t, ls))

	ms := merge.NewService(f.S)
	js, err := ms.ListSiblingMoveJournals(0)
	require.NoError(t, err)
	require.Len(t, js, 1)
	res, err := ms.UndoSiblingMove(js[0].ID)
	require.NoError(t, err)
	require.Equal(t, []string{ls}, res.Restored)
	require.Equal(t, "G", f.GroupOf(t, ls))
	f.RequireSinglePrimary(t, "G", ls)
	f.RequireSinglePrimary(t, "H", k)
}
