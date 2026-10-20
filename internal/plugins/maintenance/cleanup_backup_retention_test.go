// file: internal/plugins/maintenance/cleanup_backup_retention_test.go
// version: 1.2.1
// guid: 3b8d6e41-0c97-4a52-b1f6-92e7a5c04d18
// last-edited: 2026-10-20

package maintenance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// retentionDeps reports a chosen backup retention on top of a real temp root.
type retentionDeps struct {
	rootDirDeps
	days int
}

func (d retentionDeps) BackupRetentionDays() int { return d.days }

// bakSuffix is the backup suffix fileops writes for a backup taken ageDays ago:
// ".bak-<unix>". The sweep ages a backup by this, never by the mtime.
func bakSuffix(ageDays int) string {
	return ".bak-" + strconv.FormatInt(time.Now().Add(-time.Duration(ageDays)*24*time.Hour).Unix(), 10)
}

// touchAged writes a file and sets its mtime to ageDays ago. A backup's own
// age is in its name (bakSuffix); the mtime only matters to the files that
// must NOT be treated as backups.
func touchAged(t *testing.T, path string, ageDays int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
	mt := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)
	require.NoError(t, os.Chtimes(path, mt, mt))
}

// TestRunCleanupOldBackups_RetentionSource pins that the sweep, which DELETES
// files, takes its window from the dependency's BackupRetentionDays (the server
// answers with config.EffectiveBackupRetentionDays, tested in internal/config),
// and that a non-positive answer falls back to 30 days rather than to a zero
// window that would delete every backup, the newest included. Every case also
// proves the deletion stays in its lane: a file inside the window, an old file
// that is not a .bak-* backup, and an old .bak-* file outside RootDir are never
// touched. This is the scheduler copy's retention test, moved here when that
// copy was merged into this op.
func TestRunCleanupOldBackups_RetentionSource(t *testing.T) {
	for _, tc := range []struct {
		name string
		days int
		// wantGone is keyed by the file's age in days.
		wantGone map[int]bool
	}{
		{"10 days: the 20- and 40-day files go", 10, map[int]bool{5: false, 20: true, 40: true}},
		{"30 days: only the 40-day file goes", 30, map[int]bool{5: false, 20: false, 40: true}},
		{"0 falls back to 30", 0, map[int]bool{5: false, 20: false, 40: true}},
		{"negative falls back to 30", -3, map[int]bool{5: false, 20: false, 40: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir() // separate temp dir: not under RootDir
			withPluginAppDirs(t, root, false)

			files := map[int]string{}
			for _, age := range []int{5, 20, 40} {
				p := filepath.Join(root, "lib", fmt.Sprintf("age%d", age), "a.m4b"+bakSuffix(age))
				files[age] = p
				touchAged(t, p, age)
			}
			notBackup := filepath.Join(root, "lib", "cover.jpg")
			touchAged(t, notBackup, 400)
			outsideBackup := filepath.Join(outside, "a.m4b"+bakSuffix(400))
			touchAged(t, outsideBackup, 400)

			p := New(retentionDeps{root: root, days: tc.days})
			require.NoError(t, p.runCleanupOldBackups(context.Background(), nil, &fakeReporter{}))

			for age, path := range files {
				if tc.wantGone[age] {
					require.NoFileExists(t, path, "%d-day backup should be deleted", age)
				} else {
					require.FileExists(t, path, "%d-day backup must survive", age)
				}
			}
			require.FileExists(t, notBackup, "an old non-.bak file must never be deleted")
			require.FileExists(t, outsideBackup, "a .bak-* file outside RootDir must never be deleted")
		})
	}
}

