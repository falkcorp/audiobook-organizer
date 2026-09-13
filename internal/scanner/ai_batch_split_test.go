// file: internal/scanner/ai_batch_split_test.go
// version: 1.0.0
// guid: 5b0e7c2a-9d41-4f38-a6e2-3c8f1d7b2e90
// last-edited: 2026-09-13

package scanner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// countErr is the error OpenAIParser.ParseBatch returns for a reply with the
// wrong number of results: a *ReplyParseError wrapping a *ResultCountError.
func countErr(got, expected int) error {
	return &ai.ReplyParseError{Err: &ai.ResultCountError{Got: got, Expected: expected}, Excerpt: "{}"}
}

// poisonParser behaves like the production failure: any batch containing the
// poison filename comes back with the wrong count (one result for many, or two
// for one), and every other batch parses with each book titled after its own
// filename so a positional mix-up would be visible.
type poisonParser struct {
	poison   string
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    atomic.Int64
	sizes    []int
}

func (p *poisonParser) ParseBatch(_ context.Context, filenames []string) ([]*ai.ParsedMetadata, error) {
	p.mu.Lock()
	p.inFlight++
	p.maxSeen = max(p.maxSeen, p.inFlight)
	p.sizes = append(p.sizes, len(filenames))
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.inFlight--; p.mu.Unlock() }()
	p.calls.Add(1)

	if p.poison == "*" || slices.Contains(filenames, p.poison) {
		got := 1
		if len(filenames) == 1 {
			got = 2
		}
		return nil, countErr(got, len(filenames))
	}
	out := make([]*ai.ParsedMetadata, len(filenames))
	for i, f := range filenames {
		out[i] = &ai.ParsedMetadata{Title: "parsed:" + f}
	}
	return out, nil
}

func distinctCandidates(n int) ([]Book, []int) {
	books := make([]Book, n)
	idx := make([]int, n)
	for i := range books {
		books[i] = Book{FilePath: fmt.Sprintf("/lib/book-%02d.m4b", i)}
		idx[i] = i
	}
	return books, idx
}

func noopSave(context.Context, *Book) (string, error) { return "", nil }

func withNoSplitDelay(t *testing.T) {
	t.Helper()
	prev := splitCallDelay
	splitCallDelay = 0
	t.Cleanup(func() { splitCallDelay = prev })
}

// The production shape: batch of 8, one worker, one filename that makes the
// model collapse the reply. Every other book must still be parsed -- and onto
// the RIGHT book -- and the poison one reported as a failed book with a reason.
func TestRunAIBatchPhase_SplitRecoversBooksAroundAPoisonFilename(t *testing.T) {
	withAIParseBatchConfig(t, 8, 90, 1)
	withNoSplitDelay(t)
	books, cands := distinctCandidates(8)
	poison := filepath.Base(books[5].FilePath)
	p := &poisonParser{poison: poison}

	s := runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), noopSave)

	for i, b := range books {
		want := "parsed:" + filepath.Base(b.FilePath)
		if i == 5 {
			want = ""
		}
		if b.Title != want {
			t.Errorf("books[%d].Title = %q, want %q", i, b.Title, want)
		}
	}
	if s.BooksParsed != 7 || s.BooksFailed != 1 || s.BatchesFailed != 0 || s.BatchesSplit != 1 || s.BatchesOK != 1 {
		t.Errorf("summary = %+v, want 7 parsed, 1 failed book, 0 failed batches, 1 split, 1 ok batch", s)
	}
	if len(s.BookFailures) != 1 || s.BookFailures[0].Path != books[5].FilePath || s.BookFailures[0].Err == "" {
		t.Errorf("BookFailures = %+v, want the poison book with a reason", s.BookFailures)
	}
	if !s.Failed() {
		t.Error("a run that lost a book reported itself as not failed")
	}
	// 8 -> 4,4 -> (2,2) -> (1,1): 1 + 2 + 2 + 2 = 7 calls.
	if got := p.calls.Load(); got != 7 {
		t.Errorf("calls = %d (sizes %v), want 7 for one poison file in 8", got, p.sizes)
	}
	if p.maxSeen != 1 {
		t.Errorf("max in-flight = %d, want 1: split calls must be sequential", p.maxSeen)
	}
}

