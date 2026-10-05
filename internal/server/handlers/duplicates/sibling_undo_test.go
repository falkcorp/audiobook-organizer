// file: internal/server/handlers/duplicates/sibling_undo_test.go
// version: 1.0.0
// guid: cefc1ee2-288b-4ccf-a693-5940c218a848
// last-edited: 2026-10-05

package duplicates_test

import (
	"encoding/json"
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

	nw := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(nw)
	c.Request = httptest.NewRequest(http.MethodPost, "/merge/sibling-undo/nope", nil)
	c.Params = gin.Params{{Key: "journal_id", Value: "nope"}}
	h.UndoSiblingMove(c)
	require.Equal(t, http.StatusNotFound, nw.Code)
}
