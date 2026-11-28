// file: internal/plugins/dedup/books_tasklists.go
// version: 0.1.0
// guid: 0b5e7d42-93c1-4a68-b7f0-e2d14c8a9573
// last-edited: 2026-11-28

package dedup

import (
	"context"
	"fmt"
	"reflect"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
)

// The books module's own KindDeps for the dedup kinds (F5-18 A6d), dark: nothing
// outside tests calls BooksTaskLists until the flip's registration commit.
//
// What it builds (deps_books.go): the five groups' read stores, the gated writer
// behind every NewWriter (each method ONE gate call stamped with the method's
// whole row), the split-book writer, the LSH writer, and the engine factories'
// adapters. What it does NOT build is the dedup engine over the run's gated
// writer. The engine writes candidates, labels and merges through its own stores
// (seams.d/F5-04.md, "Engine writes are the engine's"), so the flip hands in
// NewEngine, as it does for AcoustID's scan engine.
//
// The receiver of Kinds is a zero Plugin: the native kinds read nothing from the
// plugin (the HEAD shells' p.engine, p.store and p.embeddingStore are the
// inputs below).

// Source is the library store the builder reads and writes through. It is the
// plugin's own store interface, so the same value HEAD's New takes satisfies it.
type Source = pluginStore

// IndexSource is the store's secondary-index and flag capabilities, resolved by
// the caller through database.AsCapability (the registry hands the plugin the
// server's search-indexing decorator, which does not carry them itself).
type IndexSource interface {
	ISBNIndexStore
	WindowIndexStore
	LSHIndexStore
	BookfileSegDropStore
}

// WriteEngine is the dedup engine the kinds take over a run's gated writer: the
// scoring group's calls, the embed calls and the review pass.
type WriteEngine interface {
	scoringEngine
	embeddingKindEngine
	llmReviewEngine
}

// LiveEngine is the live engine's calls that are not a gated engine's: the pair
// scorer, the score config, the embedding model, the read-only rescore preview
// and the re-band the band-threshold apply runs.
type LiveEngine interface {
	pairScorer
	ScoreConfig() unified.ScoreConfig
	EmbeddingModel() string
	Rescore(ctx context.Context, apply bool) (dedupengine.RescoreResult, error)
	ReloadScoreConfig(ctx context.Context, cfg unified.ScoreConfig) (dedupengine.RescoreResult, error)
}

// BooksTaskInputs is everything BooksTaskLists builds the dedup lists from.
type BooksTaskInputs struct {
	// Source is the library store; Indexes its index capabilities.
	Source  Source
	Indexes IndexSource
	// Embeddings is the embedding store (dedup candidates, labeled examples and
	// the emb:v: rows). HEAD registers none of the dedup operations without an
	// engine; the flip omits this list for a server with embedding disabled.
	Embeddings *database.EmbeddingStore
	// NewEngine builds the dedup engine over the run's gated writer (nil writer in
	// a Preview run). Engine is the live engine's reads and its re-band.
	NewEngine func(tasks.Writer) WriteEngine
	Engine    LiveEngine
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every dedup kind with its schedule, over the books
// module's own dependency values. A nil input makes it return an error naming
// it: the kind would otherwise register and refuse at its first run.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for _, c := range []struct {
		name string
		set  bool
	}{
		{"Source", isSet(in.Source)},
		{"Indexes", isSet(in.Indexes)},
		{"Embeddings", in.Embeddings != nil},
		{"NewEngine", in.NewEngine != nil},
		{"Engine", isSet(in.Engine)},
		{"Schedules", in.Schedules != nil},
	} {
		if !c.set {
			missing = append(missing, c.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("dedup: BooksTaskLists: missing input(s): %v", missing)
	}
	deps, err := newBooksKindDeps(in)
	if err != nil {
		return nil, err
	}
	return (&Plugin{}).Kinds(deps, in.Schedules), nil
}

// isSet reports whether v is a usable interface value: not nil, and not a typed
// nil held in a non-nil interface.
func isSet(v any) bool {
	if v == nil {
		return false
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return !rv.IsNil()
	}
	return true
}

// newBooksGate is the one place the gate is built over a run's writer. A nil w
// (a ReadOnly or Preview run) refuses every gated write.
func newBooksGate(w tasks.Writer, rows writer.Rows) *writer.Gate { return writer.ForRun(w, rows) }
