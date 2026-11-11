// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/clean/clean_test.go
// version: 2.0.0
// guid: 262cd051-9412-44a0-a9ce-d84f0b366b2a
// last-edited: 2026-11-10

package clean

import "github.com/falkcorp/mediacore/tasks"

// Test files are never reported: an unregistered kind, an empty Sequential
// reason and a Common without Help here are not findings.
var testOnly = tasks.Task("clean.test-only", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{
	Common: tasks.Common{Title: "x"}})

var seq = tasks.Sequential("")
