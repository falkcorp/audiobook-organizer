// file: core/internal/backuparchive/open_other.go
// version: 1.0.0
// guid: 50e57d6d-723e-4108-8b1e-9120ce0d9742
// last-edited: 2026-11-12

//go:build !unix

package backuparchive

// openNoFollow is 0 where the platform has no O_NOFOLLOW: OpenRegular's
// Lstat-then-Fstat SameFile check is the whole defence there.
const openNoFollow = 0
