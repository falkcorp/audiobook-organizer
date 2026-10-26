// file: internal/server/handlers/playlists_failures_test.go
// version: 1.0.0
// guid: 8a3c5e91-4d27-4b60-9f18-e2b7d0c6a453
// last-edited: 2026-10-26

package handlers_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/search"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
)

type playlistDetailBody struct {
	Data struct {
		BookIDs      []string `json:"book_ids"`
		BookIDsTotal int      `json:"book_ids_total"`
		Unavailable  bool     `json:"members_unavailable"`
		Message      string   `json:"members_unavailable_message"`
	} `json:"data"`
}

func indexedPlaylistDocs(t *testing.T, n int) func() *search.BleveIndex {
	t.Helper()
	idx, err := search.Open(t.TempDir() + "/bleve")
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	require.NoError(t, idx.MarkRebuilt())
	require.NoError(t, idx.IndexBookBatch(syntheticDocs(n)))
	return func() *search.BleveIndex { return idx }
}

func getPlaylist(t *testing.T, h *handlers.PlaylistHandler) (*playlistDetailBody, int, string) {
	t.Helper()
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-1", "alice")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.GetPlaylist(c)
	var body playlistDetailBody
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	}
	return &body, w.Code, w.Body.String()
}

// NIT 13: a stored definition the evaluator cannot use is the playlist's problem,
// not a bad request from the person reading it. A query that no longer parses and
// a sort spec that is not JSON read as "no books", with a way to fix them, the
// cached list is dropped, and nothing from the parser is echoed.
func TestPlaylists_AStoredDefinitionTheEvaluatorCannotUseReadsAsUnavailable(t *testing.T) {
	for _, tc := range []struct{ name, query, sort string }{
		{"a query that does not parse", "title:(unclosed", ""},
		{"a sort spec that is not JSON", "Synthetic", "not json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pl := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
				Query: tc.query, SortJSON: tc.sort, CreatedByUserID: "alice",
				MaterializedBookIDs: []string{"s00001"}, MaterializedTotal: 1}
			store := handlersmocks.NewMockPlaylistStore(t)
			store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
			store.EXPECT().GetBookByID(mock.Anything).Return(&database.Book{}, nil).Maybe()
			var saved *database.UserPlaylist
			store.EXPECT().UpdateUserPlaylist(pl).RunAndReturn(func(p *database.UserPlaylist) error {
				cp := *p
				saved = &cp
				return nil
			})
			h := handlers.NewPlaylistHandler(store, indexedPlaylistDocs(t, 3))
			body, code, raw := getPlaylist(t, h)
			require.Equal(t, http.StatusOK, code, raw)
			assert.True(t, body.Data.Unavailable)
			assert.Empty(t, body.Data.BookIDs)
			assert.Contains(t, body.Data.Message, "Edit the playlist")
			assert.NotContains(t, body.Data.Message, "unclosed", "no parser text")
			require.NotNil(t, saved, "the cached list is cleared")
			assert.Empty(t, saved.MaterializedBookIDs)
		})
	}
}

// NIT 4: a per-user refusal leads into the sentence the person got at save time
// once, not as "...shows no books: query: read_status must be ...".
func TestPlaylists_TheUnavailableMessageDoesNotRepeatAWrapper(t *testing.T) {
	pl := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
		Query: "read_status:completed", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
	h := handlers.NewPlaylistHandler(store, noSearchIndex)
	body, code, raw := getPlaylist(t, h)
	require.Equal(t, http.StatusOK, code, raw)
	assert.Contains(t, body.Data.Message, "shows no books: read_status must be one of")
	assert.NotContains(t, body.Data.Message, "query:")
	assert.NotContains(t, body.Data.Message, "search:")
}

// NIT 13: a failure that is the server's (a viewer-state read that fails) is a 500,
// as it is for collections, never a 400 with the error text; the viewer's id and the
// book id in that text stay out of the response.
func TestPlaylists_AStateReadFailureIsAServerErrorWithoutTheViewersIds(t *testing.T) {
	pl := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
		Query: "Synthetic read_status:finished", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t) // nothing is written
	store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
	store.EXPECT().GetUserBookState(mock.Anything, mock.Anything).
		Return(nil, errors.New("decode alice/s00001: unexpected end of JSON input"))
	h := handlers.NewPlaylistHandler(store, indexedPlaylistDocs(t, 3))
	_, code, raw := getPlaylist(t, h)
	assert.Equal(t, http.StatusInternalServerError, code, raw)
	assert.NotContains(t, raw, "alice")
	assert.NotContains(t, raw, "s00001")

	// Materialize answers the same way.
	src := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
		Query: "Synthetic read_status:finished", CreatedByUserID: "alice"}
	store2 := handlersmocks.NewMockPlaylistStore(t) // no snapshot is created
	store2.EXPECT().GetUserPlaylist("pl-1").Return(src, nil)
	store2.EXPECT().GetUserBookState(mock.Anything, mock.Anything).
		Return(nil, errors.New("decode alice/s00001: unexpected end of JSON input"))
	h2 := handlers.NewPlaylistHandler(store2, indexedPlaylistDocs(t, 3))
	c, w := playlistRequest(t, http.MethodPost, "/playlists/pl-1/materialize", "alice", map[string]any{})
	h2.MaterializePlaylist(c)
	assert.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "alice")
}

// Materializing a playlist whose own query is broken is the caller's to fix: 400.
func TestPlaylists_MaterializingABrokenDefinitionIsABadRequest(t *testing.T) {
	src := &database.UserPlaylist{ID: "pl-1", Name: "Hush", Type: database.UserPlaylistTypeSmart,
		Query: "title:(unclosed", CreatedByUserID: "alice"}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(src, nil)
	h := handlers.NewPlaylistHandler(store, indexedPlaylistDocs(t, 3))
	c, w := playlistRequest(t, http.MethodPost, "/playlists/pl-1/materialize", "alice", map[string]any{})
	h.MaterializePlaylist(c)
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
}

// NIT 3: a snapshot cut to the member bound says how many books its query matched,
// on the page that shows it. Once the list is edited it stops saying so: an add or a
// remove changes its length, and the count would describe a list that is gone.
func TestPlaylists_ACutSnapshotReportsItsTotalUntilItIsEdited(t *testing.T) {
	ids := make([]string, database.MaxMaterializedMembers)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%05d", i)
	}
	total := database.MaxMaterializedMembers + 2000
	for _, tc := range []struct {
		name  string
		list  []string
		total int
		want  int
	}{
		{"a snapshot at the bound", ids, total, total},
		{"a snapshot after a remove", ids[1:], total, 0},
		{"a snapshot after an add", append(append([]string{}, ids...), "s99999"), total, 0},
		{"a static list that was never cut", ids[:10], 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pl := &database.UserPlaylist{ID: "pl-1", Name: "Hush (snapshot)", Type: database.UserPlaylistTypeStatic,
				BookIDs: tc.list, MaterializedTotal: tc.total, CreatedByUserID: "alice"}
			store := handlersmocks.NewMockPlaylistStore(t)
			store.EXPECT().GetUserPlaylist("pl-1").Return(pl, nil)
			h := handlers.NewPlaylistHandler(store, noSearchIndex)
			body, code, raw := getPlaylist(t, h)
			require.Equal(t, http.StatusOK, code, raw)
			assert.Equal(t, tc.want, body.Data.BookIDsTotal)
			assert.Len(t, body.Data.BookIDs, len(tc.list))
		})
	}
}
