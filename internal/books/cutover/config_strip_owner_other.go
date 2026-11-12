// file: internal/books/cutover/config_strip_owner_other.go
// version: 1.0.0
// guid: 7d9a1b2c-3e4f-4a5b-8c6d-9e0f1a2b3c4d
// last-edited: 2026-11-11

//go:build !unix

package cutover

import "os"

// fileOwner reports no owner on platforms without numeric uid/gid.
func fileOwner(os.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
