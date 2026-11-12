// file: core/library/openread_other.go
// version: 1.0.0
// guid: 101df606-ec0d-419b-b36a-2b453bbeab70
// last-edited: 2026-11-11

//go:build !unix

package library

// openNonblock: there are no FIFOs to wait on.
const openNonblock = 0
