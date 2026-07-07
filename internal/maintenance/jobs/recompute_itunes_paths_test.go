// file: internal/maintenance/jobs/recompute_itunes_paths_test.go
// version: 1.1.0
// guid: b7c8d9e0-f1a2-3456-bcde-789012345012
// last-edited: 2026-07-06

// Package jobs_test exercises the recompute-itunes-paths maintenance job.
// noopReporter and the blank jobs import are provided by fix_read_by_narrator_test.go.
package jobs_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func TestRecomputeItunesPathsJob_Registered(t *testing.T) {
	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	if j.ID() != "recompute-itunes-paths" {
		t.Fatalf("unexpected ID: %q", j.ID())
	}
	if j.Name() == "" {
		t.Fatal("Name() must not be empty")
	}
	if j.Description() == "" {
		t.Fatal("Description() must not be empty")
	}
	if j.Category() == "" {
		t.Fatal("Category() must not be empty")
	}
}

func TestRecomputeItunesPathsJob_DefaultParams(t *testing.T) {
	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	params := j.DefaultParams()
	if params == nil {
		t.Fatal("DefaultParams() must not be nil")
	}
}

func TestRecomputeItunesPathsJob_EmptyStore(t *testing.T) {
	// No book files → no-op, no error.
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{}, nil
		},
	}

	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	if err = j.Run(context.Background(), store, &noopReporter{}, true); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRecomputeItunesPathsJob_DryRunDoesNotUpdate(t *testing.T) {
	// File with a mismatched itunes_path; in dry-run no UpdateBookFile should be called.
	files := []database.BookFileCore{
		{ID: "bf-1", BookID: "book-1", FilePath: "/books/author/title/chapter.mp3", ITunesPath: "/old/path.mp3"},
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			updateCalled = true
			return nil
		},
	}

	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	if err = j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if updateCalled {
		t.Fatal("dry_run=true: UpdateBookFile must not be called")
	}
}

func TestRecomputeItunesPathsJob_SkipsAlreadyCorrect(t *testing.T) {
	// When computed path == stored path, UpdateBookFile must not be called.
	// Use an empty ITunesPath so ComputeITunesPath returns "" and both sides match.
	files := []database.BookFileCore{
		{ID: "bf-2", BookID: "book-2", FilePath: "/books/author/title/chapter.mp3", ITunesPath: ""},
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			updateCalled = true
			return nil
		},
	}

	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	if err = j.Run(context.Background(), store, &noopReporter{}, false); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if updateCalled {
		t.Fatal("itunes_path already correct: UpdateBookFile must not be called")
	}
}

func TestRecomputeItunesPathsJob_CancelRespected(t *testing.T) {
	files := make([]database.BookFileCore, 5)
	for i := range files {
		files[i] = database.BookFileCore{ID: "bf-cancel-" + string(rune('0'+i)), FilePath: "/books/b/c.mp3"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return files, nil
		},
	}

	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	runErr := j.Run(ctx, store, &noopReporter{}, false)
	// Must return context.Canceled or nil — must not panic or hang.
	if runErr != nil && runErr != context.Canceled {
		t.Fatalf("unexpected error: %v", runErr)
	}
}
