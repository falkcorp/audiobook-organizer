// file: internal/deluge/integration.go
// version: 1.1.0
// guid: e5f6a7b8-c9d0-1234-ef01-345678901234
// last-edited: 2026-07-03
//
// Deluge integration for library centralization.
//
// When a book version that came from Deluge is swapped, trashed,
// or its files are reorganized, we need to update the torrent's
// save_path in Deluge so it keeps seeding from the new location.
//
// The Deluge client is optional — if not configured, the
// integration is silently skipped.

package deluge

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

var (
	globalDelugeClient   *Client
	globalDelugeClientMu sync.Mutex
)

// GetClient returns the Deluge client, initializing it if needed.
// Returns nil if Deluge is not configured.
func GetClient() *Client {
	globalDelugeClientMu.Lock()
	defer globalDelugeClientMu.Unlock()
	if globalDelugeClient != nil {
		return globalDelugeClient
	}
	url := config.AppConfig.DelugeWebURL
	pass := config.AppConfig.DelugeWebPassword
	// Fall back to download_client.torrent.deluge config.
	if url == "" {
		dc := config.AppConfig.DownloadClient.Torrent.Deluge
		if dc.Host != "" {
			port := dc.Port
			if port == 0 {
				port = 8112
			}
			url = fmt.Sprintf("http://%s:%d", dc.Host, port)
			pass = dc.Password
		}
	}
	if url == "" {
		return nil
	}
	if pass == "" {
		pass = "deluge"
	}
	c, err := New(url, pass)
	if err != nil {
		slog.Warn("failed to create deluge client", "err", err)
		return nil
	}
	globalDelugeClient = c
	return c
}

// ResetGlobalClientForTest clears the cached global client. Tests that set
// DelugeWebURL (or the download_client deluge host) and then exercise a code
// path that calls GetClient — e.g. NewServer, whose deluge-plugin Build does —
// MUST defer this: GetClient caches the constructed client in a package-level
// singleton, so restoring the config string alone leaks a live client into
// every later test in the process (under `go test -count=N` this crossed the
// iteration boundary and made MockStore-based server tests fail with
// unexpected UpsertOpDefinitionV2 calls from the deluge plugin's RegisterOp).
func ResetGlobalClientForTest() {
	globalDelugeClientMu.Lock()
	globalDelugeClient = nil
	globalDelugeClientMu.Unlock()
}

// SetGlobalClientForTest replaces the global deluge client for testing.
// Returns a restore func that must be deferred by the caller.
func SetGlobalClientForTest(c *Client) func() {
	globalDelugeClientMu.Lock()
	orig := globalDelugeClient
	globalDelugeClient = c
	globalDelugeClientMu.Unlock()
	return func() {
		globalDelugeClientMu.Lock()
		globalDelugeClient = orig
		globalDelugeClientMu.Unlock()
	}
}

// NotifyDelugeMoveStorage tells Deluge to move a torrent's storage
// to a new path. Called after a version swap or organize moves files
// that belong to a Deluge-sourced version.
//
// torrentHash is the infohash from BookVersion.TorrentHash.
// newPath is the directory where the files now live.
//
// Silently no-ops if Deluge is not configured or the torrent hash
// is empty.
func NotifyDelugeMoveStorage(torrentHash, newPath string) {
	if torrentHash == "" {
		return
	}
	if !config.AppConfig.DelugeMoveEnabled {
		slog.Info("deluge move_storage skipped (deluge_move_enabledfalse) →", "torrentHash", torrentHash, "newPath", newPath)
		return
	}
	c := GetClient()
	if c == nil {
		return
	}
	// Deluge expects the parent directory, not the file path.
	dir := filepath.Dir(newPath)
	if err := c.MoveStorage([]string{torrentHash}, dir); err != nil {
		slog.Warn("deluge move_storage →", "torrentHash", torrentHash, "dir", dir, "err", err)
	} else {
		slog.Info("deluge move_storage →", "torrentHash", torrentHash, "dir", dir)
	}
}

// NotifyDelugeAfterOrganize tells Deluge to follow a book that was
// just moved into the library by the organize pipeline.
//
// Called after the file move succeeds and the Book record has been
// updated to point at newPath. It looks up the active BookVersion(s)
// for the book; for each with a non-empty TorrentHash it calls
// NotifyDelugeMoveStorage so the torrent client keeps seeding from
// the new location.
//
// Best-effort: errors are logged but do not bubble up — the organize
// operation already succeeded.
func NotifyDelugeAfterOrganize(store interface {
	database.BookVersionStore
}, bookID, newPath string) {
	versions, err := store.GetBookVersionsByBookID(bookID)
	if err != nil {
		slog.Warn("deluge-organize failed to load versions for book", "bookID", bookID, "err", err)
		return
	}
	for _, v := range versions {
		if v.TorrentHash != "" && v.Status == database.BookVersionStatusActive {
			NotifyDelugeMoveStorage(v.TorrentHash, newPath)
		}
	}
}

// NotifyDelugeAfterUndo checks whether the reverted operation moved
// Deluge-sourced files and updates the torrent storage path.
//
// oldFilePath is the path the file was restored to (the original location
// before the organize operation ran). This is the destination Deluge needs
// to know about — NOT book.FilePath, which may not yet be updated in the DB
// at the point this is called from the undo engine.
func NotifyDelugeAfterUndo(store interface {
	database.BookReader
	database.BookVersionStore
}, bookID, oldFilePath string) {
	if oldFilePath == "" {
		return
	}
	_, _ = store.GetBookByID(bookID) // ensure book exists; ignore result
	versions, _ := store.GetBookVersionsByBookID(bookID)
	for _, v := range versions {
		if v.TorrentHash != "" && v.Status == database.BookVersionStatusActive {
			// Use oldFilePath (the restored destination), not book.FilePath,
			// because the DB FilePath may not have been updated yet when this
			// is called immediately after the file rename-back.
			NotifyDelugeMoveStorage(v.TorrentHash, oldFilePath)
		}
	}
}

// NotifyDelugeAfterVersionSwap checks whether the swapped versions
// have torrent hashes and updates Deluge accordingly.
func NotifyDelugeAfterVersionSwap(store interface {
	database.BookReader
	database.BookVersionStore
}, fromVer, toVer *database.BookVersion, bookFilePath string) {
	if toVer != nil && toVer.TorrentHash != "" {
		NotifyDelugeMoveStorage(toVer.TorrentHash, bookFilePath)
	}
	if fromVer != nil && fromVer.TorrentHash != "" {
		// The "from" version moved into .versions/{id}/ — tell Deluge.
		book, _ := store.GetBookByID(fromVer.BookID)
		if book != nil {
			slotDir := filepath.Join(filepath.Dir(book.FilePath), ".versions", fromVer.ID)
			NotifyDelugeMoveStorage(fromVer.TorrentHash, slotDir)
		}
	}
}
