// file: internal/maintenance/jobs/fix_library_states.go
// version: 1.2.0
// guid: a1000008-0000-0000-0000-000000000008
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"os"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"log/slog")

func init() { maintenance.Register(&fixLibraryStatesJob{}) }

type fixLibraryStatesJob struct{}

func (j *fixLibraryStatesJob) ID() string       { return "fix-library-states" }
func (j *fixLibraryStatesJob) Name() string     { return "Fix Library States" }
func (j *fixLibraryStatesJob) Category() string { return "library" }
func (j *fixLibraryStatesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *fixLibraryStatesJob) Description() string {
	return "Reconcile library_state field based on filesystem presence"
}
func (j *fixLibraryStatesJob) CanResume() bool { return false }
func (j *fixLibraryStatesJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	reporter.SetTotal(len(books))
	fixed := 0
	for i := range books {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		reporter.Increment()
		book := &books[i]
		_, statErr := os.Stat(book.FilePath)
		wantState := "present"
		if os.IsNotExist(statErr) {
			wantState = "missing"
		}
		cur := ""
		if book.LibraryState != nil {
			cur = *book.LibraryState
		}
		if cur == wantState {
			continue
		}
		if !dryRun {
			// Hydrate before writeback — book is Core (slim); writing it
			// straight through UpdateBook would wipe Author/Series.
			full, herr := store.GetBookByID(book.ID)
			if herr != nil || full == nil {
				slog.Error("fix-library-states hydrate failed", "id", book.ID, "err", herr)
				continue
			}
			full.LibraryState = &wantState
			if _, uerr := store.UpdateBook(full.ID, full); uerr != nil {
				msg := uerr.Error()
				slog.Error("fix-library-states UpdateBook failed", "details", msg)
			} else {
				fixed++
			}
		} else {
			fixed++
		}
	}
	_ = fixed
	slog.Info("fix-library-states complete")
	return nil
}
