// file: internal/plugins/maintenance/export_t6g1_test.go
// version: 1.0.0
// guid: c41d7e0a-52b9-4f36-8a1e-9d6b0f3c7a25
// last-edited: 2026-11-13

package maintenance

import "github.com/falkcorp/mediacore/tasks"

// T6G1KindsForTest hands the 14 native definitions to the external drift test
// (package maintenance_test), which can import internal/books where this
// package's own tests cannot (books imports this package).
func T6G1KindsForTest() []tasks.Definition {
	return (&Plugin{}).t6g1Kinds(t6g1Deps{}, nil)
}
