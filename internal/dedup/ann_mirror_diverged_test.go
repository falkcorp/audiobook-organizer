// file: internal/dedup/ann_mirror_diverged_test.go
// version: 1.0.2
// guid: e493295b-c503-4981-9c08-97312169a02c
// last-edited: 2026-11-09

package dedup

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// R1-15 follow-up review, MAJOR-1(C): a mirror the engine drops leaves the
// graph short of a Pebble row, so it must reach the ANN store's divergence
// flag (and with it the clean-stop marker), not just a log line.

// divergenceANN is a database.VectorANNStore that fails every write when fail
// is set, and records each MarkDiverged reason.
type divergenceANN struct {
	fail     bool
	writes   int
	diverged []string
}

var errDivergenceANN = errors.New("simulated ANN store failure")

func (d *divergenceANN) Upsert(context.Context, string, string, []float32, map[string]string) error {
	d.writes++
	if d.fail {
		return errDivergenceANN
	}
	return nil
}
func (d *divergenceANN) Delete(context.Context, string, string) error {
	d.writes++
	if d.fail {
		return errDivergenceANN
	}
	return nil
}
func (d *divergenceANN) Get(context.Context, string, string) (map[string]string, error) {
	return nil, nil
}
func (d *divergenceANN) FindSimilar(context.Context, string, []float32, int, map[string]string) ([]database.ChromemSimilarityResult, error) {
	return nil, nil
}
func (d *divergenceANN) CountByType(context.Context, string) (int, error) { return 0, nil }
func (d *divergenceANN) Close() error                                     { return nil }
func (d *divergenceANN) MarkDiverged(reason string)                       { d.diverged = append(d.diverged, reason) }

var _ database.SnapshotDivergence = (*divergenceANN)(nil)

// countingEmbedClient returns a client whose every call yields a 4-dim vector
// and bumps *calls, so a test can tell the cached path from a fresh embed.
func countingEmbedClient(calls *int) *ai.EmbeddingClient {
	c := ai.NewEmbeddingClientWithOptions("k", "bge-m3", "")
	c.SetRawEmbedForTest(func(_ context.Context, texts []string) ([][]float32, error) {
		*calls++
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{1, 2, 3, 4}
		}
		return out, nil
	})
	return c
}

// TestEngineMirror_AFailedMirrorMarksTheGraphDiverged covers every mirror
// outside HydrateChromem (which counts its failures and leaves the graph
// incomplete instead): a fresh book embed, a cached book embed, a fresh
// author embed, a cached author embed, and the empty-title delete.
func TestEngineMirror_AFailedMirrorMarksTheGraphDiverged(t *testing.T) {
	ctx := context.Background()
	primary := true
	books := map[string]*database.Book{
		"knightfall": {ID: "knightfall", Title: "Knightfall", IsPrimaryVersion: &primary},
		"hush":       {ID: "hush", Title: "Hu", IsPrimaryVersion: &primary}, // not a usable title
	}
	const authorID = 7
	newEngine := func(t *testing.T) (*Engine, *int) {
		engine, mock, _ := setupTestEngine(t)
		mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
		mock.GetAuthorByIDFunc = func(id int) (*database.Author, error) {
			if id == authorID {
				return &database.Author{ID: authorID, Name: "Bruce Wayne"}, nil
			}
			return nil, nil
		}
		calls := 0
		engine.embedClient = countingEmbedClient(&calls)
		return engine, &calls
	}
	embedBook := func(t *testing.T, e *Engine, id string) {
		t.Helper()
		if _, err := e.EmbedBooks(ctx, []string{id}); err != nil {
			t.Fatalf("EmbedBooks(%s): %v", id, err)
		}
	}
	embedAuthor := func(t *testing.T, e *Engine) {
		t.Helper()
		if err := e.EmbedAuthor(ctx, authorID); err != nil {
			t.Fatalf("EmbedAuthor: %v", err)
		}
	}

	for _, tc := range []struct {
		name string
		// prime runs with the ANN store working; run runs with it failing.
		prime, run func(t *testing.T, e *Engine)
		cached     bool
		want       string
	}{
		{name: "fresh book embed", run: func(t *testing.T, e *Engine) { embedBook(t, e, "knightfall") }, want: "knightfall"},
		{
			name:   "cached book embed",
			prime:  func(t *testing.T, e *Engine) { embedBook(t, e, "knightfall") },
			run:    func(t *testing.T, e *Engine) { embedBook(t, e, "knightfall") },
			cached: true, want: "knightfall",
		},
		{name: "fresh author embed", run: embedAuthor, want: strconv.Itoa(authorID)},
		{name: "cached author embed", prime: embedAuthor, run: embedAuthor, cached: true, want: strconv.Itoa(authorID)},
		{name: "empty-title delete", run: func(t *testing.T, e *Engine) { embedBook(t, e, "hush") }, want: "hush"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, calls := newEngine(t)
			ann := &divergenceANN{}
			e.SetChromemStore(ann)
			if tc.prime != nil {
				tc.prime(t, e)
				if len(ann.diverged) != 0 {
					t.Fatalf("a working ANN store was marked diverged: %v", ann.diverged)
				}
			}
			before := *calls
			ann.fail = true
			ann.writes = 0
			tc.run(t, e)
			if tc.cached && *calls != before {
				t.Fatalf("the embed called the model %d time(s); the case needs the cached path", *calls-before)
			}
			if ann.writes == 0 {
				t.Fatal("the engine never wrote the ANN store; the case does not reach a mirror")
			}
			if len(ann.diverged) != 1 || !strings.Contains(ann.diverged[0], tc.want) {
				t.Errorf("MarkDiverged calls = %q, want one naming %q", ann.diverged, tc.want)
			}
		})
	}

	t.Run("a working store is not marked", func(t *testing.T) {
		e, _ := newEngine(t)
		ann := &divergenceANN{}
		e.SetChromemStore(ann)
		embedBook(t, e, "knightfall")
		embedBook(t, e, "knightfall")
		embedBook(t, e, "hush")
		embedAuthor(t, e)
		embedAuthor(t, e)
		if ann.writes != 5 {
			t.Fatalf("ANN writes = %d, want 5", ann.writes)
		}
		if len(ann.diverged) != 0 {
			t.Errorf("MarkDiverged calls = %q, want none", ann.diverged)
		}
	})
}

