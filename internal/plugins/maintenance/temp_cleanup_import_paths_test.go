// file: internal/plugins/maintenance/temp_cleanup_import_paths_test.go
// version: 2.1.0
// guid: 5b8e2f4a-7c19-4d63-a0e5-91f3c6d28b47
// last-edited: 2026-10-15

package maintenance

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func writeAged(t *testing.T, path string, age time.Duration) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	ts := time.Now().Add(-age)
	require.NoError(t, os.Chtimes(path, ts, ts))
}

// transcodeTempFixture builds two import paths plus a missing third, with a stale
// and a fresh file, a stale .ch.m4b, and a stale file inside a non-dot app dir.
type transcodeTempFixture struct {
	deps                        *countingDeps
	missing                     string
	staleA, freshA, staleB, app string
}

func newTranscodeTempFixture(t *testing.T) transcodeTempFixture {
	t.Helper()
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

	f := transcodeTempFixture{
		deps:    &countingDeps{rootDirDeps: rootDirDeps{fakeDeps: fakeDeps{store: store}, root: t.TempDir()}},
		missing: missing,
		staleA:  filepath.Join(a, "Author", "one-transcode.tmp.m4b"),
		freshA:  filepath.Join(a, "Author", "two-transcode.tmp.m4b"),
		staleB:  filepath.Join(b, "x.ch.m4b"),
		app:     filepath.Join(a, "backups", "old-transcode.tmp.m4b"), // non-dot app dir
	}
	writeAged(t, f.staleA, 3*time.Hour)
	writeAged(t, f.freshA, 10*time.Minute)
	writeAged(t, f.staleB, 3*time.Hour)
	writeAged(t, f.app, 3*time.Hour)
	return f
}

// TestTranscodeTempCleanup_LiveRun: dry_run=false removes stale files under every
// import path; fresh files and app-dir contents stay; a missing import path is
// warned about and skipped; the library-root sweep is not run.
func TestTranscodeTempCleanup_LiveRun(t *testing.T) {
	f := newTranscodeTempFixture(t)
	rep := &fakeReporter{}
	require.NoError(t, New(f.deps).runTranscodeTempCleanup(context.Background(), json.RawMessage(`{"dry_run":false}`), rep))

	require.NoFileExists(t, f.staleA)
	require.NoFileExists(t, f.staleB)
	require.FileExists(t, f.freshA, "a file newer than max age must be kept")
	require.FileExists(t, f.app, "DATA LOSS: a file inside an app dir was deleted")
	require.Zero(t, f.deps.sweeps, "must not run the library-root sweep")

	require.Len(t, rep.logs, 2)
	require.Contains(t, rep.logs[0], "Skipping import path")
	require.Contains(t, rep.logs[0], f.missing)
	require.Equal(t, "Removed 2 stale transcode temp files from 2 of 3 import paths", rep.logs[1],
		"the summary counts only paths actually swept")
}

// TestTranscodeTempCleanup_OmittedModeIsPreview: nil params, {} and an explicit
// dry_run=true delete nothing and report what a live run would remove (owner
// rule 2026-09-25).
func TestTranscodeTempCleanup_OmittedModeIsPreview(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"dry_run":true}`)} {
		t.Run(string(raw), func(t *testing.T) {
			f := newTranscodeTempFixture(t)
			rep := &fakeReporter{}
			require.NoError(t, New(f.deps).runTranscodeTempCleanup(context.Background(), raw, rep))

			for _, p := range []string{f.staleA, f.staleB, f.freshA, f.app} {
				require.FileExists(t, p, "a preview must delete nothing")
			}
			require.Equal(t, "Preview: would remove 2 stale transcode temp files from 2 of 3 import paths",
				rep.logs[len(rep.logs)-1])
		})
	}
}

func TestTranscodeTempCleanup_ContradictoryModeFails(t *testing.T) {
	f := newTranscodeTempFixture(t)
	err := New(f.deps).runTranscodeTempCleanup(context.Background(),
		json.RawMessage(`{"dry_run":false,"dryRun":true}`), &fakeReporter{})
	require.Error(t, err)
	require.FileExists(t, f.staleA)
}

// TestTempFileCleanup_LibrarySweepUnchanged: the existing op still runs only the
// library-root sweep.
func TestTempFileCleanup_LibrarySweepUnchanged(t *testing.T) {
	deps := &countingDeps{rootDirDeps: rootDirDeps{root: t.TempDir()}}
	require.NoError(t, New(deps).runTempFileCleanup(context.Background(), nil, &fakeReporter{}))
	require.Equal(t, 1, deps.sweeps)
	require.Equal(t, deps.root, deps.sweptRoot)
}

// TestTempFileCleanup_Permissions: both ops delete files, so a caller holding
// only scan.trigger must not be able to run either.
func TestTempFileCleanup_Permissions(t *testing.T) {
	p := New(fakeDeps{})
	want := []auth.Permission{auth.PermSettingsManage}
	require.Equal(t, want, p.tempFileCleanupDef().Permissions)
	require.Equal(t, want, p.transcodeTempCleanupDef().Permissions)
}

// TestTranscodeTempCleanupDef_ShapeAgainstSibling pins the properties the
// registry's dedupe and the dispatcher depend on.
func TestTranscodeTempCleanupDef_ShapeAgainstSibling(t *testing.T) {
	p := New(fakeDeps{})
	daily, hourly := p.tempFileCleanupDef(), p.transcodeTempCleanupDef()
	require.Nil(t, hourly.Schedule, "a Schedule makes the registry dedupe on def id alone")
	require.NotNil(t, daily.Schedule)
	require.Equal(t, daily.ConcurrencyKey, hourly.ConcurrencyKey, "the key serializes the two runs")
	require.NotEqual(t, daily.ID, hourly.ID)
}

// TestTempFileCleanup_BothDefsEnqueueBackToBack documents that the two real
// defs get distinct op ids when enqueued back to back. It is NOT the guard
// against the shared-id dedupe (the registry never dedupes across def ids, so
// this passes regardless); that guard is TestTranscodeTempCleanupDef_ShapeAgainstSibling,
// which pins the new def's distinct id and absent Schedule.
func TestTempFileCleanup_BothDefsEnqueueBackToBack(t *testing.T) {
	store := dbmocks.NewMockStore(t)
	store.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	store.EXPECT().GetOperationV2(mock.Anything).Return(nil, nil).Maybe()
	store.EXPECT().GetDepRev(mock.Anything).Return(0, nil).Maybe()
	var active []database.OperationV2Row
	store.EXPECT().InsertOperationV2(mock.Anything).
		RunAndReturn(func(row database.OperationV2Row) error {
			active = append(active, row)
			return nil
		}).Maybe()
	store.EXPECT().ListActiveOperationsV2().
		RunAndReturn(func() ([]database.OperationV2Row, error) { return active, nil }).Maybe()

	reg := registry.New(store, slog.New(slog.DiscardHandler), 1, nil)
	p := New(fakeDeps{})
	daily, hourly := p.tempFileCleanupDef(), p.transcodeTempCleanupDef()
	require.NotNil(t, daily.Schedule, "the fixture must be the real, scheduled def")
	require.NoError(t, reg.RegisterOp(daily))
	require.NoError(t, reg.RegisterOp(hourly))

	first, err := reg.EnqueueOp(context.Background(), daily.ID, struct{}{})
	require.NoError(t, err)
	second, err := reg.EnqueueOp(context.Background(), hourly.ID, struct{}{})
	require.NoError(t, err)
	require.NotEqual(t, first, second, "the two sweeps must not swallow each other")
	require.Len(t, active, 2)
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
