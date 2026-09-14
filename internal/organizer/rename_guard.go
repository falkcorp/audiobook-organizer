// file: internal/organizer/rename_guard.go
// version: 1.0.0
// guid: 2b8f4d61-7c3e-4a95-b1d0-9e6a5c38f147
// last-edited: 2026-09-14

package organizer

import (
	"errors"
	"path/filepath"
	"sync/atomic"
)

// ErrProtectedRenameSource reports a rename plan that would move a protected
// file (a Deluge seeding file, the iTunes library). RenameFiles refuses the
// whole plan before anything moves.
var ErrProtectedRenameSource = errors.New("rename plan would move a protected file")

// renameSourceGuard reports whether a rename source is protected. Nil (the
// default, and in tests that do not set it) protects nothing.
var renameSourceGuard atomic.Pointer[func(string) bool]

// SetRenameSourceGuard installs the predicate RenameFiles consults for every
// source it would move. The server wires the protected-path check, including
// the Deluge save paths. Returns a func restoring the previous guard.
//
// Until 2026-09-14 RenameFiles had no protected-path check of its own: the
// callers filtered with checks that did not know the Deluge save paths, so a
// seeding file reached os.Rename.
func SetRenameSourceGuard(fn func(string) bool) (restore func()) {
	var p *func(string) bool
	if fn != nil {
		p = &fn
	}
	prev := renameSourceGuard.Swap(p)
	return func() { renameSourceGuard.Store(prev) }
}

// protectedRenameSources lists the sources in entries the guard protects,
// skipping entries already at their target (nothing moves for those).
func protectedRenameSources(entries []FileRenameEntry) []string {
	g := renameSourceGuard.Load()
	if g == nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if filepath.Clean(e.SourcePath) == filepath.Clean(e.TargetPath) {
			continue
		}
		if (*g)(e.SourcePath) {
			out = append(out, e.SourcePath)
		}
	}
	return out
}
