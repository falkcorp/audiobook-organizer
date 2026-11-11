// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/clean/clean.go
// version: 2.0.0
// guid: af42c461-77e6-43a6-99c0-db11c31b5361
// last-edited: 2026-11-10

// Package clean is a well-formed module package: tasklint reports nothing.
package clean

import (
	"github.com/falkcorp/mediacore/internal/tasklintcases/store"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

type Deps struct {
	Books store.BookReader
}

func common(title string) tasks.Common {
	return tasks.Common{Title: title, Help: "Synthetic fixture kind.", Effects: tasks.Writes("clean.books")}
}

var report = tasks.Task("clean.report", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common("report")})

// scan reads through the narrow dependency and writes through the Writer.
func scan(d Deps) tasks.Definition {
	return tasks.Batch("clean.scan", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common("scan"),
		Parallelism: tasks.Sequential("one remote session at a time"),
		Item: func(ic *tasks.ItemRC, p tasks.NoParams, id string) error {
			if _, err := d.Books.GetBook(id); err != nil {
				return err
			}
			return ic.Writer().Do("clean.touch")
		}})
}

func Register(r *module.Registrar, d Deps) {
	defs := []tasks.Definition{report}
	defs = append(defs, scan(d))
	r.Tasks(defs...)
}
