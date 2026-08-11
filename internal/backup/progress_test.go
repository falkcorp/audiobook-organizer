// file: internal/backup/progress_test.go
// version: 1.0.0
// guid: 9d1f6c07-4b28-4e93-a5f0-3c71d8e0b942
// last-edited: 2026-08-11

package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// BackupConfig.Progress exists so a long backup can prove it is still moving.
// Its caller is the pre-organize auto-backup, which the ops-registry watchdog
// CANCELS after ProgressTimeout (5m) without an UpdateProgress stamp. On
// production that backup archives 14 GB and ran 20-25 minutes.
//
// The load-bearing property is therefore not "Progress gets called" but
// "Progress is driven by completed work". A timer-driven stamp would satisfy
// the watchdog for a wedged backup too, which is the failure this guards.
// These tests assert the counters advance WITH the archive, and that they come
// from files actually written.
// ---------------------------------------------------------------------------

type progressEvent struct {
	phase     string
	filesDone int
	bytesDone int64
}

// seedDBDir writes n files of the given size into a fresh directory, standing
// in for a Pebble database's SSTs.
func seedDBDir(t *testing.T, n, size int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "audiobooks.pebble")
	if err := os.MkdirAll(dir, 0o775); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	payload := make([]byte, size)
	for i := range payload {
		// Non-constant bytes so gzip cannot collapse the archive to nothing.
		payload[i] = byte(i % 251)
	}
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, filepath.Base(dir)+"-"+string(rune('a'+i))+".sst")
		if err := os.WriteFile(name, payload, 0o644); err != nil {
			t.Fatalf("setup: write %s: %v", name, err)
		}
	}
	return dir
}

func TestCreateBackup_ProgressAdvancesWithFilesArchived(t *testing.T) {
	const fileCount = 6
	dbDir := seedDBDir(t, fileCount, 4096)

	var events []progressEvent
	cfg := DefaultBackupConfig()
	cfg.BackupDir = t.TempDir()
	cfg.Progress = func(phase string, filesDone int, bytesDone int64) {
		events = append(events, progressEvent{phase, filesDone, bytesDone})
	}

	if _, err := CreateBackup(dbDir, "pebble", cfg); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	var archive []progressEvent
	for _, e := range events {
		if e.phase == PhaseArchive {
			archive = append(archive, e)
		}
	}

	// One report per file written — the counters cannot outrun the work.
	if len(archive) != fileCount {
		t.Fatalf("expected %d %s reports (one per file), got %d", fileCount, PhaseArchive, len(archive))
	}
	for i, e := range archive {
		if e.filesDone != i+1 {
			t.Errorf("report %d: filesDone = %d, want %d", i, e.filesDone, i+1)
		}
		if i > 0 && e.bytesDone <= archive[i-1].bytesDone {
			t.Errorf("report %d: bytesDone %d did not advance past %d", i, e.bytesDone, archive[i-1].bytesDone)
		}
	}
	if got := archive[len(archive)-1].bytesDone; got != int64(fileCount*4096) {
		t.Errorf("final bytesDone = %d, want %d", got, fileCount*4096)
	}
}

// The checksum pass reads the whole finished archive. On production that is a
// 14 GB single io.Copy — by itself long enough to trip the watchdog — so it
// reports per chunk rather than going silent.
func TestCreateBackup_ProgressCoversChecksumPass(t *testing.T) {
	dbDir := seedDBDir(t, 2, 1024)

	var phases []string
	cfg := DefaultBackupConfig()
	cfg.BackupDir = t.TempDir()
	cfg.Progress = func(phase string, _ int, _ int64) {
		if len(phases) == 0 || phases[len(phases)-1] != phase {
			phases = append(phases, phase)
		}
	}

	if _, err := CreateBackup(dbDir, "pebble", cfg); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	want := []string{PhaseArchive, PhaseChecksum}
	if len(phases) != len(want) {
		t.Fatalf("phase sequence = %v, want %v", phases, want)
	}
	for i := range want {
		if phases[i] != want[i] {
			t.Fatalf("phase sequence = %v, want %v", phases, want)
		}
	}
}

// A nil Progress must stay a no-op: every pre-existing caller passes one, and
// a backup is not allowed to start failing because nobody wanted reporting.
func TestCreateBackup_NilProgressIsSafe(t *testing.T) {
	dbDir := seedDBDir(t, 3, 1024)

	cfg := DefaultBackupConfig()
	cfg.BackupDir = t.TempDir()
	cfg.Progress = nil

	info, err := CreateBackup(dbDir, "pebble", cfg)
	if err != nil {
		t.Fatalf("CreateBackup with nil Progress: %v", err)
	}
	if info.Checksum == "" {
		t.Error("expected a checksum to still be computed")
	}
	if info.Size <= 0 {
		t.Errorf("expected a non-empty archive, got size %d", info.Size)
	}
}
