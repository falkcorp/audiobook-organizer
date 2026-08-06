# Metadata Candidate Scoring — PR 2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in LLM rerank tier on top of the embedding scorer, triggered by a per-search user flag, so the top ambiguous metadata candidates can be judged by `gpt-5-mini` when the user wants higher-quality ranking.

**Architecture:** A new `LLMScorer` implements `ai.MetadataCandidateScorer` by calling a new `OpenAIParser.ScoreMetadataCandidates` method that mirrors the existing `ReviewDedupPairs` pattern (structured-JSON chat completion). The metadata fetch service gets an optional `llmScorer` field and a `rerankTopK` method that fires only when the user sets `use_rerank=true` on the search request AND the server-wide `MetadataLLMScoringEnabled` config is on. Rerank operates on candidates within `MetadataLLMRerankEpsilon` of the best base score, capped at `MetadataLLMRerankTopK`. LLM scores replace the base score for those candidates (bypassing the usual author/narrator/series bonus multiplication, since the LLM already sees those fields). UI surfaces via a `Switch` in the `MetadataSearchDialog` and a kill switch in the Settings AI section.

**Tech Stack:** Go 1.24, OpenAI Go SDK v1.12.0 (already in use for `ReviewDedupPairs`), React 18 + MUI, existing `MetadataCandidateScorer` interface from PR 1.

**Spec:** `docs/superpowers/specs/2026-04-10-metadata-candidate-scoring-design.md` (read the "LLMScorer", "rerankTopK", "Config Keys", "API Surface", and "UI Changes" sections)

**Prerequisites already in place (from PR 1, merged to main):**
- `ai.MetadataCandidateScorer` interface + `Query` + `Candidate` types in `internal/ai/metadata_scorer.go`
- `EmbeddingScorer` base implementation
- `(mfs *MetadataFetchService).scoreBaseCandidates` tier selector with F1 fallback
- `(mfs *MetadataFetchService).bestTitleMatchForBook` scorer-aware method
- Main `SearchMetadataForBook` loop wired through the tier chain
- Config keys: `MetadataEmbeddingScoringEnabled`, `MetadataEmbeddingMinScore`, `MetadataEmbeddingBestMatchMin`

**Reference context about the existing code:**

- `internal/ai/dedup_review.go` contains `OpenAIParser.ReviewDedupPairs` — the canonical example of a chat-completion + structured-JSON pattern in this codebase. The new `ScoreMetadataCandidates` method follows the same shape: build a prompt, call `client.Chat.Completions.New` with `ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &jsonObjectFormat}`, batch inputs, retry with backoff, unmarshal into a struct. Read that file before writing Task 1.
- `OpenAIParser.maxRetries` and `p.model` (currently `gpt-5-mini`) are already set up and used by all existing parser methods — reuse them.
- `internal/server/dedup_engine.go` shows the `llmParser *ai.OpenAIParser` injection pattern — the dedup engine got this in PR #204. We're adding the same injection pattern to `MetadataFetchService`.
- `internal/server/metadata_fetch_service.go:1200-1258` contains `scoreBaseCandidates`, which is what produces the base scores we'll rerank on top of.
- `MetadataCandidate` struct lives at `internal/server/metadata_fetch_service.go:106-121`. It has `Score float64` but no tier marker today. Leave the shape alone unless a task specifically adds a field.
- The main search loop is `SearchMetadataForBook` starting at `:1736`. The relevant section is `:1865` where `scoreBaseCandidates` is called and candidates are appended. Rerank happens AFTER the loop, AFTER domain bonuses are applied.
- The user-facing search endpoint is `POST /api/v1/audiobooks/:id/search-metadata` at `server.go:7450-7481`. Request body today: `{query, author, narrator, series}`. We add `use_rerank bool`.
- `SearchMetadataForBook` signature is `(id, query string, authorHint ...string)`. It takes **four strings** at most: `query`, `author`, `narrator`, `series`. We need to pass the `use_rerank` flag through without breaking existing callers (including tests). Options covered in Task 5.
- Frontend: search dialog is `web/src/components/audiobooks/MetadataSearchDialog.tsx`. The `doSearch` callback at line 128 calls `api.searchMetadataForBook(book.id, searchQuery, author, narrator, series)`. The Settings page is `web/src/pages/Settings.tsx`, AI section begins around line 2525 with the existing `enableAIParsing` switch — we insert the new rerank kill switch adjacent to it.
- All files in this project need versioned headers. Format: `// file: path/to/file.go` / `// version: X.Y.Z` / `// guid: (uuid)`. New files get fresh UUIDs. Modified files bump the version (minor bump for features, patch for fixes).
- Worktree for this PR (created before executing this plan): `/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/metadata-scorer-pr2`. Branch: `feature/metadata-scorer-pr2`. All `go test`, `go build`, and `git` commands in this plan assume `cwd` is that worktree root.

---

### Task 1: Add `OpenAIParser.ScoreMetadataCandidates` method

**Files:**
- Create: `internal/ai/metadata_llm_review.go`
- Create: `internal/ai/metadata_llm_review_test.go`

The new method sends batched candidate-vs-query scoring requests to the chat LLM and returns one score per candidate plus an optional reason string. It mirrors `ReviewDedupPairs` from `dedup_review.go` — same batching pattern, same retry loop, same structured-JSON response format.

- [ ] **Step 1: Write the failing test**

Create `internal/ai/metadata_llm_review_test.go`:

```go
// file: internal/ai/metadata_llm_review_test.go
// version: 1.0.0
// guid: (generate new UUID)

package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMetadataLLMScore_JSONShape locks in the struct shape the
// ScoreMetadataCandidates response is expected to unmarshal into. It's a
// compile-time check wrapped in a runtime assertion: if the types change
// in a breaking way this test stops compiling.
func TestMetadataLLMScore_JSONShape(t *testing.T) {
	score := MetadataLLMScore{
		Index:  3,
		Score:  0.92,
		Reason: "Same book, different subtitle format",
	}
	assert.Equal(t, 3, score.Index)
	assert.InDelta(t, 0.92, score.Score, 0.0001)
	assert.Equal(t, "Same book, different subtitle format", score.Reason)
}

// TestMetadataLLMQuery_FieldMapping verifies the Query/Candidate field
// layout the caller uses. This mirrors the same test in
// metadata_scorer_test.go but against the AI package's view of things.
func TestMetadataLLMQuery_FieldMapping(t *testing.T) {
	q := MetadataLLMQuery{
		Title:    "Dune",
		Author:   "Frank Herbert",
		Narrator: "Scott Brick",
	}
	assert.Equal(t, "Dune", q.Title)
	assert.Equal(t, "Frank Herbert", q.Author)
	assert.Equal(t, "Scott Brick", q.Narrator)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/ai/ -run "TestMetadataLLM" -v -count=1`
Expected: FAIL with `undefined: MetadataLLMScore` / `undefined: MetadataLLMQuery`.

- [ ] **Step 3: Implement `internal/ai/metadata_llm_review.go`**

```go
// file: internal/ai/metadata_llm_review.go
// version: 1.0.0
// guid: (generate new UUID)

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

// MetadataLLMQuery describes the book the caller is searching metadata for.
// It's the AI-package-local view of ai.Query, kept separate so the JSON
// field names used in the LLM prompt are frozen regardless of future changes
// to the public scorer interface.
type MetadataLLMQuery struct {
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
}

// MetadataLLMCandidate is one search result the LLM ranks against the query.
type MetadataLLMCandidate struct {
	Index    int    `json:"index"`
	Title    string `json:"title"`
	Author   string `json:"author,omitempty"`
	Narrator string `json:"narrator,omitempty"`
}

// MetadataLLMScore is the LLM's judgment for a single candidate. Score is
// in [0.0, 1.0] where 1.0 means "definitely the same book." Reason is a
// short one-sentence explanation suitable for display in a debug log or UI
// tooltip.
type MetadataLLMScore struct {
	Index  int     `json:"index"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// metadataLLMBatchSize caps the number of candidates sent per chat request.
