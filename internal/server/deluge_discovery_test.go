// file: internal/server/deluge_discovery_test.go
// version: 3.1.0
// guid: f7a8b9c0-d1e2-3f4a-5b6c-7d8e9f0a1b2c
// last-edited: 2026-08-21
//
// Tests for the discovery helpers — now delegates to internal/deluge/discovery.go.

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// The shared download dir — every torrent has this as save_path.
const dlDir = "/mnt/bigdata/books/deluge"

// ---------------------------------------------------------------------------
// Tier 2: IsPathTracked
// ---------------------------------------------------------------------------

func TestIsPathTracked_EmptyContentPath(t *testing.T) {
	known := map[string]struct{}{dlDir + "/Dune/Dune.m4b": {}}
	assert.False(t, deluge.IsPathTracked("", known))
}

func TestIsPathTracked_ExactMatch(t *testing.T) {
	known := map[string]struct{}{dlDir + "/Dune.m4b": {}}
	assert.True(t, deluge.IsPathTracked(dlDir+"/Dune.m4b", known))
}

func TestIsPathTracked_ContentDirPrefixMatch(t *testing.T) {
	known := map[string]struct{}{dlDir + "/Dune/Dune.m4b": {}}
	assert.True(t, deluge.IsPathTracked(dlDir+"/Dune", known))
}

func TestIsPathTracked_UnimportedTorrent(t *testing.T) {
	known := map[string]struct{}{dlDir + "/Dune/Dune.m4b": {}}
	assert.False(t, deluge.IsPathTracked(dlDir+"/Foundation", known))
}

func TestIsPathTracked_EmptyKnown(t *testing.T) {
	assert.False(t, deluge.IsPathTracked(dlDir+"/Dune", map[string]struct{}{}))
}

func TestIsPathTracked_PartialNameNotMatched(t *testing.T) {
	known := map[string]struct{}{dlDir + "/Dune/Dune.m4b": {}}
	assert.False(t, deluge.IsPathTracked(dlDir+"/Du", known))
}

func TestIsPathTracked_TrailingSlashNormalized(t *testing.T) {
	known := map[string]struct{}{dlDir + "/Dune/Dune.m4b": {}}
	assert.True(t, deluge.IsPathTracked(dlDir+"/Dune/", known))
}

// ---------------------------------------------------------------------------
// Tier 3: IsTitleTracked / ParseTorrentNameCandidates / NormalizeTitle
// ---------------------------------------------------------------------------

func TestNormalizeTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"The Way of Kings", "the way of kings"},
		{"The Way of Kings!", "the way of kings"},
		{"Dune (2023)", "dune 2023"},
		{"Foundation - Isaac Asimov", "foundation isaac asimov"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, deluge.NormalizeTitle(c.in), c.in)
	}
}

func TestParseTorrentNameCandidates_DashSeparated(t *testing.T) {
	candidates := deluge.ParseTorrentNameCandidates("Brandon Sanderson - The Way of Kings")
	assert.Contains(t, candidates, deluge.NormalizeTitle("Brandon Sanderson"))
	assert.Contains(t, candidates, deluge.NormalizeTitle("The Way of Kings"))
}

func TestParseTorrentNameCandidates_ByKeyword(t *testing.T) {
	candidates := deluge.ParseTorrentNameCandidates("The Way of Kings by Brandon Sanderson [M4B]")
	assert.Contains(t, candidates, deluge.NormalizeTitle("The Way of Kings"))
}

func TestParseTorrentNameCandidates_DotSeparated(t *testing.T) {
	candidates := deluge.ParseTorrentNameCandidates("Dune.Frank.Herbert.2023.M4B")
	assert.Contains(t, candidates, "dune frank herbert")
}

func TestIsTitleTracked_Hit(t *testing.T) {
	titles := map[string]struct{}{
		deluge.NormalizeTitle("The Way of Kings"): {},
	}
	assert.True(t, deluge.IsTitleTracked("Brandon Sanderson - The Way of Kings [M4B]", titles))
}

func TestIsTitleTracked_Miss(t *testing.T) {
	titles := map[string]struct{}{
		deluge.NormalizeTitle("Dune"): {},
	}
	assert.False(t, deluge.IsTitleTracked("Brandon Sanderson - The Way of Kings", titles))
}

