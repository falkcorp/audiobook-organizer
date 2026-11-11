// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/list_funcs/list.go
// version: 1.1.0
// guid: 6c4628e3-0472-4d5f-b887-c1ca8e3617d1
// last-edited: 2026-11-10

// Package list_funcs is a books-style converted package before the F5 flip:
// it registers nothing itself and hands its kinds out through a list function
// (05-tasks §5T.8.2 rule 4), which -list-funcs makes a registration root.
package list_funcs

import "github.com/falkcorp/mediacore/tasks"

var common = tasks.Common{Title: "t", Help: "synthetic"}

func build(kind tasks.Kind, sched func(tasks.Kind) *int) tasks.Definition {
	return tasks.Task(kind, tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common})
}

// Kinds is the list function; Wire calls it, so it is a root.
func Kinds(sched func(tasks.Kind) *int) []tasks.Definition {
	return []tasks.Definition{build("listf.a", sched)}
}

// Wire stands for books' wiring, which calls the list function.
func Wire() int { return len(Kinds(nil)) }

// Unwired is a list function nothing calls: it registers nothing, so the kind
// its helper call builds is reported at that call.
func Unwired() []tasks.Definition {
	return []tasks.Definition{build("listf.unwired", nil)} // want `definition built by build in Unwired is not registered: no Registrar.Tasks call in this package and no called list function`
}

func stray() tasks.Definition {
	return tasks.Task("listf.stray", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common}) // want `tasks.Task definition in stray is not registered: no Registrar.Tasks call in this package and no called list function`
}
