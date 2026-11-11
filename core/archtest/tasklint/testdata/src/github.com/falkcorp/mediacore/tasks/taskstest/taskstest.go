// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/tasks/taskstest/taskstest.go
// version: 2.0.0
// guid: 8145adc2-126a-44e2-83ac-337d77b3807c
// last-edited: 2026-11-10

// Package taskstest stands in for core/tasks/taskstest, which tasklint never
// reports: this unregistered definition and empty Sequential reason are not
// findings.
package taskstest

import "github.com/falkcorp/mediacore/tasks"

var Probe = tasks.Task("taskstest.probe", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{})

var Seq = tasks.Sequential("")
