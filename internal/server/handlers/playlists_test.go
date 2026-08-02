// file: internal/server/handlers/playlists_test.go
// version: 1.0.0
// guid: f1e2d3c4-b5a6-7890-cdef-1234567890ab
// last-edited: 2026-06-03

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// newPlaylistCtxAs builds a gin context whose CallingUserID resolves to userID
// (via the "auth_user" context key read by servermiddleware.CurrentUser).
func newPlaylistCtxAs(method, path, userID string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	c.Set("auth_user", &database.User{ID: userID})
	return c, w
}

// otherUsersPlaylist is a static playlist owned by "userB".
func otherUsersPlaylist() *database.UserPlaylist {
	return &database.UserPlaylist{
		ID:              "pl-1",
		Name:            "userB private",
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         []string{"b1"},
		CreatedByUserID: "userB",
	}
}

// TestPlaylistHandler_GetPlaylist_CrossUser_Returns404 confirms that user A
// cannot read user B's playlist by ID — the response is 404 (not 200, not 403)
// so the existence of another user's playlist is not disclosed (IDOR guard).
func TestPlaylistHandler_GetPlaylist_CrossUser_Returns404(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(otherUsersPlaylist(), nil)

	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-1", "userA")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.GetPlaylist(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestPlaylistHandler_DeletePlaylist_CrossUser_Returns404 confirms user A
// cannot delete user B's playlist. DeleteUserPlaylist must never be reached.
func TestPlaylistHandler_DeletePlaylist_CrossUser_Returns404(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(otherUsersPlaylist(), nil)
	// DeleteUserPlaylist is intentionally NOT expected — reaching it is a bug.

	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodDelete, "/playlists/pl-1", "userA")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.DeletePlaylist(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestPlaylistHandler_UpdatePlaylist_CrossUser_Returns404 confirms user A
// cannot mutate user B's playlist; UpdateUserPlaylist must never be reached.
func TestPlaylistHandler_UpdatePlaylist_CrossUser_Returns404(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(otherUsersPlaylist(), nil)

	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodPut, "/playlists/pl-1", "userA")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.UpdatePlaylist(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestPlaylistHandler_GetPlaylist_Owner_Succeeds confirms the owner still has
// access — the ownership check must not lock out the legitimate user.
func TestPlaylistHandler_GetPlaylist_Owner_Succeeds(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(otherUsersPlaylist(), nil)

	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-1", "userB") // owner
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.GetPlaylist(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestPlaylistHandler_GetPlaylist_LegacyUnowned_Accessible confirms that a
// playlist with an empty CreatedByUserID (legacy / pre-ownership iTunes import)
// remains accessible to any caller — the IDOR fix must not hide pre-existing data.
func TestPlaylistHandler_GetPlaylist_LegacyUnowned_Accessible(t *testing.T) {
	legacy := &database.UserPlaylist{
		ID:              "pl-legacy",
		Name:            "iTunes import",
		Type:            database.UserPlaylistTypeStatic,
		BookIDs:         []string{"b1"},
		CreatedByUserID: "", // unowned — created before ownership tracking
	}
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-legacy").Return(legacy, nil)

	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-legacy", "userA")
	c.Params = gin.Params{{Key: "id", Value: "pl-legacy"}}
	h.GetPlaylist(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

// TestPlaylistHandler_ListPlaylists_ScopesToCaller confirms the list endpoint
// passes the calling user's ID to the store so only that user's playlists are
// returned (no cross-user disclosure).
func TestPlaylistHandler_ListPlaylists_ScopesToCaller(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().
		ListUserPlaylistsForUser("userA", "", mock.Anything, mock.Anything).
		Return([]database.UserPlaylist{}, 0, nil)

	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists", "userA")
	h.ListPlaylists(c)

	assert.Equal(t, http.StatusOK, w.Code)
}
