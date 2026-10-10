// file: internal/plugins/maintenance/cleanup_backup_age_test.go
// version: 1.0.0
// guid: 0f6a3d92-7e41-4c58-b2a9-6d8e1c5f4b37
// last-edited: 2026-10-10

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

// maintenance.cleanup-old-backups ages a .bak-* file by the time in its name.
// A create_backups backup is a hardlink and keeps the audio's old mtime, so a
// fresh backup of an old file must survive, and an old backup must go however
// recent its mtime. A name without a time falls back to mtime.
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
		{"fresh-of-old.m4b.bak-" + unixName(time.Hour), 400 * day, false},
		{"fresh-of-old-suffixed.m4b.bak-" + unixName(time.Hour) + "-2", 400 * day, false},
		{"old-touched.m4b.bak-" + unixName(60*day), time.Hour, true},
		{"legacy-old.m4b.bak-" + legacyName(60*day), time.Hour, true},
		{"legacy-fresh.m4b.bak-" + legacyName(2*day), 400 * day, false},
		{"no-time-old.m4b.bak-unknown", 60 * day, true},
		{"no-time-fresh.m4b.bak-unknown", 2 * day, false},
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
			require.FileExists(t, path, "%s is within retention and must survive", c.file)
		}
	}
}