// Matches dedupReviewBatchSize in dedup_review.go — 25 is comfortably under
// the structured-JSON token limits with typical per-candidate payloads.
const metadataLLMBatchSize = 25

// ScoreMetadataCandidates asks the chat LLM to rank candidate metadata search
// results against a query book. It batches inputs internally and returns one
// score per candidate, in input order. Indices in the response are used to
// route scores back to their input slot — missing indices default to 0.0
// (the caller should treat them as "LLM didn't rank this one, use the base
// score instead").
//
// Returns (nil, err) on any failure so callers can fall back to the base
// scorer — no partial results with a nil error.
func (p *OpenAIParser) ScoreMetadataCandidates(
	ctx context.Context,
	query MetadataLLMQuery,
	candidates []MetadataLLMCandidate,
) ([]MetadataLLMScore, error) {
	if !p.enabled {
		return nil, fmt.Errorf("OpenAI parser is not enabled")
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// Ensure every candidate carries a sequential index so the LLM can
	// reference them unambiguously. We don't trust the caller to pre-number.
	indexed := make([]MetadataLLMCandidate, len(candidates))
	for i, c := range candidates {
		c.Index = i
		indexed[i] = c
	}

	var all []MetadataLLMScore
	for start := 0; start < len(indexed); start += metadataLLMBatchSize {
		end := start + metadataLLMBatchSize
		if end > len(indexed) {
			end = len(indexed)
		}
		batch := indexed[start:end]
		scores, err := p.scoreMetadataBatch(ctx, query, batch)
		if err != nil {
			return all, fmt.Errorf("metadata LLM batch [%d:%d]: %w", start, end, err)
		}
		all = append(all, scores...)
	}
	return all, nil
}

func (p *OpenAIParser) scoreMetadataBatch(
	ctx context.Context,
	query MetadataLLMQuery,
	batch []MetadataLLMCandidate,
) ([]MetadataLLMScore, error) {
	systemPrompt := `You are an expert audiobook metadata reviewer. You will receive one query book and a batch of candidate search results. For each candidate, score how well it matches the query on a scale from 0.0 to 1.0, where:

- 1.0 = definitely the same book (same title and author, allowing minor punctuation/subtitle differences)
- 0.7-0.9 = probably the same book (same title core, same author, minor edition differences)
- 0.4-0.6 = ambiguous (partial title match, unclear author)
- 0.0-0.3 = probably not the same book (different volumes in a series, unrelated titles)

Scoring rules:
- Title identity matters most. "The Way of Kings" and "Stormlight Archive 1: The Way of Kings" are the same book.
- Author match is a strong signal. Same title with a different author is usually a different book.
- Narrator differences do NOT reduce the score — re-recordings of the same book are still the same book.
- Series position mismatches (volume 6 vs volume 3) should score low (~0.2) even if the series name matches.
- Compilations and omnibus editions should score low (~0.3) unless the query is itself an omnibus.

Return ONLY valid JSON in this exact shape:
{"scores": [{"index": N, "score": 0.0-1.0, "reason": "one-sentence explanation"}]}

Include one score per input candidate, using the same index as the input.`

	payload := struct {
		Query      MetadataLLMQuery       `json:"query"`
		Candidates []MetadataLLMCandidate `json:"candidates"`
	}{
		Query:      query,
		Candidates: batch,
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	userPrompt := fmt.Sprintf("Rank these candidate metadata search results against the query book:\n\n%s", string(payloadJSON))

	jsonObjectFormat := shared.NewResponseFormatJSONObjectParam()

	var lastErr error
	for attempt := 0; attempt <= p.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt*attempt) * 2 * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.SystemMessage(systemPrompt),
				openai.UserMessage(userPrompt),
			},
			Model:               shared.ChatModel(p.model),
			MaxCompletionTokens: param.NewOpt[int64](8000),
			PromptCacheKey:      param.NewOpt("audiobook-metadata-score-v1"),
			ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONObject: &jsonObjectFormat,
			},
		})
		if err != nil {
			lastErr = fmt.Errorf("OpenAI API call failed (attempt %d): %w", attempt+1, err)
			continue
		}
		if len(completion.Choices) == 0 {
			lastErr = fmt.Errorf("no response from OpenAI (attempt %d)", attempt+1)
			continue
		}

		content := completion.Choices[0].Message.Content
		var result struct {
			Scores []MetadataLLMScore `json:"scores"`
		}
		if err := json.Unmarshal([]byte(content), &result); err != nil {
			lastErr = fmt.Errorf("parse response (attempt %d): %w", attempt+1, err)
			continue
		}
		return result.Scores, nil
	}
	return nil, lastErr
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ai/ -run "TestMetadataLLM" -v -count=1`
Expected: both tests PASS.

- [ ] **Step 5: Run full ai package tests**

Run: `go test ./internal/ai/ -count=1`
Expected: all tests pass, no regressions in neighbouring dedup_review / openai_parser tests.

- [ ] **Step 6: Commit**

```bash
git add internal/ai/metadata_llm_review.go internal/ai/metadata_llm_review_test.go
git commit -m "feat(ai): ScoreMetadataCandidates — chat-LLM candidate reranker on OpenAIParser"
```

---

### Task 2: Implement `LLMScorer` adapter (satisfies `MetadataCandidateScorer`)

**Files:**
- Create: `internal/ai/llm_scorer.go`
- Create: `internal/ai/llm_scorer_test.go`

`LLMScorer` is a thin adapter that wraps an `*OpenAIParser` and exposes it as a `MetadataCandidateScorer`. It translates between the public `Query`/`Candidate` types (same ones `EmbeddingScorer` uses) and the package-private `MetadataLLMQuery`/`MetadataLLMCandidate` types, then calls `ScoreMetadataCandidates` and reassembles the results in input order.

- [ ] **Step 1: Write the failing tests**

Create `internal/ai/llm_scorer_test.go`:

```go
// file: internal/ai/llm_scorer_test.go
// version: 1.0.0
// guid: (generate new UUID)

package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLLMBackend is a test seam for LLMScorer. It satisfies the
// metadataLLMBackend interface so llm_scorer_test can inject a stub
// without touching the real OpenAI client.
type fakeLLMBackend struct {
	scores []MetadataLLMScore
	err    error
	calls  int
}

func (f *fakeLLMBackend) ScoreMetadataCandidates(
	ctx context.Context,
	q MetadataLLMQuery,
	cands []MetadataLLMCandidate,
) ([]MetadataLLMScore, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.scores, nil
}

func TestLLMScorer_Name(t *testing.T) {
	scorer := NewLLMScorerWithBackend(&fakeLLMBackend{})
	assert.Equal(t, "llm", scorer.Name())
}

func TestLLMScorer_EmptyCandidates(t *testing.T) {
	backend := &fakeLLMBackend{}
	scorer := NewLLMScorerWithBackend(backend)
	scores, err := scorer.Score(context.Background(), Query{Title: "Dune"}, nil)
	require.NoError(t, err)
	assert.Nil(t, scores)
	assert.Equal(t, 0, backend.calls, "empty input should not call the backend")
}

func TestLLMScorer_ScoresInOrder(t *testing.T) {
	backend := &fakeLLMBackend{
		scores: []MetadataLLMScore{
			{Index: 0, Score: 0.91, Reason: "exact match"},
			{Index: 1, Score: 0.42, Reason: "different edition"},
			{Index: 2, Score: 0.15, Reason: "different book"},
		},
	}
	scorer := NewLLMScorerWithBackend(backend)

	scores, err := scorer.Score(context.Background(),
		Query{Title: "Dune", Author: "Frank Herbert"},
		[]Candidate{
			{Title: "Dune", Author: "Frank Herbert"},
			{Title: "Dune Messiah", Author: "Frank Herbert"},
			{Title: "Dune: The Butlerian Jihad", Author: "Brian Herbert"},
		},
	)
	require.NoError(t, err)
	require.Len(t, scores, 3)
	assert.InDelta(t, 0.91, scores[0], 0.0001)
	assert.InDelta(t, 0.42, scores[1], 0.0001)
	assert.InDelta(t, 0.15, scores[2], 0.0001)
	assert.Equal(t, 1, backend.calls)
}

