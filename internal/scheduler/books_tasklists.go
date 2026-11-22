// file: internal/scheduler/books_tasklists.go
// version: 0.1.0
// guid: a2d85e17-4c06-4b93-8f1a-3e9b70d6c458
// last-edited: 2026-11-22

package scheduler

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance/resources"
	"github.com/falkcorp/audiobook-organizer/internal/metabatch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own t7Deps for the scheduler's kinds (F5-18 A6), dark:
// nothing outside tests calls BooksTaskLists until the flip's registration
// commit.
//
// The one kind, scheduler.metadata-upgrade, runs a sweep inside
// metabatch.MetadataUpgradeService that writes through its own store under the
// bulk-apply gate, not through a tasks.Writer. The gate therefore stamps the
// whole sweep as ONE write over the kind's declared resources (a write-ahead
// intent for the pass, and a refusal for a Preview or a stopped run before the
// sweep starts), the way the group-4 and group-5 writers stamp their batched
// passes. It does not fence the sweep's individual rows.

// MetadataUpgrader is the upgrade sweep: metabatch.MetadataUpgradeService.
type MetadataUpgrader interface {
	RunUpgrade(ctx context.Context, limit int, progress operations.ProgressReporter) (*metabatch.UpgradeResult, error)
}

// BooksTaskInputs is everything BooksTaskLists builds the scheduler's list from.
type BooksTaskInputs struct {
	// Upgrader is the metadata upgrade sweep. It is required: HEAD refused a run
	// with no metadata fetch service wired, and a nil Upgrader is that state.
	Upgrader MetadataUpgrader
	// StandDownKey is the concurrency key the library scan kinds run under, which
	// a live upgrade holds with rc.StandDown. Empty refuses a live write.
	StandDownKey tasks.Key
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every scheduler kind with its schedule, over the books
// module's own dependency values.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for name, set := range map[string]bool{
		"Upgrader": in.Upgrader != nil, "StandDownKey": in.StandDownKey != "", "Schedules": in.Schedules != nil,
	} {
		if !set {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("scheduler: BooksTaskLists: missing input(s): %v", missing)
	}
	deps, err := newBooksT7Deps(in)
	if err != nil {
		return nil, err
	}
	// The kind's declaration reads nothing from the registrar (metadataUpgradeDef
	// is a literal), so a zero one is the receiver.
	return (&ExtraOpsRegistrar{}).t7Kinds(deps, in.Schedules), nil
}

// booksT7Row is the row of the sweep's one gated call: every resource the kind
// declares (extra_ops.go: books, authors, series, metadata_cache, operations).
func booksT7Row() writer.Rows {
	return writer.Rows{"RunUpgrade": resources.Union([]ops.Resource{
		ops.ResBooks, ops.ResAuthors, ops.ResSeries, ops.ResMetadataCache, ops.ResOperations,
	})}
}

func newBooksT7Deps(in BooksTaskInputs) (t7Deps, error) {
	rows := booksT7Row()
	up := in.Upgrader
	return t7Deps{
		Store:        booksT7Store{},
		StandDownKey: in.StandDownKey,
		NewWriter: func(w tasks.Writer) t7Writer {
			return &booksT7Writer{up: up, gate: writer.ForRun(w, rows)}
		},
	}, nil
}

// booksT7Store is the read side: the upgrade can run once an Upgrader is wired,
// and BooksTaskLists refuses to build without one.
type booksT7Store struct{}

func (booksT7Store) Ready() error { return nil }

// booksT7Writer is one run's gated write. A ReadOnly run has no writer (w is
// nil): the sweep then refuses instead of dereferencing nil.
type booksT7Writer struct {
	up   MetadataUpgrader
	gate *writer.Gate
}

func (w *booksT7Writer) RunUpgrade(ctx context.Context, limit int, progress operations.ProgressReporter) (*metabatch.UpgradeResult, error) {
	return writer.Value(ctx, w.gate, "RunUpgrade", writer.Ref(string(ops.ResBooks), ""), func() (*metabatch.UpgradeResult, error) {
		return w.up.RunUpgrade(ctx, limit, progress)
	})
}