// ---------------------------------------------------------------------------
// Tier 4: IsContentHashTracked / SHA256File
// ---------------------------------------------------------------------------

func TestSha256File(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.m4b")
	require.NoError(t, os.WriteFile(f, []byte("audiodata"), 0o644))

	hash1, err := deluge.SHA256File(f)
	require.NoError(t, err)
	assert.Len(t, hash1, 64) // hex SHA256

	// Same content → same hash.
	hash2, _ := deluge.SHA256File(f)
	assert.Equal(t, hash1, hash2)
}

func TestSha256File_Missing(t *testing.T) {
	_, err := deluge.SHA256File("/nonexistent/file.m4b")
	assert.Error(t, err)
}

func TestIsContentHashTracked_MatchFound(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "book.m4b")
	require.NoError(t, os.WriteFile(audio, []byte("audiodata"), 0o644))

	expected, _ := deluge.SHA256File(audio)
	lookup := func(h string) bool { return h == expected }

	assert.True(t, deluge.IsContentHashTracked(dir, lookup))
}

func TestIsContentHashTracked_NoMatch(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "book.m4b"), []byte("audiodata"), 0o644))

	lookup := func(h string) bool { return false }
	assert.False(t, deluge.IsContentHashTracked(dir, lookup))
}

func TestIsContentHashTracked_SkipsNonAudioFiles(t *testing.T) {
	dir := t.TempDir()
	// Only a .txt file — no audio files to hash.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("text"), 0o644))

	called := false
	lookup := func(h string) bool { called = true; return true }
	assert.False(t, deluge.IsContentHashTracked(dir, lookup))
	assert.False(t, called, "lookup should not be called for non-audio files")
}

func TestIsContentHashTracked_MissingDir(t *testing.T) {
	// Walk on a nonexistent dir returns false without panicking.
	lookup := func(h string) bool { return true }
	assert.False(t, deluge.IsContentHashTracked("/nonexistent/path", lookup))
}

// ---------------------------------------------------------------------------
// handleDiscoveryImport — the hydrate-then-import guard (TODO L10525)
// ---------------------------------------------------------------------------

// discoveryImportResponse mirrors the anonymous response struct built by
// handleDiscoveryImport, wrapped in httputil.RespondWithOK's {"data": ...}.
type discoveryImportResponse struct {
	Data struct {
		Total    int  `json:"total"`
		Imported int  `json:"imported"`
		Skipped  int  `json:"skipped"`
		Failed   int  `json:"failed"`
		DryRun   bool `json:"dry_run"`
		Results  []struct {
			FileID  string `json:"file_id"`
			Path    string `json:"path"`
			NewPath string `json:"new_path,omitempty"`
			Error   string `json:"error,omitempty"`
		} `json:"results"`
	} `json:"data"`
}

// discoveryImportFixture builds the one-pending-file world shared by the
// hydrate-failure and hydrate-success tests below. Only GetBookFileByIDFunc
// differs between them, so a green happy-path run is the positive control
// proving `failed == 1` in the failure tests is not satisfied by an empty
// pending list. srcPath is created on disk outside RootDir so the success
// path has a real file for ImportToLibrary to copy.
func discoveryImportFixture(t *testing.T) (root string, full *database.BookFile) {
	t.Helper()

	root = t.TempDir()
	srcDir := t.TempDir() // outside root, so dest != src and the copy really runs
	srcPath := filepath.Join(srcDir, "book.m4b")
	require.NoError(t, os.WriteFile(srcPath, []byte("audiodata"), 0o644))

	origRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = root
	t.Cleanup(func() { config.AppConfig.RootDir = origRoot })

	// handleDiscoveryImport bails with 503 when getDelugeClient() is nil. The
	// client is never dialed on this path (DelugeMoveEnabled is off), so a
	// bare client value is enough. SetGlobalClientForTest's restore func must
	// be deferred — GetClient caches into a package singleton that otherwise
	// leaks into every later test in the process.
	client, err := deluge.New("http://deluge.invalid:8112", "deluge")
	require.NoError(t, err)
	t.Cleanup(deluge.SetGlobalClientForTest(client))

	full = &database.BookFile{
		ID:         "f1",
		BookID:     "b1",
		FilePath:   srcPath,
		DelugeHash: "abc123",
	}
	return root, full
}

