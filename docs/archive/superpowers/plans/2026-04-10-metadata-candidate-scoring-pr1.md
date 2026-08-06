# Metadata Candidate Scoring — PR 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `significantWords` F1 title-similarity signal in `metadata_fetch_service.go` with cosine similarity against the book's stored `text-embedding-3-large` vector, behind a new `MetadataCandidateScorer` interface that keeps the door open for additional scorer implementations (LLM, reranker).

**Architecture:** Introduce a new `ai.MetadataCandidateScorer` interface with a single implementation `ai.EmbeddingScorer` that wraps the existing `*ai.EmbeddingClient` + `*database.EmbeddingStore`. Refactor `scoreOneResult` into a base-score computation (pluggable) plus a "non-base adjustments" function (compilation penalty, length penalty, rich-metadata bonus) that runs regardless of tier. Wire the scorer into `metadata_fetch_service.go` with an explicit fallback chain: embedding → F1, so any scorer failure drops through to the existing tested code path and the search never breaks because of scorer problems.

**Tech Stack:** Go 1.24, existing OpenAI embeddings client (`github.com/openai/openai-go` v1.12.0), existing `database.EmbeddingStore` SQLite sidecar, `github.com/stretchr/testify` for tests.

**Spec:** `docs/superpowers/specs/2026-04-10-metadata-candidate-scoring-design.md` (read the "Scorer Interface", "Scoring Pipeline Changes", and "PR Split" sections before starting)

**Reference context about the existing code:**

- `scoreOneResult` lives at `internal/server/metadata_fetch_service.go:1000-1062`. It computes F1 on `significantWords` token sets, applies a compilation penalty, a length penalty, and a rich-metadata bonus. It's called from three places: the main search loop (`:1686`), the ASIN direct-lookup path (`:1771`), and `bestTitleMatchWithContext` (`:1108`).
- `bestTitleMatchWithContext` lives at `:1094-1151`. It iterates results, calls `scoreOneResult`, applies author/narrator/audiobook bonuses, and returns the single highest-scoring result if it clears `minScore = 0.35`.
- The main search loop lives in `SearchMetadataForBook` starting at `:1550`. The F1 score is computed at `:1686`, then author/narrator/series/audiobook bonuses stack on top at `:1693-1731`, then the result is pushed into `candidates` at `:1733`.
- `MetadataFetchService` struct is at `:33-40` with the constructor at `:47`. It follows a setter-injection pattern — see `SetOLStore`, `SetDedupEngine`, `SetActivityService`, `SetISBNEnrichment`.
- The embedding client is constructed at `internal/server/server.go:828` inside the existing dedup init block. The `database.EmbeddingStore` is constructed at `:822`. These are the exact handles the scorer will reuse.
- `database.CosineSimilarity(a, b []float32) float32` already exists in `internal/database/embedding_store.go` — use it, do not write your own.
- `ai.BuildEmbeddingText(entityType, title, author, narrator string) string` already exists in `internal/ai/embedding_client.go` — use it, do not build embedding text inline.
- `ai.TextHash(text string) string` is also in the embedding client.
- All files in this project need versioned headers. Format:
```go
// file: path/to/file.go
// version: X.Y.Z
// guid: (new UUID for new files; bump minor for modifications)
```

---

### Task 1: Create the scorer interface and shared types

**Files:**
- Create: `internal/ai/metadata_scorer.go`
- Create: `internal/ai/metadata_scorer_test.go`

- [ ] **Step 1: Write the interface contract test first**

Create `internal/ai/metadata_scorer_test.go`:

```go
// file: internal/ai/metadata_scorer_test.go
// version: 1.0.0
// guid: 9c4a2e1d-5f68-4b70-9a3c-8e1f5d7b2c04

package ai

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubScorer is a tiny MetadataCandidateScorer implementation used to lock in
// the interface shape. It always returns 0.5 for every candidate.
type stubScorer struct{ name string }

func (s *stubScorer) Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error) {
	out := make([]float64, len(cands))
	for i := range out {
		out[i] = 0.5
	}
	return out, nil
}

func (s *stubScorer) Name() string { return s.name }

// TestMetadataCandidateScorer_InterfaceShape verifies that a concrete
// implementation satisfies the interface and can be assigned to the type.
// This is a compile-time check in disguise; if the interface changes in a
// breaking way, this test stops compiling.
func TestMetadataCandidateScorer_InterfaceShape(t *testing.T) {
	var scorer MetadataCandidateScorer = &stubScorer{name: "stub"}
	assert.Equal(t, "stub", scorer.Name())

	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{
		{Title: "Dune"},
		{Title: "Dune Messiah"},
	})
	require.NoError(t, err)
	assert.Len(t, scores, 2)
	for _, s := range scores {
		assert.GreaterOrEqual(t, s, 0.0)
		assert.LessOrEqual(t, s, 1.0)
	}
}

// TestMetadataCandidateScorer_EmptyCandidates verifies the documented
// "nil candidates, nil score slice" behavior.
func TestMetadataCandidateScorer_EmptyCandidates(t *testing.T) {
	scorer := &stubScorer{name: "stub"}
	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, nil)
	require.NoError(t, err)
	assert.Nil(t, scores)
}
```

Note: `stubScorer` as written returns a non-nil slice even for empty input. Adjust it inside the test so `TestMetadataCandidateScorer_EmptyCandidates` passes — return `nil, nil` when `len(cands) == 0`. Add this branch to `stubScorer.Score`:

```go
func (s *stubScorer) Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	out := make([]float64, len(cands))
	for i := range out {
		out[i] = 0.5
	}
	return out, nil
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer && go test ./internal/ai/ -run TestMetadataCandidateScorer -v -count=1`
Expected: FAIL with `undefined: MetadataCandidateScorer` / `undefined: Query` / `undefined: Candidate`.

- [ ] **Step 3: Create the interface and types**

Create `internal/ai/metadata_scorer.go`:

```go
// file: internal/ai/metadata_scorer.go
// version: 1.0.0
// guid: 3b8e1c5f-7a24-4d06-91e8-f5a2b9c41d37

package ai

import "context"

// MetadataCandidateScorer ranks candidate metadata search results by how well
// each one matches a query book. It is the abstraction point that lets the
// metadata fetch pipeline swap between embedding cosine similarity, a chat
// LLM judgment, a cross-encoder reranker, or a simple token-overlap fallback
// without the caller knowing which implementation is in use.
//
// Contract for all implementations:
//
//   - Score must return exactly one score per input candidate, in the same
//     order as the input slice.
//   - Scores must be clamped to [0.0, 1.0] where 1.0 means "definitely the
//     same book" and 0.0 means "definitely not."
//   - Implementations must NEVER return a partial result with a nil error.
//     Any failure (API error, missing dependency, empty query) returns
//     (nil, err) so the caller can fall back to the next tier.
//   - An empty cands slice returns (nil, nil) — not an error, just nothing
//     to score.
//   - Name returns a short identifier used in logs and UI badges
//     ("embedding", "llm:gpt-5-mini", "rerank:cohere-v3"). It must be stable
//     across the lifetime of a process so logs stay searchable.
type MetadataCandidateScorer interface {
	Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error)
	Name() string
}

// Query describes the book the caller is searching metadata for. BookID is
// an optional fast-path — if set and the scorer has a pre-computed vector
// for that book in the EmbeddingStore, it can skip the cost of re-embedding
// the query. Scorers that do not have an embedding store should just ignore
// BookID.
type Query struct {
	BookID   string
	Title    string
	Author   string
	Narrator string
}

// Candidate is one search result being scored. Fields mirror the identity
// slice of metadata.BookMetadata — title, author, narrator — because those
// are the three fields that matter for ranking and including more (publisher,
// description, cover URL) just inflates token counts without improving the
// signal.
type Candidate struct {
	Title    string
	Author   string
	Narrator string
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/ai/ -run TestMetadataCandidateScorer -v -count=1`
Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ai/metadata_scorer.go internal/ai/metadata_scorer_test.go
git commit -m "feat(ai): MetadataCandidateScorer interface with Query and Candidate types"
```

---

### Task 2: Implement EmbeddingScorer

**Files:**
- Create: `internal/ai/embedding_scorer.go`
- Create: `internal/ai/embedding_scorer_test.go`

**Design recap:** `EmbeddingScorer` holds an `*EmbeddingClient` and an optional `*database.EmbeddingStore`. Its `Score` method:

1. If `query.BookID != ""` and `store != nil`, try to load a stored vector for that book. On hit, use it as the query vector and skip embedding.
2. Otherwise, build a query text via `BuildEmbeddingText("book", ...)` and call `client.EmbedOne`.
3. Build candidate texts the same way, batch-embed them via `client.EmbedBatch`.
4. For each candidate vector, compute `max(0, cosine(queryVec, candVec))` using `database.CosineSimilarity` and return the scores.

Tests use a fake `embeddingAPI` that returns deterministic one-hot vectors so cosine math is predictable without touching the real API.

- [ ] **Step 1: Write the failing tests**

Create `internal/ai/embedding_scorer_test.go`:

```go
// file: internal/ai/embedding_scorer_test.go
// version: 1.0.0
// guid: 6d52f1a8-3c79-4e05-89b4-2a0c8f7d4e61

