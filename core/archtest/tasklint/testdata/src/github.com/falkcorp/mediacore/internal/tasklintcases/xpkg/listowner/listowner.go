// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/listowner/listowner.go
// version: 1.0.0
// guid: 6053f70a-c67a-4631-8c0e-2a40b2e5cf2c
// last-edited: 2026-11-10

// Package listowner hands its kinds out through a list function that the wire
// package calls (books before the F5 flip, with ListFuncs).
package listowner

import "github.com/falkcorp/mediacore/tasks"

func Kinds() []tasks.Definition {
	return []tasks.Definition{tasks.Task("listowner.a", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: tasks.Common{Help: "synthetic"}})}
}
