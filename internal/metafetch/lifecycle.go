// file: internal/metafetch/lifecycle.go
// version: 1.4.0

// Lifecycle methods on *metafetch.Service that the serviceregistry
// container picks up via interface satisfaction. PostInit wires the
// service's dependencies (dedup engine, scorers, activity service) by
// pulling them from the container. Replaces the inline SetX block that
// lived in NewServer's AI cluster region.

package metafetch

import (
	"context"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/serviceregistry"
)

// PostInit wires metafetch's optional dependencies into the service.
// Each dep is TryGet'd — if absent (config-gated services that built to
// nil), the corresponding Set call is skipped. The service degrades
// gracefully: scoring tiers + dedup hooks just don't fire.
//
// Replaces inline SetX calls from NewServer's pre-W7 AI block.
// Specifically:
//
//   - SetDedupEngine   (W4 dedup, config-gated)
//   - SetMetadataScorer (W4 metadatascorer, config-gated)
//   - SetMetadataLLMScorer (W4 metadatallmscorer, config-gated)
//   - SetActivityService (W2 activity, DatabasePath-gated)
//
// Several other SetX calls (SetISBNEnrichment, SetOLStore, SetSafeWriteDeps,
// SetWriteBackBatcher) still live inline in NewServer because they depend
// on server-local state (olService, protectedPathCache) that isn't in
// the container yet. Those move when their underlying services migrate.
func (mfs *Service) PostInit(ctx context.Context, c *serviceregistry.Container) error {
	if mfs == nil {
		return nil
	}

	// Dedup engine wiring — engine is config-gated, may be nil
	if engine, ok := serviceregistry.TryGet[*dedup.Engine](c, "dedup"); ok && engine != nil {
		mfs.SetDedupEngine(engine)
		slog.Info("PostInit SetDedupEngine wired")
	}

	// Embedding scorer — config-gated on MetadataEmbeddingScoringEnabled
	if scorer, ok := serviceregistry.TryGet[*ai.EmbeddingScorer](c, "metadatascorer"); ok && scorer != nil {
		mfs.SetMetadataScorer(scorer)
		slog.Info("Metadata candidate scoring embedding tier enabled")
	}

	// LLM rerank scorer — wired unconditionally when llmparser is
	// available; the per-search use_rerank flag + MetadataLLMScoringEnabled
	// config gate whether it actually fires.
	if scorer, ok := serviceregistry.TryGet[*ai.LLMScorer](c, "metadatallmscorer"); ok && scorer != nil {
		mfs.SetMetadataLLMScorer(scorer)
	}

	// Activity service — DatabasePath-gated
	if svc, ok := serviceregistry.TryGet[*activity.Service](c, "activity"); ok && svc != nil {
		mfs.SetActivityService(svc)
	}

	// iTunes write-back enqueuer — type-asserted via the local
	// WriteBackEnqueuer interface so this file stays out of any
	// internal/itunes/service import cycle (itunes/service imports
	// metafetch).
	if enq, ok := serviceregistry.TryGet[WriteBackEnqueuer](c, "writebackbatcher"); ok && enq != nil {
		mfs.SetWriteBackBatcher(enq)
	}

	// OL dump store — local-first lookups. olservice opens the store
	// lazily on first EnsureStore; if nothing has called that yet,
	// Store() returns nil and we skip wiring (the chain reverts to
	// network-only).
	if ol, ok := serviceregistry.TryGet[*OpenLibraryService](c, "olservice"); ok && ol != nil {
		if store := ol.Store(); store != nil {
			mfs.SetOLStore(store)
		}
	}

	// ISBN enrichment — needs a source chain. Empty chain (e.g. no
	// AI key, no Audnexus) means no enrichment.
	if store, ok := serviceregistry.TryGet[isbnEnrichmentStore](c, "store"); ok && store != nil {
		if sources := mfs.BuildSourceChain(); len(sources) > 0 {
			mfs.SetISBNEnrichment(NewISBNService(store, sources))
		}
	}

	return nil
}
