// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/writer_bypass/writer.go
// version: 1.0.0
// guid: bef81223-dafb-4c63-9f6b-b3e836ae234b
// last-edited: 2026-10-16

// Package writer_bypass writes the store from inside definitions.
package writer_bypass

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Deps holds narrow interfaces only, so store-in-deps stays quiet.
type Deps struct {
	Books database.BookWriter
	Read  database.BookReader
}

var common = ops.Common{Title: "t", Help: "synthetic", Permission: "settings.manage"}

// touch is a same-package helper the Item calls: one level deep.
func touch(w database.BookWriter, id string) error {
	return w.UpdateBook(id, nil) // want `database.BookWriter.UpdateBook writes the store directly inside Item \(via a function it calls\)`
}

// deeper is two levels down from the Item: out of the rule's reach.
func deeper(w database.BookWriter) error { return w.DeleteBook("synthetic-2") }

func viaTwo(w database.BookWriter) error { return deeper(w) }

func Ops(d Deps) ops.Bundle {
	task := ops.Task("synthetic.task", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: common,
		Run: func(rc *ops.RC, p ops.NoParams) (ops.NoResult, error) {
			_, err := d.Read.GetBook("synthetic-1") // a read: fine
			if err != nil {
				return ops.NoResult{}, err
			}
			return ops.NoResult{}, d.Books.UpdateBook("synthetic-1", nil) // want `database.BookWriter.UpdateBook writes the store directly inside Run`
		}})
	batch := ops.Batch("synthetic.batch", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: common,
		Item: func(rc *ops.ItemRC, p ops.NoParams, id string) error {
			if err := touch(d.Books, id); err != nil {
				return err
			}
			return viaTwo(d.Books)
		}})
	fixer := ops.Fixer("synthetic.fixer", ops.FixerSpec[ops.NoParams, string]{Common: common,
		Apply: func(rc *ops.ItemRC, w ops.Writer, p ops.NoParams, row ops.Row) error {
			return d.Books.DeleteBook("synthetic-3") //oplint:allow synthetic fixture: the owner signed off on this delete
		},
		AfterApply: func(rc *ops.RC, applied []string) error {
			return d.Books.UpdateBook("synthetic-4", nil) //oplint:allow // want `//oplint:allow needs a reason after it`
		}})
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{task, batch, fixer}}
}
