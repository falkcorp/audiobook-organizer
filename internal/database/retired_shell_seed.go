// file: internal/database/retired_shell_seed.go
// version: 1.0.0
// guid: 5c7d1e3a-9b24-4f60-8a1d-2e6b0c4f7a93
// last-edited: 2026-10-28

package database

import (
	"errors"
	"testing"
)

// ErrShellDeleteOutsideTest is what DeleteRetiredShellForTest returns when it
// is called from a binary that is not a test.
var ErrShellDeleteOutsideTest = errors.New("DeleteRetiredShellForTest called outside a test binary")

// DeleteRetiredShellForTest is DeleteBook for tests that need a retire shell
// that no longer exists: the state a revert of a retire meets after a delete
// that predates the refusal (DeleteBook refuses a shell that still holds
// retired versions; see retired_shell.go) took the shell away. Everything else
// DeleteBook checks still applies; only the retired-shell refusal is skipped.
// Outside a test binary it deletes nothing and returns ErrShellDeleteOutsideTest:
// no production path may delete a shell, which is the seam the journaled purge
// will add its own entry point for.
func (p *PebbleStore) DeleteRetiredShellForTest(id string) error {
	if !testing.Testing() {
		return ErrShellDeleteOutsideTest
	}
	return p.deleteBook(id, deleteBookOpts{allowRetiredShell: true})
}
