// file: internal/metafetch/service_wiring.go
// version: 1.2.0
// guid: 571bfbf4-238b-49cb-a6d8-b302921dd1c4
// last-edited: 2026-05-01

package metafetch

import (
	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/dedup"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/openlibrary"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

func NewService(db Store) *Service {
	return &Service{db: db}
}

// SetOverrideSources overrides the metadata source chain for testing.
func (mfs *Service) SetOverrideSources(sources []metadata.MetadataSource) {
	mfs.overrideSources = sources
}

// SetActivityService sets the activity service for dual-writing to the unified activity log.
func (mfs *Service) SetActivityService(svc *activity.Service) {
	mfs.activityService = svc
}

// SetWriteBackBatcher sets the iTunes write-back batcher.
func (mfs *Service) SetWriteBackBatcher(b WriteBackEnqueuer) {
	mfs.writeBackBatcher = b
}

// SetSafeWriteDeps installs the Deluge pre-flight guard for cover-art writes.
// Must be called before any cover embedding occurs. Both fields of deps should
// be non-nil for the guard to be fully effective.
func (mfs *Service) SetSafeWriteDeps(deps tagger.SafeWriteDeps) {
	mfs.safeWriteDeps = deps
}

// SetOLStore sets the Open Library dump store for local-first lookups.
func (mfs *Service) SetOLStore(store *openlibrary.OLStore) {
	mfs.olStore = store
}

// SetDedupEngine sets the dedup engine for post-apply dedup checks.
func (mfs *Service) SetDedupEngine(engine *dedup.Engine) {
	mfs.dedupEngine = engine
}

// SetMetadataScorer injects the pluggable metadata candidate scorer. A nil
// scorer (or a scorer that returns errors at runtime) makes the search
// pipeline fall back to the pre-existing significantWords F1 path, so this
// method is safe to leave unset.
func (mfs *Service) SetMetadataScorer(scorer ai.MetadataCandidateScorer) {
	mfs.metadataScorer = scorer
}

// SetMetadataLLMScorer injects the LLM rerank scorer. A nil scorer or a
// scorer that returns errors at runtime makes the rerank pass a no-op, so
// this method is safe to leave unset.
func (mfs *Service) SetMetadataLLMScorer(scorer ai.MetadataCandidateScorer) {
	mfs.llmScorer = scorer
}

// SetISBNEnrichment sets the ISBN enrichment service for background ISBN/ASIN lookups.
func (mfs *Service) SetISBNEnrichment(svc *ISBNService) {
	mfs.isbnEnrichment = svc
}

// ISBNEnrichment returns the ISBN enrichment service (may be nil).
func (mfs *Service) ISBNEnrichment() *ISBNService {
	return mfs.isbnEnrichment
}
