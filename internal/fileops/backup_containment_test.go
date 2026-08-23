// file: internal/fileops/backup_containment_test.go
// version: 1.0.1
// guid: bf4ef376-6572-46ff-aeaf-d7d24fb2cc8d
// last-edited: 2026-08-23

package fileops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The go/path-injection suppressions on the two rollback `os.Stat(op.backupPath)`
// sinks in safe_operations.go cite one barrier: NewFileOperation derives
// backupPath through safepath.Join, so it cannot leave the backup directory.
// These tests pin that barrier so removing it breaks CI instead of silently
// invalidating the justification.

// escapingBackupDirIsRejected is the negative case: a relative BackupDir that
// climbs out of the target's directory must be refused, and nothing may be
// created at the escaped location.
func TestNewFileOperation_EscapingBackupDirIsRejected(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "library", "book")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("setup: MkdirAll: %v", err)
	}
	srcFile := filepath.Join(work, "source.m4b")
	if err := os.WriteFile(srcFile, []byte("audio"), 0o644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}
	dstFile := filepath.Join(work, "dest.m4b")

	escapeDir := filepath.Join(root, "escape-backups")

	cfg := DefaultConfig()
	cfg.BackupDir = filepath.Join("..", "..", "escape-backups")

	op, err := NewFileOperation(srcFile, dstFile, cfg)
	if err == nil {
		// Errorf, not Fatalf: the filesystem assertion below is the stronger
		// check and must still run when this one fails.
		t.Errorf("NewFileOperation accepted an escaping BackupDir; backupPath=%q", op.backupPath)
	}

	// Assert on the filesystem, not on the shape of the error string: the
	// escaped directory must not have been created.
	if _, statErr := os.Stat(escapeDir); !os.IsNotExist(statErr) {
		t.Fatalf("escaped backup directory %q exists (stat err %v); the containment barrier let a write through", escapeDir, statErr)
	}
}

// TestNewFileOperation_NestedBackupDirStillWorks is the positive control: code
// that rejected every relative BackupDir would pass the negative test above
// while breaking the default configuration. A legitimate nested relative
// BackupDir must still produce a working, committable operation.
func TestNewFileOperation_NestedBackupDirStillWorks(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "library", "book")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("setup: MkdirAll: %v", err)
	}
	srcFile := filepath.Join(work, "source.m4b")
	content := []byte("audio payload")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}
	dstFile := filepath.Join(work, "dest.m4b")

	cfg := DefaultConfig()
	cfg.PreserveOriginal = true
	cfg.BackupDir = filepath.Join("sub", ".audiobook-backups")

	op, err := NewFileOperation(srcFile, dstFile, cfg)
	if err != nil {
		t.Fatalf("NewFileOperation rejected a legitimate nested BackupDir: %v", err)
	}

	wantDir := filepath.Join(work, "sub", ".audiobook-backups")
	if !strings.HasPrefix(op.backupPath, wantDir+string(filepath.Separator)) {
		t.Fatalf("resolved backupPath %q is not inside %q", op.backupPath, wantDir)
	}
	if info, statErr := os.Stat(wantDir); statErr != nil || !info.IsDir() {
		t.Fatalf("backup directory %q was not created (err %v)", wantDir, statErr)
	}

	if err := op.Execute(); err != nil {
		t.Fatalf("Execute on a legitimate nested BackupDir failed: %v", err)
	}
	got, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("target content = %q; want %q", got, content)
	}
	if err := op.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
}