package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEmbedAPI is an in-process stand-in for the OpenAI embeddings endpoint.
// Tests install a textToVec function that maps a text to a deterministic
// vector, so cosine math is predictable without real API calls.
type fakeEmbedAPI struct {
	textToVec func(string) []float32
	embedOne  int // call counts for assertions
	embedBatch int
	failNext  error
}

func (f *fakeEmbedAPI) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	f.embedOne++
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	return f.textToVec(text), nil
}

func (f *fakeEmbedAPI) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	f.embedBatch++
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = f.textToVec(t)
	}
	return out, nil
}

// oneHotByPrefix returns a 4-dim vector where the hot index depends on the
// first character of the text. Two texts sharing a first letter get identical
// vectors (cosine = 1.0), different letters get orthogonal vectors
// (cosine = 0.0). This makes the test assertions trivial to read.
func oneHotByPrefix(text string) []float32 {
	if text == "" {
		return []float32{0, 0, 0, 0}
	}
	switch text[0] {
	case 'a', 'A':
		return []float32{1, 0, 0, 0}
	case 'b', 'B':
		return []float32{0, 1, 0, 0}
	case 'c', 'C':
		return []float32{0, 0, 1, 0}
	default:
		return []float32{0, 0, 0, 1}
	}
}

func newFakeScorer(t *testing.T) (*EmbeddingScorer, *fakeEmbedAPI) {
	t.Helper()
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, nil)
	return scorer, api
}

func TestEmbeddingScorer_Name(t *testing.T) {
	scorer, _ := newFakeScorer(t)
	assert.Equal(t, "embedding", scorer.Name())
}

func TestEmbeddingScorer_EmptyCandidates(t *testing.T) {
	scorer, api := newFakeScorer(t)
	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, nil)
	require.NoError(t, err)
	assert.Nil(t, scores)
	assert.Equal(t, 0, api.embedOne, "empty candidates should not trigger query embedding")
	assert.Equal(t, 0, api.embedBatch, "empty candidates should not trigger candidate batch")
}

func TestEmbeddingScorer_CosineRanking(t *testing.T) {
	scorer, api := newFakeScorer(t)

	// Query title "Dune" starts with 'd' → hot index 3 (the default branch).
	// Candidates use known prefixes that give orthogonal or identical vectors.
	scores, err := scorer.Score(context.Background(), Query{Title: "Dune by Frank Herbert"}, []Candidate{
		{Title: "abyss", Author: "X"},       // different prefix → cosine 0
		{Title: "different", Author: "X"},   // 'd' prefix → same vector as query → cosine 1
		{Title: "boring", Author: "X"},      // different prefix → cosine 0
	})
	require.NoError(t, err)
	require.Len(t, scores, 3)
	assert.InDelta(t, 0.0, scores[0], 0.01, "candidate 0 should be orthogonal to query")
	assert.InDelta(t, 1.0, scores[1], 0.01, "candidate 1 should match query perfectly")
	assert.InDelta(t, 0.0, scores[2], 0.01, "candidate 2 should be orthogonal to query")

	assert.Equal(t, 1, api.embedOne, "query should be embedded once")
	assert.Equal(t, 1, api.embedBatch, "candidates should be batch-embedded once")
}

func TestEmbeddingScorer_ClampsNegativeCosine(t *testing.T) {
	// Force an opposite-direction vector to produce cosine = -1, verify it
	// clamps to 0.
	api := &fakeEmbedAPI{
		textToVec: func(text string) []float32 {
			if text[0] == 'q' {
				return []float32{1, 0, 0, 0}
			}
			return []float32{-1, 0, 0, 0}
		},
	}
	scorer := NewEmbeddingScorerWithAPI(api, nil)
	scores, err := scorer.Score(context.Background(), Query{Title: "query"}, []Candidate{
		{Title: "other"},
	})
	require.NoError(t, err)
	assert.Equal(t, 0.0, scores[0], "negative cosine should clamp to 0")
}

func TestEmbeddingScorer_QueryEmbedError(t *testing.T) {
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix, failNext: errors.New("boom")}
	scorer := NewEmbeddingScorerWithAPI(api, nil)

	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{
		{Title: "Dune"},
	})
	require.Error(t, err)
	assert.Nil(t, scores, "partial results are never returned")
}

func TestEmbeddingScorer_CandidateBatchError(t *testing.T) {
	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, nil)

	// First call succeeds (query), next call (batch) fails.
	_, _ = scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{{Title: "Dune"}})
	api.failNext = errors.New("batch failure")

	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, []Candidate{
		{Title: "Dune"},
		{Title: "Dune Messiah"},
	})
	require.Error(t, err)
	assert.Nil(t, scores)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ai/ -run TestEmbeddingScorer -v -count=1`
Expected: FAIL with `undefined: EmbeddingScorer` / `undefined: NewEmbeddingScorerWithAPI`.

- [ ] **Step 3: Implement EmbeddingScorer**

Create `internal/ai/embedding_scorer.go`:

```go
// file: internal/ai/embedding_scorer.go
// version: 1.0.0
// guid: a7b1c4e9-2d68-4f05-83ab-5c1e9f4d8b72

package ai

