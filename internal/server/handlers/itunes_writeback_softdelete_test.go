// file: internal/server/handlers/itunes_writeback_softdelete_test.go
// version: 1.0.0
// guid: 7f3c9d21-4b86-4a70-9e15-8c02da6f3b47
// last-edited: 2026-08-14

package handlers_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	handlersmocks "github.com/falkcorp/audiobook-organizer/internal/server/handlers/mocks"
	"github.com/stretchr/testify/require"
)

// writeMinimalITunesLibrary drops a two-track plist in a temp dir and returns
// its path. WriteBackPreview parses the library before it looks at any book, so
// there is no reaching the code under test without one.
func writeMinimalITunesLibrary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Library.xml")
	const body = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>100</key>
		<dict>
			<key>Track ID</key><integer>100</integer>
			<key>Persistent ID</key><string>LIVEPID000000001</string>
			<key>Name</key><string>Kept Book</string>
			<key>Location</key><string>file://localhost/lib/kept.m4b</string>
		</dict>
		<key>200</key>
		<dict>
			<key>Track ID</key><integer>200</integer>
			<key>Persistent ID</key><string>TRASHPID00000002</string>
			<key>Name</key><string>Trashed Book</string>
			<key>Location</key><string>file://localhost/lib/trashed.m4b</string>
		</dict>
	</dict>
</dict>
</plist>
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

// TestITunesHandler_WriteBackPreview_ExcludesSoftDeletedByExplicitID is the
// regression test named in todo.d/2026-08-13-itunes-pid-listing-includes-trash.md:
// a soft-deleted book with an iTunes PID must not appear in the writeback
// preview.
//
// It targets the EXPLICIT book_ids branch specifically. The other branch calls
// ListBooksByITunesPID, which now excludes the trash at the store and is gated
// by TestListBooksByITunesPID_ExcludesTrashOnBothPaths. This branch never
// touches that method — it calls GetBookByID, which returns soft-deleted rows
// on purpose, because that is how a restore reads one back. So fixing the store
// alone left the preview reachable by naming an ID, and only a test at this
// layer can tell the difference between the two routes.
//
// Why it matters: the preview is what decides which metadata is offered for
// writing back into the iTunes library. Prod carried 3,953 soft-deleted books
// as of 2026-08-14, and any of them holding a persistent ID were eligible.
func TestITunesHandler_WriteBackPreview_ExcludesSoftDeletedByExplicitID(t *testing.T) {
	orig := config.AppConfig
	defer func() { config.AppConfig = orig }()
	config.AppConfig.ITunes.LibraryReadPath = writeMinimalITunesLibrary(t)

	livePID := "LIVEPID000000001"
	trashPID := "TRASHPID00000002"
	deleted := true

	store := handlersmocks.NewMockITunesStore(t)
	store.EXPECT().GetBookByID("live-1").Return(&database.Book{
		ID: "live-1", Title: "Kept Book", FilePath: "/lib/kept.m4b",
		ITunesPersistentID: &livePID,
	}, nil)
	store.EXPECT().GetBookByID("trashed-1").Return(&database.Book{
		ID: "trashed-1", Title: "Trashed Book", FilePath: "/lib/trashed.m4b",
		ITunesPersistentID: &trashPID,
		MarkedForDeletion:  &deleted,
	}, nil)

	h := handlers.NewITunesHandler(enabledSvc(t), nil, nil, store)
	c, w := newITunesCtx(http.MethodPost, "/itunes/write-back/preview",
		`{"book_ids":["live-1","trashed-1"]}`, nil)
	h.WriteBackPreview(c)

	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var resp struct {
		Data struct {
			Items []struct {
				BookID             string `json:"book_id"`
				ITunesPersistentID string `json:"itunes_persistent_id"`
			} `json:"items"`
			Total int `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

	ids := make([]string, 0, len(resp.Data.Items))
	for _, it := range resp.Data.Items {
		ids = append(ids, it.BookID)
	}

	// Both halves asserted. Requiring only the absence of the trashed book
	// would also pass if the handler had returned nothing at all, which is a
	// different bug wearing the same green.
	require.Contains(t, ids, "live-1", "live iTunes-mapped book missing from the preview")
	require.NotContains(t, ids, "trashed-1",
		"soft-deleted book reached the writeback preview — its metadata would be offered "+
			"for writing back into the iTunes library")
	require.Equal(t, 1, resp.Data.Total, "Total must count the filtered set, not the requested one")
}
