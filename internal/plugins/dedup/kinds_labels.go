// file: internal/plugins/dedup/kinds_labels.go
// version: 1.0.0
// guid: 8f265d0b-a0d3-4ccc-8090-632ef8c21b5d
// last-edited: 2026-11-18

package dedup

import "github.com/falkcorp/mediacore/tasks"

// LabelKindDeps is what the labels group's native kinds need from their owner (F5-04
// stub: the group's worker replaces this file).
type LabelKindDeps struct{}

func labelKinds(_ *Plugin, _ LabelKindDeps, _ func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return nil
}
