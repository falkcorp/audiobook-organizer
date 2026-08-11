// file: internal/organizer/auto_backup_test.go
// version: 1.0.0
// guid: 4b7e2d18-9c53-4a06-8f21-6d5e3a90c471
// last-edited: 2026-08-11

package organizer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// These tests pin the two defects measured on production 2026-08-11, where every
// organize run spent 20-25 minutes on a pre-organize backup that then failed:
//
//	01:54:14 organize starting -> 02:14:42 "Auto-backup failed: failed to add
//	  files to archive: lstat .../audiobooks.pebble/536537.sst: no such file or
//	  directory"                                                      (20m28s)
//	06:31:29 organize starting -> 06:56:00 same failure on 548621.log (24m31s)
//	06:36:36 "registry: strike recorded ... kind=stuck  no progress for 5m8s"
//
// NOTE ON WHAT IS AND IS NOT BEING TESTED. The compaction race itself does not
// reproduce on a small test database: the live-directory walk succeeds here.
// So a test asserting "a backup file was produced" would pass with OR without
// the fix and would be worthless. These tests assert on the METHOD CHOSEN,
// which is what actually regresses.

// storeDecorator wraps a Store the way the production search-index decorator
// does: it satisfies Store by embedding it, exposes Unwrap for
// database.AsCapability, and — crucially — does NOT itself implement
// backup.Checkpointable. A bare `orgSvc.db.(backup.Checkpointable)` therefore
// fails against this type, which is exactly the production bug.
type storeDecorator struct {
	Store
	inner database.Store
}

func (d *storeDecorator) Unwrap() database.Store { return d.inner }

// withBackupEnv points config.AppConfig at a temp database and backup dir, and
// restores the previous globals afterwards.
func withBackupEnv(t *testing.T, dbPath string) string {
	t.Helper()
	prevPath, prevType := config.AppConfig.DatabasePath, config.AppConfig.DatabaseType
	t.Cleanup(func() {
		config.AppConfig.DatabasePath, config.AppConfig.DatabaseType = prevPath, prevType
	})
	config.AppConfig.DatabasePath = dbPath
	config.AppConfig.DatabaseType = "pebble"
	return filepath.Join(filepath.Dir(dbPath), "backups")
}

func TestAutoBackup_UsesCheckpointThroughADecorator(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	pebbleStore, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = pebbleStore.Close() })

	withBackupEnv(t, dbPath)

	wrapped := &storeDecorator{Store: pebbleStore, inner: pebbleStore}

	// Guard: if a bare assertion starts resolving through the decorator, this
	// test has stopped reproducing the production shape and must be rewritten
	// rather than trusted.
	if _, ok := interface{}(wrapped).(interface{ Checkpoint(string) error }); ok {
		t.Fatal("a bare type assertion now resolves Checkpoint through the decorator; " +
			"this test no longer reproduces the production bug")
	}

	svc := NewService(wrapped)
	got := svc.autoBackup(logger.New("test"))

	if got != backupCheckpoint {
		t.Fatalf("autoBackup took the %q path, want %q — the checkpoint capability "+
			"was not resolved through the decorator, so the backup archives the "+
			"LIVE Pebble directory and races compaction", got, backupCheckpoint)
	}
}

func TestAutoBackup_SkipsWhenARecentBackupExists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	pebbleStore, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = pebbleStore.Close() })

	backupDir := withBackupEnv(t, dbPath)
	if mkErr := os.MkdirAll(backupDir, 0o775); mkErr != nil {
		t.Fatalf("mkdir backups: %v", mkErr)
	}
	recent := filepath.Join(backupDir, "audiobooks_pebble_20260811_000000.tar.gz")
	if wErr := os.WriteFile(recent, []byte("not a real archive"), 0o644); wErr != nil {
		t.Fatalf("seed backup: %v", wErr)
	}

	svc := NewService(pebbleStore)
	if got := svc.autoBackup(logger.New("test")); got != backupSkippedRecent {
		t.Fatalf("autoBackup took the %q path, want %q — a fresh backup already "+
			"existed, so re-archiving 14 GB before every organize is pure delay",
			got, backupSkippedRecent)
	}
}

func TestAutoBackup_StillBacksUpWhenTheOnlyBackupIsStale(t *testing.T) {
	// The skip must not become "never back up again". This is the negative
	// control for the test above.
	dbPath := filepath.Join(t.TempDir(), "audiobooks.pebble")
	pebbleStore, err := database.NewPebbleStore(dbPath)
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { _ = pebbleStore.Close() })

	backupDir := withBackupEnv(t, dbPath)
	if mkErr := os.MkdirAll(backupDir, 0o775); mkErr != nil {
		t.Fatalf("mkdir backups: %v", mkErr)
	}
	stale := filepath.Join(backupDir, "audiobooks_pebble_20260810_000000.tar.gz")
	if wErr := os.WriteFile(stale, []byte("not a real archive"), 0o644); wErr != nil {
		t.Fatalf("seed backup: %v", wErr)
	}
	old := time.Now().Add(-2 * autoBackupMinInterval)
	if cErr := os.Chtimes(stale, old, old); cErr != nil {
		t.Fatalf("chtimes: %v", cErr)
	}

	svc := NewService(pebbleStore)
	if got := svc.autoBackup(logger.New("test")); got == backupSkippedRecent {
		t.Fatalf("autoBackup skipped on a backup %s old; the %s freshness window "+
			"must not suppress backups indefinitely", 2*autoBackupMinInterval, autoBackupMinInterval)
	}
}
