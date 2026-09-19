// file: internal/plugins/maintenance/regroup_recompute_once_test.go
// version: 1.0.0
// guid: 1459a1bf-2291-48b5-9eaa-be04214b421c
// last-edited: 2026-09-19

package maintenance

// The regroup track passes rewrite many rows of ONE survivor. Per-row
// UpdateBookFile recomputed the survivor's aggregates after every row, each
// recompute re-reading all of its rows: O(n^2), the defect that got
// duration-reextract killed as stuck on 2026-09-19. These run on a real
// PebbleStore because the per-row recompute lives inside the store, where a
// mock cannot see it.

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
)

func seedTrackBook(t *testing.T, s *database.PebbleStore, n int, path func(i int) string) (string, []string) {
	t.Helper()
	book, err := s.CreateBook(&database.Book{Title: "Chapters", FilePath: "/lib/Chapters"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	paths := make([]string, n)
	for i := range n {
		paths[i] = path(i)
		if err := s.CreateBookFile(&database.BookFile{
			BookID: book.ID, FilePath: paths[i], FileSize: 1_000_000, Duration: 60,
		}); err != nil {
			t.Fatalf("CreateBookFile: %v", err)
		}
	}
	return book.ID, paths
}

func TestApplyDiscTrackNumbers_RecomputesSurvivorOnce(t *testing.T) {
	s := newRepairPebble(t)
	const n = 20
	bookID, paths := seedTrackBook(t, s, n, func(i int) string {
		return fmt.Sprintf("/lib/Chapters/part%02d.mp3", i)
	})
	p := regroupPayload{Files: paths, DiscNumbers: make([]int, n), TrackNumbers: make([]int, n)}
	for i := range n {
		p.DiscNumbers[i] = 1
		p.TrackNumbers[i] = i + 1
	}

	logs := aggtest.Capture(t)
	updated, err := applyDiscTrackNumbers(context.Background(), s, bookID, p)
	if err != nil {
		t.Fatalf("applyDiscTrackNumbers: %v", err)
	}
	if updated != n {
		t.Fatalf("updated %d rows, want %d", updated, n)
	}
	if got := aggtest.CountInvocations(logs(), bookID); got != 1 {
		t.Errorf("survivor aggregates recomputed %d times for %d rows, want 1", got, n)
	}
	files, _ := s.GetBookFiles(bookID)
	for _, f := range files {
		if f.DiscNumber != 1 || f.TrackNumber == 0 {
			t.Fatalf("row %s disc/track = %d/%d, want 1/>0", f.FilePath, f.DiscNumber, f.TrackNumber)
		}
	}
}

func TestFSRegroupTrackPass_RecomputesSurvivorOnce(t *testing.T) {
	s := newRepairPebble(t)
	const n = 20
	bookID, _ := seedTrackBook(t, s, n, func(i int) string {
		return fmt.Sprintf("/lib/Chapters/Chapters - %02d/f.mp3", i+1)
	})

	rep := &livenessReporter{}
	a := &fsApplier{store: s, opID: "op-test", reporter: rep}
	logs := aggtest.Capture(t)
	recomputed := a.renumberFragmentTracks(context.Background(), bookID, "/lib/Chapters")
	if !recomputed {
		t.Fatalf("track pass reported no recompute (errs=%d)", a.errs.Load())
	}
	if got := aggtest.CountInvocations(logs(), bookID); got != 1 {
		t.Errorf("survivor aggregates recomputed %d times for %d rows, want 1", got, n)
	}
	if got := rep.touches.Load(); got != n {
		t.Errorf("liveness touched %d times, want once per row (%d)", got, n)
	}
	files, _ := s.GetBookFiles(bookID)
	for _, f := range files {
		if f.TrackNumber == 0 {
			t.Fatalf("row %s track not set", f.FilePath)
		}
	}
	changes, err := s.GetOperationChanges("op-test")
	if err != nil {
		t.Fatalf("GetOperationChanges: %v", err)
	}
	if len(changes) != n {
		t.Errorf("journaled %d track changes, want %d", len(changes), n)
	}
}