import (
	"context"
	"fmt"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

// embeddingAPI is the minimal surface EmbeddingScorer needs from an embedding
// client. It exists purely so tests can inject a fake without spinning up the
// real OpenAI client. Production code always wires a real *EmbeddingClient
// here via NewEmbeddingScorer.
type embeddingAPI interface {
	EmbedOne(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}

// EmbeddingScorer ranks metadata candidates by cosine similarity between the
// query book's embedding and each candidate's embedding. When a BookID is
// supplied and the EmbeddingStore has a cached vector for that book, the
// query embedding step is skipped entirely — this is the common case in
// production since all library books are embedded by the initial backfill.
type EmbeddingScorer struct {
	api   embeddingAPI
	store *database.EmbeddingStore // optional; enables BookID fast-path
}

// NewEmbeddingScorer wraps a real *EmbeddingClient for production use.
// A nil store is allowed and disables the BookID fast-path — the scorer
// will always embed the query text on the fly.
func NewEmbeddingScorer(client *EmbeddingClient, store *database.EmbeddingStore) *EmbeddingScorer {
	return &EmbeddingScorer{api: client, store: store}
}

// NewEmbeddingScorerWithAPI is the test seam. Do not call this from
// production code.
func NewEmbeddingScorerWithAPI(api embeddingAPI, store *database.EmbeddingStore) *EmbeddingScorer {
	return &EmbeddingScorer{api: api, store: store}
}

// Name implements MetadataCandidateScorer.
func (s *EmbeddingScorer) Name() string { return "embedding" }

// Score implements MetadataCandidateScorer.
func (s *EmbeddingScorer) Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	if s.api == nil {
		return nil, fmt.Errorf("embedding scorer: no embedding API configured")
	}

	qVec, err := s.queryVector(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("embedding scorer: query vector: %w", err)
	}

	texts := make([]string, len(cands))
	for i, c := range cands {
		texts[i] = BuildEmbeddingText("book", c.Title, c.Author, c.Narrator)
	}

	candVecs, err := s.api.EmbedBatch(ctx, texts)
	if err != nil {
		return nil, fmt.Errorf("embedding scorer: candidate batch: %w", err)
	}
	if len(candVecs) != len(cands) {
		return nil, fmt.Errorf("embedding scorer: batch returned %d vectors for %d candidates",
			len(candVecs), len(cands))
	}

	scores := make([]float64, len(cands))
	for i, cv := range candVecs {
		cos := database.CosineSimilarity(qVec, cv)
		if cos < 0 {
			cos = 0
		}
		scores[i] = float64(cos)
	}
	return scores, nil
}

// queryVector returns the vector for the query book, preferring the
// EmbeddingStore fast-path when a BookID is set and a cached vector exists,
// and falling back to a live API embed otherwise.
func (s *EmbeddingScorer) queryVector(ctx context.Context, q Query) ([]float32, error) {
	if q.BookID != "" && s.store != nil {
		if existing, err := s.store.Get("book", q.BookID); err == nil && existing != nil && len(existing.Vector) > 0 {
			return existing.Vector, nil
		}
	}
	text := BuildEmbeddingText("book", q.Title, q.Author, q.Narrator)
	return s.api.EmbedOne(ctx, text)
}
```

Note the type assertion: `*EmbeddingClient` must satisfy `embeddingAPI`. It does — `EmbedOne` and `EmbedBatch` are both defined on the real client already (see `internal/ai/embedding_client.go`). This is a free compile-time check.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ai/ -run TestEmbeddingScorer -v -count=1`
Expected: all 6 tests PASS.

- [ ] **Step 5: Run the full ai package tests to make sure nothing regressed**

Run: `go test ./internal/ai/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ai/embedding_scorer.go internal/ai/embedding_scorer_test.go
git commit -m "feat(ai): EmbeddingScorer with BookID fast-path and cosine ranking"
```

---

### Task 3: Add BookID fast-path test with a real EmbeddingStore

The previous task tested the hot-path with a nil store. This task adds coverage for the fast-path where a cached book vector is loaded from a real (in-memory) EmbeddingStore, skipping the query embed call.

**Files:**
- Modify: `internal/ai/embedding_scorer_test.go`

- [ ] **Step 1: Add the fast-path test**

Append to `internal/ai/embedding_scorer_test.go`:

```go
func TestEmbeddingScorer_BookIDFastPath(t *testing.T) {
	// Spin up a real temp-dir EmbeddingStore and seed a known vector for a
	// specific book ID. Verify the scorer uses that vector instead of calling
	// EmbedOne.
	tmpDir := t.TempDir()
	store, err := database.NewEmbeddingStore(tmpDir + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	// Seed book BOOK_A with a one-hot 'a'-style vector so it matches
	// candidates whose text starts with 'a'.
	require.NoError(t, store.Upsert(database.Embedding{
		EntityType: "book",
		EntityID:   "BOOK_A",
		TextHash:   "hash-a",
		Vector:     []float32{1, 0, 0, 0},
		Model:      "text-embedding-3-large",
	}))

	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, store)

	scores, err := scorer.Score(context.Background(),
		Query{BookID: "BOOK_A", Title: "whatever the title is"},
		[]Candidate{
			{Title: "abyss"},      // 'a' → matches seeded vector
			{Title: "different"},  // default → orthogonal
		},
	)
	require.NoError(t, err)
	require.Len(t, scores, 2)
	assert.InDelta(t, 1.0, scores[0], 0.01)
	assert.InDelta(t, 0.0, scores[1], 0.01)

	assert.Equal(t, 0, api.embedOne, "BookID fast-path should skip query embedding")
	assert.Equal(t, 1, api.embedBatch, "candidates are still batch-embedded")
}

func TestEmbeddingScorer_BookIDMissFallsBackToEmbed(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := database.NewEmbeddingStore(tmpDir + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	// Store has no entry for BOOK_MISSING.

	api := &fakeEmbedAPI{textToVec: oneHotByPrefix}
	scorer := NewEmbeddingScorerWithAPI(api, store)

	_, err = scorer.Score(context.Background(),
		Query{BookID: "BOOK_MISSING", Title: "Dune"},
		[]Candidate{{Title: "Dune"}},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, api.embedOne, "store miss should fall back to EmbedOne")
}
```

Add `"github.com/jdfalk/audiobook-organizer/internal/database"` to the test file imports if the goimports step doesn't do it automatically.

- [ ] **Step 2: Run the tests**

Run: `go test ./internal/ai/ -run TestEmbeddingScorer -v -count=1`
Expected: 8 tests PASS (6 from task 2 + 2 new ones).

- [ ] **Step 3: Commit**

```bash
git add internal/ai/embedding_scorer_test.go
git commit -m "test(ai): EmbeddingScorer BookID fast-path against real EmbeddingStore"
```

---

### Task 4: Add scorer config keys

**Files:**
- Modify: `internal/config/config.go`

**Scope:** three new config keys for PR 1 only. PR 2 adds three more later.

- [ ] **Step 1: Bump config version and add fields**

In `internal/config/config.go`, first bump the header version (search for `// version:` near the top of the file, increment the minor).

Find the embedding-related fields added in PR #203 (grep for `EmbeddingEnabled`). They look like:

```go
EmbeddingEnabled         bool    `json:"embedding_enabled"`
EmbeddingModel           string  `json:"embedding_model"`
DedupBookHighThreshold   float64 `json:"dedup_book_high_threshold"`
DedupBookLowThreshold    float64 `json:"dedup_book_low_threshold"`
DedupAuthorHighThreshold float64 `json:"dedup_author_high_threshold"`
DedupAuthorLowThreshold  float64 `json:"dedup_author_low_threshold"`
DedupAutoMergeEnabled    bool    `json:"dedup_auto_merge_enabled"`
```

Append three new fields right after `DedupAutoMergeEnabled`:

```go
// Metadata candidate scoring (PR1)
MetadataEmbeddingScoringEnabled bool    `json:"metadata_embedding_scoring_enabled"` // default true
MetadataEmbeddingMinScore       float64 `json:"metadata_embedding_min_score"`        // default 0.50
MetadataEmbeddingBestMatchMin   float64 `json:"metadata_embedding_best_match_min"`   // default 0.70
```

- [ ] **Step 2: Add defaults to the AppConfig initializer**

Find the viper-backed initializer that sets `EmbeddingEnabled = true` — grep for `EmbeddingEnabled:         true` or `AppConfig.EmbeddingEnabled = true`. There are two places in `config.go` that set embedding defaults (one around line 848, one in `ResetToDefaults` near line 1012). Add the new fields in both places.

Main initializer block (look for the PR #189 comment "Embedding-based dedup (defaults used unless DB settings override)" or similar). Append:

```go
AppConfig.MetadataEmbeddingScoringEnabled = true
AppConfig.MetadataEmbeddingMinScore = 0.50
AppConfig.MetadataEmbeddingBestMatchMin = 0.70
```

`ResetToDefaults` block — add the same three lines in the same spot. If the surrounding style uses struct-literal form (`EmbeddingEnabled: true,`), match it.

- [ ] **Step 3: Verify build**

Run: `go build ./internal/config/`
Expected: clean build, no errors.

- [ ] **Step 4: Verify nothing else in the project regressed**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(config): metadata embedding scoring config keys (enabled, thresholds)"
```

---

### Task 5: Load new config keys from the settings DB

**Files:**
- Modify: `internal/config/persistence.go`

PebbleDB stores user-editable config in a settings table. `applySetting(key, value, typ)` is a big switch that maps DB row keys to `AppConfig` fields. New fields only get read from the DB if they have a case in this switch.

- [ ] **Step 1: Find the applySetting switch**

Run: `grep -n "case \"embedding_enabled\"" internal/config/persistence.go`

Note the line number. The surrounding switch handles all existing embedding/dedup keys — add the three new ones in the same block, immediately after `case "dedup_auto_merge_enabled":`.

- [ ] **Step 2: Add the three new cases**

For reference, the existing `dedup_auto_merge_enabled` case probably looks like:

```go
case "dedup_auto_merge_enabled":
    if v, err := strconv.ParseBool(value); err == nil {
        AppConfig.DedupAutoMergeEnabled = v
    }
```

Append, matching the exact pattern used by the surrounding cases (strconv.ParseFloat for floats, strconv.ParseBool for bools):

```go
case "metadata_embedding_scoring_enabled":
    if v, err := strconv.ParseBool(value); err == nil {
        AppConfig.MetadataEmbeddingScoringEnabled = v
    }
case "metadata_embedding_min_score":
    if v, err := strconv.ParseFloat(value, 64); err == nil {
        AppConfig.MetadataEmbeddingMinScore = v
    }
case "metadata_embedding_best_match_min":
    if v, err := strconv.ParseFloat(value, 64); err == nil {
        AppConfig.MetadataEmbeddingBestMatchMin = v
    }
```

If the surrounding cases use a different idiom (e.g., a helper like `setBoolFromSetting`), match that instead.

- [ ] **Step 3: Verify build**

Run: `go build ./internal/config/`
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add internal/config/persistence.go
git commit -m "feat(config): load metadata scoring keys from settings DB"
```

---

### Task 6: Refactor scoreOneResult into base + non-base halves

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

**Goal:** split the existing function so the F1 computation is swappable while the "compilation penalty, length penalty, rich-metadata bonus" tail stays shared across all scoring tiers. Behavior must be **bit-for-bit identical** to the current F1 path so existing tests keep passing.

Current structure (`metadata_fetch_service.go:1000-1062`):

```go
func scoreOneResult(r metadata.BookMetadata, searchWords map[string]bool) float64 {
    // 1. Compute F1 from word-set overlap
    // 2. Compilation penalty (multiplier)
    // 3. Length penalty (multiplier)
    // 4. Rich-metadata bonus (additive)
    // return f1 + bonus
}
```

Target structure: one function computes the F1 base score only, another function applies the non-base adjustments to any base score. `scoreOneResult` becomes a thin orchestrator that calls both in sequence. The three existing call sites (`:1108`, `:1686`, `:1771`) stay on `scoreOneResult` and see no behavior change.

- [ ] **Step 1: Write the regression test first**

Create a new test file `internal/server/metadata_scoring_refactor_test.go`:

```go
// file: internal/server/metadata_scoring_refactor_test.go
// version: 1.0.0
// guid: e8d1a6c4-9b23-4057-86fa-0f3e7c5d2b90

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jdfalk/audiobook-organizer/internal/metadata"
)

// TestScoreOneResult_RefactorEquivalence locks in the current output of
// scoreOneResult against representative inputs so the split into base +
// non-base halves can't accidentally change the combined result.
//
// These golden values come from the pre-refactor implementation — run the
// current code once to capture them, then freeze them here.
func TestScoreOneResult_RefactorEquivalence(t *testing.T) {
	searchWords := significantWords("The Way of Kings")

	cases := []struct {
		name   string
		input  metadata.BookMetadata
		minExp float64
		maxExp float64
	}{
		{
			name: "exact title match with rich metadata",
			input: metadata.BookMetadata{
				Title:       "The Way of Kings",
				Description: "long description",
				CoverURL:    "https://example/cover.jpg",
				Narrator:    "Kate Reading",
				ISBN:        "9780765326355",
			},
			// F1 = 1.0, full rich-metadata bonus (+0.15), no penalties
			minExp: 1.10, maxExp: 1.20,
		},
		{
			name: "compilation penalty fires",
			input: metadata.BookMetadata{
				Title: "The Way of Kings (Stormlight Archive Omnibus)",
			},
			// Contains "omnibus" → compilation multiplier 0.15 → tiny score
			minExp: 0.0, maxExp: 0.20,
		},
		{
			name: "unrelated title",
			input: metadata.BookMetadata{
				Title: "Completely Different Book",
			},
			// F1 ~ 0 (no overlap) + no bonus
			minExp: 0.0, maxExp: 0.05,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scoreOneResult(tc.input, searchWords)
			assert.GreaterOrEqual(t, got, tc.minExp, "score below expected range")
			assert.LessOrEqual(t, got, tc.maxExp, "score above expected range")
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it passes on the current implementation**

Run: `go test ./internal/server/ -run TestScoreOneResult_RefactorEquivalence -v -count=1`
Expected: PASS (we're locking in current behavior).

If it fails, tighten the expected ranges to match the actual current output. This test is your safety net for the refactor.

- [ ] **Step 3: Split scoreOneResult into two halves**

In `internal/server/metadata_fetch_service.go`, bump the file version header (currently 4.39.0, make it 4.40.0).

Replace the existing `scoreOneResult` function (`:1000-1062`) with three functions:

```go
// computeF1Base returns just the F1 token-overlap portion of the score, with
// no penalties or bonuses applied. It's the "base score" contribution from
// the significantWords pathway, extracted so alternative scorers (embedding,
// LLM, reranker) can supply their own base score and reuse the shared
// non-base adjustment function.
func computeF1Base(r metadata.BookMetadata, searchWords map[string]bool) float64 {
	resultWords := significantWords(r.Title)
	if len(searchWords) == 0 || len(resultWords) == 0 {
		return 0
	}

	recallHits := 0
	for w := range searchWords {
		if resultWords[w] {
			recallHits++
		}
	}
	recall := float64(recallHits) / float64(len(searchWords))

	precHits := 0
	for w := range resultWords {
		if searchWords[w] {
			precHits++
		}
	}
	precision := float64(precHits) / float64(len(resultWords))

	if recall+precision == 0 {
		return 0
	}
	return 2 * recall * precision / (recall + precision)
}

// applyNonBaseAdjustments applies the compilation penalty, length penalty,
// and rich-metadata bonus to a base score. These adjustments are meaningful
// regardless of which scorer tier produced the base score and are applied
// identically on every path.
//
// `baseWordCount` is the number of significant words in the search title —
// used for the length penalty. Pass 0 to disable the length penalty (e.g.
// when the length ratio is meaningless for a non-token-overlap scorer).
func applyNonBaseAdjustments(baseScore float64, r metadata.BookMetadata, baseWordCount int) float64 {
	score := baseScore

	// Compilation penalty
	if isCompilation(r.Title) {
		score *= 0.15
	}

	// Length penalty: penalise results that are much longer than the search.
	// Only applies when baseWordCount > 0 (the F1 path).
	if baseWordCount > 0 {
		resultWords := significantWords(r.Title)
		nSearch := float64(baseWordCount)
		nResult := float64(len(resultWords))
		if nResult > 1.5*nSearch {
			score *= (1.5 * nSearch) / nResult
		}
	}

	// Rich-metadata bonus (capped at +0.15, additive)
	bonus := 0.0
	if r.Description != "" {
		bonus += 0.05
	}
	if r.CoverURL != "" {
		bonus += 0.05
	}
	if r.Narrator != "" {
		bonus += 0.05
	}
	if r.ISBN != "" {
		bonus += 0.05
	}
	if bonus > 0.15 {
		bonus = 0.15
	}

	return score + bonus
}

// scoreOneResult preserves the pre-refactor signature and behavior. It
// computes the F1 base score and applies non-base adjustments in one call.
// Existing callers are unchanged.
func scoreOneResult(r metadata.BookMetadata, searchWords map[string]bool) float64 {
	base := computeF1Base(r, searchWords)
	if base == 0 {
		return 0
	}
	return applyNonBaseAdjustments(base, r, len(searchWords))
}
```

Two important details:

1. The short-circuit `if len(resultWords) == 0 { return 0 }` used to live inside `scoreOneResult`. It's now inside `computeF1Base`, which returns 0. In the new `scoreOneResult`, `if base == 0 { return 0 }` preserves the original's behavior of skipping the rich-metadata bonus when there was no F1 contribution at all. This matches the pre-refactor code exactly — the old function also returned `f1 + bonus` after the penalties, but with F1 = 0 and no penalty effects, the old return would be `0 + bonus`. We're tightening this: if F1 was zero, we now return 0 outright. **Verify that the regression test still passes with this change** — if the old code's "F1=0 but rich metadata gives 0.15" edge case mattered for any test, we need to keep the original behavior. Run the test in step 5 and check.

2. The length penalty reads `significantWords(r.Title)` twice in the new layout (once in `computeF1Base`, once in `applyNonBaseAdjustments`). That's fine — it's pure and cheap. If profiling ever shows it mattering, the caller can cache it and pass it in.

- [ ] **Step 4: Run the regression test**

Run: `go test ./internal/server/ -run TestScoreOneResult_RefactorEquivalence -v -count=1`
Expected: PASS.

If any case fails, the `base == 0` short-circuit changed behavior for that edge case. Revise `scoreOneResult` to match the old behavior:

```go
func scoreOneResult(r metadata.BookMetadata, searchWords map[string]bool) float64 {
	base := computeF1Base(r, searchWords)
	return applyNonBaseAdjustments(base, r, len(searchWords))
}
```

(Remove the `if base == 0 { return 0 }` guard.) Re-run the test.

- [ ] **Step 5: Run all metadata fetch tests**

Run: `go test ./internal/server/ -run "TestMetadata|TestScore|TestBestTitle" -count=1 -timeout 60s`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/metadata_fetch_service.go internal/server/metadata_scoring_refactor_test.go
git commit -m "refactor: split scoreOneResult into computeF1Base + applyNonBaseAdjustments"
```

---

### Task 7: Add the scorer field and setter to MetadataFetchService

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

- [ ] **Step 1: Add the field and setter**

In `metadata_fetch_service.go`, find the `MetadataFetchService` struct (`:33-40`) and append one new field right after `dedupEngine`:

```go
type MetadataFetchService struct {
	db              database.Store
	olStore         *openlibrary.OLStore
	overrideSources []metadata.MetadataSource // for testing
	isbnEnrichment  *ISBNEnrichmentService
	activityService *ActivityService
	dedupEngine     *DedupEngine
	metadataScorer  ai.MetadataCandidateScorer // optional; nil = fallback to F1
}
```

Add the `"github.com/jdfalk/audiobook-organizer/internal/ai"` import at the top of the file if it's not already there (grep first; the file may already import it indirectly).

Right after `SetDedupEngine` (`:57-59`), add:

```go
// SetMetadataScorer injects the pluggable metadata candidate scorer. A nil
// scorer (or a scorer that returns errors at runtime) makes the search
// pipeline fall back to the pre-existing significantWords F1 path, so this
// method is safe to leave unset.
func (mfs *MetadataFetchService) SetMetadataScorer(scorer ai.MetadataCandidateScorer) {
	mfs.metadataScorer = scorer
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./internal/server/`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/server/metadata_fetch_service.go
git commit -m "feat(metadata): add MetadataCandidateScorer field + setter on MetadataFetchService"
```

---

### Task 8: Implement the scoreBaseCandidates helper

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

This is the tier-selection logic. It tries the injected `metadataScorer` first, then falls back to the F1 path. Output is one score per input result, aligned to input order.

- [ ] **Step 1: Write the failing test**

Append to `internal/server/metadata_scoring_refactor_test.go`:

```go
import (
	"context"
	"errors"

	"github.com/jdfalk/audiobook-organizer/internal/ai"
	"github.com/jdfalk/audiobook-organizer/internal/config"
	"github.com/jdfalk/audiobook-organizer/internal/database"
)

// scorerStub is a controllable MetadataCandidateScorer for tests.
type scorerStub struct {
	name      string
	scores    []float64
	err       error
	callCount int
}

func (s *scorerStub) Score(ctx context.Context, q ai.Query, cands []ai.Candidate) ([]float64, error) {
	s.callCount++
	if s.err != nil {
		return nil, s.err
	}
	return s.scores, nil
}

func (s *scorerStub) Name() string { return s.name }

func TestScoreBaseCandidates_EmbeddingTierUsed(t *testing.T) {
	mfs := &MetadataFetchService{metadataScorer: &scorerStub{
		name:   "embedding",
		scores: []float64{0.9, 0.7, 0.3},
	}}
	prev := config.AppConfig.MetadataEmbeddingScoringEnabled
	config.AppConfig.MetadataEmbeddingScoringEnabled = true
	defer func() { config.AppConfig.MetadataEmbeddingScoringEnabled = prev }()

	results := []metadata.BookMetadata{
		{Title: "A"}, {Title: "B"}, {Title: "C"},
	}
	searchWords := significantWords("A")
	book := &database.Book{ID: "BOOK1", Title: "A"}

	scores, tier := mfs.scoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "embedding", tier)
	assert.Equal(t, []float64{0.9, 0.7, 0.3}, scores)
}

func TestScoreBaseCandidates_ConfigDisabledFallsBackToF1(t *testing.T) {
	mfs := &MetadataFetchService{metadataScorer: &scorerStub{
		name:   "embedding",
		scores: []float64{1.0, 1.0, 1.0},
	}}
	prev := config.AppConfig.MetadataEmbeddingScoringEnabled
	config.AppConfig.MetadataEmbeddingScoringEnabled = false
	defer func() { config.AppConfig.MetadataEmbeddingScoringEnabled = prev }()

	results := []metadata.BookMetadata{
		{Title: "The Way of Kings"},
		{Title: "Completely Unrelated Book"},
	}
	searchWords := significantWords("The Way of Kings")
	book := &database.Book{ID: "BOOK1", Title: "The Way of Kings"}

	scores, tier := mfs.scoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "f1", tier)
	assert.Len(t, scores, 2)
	// First result is a full-title match → F1 = 1.0
	assert.InDelta(t, 1.0, scores[0], 0.01)
	// Second is unrelated → F1 ~ 0
	assert.InDelta(t, 0.0, scores[1], 0.1)
}

