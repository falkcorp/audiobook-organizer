// file: internal/server/deluge_discover_import_link_test.go
// version: 1.2.0
// guid: 2d9c4e61-8b57-4f03-a1e8-6c3b7d905a42
// last-edited: 2026-10-10
//
// Tests for the torrent-to-book link that POST /api/v1/deluge/discover/import
// writes: BookVersion.TorrentHash and BookFile.DelugeHash.

package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/importer"
	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

const linkTestHash = "00112233445566778899aabbccddeeff00112233"

// linkFakeDeluge is a minimal Deluge Web JSON-RPC server for one torrent.
type linkFakeDeluge struct {
	srv        *httptest.Server
	moves      atomic.Int32
	calls      atomic.Int32 // every JSON-RPC request, of any method
	known      bool         // does core.get_torrent_status know linkTestHash
	savePath   string       // torrent save_path
	filePaths  []string
	listLabel  string
	torrentKey string
}

func newLinkFakeDeluge(t *testing.T, savePath string, filePaths ...string) *linkFakeDeluge {
	t.Helper()
	f := &linkFakeDeluge{known: true, savePath: savePath, filePaths: filePaths, listLabel: "audiobooks", torrentKey: linkTestHash}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
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
	t.Cleanup(func() { assert.Zero(t, f.moves.Load(), "discovery import must never send core.move_storage") })
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
	gin.SetMode(gin.TestMode)

	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	dir := t.TempDir()
	_, err = store.CreateImportPath(dir, "downloads")
	require.NoError(t, err)
	content := filepath.Join(dir, "Example Book.m4b")
	require.NoError(t, os.WriteFile(content, []byte("audiodata"), 0o644))

	prevExt, prevDisc, prevMove, prevLabel := config.AppConfig.SupportedExtensions,
		config.AppConfig.DelugeDiscoveryEnabled, config.AppConfig.DelugeMoveEnabled, config.AppConfig.DelugeDiscoveryLabel
	config.AppConfig.SupportedExtensions = []string{".m4b", ".mp3", ".m4a"}
	config.AppConfig.DelugeDiscoveryEnabled = true
	config.AppConfig.DelugeMoveEnabled = false
	config.AppConfig.DelugeDiscoveryLabel = "audiobooks"
	t.Cleanup(func() {
		config.AppConfig.SupportedExtensions = prevExt
		config.AppConfig.DelugeDiscoveryEnabled = prevDisc
		config.AppConfig.DelugeMoveEnabled = prevMove
		config.AppConfig.DelugeDiscoveryLabel = prevLabel
	})

	return &linkImportEnv{
		store:   store,
		srv:     &Server{store: store, importService: importer.NewImportService(store)},
		dlDir:   dir,
		content: content,
	}
}

func (e *linkImportEnv) useClient(t *testing.T, url string) {
	t.Helper()
	client, err := deluge.New(url, "deluge")
	require.NoError(t, err)
	t.Cleanup(deluge.SetGlobalClientForTest(client))
}

func (e *linkImportEnv) post(t *testing.T, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/deluge/discover/import", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	e.srv.handleDelugeDiscoverImport(c)
	return w
}

func (e *linkImportEnv) bookCount(t *testing.T) int {
	t.Helper()
	books, err := e.store.GetAllBooksCore(0, 0)
	require.NoError(t, err)
	return len(books)
}

type linkImportResp struct {
	Book struct {
		ID string `json:"id"`
	} `json:"book"`
	TorrentHash     string `json:"torrent_hash"`
	TorrentLinked   bool   `json:"torrent_linked"`
	TorrentLinkNote string `json:"torrent_link_note"`
}