// The bound, exactly: a batch where EVERY call comes back short splits all the
// way down, and that worst case is 2n-1 calls in total -- 2n-2 extra. Exact
// equality, so a broken recursion floor (splitting past one file, or stopping
// above it) shows up as a different count.
func TestParseBatchSplitting_WorstCaseCallCountIsExactlyTheBound(t *testing.T) {
	for n := 1; n <= 20; n++ {
		names := make([]string, n)
		for i := range names {
			names[i] = fmt.Sprintf("f%d", i)
		}
		p := &poisonParser{poison: "*"}
		call := func(f []string) ([]*ai.ParsedMetadata, error) { return p.ParseBatch(context.Background(), f) }
		out := parseBatchSplitting(names, call, func(int) bool { return true })

		if got, want := int(p.calls.Load()), 2*n-1; got != want {
			t.Errorf("n=%d: %d calls, want exactly %d", n, got, want)
		}
		if out.extraCalls > maxSplitExtraCalls(n) {
			t.Errorf("n=%d: %d extra calls exceeds the bound %d", n, out.extraCalls, maxSplitExtraCalls(n))
		}
		if out.err != nil {
			t.Errorf("n=%d: budget guard fired on a clean halving: %v", n, out.err)
		}
		for i, e := range out.bookErrs {
			if e == nil {
				t.Errorf("n=%d: file %d not reported as a failed book", n, i)
			}
		}
		for _, sz := range p.sizes {
			if sz < 1 {
				t.Errorf("n=%d: made a call with %d filenames", n, sz)
			}
		}
	}
}

// Through the phase: the production batch of 8, all poisoned, costs 15 calls
// and records 8 failed books -- not 8 failed BATCHES, which would trip the
// 3-failure abort meant for a dead backend.
func TestRunAIBatchPhase_AllPoisonedBatchStaysWithinBoundAndDoesNotAbort(t *testing.T) {
	withAIParseBatchConfig(t, 8, 90, 1)
	withNoSplitDelay(t)
	books, cands := distinctCandidates(16) // two batches
	p := &poisonParser{poison: "*"}

	s := runAIBatchPhase(context.Background(), p, books, cands, logger.New("test"), noopSave)

	if got, want := p.calls.Load(), int64(2*(1+maxSplitExtraCalls(8))); got != want {
		t.Errorf("calls = %d, want %d (two batches at the 2n-1 bound)", got, want)
	}
	if s.BooksFailed != 16 || s.BatchesFailed != 0 || s.Aborted() {
		t.Errorf("summary = %+v, want 16 failed books, 0 failed batches, no abort", s)
	}
}

// Transport errors, timeouts, other bad replies and permanent failures must
// NOT split: one call per batch, handled by the existing failure path.
func TestRunAIBatchPhase_NonCountErrorsDoNotSplit(t *testing.T) {
	withAIParseBatchConfig(t, 8, 90, 1)
	withNoSplitDelay(t)
	for name, err := range map[string]error{
		"transport":           errors.New("connection reset by peer"),
		"timeout":             context.DeadlineExceeded,
		"other reply error":   &ai.ReplyParseError{Err: errors.New("invalid character"), Excerpt: "x"},
		"permanent and count": errors.Join(&ai.PermanentError{Err: errors.New("insufficient_quota")}, countErr(1, 8)),
	} {
		t.Run(name, func(t *testing.T) {
			books, cands := distinctCandidates(8)
			f := &fakeAIParser{err: err}
			s := runAIBatchPhase(context.Background(), f, books, cands, logger.New("test"), noopSave)
			if got := f.calls.Load(); got != 1 {
				t.Errorf("calls = %d, want 1: a %s error was split", got, name)
			}
			if s.BatchesSplit != 0 || s.BooksFailed != 0 || s.BatchesFailed != 1 {
				t.Errorf("summary = %+v, want no split, no failed books, 1 failed batch", s)
			}
			if len(s.BatchFailures) != 1 || len(s.BatchFailures[0].Filenames) != 8 {
				t.Errorf("BatchFailures = %+v, want the whole batch of 8 recorded", s.BatchFailures)
			}
		})
	}
}

// A transport error DURING a split ends the split: no further calls, one batch
// failure for the whole batch, and the half already parsed is kept.
func TestParseBatchSplitting_TransportErrorMidSplitStops(t *testing.T) {
	names := []string{"a", "b", "c", "d"}
	var calls int
	call := func(f []string) ([]*ai.ParsedMetadata, error) {
		calls++
		switch calls {
		case 1:
			return nil, countErr(1, len(f))
		case 2:
			return []*ai.ParsedMetadata{{Title: "a"}, {Title: "b"}}, nil
		default:
			return nil, errors.New("connection refused")
		}
	}
	out := parseBatchSplitting(names, call, func(int) bool { return true })
	if calls != 3 || out.err == nil || isResultCountMismatch(out.err) {
		t.Fatalf("calls=%d err=%v, want 3 calls ending on the transport error", calls, out.err)
	}
	if !out.resolved[0] || !out.resolved[1] || out.resolved[2] || out.resolved[3] {
		t.Errorf("resolved = %v, want first half kept and second half left", out.resolved)
	}
}

// An abort mid-split stops re-asking.
func TestParseBatchSplitting_AbortStopsTheSplit(t *testing.T) {
	var calls int
	call := func(f []string) ([]*ai.ParsedMetadata, error) { calls++; return nil, countErr(1, len(f)) }
	out := parseBatchSplitting([]string{"a", "b", "c", "d"}, call, func(int) bool { return false })
	if calls != 1 || !out.stopped || out.err != nil {
		t.Errorf("calls=%d stopped=%v err=%v, want 1 call and a clean stop", calls, out.stopped, out.err)
	}
}
