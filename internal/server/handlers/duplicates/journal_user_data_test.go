// file: internal/server/handlers/duplicates/journal_user_data_test.go
// version: 1.1.1
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
	duplicatesmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates/mocks"
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
		h := duplicates.New(f.S, nil, nil, nil, func() duplicates.MergeService { return ms }, nil)
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
	w := doReq(t, duplicates.New(f.S, nil, nil, nil, func() duplicates.MergeService { return ms }, nil).ListCombineJournals,
		http.MethodGet, "/merge/combine-journal", nil)
	require.Contains(t, w.Body.String(), "invented library warning", "a warning naming no user stays")
}

// The strip covers every user-naming field of both journals -- each absorbed
// book's and each state follow's progress, the warnings and the last error --
// keeps what names no user, and works on a copy: the journals the merge
// service handed over still hold everything after the response is written.
func TestJournalLists_StripWorksOnACopy(t *testing.T) {
	const listener = "invented-copy-listener"
	progress := []merge.CombineUserProgress{{UserID: listener}}
	combine := []merge.CombineJournal{{
		ID: "01JCJCOPY", SurvivorID: "book-invented-s",
		Absorbed:  []merge.CombineAbsorbed{{BookID: "book-invented-a", Progress: progress}},
		Warnings:  []string{"user " + listener + " progress on survivor book-invented-s changed after the merge; left as is", "invented combine library warning"},
		LastError: "put back progress of user " + listener + ": invented",
	}}
	sibling := []merge.SiblingMoveJournal{{
		ID: "01JSJCOPY", SurvivorID: "book-invented-s",
		StateFollows: []merge.StateFollow{{LoserID: "book-invented-l", HolderID: "book-invented-h", Progress: progress}},
		Warnings:     []string{"user " + listener + " progress on survivor book-invented-h was put back; left as is", "invented sibling library warning"},
		LastError:    "invented sibling error naming user " + listener,
	}}
	ms := duplicatesmocks.NewMockMergeService(t)
	ms.EXPECT().ListCombineJournals(50).Return(combine, nil)
	ms.EXPECT().ListSiblingMoveJournals(50).Return(sibling, nil)
	h := duplicates.New(nil, nil, nil, nil, func() duplicates.MergeService { return ms }, nil)

	var cEnv struct {
		Data struct {
			Journals []merge.CombineJournal `json:"journals"`
		} `json:"data"`
	}
	w := doReq(t, h.ListCombineJournals, http.MethodGet, "/merge/combine-journal", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), listener)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &cEnv))
	require.Len(t, cEnv.Data.Journals, 1)
	got := cEnv.Data.Journals[0]
	require.Equal(t, "book-invented-a", got.Absorbed[0].BookID)
	require.Empty(t, got.Absorbed[0].Progress)
	require.Equal(t, []string{"invented combine library warning"}, got.Warnings)
	require.Empty(t, got.LastError)

	var sEnv struct {
		Data struct {
			Journals []merge.SiblingMoveJournal `json:"journals"`
		} `json:"data"`
	}
	w = doReq(t, h.ListSiblingMoveJournals, http.MethodGet, "/merge/sibling-journal", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotContains(t, w.Body.String(), listener)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &sEnv))
	require.Len(t, sEnv.Data.Journals, 1)
	gotS := sEnv.Data.Journals[0]
	require.Equal(t, "book-invented-l", gotS.StateFollows[0].LoserID)
	require.Empty(t, gotS.StateFollows[0].Progress)
	require.Equal(t, []string{"invented sibling library warning"}, gotS.Warnings)
	require.Empty(t, gotS.LastError)

	// The service's journals are untouched.
	require.Len(t, combine[0].Absorbed[0].Progress, 1)
	require.Len(t, combine[0].Warnings, 2)
	require.Contains(t, combine[0].LastError, listener)
	require.Len(t, sibling[0].StateFollows[0].Progress, 1)
	require.Len(t, sibling[0].Warnings, 2)
	require.Contains(t, sibling[0].LastError, listener)
}
