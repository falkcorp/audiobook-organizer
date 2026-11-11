// file: core/archtest/tasklint/testdata/src/github.com/falkcorp/mediacore/internal/legacyengine/engine.go
// version: 1.0.0
// guid: f3707d01-a66a-4204-8569-a349647b27b9
// last-edited: 2026-11-10

// Package legacyengine stands in for an app's pre-core definition form (books:
// HEAD's v2 registry.OperationDef), which only the legacy-defs flag makes
// tasklint read as a definition.
package legacyengine

type OperationDef struct {
	ID       string
	Run      func(id string) error
	Validate func(id string) error
}