func TestScoreBaseCandidates_ScorerErrorFallsBackToF1(t *testing.T) {
	stub := &scorerStub{name: "embedding", err: errors.New("api boom")}
	mfs := &MetadataFetchService{metadataScorer: stub}
	prev := config.AppConfig.MetadataEmbeddingScoringEnabled
	config.AppConfig.MetadataEmbeddingScoringEnabled = true
	defer func() { config.AppConfig.MetadataEmbeddingScoringEnabled = prev }()

	results := []metadata.BookMetadata{{Title: "The Way of Kings"}}
	searchWords := significantWords("The Way of Kings")
	book := &database.Book{ID: "BOOK1", Title: "The Way of Kings"}

	scores, tier := mfs.scoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "f1", tier, "scorer error should fall back to F1 tier")
	assert.Equal(t, 1, stub.callCount, "scorer should be called exactly once")
	assert.InDelta(t, 1.0, scores[0], 0.01)
}

func TestScoreBaseCandidates_NilScorerFallsBackSilently(t *testing.T) {
	mfs := &MetadataFetchService{metadataScorer: nil}
	prev := config.AppConfig.MetadataEmbeddingScoringEnabled
	config.AppConfig.MetadataEmbeddingScoringEnabled = true
	defer func() { config.AppConfig.MetadataEmbeddingScoringEnabled = prev }()

	results := []metadata.BookMetadata{{Title: "The Way of Kings"}}
	searchWords := significantWords("The Way of Kings")
	book := &database.Book{ID: "BOOK1", Title: "The Way of Kings"}

	scores, tier := mfs.scoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "f1", tier)
	assert.Len(t, scores, 1)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/server/ -run TestScoreBaseCandidates -v -count=1`
Expected: FAIL with `mfs.scoreBaseCandidates undefined`.

- [ ] **Step 3: Implement scoreBaseCandidates**

Add this method to `metadata_fetch_service.go`, right after `applyNonBaseAdjustments`:

```go
// scoreBaseCandidates picks the highest-available base scorer tier and
// returns one base score per input result, aligned to input order, along
// with a short tier name for logs and UI badges ("embedding", "f1", ...).
//
// The fallback chain is:
//   1. If MetadataEmbeddingScoringEnabled AND a scorer is injected AND the
//      scorer succeeds → use those scores. Tier = scorer.Name().
//   2. Otherwise, compute F1 inline. Tier = "f1".
//
// Any scorer error is logged and falls through to the F1 tier. The search
// path must never fail because of a scorer problem — F1 is always reachable
// as a last resort since it only depends on the in-memory result data.
func (mfs *MetadataFetchService) scoreBaseCandidates(
	ctx context.Context,
	book *database.Book,
	results []metadata.BookMetadata,
	searchWords map[string]bool,
) ([]float64, string) {
	if config.AppConfig.MetadataEmbeddingScoringEnabled && mfs.metadataScorer != nil && len(results) > 0 {
		query := ai.Query{
			BookID:   book.ID,
			Title:    book.Title,
			Narrator: derefStr(book.Narrator),
		}
		if book.AuthorID != nil {
			if author, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
				query.Author = author.Name
			}
		}

		cands := make([]ai.Candidate, len(results))
		for i, r := range results {
			cands[i] = ai.Candidate{
				Title:    r.Title,
				Author:   r.Author,
				Narrator: r.Narrator,
			}
		}

		scores, err := mfs.metadataScorer.Score(ctx, query, cands)
		if err == nil && len(scores) == len(results) {
			return scores, mfs.metadataScorer.Name()
		}
		if err != nil {
			log.Printf("[WARN] metadata-scorer %s failed, falling back to F1: %v",
				mfs.metadataScorer.Name(), err)
		} else {
			log.Printf("[WARN] metadata-scorer %s returned %d scores for %d results, falling back to F1",
				mfs.metadataScorer.Name(), len(scores), len(results))
		}
	}

	// F1 fallback tier.
	scores := make([]float64, len(results))
	for i, r := range results {
		scores[i] = computeF1Base(r, searchWords)
	}
	return scores, "f1"
}
```

You'll need these imports in the file (grep first):
- `"context"` — likely already present
- `"github.com/jdfalk/audiobook-organizer/internal/ai"` — added in task 7
- `"github.com/jdfalk/audiobook-organizer/internal/config"` — likely already present
- `log` — definitely already present

The `derefStr` helper is already in the server package (used by `dedup_engine.go`).

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/server/ -run TestScoreBaseCandidates -v -count=1`
Expected: all 4 tests PASS.

