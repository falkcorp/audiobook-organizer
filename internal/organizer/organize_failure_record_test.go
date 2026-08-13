// file: internal/organizer/organize_failure_record_test.go
// version: 1.0.0
// guid: 6f13c8b0-4a72-49de-85c1-2b90e7d43a1f
// last-edited: 2026-08-13

package organizer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// PerformOrganize has two places that increment stats.Failed. Only one of them
// used to write an organize_failed OperationChange.
//
// The consequence is not a missing log line — CreateOrganizedVersion logs its
// own error. It is that the operation's SUMMARY and the operation's CHANGE LOG
// disagree, and nothing says so. Reconciling "the op reports N failed" against
// the organize_failed rows silently returns fewer than N, and the difference
// reads as books that were fine rather than failures nobody recorded.
//
// That matters here specifically: an earlier survey of production organize
// failures could not reproduce its own headline figure of 3,194, and the
// leading explanation was that the number came from a stats.Failed which counts
// more than the log lines do. This is one concrete way those two can drift.

// failureRecorder collects the OperationChange rows a run writes.
type failureRecorder struct {
	mu      sync.Mutex
	changes []database.OperationChange
}

func (r *failureRecorder) record(c *database.OperationChange) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changes = append(r.changes, *c)
	return nil
}

func (r *failureRecorder) ofType(t string) []database.OperationChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []database.OperationChange
	for _, c := range r.changes {
		if c.ChangeType == t {
			out = append(out, c)
		}
	}
	return out
}

// TestOrganizeFailure_CreateVersionFailureIsRecorded drives a real
// PerformOrganize whose CreateOrganizedVersion cannot succeed, and asserts the
// failure reaches the operation's change log rather than only the counter.
func TestOrganizeFailure_CreateVersionFailureIsRecorded(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "import")
	rootDir := filepath.Join(tmpDir, "library")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir import: %v", err)
	}
	srcPath := filepath.Join(srcDir, "foundation.m4b")
	if err := os.WriteFile(srcPath, []byte("audio bytes"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	// PerformOrganize reads RootDir and the naming patterns from the process
	// config. Restore them so this test cannot leak into its neighbours.
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	config.AppConfig.RootDir = rootDir
	config.AppConfig.FolderNamingPattern = "{author}"
	config.AppConfig.FileNamingPattern = "{title}"
	config.AppConfig.OrganizationStrategy = "copy"

	const bookID = "book-createfail"
	const opID = "op-createfail"
	createErr := errors.New("pebble: batch commit failed")

	rec := &failureRecorder{}
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id != bookID {
				return nil, nil
			}
			return &database.Book{
				ID:       bookID,
				Title:    "Foundation",
				FilePath: srcPath,
				Author:   &database.Author{Name: "Asimov"},
			}, nil
		},
		// FilterBooksNeedingOrganization refuses any book outside RootDir that
		// has no active book_files — it cannot know which files to copy. Without
		// this the book is filtered out before organize runs and the test
		// passes vacuously against a run that did nothing.
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{{BookID: bookID, FilePath: srcPath}}, nil
		},
		// The organized-version row cannot be written. This is the branch that
		// used to bump the counter and jump straight to the progress label.
		CreateBookFunc: func(*database.Book) (*database.Book, error) {
			return nil, createErr
		},
		CreateOperationChangeFunc: rec.record,
	}

	svc := NewService(mockDB)
	// Set by the server package in production; nil here would panic inside
	// CreateOrganizedVersion before the failure under test could happen.
	svc.ApplyOrganizedFileMetadata = func(*database.Book, string) {}

	err := svc.PerformOrganize(
		context.Background(),
		&Request{OperationID: opID, BookIDs: []string{bookID}},
		logger.New("test"),
	)
	// One book, and it failed, so the run is a total failure and reports one.
	// The assertion under test is the change record, not this error.
	if err == nil {
		t.Log("PerformOrganize returned nil; the outcome contract is covered by organize_outcome_test.go")
	}

	// Prove the run actually REACHED the book before asserting anything about
	// what it recorded. The first draft of this test mocked no book_files, so
	// FilterBooksNeedingOrganization dropped the book, PerformOrganize
	// processed zero books, and "no organize_failed change was written" was
	// true for a reason that had nothing to do with the defect. A missing-row
	// assertion is only evidence if the row had a chance to exist.
	summaries := rec.ofType("organize_summary")
	if len(summaries) != 1 {
		t.Fatalf("expected one organize_summary row, got %d", len(summaries))
	}
	if !strings.Contains(summaries[0].NewValue, "failed:1") ||
		!strings.Contains(summaries[0].NewValue, "total:1") {
		t.Fatalf("the run did not reach the book — summary was %q, want failed:1 and total:1",
			summaries[0].NewValue)
	}

	failures := rec.ofType("organize_failed")
	if len(failures) != 1 {
		t.Fatalf("expected exactly 1 organize_failed change for the failed book, got %d (%+v)",
			len(failures), rec.changes)
	}
	if failures[0].BookID != bookID {
		t.Errorf("organize_failed change names book %q, want %q", failures[0].BookID, bookID)
	}
	if failures[0].OperationID != opID {
		t.Errorf("organize_failed change names operation %q, want %q", failures[0].OperationID, opID)
	}
	// The recorded value must carry WHY. A change row that says only "it
	// failed" leaves the reconciliation possible but the diagnosis impossible,
	// which is half the point of writing the row at all.
	if failures[0].NewValue == "" {
		t.Error("organize_failed change recorded no reason")
	}
}
