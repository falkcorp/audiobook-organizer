// file: internal/plugins/dedup/kinds_chain.go
// version: 1.0.0
// guid: ca58ea3e-cc42-4a27-b1be-b532eb5a7b6e
// last-edited: 2026-11-18

package dedup

import "github.com/falkcorp/mediacore/tasks"

// ChainKindDeps is what the chain group's native kinds need from their owner (F5-04
// stub: the group's worker replaces this file).
type ChainKindDeps struct{}

func chainKinds(_ *Plugin, _ ChainKindDeps, _ func(tasks.Kind) *tasks.Schedule) []tasks.Definition {
	return nil
}
