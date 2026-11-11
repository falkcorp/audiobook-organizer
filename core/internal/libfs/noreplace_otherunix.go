// file: core/internal/libfs/noreplace_otherunix.go
// version: 1.0.0
// guid: d3f245bd-4c9d-46c8-bb59-978bd89cff29
// last-edited: 2026-11-11

//go:build unix && !linux && !darwin

package libfs

import "errors"

// RenameNoReplace on a unix without an atomic no-replace rename is the
// fallback: a regular file is linked and unlinked (atomic no-clobber), anything
// else takes the checked rename with its documented window.
func RenameNoReplace(od *Dir, oldName string, nd *Dir, newName string) error {
	if err := errors.Join(require(od, nd), checkName(oldName), checkName(newName)); err != nil {
		return err
	}
	return RenameNoReplaceFallback(od, oldName, nd, newName)
}
