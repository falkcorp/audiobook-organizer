// file: internal/server/handlers/playlists_export_test.go
// version: 1.0.1
// guid: a78e2cb9-f69e-40d6-9608-2bd148462654
// last-edited: 2026-09-02

package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func exportBook(id, title, path string, dur *int) *database.Book {
	return &database.Book{ID: id, Title: title, FilePath: path, Duration: dur}
}

// callExport runs ExportPlaylistM3U for pl as userA and returns the recorder.
func callExport(t *testing.T, store *handlersmocks.MockPlaylistStore) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	h := handlers.NewPlaylistHandler(store, nil)
	c, w := newPlaylistCtxAs(http.MethodGet, "/playlists/pl-1/export.m3u", "userA")
	c.Params = gin.Params{{Key: "id", Value: "pl-1"}}
	h.ExportPlaylistM3U(c)
	return c, w
}

// TestExportPlaylistM3U_EmitsExtinfPairs pins the actual file body, not just a
// 200: an export that returns the wrong paths is far worse than one that errors,
// because the user only finds out when the playlist fails to play elsewhere.
func TestExportPlaylistM3U_EmitsExtinfPairs(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
		ID: "pl-1", Name: "Road Trip", Type: database.UserPlaylistTypeStatic,
		BookIDs: []string{"b1", "b2"},
	}, nil)
	store.EXPECT().GetBookByID("b1").Return(exportBook("b1", "First Book", "/lib/first.m4b", new(3600)), nil)
	store.EXPECT().GetBookByID("b2").Return(exportBook("b2", "Second Book", "/lib/second.m4b", new(120)), nil)

	_, w := callExport(t, store)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "audio/x-mpegurl; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "#EXTM3U\n"+
		"#EXTINF:3600,First Book\n/lib/first.m4b\n"+
		"#EXTINF:120,Second Book\n/lib/second.m4b\n", w.Body.String())
}

