// file: internal/server/handlers/duplicates/journal_user_data_test.go
// version: 1.0.0
// guid: 41be6667-4e23-4fde-9bef-f3782b221fbe
// last-edited: 2026-10-25

package duplicates_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// The journal lists strip every user's progress, and the warnings naming a
// user, unless the check says the caller may read other users' data; with no
// check installed they strip for everyone (fail closed). What is stripped is
// a copy: the stored journal keeps its progress.
func TestJournalLists_StripOtherUsersData(t *testing.T) {
	const listener = "invented-journal-listener"
	f := vptest.New(t)
	progress := []merge.CombineUserProgress{{UserID: listener,
		AbsorbedState: &database.UserBookState{UserID: listener, BookID: "book-invented-a", Status: "finished"}}}
	put := func(key string, v any) {
		data, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, f.S.SetRaw(key, data))
	}
	put("merge:combine-journal:01JCJ", merge.CombineJournal{ID: "01JCJ", Status: "applied", SurvivorID: "book-invented-s",
		Absorbed: []merge.CombineAbsorbed{{BookID: "book-invented-a", Progress: progress}},
		Warnings: []string{"progress of user " + listener + " on book-invented-a skipped", "invented library warning"}})
	put("merge:sibling-journal:01JSJ", merge.SiblingMoveJournal{ID: "01JSJ", Status: merge.SiblingJournalApplied,
		StateFollows: []merge.StateFollow{{LoserID: "book-invented-l", HolderID: "book-invented-h", Progress: progress}}})
	ms := merge.NewService(f.S)

	for name, tc := range map[string]struct {
		check func(*gin.Context) bool
		sees  bool
	}{
		"no check installed": {nil, false},
		"check refuses":      {func(*gin.Context) bool { return false }, false},
		"check allows":       {func(*gin.Context) bool { return true }, true},
	} {
		h := duplicates.New(f.S, nil, nil, nil, func() duplicates.MergeService { return ms }, nil, nil)
		if tc.check != nil {
			h = h.WithOthersUserDataAccess(tc.check)
		}
		for url, fn := range map[string]gin.HandlerFunc{
			"/merge/combine-journal": h.ListCombineJournals,
			"/merge/sibling-journal": h.ListSiblingMoveJournals,
		} {
			w := doReq(t, fn, http.MethodGet, url, nil)
			require.Equal(t, http.StatusOK, w.Code, "%s %s: %s", name, url, w.Body.String())
			body := w.Body.String()
			require.Contains(t, body, "book-invented-", "%s %s: the journal stays", name, url)
			if tc.sees {
				require.Contains(t, body, listener, "%s %s", name, url)
				continue
			}
			require.NotContains(t, body, listener, "%s %s", name, url)
			require.NotContains(t, body, `"progress"`, "%s %s", name, url)
		}
	}
	w := doReq(t, duplicates.New(f.S, nil, nil, nil, func() duplicates.MergeService { return ms }, nil, nil).ListCombineJournals,
		http.MethodGet, "/merge/combine-journal", nil)
	require.Contains(t, w.Body.String(), "invented library warning", "a warning naming no user stays")

	stored, err := ms.ListCombineJournals(0)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Len(t, stored[0].Absorbed[0].Progress, 1, "the store's journal keeps its progress")
}
