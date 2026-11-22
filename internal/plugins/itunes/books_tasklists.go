// file: internal/plugins/itunes/books_tasklists.go
// version: 0.1.0
// guid: e3a07b52-9c14-4d68-b2f5-18d6a4c0e793
// last-edited: 2026-11-22

package itunes

import (
	"fmt"

	"github.com/falkcorp/mediacore/tasks"
)

// BooksTaskInputs is everything BooksTaskLists builds the iTunes list from. The
// iTunes kind needs no store, writer or client (its core is a refusal, see
// kinds.go), so the schedule hook is the only input.
type BooksTaskInputs struct {
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every iTunes kind with its schedule. It is dark:
// nothing outside tests calls it until the flip's registration commit.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	if in.Schedules == nil {
		return nil, fmt.Errorf("itunes: BooksTaskLists: missing input(s): [Schedules]")
	}
	return (&Plugin{}).Kinds(KindDeps{}, in.Schedules), nil
}
