// file: internal/organizer/organize_fetch_retired_test.go
// version: 1.0.1
// guid: bb2c34c1-71a9-4a36-9d22-29bdc760d624
// last-edited: 2026-11-17

package organizer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// failureRecordingLogger records Warn and Error lines on top of the Info ones.
type failureRecordingLogger struct {
	infoRecordingLogger
	fmu      sync.Mutex
	failures []string
}

func (l *failureRecordingLogger) Warn(format string, args ...any) {
	l.fmu.Lock()
	defer l.fmu.Unlock()
	l.failures = append(l.failures, fmt.Sprintf(format, args...))
}

func (l *failureRecordingLogger) Error(format string, args ...any) {
	l.fmu.Lock()
	defer l.fmu.Unlock()
	l.failures = append(l.failures, fmt.Sprintf(format, args...))
}

// Organize's fetch-first loop is unattended: a book the fetch refuses as
// retired is a counted skip. It does not stop the run, is not counted as
// enriched, and is not logged as a failure.
func TestPerformOrganize_FetchFirstCountsRetiredBooksAsSkips(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	config.Mutate(func(c *config.Config) {
		c.RootDir = t.TempDir()
		c.DatabasePath = "" // no auto-backup
	})

	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "The Long Halloween"}, nil
		},
		GetAllBooksCoreFunc: func(int, int) ([]database.BookCore, error) { return nil, nil },
	}
	svc := NewService(mockDB)
	var mu sync.Mutex
	var fetched []string
	svc.FetchMetadataForBook = func(_ context.Context, id string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		fetched = append(fetched, id)
		if id == "b2" {
			// The shape the server's wiring returns for metafetch.ErrBookRetired.
			return nil, fmt.Errorf("%w: %w", ErrFetchSkippedRetired, errors.New("metadata: book is retired"))
		}
		return nil, nil
	}

	log := &failureRecordingLogger{}
	_ = svc.PerformOrganize(context.Background(),
		&Request{BookIDs: []string{"b1", "b2", "b3"}, FetchMetadataFirst: true},
		logger.Logger(log))

	mu.Lock()
	defer mu.Unlock()
	if len(fetched) != 3 {
		t.Fatalf("fetch calls = %v, want all 3 books: a retired book must not stop the loop", fetched)
	}
	if !log.saw("Metadata enriched for 2 books, 1 retired books skipped") {
		t.Errorf("info lines %v, want the enriched count without the retired book and a retired skip count", log.infos)
	}
	log.fmu.Lock()
	defer log.fmu.Unlock()
	for _, line := range log.failures {
		// Unrelated warnings (the skipped auto-backup) are not this test's.
		if strings.Contains(line, "retired") || strings.Contains(line, "b2") {
			t.Errorf("a retired book was logged as a failure: %q", line)
		}
	}
}
