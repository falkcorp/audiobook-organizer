// file: internal/plugins/dedup/plugin.go
// version: 1.6.0
// guid: d1e2f3a4-b5c6-7890-abcd-ef1234567890
// last-edited: 2026-06-14

// Package dedup is the UOS plugin for deduplication operations.
// It wraps the internal dedup.Engine and registers OperationDefs through
// the public pkg/plugin/sdk interface.
package dedup

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
	dedupengine "github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Plugin is the dedup plugin. It wraps the shared dedup.Engine and embedding store so that
// the Run functions can call engine methods without importing internal packages.
type Plugin struct {
	engine         *dedupengine.Engine
	store          database.Store
	embeddingStore *database.EmbeddingStore
	registry       sdk.Registry // set in Register; used by ops that enqueue follow-on work
}

// New constructs a dedup Plugin. engine and embeddingStore may be nil if embedding is disabled;
// the embed-scan op will return a descriptive error when run.
func New(engine *dedupengine.Engine, store database.Store, embeddingStore *database.EmbeddingStore) *Plugin {
	return &Plugin{engine: engine, store: store, embeddingStore: embeddingStore}
}

// ID implements sdk.Plugin.
func (p *Plugin) ID() string { return "dedup" }

// Name implements sdk.Plugin.
func (p *Plugin) Name() string { return "Deduplication" }

// Version implements sdk.Plugin.
func (p *Plugin) Version() string { return "1.0.0" }

// Register registers all dedup OperationDefs with the UOS registry.
// UOS-07 registers embed-scan; UOS-09 adds full-scan, llm-review, and book-signature-scan.
// T012 adds lsh-index-build (fable5).
func (p *Plugin) Register(r sdk.Registry) error {
	p.registry = r // save so op runners can enqueue follow-on operations
	if p.engine == nil {
		return nil
	}

	ops := []sdk.OperationDef{
		p.embedScanDef(),
		p.embedAsyncDef(),
		p.fullScanDef(),
		p.llmReviewDef(),
		p.bookSignatureScanDef(),
		p.splitBookScanDef(),
		p.purgeStaleDef(),
		p.lshIndexBuildDef(),
		p.purgeLegacyFPDef(),   // T015: legacy fingerprint purge op
		p.embReencodeDef(),     // T021: float16+zstd re-encode op
		p.bookfileSegDropDef(), // T020: drop AcoustID segment fields from stored values
		p.datasetBackfillDef(), // C4: label + suppress residual pending candidates
		p.checkBookDef(),       // M4: per-book dedup check via dependency scheduler
		p.buildISBNIndexDef(),  // T022: ISBN/ASIN secondary index backfill
	}

	for _, op := range ops {
		if err := r.RegisterOp(op); err != nil {
			return err
		}
	}
	return nil
}