// TestEngineMirror_ACachedNonPrimaryBookMarksTheGraphDiverged (R1-15 round-2
// review, S2): a cache hit writes nothing to Pebble, so when it mirrors a
// book the hydrate rule leaves out (a non-primary version), the graph holds a
// row a boot would not build and nothing on the Pebble side says so. The
// mirror must mark the graph diverged. A primary book's cache hit, and a
// book with no primary flag at all, must not.
func TestEngineMirror_ACachedNonPrimaryBookMarksTheGraphDiverged(t *testing.T) {
	ctx := context.Background()
	primary, nonPrimary := true, false
	books := map[string]*database.Book{
		"the-court-of-owls": {ID: "the-court-of-owls", Title: "The Court of Owls", IsPrimaryVersion: &nonPrimary},
		"knightfall":        {ID: "knightfall", Title: "Knightfall", IsPrimaryVersion: &primary},
		"year-one":          {ID: "year-one", Title: "Year One"},
	}
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"the-court-of-owls", true},
		{"knightfall", false},
		{"year-one", false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			engine, mock, _ := setupTestEngine(t)
			mock.GetBookByIDFunc = func(id string) (*database.Book, error) { return books[id], nil }
			calls := 0
			engine.embedClient = countingEmbedClient(&calls)
			ann := &divergenceANN{}
			engine.SetChromemStore(ann)

			if _, err := engine.EmbedBooks(ctx, []string{tc.id}); err != nil {
				t.Fatalf("fresh EmbedBooks: %v", err)
			}
			if len(ann.diverged) != 0 {
				t.Fatalf("the fresh embed marked the graph diverged: %v", ann.diverged)
			}
			before, writes := calls, ann.writes
			if _, err := engine.EmbedBooks(ctx, []string{tc.id}); err != nil {
				t.Fatalf("cached EmbedBooks: %v", err)
			}
			if calls != before {
				t.Fatalf("the second embed called the model; the case needs the cached path")
			}
			if ann.writes != writes+1 {
				t.Fatalf("the cached embed made %d ANN writes, want 1 (the mirror)", ann.writes-writes)
			}
			got := len(ann.diverged) == 1 && strings.Contains(ann.diverged[0], tc.id)
			if got != tc.want || len(ann.diverged) > 1 {
				t.Errorf("MarkDiverged calls = %q, want a call naming %s: %v", ann.diverged, tc.id, tc.want)
			}
		})
	}
}