- [ ] **Step 5: Verify the broader server test suite still passes**

Run: `go test ./internal/server/ -run "TestMetadata|TestScore|TestBestTitle" -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/server/metadata_fetch_service.go internal/server/metadata_scoring_refactor_test.go
git commit -m "feat(metadata): scoreBaseCandidates tier selection with F1 fallback"
```

---

### Task 9: Wire scoreBaseCandidates into the main search loop

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

The main search loop in `SearchMetadataForBook` calls `scoreOneResult` directly at `:1686`. This task replaces that one line with a pre-scored lookup from `scoreBaseCandidates`, then applies `applyNonBaseAdjustments` explicitly so the compilation/length/rich-metadata logic still runs on top of the new base score.

The ASIN direct-lookup path at `:1771` is a single-result special case — leave it on `scoreOneResult` for now (it's not on the hot path and touching it would double the blast radius of this task).

- [ ] **Step 1: Read the current main search loop**

Grep: `grep -n "for _, r := range allResults" internal/server/metadata_fetch_service.go`

Read the function from the top of `SearchMetadataForBook` (`:1550`) down to past the `candidates = append` call (`:1748`) so you understand the variable names in scope: `book`, `searchTitle`, `bookAuthor`, `bookNarrator`, `searchSeries`, `searchWords`, `allResults`, `sources`, `src`, `candidates`, `seen`.

The existing loop looks roughly like:

```go
for _, r := range allResults {
    key := strings.ToLower(r.Title + "|" + r.Author)
    if seen[key] {
        continue
    }
    seen[key] = true

    score := scoreOneResult(r, searchWords)
    if score <= 0 {
        log.Printf("[DEBUG] metadata-search: score=0 for %q by %q from %s", r.Title, r.Author, src.Name())
        continue
    }

    // ... author/narrator/series/audiobook bonuses on top of score ...

    candidates = append(candidates, MetadataCandidate{
        // ...
        Score: score,
    })
}
```

- [ ] **Step 2: Restructure the loop to pre-score via scoreBaseCandidates**

The tricky part: the current loop processes one source at a time (inside `for _, src := range sources`), so `allResults` is actually a per-source slice. The `seen` dedupe set also lives above the loop. The cleanest refactor is to pre-score the per-source slice right before the inner loop.

Replace this block (roughly `:1677-1690`):

```go
log.Printf("[DEBUG] metadata-search: %s returned %d raw results for %q", src.Name(), len(allResults), searchTitle)

for _, r := range allResults {
    key := strings.ToLower(r.Title + "|" + r.Author)
    if seen[key] {
        continue
    }
    seen[key] = true

    score := scoreOneResult(r, searchWords)
    if score <= 0 {
        log.Printf("[DEBUG] metadata-search: score=0 for %q by %q from %s", r.Title, r.Author, src.Name())
        continue
    }
```

With:

```go
log.Printf("[DEBUG] metadata-search: %s returned %d raw results for %q", src.Name(), len(allResults), searchTitle)

baseScores, baseTier := mfs.scoreBaseCandidates(context.Background(), book, allResults, searchWords)
log.Printf("[DEBUG] metadata-search: scored %d results from %s with tier %s", len(allResults), src.Name(), baseTier)

// Score-filter threshold differs by tier. F1 filters at <=0; embedding uses
// the configured MetadataEmbeddingMinScore.
minBaseScore := 0.0
if baseTier == "embedding" {
    minBaseScore = config.AppConfig.MetadataEmbeddingMinScore
}

for i, r := range allResults {
    key := strings.ToLower(r.Title + "|" + r.Author)
    if seen[key] {
        continue
    }
    seen[key] = true

    baseScore := baseScores[i]
    if baseScore <= minBaseScore {
        log.Printf("[DEBUG] metadata-search: score=%.3f (tier=%s) below threshold for %q by %q from %s",
            baseScore, baseTier, r.Title, r.Author, src.Name())
        continue
    }

    // Apply non-base adjustments (compilation, length, rich metadata).
    // For non-F1 tiers, pass baseWordCount=0 so the length penalty is
    // suppressed — it's a token-overlap-specific signal that doesn't
    // translate to semantic embedding scores.
    baseWordCount := 0
    if baseTier == "f1" {
        baseWordCount = len(searchWords)
    }
    score := applyNonBaseAdjustments(baseScore, r, baseWordCount)
```

Then continue with the existing author/narrator/series/audiobook bonus code unchanged, then the `candidates = append` call unchanged.

- [ ] **Step 3: Add BaseScorer to the candidate append (optional but useful)**

The spec mentions a `BaseScorer` field on `MetadataCandidate` for UI badges. That's a PR2 concern (UI surfacing), but storing the tier name now costs nothing. Find the `candidates = append(candidates, MetadataCandidate{...})` call and add `BaseScorer: baseTier,` to the struct literal **if and only if** `MetadataCandidate` already has or can gain that field easily. Grep for the struct definition:

```bash
grep -n "type MetadataCandidate struct" internal/server/metadata_fetch_service.go
```

If the struct is in the same file and simple, add the field:

```go
BaseScorer string `json:"base_scorer,omitempty"`
```

If the struct is shared with other packages or has a lot of consumers, **skip this step** and defer to PR2. The main-loop wiring is what matters for PR1.

- [ ] **Step 4: Build**

Run: `go build ./internal/server/`
Expected: clean build. Likely errors:
- Unused variable warnings if `searchWords` is no longer used downstream — re-grep to verify.
- Import of `context` missing — add it.

- [ ] **Step 5: Run the metadata search tests**

Run: `go test ./internal/server/ -run "TestMetadata|TestScore|TestBestTitle|TestSearchMetadata" -count=1 -timeout 60s -v`
Expected: PASS. The tests exercise the F1 path (since `metadataScorer` is nil in the default test setup), so the fallback chain should produce identical results to before.

If tests fail with differences in ranking, most likely cause: the new `minBaseScore` filter is dropping results the old `score > 0` filter kept, because for F1 the condition `score <= 0` is stricter than `baseScore <= 0` (F1 can yield exactly 0 but rich-metadata bonus could still push the old score positive). To match the old behavior exactly on the F1 path, use:

```go
if baseTier == "f1" && baseScore == 0 {
    // Old scoreOneResult returned 0+bonus here, which could be >0. Reproduce that
    // by applying adjustments before filtering.
    adjusted := applyNonBaseAdjustments(baseScore, r, len(searchWords))
    if adjusted <= 0 {
        continue
    }
    baseScore = adjusted
} else if baseScore <= minBaseScore {
    continue
}
```

Actually — simpler fix: just change the filter order. Compute `applyNonBaseAdjustments` first, then filter on the adjusted score:

```go
baseWordCount := 0
if baseTier == "f1" {
    baseWordCount = len(searchWords)
}
score := applyNonBaseAdjustments(baseScore, r, baseWordCount)

// Tier-specific minimum on the adjusted score
minScore := 0.0
if baseTier == "embedding" {
    minScore = config.AppConfig.MetadataEmbeddingMinScore
}
if score <= minScore {
    log.Printf("[DEBUG] metadata-search: adjusted score=%.3f (tier=%s) below threshold for %q by %q from %s",
        score, baseTier, r.Title, r.Author, src.Name())
    continue
}
```

Use this version. It's one less branch and matches the old F1 behavior exactly when `minScore = 0`.

- [ ] **Step 6: Commit**

```bash
git add internal/server/metadata_fetch_service.go
git commit -m "feat(metadata): wire scoreBaseCandidates into main search loop with tier-specific filters"
```

---

### Task 10: Update bestTitleMatchWithContext to use the scorer

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

`bestTitleMatchWithContext` at `:1094-1151` is called from `FetchMetadataForBook` (`:273`) and `FetchMetadataForBookByTitle` (`:425`) — both are "narrow down to the single best result" code paths. It currently calls `scoreOneResult` directly. This task plumbs the scorer through so those paths also benefit from embedding-based scoring.

Unlike the main search loop, `bestTitleMatchWithContext` doesn't receive a `*database.Book` — it only has `bookAuthor` and `bookNarrator` strings. The scorer's `BookID` fast-path won't work here, so the scorer always embeds the query on the fly. That's fine.

- [ ] **Step 1: Read the current bestTitleMatchWithContext signature**

```go
func bestTitleMatchWithContext(results []metadata.BookMetadata, bookAuthor, bookNarrator string, titles ...string) []metadata.BookMetadata
```

This is a package-level function, not a method on `MetadataFetchService`, so it has no access to the scorer. We have two options:

a) Make it a method on `MetadataFetchService` and pass `mfs` through. Breaks the call sites but gives clean scorer access.
b) Leave it as a function and add an optional scorer parameter. Less disruptive but uglier signature.

