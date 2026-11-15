// file: internal/plugins/maintenance/kinds_resources_test.go
// version: 1.0.0
// guid: bcdb3688-95b2-48e0-8c53-bb880996284f
// last-edited: 2026-11-15

package maintenance

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// t6RowsOf is the sorted union of the shared table's rows for the named writer
// methods: what a kind that can call exactly those methods must declare for them.
// A test spells the methods a kind can call, never the resources they write, so
// the table is the one place a method's resources are stated. A method with no row
// panics: it is a typo or a method the table was never given.
func t6RowsOf(methods ...string) []ops.Resource {
	rows := make([][]ops.Resource, 0, len(methods))
	for _, m := range methods {
		row, ok := t6WriterResources[m]
		if !ok {
			panic(fmt.Sprintf("t6RowsOf: %q has no row in t6WriterResources", m))
		}
		rows = append(rows, row)
	}
	return t6RowsUnion(rows...)
}
