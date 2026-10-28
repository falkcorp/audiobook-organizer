// file: internal/versions/rename_noreplace_other.go
// version: 1.0.0
// guid: 4c7a2e58-1f93-4d06-b8a4-0e5d9f3c7b61
// last-edited: 2026-10-28

//go:build !linux && !darwin

package versions

// renameNoReplace is renameChecked here: this platform has no atomic
// no-replace rename wired in, so the time-of-check/time-of-use window described
// on renameChecked applies.
func renameNoReplace(oldPath, newPath string) error {
	return renameChecked(oldPath, newPath)
}
