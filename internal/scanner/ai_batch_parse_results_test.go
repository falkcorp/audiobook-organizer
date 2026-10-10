// file: internal/scanner/ai_batch_parse_results_test.go
// version: 1.0.0
// guid: 7a3c9e51-2d84-4b06-b1f7-5e0a9c63d842
// last-edited: 2026-10-10

package scanner

import (
	"context"
	"errors"
	"sync"
	"testing"

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
//
// An item the split re-asks is counted once, when it resolves, not once per
// call. An item the phase never reached (aborted or cancelled before its batch
// ran) is not counted: it has no disposition yet.
func TestParseResults_Disposition(t *testing.T) {
	prevDelay := splitCallDelay
	splitCallDelay = 0
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

			cands := make([]int, len(c.books))
			for i := range cands {
				cands[i] = i
			}
			runAIBatchPhase(context.Background(), c.parser, c.books, cands, logger.New("test"), saveBookAndReportPath)

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
		})
	}
}
