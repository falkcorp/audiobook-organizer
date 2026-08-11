// file: internal/fileops/rollback_failure_test.go
// version: 1.0.0
// guid: c81f5a30-7d64-4e2b-96a1-40b7c5e93d18
// last-edited: 2026-08-11

package fileops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A discarded rollback error is worse than no rollback at all: the error the
// caller receives then describes a different world than the one on disk.
//
// Both sites guarded here used to read `_ = copyFile(op.backupPath,
// op.targetPath)`. In each, execution has already modified targetPath by the
// time the rollback runs, so a failed rollback means a damaged file is left
// behind — while Execute() returns "failed to copy file" or "operation failed
// integrity check", both of which read as "nothing happened".
//
// These tests force the rollback itself to fail, which is the only way to
// exercise the branch. A test that merely makes the *copy* fail passes with or
// without the fix.
// ---------------------------------------------------------------------------

// skipIfRoot bails out when permission bits cannot be used to force failure.
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("runs as root: mode bits do not deny access, so the failure cannot be forced")
	}
}

// newOpWithUnreadableBackup builds a real FileOperation whose backup file
// exists but cannot be read, so any rollback attempt fails at os.Open.
func newOpWithUnreadableBackup(t *testing.T, cfg OperationConfig) (*FileOperation, string) {
	t.Helper()

	root := t.TempDir()
	srcPath := filepath.Join(root, "source.mp3")
	if err := os.WriteFile(srcPath, []byte("original audio payload"), 0o644); err != nil {
		t.Fatalf("setup: write source: %v", err)
	}

	// targetDir is read-only, so creating a NEW file inside it fails — that is
	// what makes the forward copy fail without touching the source.
	targetDir := filepath.Join(root, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("setup: mkdir targetDir: %v", err)
	}
	targetPath := filepath.Join(targetDir, "out.mp3")

	cfg.BackupDir = filepath.Join(root, "backups")
	op, err := NewFileOperation(srcPath, targetPath, cfg)
	if err != nil {
		t.Fatalf("setup: NewFileOperation: %v", err)
	}

	// The backup must EXIST (Execute stats it before rolling back) but be
	// unreadable, so copyFile fails at os.Open rather than being skipped.
	if err := os.WriteFile(op.backupPath, []byte("the only intact copy"), 0o644); err != nil {
		t.Fatalf("setup: write backup: %v", err)
	}
	if err := os.Chmod(op.backupPath, 0o000); err != nil {
		t.Fatalf("setup: chmod backup: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(op.backupPath, 0o644) })

	if err := os.Chmod(targetDir, 0o500); err != nil {
		t.Fatalf("setup: chmod targetDir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(targetDir, 0o755) })

	return op, targetPath
}

func TestExecute_CopyFails_AndRollbackFails_SaysSo(t *testing.T) {
	skipIfRoot(t)

	op, _ := newOpWithUnreadableBackup(t, OperationConfig{VerifyChecksums: false, MaxBackups: 5})

	err := op.Execute()
	if err == nil {
		t.Fatal("expected Execute to fail")
	}

	// The old behaviour: exactly "failed to copy file: ..." and nothing about
	// the rollback, so the caller cannot know the target may be damaged.
	if !strings.Contains(err.Error(), "ROLLBACK ALSO FAILED") {
		t.Fatalf("a failed rollback was swallowed — the error does not mention it: %v", err)
	}
	if !strings.Contains(err.Error(), op.backupPath) {
		t.Errorf("the error must name where the intact copy is (%s), got: %v", op.backupPath, err)
	}
}

func TestExecute_ChecksumMismatch_AndRollbackFails_SaysSo(t *testing.T) {
	skipIfRoot(t)

	root := t.TempDir()
	srcPath := filepath.Join(root, "source.mp3")
	if err := os.WriteFile(srcPath, []byte("original audio payload"), 0o644); err != nil {
		t.Fatalf("setup: write source: %v", err)
	}
	targetPath := filepath.Join(root, "out.mp3")

	op, err := NewFileOperation(srcPath, targetPath, OperationConfig{
		VerifyChecksums: true,
		BackupDir:       filepath.Join(root, "backups"),
		MaxBackups:      5,
	})
	if err != nil {
		t.Fatalf("setup: NewFileOperation: %v", err)
	}

	// Force the mismatch branch: the copy will succeed and hash correctly, so
	// the recorded "original" hash is what has to disagree. This reaches the
	// branch that has just PROVEN the target is corrupt.
	op.originalHash = "0000000000000000000000000000000000000000000000000000000000000000"

	if err := os.WriteFile(op.backupPath, []byte("the only intact copy"), 0o644); err != nil {
		t.Fatalf("setup: write backup: %v", err)
	}
	if err := os.Chmod(op.backupPath, 0o000); err != nil {
		t.Fatalf("setup: chmod backup: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(op.backupPath, 0o644) })

	err = op.Execute()
	if err == nil {
		t.Fatal("expected Execute to fail on checksum mismatch")
	}
	if !strings.Contains(err.Error(), "ROLLBACK ALSO FAILED") {
		t.Fatalf("the restore after a PROVEN-corrupt target failed and was swallowed: %v", err)
	}
	if !strings.Contains(err.Error(), "known-corrupt") {
		t.Errorf("the error must say a known-bad file was left in place, got: %v", err)
	}
}

// Control: when the rollback SUCCEEDS the error must stay clean. Without this,
// the tests above would pass against an implementation that shouted about a
// failed rollback on every error.
//
// Note the fixture shape, which the first draft of this test got wrong: the
// forward copy has to fail on its SOURCE (unreadable original), not on its
// destination. Denying writes to the target directory makes the forward copy
// fail — but it makes the rollback fail too, for exactly the same reason, so
// "rollback succeeds" becomes unreachable and the control proves nothing.
func TestExecute_CopyFails_RollbackSucceeds_ErrorStaysClean(t *testing.T) {
	skipIfRoot(t)

	root := t.TempDir()
	srcPath := filepath.Join(root, "source.mp3")
	if err := os.WriteFile(srcPath, []byte("original audio payload"), 0o644); err != nil {
		t.Fatalf("setup: write source: %v", err)
	}
	targetPath := filepath.Join(root, "out.mp3")

	op, err := NewFileOperation(srcPath, targetPath, OperationConfig{
		VerifyChecksums: false,
		BackupDir:       filepath.Join(root, "backups"),
		MaxBackups:      5,
	})
	if err != nil {
		t.Fatalf("setup: NewFileOperation: %v", err)
	}

	// Readable backup this time, so the rollback can succeed.
	backupContent := []byte("the only intact copy")
	if err := os.WriteFile(op.backupPath, backupContent, 0o644); err != nil {
		t.Fatalf("setup: write backup: %v", err)
	}

	// Source unreadable: the forward copy fails at os.Open(src) while the
	// target directory stays writable, so the rollback can complete.
	if err := os.Chmod(srcPath, 0o000); err != nil {
		t.Fatalf("setup: chmod source: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(srcPath, 0o644) })

	err = op.Execute()
	if err == nil {
		t.Fatal("expected Execute to fail")
	}
	if strings.Contains(err.Error(), "ROLLBACK ALSO FAILED") {
		t.Fatalf("rollback succeeded but the error claims it failed: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to copy file") {
		t.Errorf("expected the original copy error, got: %v", err)
	}

	// And the rollback actually restored the backup, not just returned nil.
	got, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("target missing after a supposedly successful rollback: %v", readErr)
	}
	if string(got) != string(backupContent) {
		t.Errorf("target content = %q, want the restored backup %q", got, backupContent)
	}
}
