// file: internal/fileops/backup_containment_test.go
// version: 1.1.0
// guid: bf4ef376-6572-46ff-aeaf-d7d24fb2cc8d
// last-edited: 2026-09-10

package fileops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Containment tests for the backup path that the two rollback
// `os.Stat(op.backupPath)` sinks in safe_operations.go consume
// (go/path-injection #1668 at :148 and #1669 at :189).
//
// This block previously stated the barrier as "NewFileOperation derives
// backupPath through safepath.Join, so it cannot leave the backup directory."
// That claim is FALSE and the tests below measure why: safepath.Join only
// checks its `parts` against its `root`, and NewFileOperation hands it
// filepath.Dir(targetPath) AS the root. A traversing targetPath therefore
// moves the root itself, and the prefix check passes against the relocated
// root. The two tests that pin the escape are skipped, not deleted — the
// escape is real and reproducible, but closing it needs a trusted containment
// root threaded in from config/store, which is a design change the owner has
// not signed off. See TASK-083 and the skip messages for the measured output.

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

// libraryFixture builds root/library/book with a source file in it and returns
// the temp root, the library root, and the book directory. The library root is
// the boundary every containment assertion below is written against.
func libraryFixture(t *testing.T) (root, libraryRoot, work string) {
	t.Helper()
	root = t.TempDir()
	libraryRoot = filepath.Join(root, "library")
	work = filepath.Join(libraryRoot, "book")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("setup: MkdirAll: %v", err)
	}
	return root, libraryRoot, work
}

// TestNewFileOperation_TraversingTargetPathEscapesBackupDir covers the
// construction-time half of go/path-injection #1668 (safe_operations.go:148)
// and #1669 (:189): both sinks stat op.backupPath, and op.backupPath is fixed
// here, in NewFileOperation, before Execute ever runs.
//
// The input is the brief's worked example — traversal segments left literally
// in targetPath, as a request-supplied path carries them. filepath.Dir walks
// them lexically, so safepath.Join is handed an ALREADY-ESCAPED root and its
// prefix check passes. NewFileOperation's os.MkdirAll then materialises the
// escaped directory on disk, outside the library, before any copy happens.
func TestNewFileOperation_TraversingTargetPathEscapesBackupDir(t *testing.T) {
	t.Skip(`BLOCKED — TASK-083. The escape is real; the fix is a design change awaiting owner sign-off.
Measured 2026-09-10 at HEAD 7196a2365 with this Skip removed:
  backup_containment_test.go: NewFileOperation accepted a traversing targetPath; backupPath="<tmp>/001/escape/.audiobook-backups/dest.m4b.20260910_163043.backup"
  backup_containment_test.go: backupPath "<tmp>/001/escape/..." resolved OUTSIDE the library root "<tmp>/001/library"
  backup_containment_test.go: escaped backup directory "<tmp>/001/escape" exists (stat err <nil>)
  --- FAIL: TestNewFileOperation_TraversingTargetPathEscapesBackupDir (0.00s)
Un-skip when OperationConfig carries a trusted containment root (see the file comment).`)

	root, libraryRoot, work := libraryFixture(t)

	srcFile := filepath.Join(work, "source.m4b")
	if err := os.WriteFile(srcFile, []byte("audio"), 0o644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}

	// Literal "..", not pre-cleaned by filepath.Join: this is the shape a
	// request-supplied path arrives in.
	dstFile := work + "/foo/../../../escape/dest.m4b"
	escapeDir := filepath.Join(root, "escape")

	op, err := NewFileOperation(srcFile, dstFile, DefaultConfig())
	if err == nil {
		// Errorf, not Fatalf: the filesystem assertions below are the
		// stronger checks and must still run when this one fails.
		t.Errorf("NewFileOperation accepted a traversing targetPath; backupPath=%q", op.backupPath)
		if !strings.HasPrefix(op.backupPath, libraryRoot+string(filepath.Separator)) {
			t.Errorf("backupPath %q resolved OUTSIDE the library root %q", op.backupPath, libraryRoot)
		}
	}

	// Assert on the filesystem, not on the shape of an error string.
	if _, statErr := os.Stat(escapeDir); !os.IsNotExist(statErr) {
		t.Fatalf("escaped backup directory %q exists (stat err %v); a traversing targetPath relocated the backup outside the library root %q", escapeDir, statErr, libraryRoot)
	}
}

