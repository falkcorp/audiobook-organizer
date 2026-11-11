// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/kernel/kernel.go
// version: 1.0.0
// guid: a755f1c3-87d3-44be-9f5c-e03771218130
// last-edited: 2026-11-10

// Package kernel registers the owner package's kinds.
package kernel

import (
	"github.com/falkcorp/mediacore/internal/tasklintcases/xpkg/owner"
	"github.com/falkcorp/mediacore/module"
)

func Register(r *module.Registrar) {
	r.Tasks(owner.Single)
	r.Tasks(owner.Kinds()...)
}