// TestExportPlaylistM3U_FilenameCannotEscapeOrInjectHeaders is the highest-risk
// case: the attachment filename is built from a user-chosen playlist name. A
// name containing path separators, quotes or CRLF must not escape the filename
// or split the Content-Disposition header into a second header.
func TestExportPlaylistM3U_FilenameCannotEscapeOrInjectHeaders(t *testing.T) {
	for _, tc := range []struct {
		name        string
		playlist    string
		mustNotHave []string
	}{
		{"path traversal", "../../etc/passwd", []string{"/"}},
		{"crlf header injection", "eviL\r\nX-Injected: yes", []string{"\r", "\n"}},
		{"quote breaks out of filename", `a"b`, []string{`"b`}},
		{"backslash traversal", `..\..\windows\system32`, []string{`\\`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := handlersmocks.NewMockPlaylistStore(t)
			store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
				ID: "pl-1", Name: tc.playlist, Type: database.UserPlaylistTypeStatic,
			}, nil)

			_, w := callExport(t, store)

			require.Equal(t, http.StatusOK, w.Code)
			cd := w.Header().Get("Content-Disposition")
			for _, bad := range tc.mustNotHave {
				assert.NotContains(t, cd, bad, "Content-Disposition %q still carries %q", cd, bad)
			}
			// Exactly one header, and it still ends in .m3u -- proves nothing
			// was split off into a second header line.
			assert.Len(t, w.Header().Values("Content-Disposition"), 1)
			assert.Empty(t, w.Header().Get("X-Injected"))
			assert.True(t, strings.HasSuffix(cd, `.m3u"`), "want a .m3u attachment, got %q", cd)
			// A ".." with no separator left beside it cannot traverse anything,
			// so it is not asserted against -- the property that matters is that
			// no path separator survives and the name cannot start with a dot.
			name := strings.TrimSuffix(strings.TrimPrefix(cd, `attachment; filename="`), `"`)
			assert.NotContains(t, name, "/", "path separator survived in %q", name)
			assert.NotContains(t, name, `\`, "path separator survived in %q", name)
			assert.False(t, strings.HasPrefix(name, "."), "filename starts with a dot: %q", name)
		})
	}
}

// TestExportPlaylistM3U_TitleWithCommaAndNewline: a comma is legal in an
// #EXTINF title (the format splits on the FIRST comma only) and must survive
// verbatim; a newline is not and must be flattened, or it would forge an extra
// entry in the playlist.
func TestExportPlaylistM3U_TitleWithCommaAndNewline(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
		ID: "pl-1", Name: "Edge", Type: database.UserPlaylistTypeStatic,
		BookIDs: []string{"b1"},
	}, nil)
	store.EXPECT().GetBookByID("b1").Return(
		exportBook("b1", "Hello, World\n#EXTINF:1,forged\n/etc/passwd", "/lib/x.m4b", new(10)), nil)

	_, w := callExport(t, store)

	body := w.Body.String()
	assert.Contains(t, body, "Hello, World", "comma must survive verbatim")
	// Exactly one #EXTINF *line*. The forged "#EXTINF:1,forged" is flattened
	// onto the end of the title and is inert there: an m3u reader takes the
	// title as everything after the FIRST comma, so it can never become a
	// second entry. Counting the substring would measure the wrong thing --
	// what matters is that no new LINE was forged.
	var extinfLines, total int
	for ln := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
		total++
		if strings.HasPrefix(ln, "#EXTINF:") {
			extinfLines++
		}
	}
	assert.Equal(t, 1, extinfLines, "forged a second #EXTINF line:\n%s", body)
	assert.Equal(t, 3, total, "want header + extinf + path, got:\n%s", body)
	assert.NotContains(t, body, "\n/etc/passwd", "forged path became its own line:\n%s", body)
}

// TestExportPlaylistM3U_NonASCIITitleIsUTF8 -- the library has non-ASCII titles
// and the Content-Type promises UTF-8.
func TestExportPlaylistM3U_NonASCIITitleIsUTF8(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
		ID: "pl-1", Name: "Intl", Type: database.UserPlaylistTypeStatic,
		BookIDs: []string{"b1"},
	}, nil)
	store.EXPECT().GetBookByID("b1").Return(
		exportBook("b1", "Sōsuke no Bōken — Ünïcode", "/lib/ünï.m4b", new(5)), nil)

	_, w := callExport(t, store)

	assert.Contains(t, w.Body.String(), "Sōsuke no Bōken — Ünïcode")
	assert.Contains(t, w.Body.String(), "/lib/ünï.m4b")
}

// TestExportPlaylistM3U_EmptyAndStaleEntries: an empty playlist is a valid
// header-only file, not an error, and a book that no longer resolves is dropped
// rather than written as a blank line that would break the reader.
func TestExportPlaylistM3U_EmptyAndStaleEntries(t *testing.T) {
	t.Run("empty playlist is header only", func(t *testing.T) {
		store := handlersmocks.NewMockPlaylistStore(t)
		store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
			ID: "pl-1", Name: "Empty", Type: database.UserPlaylistTypeStatic,
		}, nil)
		_, w := callExport(t, store)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "#EXTM3U\n", w.Body.String())
	})

	t.Run("stale and pathless books are dropped, good ones kept", func(t *testing.T) {
		store := handlersmocks.NewMockPlaylistStore(t)
		store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
			ID: "pl-1", Name: "Mixed", Type: database.UserPlaylistTypeStatic,
			BookIDs: []string{"gone", "nopath", "good"},
		}, nil)
		store.EXPECT().GetBookByID("gone").Return(nil, nil)
		store.EXPECT().GetBookByID("nopath").Return(exportBook("nopath", "No Path", "", new(1)), nil)
		store.EXPECT().GetBookByID("good").Return(exportBook("good", "Good", "/lib/g.m4b", new(7)), nil)

		_, w := callExport(t, store)

		// The positive control matters: without it this would pass just as
		// happily if the handler dropped everything.
		assert.Equal(t, "#EXTM3U\n#EXTINF:7,Good\n/lib/g.m4b\n", w.Body.String())
	})
}

// TestExportPlaylistM3U_SmartUsesMaterializedIDs -- a smart playlist exports
// its last evaluation, and one never materialized exports a header-only file
// rather than erroring.
func TestExportPlaylistM3U_SmartUsesMaterializedIDs(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(&database.UserPlaylist{
		ID: "pl-1", Name: "Smart", Type: database.UserPlaylistTypeSmart,
		BookIDs:             []string{"ignored"},
		MaterializedBookIDs: []string{"m1"},
	}, nil)
	store.EXPECT().GetBookByID("m1").Return(exportBook("m1", "Materialized", "/lib/m.m4b", new(9)), nil)

	_, w := callExport(t, store)

	body := w.Body.String()
	assert.Contains(t, body, "/lib/m.m4b")
	assert.NotContains(t, body, "ignored", "static BookIDs must not be used for a smart playlist")
}

// TestExportPlaylistM3U_CrossUser_Returns404 -- same IDOR guard as the other
// playlist reads: another user's playlist is 404, not 403, and its contents
// are never fetched.
func TestExportPlaylistM3U_CrossUser_Returns404(t *testing.T) {
	store := handlersmocks.NewMockPlaylistStore(t)
	store.EXPECT().GetUserPlaylist("pl-1").Return(otherUsersPlaylist(), nil)

	_, w := callExport(t, store)

	assert.Equal(t, http.StatusNotFound, w.Code)
}
