// file: internal/backup/progress_abort_test.go
// version: 1.0.0
// guid: 5c8e2a17-4f93-4d06-a1b7-9e3d6f0c8b25
// last-edited: 2026-09-12

package backup

import (
	"errors"
	"os"
	"testing"
)

// TestCreateBackup_ProgressErrorStopsArchiveBetweenFiles pins the checkpoint a
// scan stand-down relies on: a progress callback error stops the archive walk
// at the next file boundary (no further files archived) and the partial archive
// is removed rather than left behind as a truncated backup.
func TestCreateBackup_ProgressErrorStopsArchiveBetweenFiles(t *testing.T) {
	dbDir := seedDBDir(t, 6, 4096)
	errStop := errors.New("stood down")

	archived := 0
	cfg := DefaultBackupConfig()
	cfg.BackupDir = t.TempDir()
	cfg.Progress = func(phase string, _ int, _ int64) error {
		if phase == PhaseArchive {
			archived++
			return errStop
		}
		return nil
	}

	if _, err := CreateBackup(dbDir, "pebble", cfg); err == nil {
		t.Fatal("CreateBackup: want an error after the progress callback refused, got nil")
	}
	if archived != 1 {
		t.Fatalf("archive walk continued past the checkpoint: %d files reported, want 1", archived)
	}
	entries, err := os.ReadDir(cfg.BackupDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		t.Errorf("partial archive left behind: %s", e.Name())
	}
}
