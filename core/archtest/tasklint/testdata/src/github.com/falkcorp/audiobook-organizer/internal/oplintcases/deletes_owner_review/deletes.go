// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/deletes_owner_review/deletes.go
// version: 1.0.0
// guid: 77c542fd-e225-4255-866a-44c36cf134ae
// last-edited: 2026-10-16

// Package deletes_owner_review declares and performs deletes.
package deletes_owner_review

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

type Deps struct{}

func Ops(d Deps) ops.Bundle {
	c := ops.Common{Title: "t", Help: "synthetic", Permission: "settings.manage",
		Effects: ops.Writes(ops.Deletes("synthetic_rows"))} // want `ops.Deletes needs owner review`
	signed := ops.Common{Title: "t", Help: "synthetic", Permission: "settings.manage",
		Effects: ops.Writes(ops.Deletes("synthetic_rows"))} //oplint:allow synthetic fixture: owner approved deleting synthetic_rows
	t := ops.Task("synthetic.deleter", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: c,
		Run: func(rc *ops.RC, p ops.NoParams) (ops.NoResult, error) {
			return ops.NoResult{}, rc.Deleter().Delete("synthetic-1") // want `Deleter\(\) needs owner review`
		}})
	u := ops.Task("synthetic.signed", ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: signed})
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{t, u}}
}