// callDiscoveryImport runs the handler against a MockStore whose hydrate call
// is supplied by the caller, and decodes the response.
func callDiscoveryImport(
	t *testing.T,
	full *database.BookFile,
	hydrate func(bookID, fileID string) (*database.BookFile, error),
) (discoveryImportResponse, *database.BookFile) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	var updated *database.BookFile
	store := &database.MockStore{
		GetBookFilesNeedingDelugeImportCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{full.Core()}, nil
		},
		GetBookFileByIDFunc: hydrate,
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			updated = file
			return nil
		},
	}

	srv := &Server{store: store}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/discovery/import", nil)

	srv.handleDiscoveryImport(c)
	require.Equal(t, http.StatusOK, w.Code, "body: %s", w.Body.String())

	var resp discoveryImportResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp, updated
}

// TestHandleDiscoveryImport_HydrateFailure pins the `hydrateErr != nil ||
// full == nil` guard at internal/server/deluge_discovery.go:168. Both arms
// must record a per-file error result and skip ImportToLibrary rather than
// silently dropping the file.
//
// Why the assertion is on the exact error STRING and not on a call count or
// `failed == 1`: if the guard is deleted, the nil BookFile flows into
// delugeclient.ImportToLibrary, whose own `bookFile == nil` check returns an
// error immediately. The unguarded path therefore produces the SAME
// failed=1 / len(results)=1 / Error != "" shape and never reaches
// UpdateBookFile either. The error text is the only signal that discriminates
// the guard from ImportToLibrary's nil check — do not "simplify" these
// assertions back to a count, it removes the coverage.
func TestHandleDiscoveryImport_HydrateFailure(t *testing.T) {
	hydrateErr := errors.New("pebble: get f1: io error")

	cases := []struct {
		name    string
		hydrate func(bookID, fileID string) (*database.BookFile, error)
		wantErr string
	}{
		{
			// Non-nil error variant: the store's own error text is surfaced.
			name: "hydrate returns an error",
			hydrate: func(string, string) (*database.BookFile, error) {
				return nil, hydrateErr
			},
			wantErr: hydrateErr.Error(),
		},
		{
			// Nil-error variant: a missing row with no error must still be
			// treated as a failure (the `|| full == nil` half of the guard).
			name: "hydrate returns nil without an error",
			hydrate: func(string, string) (*database.BookFile, error) {
				return nil, nil
			},
			wantErr: "book file not found",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, full := discoveryImportFixture(t)
			resp, updated := callDiscoveryImport(t, full, tc.hydrate)

			assert.Equal(t, 1, resp.Data.Total)
			assert.Equal(t, 0, resp.Data.Imported)
			assert.Equal(t, 1, resp.Data.Failed, "hydrate failure must count as failed")
			require.Len(t, resp.Data.Results, 1)
			assert.Equal(t, full.ID, resp.Data.Results[0].FileID)
			// The discriminating assertion — see the doc comment above.
			assert.Equal(t, tc.wantErr, resp.Data.Results[0].Error)
			assert.Empty(t, resp.Data.Results[0].NewPath)
			assert.Nil(t, updated, "ImportToLibrary must not write back on a hydrate failure")
		})
	}
}

// TestHandleDiscoveryImport_HydrateSuccess is the positive control for the
// test above: same fixture, same pending file, a working hydrate. It proves
// the pending list is non-empty and that the import path is reachable, so
// `failed == 1` there cannot be passing vacuously.
func TestHandleDiscoveryImport_HydrateSuccess(t *testing.T) {
	root, full := discoveryImportFixture(t)

	resp, updated := callDiscoveryImport(t, full, func(bookID, fileID string) (*database.BookFile, error) {
		if bookID == full.BookID && fileID == full.ID {
			return full, nil
		}
		return nil, nil
	})

	assert.Equal(t, 1, resp.Data.Total)
	assert.Equal(t, 1, resp.Data.Imported)
	assert.Equal(t, 0, resp.Data.Failed)
	require.Len(t, resp.Data.Results, 1)
	assert.Empty(t, resp.Data.Results[0].Error)
	assert.Equal(t, filepath.Join(root, "book.m4b"), resp.Data.Results[0].NewPath)

	require.NotNil(t, updated, "a successful hydrate must reach ImportToLibrary's UpdateBookFile")
	assert.NotNil(t, updated.ImportedFromDelugeAt)
}
