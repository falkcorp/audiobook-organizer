// file: internal/backup/path_wrappers_test.go
// version: 1.0.1
// guid: 747b560c-7210-40b1-bfb9-50efcbb78f38
// last-edited: 2026-11-08

package backup

import "path/filepath"

// RestoreBackup restores the archive at backupPath. It exists only for tests
// that already hold a full path; production callers (the HTTP handler) pass a
// backup directory and a raw filename to PrepareRestoreIn.
func RestoreBackup(backupPath, targetPath string, verify bool) error {
	return RestoreBackupIn(filepath.Dir(backupPath), filepath.Base(backupPath), targetPath, verify)
}

// RestoreBackupIn is PrepareRestoreIn then Extract, for tests that do not
// care where the restore starts writing. The HTTP handler calls the two
// itself, because it marks the HNSW snapshot diverged between them.
func RestoreBackupIn(backupDir, filename, targetPath string, verify bool) error {
	r, err := PrepareRestoreIn(backupDir, filename, verify)
	if err != nil {
		return err
	}
	return r.Extract(targetPath)
}

// DeleteBackup deletes the archive at backupPath and its checksum sidecar. It
// exists only for tests that already hold a full path; production callers pass
// a backup directory and a raw filename to DeleteBackupIn.
func DeleteBackup(backupPath string) error {
	return DeleteBackupIn(filepath.Dir(backupPath), filepath.Base(backupPath))
}