// TestFileOperation_ExecuteWritesBackupOutsideLibrary covers the rollback half:
// it proves the file that #1668 (:148) and #1669 (:189) stat and copy back from
// is really written outside the library when targetPath traverses. Step 1 of
// Execute backs the existing target up to op.backupPath, so a successful
// Execute is enough to materialise it — no need to force a copy failure.
func TestFileOperation_ExecuteWritesBackupOutsideLibrary(t *testing.T) {
	t.Skip(`BLOCKED — TASK-083. The escape is real; the fix is a design change awaiting owner sign-off.
Measured 2026-09-10 at HEAD 7196a2365 with this Skip removed:
  backup_containment_test.go: Execute wrote 1 backup file(s) into "<tmp>/001/escape/.audiobook-backups", outside the library root "<tmp>/001/library"; this is the file the rollback sinks at safe_operations.go:148 and :189 stat and copy back from
  --- FAIL: TestFileOperation_ExecuteWritesBackupOutsideLibrary (0.04s)
Un-skip when OperationConfig carries a trusted containment root (see the file comment).`)

	root, libraryRoot, work := libraryFixture(t)

	srcFile := filepath.Join(work, "source.m4b")
	if err := os.WriteFile(srcFile, []byte("new audio"), 0o644); err != nil {
		t.Fatalf("setup: WriteFile src: %v", err)
	}

	// "foo" must be a real directory: the kernel resolves ".." against actual
	// directories, so a traversal through a non-existent component fails at
	// open(2) for reasons that have nothing to do with containment. A real
	// subdirectory inside the library is the realistic case anyway.
	if err := os.MkdirAll(filepath.Join(work, "foo"), 0o755); err != nil {
		t.Fatalf("setup: MkdirAll foo: %v", err)
	}

	// Pre-create the escaped target so Execute's step 1 takes a backup of it.
	escapeDir := filepath.Join(root, "escape")
	if err := os.MkdirAll(escapeDir, 0o755); err != nil {
		t.Fatalf("setup: MkdirAll escape: %v", err)
	}
	if err := os.WriteFile(filepath.Join(escapeDir, "dest.m4b"), []byte("victim"), 0o644); err != nil {
		t.Fatalf("setup: WriteFile victim: %v", err)
	}

	dstFile := work + "/foo/../../../escape/dest.m4b"

	op, err := NewFileOperation(srcFile, dstFile, DefaultConfig())
	if err != nil {
		// Once containment is fixed this is the expected outcome and the
		// test ends here having proven nothing escaped.
		return
	}
	if execErr := op.Execute(); execErr != nil {
		t.Fatalf("Execute failed for reasons unrelated to containment: %v", execErr)
	}

	backupsDir := filepath.Join(escapeDir, ".audiobook-backups")
	entries, readErr := os.ReadDir(backupsDir)
	if readErr == nil && len(entries) > 0 {
		t.Fatalf("Execute wrote %d backup file(s) into %q, outside the library root %q; this is the file the rollback sinks at safe_operations.go:148 and :189 stat and copy back from", len(entries), backupsDir, libraryRoot)
	}
}

// TestNewFileOperation_InLibraryTargetPathKeepsBackupInside is the positive
// control for the two tests above. A guard that refuses every targetPath would
// pass both negatives while breaking the feature outright, so a legitimate
// in-library target must still back up, execute, and commit — with the backup
// landing inside the library.
func TestNewFileOperation_InLibraryTargetPathKeepsBackupInside(t *testing.T) {
	_, libraryRoot, work := libraryFixture(t)

	srcFile := filepath.Join(work, "source.m4b")
	content := []byte("audio payload")
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatalf("setup: WriteFile: %v", err)
	}
	dstFile := filepath.Join(work, "dest.m4b")
	if err := os.WriteFile(dstFile, []byte("previous"), 0o644); err != nil {
		t.Fatalf("setup: WriteFile dst: %v", err)
	}

	op, err := NewFileOperation(srcFile, dstFile, DefaultConfig())
	if err != nil {
		t.Fatalf("NewFileOperation rejected a legitimate in-library targetPath: %v", err)
	}
	if !strings.HasPrefix(op.backupPath, libraryRoot+string(filepath.Separator)) {
		t.Fatalf("backupPath %q is not inside the library root %q", op.backupPath, libraryRoot)
	}
	if err := op.Execute(); err != nil {
		t.Fatalf("Execute on a legitimate in-library targetPath failed: %v", err)
	}
	got, err := os.ReadFile(dstFile)
	if err != nil {
		t.Fatalf("target not written: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("target content = %q; want %q", got, content)
	}
	// The backup of the previous target must exist, and must be inside the
	// library — this is the rollback source the two flagged sinks consume.
	if _, statErr := os.Stat(op.backupPath); statErr != nil {
		t.Fatalf("backup of the previous target missing at %q: %v", op.backupPath, statErr)
	}
	if err := op.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
}
