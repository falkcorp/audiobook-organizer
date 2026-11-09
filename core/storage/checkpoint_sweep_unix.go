// file: core/storage/checkpoint_sweep_unix.go
// version: 1.0.1
// guid: 0e4b8d21-7c3a-4f59-b6e2-91a7d5c0f348
// last-edited: 2026-11-09

//go:build unix

package storage

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// unixModes: file modes carry the owner/group/other permission bits the
// sweep's mode check reads (§5S.3.12 "Modes", SEC-9).
const unixModes = true

// checkSweepLock refuses the file at the sweep lock's name (path) in cpDir
// before a take opens it (planSweepLock): one that is not a regular file,
// or that the checkpoint directory's owner does not own. Lstat, so a
// symbolic link is refused rather than followed. An absent file is the
// take's to create.
//
// It checks the name before the take opens it, not the file opened, as
// D24's swap lock does (os.Root, then Lstat and SameFile): between the two
// only the directory's owner or root can change the name, since the
// checkpoint directory is 0700, so this is a check of what an earlier run
// left behind, not a defence against a hostile local user.
func checkSweepLock(cpDir, path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("storage: the checkpoint sweep lock %s: %w", path, err)
	}
	di, err := os.Stat(cpDir)
	if err != nil {
		return fmt.Errorf("storage: the checkpoint directory %s: %w", cpDir, err)
	}
	file, fok := fi.Sys().(*syscall.Stat_t)
	dir, dok := di.Sys().(*syscall.Stat_t)
	if !fok || !dok {
		return fmt.Errorf("storage: the checkpoint sweep lock %s: no stat(2) record", path)
	}
	return planSweepLock(path, fi.Mode().IsRegular(), file.Uid, dir.Uid)
}
