// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/missing_help/help.go
// version: 1.0.0
// guid: c6caa0bc-0d38-4c45-9831-bae708916c84
// last-edited: 2026-10-16

// Package missing_help declares defs without Help.
package missing_help

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

type Deps struct{}

func Ops(d Deps) ops.Bundle {
	a := ops.Task("synthetic.a", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Common: ops.Common{Title: "a", Permission: "settings.manage"}}) // want `warning: ops.Common has no Help`
	b := ops.Task("synthetic.b", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Common: ops.Common{Title: "b", Help: " ", Permission: "settings.manage"}}) // want `warning: ops.Common has no Help`
	c := ops.Task("synthetic.c", ops.TaskSpec[ops.NoParams, ops.NoResult]{
		Common: ops.Common{Title: "c", Help: "Says what c does.", Permission: "settings.manage"}})
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{a, b, c}}
}