Go with (a). The call sites already have `mfs` in scope.

- [ ] **Step 2: Convert bestTitleMatch and bestTitleMatchWithContext to methods**

Change:

```go
func bestTitleMatch(results []metadata.BookMetadata, titles ...string) []metadata.BookMetadata {
	return bestTitleMatchWithContext(results, "", "", titles...)
}

func bestTitleMatchWithContext(results []metadata.BookMetadata, bookAuthor, bookNarrator string, titles ...string) []metadata.BookMetadata {
	// ... uses scoreOneResult directly ...
}
```

To:

```go
func (mfs *MetadataFetchService) bestTitleMatch(results []metadata.BookMetadata, titles ...string) []metadata.BookMetadata {
	return mfs.bestTitleMatchWithContext(nil, results, "", "", titles...)
}

func (mfs *MetadataFetchService) bestTitleMatchWithContext(
	book *database.Book, // optional, for BookID fast-path; nil is acceptable
	results []metadata.BookMetadata,
	bookAuthor, bookNarrator string,
	titles ...string,
) []metadata.BookMetadata {
	const f1MinScore = 0.35

	// Union of significant words from all title variants.
	searchWords := map[string]bool{}
	for _, t := range titles {
		for w := range significantWords(t) {
			searchWords[w] = true
		}
	}

	// Score the batch via the tier chain.
	var baseBook *database.Book
	if book != nil {
		baseBook = book
	} else {
		// Fabricate a minimal Book so scoreBaseCandidates has something to
		// key on. BookID stays empty, so the scorer embeds the query on the
		// fly — which is exactly what we want when the caller has no book.
		baseBook = &database.Book{Title: firstNonEmpty(titles...)}
	}
	baseScores, baseTier := mfs.scoreBaseCandidates(context.Background(), baseBook, results, searchWords)

	minScore := f1MinScore
	if baseTier == "embedding" {
		minScore = config.AppConfig.MetadataEmbeddingBestMatchMin
	}

	bestIdx := -1
	bestScore := 0.0
	for i, r := range results {
		baseScore := baseScores[i]
		// Non-base adjustments
		baseWordCount := 0
		if baseTier == "f1" {
			baseWordCount = len(searchWords)
		}
		score := applyNonBaseAdjustments(baseScore, r, baseWordCount)

		// Author-based scoring bonuses (copy from old bestTitleMatchWithContext verbatim)
		if bookAuthor != "" {
			if r.Author != "" {
				rAuthorLower := strings.ToLower(r.Author)
				bAuthorLower := strings.ToLower(bookAuthor)
				if strings.Contains(rAuthorLower, bAuthorLower) || strings.Contains(bAuthorLower, rAuthorLower) {
					score *= 1.5
				} else {
					score *= 0.7
				}
			} else {
				score *= 0.75
			}
		}

		// Narrator-based scoring
		if bookNarrator != "" && r.Narrator != "" {
			rNarrLower := strings.ToLower(r.Narrator)
			bNarrLower := strings.ToLower(bookNarrator)
			if strings.Contains(rNarrLower, bNarrLower) || strings.Contains(bNarrLower, rNarrLower) {
				score *= 1.3
			}
		}

		// Audiobook-specific
		if r.Narrator != "" {
			score *= 1.15
		} else {
			score *= 0.85
		}

		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}

	if bestIdx >= 0 && bestScore >= minScore {
		return []metadata.BookMetadata{results[bestIdx]}
	}
	return nil
}

// firstNonEmpty returns the first non-empty string from its arguments, or "".
func firstNonEmpty(strs ...string) string {
	for _, s := range strs {
		if s != "" {
			return s
		}
	}
	return ""
}
```

