// file: internal/repairs/books_tasklists.go
// version: 0.1.0
// guid: c58f2e90-7b14-4d36-a9e1-0d4f83b6c725
// last-edited: 2026-11-22

package repairs

import (
	"context"
	"fmt"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// The books module's own dependency values for the Repairs lane's kinds (F5-18
// A6), dark: nothing outside tests calls BooksTaskLists until the flip's
// registration commit.
//
// What this file does not build, and why:
//   - The fixers. They are the maintenance plugin's and each captures that
//     plugin (its deps, not just the stores on the context), so the caller
//     hands them in (maintenance.Plugin.Repairs().List()). Whether the flip
//     keeps the plugin alive for them or converts them is the flip's decision.
//   - PlanRows. The kernel owns it (a run's plan_ref names the plan run whose
//     rows these are; KindDeps.PlanRows says so), so the caller passes the
//     kernel's reader.
//   - Scopes. It is books' atom table (internal/books/atoms.go), which this
//     package cannot import.
//   - BindStores. The FixerStores value is the maintenance package's own
//     interface over the host's accessors; books supplies the binding.

// WriterStores is every store the per-run Writer is built over: the single book
// write primitive, the history row, the book_file writer, the operation journal,
// the credit writer and the field-lock writer (writer.go, writer_files.go,
// writer_credits.go, writer_locks.go). A real store satisfies it.
type WriterStores interface {
	BookModifier
	HistoryRecorder
	BookFileWriter
	ChangeJournal
	CreditStore
	FieldStateStore
}

// Writer source, change type and batch prefix of a Repairs apply, as HEAD's
// repairs.apply op stamps them (maintenance/repairs_ops.go): the fixer's id,
// "bulk_update", "repairs-".
const (
	writerChangeType  = "bulk_update"
	writerBatchPrefix = "repairs-"
)

// BooksTaskInputs is everything BooksTaskLists builds the Repairs kinds from.
type BooksTaskInputs struct {
	// Fixers are the fixers to declare a kind for, in the lane's order.
	Fixers []Fixer
	// Guard, Tags and Series are the framework guards' readers (KindDeps). Tags
	// and Series are optional: the guards skip the tag check and the series-name
	// check when they are nil, as a caller with no such store does.
	Guard  GuardReader
	Tags   GuardTagReader
	Series SeriesNamer
	// Stores is what each run's Writer writes through.
	Stores WriterStores
	// TagWriter and UserState are the Writer's optional capabilities (the
	// tag-franchise fixer's book_tag rows, the Audible read-status fixer's
	// listening state). Nil leaves the Writer without them: a fixer that needs
	// one fails its write rather than going around it.
	TagWriter TagStore
	UserState UserStateStore
	// PlanRows reads the planned rows of an apply run's plan (the kernel's).
	PlanRows PlannedRows
	// BindStores puts the fixer stores on a context (KindDeps.BindStores).
	BindStores func(ctx context.Context) context.Context
	// Scopes resolves a HEAD permission atom to its native scopes.
	Scopes func(atom auth.Permission) ([]rbac.Permission, bool)
	// StandDownKey is the concurrency key of the library scan kinds, which an
	// apply stands down while it writes.
	StandDownKey tasks.Key
	// Concurrency is the apply and guard worker count; 0 means the default.
	Concurrency int
}

// BooksTaskLists returns one definition per fixer, over the books module's own
// dependency values. A missing required input is an error naming it: the kinds
// would otherwise register and refuse at their first run.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for _, c := range []struct {
		name string
		set  bool
	}{
		{"Fixers", len(in.Fixers) > 0}, {"Guard", in.Guard != nil}, {"Stores", in.Stores != nil},
		{"PlanRows", in.PlanRows != nil}, {"BindStores", in.BindStores != nil},
		{"Scopes", in.Scopes != nil}, {"StandDownKey", in.StandDownKey != ""},
	} {
		if !c.set {
			missing = append(missing, c.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("repairs: BooksTaskLists: missing input(s): %v", missing)
	}
	defs := Kinds(in.Fixers, KindDeps{
		Guard:        in.Guard,
		Tags:         in.Tags,
		Series:       in.Series,
		NewWriter:    newBooksWriter(in),
		PlanRows:     in.PlanRows,
		BindStores:   in.BindStores,
		Scopes:       in.Scopes,
		StandDownKey: in.StandDownKey,
		Concurrency:  in.Concurrency,
	})
	// A kind whose Start gate does not resolve (an atom books' table does not know)
	// is not registrable: report it here rather than registering a kind nobody
	// can start.
	for i, d := range defs {
		if probs := tasks.Problems(d); len(probs) > 0 {
			return nil, fmt.Errorf("repairs: fixer %s: %v", in.Fixers[i].ID(), probs)
		}
	}
	return defs, nil
}

// newBooksWriter builds the run's Writer for one fixer over in.Stores, journaled
// under the run's id (so the run's revert undoes the apply), the way HEAD's
// repairs.apply builds its own.
func newBooksWriter(in BooksTaskInputs) func(ctx context.Context, run tasks.RunID, f Fixer) (*Writer, error) {
	return func(_ context.Context, run tasks.RunID, f Fixer) (*Writer, error) {
		w := NewWriter(in.Stores, in.Stores, f.ID(), writerChangeType, writerBatchPrefix).
			WithJournal(in.Stores, in.Stores, string(run)).
			WithCredits(in.Stores).
			WithFieldStates(in.Stores)
		if in.TagWriter != nil {
			w.WithTags(in.TagWriter)
		}
		if in.UserState != nil {
			w.WithUserState(in.UserState)
		}
		return w, nil
	}
}
