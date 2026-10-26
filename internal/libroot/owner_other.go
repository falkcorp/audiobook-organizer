//go:build !unix

// file: internal/libroot/owner_other.go
// version: 1.0.0
// guid: 54fbfe80-14c2-4655-8363-7421cb1bd420
// last-edited: 2026-10-26

package libroot

import "os"

// OwnedByCurrentUser reports whether info describes a file owned by the user
// this process runs as. Platforms without Unix ownership have nothing to check,
// so it reports true.
func OwnedByCurrentUser(os.FileInfo) bool { return true }
