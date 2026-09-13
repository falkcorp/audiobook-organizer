// file: internal/scanner/ai_parse_giveup_test.go
// version: 1.0.0
// guid: 9a4d2e7b-1c58-4b36-8f0e-5b7c3a9d1e62
// last-edited: 2026-09-13

package scanner

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// countingPoisonParser is poisonParser plus a count of calls that included a
// given filename, so a test can say "0 calls for THAT book".
type countingPoisonParser struct {
	poisonParser
	mu        sync.Mutex
	callsWith map[string]int
}

func (p *countingPoisonParser) ParseBatch(ctx context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	p.mu.Lock()
	for _, f := range filenames {
		p.callsWith[f]++
	}
	p.mu.Unlock()
	return p.poisonParser.ParseBatch(ctx, filenames)
}

func withGiveUpStore(t *testing.T) {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	prev := getStore()
	SetStore(store)
	t.Cleanup(func() { SetStore(prev); cleanup() })
}

func runPoisonedPhase(t *testing.T, paths []string, poison string) (AIPhaseSummary, *countingPoisonParser, []Book) {
	t.Helper()
	books := make([]Book, len(paths))
	cands := make([]int, len(paths))
	for i, p := range paths {
		books[i] = Book{FilePath: p}
		cands[i] = i
	}
	p := &countingPoisonParser{poisonParser: poisonParser{poison: poison}, callsWith: map[string]int{}}
	s := runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), noopSave)
	return s, p, books
}

// A poisoned file is asked about in exactly maxAIParseSingleFileFailures runs,
// then skipped: on the run after, the model sees it in ZERO calls, while the
// books around it keep being parsed.
func TestAIParseGiveUp_PoisonedBookRetriedNTimesThenSkipped(t *testing.T) {
	withAIParseBatchConfig(t, 8, 90, 1)
	withNoSplitDelay(t)
	withGiveUpStore(t)

	paths := make([]string, 8)
	for i := range paths {
		paths[i] = filepath.Join("/lib", "book-"+string(rune('a'+i))+".m4b")
	}
	poison := filepath.Base(paths[5])

	for run := 1; run <= maxAIParseSingleFileFailures; run++ {
		s, p, _ := runPoisonedPhase(t, paths, poison)
		if p.callsWith[poison] == 0 {
			t.Fatalf("run %d: poison not attempted before the cap", run)
		}
		if s.BooksFailed != 1 || s.BooksSkipped != 0 {
			t.Fatalf("run %d: summary %+v, want 1 failed, 0 skipped", run, s)
		}
		wantGivenUp := 0
		if run == maxAIParseSingleFileFailures {
			wantGivenUp = 1
		}
		if s.BooksGivenUp != wantGivenUp {
			t.Errorf("run %d: BooksGivenUp = %d, want %d", run, s.BooksGivenUp, wantGivenUp)
		}
	}

	m, err := loadAIParseFailureMark(paths[5])
	if err != nil || m.Count != maxAIParseSingleFileFailures || m.LastReason == "" {
		t.Fatalf("mark = %+v err=%v, want count %d with a reason", m, err, maxAIParseSingleFileFailures)
	}

	s, p, books := runPoisonedPhase(t, paths, poison)
	if got := p.callsWith[poison]; got != 0 {
		t.Errorf("run %d: %d AI call(s) included the given-up book, want 0", maxAIParseSingleFileFailures+1, got)
	}
	if s.BooksSkipped != 1 || s.BooksFailed != 0 || s.BooksParsed != 7 || s.BooksNominated != 8 {
		t.Errorf("summary %+v, want 8 nominated, 1 skipped, 7 parsed, 0 failed", s)
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1: the 7 healthy books should parse in one batch without a split", got)
	}
	if books[5].Title != "" {
		t.Errorf("skipped book was written: %q", books[5].Title)
	}
}

// A path change -- a new directory with the same filename, or a new filename --
// makes a given-up book eligible again.
func TestAIParseGiveUp_RenameMakesBookEligibleAgain(t *testing.T) {
	withAIParseBatchConfig(t, 8, 90, 1)
	withNoSplitDelay(t)
	withGiveUpStore(t)

	orig := "/lib/old/poison.m4b"
	for range maxAIParseSingleFileFailures {
		runPoisonedPhase(t, []string{orig}, "poison.m4b")
	}
	if s, p, _ := runPoisonedPhase(t, []string{orig}, "poison.m4b"); s.BooksSkipped != 1 || p.calls.Load() != 0 {
		t.Fatalf("precondition: book not given up (summary %+v, %d calls)", s, p.calls.Load())
	}

	for _, moved := range []string{"/lib/new/poison.m4b", "/lib/old/poison-renamed.m4b"} {
		s, p, _ := runPoisonedPhase(t, []string{moved}, "poison.m4b")
		if s.BooksSkipped != 0 || p.calls.Load() == 0 {
			t.Errorf("%s: summary %+v with %d call(s), want the moved book attempted", moved, s, p.calls.Load())
		}
	}
}

// No store (the inline path in unit tests, or a store that is not wired): the
// cap is inert and nothing is skipped.
func TestAIParseGiveUp_NoStoreSkipsNothing(t *testing.T) {
	prev := getStore()
	SetStore(nil)
	t.Cleanup(func() { SetStore(prev) })
	books, cands := distinctCandidates(3)
	kept, skipped := filterGivenUpAIParse(books, cands, logger.New("test"))
	if skipped != 0 || !slices.Equal(kept, cands) {
		t.Errorf("kept %v skipped %d, want all kept", kept, skipped)
	}
}
