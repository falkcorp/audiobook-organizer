// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/stray/stray.go
// version: 1.0.0
// guid: b6fc9aea-7c88-4dd4-ba46-f2821df7e58f
// last-edited: 2026-11-10

// Package stray builds kinds that no loaded package registers.
package stray

import (
	"github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/owner"
	"github.com/falkcorp/mediacore/tasks"
)

// Forgotten has no registrar anywhere.
var Forgotten = tasks.Task("stray.forgotten", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: tasks.Common{Help: "synthetic"}})

// Lost calls the owner's helper; that call site is the kind site.
func Lost() tasks.Definition { return owner.Build("stray.lost") }
