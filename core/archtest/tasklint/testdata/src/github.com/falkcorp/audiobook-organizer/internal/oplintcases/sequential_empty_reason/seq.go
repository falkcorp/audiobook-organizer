// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/sequential_empty_reason/seq.go
// version: 1.0.0
// guid: 350ee82a-b42b-42e1-a4ee-e6b7b00d305d
// last-edited: 2026-10-16

// Package sequential_empty_reason declares Sequential with no reason.
package sequential_empty_reason

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

type Deps struct{}

const blank = "   "

var common = ops.Common{Title: "t", Help: "synthetic", Permission: "settings.manage"}

func Ops(d Deps) ops.Bundle {
	a := ops.Batch("synthetic.a", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: common,
		Concurrency: ops.Sequential("")}) // want `ops.Sequential needs a reason`
	b := ops.Batch("synthetic.b", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: common,
		Concurrency: ops.Sequential(blank)}) // want `ops.Sequential needs a reason`
	c := ops.Batch("synthetic.c", ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: common,
		Concurrency: ops.Sequential("one remote session at a time")})
	return ops.Bundle{Plugin: "synthetic", Defs: []ops.Definition{a, b, c}}
}
