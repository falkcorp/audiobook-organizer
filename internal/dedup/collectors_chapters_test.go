// file: internal/dedup/collectors_chapters_test.go
// version: 1.0.0
// guid: 9a17c3e5-8f62-4b04-bd31-7e05c9a2f861
// last-edited: 2026-09-22

package dedup

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// fakeChapterStore serves canned tables and can fail for one specific book, so
// the "one unreadable candidate must not sink the rest" path is testable.
type fakeChapterStore struct {
	tables  map[string][]database.Chapter
	failFor map[string]bool
}

func (f fakeChapterStore) GetChaptersForBook(bookID string) ([]database.Chapter, error) {
	if f.failFor[bookID] {
		return nil, errors.New("pebble: read failed")
	}
	return f.tables[bookID], nil
}

// table builds a chapter table from (start, end) pairs.
func table(bounds ...[2]float64) []database.Chapter {
	out := make([]database.Chapter, 0, len(bounds))
	for i, b := range bounds {
		out = append(out, database.Chapter{ID: i, StartSec: b[0], EndSec: b[1]})
	}
	return out
}

var baseTable = table([2]float64{0, 100}, [2]float64{100, 250}, [2]float64{250, 400}, [2]float64{400, 610})

func TestCollectChapterStructure_ExactTableScoresTop(t *testing.T) {
	st := fakeChapterStore{tables: map[string][]database.Chapter{
		"a": baseTable,
		"b": baseTable,
	}}
	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1: %+v", len(sigs), sigs)
	}
	if sigs[0].Kind != unified.SigChapterStructure {
		t.Errorf("Kind = %q, want %q", sigs[0].Kind, unified.SigChapterStructure)
	}
	// An identical table is the strongest this signal ever gets: the top of
	// the owner's 0.85-0.93 range.
	if sigs[0].Confidence != 0.93 {
		t.Errorf("Confidence = %v, want 0.93 for an exact table", sigs[0].Confidence)
	}
}

func TestCollectChapterStructure_ConfidenceDegradesWithDrift(t *testing.T) {
	// Every boundary off by half the tolerance: confidence should land in the
	// middle of the range, not at either end.
	drifted := table([2]float64{0, 100.5}, [2]float64{100.5, 250.5}, [2]float64{250.5, 400.5}, [2]float64{400.5, 610.5})
	st := fakeChapterStore{tables: map[string][]database.Chapter{"a": baseTable, "b": drifted}}

	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1", len(sigs))
	}
	got := sigs[0].Confidence
	if got <= 0.85 || got >= 0.93 {
		t.Errorf("Confidence = %v, want strictly inside (0.85, 0.93) for half-tolerance drift", got)
	}
	if want := 0.89; got < want-0.001 || got > want+0.001 {
		t.Errorf("Confidence = %v, want ~%v (midpoint) for half-tolerance drift", got, want)
	}
}

func TestCollectChapterStructure_OutsideToleranceIsNotAMatch(t *testing.T) {
	// One boundary 3s out: a different recording, not a different encode.
	off := table([2]float64{0, 100}, [2]float64{100, 253}, [2]float64{253, 400}, [2]float64{400, 610})
	st := fakeChapterStore{tables: map[string][]database.Chapter{"a": baseTable, "b": off}}

	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 — partial agreement must earn nothing: %+v", len(sigs), sigs)
	}
}

func TestCollectChapterStructure_DifferentCountIsNotAMatch(t *testing.T) {
	short := table([2]float64{0, 100}, [2]float64{100, 250}, [2]float64{250, 400})
	st := fakeChapterStore{tables: map[string][]database.Chapter{"a": baseTable, "b": short}}

	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 for a different chapter count", len(sigs))
	}
}

// A two-chapter book is not distinctive: a great many books are one file with
// a couple of sections, and matching on that would fire constantly.
func TestCollectChapterStructure_TooFewChaptersIsNotEvidence(t *testing.T) {
	tiny := table([2]float64{0, 100}, [2]float64{100, 250})
	st := fakeChapterStore{tables: map[string][]database.Chapter{"a": tiny, "b": tiny}}

	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 for a 2-chapter table", len(sigs))
	}
}

// Absence is not an error -- the same contract the other collectors follow.
func TestCollectChapterStructure_NoChaptersIsNotAnError(t *testing.T) {
	st := fakeChapterStore{tables: map[string][]database.Chapter{"b": baseTable}}
	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("a book with no chapters must not be an error, got: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0", len(sigs))
	}
}

// One unreadable candidate must not sink the others.
func TestCollectChapterStructure_UnreadableCandidateIsSkipped(t *testing.T) {
	st := fakeChapterStore{
		tables:  map[string][]database.Chapter{"a": baseTable, "bad": baseTable, "good": baseTable},
		failFor: map[string]bool{"bad": true},
	}
	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"bad", "good"}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1 (the readable candidate)", len(sigs))
	}
}

// A book is not evidence for itself.
func TestCollectChapterStructure_SelfIsSkipped(t *testing.T) {
	st := fakeChapterStore{tables: map[string][]database.Chapter{"a": baseTable}}
	sigs, err := CollectChapterStructure(context.Background(), st, "a", []string{"a", ""}, DefaultChapterCollectorConfig())
	if err != nil {
		t.Fatalf("CollectChapterStructure: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("got %d signals, want 0 — a book must not match itself", len(sigs))
	}
}

// The reading the base table cannot be read at all IS an error: that is a store
// failure, not an absent artifact, and must stay visible.
func TestCollectChapterStructure_BaseReadFailureIsAnError(t *testing.T) {
	st := fakeChapterStore{
		tables:  map[string][]database.Chapter{"a": baseTable},
		failFor: map[string]bool{"a": true},
	}
	if _, err := CollectChapterStructure(context.Background(), st, "a", []string{"b"}, DefaultChapterCollectorConfig()); err == nil {
		t.Fatal("a store failure reading the base book returned nil error, want an error")
	}
}

// The collector must honour cancellation mid-sweep.
func TestCollectChapterStructure_RespectsContextCancellation(t *testing.T) {
	st := fakeChapterStore{tables: map[string][]database.Chapter{"a": baseTable, "b": baseTable}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CollectChapterStructure(ctx, st, "a", []string{"b"}, DefaultChapterCollectorConfig()); err == nil {
		t.Fatal("cancelled context returned nil error, want ctx.Err()")
	}
}
