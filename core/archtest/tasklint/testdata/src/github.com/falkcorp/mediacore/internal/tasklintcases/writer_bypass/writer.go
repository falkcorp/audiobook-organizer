// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/writer_bypass/writer.go
// version: 2.0.0
// guid: bef81223-dafb-4c63-9f6b-b3e836ae234b
// last-edited: 2026-11-10

// Package writer_bypass writes the store from inside definitions.
package writer_bypass

import (
	"github.com/falkcorp/mediacore/internal/tasklintcases/store"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/tasks"
)

// Deps holds narrow interfaces only, so store-capture stays quiet.
type Deps struct {
	Books store.BookWriter
	Read  store.BookReader
}

var common = tasks.Common{Title: "t", Help: "synthetic"}

// touch is a same-package helper the Item calls: one level deep.
func touch(w store.BookWriter, id string) error {
	return w.UpdateBook(id, nil) // want `store.BookWriter.UpdateBook writes the store directly inside Item \(via a function it calls\)`
}

// deeper is two levels down from the Item: out of the rule's reach.
func deeper(w store.BookWriter) error { return w.DeleteBook("harvey") }

func viaTwo(w store.BookWriter) error { return deeper(w) }

func Register(r *module.Registrar, d Deps) {
	task := tasks.Task("writer.task", tasks.TaskSpec[tasks.NoParams, tasks.NoResult]{Common: common,
		Run: func(rc *tasks.RC, p tasks.NoParams) (tasks.NoResult, error) {
			_, err := d.Read.GetBook("bruce") // a read: fine
			if err != nil {
				return tasks.NoResult{}, err
			}
			return tasks.NoResult{}, d.Books.UpdateBook("bruce", nil) // want `store.BookWriter.UpdateBook writes the store directly inside Run`
		}})
	batch := tasks.Batch("writer.batch", tasks.BatchSpec[tasks.NoParams, string, tasks.NoResult]{Common: common,
		Parallelism: tasks.CPU(),
		Item: func(ic *tasks.ItemRC, p tasks.NoParams, id string) error {
			if err := touch(d.Books, id); err != nil {
				return err
			}
			return viaTwo(d.Books)
		}})
	fixer := tasks.Fixer("writer.fixer", tasks.FixerSpec[tasks.NoParams, string]{Common: common,
		Parallelism: tasks.CPU(),
		Apply: func(ic *tasks.ItemRC, w tasks.Writer, p tasks.NoParams, row tasks.Row) error {
			return d.Books.DeleteBook("selina") //tasklint:allow synthetic fixture: the owner signed off on this delete
		},
		AfterApply: func(rc *tasks.RC, applied []string) error {
			return d.Books.UpdateBook("oswald", nil) //tasklint:allow // want `//tasklint:allow needs a reason after it`
		}})
	r.Tasks(task, batch, fixer)
}