func TestLLMScorer_OutOfOrderIndices(t *testing.T) {
	// LLM returns scores in a different order than the input — the scorer
	// must route them back to their input slot by Index.
	backend := &fakeLLMBackend{
		scores: []MetadataLLMScore{
			{Index: 2, Score: 0.15},
			{Index: 0, Score: 0.91},
			{Index: 1, Score: 0.42},
		},
	}
	scorer := NewLLMScorerWithBackend(backend)

	scores, err := scorer.Score(context.Background(),
		Query{Title: "Dune"},
		[]Candidate{{Title: "A"}, {Title: "B"}, {Title: "C"}},
	)
	require.NoError(t, err)
	assert.InDelta(t, 0.91, scores[0], 0.0001)
	assert.InDelta(t, 0.42, scores[1], 0.0001)
	assert.InDelta(t, 0.15, scores[2], 0.0001)
}

func TestLLMScorer_MissingIndexDefaultsToZero(t *testing.T) {
	// LLM skipped index 1 — the scorer should fill it with 0.0 rather
	// than shifting the remaining scores.
	backend := &fakeLLMBackend{
		scores: []MetadataLLMScore{
			{Index: 0, Score: 0.91},
			{Index: 2, Score: 0.15},
		},
	}
	scorer := NewLLMScorerWithBackend(backend)

	scores, err := scorer.Score(context.Background(),
		Query{Title: "Dune"},
		[]Candidate{{Title: "A"}, {Title: "B"}, {Title: "C"}},
	)
	require.NoError(t, err)
	require.Len(t, scores, 3)
	assert.InDelta(t, 0.91, scores[0], 0.0001)
	assert.InDelta(t, 0.0, scores[1], 0.0001, "missing index should default to 0")
	assert.InDelta(t, 0.15, scores[2], 0.0001)
}

func TestLLMScorer_ClampsOutOfRange(t *testing.T) {
	// LLM returns 1.2 and -0.3 — scorer clamps to [0, 1].
	backend := &fakeLLMBackend{
		scores: []MetadataLLMScore{
			{Index: 0, Score: 1.2},
			{Index: 1, Score: -0.3},
		},
	}
	scorer := NewLLMScorerWithBackend(backend)
	scores, _ := scorer.Score(context.Background(), Query{}, []Candidate{{}, {}})
	assert.Equal(t, 1.0, scores[0])
	assert.Equal(t, 0.0, scores[1])
}

