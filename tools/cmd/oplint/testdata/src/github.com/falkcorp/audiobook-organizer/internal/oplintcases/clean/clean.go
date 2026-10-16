// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/clean/clean.go
// version: 1.0.0
// guid: af42c461-77e6-43a6-99c0-db11c31b5361
// last-edited: 2026-10-16

// Package clean is a well-formed bundle: oplint reports nothing.
package clean

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

type Deps struct {
	Books database.BookReader
}

func common(title string) ops.Common {
	return ops.Common{Title: title, Help: "Synthetic fixture def.", Permission: "settings.manage",
		Effects: ops.Writes("books")}
}

var report = ops.Task("synthetic.report", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: common("report")})

func Ops(d Deps) ops.Bundle {
	scan := ops.Batch("synthetic.scan", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: common("scan"),
		Concurrency: ops.Sequential("one remote session at a time"),
		Item: func(rc *ops.ItemRC, p ops.NoParams, id string) error {
			if _, err := d.Books.GetBook(id); err != nil {
				return err
			}
			return rc.Writer().Effect()
		}})
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{report, scan}}
}
