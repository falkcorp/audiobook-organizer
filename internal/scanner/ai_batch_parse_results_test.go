// file: internal/scanner/ai_batch_parse_results_test.go
// version: 1.3.0
// guid: 7a3c9e51-2d84-4b06-b1f7-5e0a9c63d842
// last-edited: 2026-10-11

package scanner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// scriptedAIParser answers each ParseBatch call from a function of its
// filenames, so a test controls exactly which sub-batches succeed.
type scriptedAIParser func(names []string) ([]*ai.ParsedMetadata, error)

func (f scriptedAIParser) ParseBatch(_ context.Context, names []string) ([]*ai.ParsedMetadata, error) {
	return f(names)
}

// countMismatch is the reply error the split handles: a ResultCountError under
// a ReplyParseError, as ai.ParseBatch returns it.
func countMismatch() error {
	return &ai.ReplyParseError{Err: &ai.ResultCountError{Got: 1, Expected: 2}}
}

// TestParseResults_Disposition: ai_parse_results_total gets exactly one count
// per item of a batch, at its final disposition.
//
//	resolved, result merged at least one field        accepted=true
//	resolved, nil result ("had nothing")              accepted=false
//	resolved, result present but nothing merged       accepted=false
//	unresolved, own error (count mismatch in a 1)     accepted=false
//	unresolved, batch failed, no error of its own     accepted=false
//	unresolved, split stopped mid-walk (abort/cancel) accepted=false
//
// One rule for the unresolved cases: an item of a batch whose first call was
// made and that ended with no result is counted false, whether a failed call
// or a stopped split left it that way. An item the split re-asks is counted
// once, when it resolves, not once per call. An item of a batch the phase never
// reached (aborted or cancelled before its first call) is not counted: it has
// no disposition yet.
func TestParseResults_Disposition(t *testing.T) {
	prevDelay := splitCallDelay
	t.Cleanup(func() { splitCallDelay = prevDelay })

	titled := func(names []string) []*ai.ParsedMetadata {
		out := make([]*ai.ParsedMetadata, len(names))
		for i := range out {
			out[i] = &ai.ParsedMetadata{Title: "parsed"}
		}
		return out
	}

	for _, c := range []struct {
		name      string
		books     []Book
		parser    scriptedAIParser
		wantTrue  int
		wantFalse int
		// batchSize, when set, overrides the phase's batch size so one case
		// runs several batches on concurrent workers.
		batchSize int
		// cancelAfterCalls cancels the phase context once that many ParseBatch
		// calls have returned, to stop a split between two of its calls.
		cancelAfterCalls int64
		// splitDelay is the pause before each extra split call. A case that
		// cancels needs it non-zero: with a zero delay the timer and the
		// cancelled context are both ready and the select picks either.
		splitDelay time.Duration
		// wantBatchHeads, when set, pins that batching happened: the first
		// filename of every call asked about exactly batchSize files, sorted.
		// A split call is smaller than a batch, so these are the batches' first
		// calls, one per batch. Without it a case with a batch size override
		// would still pass if the override were ignored and one big batch split
		// its way down to the same counts.
		wantBatchHeads []string
		// callDelay is how long each ParseBatch call takes. A case that asserts
		// concurrency needs the calls to overlap long enough to be seen.
		callDelay time.Duration
		// wantConcurrent asserts more than one call was in flight at once.
		wantConcurrent bool
		// maxElapsed, when set, bounds how long runAIBatchPhase may take. The
		// failed-batch back-off is five seconds; a case that ends the phase
		// during it uses this to pin that the wait ends with the phase's
		// context instead of running out.
		maxElapsed time.Duration
	}{
		{
			name:  "result merged into an empty book",
			books: []Book{{FilePath: "/x/a.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				return []*ai.ParsedMetadata{{Title: "T", Author: "A"}}, nil
			},
			wantTrue: 1,
		},
		{
			name:  "nil result for the filename",
			books: []Book{{FilePath: "/x/a.m4b"}, {FilePath: "/x/b.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				return []*ai.ParsedMetadata{nil, {Title: "T"}}, nil
			},
			wantTrue: 1, wantFalse: 1,
		},
		{
			name:  "result present but empty",
			books: []Book{{FilePath: "/x/a.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				return []*ai.ParsedMetadata{{}}, nil
			},
			wantFalse: 1,
		},
		{
			name: "result present but every field already set",
			books: []Book{{FilePath: "/x/a.m4b", Title: "t", Author: "a", Series: "s", Position: 1,
				Narrator: "n", Publisher: "p", Year: 2001}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				return []*ai.ParsedMetadata{{Title: "T", Author: "A", Series: "S", SeriesNum: 2,
					Narrator: "N", Publisher: "P", Year: 2002}}, nil
			},
			wantFalse: 1,
		},
		{
			name:  "split: every item resolves in a smaller batch, counted once",
			books: []Book{{FilePath: "/x/a.m4b"}, {FilePath: "/x/b.m4b"}, {FilePath: "/x/c.m4b"}, {FilePath: "/x/d.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				if len(n) > 2 {
					return nil, countMismatch()
				}
				return titled(n), nil
			},
			wantTrue: 4,
		},
		{
			name:  "split: one item still wrong on its own",
			books: []Book{{FilePath: "/x/a.m4b"}, {FilePath: "/x/b.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				if len(n) == 2 || n[0] == "b.m4b" {
					return nil, countMismatch()
				}
				return titled(n), nil
			},
			wantTrue: 1, wantFalse: 1,
		},
		{
			name:  "batch fails outright: every item unresolved with no error of its own",
			books: []Book{{FilePath: "/x/a.m4b"}, {FilePath: "/x/b.m4b"}, {FilePath: "/x/c.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				return nil, errors.New("insufficient_quota: credit balance exhausted")
			},
			wantFalse: 3,
		},
		{
			// The phase context is really cancelled by the first call, which then
			// returns the cancellation. The call still failed, so the phase takes
			// its failed-batch path, including the five-second back-off; the
			// back-off ends at once because the phase context is done
			// (aiGroupCtx.Done()). maxElapsed pins that: a back-off that
			// ignored the cancellation would run the full five seconds.
			name:  "first call cancelled: every item unresolved with no error of its own",
			books: []Book{{FilePath: "/x/a.m4b"}, {FilePath: "/x/b.m4b"}, {FilePath: "/x/c.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				return nil, context.Canceled
			},
			cancelAfterCalls: 1,
			wantFalse:        3,
			maxElapsed:       2 * time.Second,
		},
		{
			// The same situation as the case above reached the other way: the
			// first call was made (a count mismatch), the first half resolved,
			// and the phase stopped before the second half. The second half's
			// two items were reached and left unresolved, so they count false
			// exactly as in a cancel during the first call.
			name:  "split stopped between calls: the half never asked is counted false",
			books: []Book{{FilePath: "/x/a.m4b"}, {FilePath: "/x/b.m4b"}, {FilePath: "/x/c.m4b"}, {FilePath: "/x/d.m4b"}},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				if len(n) > 2 {
					return nil, countMismatch()
				}
				return titled(n), nil
			},
			cancelAfterCalls: 2,
			splitDelay:       50 * time.Millisecond,
			wantTrue:         2, wantFalse: 2,
		},
		{
			// Three batches of four on concurrent workers: one clean, one that
			// splits into two resolvable halves, one with a poisoned file that
			// is still wrong on its own. 12 items, 12 counts, none twice.
			name: "several batches in flight at once: every item counted exactly once",
			books: []Book{
				{FilePath: "/x/a0.m4b"}, {FilePath: "/x/a1.m4b"}, {FilePath: "/x/a2.m4b"}, {FilePath: "/x/a3.m4b"},
				{FilePath: "/x/s0.m4b"}, {FilePath: "/x/s1.m4b"}, {FilePath: "/x/s2.m4b"}, {FilePath: "/x/s3.m4b"},
				{FilePath: "/x/p0.m4b"}, {FilePath: "/x/p1.m4b"}, {FilePath: "/x/p2.m4b"}, {FilePath: "/x/p3.m4b"},
			},
			parser: func(n []string) ([]*ai.ParsedMetadata, error) {
				// Decided by the names asked about, not by call order, so the
				// result does not depend on how the workers interleave.
				poisoned := len(n) > 1 && slices.Contains(n, "p0.m4b")
				if poisoned || (strings.HasPrefix(n[0], "s") && len(n) > 2) {
					return nil, countMismatch()
				}
				if len(n) == 1 && n[0] == "p0.m4b" {
					return nil, countMismatch()
				}
				return titled(n), nil
			},
			batchSize: 4,
			wantTrue:  11, wantFalse: 1,
			wantBatchHeads: []string{"a0.m4b", "p0.m4b", "s0.m4b"},
			callDelay:      150 * time.Millisecond,
			wantConcurrent: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var got []bool
			prev := recordParseResult
			recordParseResult = func(task string, accepted bool) {
				if task != ai.TaskBatchParse {
					t.Errorf("task = %q, want %q", task, ai.TaskBatchParse)
				}
				mu.Lock()
				got = append(got, accepted)
				mu.Unlock()
			}
			t.Cleanup(func() { recordParseResult = prev })

			splitCallDelay = c.splitDelay
			if c.batchSize > 0 {
				withAIParseBatchConfig(t, c.batchSize, 0, len(c.books)/c.batchSize)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			parser := c.parser
			if c.cancelAfterCalls > 0 {
				var calls atomic.Int64
				parser = func(n []string) ([]*ai.ParsedMetadata, error) {
					res, err := c.parser(n)
					if calls.Add(1) == c.cancelAfterCalls {
						cancel()
					}
					return res, err
				}
			}

			// Records every call's filenames and how many were in flight at once.
			var callMu sync.Mutex
			var callNames [][]string
			var inFlight, peakInFlight atomic.Int32
			inner := parser
			parser = func(n []string) ([]*ai.ParsedMetadata, error) {
				cur := inFlight.Add(1)
				defer inFlight.Add(-1)
				for {
					peak := peakInFlight.Load()
					if cur <= peak || peakInFlight.CompareAndSwap(peak, cur) {
						break
					}
				}
				callMu.Lock()
				callNames = append(callNames, slices.Clone(n))
				callMu.Unlock()
				time.Sleep(c.callDelay)
				return inner(n)
			}

			cands := make([]int, len(c.books))
			for i := range cands {
				cands[i] = i
			}
			phaseStart := time.Now()
			runAIBatchPhase(ctx, parser, c.books, cands, logger.New("test"), saveBookAndReportPath)
			if c.maxElapsed > 0 {
				if took := time.Since(phaseStart); took > c.maxElapsed {
					t.Errorf("runAIBatchPhase took %s, want under %s (the failed-batch back-off must end when the phase is cancelled)", took, c.maxElapsed)
				}
			}

			mu.Lock()
			defer mu.Unlock()
			nTrue, nFalse := 0, 0
			for _, a := range got {
				if a {
					nTrue++
				} else {
					nFalse++
				}
			}
			if nTrue != c.wantTrue || nFalse != c.wantFalse {
				t.Errorf("accepted=true %d, accepted=false %d; want %d and %d (items %d, counts %d)",
					nTrue, nFalse, c.wantTrue, c.wantFalse, len(c.books), len(got))
			}

			if c.wantBatchHeads != nil {
				callMu.Lock()
				var heads []string
				for _, names := range callNames {
					if len(names) == c.batchSize {
						heads = append(heads, names[0])
					}
				}
				callMu.Unlock()
				slices.Sort(heads)
				if !slices.Equal(heads, c.wantBatchHeads) {
					t.Errorf("first calls of %d files began with %v, want %v (the batch size override did not cut the batches)",
						c.batchSize, heads, c.wantBatchHeads)
				}
			}
			if c.wantConcurrent && peakInFlight.Load() < 2 {
				t.Errorf("peak calls in flight = %d, want more than 1 (the batches did not run concurrently)", peakInFlight.Load())
			}
		})
	}
}
