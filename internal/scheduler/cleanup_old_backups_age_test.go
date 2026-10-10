// file: internal/scheduler/cleanup_old_backups_age_test.go
// version: 1.0.0
// guid: 8d2b5f17-3a96-4e0c-91d4-c7e6a0b38f52
// last-edited: 2026-10-10

package scheduler

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// scheduler.cleanup-old-backups ages a .bak-* file by the time in its name,
// as maintenance.cleanup-old-backups does: a hardlinked create_backups backup
// keeps the audio's old mtime, so mtime alone would delete it on the next run.
func TestRunCleanupOldBackups_AgesByNameTime(t *testing.T) {
	root := t.TempDir()
	prevRoot := config.AppConfig.RootDir
	prevBackup, prevSoft := config.AppConfig.BackupRetentionDays, config.AppConfig.PurgeSoftDeletedAfterDays
	prevBackupDir, prevDump := config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir
	prevDB, prevPlaylist := config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir
	t.Cleanup(func() {
		config.AppConfig.RootDir = prevRoot
		config.AppConfig.BackupRetentionDays, config.AppConfig.PurgeSoftDeletedAfterDays = prevBackup, prevSoft
		config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = prevBackupDir, prevDump
		config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = prevDB, prevPlaylist
	})
	config.AppConfig.RootDir = root
	config.AppConfig.BackupDir, config.AppConfig.OpenLibraryDumpDir = "", ""
	config.AppConfig.DatabasePath, config.AppConfig.PlaylistDir = "", ""
	config.AppConfig.BackupRetentionDays = 30
	config.AppConfig.PurgeSoftDeletedAfterDays = 30

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

	require.NoError(t, runCleanupOldBackups(t.Context(), noopProgress{}))

	for _, c := range cases {
		path := filepath.Join(root, "Author", "Book", c.file)
		if c.wantGone {
			require.NoFileExists(t, path, "%s is past retention by its name time and must be removed", c.file)
		} else {
			require.FileExists(t, path, "%s is within retention and must survive", c.file)
		}
	}
}
