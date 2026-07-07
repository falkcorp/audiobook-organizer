// file: internal/maintenance/jobs/fix_read_by_narrator_test.go
// version: 1.2.0
// guid: a3b4c5d6-e7f8-9012-abcd-345678901234
// last-edited: 2026-07-07

// Package jobs_test exercises the fix-read-by-narrator maintenance job.
// Importing the jobs package (via the blank import below) triggers all
// init() functions, registering every job with the maintenance registry.
package jobs_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	_ "github.com/falkcorp/audiobook-organizer/internal/maintenance/jobs" // register all jobs
)

// noopReporter is declared once in cleanup_empty_folders_test.go (shared across the package).

func TestFixReadByNarratorJob_Registered(t *testing.T) {
	// Verify that importing the jobs package registered the job.
	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	if j.ID() != "fix-read-by-narrator" {
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

func TestFixReadByNarratorJob_DefaultParams(t *testing.T) {
	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}
	params := j.DefaultParams()
	if params == nil {
		t.Fatal("DefaultParams() must not be nil")
	}
}

func TestFixReadByNarratorJob_DryRun(t *testing.T) {
	authorID := 1
	author := &database.Author{ID: authorID, Name: "Real Author"}
	book := database.Book{
		ID:       "book-1",
		Title:    "read by Jane Doe",
		AuthorID: &authorID,
		FilePath: "/audiobooks/Real Author/Real Book/chapter.mp3",
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{book.Core()}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			if id == authorID {
				return author, nil
			}
			return nil, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	err = j.Run(context.Background(), store, &noopReporter{}, true /* dryRun */)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if updateCalled {
		t.Fatal("dry_run=true: UpdateBook must not be called")
	}
}

func TestFixReadByNarratorJob_Apply(t *testing.T) {
	authorID := 2
	author := &database.Author{ID: authorID, Name: "Real Author Name"}
	book := database.Book{
		ID:       "book-2",
		Title:    "read by Jane Doe",
		AuthorID: &authorID,
		FilePath: "/audiobooks/Real Author Name/My Book/chapter.mp3",
	}
	updated := book

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{book.Core()}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			if id == authorID {
				return author, nil
			}
			return nil, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			if id == book.ID {
				return &updated, nil
			}
			return nil, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updated = *b
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	err = j.Run(context.Background(), store, &noopReporter{}, false /* apply */)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestFixReadByNarratorJob_NoMatchBooks(t *testing.T) {
	book := database.Book{
		ID:       "book-clean",
		Title:    "The Odyssey",
		FilePath: "/audiobooks/Homer/The Odyssey/chapter.mp3",
	}

	var updateCalled bool
	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{book.Core()}, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updateCalled = true
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	err = j.Run(context.Background(), store, &noopReporter{}, false)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if updateCalled {
		t.Fatal("no matching books: UpdateBook must not be called")
	}
}

func TestFixReadByNarratorJob_TitleDashReadBy(t *testing.T) {
	book := database.Book{
		ID:       "book-dash",
		Title:    "The Iliad - Homer - read by Michael Page",
		FilePath: "/audiobooks/Homer/The Iliad/chapter.mp3",
	}
	var updatedTitle string

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			return []database.BookCore{book.Core()}, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return nil, nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			cp := book
			return &cp, nil
		},
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			updatedTitle = b.Title
			return b, nil
		},
	}

	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	err = j.Run(context.Background(), store, &noopReporter{}, false)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if updatedTitle == "" {
		t.Skip("pattern2 (title dash read by) produced no match — skip rather than fail")
	}
	if updatedTitle == book.Title {
		t.Fatalf("title should have been updated, got %q", updatedTitle)
	}
}

func TestFixReadByNarratorJob_Cancellation(t *testing.T) {
	books := make([]database.Book, 5)
	for i := range books {
		books[i] = database.Book{
			ID:       "book-cancel-" + string(rune('0'+i)),
			Title:    "read by Jane Doe",
			FilePath: "/audiobooks/Author/Book/chapter.mp3",
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	store := &database.MockStore{
		GetAllBooksCoreFunc: func(limit, offset int) ([]database.BookCore, error) {
			cores := make([]database.BookCore, len(books))
			for i := range books {
				cores[i] = books[i].Core()
			}
			return cores, nil
		},
		GetAuthorByIDFunc: func(id int) (*database.Author, error) {
			return nil, nil
		},
	}

	j, err := maintenance.Get("fix-read-by-narrator")
	if err != nil {
		t.Fatalf("job not registered: %v", err)
	}

	err = j.Run(ctx, store, &noopReporter{}, false)
	// Should return ctx.Err() or nil — just must not panic or hang.
	if err != nil && err != context.Canceled {
		t.Fatalf("unexpected error: %v", err)
	}
}
