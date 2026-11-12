// file: core/library/openread_unix.go
// version: 1.0.0
// guid: 6da28e66-f6dd-4232-9b39-e66ee04105b5
// last-edited: 2026-11-11

//go:build unix

package library

import "syscall"

// openNonblock keeps the open of a FIFO from waiting for a writer.
const openNonblock = syscall.O_NONBLOCK
