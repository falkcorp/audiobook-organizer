// file: internal/books/tasklists.go
// version: 0.1.0
// guid: 7d3a9e52-1c64-4b08-a5f7-92e0d4b6c183
// last-edited: 2026-11-22

package books

import (
	"fmt"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
)

// taskListInputs is everything bookTaskLists builds the module's task lists
// from: the books services, each as the narrow interface its package declares
// (never database.Store). Schedules is books' one schedule hook (NewSchedules);
// it is copied into every package's inputs so the lists cannot disagree on it.
type taskListInputs struct {
	Schedules   func(tasks.Kind) *tasks.Schedule
	Maintenance maintenance.BooksTaskInputs
}

// bookTaskLists is every task definition the books module registers through
// Registrar.Tasks at the F5-18 flip, built over in. It is NOT called by
// Module(): until the flip's registration commit nothing reaches it, so no
// native kind is registered by it (F5-18 A6, dark).
//
// Left out on purpose: the 14 group-6 maintenance kinds. They hold a
// library-scan stand-down for an apply's whole length and must not register
// before core has Hold.Renew. TODO-F5-11-HOLD-RENEW (slice F5-18e):
// once that merges, append maintenance.BooksHeldTaskLists here and delete
// heldKinds from the registration test.
func bookTaskLists(in taskListInputs) ([]tasks.Definition, error) {
	var out []tasks.Definition

	mi := in.Maintenance
	mi.Schedules = in.Schedules
	m, err := maintenance.BooksTaskLists(mi)
	if err != nil {
		return nil, fmt.Errorf("books task lists: %w", err)
	}
	out = append(out, m...)

	return out, nil
}