func decodeLinkResp(t *testing.T, w *httptest.ResponseRecorder) linkImportResp {
	t.Helper()
	var env struct {
		Data linkImportResp `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env), "body: %s", w.Body.String())
	return env.Data
}

func TestDiscoverImport_LinksVerifiedTorrent(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	env.useClient(t, fake.srv.URL)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	t.Logf("linked response: %s", strings.TrimSpace(w.Body.String()))
	resp := decodeLinkResp(t, w)
	assert.True(t, resp.TorrentLinked)
	assert.Empty(t, resp.TorrentLinkNote)
	assert.Equal(t, linkTestHash, resp.TorrentHash)

	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	require.NotNil(t, ver, "the torrent-hash index must find the version")
	assert.Equal(t, resp.Book.ID, ver.BookID)
	assert.Equal(t, "deluge", ver.Source)

	files, err := env.store.GetBookFiles(resp.Book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, linkTestHash, files[0].DelugeHash)
	assert.Nil(t, files[0].ImportedFromDelugeAt)
}

func TestDiscoverImport_PathNotInTorrent_400(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Other File.m4b")
	env.useClient(t, fake.srv.URL)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "content_path is not a file of that torrent")
	assert.Zero(t, env.bookCount(t))
}

func TestDiscoverImport_UnknownTorrent_400(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	fake.known = false
	env.useClient(t, fake.srv.URL)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Contains(t, w.Body.String(), "torrent_hash does not name a torrent in Deluge")
	assert.Zero(t, env.bookCount(t))
}

func TestDiscoverImport_MalformedHash_400(t *testing.T) {
	env := newLinkImportEnv(t)
	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": "abc123"})
	require.Equal(t, http.StatusBadRequest, w.Code, "body: %s", w.Body.String())
	assert.Zero(t, env.bookCount(t))
}

func TestDiscoverImport_DelugeUnreachable_ImportsUnlinked(t *testing.T) {
	env := newLinkImportEnv(t)
	// A closed port: listen, take the address, close.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	url := "http://" + ln.Addr().String()
	require.NoError(t, ln.Close())
	env.useClient(t, url)
	logs := logtest.Capture(t, slog.LevelWarn)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	t.Logf("unlinked response: %s", strings.TrimSpace(w.Body.String()))
	resp := decodeLinkResp(t, w)
	assert.False(t, resp.TorrentLinked)
	// A fixed category, never upstream text: the transport error names the
	// Deluge URL, an internal host.
	assert.Equal(t, "deluge unreachable", resp.TorrentLinkNote)
	addr := strings.TrimPrefix(url, "http://")
	assert.NotContains(t, w.Body.String(), addr, "the response must not echo the Deluge address")
	// Positive control: the detail is not lost, it goes to the server log.
	assert.Contains(t, logs.String(), addr, "the upstream error must be logged")

	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	assert.Nil(t, ver, "no hash may be written for an unverified torrent")
	files, err := env.store.GetBookFiles(resp.Book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Empty(t, files[0].DelugeHash)
}

func TestDiscoverImport_NoClient_ImportsUnlinked(t *testing.T) {
	env := newLinkImportEnv(t)
	t.Cleanup(deluge.SetGlobalClientForTest(nil))
	prevURL, prevHost := config.AppConfig.DelugeWebURL, config.AppConfig.DownloadClient.Torrent.Deluge.Host
	config.AppConfig.DelugeWebURL = ""
	config.AppConfig.DownloadClient.Torrent.Deluge.Host = ""
	t.Cleanup(func() {
		config.AppConfig.DelugeWebURL = prevURL
		config.AppConfig.DownloadClient.Torrent.Deluge.Host = prevHost
	})

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	resp := decodeLinkResp(t, w)
	assert.False(t, resp.TorrentLinked)
	assert.Equal(t, "deluge not configured", resp.TorrentLinkNote)
	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	assert.Nil(t, ver)
}

func TestDiscoverImport_UppercaseHashIsNormalized(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	env.useClient(t, fake.srv.URL)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": strings.ToUpper(linkTestHash)})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	assert.True(t, decodeLinkResp(t, w).TorrentLinked)
	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	require.NotNil(t, ver, "the stored hash must be the lower-cased form")
	assert.Equal(t, linkTestHash, ver.TorrentHash)
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
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	env.useClient(t, fake.srv.URL)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	resp := decodeLinkResp(t, w)

	cands, err := env.store.GetBookFilesNeedingDelugeImportCore()
	require.NoError(t, err)
	var found bool
	for _, c := range cands {
		if c.BookID == resp.Book.ID {
			found = true
		}
	}
	assert.True(t, found, "a linked import must be a bulk-import candidate (designed copy-model hand-off)")
}

func TestDiscoverImport_ListSkipsLinkedTorrent(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	client, err := deluge.New(fake.srv.URL, "deluge")
	require.NoError(t, err)
	t.Cleanup(deluge.SetGlobalClientForTest(client))

	// Positive control: before the import the torrent is listed.
	before, err := deluge.DiscoverUnimported(env.store, client, "audiobooks")
	require.NoError(t, err)
	require.Len(t, before, 1)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())

	after, err := deluge.DiscoverUnimported(env.store, client, "audiobooks")
	require.NoError(t, err)
	assert.Empty(t, after, "tier 1 must now skip the linked torrent")
}

// Content the owner purged must not come back linked: the file is imported,
// but neither a version nor the file row carries the hash, and the response
// says so from what was written rather than from the verified hash.
func TestDiscoverImport_PurgedContent_ImportsUnlinked(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	env.useClient(t, fake.srv.URL)

	old, err := env.store.CreateBookVersion(&database.BookVersion{
		BookID: "01JOLDBOOK0000000000000000", Status: database.BookVersionStatusInactivePurged,
		Format: "m4b", Source: "deluge", TorrentHash: linkTestHash,
	})
	require.NoError(t, err)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	resp := decodeLinkResp(t, w)
	assert.False(t, resp.TorrentLinked, "purged content must not be reported linked")
	assert.Equal(t, importer.TorrentLinkNotePurged, resp.TorrentLinkNote)
	require.NotEmpty(t, resp.Book.ID, "the import itself still succeeds")

	files, err := env.store.GetBookFiles(resp.Book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Empty(t, files[0].DelugeHash, "the purged torrent's hash must not reach the file row")

	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	require.NotNil(t, ver)
	assert.Equal(t, old.ID, ver.ID, "the torrent index must still name the purged version, not a new one")

	cands, err := env.store.GetBookFilesNeedingDelugeImportCore()
	require.NoError(t, err)
	for _, c := range cands {
		assert.NotEqual(t, resp.Book.ID, c.BookID, "purged content must not become a bulk-import candidate")
	}
}

// A Deluge that accepts the connection and never answers must not hold the
// request for the client's 5-minute transport timeout: the verification is
// bounded by discoverVerifyTimeout and the import goes ahead unlinked.
func TestDiscoverImport_DelugeHangs_TimesOutUnlinked(t *testing.T) {
	env := newLinkImportEnv(t)
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	// Cleanups run last-in first-out: release the handler before Close waits
	// for its connection.
	t.Cleanup(hang.Close)
	t.Cleanup(func() { close(release) })
	env.useClient(t, hang.URL)

	prev := discoverVerifyTimeout
	discoverVerifyTimeout = 200 * time.Millisecond
	t.Cleanup(func() { discoverVerifyTimeout = prev })

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	}()
	var w *httptest.ResponseRecorder
	select {
	case w = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the discover import did not return: the Deluge verification is not bounded")
	}
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	resp := decodeLinkResp(t, w)
	assert.False(t, resp.TorrentLinked)
	assert.Equal(t, "deluge timed out", resp.TorrentLinkNote)
	assert.NotContains(t, w.Body.String(), strings.TrimPrefix(hang.URL, "http://"))

	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	assert.Nil(t, ver, "no hash may be written for a torrent that was not verified")
	files, err := env.store.GetBookFiles(resp.Book.ID)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Empty(t, files[0].DelugeHash)
}

// A torrent a live book already holds is refused with 409 before Deluge is
// asked anything and before any book is created: a retried or doubled discover
// import must neither duplicate the book nor move the one-to-one torrent index
// away from the book that owns it.
func TestDiscoverImport_AlreadyLinked_409(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	env.useClient(t, fake.srv.URL)

	first := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, first.Code, "body: %s", first.Body.String())
	firstResp := decodeLinkResp(t, first)
	require.True(t, firstResp.TorrentLinked, "control: the first import links the torrent")
	owner, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	require.NotNil(t, owner)
	books := env.bookCount(t)
	callsBefore := fake.calls.Load()

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusConflict, w.Code, "body: %s", w.Body.String())
	var body struct {
		Code   string `json:"code"`
		BookID string `json:"book_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "TORRENT_ALREADY_LINKED", body.Code)
	assert.Equal(t, firstResp.Book.ID, body.BookID, "the 409 names the book that holds the torrent")

	assert.Equal(t, books, env.bookCount(t), "no second book may be created")
	assert.Equal(t, callsBefore, fake.calls.Load(), "the refusal comes before any Deluge request")
	after, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	require.NotNil(t, after)
	assert.Equal(t, owner.ID, after.ID, "the torrent index must still name the first book's version")
}

// Control for the 409: an index entry whose book was deleted is stale, so the
// torrent can be imported and linked again rather than refused forever.
func TestDiscoverImport_StaleTorrentIndex_Links(t *testing.T) {
	env := newLinkImportEnv(t)
	fake := newLinkFakeDeluge(t, env.dlDir, "Example Book.m4b")
	env.useClient(t, fake.srv.URL)

	_, err := env.store.CreateBookVersion(&database.BookVersion{
		BookID: "01JGONEBOOK000000000000000", Status: database.BookVersionStatusActive,
		Format: "m4b", Source: "deluge", TorrentHash: linkTestHash,
	})
	require.NoError(t, err)

	w := env.post(t, map[string]string{"content_path": env.content, "torrent_hash": linkTestHash})
	require.Equal(t, http.StatusCreated, w.Code, "body: %s", w.Body.String())
	resp := decodeLinkResp(t, w)
	assert.True(t, resp.TorrentLinked)
	ver, err := env.store.GetBookVersionByTorrentHash(linkTestHash)
	require.NoError(t, err)
	require.NotNil(t, ver)
	assert.Equal(t, resp.Book.ID, ver.BookID)
}
