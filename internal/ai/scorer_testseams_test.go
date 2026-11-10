// file: internal/ai/scorer_testseams_test.go
// version: 1.0.1
// guid: a38a47da-f1e8-49c1-9847-7651c2451ecb
// last-edited: 2026-11-09

package ai

import database "github.com/falkcorp/audiobook-organizer/internal/books/store"

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
