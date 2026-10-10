// file: internal/scheduler/cleanup_old_backups_retention_test.go
// version: 1.0.0
// guid: 5d1c7e3a-92b4-4f68-a0d3-8e6b41c9f257
// last-edited: 2026-10-09

package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// TestRunCleanupOldBackups_RetentionSource pins where the scheduled backup
// cleanup, which DELETES files, takes its retention from. Every case also
// proves the deletion stays inside its lane: a file newer than the window, an
// old file whose name is not a .bak-* backup, and an old .bak-* file outside
// the configured RootDir must never be touched.
func TestRunCleanupOldBackups_RetentionSource(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backupDays int
		softDays   int
		// wantGone is keyed by the file's age in days.
		wantGone map[int]bool
	}{
		{"key unset, soft-delete 30: only the 40-day file goes", 0, 30, map[int]bool{5: false, 20: false, 40: true}},
		{"key 10, soft-delete 30: the 20- and 40-day files go", 10, 30, map[int]bool{5: false, 20: true, 40: true}},
		{"both 0: falls back to 30", 0, 0, map[int]bool{5: false, 20: false, 40: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			outside := t.TempDir() // separate temp dir: not under RootDir

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
			config.AppConfig.BackupRetentionDays = tc.backupDays
			config.AppConfig.PurgeSoftDeletedAfterDays = tc.softDays

			touch := func(path string, ageDays int) {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))
				mt := time.Now().Add(-time.Duration(ageDays) * 24 * time.Hour)
				require.NoError(t, os.Chtimes(path, mt, mt))
			}

			files := map[int]string{}
			for _, age := range []int{5, 20, 40} {
				p := filepath.Join(root, "lib", fmt.Sprintf("age%d", age), "a.bak-1")
				files[age] = p
				touch(p, age)
			}
			notBackup := filepath.Join(root, "lib", "cover.jpg")
			touch(notBackup, 400)
			outsideBackup := filepath.Join(outside, "a.bak-1")
			touch(outsideBackup, 400)

			require.NoError(t, runCleanupOldBackups(t.Context(), noopProgress{}))

			for age, p := range files {
				_, err := os.Stat(p)
				if tc.wantGone[age] {
					require.True(t, os.IsNotExist(err), "%d-day backup should be deleted", age)
				} else {
					require.NoError(t, err, "%d-day backup must survive", age)
				}
			}
			_, err := os.Stat(notBackup)
			require.NoError(t, err, "an old non-.bak file must never be deleted")
			_, err = os.Stat(outsideBackup)
			require.NoError(t, err, "a .bak-* file outside RootDir must never be deleted")
		})
	}
}
