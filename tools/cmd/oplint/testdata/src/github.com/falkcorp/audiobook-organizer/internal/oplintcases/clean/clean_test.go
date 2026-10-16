// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/internal/oplintcases/clean/clean_test.go
// version: 1.0.0
// guid: 262cd051-9412-44a0-a9ce-d84f0b366b2a
// last-edited: 2026-10-16

package clean

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

// Test files are never reported: an unbundled def, an empty Sequential
// reason and a Common without Help here are not findings.
var testOnly = ops.Task("synthetic.test-only", ops.TaskSpec[ops.NoParams, ops.NoResult]{
	Common: ops.Common{Title: "x"}})

var seq = ops.Sequential("")
