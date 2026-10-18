// file: internal/server/deluge_torrent_link_test.go
// version: 2.0.0
// guid: 2d9c4e61-8b57-4f03-a1e8-6c3b7d905a42
// last-edited: 2026-10-18
//
// Tests for the torrent-to-book link the importer writes (BookVersion.TorrentHash
// and BookFile.DelugeHash) and what the kept Deluge routes do with it. The route
// that used to set the link, POST /api/v1/deluge/discover/import, was retired by
// 01-P81k and answers 410 (wire_deluge_gone_test.go); these tests drive
// importer.ImportFile directly with a trusted TorrentHash, the way that route did
// after it had verified the hash against Deluge.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
)

const linkTestHash = "00112233445566778899aabbccddeeff00112233"

// linkFakeDeluge is a minimal Deluge Web JSON-RPC server for one torrent.
type linkFakeDeluge struct {
	srv        *httptest.Server
	moves      atomic.Int32
	known      bool   // does core.get_torrent_status know linkTestHash
	savePath   string // torrent save_path
	filePaths  []string
	listLabel  string
	torrentKey string
}

func newLinkFakeDeluge(t *testing.T, savePath string, filePaths ...string) *linkFakeDeluge {
	t.Helper()
	f := &linkFakeDeluge{known: true, savePath: savePath, filePaths: filePaths, listLabel: "audiobooks", torrentKey: linkTestHash}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			ID     int64  `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(result any) {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": result})
		}
		files := make([]map[string]any, 0, len(f.filePaths))
		for i, p := range f.filePaths {
			files = append(files, map[string]any{"index": i, "path": p, "size": 9})
		}
		switch req.Method {
		case "auth.login":
			reply(true)
		case "core.get_torrent_status":
			if !f.known {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": req.ID,
					"error": map[string]any{"code": 4, "message": "InvalidTorrentError: torrent_id not in session."}})
				return
			}
			reply(map[string]any{"hash": linkTestHash, "name": "Example Book", "save_path": f.savePath, "files": files})
		case "core.get_torrents_status":
			reply(map[string]any{f.torrentKey: map[string]any{
				"hash": linkTestHash, "name": "Example Book", "save_path": f.savePath,
				"state": "Seeding", "progress": 100.0, "label": f.listLabel, "total_size": 9,
			}})
		case "core.move_storage":
			f.moves.Add(1)
			reply(nil)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { assert.Zero(t, f.moves.Load(), "discovery must never send core.move_storage") })
	return f
}

// linkImportEnv is a real in-memory store plus a staged single-file torrent
// payload at <dlDir>/Example Book.m4b, allowed as an import path.
type linkImportEnv struct {
	store   *database.PebbleStore
	srv     *Server
	dlDir   string
	content string
}

func newLinkImportEnv(t *testing.T) *linkImportEnv {
	t.Helper()

	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	_, err = store.CreateImportPath(dir, "downloads")
	require.NoError(t, err)
	content := filepath.Join(dir, "Example Book.m4b")
	require.NoError(t, os.WriteFile(content, []byte("audiodata"), 0o644))

	prevExt := config.AppConfig.SupportedExtensions
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a"}
	t.Cleanup(func() { config.AppConfig.SupportedExtensions = prevExt })

	return &linkImportEnv{
		store:   store,
		srv:     &Server{store: store, importService: importer.NewImportService(store)},
		dlDir:   dir,
		content: content,
	}
}

// importLinked imports the staged file with a trusted torrent hash, as the
// retired discover-import route did once Deluge had confirmed the hash.
func (e *linkImportEnv) importLinked(t *testing.T) *importer.ImportFileResponse {
	t.Helper()
	resp, err := e.srv.importService.ImportFile(&importer.ImportFileRequest{
		FilePath: e.content, TorrentHash: linkTestHash,
	})
	require.NoError(t, err)
	require.True(t, resp.TorrentLinked, "note: %s", resp.TorrentLinkNote)
	return resp
}

// The importer cannot import internal/deluge, so it carries its own copy of
// the hash-shape check. Both must accept and reject the same strings.
func TestTorrentHashValidatorsAgree(t *testing.T) {
	sha1 := linkTestHash
	sha256 := strings.Repeat("ab", 32)
	cases := []string{
		"zz", sha1, sha256, strings.ToUpper(sha1), sha1 + "a", sha1[:39], sha1[:32],
		strings.Repeat(" ", 40), strings.Repeat("g", 40), sha256 + "0", sha1[:20] + "-" + sha1[21:],
	}
	env := newLinkImportEnv(t)
	for _, in := range cases {
		_, normErr := deluge.NormalizeTorrentID(in)
		// The importer only checks the already-lower-cased form, so compare
		// against the normalizer's own output when it succeeds.
		impIn := in
		if n, err := deluge.NormalizeTorrentID(in); err == nil {
			impIn = n
		}
		impErr := importerHashErr(t, env, impIn)
		assert.Equal(t, normErr == nil, impErr == nil, "disagreement on %q", in)
	}
	// Upper-case is the one deliberate difference: the importer insists on the
	// already-normalized form.
	assert.Error(t, importerHashErr(t, env, strings.ToUpper(sha1)))
}

// importerHashErr reports whether ImportFile rejects hash for its shape. A
// shape rejection happens before any filesystem access, so an empty request
// path is enough to tell "bad hash" (error mentioning the hash) from the
// later path error.
func importerHashErr(t *testing.T, env *linkImportEnv, hash string) error {
	t.Helper()
	_, err := env.srv.importService.ImportFile(&importer.ImportFileRequest{FilePath: filepath.Join(env.dlDir, "missing.m4b"), TorrentHash: hash})
	if err != nil && strings.Contains(err.Error(), "invalid torrent hash") {
		return err
	}
	return nil
}

// A linked import is, by design, a candidate for the bulk copy: the row has a
// DelugeHash and no ImportedFromDelugeAt. That is the copy-model hand-off (D57):
// import in place now, copy into the library later. This pins it so a change to
// either side is a decision, not an accident.
func TestImportedRowAppearsInBulkImportCandidates(t *testing.T) {
	env := newLinkImportEnv(t)
	resp := env.importLinked(t)

	cands, err := env.store.GetBookFilesNeedingDelugeImportCore()
	require.NoError(t, err)
	var found bool
	for _, c := range cands {
		if c.BookID == resp.ID {
			found = true
		}
	}
	assert.True(t, found, "a linked import must be a bulk-import candidate (designed copy-model hand-off)")
}

// GET /deluge/discover is kept (D73): once a torrent is linked to a live book,
// tier 1 of the discovery match must stop listing it.
func TestDiscoverUnimported_ListSkipsLinkedTorrent(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	client, err := deluge.New(fake.srv.URL, "deluge")
	require.NoError(t, err)
	t.Cleanup(deluge.SetGlobalClientForTest(client))

	// Positive control: before the import the torrent is listed.
	before, err := deluge.DiscoverUnimported(env.store, client, "audiobooks")
	require.NoError(t, err)
	require.Len(t, before, 1)

	env.importLinked(t)

	after, err := deluge.DiscoverUnimported(env.store, client, "audiobooks")
	require.NoError(t, err)
	assert.Empty(t, after, "tier 1 must now skip the linked torrent")
}
