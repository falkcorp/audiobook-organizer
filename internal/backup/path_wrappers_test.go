// file: internal/backup/path_wrappers_test.go
// version: 1.0.3
// guid: 747b560c-7210-40b1-bfb9-50efcbb78f38
// last-edited: 2026-11-09

package backup

import "path/filepath"

// extractBackup extracts the archive at backupPath into targetPath, every
// entry confined to it: the archive mechanics the round-trip tests check.
// Production never extracts beside a live store any more; the live restore
// stages through StageRestore and ExtractStore (R1-25).
func extractBackup(backupPath, targetPath string) error {
	return extractArchive(backupPath, targetPath, nil)
}

// DeleteBackup deletes the archive at backupPath and its checksum sidecar. It
// exists only for tests that already hold a full path; production callers pass
// a backup directory and a raw filename to DeleteBackupIn.
func DeleteBackup(backupPath string) error {
	return DeleteBackupIn(filepath.Dir(backupPath), filepath.Base(backupPath))
}