- [ ] **Step 3: Update the two call sites to use the method form**

Find each call to `bestTitleMatchWithContext` and convert:

**Call site 1** (`:273`): `scored := bestTitleMatchWithContext(results, currentAuthor, currentNarrator, searchTitle, book.Title)`

Change to:

```go
scored := mfs.bestTitleMatchWithContext(book, results, currentAuthor, currentNarrator, searchTitle, book.Title)
```

**Call site 2** (`:425`): `scored := bestTitleMatchWithContext(results, "", titleOnlyNarrator, searchTitle, book.Title)`

Change to:

```go
scored := mfs.bestTitleMatchWithContext(book, results, "", titleOnlyNarrator, searchTitle, book.Title)
```

- [ ] **Step 4: Check for other callers of bestTitleMatch**

```bash
grep -n "bestTitleMatch\b" internal/server/
```

If there are any callers of the zero-arg `bestTitleMatch` helper, convert them to `mfs.bestTitleMatch`. If there are tests that call it directly, those tests need `mfs := &MetadataFetchService{}` prefixed.

- [ ] **Step 5: Build**

Run: `go build ./internal/server/`
Expected: clean build.

- [ ] **Step 6: Run affected tests**

Run: `go test ./internal/server/ -run "TestMetadata|TestScore|TestBestTitle|TestFetchMetadata" -count=1 -timeout 60s -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/server/metadata_fetch_service.go
git commit -m "feat(metadata): route bestTitleMatch through pluggable scorer tier chain"
```

---

### Task 11: Construct and inject the EmbeddingScorer at server startup

**Files:**
- Modify: `internal/server/server.go`

The embedding client is already constructed at `server.go:828` inside the dedup init block. This task hooks into that same block to build an `EmbeddingScorer` and inject it into `metadataFetchService` via the setter from task 7.

- [ ] **Step 1: Find the embedding init block**

Run: `grep -n "embedClient := ai.NewEmbeddingClient" internal/server/server.go`

You'll land around line 828. The existing code looks like:

```go
if config.AppConfig.OpenAIAPIKey != "" && config.AppConfig.EmbeddingEnabled {
    embedClient := ai.NewEmbeddingClient(config.AppConfig.OpenAIAPIKey)
    llmParser := ai.NewOpenAIParser(config.AppConfig.OpenAIAPIKey, config.AppConfig.EnableAIParsing)
    server.dedupEngine = NewDedupEngine(
        embeddingStore,
        database.GlobalStore,
        embedClient,
        llmParser,
        server.mergeService,
    )
    // ... threshold assignments ...
    log.Println("[INFO] Embedding store and dedup engine initialized")
    server.metadataFetchService.SetDedupEngine(server.dedupEngine)
}
```

- [ ] **Step 2: Add the scorer injection**

Right after the existing `SetDedupEngine` call, add:

```go
// Wire the embedding-based metadata candidate scorer. The scorer reuses
// the same embedClient + embeddingStore as the dedup engine; it's a
// separate lightweight wrapper that exposes the MetadataCandidateScorer
// interface. Any failure at search time falls back to the F1 path inside
// scoreBaseCandidates, so this is safe to leave wired up unconditionally
// once the embedding infra is available.
if config.AppConfig.MetadataEmbeddingScoringEnabled {
    server.metadataFetchService.SetMetadataScorer(
        ai.NewEmbeddingScorer(embedClient, embeddingStore),
    )
    log.Println("[INFO] Metadata candidate scoring: embedding tier enabled")
}
```

