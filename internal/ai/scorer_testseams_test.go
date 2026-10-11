// file: internal/ai/scorer_testseams_test.go
// version: 1.0.0
// guid: a38a47da-f1e8-49c1-9847-7651c2451ecb
// last-edited: 2026-10-11

package ai

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Test seams that inject a fake backend into a scorer. Production builds the
// scorers through NewEmbeddingScorer / NewLLMScorer; these two had no
// production caller (deadcode-unreachable) and moved here from the non-test
// files.

func NewEmbeddingScorerWithAPI(api embeddingAPI, store *database.EmbeddingStore) *EmbeddingScorer {
	return &EmbeddingScorer{api: api, store: store}
}

func NewLLMScorerWithBackend(backend metadataLLMBackend) *LLMScorer {
	return &LLMScorer{backend: backend}
}
