// file: internal/plugins/dedup/kinds_index.go
// version: 1.0.0
// guid: 7add27d1-edca-4f7e-be5d-bc6af26d1ab8
// last-edited: 2026-11-18

package dedup

import "github.com/falkcorp/mediacore/tasks"

// IndexKindDeps is what the index group's native kinds need from their owner (F5-04
// stub: the group's worker replaces this file).
type IndexKindDeps struct{}

func indexKinds(_ *Plugin, _ IndexKindDeps, _ func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return nil
}
