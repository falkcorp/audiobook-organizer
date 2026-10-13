// file: internal/plugins/maintenance/cleanup_backup_age_test.go
// version: 1.1.0
// guid: 0f6a3d92-7e41-4c58-b2a9-6d8e1c5f4b37
// last-edited: 2026-10-13

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// maintenance.cleanup-old-backups deletes only a name fileops wrote, aged by the
// time in that name. A create_backups backup is a hardlink and keeps the
// audio's old mtime, so a fresh backup of an old file must survive, and an old
// backup must go however recent its mtime. Names that are not backups are
// covered by TestRunCleanupOldBackups_NeverDeletesANonBackupName.
func TestRunCleanupOldBackups_AgesByNameTime(t *testing.T) {
	root := t.TempDir()
	withPluginAppDirs(t, root, false)

	now := time.Now()
	day := 24 * time.Hour
	unixName := func(age time.Duration) string { return strconv.FormatInt(now.Add(-age).Unix(), 10) }
	legacyName := func(age time.Duration) string { return now.Add(-age).Format("20060102-150405") }

	cases := []struct {
		file     string
		mtimeAge time.Duration
		wantGone bool
	}{
		// Recognised backup names: aged by the name.
		{"fresh-of-old.m4b.bak-" + unixName(time.Hour), 400 * day, false},
		{"fresh-of-old-suffixed.m4b.bak-" + unixName(time.Hour) + "-2", 400 * day, false},
		{"old-touched.m4b.bak-" + unixName(60*day), time.Hour, true},
		{"old-suffixed.m4b.bak-" + unixName(60*day) + "-3", time.Hour, true},
		{"legacy-old.m4b.bak-" + legacyName(60*day), time.Hour, true},
		{"legacy-fresh.m4b.bak-" + legacyName(2*day), 400 * day, false},
	}
	for _, c := range cases {
		p := filepath.Join(root, "Author", "Book", c.file)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		mt := now.Add(-c.mtimeAge)
		require.NoError(t, os.Chtimes(p, mt, mt))
	}

	p := New(rootDirDeps{root: root})
	require.NoError(t, p.runCleanupOldBackups(context.Background(), nil, &fakeReporter{}))

	for _, c := range cases {
		path := filepath.Join(root, "Author", "Book", c.file)
		if c.wantGone {
			require.NoFileExists(t, path, "%s is past retention by its name time and must be removed", c.file)
		} else {
			require.FileExists(t, path, "%s is within retention or not a backup name and must survive", c.file)
		}
	}
}

// TestRunCleanupOldBackups_NeverDeletesANonBackupName pins the data-loss guard:
// a file whose name contains ".bak-" but carries no recognised backup stamp
// (<unix>[-N] or the legacy YYYYMMDD-HHMMSS, at the very end of the name) is not
// a backup and is never deleted, however old its mtime. The sweep used to age
// such a file by mtime and delete it ("x.bak-notes.m4b" went after 30 days).
func TestRunCleanupOldBackups_NeverDeletesANonBackupName(t *testing.T) {
	root := t.TempDir()
	withPluginAppDirs(t, root, false)

	now := time.Now()
	day := 24 * time.Hour
	unixName := func(age time.Duration) string { return strconv.FormatInt(now.Add(-age).Unix(), 10) }

	names := []string{
		"x.bak-notes.m4b",
		"no-time.m4b.bak-unknown",
		"stamp-not-last.bak-" + unixName(60*day) + ".m4b",
		"too-early.m4b.bak-1",
		"too-early-suffixed.m4b.bak-1-2",
		"future.m4b.bak-" + strconv.FormatInt(now.Add(30*day).Unix(), 10),
		"legacy-impossible-date.m4b.bak-20251399-250000",
		".bak-" + unixName(60*day),
		// Other timestamp formats under the same infix (iTunes library backups).
		"library.itl.bak-20240101T000000Z",
		"library.itl.bak-2024-01-01T07-14-49.000000000Z",
	}
	for _, name := range names {
		p := filepath.Join(root, "Author", "Book", name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x"), 0o644))
		mt := now.Add(-400 * day)
		require.NoError(t, os.Chtimes(p, mt, mt))
	}

	p := New(rootDirDeps{root: root})
	require.NoError(t, p.runCleanupOldBackups(context.Background(), nil, &fakeReporter{}))

	for _, name := range names {
		require.FileExists(t, filepath.Join(root, "Author", "Book", name),
			"DATA LOSS: %s is not a recognised backup name and must never be deleted", name)
	}
}
