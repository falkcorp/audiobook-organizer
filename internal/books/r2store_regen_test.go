//go:build regen_r2fixture

// file: internal/books/r2store_regen_test.go
// version: 0.1.0
// guid: d6e08b37-5a42-4c19-9f7e-31b8c4a05e92
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// TestRegenerateR2Fixture rewrites testdata/r2store and testdata/r2store.json:
//
//	go test -count=1 -tags regen_r2fixture -run TestRegenerateR2Fixture ./internal/books/
//
// It is behind a build tag because it replaces committed files and builds under a
// fixed directory (r2StoreBuildDir) that two runs on one host cannot share. Run
// it from the repository root's internal/books directory; it writes relative to
// the package. See r2store_gen_test.go for what it writes and when to run it.
func TestRegenerateR2Fixture(t *testing.T) {
	built := buildR2Store(t)
	files := r2StoreFiles(t, built.Dir)
	r2StoreLeak(t, built.Dir, files)

	out := filepath.FromSlash(r2StoreDirRel)
	require.NoError(t, os.RemoveAll(out))
	var sums []r2StoreFileSum
	for _, rel := range files {
		// r2store/<rel> mirrors the build directory's own layout.
		dst := filepath.Join(out, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
		b, err := os.ReadFile(filepath.Join(built.Dir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dst, b, 0o644))
		sums = append(sums, r2StoreFileSum{Path: rel, SHA256: r2StoreSum(t, dst)})
	}
	// A kept set the build did not produce would have failed r2StoreFiles.
	version := r2StoreBooksVersion(t, r2StoreDB(out))
	m := r2StoreManifest{
		Command: r2storeRegenCommand, BuildDir: r2StoreBuildDir,
		BooksVersion: version, ToppedUp: built.ToppedUp, Files: sums,
	}
	if m.ToppedUp == nil {
		m.ToppedUp = []string{}
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.FromSlash(r2StoreManifestRel), append(raw, '\n'), 0o644))
	t.Logf("wrote %d files; books version %d; topped up %v", len(files), version, built.ToppedUp)
}

// r2StoreBooksVersion is books' applied version, read from the closed store's
// db_version row the way books' own version source does.
func r2StoreBooksVersion(t *testing.T, path string) int {
	t.Helper()
	raw, ok := r2StoreRows(t, path)["preference:db_version"]
	require.True(t, ok, "the store has no db_version row")
	var pref database.UserPreference
	require.NoError(t, json.Unmarshal([]byte(raw), &pref))
	require.NotNil(t, pref.Value)
	var v database.DatabaseVersion
	require.NoError(t, json.Unmarshal([]byte(*pref.Value), &v))
	return v.Version
}
