// file: internal/backup/checkpoint_staging_test.go
// version: 1.0.0
// guid: 15ce2610-0de8-4e85-87b7-6767657e19bc
// last-edited: 2026-09-22

package backup

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingCheckpointer writes one file into destDir, as Pebble would, and
// remembers where it was asked to write.
type recordingCheckpointer struct{ dest string }

func (r *recordingCheckpointer) Checkpoint(destDir string) error {
	r.dest = destDir
	if _, err := os.Stat(destDir); err == nil {
		return errors.New("pebble contract: destDir must not exist")
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destDir, "000042.sst"), []byte("sst-bytes"), 0o644)
}

func withDeviceIDs(t *testing.T, fn func(string) (uint64, bool, error)) {
	t.Helper()
	prev := deviceIDFn
	t.Cleanup(func() { deviceIDFn = prev })
	deviceIDFn = fn
}

func archiveEntryNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		names = append(names, h.Name)
	}
}

// THE regression test for 2026-09-22. The checkpoint was staged inside
// BackupDir, which production keeps on a different ZFS dataset from the
// database; hard links failed with EXDEV, Pebble silently copied 14 GB, and
// the watchdog killed the scan the backup ran in. The checkpoint must be
// staged beside the database, never under BackupDir.
func TestCreateBackupWithCheckpoint_StagesBesideTheDatabase(t *testing.T) {
	withDiskStats(t, 1<<40, 1<<40, nil)
	parent := t.TempDir()
	db := filepath.Join(parent, "audiobooks.pebble")
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(t.TempDir(), "backups")

	cp := &recordingCheckpointer{}
	info, err := CreateBackupWithCheckpoint(cp, db, "pebble", BackupConfig{BackupDir: backupDir, MaxBackups: 5})
	if err != nil {
		t.Fatalf("backup: %v", err)
	}

	stagingRoot := filepath.Join(parent, checkpointStagingDirName)
	if got := filepath.Dir(filepath.Dir(cp.dest)); got != stagingRoot {
		t.Errorf("checkpoint staged under %s, want under %s (beside the database)", got, stagingRoot)
	}
	if strings.HasPrefix(cp.dest, backupDir) {
		t.Errorf("checkpoint staged inside BackupDir (%s): a cross-filesystem BackupDir makes Pebble copy the whole database", cp.dest)
	}
	if cp.dest == db {
		t.Fatal("checkpoint targeted the live database directory")
	}

	// Entries keep the database's own name so restore recreates it.
	names := archiveEntryNames(t, info.Path)
	want := "audiobooks.pebble/000042.sst"
	found := false
	for _, n := range names {
		if n == want {
			found = true
		}
	}
	if !found {
		t.Errorf("archive entries %v lack %q", names, want)
	}

	// The run dir is removed; the staging root itself may stay.
	entries, _ := os.ReadDir(stagingRoot)
	if len(entries) != 0 {
		t.Errorf("staging root not cleaned up: %d entries left", len(entries))
	}
}

func TestCreateBackupWithCheckpoint_RefusesCrossDeviceStaging(t *testing.T) {
	withDiskStats(t, 1<<40, 1<<40, nil)
	parent := t.TempDir()
	db := filepath.Join(parent, "audiobooks.pebble")
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}
	withDeviceIDs(t, func(p string) (uint64, bool, error) {
		if strings.Contains(p, checkpointStagingDirName) {
			return 61, true, nil
		}
		return 1048818, true, nil
	})

	cp := &recordingCheckpointer{}
	_, err := CreateBackupWithCheckpoint(cp, db, "pebble", BackupConfig{BackupDir: filepath.Join(t.TempDir(), "b")})
	if !errors.Is(err, ErrCheckpointCrossDevice) {
		t.Fatalf("want ErrCheckpointCrossDevice, got %v", err)
	}
	if cp.dest != "" {
		t.Error("Checkpoint ran across devices; Pebble would have byte-copied the database")
	}
}

// An unknown device (no st_dev on the platform) must not refuse the backup.
func TestEnsureSameDevice_UnknownDevicePasses(t *testing.T) {
	withDeviceIDs(t, func(p string) (uint64, bool, error) {
		if strings.Contains(p, "staging") {
			return 0, false, nil
		}
		return 7, true, nil
	})
	if err := ensureSameDevice("/x/staging", "/x/db"); err != nil {
		t.Fatalf("unknown device refused: %v", err)
	}
}

func TestCreateBackupWithCheckpoint_CanceledBeforeCheckpointDoesNotStartIt(t *testing.T) {
	withDiskStats(t, 1<<40, 1<<40, nil)
	parent := t.TempDir()
	db := filepath.Join(parent, "audiobooks.pebble")
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("run canceled")
	cp := &recordingCheckpointer{}
	_, err := CreateBackupWithCheckpoint(cp, db, "pebble", BackupConfig{
		BackupDir: filepath.Join(t.TempDir(), "b"),
		Progress:  func(string, int, int64) error { return stop },
	})
	if !errors.Is(err, stop) {
		t.Fatalf("want the progress callback's error, got %v", err)
	}
	if cp.dest != "" {
		t.Error("checkpoint started after the caller had already canceled")
	}
}

func TestSweepStaleCheckpoints_RemovesOnlyStaleMatchingDirs(t *testing.T) {
	staging := t.TempDir()
	backupDir := t.TempDir()
	now := time.Now()
	old := now.Add(-48 * time.Hour)

	mk := func(dir, name string, mtime time.Time, isDir bool) string {
		p := filepath.Join(dir, name)
		if isDir {
			if err := os.MkdirAll(filepath.Join(p, "inner"), 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	staleRun := mk(staging, "run-111", old, true)
	freshRun := mk(staging, "run-222", now, true)
	otherInStaging := mk(staging, "keepme", old, true)
	staleLegacy := mk(backupDir, "pebble-checkpoint-1277797855", old, true)
	freshLegacy := mk(backupDir, "pebble-checkpoint-999", now, true)
	archive := mk(backupDir, "audiobooks_pebble_20260101_000000.tar.gz", old, false)
	legacyActivity := mk(backupDir, "legacy-activity", old, true)
	namedLikeLegacyFile := mk(backupDir, "pebble-checkpoint-file", old, false)

	sweepStaleCheckpoints(staging, backupDir, now)

	gone := map[string]string{"stale run dir": staleRun, "stale legacy checkpoint": staleLegacy}
	kept := map[string]string{
		"fresh run dir": freshRun, "unrelated staging dir": otherInStaging,
		"fresh legacy checkpoint": freshLegacy, "archive": archive,
		"legacy-activity": legacyActivity, "file named like a checkpoint": namedLikeLegacyFile,
	}
	for what, p := range gone {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep: %s", what, p)
		}
	}
	for what, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed by the sweep (%v): %s", what, err, p)
		}
	}
}
