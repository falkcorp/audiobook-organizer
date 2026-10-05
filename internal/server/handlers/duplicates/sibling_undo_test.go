// file: internal/server/handlers/duplicates/sibling_undo_test.go
// version: 1.1.0
// guid: cefc1ee2-288b-4ccf-a693-5940c218a848
// last-edited: 2026-10-05

package duplicates_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/duplicates"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// Link-as-versions writes no dedup journal. It returns the sibling-move
// journal id merge.Service wrote, and POST /merge/sibling-undo/:id puts the
// loser's sibling back in its group as that group's primary.
func TestLinkBookDuplicatesAsVersions_SiblingMoveUndoEndpoint(t *testing.T) {
	f := vptest.New(t)
	prev := config.AppConfig.RootDir
	config.AppConfig.RootDir = f.Root
	t.Cleanup(func() { config.AppConfig.RootDir = prev })
	k := f.Book(t, vptest.Spec{ID: "k", Group: "H", Primary: "true"})
	l := f.Book(t, vptest.Spec{ID: "l", Group: "G", Primary: "true", NoFile: true})
	ls := f.Book(t, vptest.Spec{ID: "ls", Group: "G", Primary: "false"})

	ms := merge.NewService(f.S)
	h := duplicates.New(f.S, nil, nil, nil, nil,
		func() duplicates.MergeService { return ms }, nil, nil, nil, nil)

	// No explicit primary on this endpoint: L has no file, so K wins.
	w := doReq(t, h.LinkBookDuplicatesAsVersions, http.MethodPost, "/audiobooks/duplicates/link",
		map[string]any{"book_ids": []string{l, k}})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var env struct {
		Data struct {
			PrimaryID        string `json:"primary_id"`
			SiblingJournalID string `json:"sibling_journal_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), w.Body.String())
	body := env.Data
	require.Equal(t, k, body.PrimaryID)
	require.NotEmpty(t, body.SiblingJournalID)
	require.Equal(t, "H", f.GroupOf(t, ls))

	uw := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(uw)
	c.Request = httptest.NewRequest(http.MethodPost, "/merge/sibling-undo/"+body.SiblingJournalID, nil)
	c.Params = gin.Params{{Key: "journal_id", Value: body.SiblingJournalID}}
	h.UndoSiblingMove(c)
	require.Equal(t, http.StatusOK, uw.Code, uw.Body.String())

	require.Equal(t, "G", f.GroupOf(t, ls))
	f.RequireSinglePrimary(t, "G", ls)
	f.RequireSinglePrimary(t, "H", k)

	// A repeat is refused with 409, not a 500 and not a second restore.
	rw := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rw)
	c.Request = httptest.NewRequest(http.MethodPost, "/merge/sibling-undo/"+body.SiblingJournalID, nil)
	c.Params = gin.Params{{Key: "journal_id", Value: body.SiblingJournalID}}
	h.UndoSiblingMove(c)
	require.Equal(t, http.StatusConflict, rw.Code, rw.Body.String())
	require.Equal(t, "G", f.GroupOf(t, ls))

	nw := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(nw)
	c.Request = httptest.NewRequest(http.MethodPost, "/merge/sibling-undo/nope", nil)
	c.Params = gin.Params{{Key: "journal_id", Value: "nope"}}
	h.UndoSiblingMove(c)
	require.Equal(t, http.StatusNotFound, nw.Code)
}

// The list caps its page: limit=0 (or none) is the default of 50, and a
// larger limit is capped at 500.
func TestListSiblingMoveJournals_LimitCap(t *testing.T) {
	f := vptest.New(t)
	for i := 0; i < 501; i++ {
		id := fmt.Sprintf("01J%023d", i)
		data, err := json.Marshal(merge.SiblingMoveJournal{ID: id, Status: merge.SiblingJournalApplied})
		require.NoError(t, err)
		require.NoError(t, f.S.SetRaw("merge:sibling-journal:"+id, data))
	}
	ms := merge.NewService(f.S)
	h := duplicates.New(f.S, nil, nil, nil, nil,
		func() duplicates.MergeService { return ms }, nil, nil, nil, nil)
	count := func(url string) int {
		w := doReq(t, h.ListSiblingMoveJournals, http.MethodGet, url, nil)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var env struct {
			Data struct {
				Count int `json:"count"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
		return env.Data.Count
	}
	require.Equal(t, 50, count("/merge/sibling-journal"))
	require.Equal(t, 50, count("/merge/sibling-journal?limit=0"))
	require.Equal(t, 7, count("/merge/sibling-journal?limit=7"))
	require.Equal(t, 500, count("/merge/sibling-journal?limit=100000"))
}