- [ ] **Step 3: Bump the server.go version header**

At the top of `internal/server/server.go`, bump the `// version:` line (e.g. 1.151.0 → 1.152.0).

- [ ] **Step 4: Build**

Run: `go build ./internal/server/`
Expected: clean build.

- [ ] **Step 5: Verify the whole project still builds**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add internal/server/server.go
git commit -m "feat(server): inject EmbeddingScorer into MetadataFetchService at startup"
```

---

### Task 12: End-to-end smoke test

**Files:**
- Modify: `internal/server/metadata_scoring_refactor_test.go`

This test wires a real `MetadataFetchService` with a stub scorer, runs a search-like pipeline on a canned result set, and verifies the stub scorer was called and its scores shaped the final output. This is the "did the glue actually connect" test, not a deep integration test.

- [ ] **Step 1: Add the test**

Append to `internal/server/metadata_scoring_refactor_test.go`:

```go
// TestMetadataScorer_WiredEndToEnd verifies that an injected scorer's output
// reaches the main search loop via scoreBaseCandidates. It uses a
// controllable scorerStub and feeds it canned results, then checks that the
// stub was invoked and its scores became the base scores used downstream.
func TestMetadataScorer_WiredEndToEnd(t *testing.T) {
	// Stub scorer that prefers the second candidate over the first.
	stub := &scorerStub{
		name:   "embedding",
		scores: []float64{0.30, 0.95},
	}

	mfs := &MetadataFetchService{metadataScorer: stub}
	prev := config.AppConfig.MetadataEmbeddingScoringEnabled
	prevMin := config.AppConfig.MetadataEmbeddingMinScore
	config.AppConfig.MetadataEmbeddingScoringEnabled = true
	config.AppConfig.MetadataEmbeddingMinScore = 0.50
	defer func() {
		config.AppConfig.MetadataEmbeddingScoringEnabled = prev
		config.AppConfig.MetadataEmbeddingMinScore = prevMin
	}()

	book := &database.Book{ID: "BOOK_X", Title: "Query Title"}
	results := []metadata.BookMetadata{
		{Title: "Weak Match", Author: "Someone"},
		{Title: "Strong Match", Author: "Someone"},
	}
	searchWords := significantWords("Query Title")

	scores, tier := mfs.scoreBaseCandidates(context.Background(), book, results, searchWords)
	assert.Equal(t, "embedding", tier)
	assert.Equal(t, []float64{0.30, 0.95}, scores)
	assert.Equal(t, 1, stub.callCount, "scorer called exactly once")

	// Verify the min-score filter logic by running it inline (mirrors the
	// main search loop's minScore check after applyNonBaseAdjustments).
	var kept []int
	for i, s := range scores {
		adjusted := applyNonBaseAdjustments(s, results[i], 0)
		if adjusted > config.AppConfig.MetadataEmbeddingMinScore {
			kept = append(kept, i)
		}
	}
	assert.Equal(t, []int{1}, kept, "only the strong match should survive the filter")
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/server/ -run TestMetadataScorer_WiredEndToEnd -v -count=1`
Expected: PASS.

- [ ] **Step 3: Run the full server test suite**

Run: `go test ./internal/server/ -count=1 -timeout 180s`
Expected: PASS.

- [ ] **Step 4: Run the full project test suite**

Run: `go test ./... -count=1 -timeout 240s 2>&1 | tail -30`
Expected: all packages PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/metadata_scoring_refactor_test.go
git commit -m "test(metadata): end-to-end smoke test for scorer wiring"
```

---

### Task 13: Full build, deploy, and manual verification

- [ ] **Step 1: Full build**

Run: `make build-api`
Expected: builds cleanly.

- [ ] **Step 2: Frontend type check**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no errors. This PR does not touch the frontend so this is just a sanity check.

- [ ] **Step 3: Deploy to dev**

Run: `make deploy-debug`
Expected: binary deployed to the prod server, service restarted.

- [ ] **Step 4: Verify the scorer is live**

```bash
ssh <user>@unimatrixzero.local "journalctl -u audiobook-organizer --no-pager --since '2 min ago' | grep -iE 'metadata candidate scoring'"
```
Expected: `[INFO] Metadata candidate scoring: embedding tier enabled`.

- [ ] **Step 5: Run a real metadata search**

Pick a book from the library via the UI or API, trigger a metadata search, and watch the logs:

```bash
ssh <user>@unimatrixzero.local "journalctl -u audiobook-organizer -f --no-pager | grep metadata-search"
```

Expected log lines include:
- `metadata-search: <source> returned N raw results for ...`
- `metadata-search: scored N results from <source> with tier embedding`
- A mix of "adjusted score=..." entries and accepted candidates.

If the tier shows `f1` instead of `embedding`, the scorer wiring failed at startup. Check for scorer init logs in the earlier part of the journal.

- [ ] **Step 6: Create the PR**

```bash
git push -u origin feature/metadata-scorer-pr1
gh pr create \
  --title "feat: embedding-based metadata candidate scoring (PR 1 — base)" \
  --body "$(cat <<'EOF'
## Summary

PR 1 of 2 in the metadata candidate scoring rollout. Introduces the
`ai.MetadataCandidateScorer` interface and a single implementation,
`ai.EmbeddingScorer`, that wraps the existing OpenAI embedding client +
`database.EmbeddingStore`. Wires the scorer into `metadata_fetch_service.go`
as the primary base-score tier, with the pre-existing `significantWords` F1
path as a safe fallback.

PR 2 (follow-up) will add the optional LLM rerank tier + per-search UI
toggle + settings switch.

## What's in this PR

- New file `internal/ai/metadata_scorer.go` — interface + `Query` + `Candidate` types
- New file `internal/ai/embedding_scorer.go` — `EmbeddingScorer` with `BookID` fast-path
- New test files covering the interface contract, cosine ranking, `BookID` fast-path, negative-cosine clamping, and both error paths
- Refactor of `scoreOneResult` in `metadata_fetch_service.go` into `computeF1Base` + `applyNonBaseAdjustments` with a regression test locking current behavior
- New `scoreBaseCandidates` tier selector on `MetadataFetchService` — tries the injected scorer first, falls back to F1 on error, nil scorer, or disabled config
- Main search loop (`SearchMetadataForBook`) and `bestTitleMatchWithContext` both route through the new tier chain
- Three new config keys: `MetadataEmbeddingScoringEnabled`, `MetadataEmbeddingMinScore`, `MetadataEmbeddingBestMatchMin`
- Startup wiring in `server.go` — constructs the scorer when the embedding infra is available and injects it into the metadata fetch service

## What's NOT in this PR

- No LLM rerank tier (PR 2)
- No per-search user toggle (PR 2)
- No UI changes (PR 2)
- No Cohere / Voyage / external reranker — explicitly declined during design to keep the vendor surface at one
- No candidate embedding cache — cost is ~\$0.00014/search, not worth the complexity yet

## Fallback guarantees

Every failure mode lands in the F1 path. The search can't break because of
scorer problems:

- No API key → scorer not wired → F1
- Scorer returns error → logged, F1 fallback
- Config disabled → F1
- Book has no stored vector → scorer embeds query on the fly, still works
- All candidates empty → scorer never called

## Test plan

- [x] Unit tests for the scorer interface, EmbeddingScorer math, error paths, BookID fast-path
- [x] Regression test locking existing \`scoreOneResult\` behavior
- [x] Tier selection tests covering happy path, disabled config, scorer error, nil scorer
- [x] End-to-end wiring smoke test
- [x] Full server + full project test suites pass
- [ ] Deploy to prod, confirm \"Metadata candidate scoring: embedding tier enabled\" log
- [ ] Manual metadata search on a known-hard case (subtitle variant, series with trailing number) — verify the embedding tier ranks the correct candidate first

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 7: Merge**

Wait for CI. When green, rebase-merge:

```bash
PR_NUMBER=$(gh pr list --head feature/metadata-scorer-pr1 --json number -q '.[0].number')
gh pr merge "$PR_NUMBER" --rebase --delete-branch
git checkout main && git pull
```
