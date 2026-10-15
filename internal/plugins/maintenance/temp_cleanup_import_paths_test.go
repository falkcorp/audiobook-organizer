// file: internal/plugins/maintenance/temp_cleanup_import_paths_test.go
// version: 1.0.0
// guid: 5b8e2f4a-7c19-4d63-a0e5-91f3c6d28b47
// last-edited: 2026-10-15

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func writeAged(t *testing.T, path string, age time.Duration) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	ts := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, ts, ts))
}

// TestTempFileCleanup_ImportPathsOnly pins the import_paths_only mode that
// replaced the per-import-path transcode ticker: every import path is its own
// walk root, stale transcode temp files go, fresh ones and app-dir contents
// stay, a missing import path is skipped, and the library-root sweep is not run.
func TestTempFileCleanup_ImportPathsOnly(t *testing.T) {
	store, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	a, b := t.TempDir(), t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone")
	for i, p := range []string{a, b, missing} {
		_, err := store.CreateImportPath(p, []string{"a", "b", "missing"}[i])
		require.NoError(t, err)
	}
	withPluginAppDirs(t, a, true)

	staleA := filepath.Join(a, "Author", "one-transcode.tmp.m4b")
	freshA := filepath.Join(a, "Author", "two-transcode.tmp.m4b")
	staleB := filepath.Join(b, "x.ch.m4b")
	appFile := filepath.Join(a, "backups", "old-transcode.tmp.m4b") // non-dot app dir
	writeAged(t, staleA, 3*time.Hour)
	writeAged(t, freshA, 10*time.Minute)
	writeAged(t, staleB, 3*time.Hour)
	writeAged(t, appFile, 3*time.Hour)

	deps := &countingDeps{rootDirDeps: rootDirDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}}
	p := New(deps)
	raw := json.RawMessage(`{"import_paths_only":true,"max_age_hours":2}`)
	require.NoError(t, p.runTempFileCleanup(context.Background(), raw, &fakeReporter{}))

	require.NoFileExists(t, staleA)
	require.NoFileExists(t, staleB)
	require.FileExists(t, freshA, "a file newer than max age must be kept")
	require.FileExists(t, appFile, "DATA LOSS: a file inside an app dir was deleted")
	require.Zero(t, deps.sweeps, "import_paths_only must not run the library-root sweep")
}

func TestTempFileCleanup_NoParamsRunsLibrarySweep(t *testing.T) {
	deps := &countingDeps{rootDirDeps: rootDirDeps{root: t.TempDir()}}
	p := New(deps)
	require.NoError(t, p.runTempFileCleanup(context.Background(), nil, &fakeReporter{}))
	require.Equal(t, 1, deps.sweeps)
	require.Equal(t, deps.root, deps.sweptRoot)
}

func TestTempFileCleanup_BadParamsFail(t *testing.T) {
	p := New(&countingDeps{rootDirDeps: rootDirDeps{root: t.TempDir()}})
	require.Error(t, p.runTempFileCleanup(context.Background(), json.RawMessage(`{`), &fakeReporter{}))
}

// TestTempFileCleanup_PermissionIsSettingsManage: the op deletes files, so a
// caller holding only scan.trigger must not be able to run it.
func TestTempFileCleanup_PermissionIsSettingsManage(t *testing.T) {
	def := New(fakeDeps{}).tempFileCleanupDef()
	require.Equal(t, []auth.Permission{auth.PermSettingsManage}, def.Permissions)
}

// countingDeps records the library-root sweep.
type countingDeps struct {
	rootDirDeps
	sweeps    int
	sweptRoot string
}

func (d *countingDeps) CleanupOrphanedTempFiles(root, _ string) int {
	d.sweeps++
	d.sweptRoot = root
	return 0
}