// TestCleanupOldBackups_DefMergeContract pins what the scheduler copy's merge
// promised: its gate, its larger budgets, its ID as an alias, and a resume
// policy that matches the sweep being re-runnable.
func TestCleanupOldBackups_DefMergeContract(t *testing.T) {
	def := New(fakeDeps{}).cleanupOldBackupsDef()

	require.Equal(t, "maintenance.cleanup-old-backups", def.ID)
	require.Equal(t, []string{"scheduler.cleanup-old-backups"}, def.FormerIDs)
	require.Equal(t, []auth.Permission{auth.PermSettingsManage}, def.Permissions)
	require.GreaterOrEqual(t, def.Timeout, time.Hour)
	require.GreaterOrEqual(t, def.ProgressTimeout, time.Hour)
	require.Equal(t, sdk.ResumeRequeue, def.ResumePolicy)
	// The sweep removes files only; it declares no DB write set (see the def's
	// comment), so a Writes entry appearing here is a decision to revisit, not
	// an accident to keep.
	require.Empty(t, def.Writes)
	require.True(t, def.Cancellable)
}

// TestCleanupOldBackups_RequeueIsIdempotent backs ResumeRequeue: an interrupted
// sweep that is run again from zero finishes the job, never removes a file the
// first pass was right to keep, and a further run over the finished tree does
// nothing.
func TestCleanupOldBackups_RequeueIsIdempotent(t *testing.T) {
	root := t.TempDir()
	withPluginAppDirs(t, root, false)

	var stale, fresh []string
	for i := range 6 {
		s := filepath.Join(root, "A", fmt.Sprintf("b%d", i), "x.m4b"+bakSuffix(90))
		touchAged(t, s, 400)
		stale = append(stale, s)
		f := filepath.Join(root, "A", fmt.Sprintf("b%d", i), "x.m4b"+bakSuffix(2))
		touchAged(t, f, 400)
		fresh = append(fresh, f)
	}
	keep := filepath.Join(root, "A", "b0", "x.m4b")
	touchAged(t, keep, 400)

	p := New(retentionDeps{root: root, days: 30})
	sweep := func(ctx context.Context) error {
		return p.runCleanupOldBackups(ctx, nil, &fakeReporter{})
	}

	// A run canceled before its first step is the extreme interruption: it
	// reports the cancel and deletes nothing.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sweep(canceled), context.Canceled)
	for _, f := range stale {
		require.FileExists(t, f, "a canceled run must not have deleted anything")
	}

	// A run that died halfway: the first three stale backups are already gone.
	for _, f := range stale[:3] {
		require.NoError(t, os.Remove(f))
	}
	require.NoError(t, sweep(context.Background()), "the rerun finishes the interrupted sweep")
	for _, f := range stale {
		require.NoFileExists(t, f)
	}
	for _, f := range append(fresh, keep) {
		require.FileExists(t, f, "the rerun must keep what is inside the window and every non-backup")
	}

	// And once more over the finished tree: no error, nothing further removed.
	require.NoError(t, sweep(context.Background()))
	for _, f := range append(fresh, keep) {
		require.FileExists(t, f)
	}
}

// TestRunCleanupOldBackups_FailedRemoveIsLoggedNotCounted pins what a remove
// that fails does, which is also what a backup that vanishes after the walk saw
// it does: the run carries on with the next file, returns no error, and does
// not count the file it could not remove. The failure is a directory the
// process cannot write to (a real vanished-after-lstat race has no seam).
func TestRunCleanupOldBackups_FailedRemoveIsLoggedNotCounted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the remove cannot be made to fail")
	}
	root := t.TempDir()
	withPluginAppDirs(t, root, false)

	locked := filepath.Join(root, "A", "locked", "x.m4b"+bakSuffix(90))
	removable := filepath.Join(root, "B", "ok", "y.m4b"+bakSuffix(90))
	touchAged(t, locked, 90)
	touchAged(t, removable, 90)
	lockedDir := filepath.Dir(locked)
	require.NoError(t, os.Chmod(lockedDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })

	rep := &fakeReporter{}
	p := New(retentionDeps{root: root, days: 30})
	require.NoError(t, p.runCleanupOldBackups(context.Background(), nil, rep))

	require.FileExists(t, locked, "a remove the OS refused leaves the file in place")
	require.NoFileExists(t, removable, "one failed remove must not stop the sweep")
	rep.mu.Lock()
	defer rep.mu.Unlock()
	require.Contains(t, rep.logs, "Backup cleanup complete: removed 1 file(s)",
		"only the file that was actually removed is counted")
}
