// file: internal/maintenance/jobs/recompute_itunes_paths_test.go
// version: 1.2.1
// guid: b7c8d9e0-f1a2-3456-bcde-789012345012
// last-edited: 2026-09-12

// Package jobs_test exercises the recompute-itunes-paths maintenance job.
// noopReporter and the blank jobs import are provided by fix_read_by_narrator_test.go.
package jobs_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// recordingReporter keeps each log line, prefixed with its level.
type recordingReporter struct{ logs []string }

func (r *recordingReporter) SetTotal(int) {}
func (r *recordingReporter) Increment()   {}
func (r *recordingReporter) Log(level, msg string, _ *string) {
	r.logs = append(r.logs, level+": "+msg)
}

// useITunesMapping maps local /lib to C:/Music for the length of one test.
func useITunesMapping(t *testing.T) {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.ITunes.PathMappings = []config.ITunesPathMap{{From: "C:/Music", To: "/lib"}}
}

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

// A row that no mapping covers keeps its stored iTunes path and is listed;
// a mapped row in the same run is still rewritten. /lib2 is a sibling of the
// mapped root /lib, which the boundary match (#3338) no longer maps.
func TestRecomputeItunesPathsJob_KeepsStoredPathNoMappingCovers(t *testing.T) {
	useITunesMapping(t)
	sibling := database.BookFile{ID: "bf-sib", BookID: "book-sib", FilePath: "/lib2/a/b.m4b",
		ITunesPath: "file://localhost/C:/Music2/a/b.m4b"}
	mapped := database.BookFile{ID: "bf-map", BookID: "book-map", FilePath: "/lib/a/c.m4b",
		ITunesPath: "file://localhost/C:/old/c.m4b"}
	rows := map[string][]database.BookFile{"book-sib": {sibling}, "book-map": {mapped}}

	var writes []string
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) {
			return []database.BookFileCore{
				{ID: sibling.ID, BookID: sibling.BookID, FilePath: sibling.FilePath, ITunesPath: sibling.ITunesPath},
				{ID: mapped.ID, BookID: mapped.BookID, FilePath: mapped.FilePath, ITunesPath: mapped.ITunesPath},
			}, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			return append([]database.BookFile(nil), rows[bookID]...), nil
		},
		UpdateBookFileFunc: func(id string, file *database.BookFile) error {
			writes = append(writes, id+" -> "+file.ITunesPath)
			return nil
		},
	}

	j, err := maintenance.Get("recompute-itunes-paths")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	rep := &recordingReporter{}
	if err := j.Run(context.Background(), store, rep, false); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	want := []string{"bf-map -> file://localhost/C:/Music/a/c.m4b"}
	if !slices.Equal(writes, want) {
		t.Fatalf("writes = %q, want %q: a row no mapping covers must keep its stored path", writes, want)
	}
	logs := strings.Join(rep.logs, "\n")
	if !strings.Contains(logs, "warn: book_file bf-sib") {
		t.Errorf("the kept row must be listed as a warning; logs:\n%s", logs)
	}
	if !strings.Contains(logs, "updated 1 book_file rows; kept 1 stored iTunes paths") {
		t.Errorf("the summary must count updated and kept rows; logs:\n%s", logs)
	}
}

func TestRecomputeItunesPathsJob_DryRunDoesNotUpdate(t *testing.T) {
	// A file whose computed iTunes path differs from the stored one: a real
	// change, so dry-run must not call UpdateBookFile. The mapping has to
	// cover FilePath; a row no mapping covers is kept and never reaches the
	// dry-run branch at all.
	useITunesMapping(t)
	files := []database.BookFileCore{
		{ID: "bf-1", BookID: "book-1", FilePath: "/lib/author/title/chapter.mp3", ITunesPath: "/old/path.mp3"},
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
