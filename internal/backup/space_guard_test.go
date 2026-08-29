// file: internal/backup/space_guard_test.go
// version: 1.0.0
// guid: 3b6d0f27-58c1-49ea-a704-1f8e2d95c6b3
// last-edited: 2026-08-29

package backup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withDiskStats swaps the free-space probe for the duration of a test.
func withDiskStats(t *testing.T, total, free uint64, err error) {
	t.Helper()
	prev := diskStatsFn
	t.Cleanup(func() { diskStatsFn = prev })
	diskStatsFn = func(string) (uint64, uint64, error) { return total, free, err }
}

// seedDB writes a fake database directory of roughly size bytes.
func seedDB(t *testing.T, size int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000001.sst"), make([]byte, size), 0o600); err != nil {
		t.Fatalf("seed db: %v", err)
	}
	return dir
}

func TestDirSizeBytes_SumsRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b"), make([]byte, 250), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := dirSizeBytes(dir)
	if err != nil {
		t.Fatalf("dirSizeBytes: %v", err)
	}
	if got != 350 {
		t.Fatalf("got %d, want 350 (directories must not contribute)", got)
	}
}

// THE test for this change. A guard that refuses only after os.Create has
// already consumed space on a full filesystem has not prevented anything --
// that is precisely the production failure. Assert the directory stays empty.
func TestCreateBackup_RefusesAndWritesNothingWhenSpaceShort(t *testing.T) {
	db := seedDB(t, 4096)
	backupDir := filepath.Join(t.TempDir(), "backups")
	withDiskStats(t, 1<<40, 1024, nil) // 1 KiB free, far under the archive + margin

	cfg := DefaultBackupConfig()
	cfg.BackupDir = backupDir

	info, err := CreateBackup(db, "test", cfg)
	if err == nil {
		t.Fatal("expected refusal, got a successful backup")
	}
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("wrong error: %v", err)
	}
	if info != nil {
		t.Fatalf("expected nil info on refusal, got %+v", info)
	}

	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read backup dir: %v", readErr)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			t.Fatalf("guard refused but still created %s -- it consumed space anyway", e.Name())
		}
	}
}

// The error must name the actual numbers; an operator reading it at 3am needs
// to know how short they are, not merely that something failed.
func TestCreateBackup_RefusalErrorReportsSizes(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 1<<40, 1024, nil)
	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	_, err := CreateBackup(db, "test", cfg)
	if err == nil {
		t.Fatal("expected refusal")
	}
	for _, want := range []string{"free", "margin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	}
}

// Fail-closed: an unmeasurable filesystem must not be written to blind.
func TestCreateBackup_RefusesWhenFreeSpaceUnknown(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 0, 0, errors.New("statfs exploded"))
	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	_, err := CreateBackup(db, "test", cfg)
	if !errors.Is(err, ErrInsufficientDiskSpace) {
		t.Fatalf("expected fail-closed refusal, got %v", err)
	}
}

// Ample space must still work -- a guard that refuses everything is useless.
func TestCreateBackup_ProceedsWhenSpaceAmple(t *testing.T) {
	db := seedDB(t, 4096)
	withDiskStats(t, 1<<40, 1<<40, nil)
	cfg := DefaultBackupConfig()
	cfg.BackupDir = filepath.Join(t.TempDir(), "backups")

	info, err := CreateBackup(db, "test", cfg)
	if err != nil {
		t.Fatalf("backup refused despite ample space: %v", err)
	}
	if info == nil || info.Size == 0 {
		t.Fatalf("expected a real archive, got %+v", info)
	}
}

// --- retention -------------------------------------------------------------

// writeArchives lays down n fake .tar.gz files of the given size, oldest first.
func writeArchives(t *testing.T, dir string, n, size int) []string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var names []string
	base := time.Now().Add(-time.Duration(n) * time.Hour)
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, "audiobooks_test_"+string(rune('a'+i))+".tar.gz")
		if err := os.WriteFile(name, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	return names
}

func remaining(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tar.gz") {
			n++
		}
	}
	return n
}

// The production bug: a COUNT bound cannot express "do not fill the disk" once
// the counted files grow. Ten 15 GB archives satisfy MaxBackups=10 and consume
// 150 GB on a 141 GB filesystem.
func TestEnforceRetention_TotalBytesBoundEvictsBeyondCountBound(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 5, 1000) // 5 files, 5000 bytes, count bound satisfied

	if err := enforceRetention(dir, 10, 2500, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}

	if got := remaining(t, dir); got != 2 {
		t.Fatalf("expected 2 archives under a 2500-byte cap, got %d", got)
	}
}

func TestEnforceRetention_EvictsOldestFirst(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	names := writeArchives(t, dir, 4, 1000)

	if err := enforceRetention(dir, 2, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}

	for _, gone := range names[:2] {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("expected oldest %s to be evicted", filepath.Base(gone))
		}
	}
	for _, kept := range names[2:] {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("expected newest %s to survive: %v", filepath.Base(kept), err)
		}
	}
}

// incomingBytes reserves room for the archive about to be written, which is why
// pruning before the write can make a backup fit that would otherwise fail.
func TestEnforceRetention_ReservesRoomForIncoming(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 2, 1000) // 2000 bytes present

	// Cap 2500, incoming 1000: 2000+1000 > 2500, so one must go.
	if err := enforceRetention(dir, 10, 2500, 1000); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	if got := remaining(t, dir); got != 1 {
		t.Fatalf("expected 1 archive after reserving for incoming, got %d", got)
	}
}

// MaxTotalBytes uses the ordinary Go zero convention (unlimited). Making it mean
// "keep nothing" would turn every pre-existing BackupConfig literal into a
// silent delete-everything.
func TestEnforceRetention_ZeroTotalBytesMeansUnlimited(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 3, 1_000_000)

	if err := enforceRetention(dir, -1, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	if got := remaining(t, dir); got != 3 {
		t.Fatalf("zero MaxTotalBytes must not evict; got %d of 3", got)
	}
}

// A negative count bound means unlimited. The old implementation used
// len(backups)-maxBackups as a loop bound, so this indexed past the slice.
func TestEnforceRetention_NegativeCountIsUnlimitedNotPanic(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backups")
	writeArchives(t, dir, 3, 100)

	if err := enforceRetention(dir, -1, 0, 0); err != nil {
		t.Fatalf("enforceRetention: %v", err)
	}
	if got := remaining(t, dir); got != 3 {
		t.Fatalf("negative count must keep everything; got %d of 3", got)
	}
}
