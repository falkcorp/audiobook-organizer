// file: internal/metafetch/service_apply_merge_primary_test.go
// version: 1.0.0
// guid: ac096b2d-9e05-41eb-869a-939958c3ea85
// last-edited: 2026-09-11

package metafetch

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// mergePrimaryFixture builds a two-book MATCH-4 cluster: "big" has five
// files, "small" has one. Whatever else happens, "big" is the only correct
// primary. filesErrFor names the book whose GetBookFiles call fails.
type mergePrimaryFixture struct {
	mock *database.MockStore

	mu        sync.Mutex
	demotions [][2]string // {primaryID, duplicateID} per FlagMetadataHashDuplicate call
}

func newMergePrimaryFixture(filesErrFor string) *mergePrimaryFixture {
	f := &mergePrimaryFixture{}
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Hour)
	hash := "deadbeef"
	books := map[string]database.Book{
		"big":   {ID: "big", Title: "Big", MetadataSourceHash: &hash, CreatedAt: &later},
		"small": {ID: "small", Title: "Small", MetadataSourceHash: &hash, CreatedAt: &earlier},
	}
	fileCounts := map[string]int{"big": 5, "small": 1}

	f.mock = &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := books[id]
			if !ok {
				return nil, errors.New("not found")
			}
			return &b, nil
		},
		UpdateBookFunc: func(id string, book *database.Book) (*database.Book, error) {
			return book, nil
		},
		GetBooksByMetadataSourceHashFunc: func(h string) ([]database.Book, error) {
			return []database.Book{books["big"], books["small"]}, nil
		},
		GetBookFilesFunc: func(bookID string) ([]database.BookFile, error) {
			if bookID == filesErrFor {
				return nil, errors.New("pebble: read timed out")
			}
			out := make([]database.BookFile, fileCounts[bookID])
			for i := range out {
				out[i] = database.BookFile{ID: bookID + "-f", BookID: bookID}
			}
			return out, nil
		},
		FlagMetadataHashDuplicateFunc: func(primaryID, duplicateID string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.demotions = append(f.demotions, [2]string{primaryID, duplicateID})
			return nil
		},
	}
	return f
}

func (f *mergePrimaryFixture) demoted() [][2]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]string(nil), f.demotions...)
}

// TestCheckMetadataSourceHashDuplicates_ReadErrorAbortsElection is the SF-04
// regression. Before the fix a GetBookFiles error was scored as zero files,
// so "big" (five files, unreadable this instant) lost the election to
// "small" (one file) and was demoted to merged_into_book_id=small — a
// library mutation made on bad data, with nothing logged. The election must
// abort with an error and write nothing.
func TestCheckMetadataSourceHashDuplicates_ReadErrorAbortsElection(t *testing.T) {
	f := newMergePrimaryFixture("big")
	svc := NewService(f.mock)

	err := svc.checkMetadataSourceHashDuplicates("small", "deadbeef")
	if err == nil {
		t.Fatal("expected the election to abort on a GetBookFiles read error, got nil")
	}
	if !strings.Contains(err.Error(), "big") || !strings.Contains(err.Error(), "read timed out") {
		t.Errorf("error should name the unreadable book and wrap the store error, got: %v", err)
	}
	if got := f.demoted(); len(got) != 0 {
		t.Fatalf("no book may be demoted when a scoring read fails; got demotions %v", got)
	}
}

// TestCheckMetadataSourceHashDuplicates_SelfLookupErrorAbortsElection covers
// the sibling input: when the triggering book is not in the hash query result
// and its own GetBookByID fails, the election must abort rather than silently
// run without it.
func TestCheckMetadataSourceHashDuplicates_SelfLookupErrorAbortsElection(t *testing.T) {
	f := newMergePrimaryFixture("")
	f.mock.GetBookByIDFunc = func(id string) (*database.Book, error) {
		return nil, errors.New("pebble: read timed out")
	}
	svc := NewService(f.mock)

	err := svc.checkMetadataSourceHashDuplicates("third", "deadbeef")
	if err == nil {
		t.Fatal("expected the election to abort on a GetBookByID read error, got nil")
	}
	if got := f.demoted(); len(got) != 0 {
		t.Fatalf("no book may be demoted when the self lookup fails; got demotions %v", got)
	}
}

// TestCheckMetadataSourceHashDuplicates_MostFilesWins pins the ranking rule
// the fix must not change: with every read succeeding, the book with the
// most files is primary and the other is demoted to it.
func TestCheckMetadataSourceHashDuplicates_MostFilesWins(t *testing.T) {
	f := newMergePrimaryFixture("")
	svc := NewService(f.mock)

	if err := svc.checkMetadataSourceHashDuplicates("small", "deadbeef"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := f.demoted()
	if len(got) != 1 || got[0] != [2]string{"big", "small"} {
		t.Fatalf("expected exactly one demotion small->big, got %v", got)
	}
}

// TestApplyMetadataCandidate_MergeReadErrorDoesNotFailApply checks the
// caller's contract: the metadata apply has already been written when the
// duplicate check runs, so a read error there is logged and must neither
// fail the apply nor demote anything.
func TestApplyMetadataCandidate_MergeReadErrorDoesNotFailApply(t *testing.T) {
	f := newMergePrimaryFixture("big")
	svc := NewService(f.mock)

	resp, err := svc.ApplyMetadataCandidate("small", MetadataCandidate{
		Title:  "Small",
		Author: "Some Author",
		Source: "test",
		ASIN:   "B000TEST01",
	}, nil)
	if err != nil {
		t.Fatalf("apply must succeed when only the duplicate check's read fails, got: %v", err)
	}
	if resp == nil || resp.Book == nil {
		t.Fatal("apply returned no book")
	}
	if got := f.demoted(); len(got) != 0 {
		t.Fatalf("no book may be demoted when a scoring read fails; got demotions %v", got)
	}
}
