// file: internal/plugins/deluge/kinds_shims_test.go
// version: 1.0.0
// guid: 9a05df3d-aa09-43da-bd2e-e29f83a8d82b
// last-edited: 2026-11-19

// Test-only shims: helpers the tests call that no production path does (the
// HEAD shell and the native kind both go through the *Via functions).

package deluge

import (
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/mediacore/tasks"
)

// linkRunOptions builds the RunItems options for both worker phases.
//
// A named function so a test runs the real options rather than a copy: an
// omitted Concurrency is clamped to 1 by run_items.go, which is how other
// full-library ops ended up sequential behind a worker-pool API.
func linkRunOptions(workers int, total int, tally *linkTally) registry.RunItemsOptions {
	return registry.RunItemsOptions{
		Concurrency:   linkWorkers(workers),
		ProgressTotal: total,
		Label:         linkCounterLabel(tally),
	}
}

// kindFor is the kind name rule R3 gives a HEAD def ID: "books." + the ID.
func kindFor(headID string) tasks.Kind { return tasks.Kind("books." + headID) }