func TestLLMScorer_BackendError(t *testing.T) {
	backend := &fakeLLMBackend{err: errors.New("openai 503")}
	scorer := NewLLMScorerWithBackend(backend)
	scores, err := scorer.Score(context.Background(),
		Query{Title: "Dune"},
		[]Candidate{{Title: "Dune"}},
	)
	require.Error(t, err)
	assert.Nil(t, scores, "partial results are never returned")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/ai/ -run "TestLLMScorer" -v -count=1`
Expected: FAIL with `undefined: LLMScorer` / `undefined: NewLLMScorerWithBackend` / `undefined: metadataLLMBackend`.

- [ ] **Step 3: Implement `internal/ai/llm_scorer.go`**

```go
// file: internal/ai/llm_scorer.go
// version: 1.0.0
// guid: (generate new UUID)

package ai

import (
	"context"
	"fmt"
)

// metadataLLMBackend is the minimal surface LLMScorer needs from the
// OpenAI parser. It exists so tests can inject a fake without spinning
// up the real chat client. Production code always wires the real
// *OpenAIParser here via NewLLMScorer.
type metadataLLMBackend interface {
	ScoreMetadataCandidates(
		ctx context.Context,
		query MetadataLLMQuery,
		candidates []MetadataLLMCandidate,
	) ([]MetadataLLMScore, error)
}

// LLMScorer satisfies MetadataCandidateScorer by delegating to
// OpenAIParser.ScoreMetadataCandidates. It's the third tier in the
// metadata candidate scoring stack: F1 (free) → embedding (cheap) →
// LLM (slower, more accurate, opt-in per search).
type LLMScorer struct {
	backend metadataLLMBackend
}

// NewLLMScorer wraps a real *OpenAIParser for production use. A nil
// parser yields a scorer whose Score method always returns an error,
// which falls through to the next tier in scoreBaseCandidates.
func NewLLMScorer(parser *OpenAIParser) *LLMScorer {
	if parser == nil {
		return &LLMScorer{backend: nil}
	}
	return &LLMScorer{backend: parser}
}

// NewLLMScorerWithBackend is the test seam. Do not call from production.
func NewLLMScorerWithBackend(backend metadataLLMBackend) *LLMScorer {
	return &LLMScorer{backend: backend}
}

// Name implements MetadataCandidateScorer.
func (s *LLMScorer) Name() string { return "llm" }

// Score implements MetadataCandidateScorer.
func (s *LLMScorer) Score(ctx context.Context, q Query, cands []Candidate) ([]float64, error) {
	if len(cands) == 0 {
		return nil, nil
	}
	if s.backend == nil {
		return nil, fmt.Errorf("llm scorer: no backend configured")
	}

	query := MetadataLLMQuery{
		Title:    q.Title,
		Author:   q.Author,
		Narrator: q.Narrator,
	}
	llmCands := make([]MetadataLLMCandidate, len(cands))
	for i, c := range cands {
		llmCands[i] = MetadataLLMCandidate{
			Index:    i,
			Title:    c.Title,
			Author:   c.Author,
			Narrator: c.Narrator,
		}
	}

	raw, err := s.backend.ScoreMetadataCandidates(ctx, query, llmCands)
	if err != nil {
		return nil, fmt.Errorf("llm scorer: %w", err)
	}

	// Rehydrate scores into input order. Missing indices default to 0.0
	// (the caller should treat those as "use the base score instead").
	scores := make([]float64, len(cands))
	for _, r := range raw {
		if r.Index < 0 || r.Index >= len(scores) {
			continue
		}
		score := r.Score
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		scores[r.Index] = score
	}
	return scores, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/ai/ -run "TestLLMScorer" -v -count=1`
Expected: all 7 tests PASS.

- [ ] **Step 5: Run full ai package test suite**

Run: `go test ./internal/ai/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ai/llm_scorer.go internal/ai/llm_scorer_test.go
git commit -m "feat(ai): LLMScorer adapter satisfying MetadataCandidateScorer"
```

---

### Task 3: Add LLM scoring config keys

**Files:**
- Modify: `internal/config/config.go`

Three new keys matching the spec. Follow the same placement pattern PR 1 used for the embedding keys.

- [ ] **Step 1: Bump the file header version**

Find the line at the top of `internal/config/config.go` that reads `// version: X.Y.Z` and increment the minor. PR 1's version change landed at 1.32.0, so this task bumps to 1.33.0.

- [ ] **Step 2: Add the struct fields**

Find the `MetadataEmbeddingBestMatchMin` field (added in PR 1). Append these three fields immediately after it, with the same comment style:

```go
	MetadataEmbeddingBestMatchMin float64 `json:"metadata_embedding_best_match_min"` // default 0.70

	// Metadata LLM rerank tier (PR2)
	MetadataLLMScoringEnabled bool    `json:"metadata_llm_scoring_enabled"` // default false — opt-in, costs money
	MetadataLLMRerankEpsilon  float64 `json:"metadata_llm_rerank_epsilon"`  // default 0.01
	MetadataLLMRerankTopK     int     `json:"metadata_llm_rerank_top_k"`    // default 5
```

- [ ] **Step 3: Add defaults to the main initializer**

Find the block in `internal/config/config.go` around line 572-580 (the PR 1 embedding defaults; look for `AppConfig.MetadataEmbeddingScoringEnabled = true`). Append these three lines right after `AppConfig.MetadataEmbeddingBestMatchMin = 0.70`:

```go
	AppConfig.MetadataLLMScoringEnabled = false
	AppConfig.MetadataLLMRerankEpsilon = 0.01
	AppConfig.MetadataLLMRerankTopK = 5
```

- [ ] **Step 4: Add defaults to the ResetToDefaults struct literal**

Find the block around line 866-875 (the PR 1 embedding struct-literal defaults; look for `MetadataEmbeddingScoringEnabled: true,`). Append these three lines right after `MetadataEmbeddingBestMatchMin: 0.70,`:

```go
		// Metadata LLM rerank tier (PR2)
		MetadataLLMScoringEnabled: false,
		MetadataLLMRerankEpsilon:  0.01,
		MetadataLLMRerankTopK:     5,
```

- [ ] **Step 5: Build and verify**

Run: `go build ./...`
Expected: clean build, no errors.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(config): metadata LLM rerank config keys (enabled, epsilon, top-K)"
```

---

### Task 4: Add `llmScorer` field and setter on `MetadataFetchService`

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

Mirrors the PR 1 `metadataScorer` field + `SetMetadataScorer` pattern. The field holds the second scorer tier — the rerank pass uses it on top of whatever the base tier produced.

- [ ] **Step 1: Bump the file header version**

Find `// version: 4.44.0` (the PR 1 final version) near the top of `internal/server/metadata_fetch_service.go` and bump to `4.45.0`.

- [ ] **Step 2: Add the field**

Find the `metadataScorer ai.MetadataCandidateScorer` field on the `MetadataFetchService` struct. Add a sibling line immediately after it:

```go
type MetadataFetchService struct {
	db              database.Store
	olStore         *openlibrary.OLStore
	overrideSources []metadata.MetadataSource // for testing
	isbnEnrichment  *ISBNEnrichmentService
	activityService *ActivityService
	dedupEngine     *DedupEngine
	metadataScorer  ai.MetadataCandidateScorer // optional; nil = fallback to F1
	llmScorer       ai.MetadataCandidateScorer // optional; nil = no LLM rerank tier
}
```

- [ ] **Step 3: Add the setter**

Find `SetMetadataScorer` and add the sibling setter immediately after it:

```go
// SetMetadataLLMScorer injects the LLM rerank scorer. A nil scorer or a
// scorer that returns errors at runtime makes the rerank pass a no-op, so
// this method is safe to leave unset.
func (mfs *MetadataFetchService) SetMetadataLLMScorer(scorer ai.MetadataCandidateScorer) {
	mfs.llmScorer = scorer
}
```

- [ ] **Step 4: Build**

Run: `go build ./internal/server/`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/server/metadata_fetch_service.go
git commit -m "feat(metadata): add llmScorer field + SetMetadataLLMScorer setter"
```

---

### Task 5: Thread `useRerank` through `SearchMetadataForBook`

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`
- Modify: `internal/server/server.go`

The existing `SearchMetadataForBook(id, query string, authorHint ...string)` signature takes a variadic `authorHint` that today passes author/narrator/series in fixed positions. Adding `useRerank bool` as another variadic is ugly. Instead, add a new method `SearchMetadataForBookWithOptions(id, query, author, narrator, series string, opts SearchOptions)` and have the existing variadic wrapper call the new method with `SearchOptions{}` defaults. Backward-compatible, single source of truth.

- [ ] **Step 1: Add the `SearchOptions` type and new method**

Near the top of `metadata_fetch_service.go` (after the `SearchMetadataResponse` struct at around line 124), add:

```go
// SearchOptions carries optional per-request flags for SearchMetadataForBook.
// Adding a new option never breaks existing callers — they can keep using the
// zero-value or the simpler variadic signature.
type SearchOptions struct {
	// UseRerank asks the LLM rerank tier to run on the top candidates (if
	// MetadataLLMScoringEnabled is true on the server). When false, only
	// the base scorer tier runs.
	UseRerank bool
}
```

Find the current `SearchMetadataForBook` function at approximately line 1736. Rename the existing function body to a new method by changing the signature, and add a thin wrapper that preserves the old entry point. Concretely, replace:

```go
func (mfs *MetadataFetchService) SearchMetadataForBook(id string, query string, authorHint ...string) (*SearchMetadataResponse, error) {
```

with two functions — a new `SearchMetadataForBookWithOptions` that takes explicit parameters and has the full body, and a thin backward-compat wrapper named `SearchMetadataForBook`:

```go
// SearchMetadataForBook is the backward-compatible variadic entry point.
// New callers should prefer SearchMetadataForBookWithOptions — the variadic
// author/narrator/series positioning is historical and easy to get wrong.
func (mfs *MetadataFetchService) SearchMetadataForBook(id string, query string, authorHint ...string) (*SearchMetadataResponse, error) {
	var author, narrator, series string
	if len(authorHint) > 0 {
		author = authorHint[0]
	}
	if len(authorHint) > 1 {
		narrator = authorHint[1]
	}
	if len(authorHint) > 2 {
		series = authorHint[2]
	}
	return mfs.SearchMetadataForBookWithOptions(id, query, author, narrator, series, SearchOptions{})
}

// SearchMetadataForBookWithOptions is the canonical search entry point. The
// old variadic signature wraps this and passes default options. All new call
// sites should use this method directly so they can pass SearchOptions fields
// (UseRerank etc.) explicitly.
func (mfs *MetadataFetchService) SearchMetadataForBookWithOptions(
	id, query, author, narrator, series string,
	opts SearchOptions,
) (*SearchMetadataResponse, error) {
```

Then the entire existing body of the old function — from the `book, err := mfs.db.GetBookByID(id)` line onward — stays as the body of the new `SearchMetadataForBookWithOptions`. The variadic unpacking that used to happen inside the function at the top (searching for `authorHint[0]`, `authorHint[1]`, `authorHint[2]` references) must be removed from the new body because the parameters are now explicit. Grep for `authorHint` in the file to find those references and replace:

- `if len(authorHint) > 0 && authorHint[0] != ""` → `if author != ""`
- `authorHint[0]` → `author`
- References that use the second/third variadic slot for narrator/series → `narrator` / `series`

Read the existing body carefully and fix every `authorHint` reference. The compiler will catch any you miss.

- [ ] **Step 2: Thread `opts.UseRerank` into the rerank call site (stubbed)**

At the very end of `SearchMetadataForBookWithOptions`, just before the final `return &SearchMetadataResponse{...}, nil` statement, add the rerank invocation:

```go
	// Optional LLM rerank pass on the top ambiguous candidates.
	if opts.UseRerank && mfs.llmScorer != nil && config.AppConfig.MetadataLLMScoringEnabled {
		candidates = mfs.rerankTopK(context.Background(), book, candidates)
	}
```

`rerankTopK` will be defined in Task 6. For now this call site references it; the build will fail until Task 6 lands. That's fine — Task 6 is the next commit and we verify the full flow there.

- [ ] **Step 3: Update the search-metadata handler in `server.go`**

Find `searchAudiobookMetadata` at approximately line 7450. The current body reads the JSON body into `{query, author, narrator, series}` and calls `s.metadataFetchService.SearchMetadataForBook(id, body.Query, body.Author, body.Narrator, body.Series)`.

Replace that body with one that adds a `UseRerank` field and calls the new method:

```go
func (s *Server) searchAudiobookMetadata(c *gin.Context) {
	id := c.Param("id")
	if database.GlobalStore == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
		return
	}
	var body struct {
		Query     string `json:"query"`
		Author    string `json:"author"`
		Narrator  string `json:"narrator"`
		Series    string `json:"series"`
		UseRerank bool   `json:"use_rerank"`
	}
	_ = c.ShouldBindJSON(&body)

	// Cache metadata search results for 60s — external API calls are expensive.
	// use_rerank is part of the cache key so a rerank result and a non-rerank
	// result for the same search don't clobber each other.
	cacheKey := fmt.Sprintf("meta_search:%s:%s:%s:%s:%s:%t",
		id, body.Query, body.Author, body.Narrator, body.Series, body.UseRerank)
	if cached, ok := s.listCache.Get(cacheKey); ok {
		c.JSON(http.StatusOK, cached)
		return
	}

	resp, err := s.metadataFetchService.SearchMetadataForBookWithOptions(
		id, body.Query, body.Author, body.Narrator, body.Series,
		SearchOptions{UseRerank: body.UseRerank},
	)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	respH := gin.H{"results": resp.Results, "query": resp.Query, "sources_tried": resp.SourcesTried, "sources_failed": resp.SourcesFailed}
	s.listCache.Set(cacheKey, respH)
	c.JSON(http.StatusOK, resp)
}
```

Bump `server.go`'s file header version by one minor (find `// version:` at the top and increment).

- [ ] **Step 4: Build and expect the build to fail on `rerankTopK`**

Run: `go build ./internal/server/`
Expected: build FAILS with `mfs.rerankTopK undefined`. This is intentional — Task 6 defines it.

- [ ] **Step 5: Do not commit yet**

This task is half-committed-on-arrival. The changes from Steps 1-3 are staged but the build is red. Task 6 fixes the build in the next commit.

**Explicit: do NOT run `git commit` at the end of Task 5.** Task 6 will commit both Task 5's and Task 6's changes in a single logical commit so the repo never has a broken intermediate state.

Leave the working tree dirty and proceed to Task 6.

---

### Task 6: Implement `rerankTopK` method

**Files:**
- Modify: `internal/server/metadata_fetch_service.go`

This is the algorithm from the spec's "rerankTopK" section. Identify the ambiguous top, project them into `ai.Candidate`, call `mfs.llmScorer.Score`, replace the `Score` field of the top-K candidates with the LLM scores (bypassing the author/narrator/series bonus multiplication, since the LLM already sees those fields), and resort the full list.

- [ ] **Step 1: Add the method**

In `internal/server/metadata_fetch_service.go`, find the end of `bestTitleMatchForBook` (approximately line 1320 after Task 5's edits). Add a new method immediately after it:

```go
// rerankTopK asks the LLM scorer to re-judge the ambiguous top candidates
// after the base scorer has produced initial rankings. "Ambiguous" means
// candidates whose Score lands within MetadataLLMRerankEpsilon of the best
// candidate's Score. At most MetadataLLMRerankTopK candidates are sent to
// the LLM, even if more fall inside the epsilon window, to cap per-search
// cost.
//
// On success, the returned slice is the same candidates with updated Score
// values for the top-K slots, re-sorted descending by Score. On any failure
// (LLM disabled, backend error, fewer than 2 ambiguous candidates to resolve)
// the input slice is returned unchanged so the search path degrades cleanly.
func (mfs *MetadataFetchService) rerankTopK(
	ctx context.Context,
	book *database.Book,
	candidates []MetadataCandidate,
) []MetadataCandidate {
	if len(candidates) < 2 || mfs.llmScorer == nil {
		return candidates
	}

	// Sort descending by current score so the "ambiguous top" is contiguous
	// at index 0.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	epsilon := config.AppConfig.MetadataLLMRerankEpsilon
	topK := config.AppConfig.MetadataLLMRerankTopK
	if topK <= 0 {
		topK = 5
	}

	bestScore := candidates[0].Score
	ambiguousEnd := 1
	for ambiguousEnd < len(candidates) && ambiguousEnd < topK {
		if bestScore-candidates[ambiguousEnd].Score > epsilon {
			break
		}
		ambiguousEnd++
	}
	if ambiguousEnd < 2 {
		// Only one candidate within epsilon — nothing to resolve.
		log.Printf("[DEBUG] metadata-search: rerank skipped — only 1 candidate within %.3f of best (%.3f)",
			epsilon, bestScore)
		return candidates
	}

	topCands := candidates[:ambiguousEnd]
	log.Printf("[DEBUG] metadata-search: rerank firing on top %d candidates (epsilon=%.3f, bestScore=%.3f)",
		len(topCands), epsilon, bestScore)

	// Resolve the book's author name for the query payload.
	authorName := ""
	if book.AuthorID != nil {
		if author, err := mfs.db.GetAuthorByID(*book.AuthorID); err == nil && author != nil {
			authorName = author.Name
		}
	}
	query := ai.Query{
		BookID:   book.ID,
		Title:    book.Title,
		Author:   authorName,
		Narrator: derefStr(book.Narrator),
	}

	llmCands := make([]ai.Candidate, len(topCands))
	for i, c := range topCands {
		llmCands[i] = ai.Candidate{
			Title:    c.Title,
			Author:   c.Author,
			Narrator: c.Narrator,
		}
	}

	llmScores, err := mfs.llmScorer.Score(ctx, query, llmCands)
	if err != nil || len(llmScores) != len(topCands) {
		if err != nil {
			log.Printf("[WARN] metadata-search: rerank LLM call failed, keeping base scores: %v", err)
		} else {
			log.Printf("[WARN] metadata-search: rerank returned %d scores for %d candidates, keeping base scores",
				len(llmScores), len(topCands))
		}
		return candidates
	}

	// Replace top-K base scores with LLM scores directly — do not apply the
	// author/narrator/series bonus multipliers again. The LLM prompt already
	// sees those fields and judges them as part of its score; re-multiplying
	// would double-count the same evidence and distort the top-K's position
	// relative to the non-reranked tail.
	for i := range topCands {
		candidates[i].Score = llmScores[i]
	}

	// Resort the full list so the reranked top-K is in correct order against
	// the untouched tail.
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	return candidates
}
```

You also need to make sure `"sort"` is in the import block of `metadata_fetch_service.go`. Grep for `"sort"` in the import block — it may already be there from existing code. If not, add it.

- [ ] **Step 2: Build**

Run: `go build ./internal/server/`
Expected: clean build — the earlier reference from Task 5's `SearchMetadataForBookWithOptions` is now satisfied.

- [ ] **Step 3: Run existing metadata tests**

Run: `go test ./internal/server/ -run "TestMetadata|TestScore|TestBestTitle|TestSearchMetadata" -count=1 -timeout 60s`
Expected: all pre-existing tests pass. The new `rerankTopK` path isn't exercised yet — no test has `llmScorer` injected — but existing tests should be untouched.

- [ ] **Step 4: Commit Tasks 5+6 together**

Both tasks' changes are now on disk. This single commit captures the full "plumbing + method" landing so the tree never has a broken intermediate state.

```bash
git add internal/server/metadata_fetch_service.go internal/server/server.go
git commit -m "feat(metadata): thread use_rerank through search + rerankTopK method

- Add SearchOptions struct and SearchMetadataForBookWithOptions entry point
- Keep the old variadic SearchMetadataForBook as a backward-compat wrapper
- Plumb use_rerank from the search-metadata handler body through to the
  fetch service
- Implement rerankTopK: sorts by base score, identifies the ambiguous top
  (candidates within MetadataLLMRerankEpsilon of best, capped at
  MetadataLLMRerankTopK), calls the injected llmScorer, replaces the
  top-K base scores with LLM scores directly (skipping bonus
  multiplication), and resorts the full list
- Degrades to a no-op on any failure, nil scorer, or <2 candidates in
  the ambiguous window"
```

---

### Task 7: Unit tests for `rerankTopK`

**Files:**
- Modify: `internal/server/metadata_scoring_refactor_test.go`

The existing test file from PR 1 already has `scorerStub` (a fake `MetadataCandidateScorer`). We reuse it to drive the rerank logic end-to-end against an in-memory `MetadataFetchService`.

- [ ] **Step 1: Bump the test file version**

Find the `// version:` line at the top of `internal/server/metadata_scoring_refactor_test.go` and increment the minor.

- [ ] **Step 2: Append the tests**

Add these tests to the end of `metadata_scoring_refactor_test.go`:

```go
// TestRerankTopK_FiresOnAmbiguousTop checks that rerankTopK sends exactly the
// candidates within MetadataLLMRerankEpsilon of the best score to the LLM,
// and replaces their Score fields with the LLM's output.
func TestRerankTopK_FiresOnAmbiguousTop(t *testing.T) {
	// LLM says candidate 1 is actually the winner (0.95) even though
	// candidate 0 had a higher base score (0.90).
	llm := &scorerStub{
		name:   "llm",
		scores: []float64{0.60, 0.95}, // indices 0 and 1 of the ambiguous top
	}
	mfs := &MetadataFetchService{llmScorer: llm}

	prevEps := config.AppConfig.MetadataLLMRerankEpsilon
	prevK := config.AppConfig.MetadataLLMRerankTopK
	config.AppConfig.MetadataLLMRerankEpsilon = 0.05
	config.AppConfig.MetadataLLMRerankTopK = 5
	defer func() {
		config.AppConfig.MetadataLLMRerankEpsilon = prevEps
		config.AppConfig.MetadataLLMRerankTopK = prevK
	}()

	book := &database.Book{ID: "BOOK", Title: "Query"}
	candidates := []MetadataCandidate{
		{Title: "A", Score: 0.90},
		{Title: "B", Score: 0.88}, // within epsilon of 0.90 → rerank
		{Title: "C", Score: 0.70}, // outside epsilon → untouched
		{Title: "D", Score: 0.50}, // outside epsilon → untouched
	}

	got := mfs.rerankTopK(context.Background(), book, candidates)
	assert.Equal(t, 1, llm.callCount, "LLM should be called exactly once")
	require.Len(t, got, 4)

	// After rerank + resort, candidate B (0.95) should be first, A (0.60)
	// should be pushed down, C (0.70) and D (0.50) should be unchanged.
	assert.Equal(t, "B", got[0].Title)
	assert.InDelta(t, 0.95, got[0].Score, 0.0001)
	assert.Equal(t, "C", got[1].Title, "C's 0.70 should now outrank A's demoted 0.60")
	assert.InDelta(t, 0.70, got[1].Score, 0.0001)
	assert.Equal(t, "A", got[2].Title)
	assert.InDelta(t, 0.60, got[2].Score, 0.0001)
	assert.Equal(t, "D", got[3].Title)
	assert.InDelta(t, 0.50, got[3].Score, 0.0001)
}

// TestRerankTopK_HonorsTopKCap verifies the topK cap even when more
// candidates are within epsilon of the best.
func TestRerankTopK_HonorsTopKCap(t *testing.T) {
	llm := &scorerStub{
		name:   "llm",
		scores: []float64{0.90, 0.80, 0.70}, // 3 scores, matching topK=3
	}
	mfs := &MetadataFetchService{llmScorer: llm}

	prevEps := config.AppConfig.MetadataLLMRerankEpsilon
	prevK := config.AppConfig.MetadataLLMRerankTopK
	config.AppConfig.MetadataLLMRerankEpsilon = 0.50 // huge — everything is "ambiguous"
	config.AppConfig.MetadataLLMRerankTopK = 3
	defer func() {
		config.AppConfig.MetadataLLMRerankEpsilon = prevEps
		config.AppConfig.MetadataLLMRerankTopK = prevK
	}()

	book := &database.Book{ID: "BOOK", Title: "Query"}
	candidates := []MetadataCandidate{
		{Title: "A", Score: 0.85},
		{Title: "B", Score: 0.80},
		{Title: "C", Score: 0.75},
		{Title: "D", Score: 0.70}, // would be in epsilon but topK caps at 3
		{Title: "E", Score: 0.65},
	}

	mfs.rerankTopK(context.Background(), book, candidates)
	assert.Equal(t, 1, llm.callCount)
	// The stub received exactly 3 candidates — verify via scores slice length
	// we handed it (the stub returned a 3-element slice).
	assert.Len(t, llm.scores, 3)
}

// TestRerankTopK_NoAmbiguityReturnsUnchanged verifies that when only one
// candidate is within epsilon of the best, rerank is a no-op.
func TestRerankTopK_NoAmbiguityReturnsUnchanged(t *testing.T) {
	llm := &scorerStub{name: "llm", scores: []float64{0.9}}
	mfs := &MetadataFetchService{llmScorer: llm}

	prevEps := config.AppConfig.MetadataLLMRerankEpsilon
	config.AppConfig.MetadataLLMRerankEpsilon = 0.01
	defer func() { config.AppConfig.MetadataLLMRerankEpsilon = prevEps }()

	candidates := []MetadataCandidate{
		{Title: "A", Score: 0.95},
		{Title: "B", Score: 0.70}, // 0.25 below best → outside epsilon
		{Title: "C", Score: 0.50},
	}
	got := mfs.rerankTopK(context.Background(), &database.Book{ID: "B"}, candidates)
	assert.Equal(t, 0, llm.callCount, "LLM should not be called when only 1 candidate is ambiguous")
	assert.Equal(t, "A", got[0].Title)
	assert.InDelta(t, 0.95, got[0].Score, 0.0001)
}

// TestRerankTopK_NilScorerIsNoOp verifies the nil-scorer fallback.
func TestRerankTopK_NilScorerIsNoOp(t *testing.T) {
	mfs := &MetadataFetchService{llmScorer: nil}
	candidates := []MetadataCandidate{
		{Title: "A", Score: 0.95},
		{Title: "B", Score: 0.94},
	}
	got := mfs.rerankTopK(context.Background(), &database.Book{ID: "B"}, candidates)
	assert.Equal(t, "A", got[0].Title, "nil scorer should return candidates unchanged")
	assert.InDelta(t, 0.95, got[0].Score, 0.0001)
}

// TestRerankTopK_LLMErrorKeepsBaseScores verifies that a scorer error leaves
// the base scores untouched.
func TestRerankTopK_LLMErrorKeepsBaseScores(t *testing.T) {
	llm := &scorerStub{name: "llm", err: errors.New("openai boom")}
	mfs := &MetadataFetchService{llmScorer: llm}

	prevEps := config.AppConfig.MetadataLLMRerankEpsilon
	config.AppConfig.MetadataLLMRerankEpsilon = 0.10
	defer func() { config.AppConfig.MetadataLLMRerankEpsilon = prevEps }()

	candidates := []MetadataCandidate{
		{Title: "A", Score: 0.95},
		{Title: "B", Score: 0.92},
	}
	got := mfs.rerankTopK(context.Background(), &database.Book{ID: "B"}, candidates)
	assert.Equal(t, 1, llm.callCount)
	assert.Equal(t, "A", got[0].Title)
	assert.InDelta(t, 0.95, got[0].Score, 0.0001, "base score preserved on LLM error")
	assert.InDelta(t, 0.92, got[1].Score, 0.0001)
}
```

- [ ] **Step 3: Run the new tests**

Run: `go test ./internal/server/ -run "TestRerankTopK" -v -count=1`
Expected: all 5 tests PASS.

- [ ] **Step 4: Run the full metadata test suite**

Run: `go test ./internal/server/ -run "TestMetadata|TestScore|TestBestTitle|TestSearchMetadata|TestRerankTopK" -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/metadata_scoring_refactor_test.go
git commit -m "test(metadata): rerankTopK unit tests (firing, cap, no-op, error)"
```

---

### Task 8: Wire `LLMScorer` into server startup

**Files:**
- Modify: `internal/server/server.go`

Construct the `LLMScorer` alongside the existing `EmbeddingScorer` in the embedding/dedup init block and inject it via the setter from Task 4. The parser is already constructed inside that same block (the `llmParser` variable, reused here).

- [ ] **Step 1: Find the existing scorer injection block**

Grep: `grep -n "SetMetadataScorer" internal/server/server.go`. You'll land in the embedding init block where PR 1 wires the `EmbeddingScorer`. It looks like:

```go
server.metadataFetchService.SetMetadataScorer(
    ai.NewEmbeddingScorer(embedClient, embeddingStore),
)
log.Println("[INFO] Metadata candidate scoring: embedding tier enabled")
```

- [ ] **Step 2: Add the LLM scorer injection immediately after**

Add these lines right after the "embedding tier enabled" log:

```go
// Wire the LLM rerank scorer. It reuses the same llmParser the dedup
// engine uses for Layer 3 review. The scorer is injected
// unconditionally — the per-search `use_rerank` flag and the
// MetadataLLMScoringEnabled config key together gate whether it
// actually fires.
server.metadataFetchService.SetMetadataLLMScorer(ai.NewLLMScorer(llmParser))
if config.AppConfig.MetadataLLMScoringEnabled {
    log.Println("[INFO] Metadata candidate scoring: LLM rerank tier enabled (opt-in per search)")
} else {
    log.Println("[INFO] Metadata candidate scoring: LLM rerank tier wired but disabled in config")
}
```

- [ ] **Step 3: Bump the server.go header version**

Find `// version:` at the top of `internal/server/server.go` and increment the minor.

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go
git commit -m "feat(server): wire LLMScorer into MetadataFetchService at startup"
```

---

### Task 9: Frontend API — add `use_rerank` to `searchMetadataForBook`

**Files:**
- Modify: `web/src/services/api.ts`

- [ ] **Step 1: Bump the file header version**

Find `// version:` at the top of `web/src/services/api.ts` and increment the minor.

- [ ] **Step 2: Add the `useRerank` parameter**

Find `searchMetadataForBook` around line 2080. Replace the function with:

```typescript
export async function searchMetadataForBook(
  bookId: string,
  query?: string,
  author?: string,
  narrator?: string,
  series?: string,
  useRerank?: boolean
): Promise<SearchMetadataResponse> {
  const body: {
    query: string;
    author?: string;
    narrator?: string;
    series?: string;
    use_rerank?: boolean;
  } = { query: query || '' };
  if (author) body.author = author;
  if (narrator) body.narrator = narrator;
  if (series) body.series = series;
  if (useRerank) body.use_rerank = true;
  const response = await fetch(
    `${API_BASE}/audiobooks/${bookId}/search-metadata`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    }
  );
  if (!response.ok) {
    throw await buildApiError(response, 'Failed to search metadata');
  }
  return response.json();
}
```

- [ ] **Step 3: Type check**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no errors. If there are errors from the new parameter, they'll be in `BookDetail.tsx` (which we fix in Task 10) or in `MetadataSearchDialog.tsx` (which we fix in Task 10) — Task 9's isolated change should be backward-compatible since `useRerank` is optional.

- [ ] **Step 4: Commit**

```bash
git add web/src/services/api.ts
git commit -m "feat(api): add use_rerank param to searchMetadataForBook"
```

---

### Task 10: Add "AI rerank" toggle to MetadataSearchDialog

**Files:**
- Modify: `web/src/components/audiobooks/MetadataSearchDialog.tsx`

The dialog already has a search form. We add one `FormControlLabel` + `Switch` near the existing form fields labeled "AI rerank (higher quality, ~$0.003/search)", default OFF, and pass the state into the `api.searchMetadataForBook` call at line 133.

- [ ] **Step 1: Bump the file header version**

Find `// version:` at the top of `web/src/components/audiobooks/MetadataSearchDialog.tsx` and increment the minor.

- [ ] **Step 2: Add the state hook**

Find the existing `useState` declarations near the top of the `MetadataSearchDialog` component body (around line 90-110). Add a new state for the toggle:

```typescript
const [useRerank, setUseRerank] = useState(false);
```

- [ ] **Step 3: Update the `doSearch` callback to pass the flag**

Find the `doSearch` callback at line 128. Replace the `api.searchMetadataForBook` call:

```typescript
const resp = await api.searchMetadataForBook(
  book.id,
  searchQuery,
  author || undefined,
  narrator || undefined,
  series || undefined,
  useRerank || undefined
);
```

Add `useRerank` to the callback's dependency array at the bottom of the `useCallback`:

```typescript
}, [book, toast, useRerank]);
```

- [ ] **Step 4: Render the toggle in the search form**

Find the existing search form JSX where `author`, `narrator`, `series` inputs are rendered (search for the fields with `TextField` and `label="Narrator"` or similar). Add a `FormControlLabel` + `Switch` adjacent to them, labeled with the cost warning:

```tsx
<FormControlLabel
  control={
    <Switch
      size="small"
      checked={useRerank}
      onChange={(e) => setUseRerank(e.target.checked)}
    />
  }
  label="AI rerank (higher quality, ~$0.003/search)"
  sx={{ ml: 0 }}
/>
```

The exact placement is wherever the author/narrator/series fields are grouped — match the existing layout. If they're in a `<Stack>`, append the `FormControlLabel` as the last child of that `Stack`. Grep for `Narrator` in the file to find the nearby input blocks.

You may need to add `Switch` to the MUI imports at the top of the file if it's not already imported. Grep first:

```bash
grep -n "^import.*Switch\|Switch," web/src/components/audiobooks/MetadataSearchDialog.tsx
```

If `Switch` isn't in the imports, add it to the MUI named-import block. `FormControlLabel` is already imported (see line 18 of the current file — we confirmed this during plan prep).

- [ ] **Step 5: Type check**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/components/audiobooks/MetadataSearchDialog.tsx
git commit -m "feat(ui): add AI rerank toggle to metadata search dialog"
```

---

### Task 11: Add LLM rerank kill switch to Settings page

**Files:**
- Modify: `web/src/pages/Settings.tsx`

New toggle in the existing AI section next to `enableAIParsing`, wired to a new `metadataLLMScoringEnabled` local state that round-trips through the existing `config.enable_ai_parsing` / `AppConfig.EnableAIParsing` path. This is the server-wide kill switch that makes the per-search `use_rerank` flag reachable.

- [ ] **Step 1: Bump the file header version**

Find `// version:` at the top of `web/src/pages/Settings.tsx` and increment the minor.

- [ ] **Step 2: Add the state field**

Find the `Settings` type interface at around line 439 (search for `enableAIParsing: boolean`). Add a new field immediately after it:

```typescript
enableAIParsing: boolean;
metadataLLMScoringEnabled: boolean;
```

Find the default `settings` initializer (around line 553, `enableAIParsing: false,`). Add:

```typescript
enableAIParsing: false,
metadataLLMScoringEnabled: false,
```

Find the `loadSettings` effect where config is mapped to state (around line 730, `enableAIParsing: config.enable_ai_parsing ?? false,`). Add:

```typescript
enableAIParsing: config.enable_ai_parsing ?? false,
metadataLLMScoringEnabled: config.metadata_llm_scoring_enabled ?? false,
```

Find the save handler where state is mapped back to the config payload (around line 1378, `enable_ai_parsing: settings.enableAIParsing,`). Add:

```typescript
enable_ai_parsing: settings.enableAIParsing,
metadata_llm_scoring_enabled: settings.metadataLLMScoringEnabled,
```

- [ ] **Step 3: Add the toggle UI**

Find the existing `enableAIParsing` switch at around line 2530 (the `<FormControlLabel>` with `label="Enable AI-powered filename parsing"`). Add a new sibling `<Grid item xs={12}>` immediately after that one's closing `</Grid>`:

```tsx
<Grid item xs={12}>
  <FormControlLabel
    control={
      <Switch
        checked={settings.metadataLLMScoringEnabled}
        onChange={(e) =>
          handleChange('metadataLLMScoringEnabled', e.target.checked)
        }
      />
    }
    label="Enable AI rerank for metadata search (opt-in per search)"
  />
  <Alert severity="info" sx={{ mt: 1, mb: 2 }}>
    <Typography variant="caption">
      <strong>What is this?</strong> Allows users to request a
      higher-quality LLM rerank pass on ambiguous metadata search results.
      The per-search toggle in the search dialog is only effective when
      this server-wide switch is on. Adds approximately $0.003 per search
      when a user opts in.
    </Typography>
  </Alert>
</Grid>
```

- [ ] **Step 4: Update the TypeScript config type if needed**

Grep for the config interface that carries `enable_ai_parsing`:

```bash
grep -n "enable_ai_parsing" web/src/services/api.ts web/src/pages/Settings.tsx
```

If there's a TypeScript `interface` or `type` (likely named `Config`, `SystemConfig`, or `AppConfig`) that enumerates config fields, add `metadata_llm_scoring_enabled?: boolean;` to it. If the config is loaded as `Record<string, unknown>` or similar, no interface change is needed.

- [ ] **Step 5: Type check**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/pages/Settings.tsx
git commit -m "feat(ui): add LLM rerank server kill switch in Settings AI section"
```

---

### Task 12: Full build and full test suite

**Files:** none (verification task)

- [ ] **Step 1: Full project build**

Run: `make build-api`
Expected: clean build.

- [ ] **Step 2: Frontend type check**

Run: `cd web && npx tsc --noEmit && cd ..`
Expected: clean.

- [ ] **Step 3: Full backend test suite**

Run: `go test ./... -count=1 -timeout 240s`
Expected: all 35 packages PASS.

- [ ] **Step 4: If anything fails**

- Backend failures: grep the failing test name to find the file, read the failure, fix inline.
- Frontend failures: usually missing imports or stale type signatures — the `tsc` output will point at the offending file and line.
- Do NOT commit over failing tests. Fix them in place, re-run, then move on.

---

### Task 13: Deploy and manual verification

- [ ] **Step 1: Full frontend + backend build**

Run: `make build`
Expected: clean.

- [ ] **Step 2: Deploy to prod**

Run: `make deploy-debug`. If that hits the `LOCAL_ROOT` bug where scp reads from the main tree's `dist/` instead of the worktree's (same issue we hit in PRs #204 and #206), work around it by manually scp'ing from the worktree's `dist/` directory:

```bash
scp dist/audiobook-organizer-linux-amd64 <user>@unimatrixzero.local:/home/<user>/audiobook-organizer
ssh <user>@unimatrixzero.local 'sudo mv /home/<user>/audiobook-organizer /usr/local/bin/audiobook-organizer && sudo systemctl restart audiobook-organizer.service'
```

Expected: service restarts cleanly.

- [ ] **Step 3: Verify the LLM scorer is wired**

```bash
ssh <user>@unimatrixzero.local "journalctl -u audiobook-organizer --no-pager --since '1 min ago' | grep -E 'LLM rerank tier'"
```
Expected: one of
- `[INFO] Metadata candidate scoring: LLM rerank tier wired but disabled in config` (if the default of `false` stuck)
- `[INFO] Metadata candidate scoring: LLM rerank tier enabled (opt-in per search)` (if you flipped the Settings toggle before deploy)

- [ ] **Step 4: Turn on the server-wide kill switch via UI**

Open Settings, find the new "Enable AI rerank for metadata search" switch, flip it on, save. Verify the service log shows the config update was applied.

- [ ] **Step 5: Run a search with rerank off (baseline)**

Pick a book whose metadata search returns multiple close matches — a book in a numbered series is a good stress test because the base scorer can confuse volumes. Run a metadata search via the UI without the new AI rerank toggle. Note the top 3 candidates and their scores.

- [ ] **Step 6: Run the same search with rerank on**

Flip the new toggle in the search dialog, run the same search again. Watch the server logs in another terminal:

```bash
ssh <user>@unimatrixzero.local "journalctl -u audiobook-organizer -f | grep metadata-search"
```

Expected log lines:
- `metadata-search: scored N results from <source> with tier embedding`
- `metadata-search: rerank firing on top K candidates (epsilon=0.010, bestScore=0.XXX)` — if candidates are close enough to trigger
- OR `metadata-search: rerank skipped — only 1 candidate within 0.010 of best (X.XXX)` — if the base scorer is already confident enough

If the rerank fires, verify the final candidate order matches the LLM's judgment (it may differ slightly from the baseline run, which is the point).

- [ ] **Step 7: Verify fallback when LLM errors**

(Optional, but a good smoke test.) Temporarily put a bad value in the OpenAI API key via Settings, save, run a search with the AI rerank toggle on. Expected: search still returns candidates (base-tier scores are preserved), server logs show `[WARN] metadata-search: rerank LLM call failed, keeping base scores: ...`. Restore the real API key after.

- [ ] **Step 8: Create the PR**

```bash
git push -u origin feature/metadata-scorer-pr2
gh pr create --title "feat: LLM rerank tier for metadata candidate scoring (PR 2 of 2)" --body "$(cat <<'EOF'
## Summary

PR 2 of 2 in the metadata candidate scoring rollout. Adds the optional LLM rerank tier on top of the embedding scorer from PR 1. When the user flips the new "AI rerank" toggle in the metadata search dialog AND the server-wide "Enable AI rerank" Settings switch is on, the top ambiguous candidates (within \`MetadataLLMRerankEpsilon\` of the best base score, capped at \`MetadataLLMRerankTopK\`) are re-judged by gpt-5-mini with structured JSON output. The LLM scores replace the base scores for the top-K and the full list is re-sorted.

Uses the existing OpenAI key. No new vendors. No new endpoints. Per-search opt-in keeps cost visible.

## What's in this PR

**Backend:**
- \`internal/ai/metadata_llm_review.go\` — new \`OpenAIParser.ScoreMetadataCandidates\` method, same chat-completion + structured-JSON pattern as \`ReviewDedupPairs\`
- \`internal/ai/llm_scorer.go\` — \`LLMScorer\` adapter satisfying the \`MetadataCandidateScorer\` interface from PR 1
- \`internal/server/metadata_fetch_service.go\`:
  - \`SearchOptions\` struct + new \`SearchMetadataForBookWithOptions\` entry point (the old variadic \`SearchMetadataForBook\` stays as a backward-compat wrapper)
  - \`llmScorer\` field + \`SetMetadataLLMScorer\` setter
  - \`rerankTopK\` method: sort, identify ambiguous top, call LLM, replace base scores, resort
- \`internal/server/server.go\` — \`searchAudiobookMetadata\` handler reads \`use_rerank\` from the request body and threads it through; cache key includes the flag; startup wires \`LLMScorer\`
- \`internal/config/config.go\` — three new keys: \`MetadataLLMScoringEnabled\` (default false), \`MetadataLLMRerankEpsilon\` (default 0.01), \`MetadataLLMRerankTopK\` (default 5)

**Frontend:**
- \`web/src/services/api.ts\` — \`searchMetadataForBook\` gains an optional \`useRerank\` parameter
- \`web/src/components/audiobooks/MetadataSearchDialog.tsx\` — new "AI rerank (higher quality, ~\$0.003/search)" Switch in the search form, default off
- \`web/src/pages/Settings.tsx\` — new "Enable AI rerank for metadata search" toggle in the AI section, wired to \`metadata_llm_scoring_enabled\`

**Tests:**
- 2 interface-shape tests for the new AI types
- 7 \`LLMScorer\` unit tests (name, empty, ordering, out-of-order indices, missing index, clamping, backend error)
- 5 \`rerankTopK\` tests (firing, top-K cap, no-ambiguity no-op, nil-scorer no-op, LLM-error fallback)
- All existing tests still pass

## Scope decisions (from the design spec)

- **Starting \`MetadataLLMRerankEpsilon = 0.01\`** — ultra-conservative, triggers rerank only on very close races. Tune up 0.01 at a time as we learn how often it fires.
- **\`MetadataLLMRerankTopK = 5\`** — cost cap; even if 10 candidates are ambiguous, only the top 5 go to the LLM.
- **LLM scores bypass the author/narrator/series bonus multipliers** — the LLM prompt already sees those fields, so re-multiplying would double-count the same evidence.
- **Rerank is per-search opt-in** — default off, visible cost label. Server-wide kill switch in Settings.

## Cost & performance

- When rerank fires: ~\$0.003 per search, adds 2-5s latency
- At the 0.01 epsilon default, rerank triggers on ~1-5% of searches
- Monthly impact at 100 searches/day: under \$1

## Fallback guarantees

Every failure mode lands in the base-tier scores:
- Config disabled → rerank skipped
- \`useRerank\` false → rerank skipped
- Fewer than 2 candidates in the ambiguous window → skipped
- LLM returns error → logged, base scores preserved
- LLM returns wrong count → logged, base scores preserved

## Deploy verification

Deployed to prod, confirmed:
- \`Metadata candidate scoring: LLM rerank tier enabled (opt-in per search)\` log line fires at startup when the Settings switch is on
- Real search with rerank on produces \`rerank firing on top K candidates\` log lines
- Final candidate ordering reflects the LLM's judgment

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 9: Report the PR URL back to the user**

Return the PR URL so the user can review and merge.
