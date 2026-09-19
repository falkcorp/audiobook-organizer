// file: internal/plugins/maintenance/regroup_recompute_once_test.go
// version: 1.1.0
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
	"github.com/falkcorp/audiobook-organizer/internal/merge"
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

// cancelAfterCombine runs the real combine, then cancels: the review handler's
// request context dying (client gone, proxy timeout) just after the merge
// committed.
type cancelAfterCombine struct {
	inner  bookCombiner
	cancel context.CancelFunc
}

func (c cancelAfterCombine) CombineBooks(ids []string, primaryID string, o *merge.CombineOverride) (*merge.CombineResult, error) {
	res, err := c.inner.CombineBooks(ids, primaryID, o)
	c.cancel()
	return res, err
}

// Once CombineBooks has committed, nothing can finish a partial numbering (a
// re-approve no-ops on <2 members; the group guard skips a numbered survivor),
// so a cancel after the combine must not stop it.
func TestApplyMultidisc_CancelAfterCombineStillNumbersEveryRow(t *testing.T) {
	store := newApplyTestStore(t)
	const n = 6
	paths := make([]string, n)
	tracks := make([]int, n)
	for i := range n {
		paths[i] = fmt.Sprintf("/lib/Saga/Saga_%d.mp3", i+1)
		tracks[i] = i + 1
	}
	ids, _ := seedNumberedBooks(t, store, paths)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	apply := ApplyMultidisc(store, cancelAfterCombine{inner: merge.NewService(store), cancel: cancel})
	item := multidiscItemWithNumbers(t, "/lib/Saga", ids, paths, make([]int, n), tracks)
	if err := apply(ctx, item); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ctx.Err() == nil {
		t.Fatal("test setup: ctx was not cancelled after the combine")
	}

	files, err := store.GetBookFiles(minID(ids...))
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != n {
		t.Fatalf("survivor has %d files, want %d", len(files), n)
	}
	for _, f := range files {
		if f.TrackNumber == 0 {
			t.Errorf("row %s left unnumbered after a post-combine cancel", f.FilePath)
		}
	}
}

// cancelOnTouch cancels ctx at the k-th liveness touch: a cancel arriving in
// the middle of the fragments track pass.
type cancelOnTouch struct {
	livenessReporter
	k      int64
	cancel context.CancelFunc
}

func (r *cancelOnTouch) TouchLiveness() {
	if r.touches.Add(1) == r.k {
		r.cancel()
	}
}

// A group is applied atomically; a cancel mid-track-pass must not leave the
// survivor half-renumbered (possibly with duplicate track numbers) while
// applyFragments goes on to retire the shells.
func TestFSRegroupTrackPass_CancelMidPassStillRenumbersWholeGroup(t *testing.T) {
	s := newRepairPebble(t)
	const n = 20
	bookID, _ := seedTrackBook(t, s, n, func(i int) string {
		return fmt.Sprintf("/lib/Chapters/Chapters - %02d/f.mp3", i+1)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rep := &cancelOnTouch{k: 3, cancel: cancel}
	a := &fsApplier{store: s, opID: "op-cancel", reporter: rep}

	if !a.renumberFragmentTracks(ctx, bookID, "/lib/Chapters") {
		t.Fatalf("track pass did not complete (errs=%d)", a.errs.Load())
	}
	if ctx.Err() == nil {
		t.Fatal("test setup: ctx was not cancelled mid-pass")
	}
	files, _ := s.GetBookFiles(bookID)
	seen := map[int]string{}
	for _, f := range files {
		if f.TrackNumber == 0 {
			t.Errorf("row %s left unnumbered by a mid-pass cancel", f.FilePath)
			continue
		}
		if prev, dup := seen[f.TrackNumber]; dup {
			t.Errorf("track %d on both %s and %s", f.TrackNumber, prev, f.FilePath)
		}
		seen[f.TrackNumber] = f.FilePath
	}
}
