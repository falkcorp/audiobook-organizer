// file: internal/plugins/dedup/books_tasklists.go
// version: 0.1.0
// guid: 0b5e7d42-93c1-4a68-b7f0-e2d14c8a9573
// last-edited: 2026-11-28

package dedup

import (
	"context"
	"fmt"
	"reflect"
	"slices"

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
// NewEngine is optional, and until a gated engine exists (TODO-F5-18-GATED-ENGINE)
// nothing hands one in: BooksTaskLists then leaves the engine kinds out
// (EngineKinds) and builds the rest. The ungated engine must never be handed in
// as NewEngine: its writes would go around the run's fence and its Effects.
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
	// engine and an embedding store, which is a server with embedding disabled:
	// the books module does not call BooksTaskLists for one (books.taskListInputs,
	// DedupEnabled), so a nil Embeddings here is a wiring error, not that shape.
	Embeddings *database.EmbeddingStore
	// NewEngine builds the dedup engine over the run's gated writer (nil writer in
	// a Preview run). OPTIONAL: unset (nil, or a typed nil) leaves out exactly the
	// EngineKinds. It is set only once a gated engine exists
	// (TODO-F5-18-GATED-ENGINE); never hand it the ungated live engine.
	NewEngine func(tasks.Writer) WriteEngine
	// Engine is the live engine's reads and its re-band.
	Engine LiveEngine
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every dedup kind with its schedule, over the books
// module's own dependency values, except the EngineKinds when NewEngine is unset.
// A nil input makes it return an error naming it: the kind would otherwise
// register and refuse at its first run.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for _, c := range []struct {
		name string
		set  bool
	}{
		{"Source", isSet(in.Source)},
		{"Indexes", isSet(in.Indexes)},
		{"Embeddings", in.Embeddings != nil},
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
	defs := (&Plugin{}).Kinds(deps, in.Schedules)
	if isSet(in.NewEngine) {
		return defs, nil
	}
	return slices.DeleteFunc(defs, func(d tasks.Definition) bool {
		return slices.Contains(engineKinds, d.Kind())
	}), nil
}

// engineKinds are the kinds that run the dedup engine over the run's gated writer
// (ScoringKindDeps.NewEngine, EmbeddingKindDeps.NewEngine and
// LabelKindDeps.NewEngine: scoringEngineFor, embeddingEngineFor and the
// llm-review shell). The ungated live engine writes candidates, labels and merges
// through its own stores, around the run's fence and Effects, so they stay out of
// BooksTaskLists until a gated engine exists (TODO-F5-18-GATED-ENGINE).
var engineKinds = []tasks.Kind{
	purgeStaleKind, autoResolveKind, fullScanKind, rescoreKind, bookSignatureScanKind, drainStaleKind,
	embedScanKind, embedAsyncKind, reembedEmbeddingsKind, breakdownBackfillKind,
	llmReviewKind,
}

// EngineKinds is the 11 kinds BooksTaskLists leaves out while NewEngine is unset:
// purge-stale, auto-resolve, full-scan, rescore, book-signature-scan, drain-stale,
// embed-scan, embed-async, reembed-embeddings, breakdown-backfill and llm-review.
// They stay out until a gated engine exists (TODO-F5-18-GATED-ENGINE), and the
// ungated live engine must never be handed in as NewEngine to bring them back.
// The books module's held table references this list rather than retyping it.
func EngineKinds() []tasks.Kind { return slices.Clone(engineKinds) }

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
