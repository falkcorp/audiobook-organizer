//go:build !unix

// file: core/library/owner_other.go
// version: 1.0.1
// guid: 54fbfe80-14c2-4655-8363-7421cb1bd420
// last-edited: 2026-11-07

package library

import "os"

// OwnedByCurrentUser reports whether info describes a file owned by the user
// this process runs as. Platforms without Unix ownership have nothing to check,
// so it reports true.
func OwnedByCurrentUser(os.FileInfo) bool { return true }

// ownedByUID is OwnedByCurrentUser against an explicit user ID; with no Unix
// ownership there is nothing to compare.
func ownedByUID(os.FileInfo, int) bool { return true }
