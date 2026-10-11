// file: internal/config/persistence_owner_other.go
// version: 1.0.0
// guid: 90e1885a-71ff-4ddd-8e01-e45935344e66
// last-edited: 2026-10-11

//go:build !unix

package config

import "os"

// fileOwner reports no owner on platforms without numeric uid/gid.
func fileOwner(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
