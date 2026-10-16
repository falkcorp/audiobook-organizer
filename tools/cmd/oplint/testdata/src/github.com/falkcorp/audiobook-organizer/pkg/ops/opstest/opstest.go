// file: tools/cmd/oplint/testdata/src/github.com/falkcorp/audiobook-organizer/pkg/ops/opstest/opstest.go
// version: 1.0.0
// guid: 8145adc2-126a-44e2-83ac-337d77b3807c
// last-edited: 2026-10-16

// Package opstest stands in for pkg/ops/opstest, which oplint never reports:
// this unbundled def and empty Sequential reason are not findings.
package opstest

import "github.com/falkcorp/audiobook-organizer/pkg/ops"

var Probe = ops.Task("synthetic.opstest-probe", ops.TaskSpec[ops.NoParams, ops.NoResult]{})

var Seq = ops.Sequential("")
