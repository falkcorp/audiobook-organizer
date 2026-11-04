// file: internal/testutil/durtest/durtest.go
// version: 1.0.0
// guid: 3c0a6f5e-8b1d-4f27-9a64-5d2e7c91b0a8
// last-edited: 2026-11-04

// Package durtest holds the one read helper tests need for a book_file
// Duration, which is a *int (nil = not measured). It imports nothing, so
// package database's own tests can use it without an import cycle.
package durtest

// Sec returns *p, or 0 when p is nil. It is for assertions and failure
// messages in tests ONLY: it deliberately conflates "not measured" with 0 so a
// failing comparison prints a number instead of panicking. A test that cares
// whether a duration is known compares the field with nil, or goes through
// DurationSec / DurationState. Production code never uses this.
func Sec(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
