// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/wire/wire.go
// version: 1.0.0
// guid: ef40ac98-ead0-4b44-a0d2-9b20c82c5634
// last-edited: 2026-11-10

// Package wire calls listowner's list function.
package wire

import "github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/listowner"

func Count() int { return len(listowner.Kinds()) }
