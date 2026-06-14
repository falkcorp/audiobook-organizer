// file: internal/ai/embedding_client.go
// version: 1.5.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-06-14

package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// EmbeddingCache is the minimal surface EmbeddingClient needs from
// a content-hash cache layer. It's an interface (not a concrete
// type) so internal/ai doesn't need to import internal/database,
// which would create a circular dependency. Production wires the
// *database.EmbeddingStore implementation at server startup.
//
// Get returns (nil, nil) on a cache miss so callers can use the
// two-valued result without needing a sentinel error constant.
// Put is called with the newly-embedded vectors after a successful
// API call; a Put failure is logged but never fatal because the
// cache is an optimization, not a correctness requirement.
//
// Added after the 2026-04-11 OpenAI quota incident: the metadata
// scorer was re-embedding every candidate on every fetch — no
// content-hash cache existed — and burned the entire monthly
// budget in minutes.
type EmbeddingCache interface {
	GetCachedEmbedding(textHash, model string) ([]float32, error)
	PutCachedEmbedding(textHash, model string, vector []float32) error
}

// EmbeddingClient handles OpenAI embedding generation. Optional
// content-hash cache (see EmbeddingCache) short-circuits repeat
// embeds of identical text so a bulk fetch of 10K books only
// hits the API for unique title+author+narrator combinations.
type EmbeddingClient struct {
	client *openai.Client
	model  string
	cache  EmbeddingCache

	// requestTimeout bounds each individual Embeddings.New call
	// (one per retry attempt). Without this, a single hung request
	// blocks the whole FullScan for the lifetime of the parent ctx
	// (up to 120 min). Set via WithRequestTimeout; default 30s.
	requestTimeout time.Duration

	// rawEmbed is the underlying API-call function. It defaults
	// to c.embedBatchRaw which hits OpenAI; tests override it
	// with a fake so the cache-partitioning logic can be
	// exercised without a real API key.
	rawEmbed func(ctx context.Context, texts []string) ([][]float32, error)
}

// defaultRequestTimeout is the per-attempt timeout applied to each
// Embeddings.New call in embedBatchRaw. 30 s is enough for any batch ≤ 64
// inputs on a healthy network; on an unreachable endpoint it triggers after
// 30 s instead of blocking for the full operation timeout (up to 120 min).
const defaultRequestTimeout = 30 * time.Second

// NewEmbeddingClient creates a new embedding client using the given API key.
// Default model is text-embedding-3-large. The returned client has no cache
// wired up — call WithCache after construction to enable content-hash caching.
func NewEmbeddingClient(apiKey string) *EmbeddingClient {
	clientOptions := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		clientOptions = append(clientOptions, option.WithBaseURL(baseURL))
	}

	client := openai.NewClient(clientOptions...)
	c := &EmbeddingClient{
		client:         &client,
		model:          "text-embedding-3-large",
		requestTimeout: defaultRequestTimeout,
	}
	c.rawEmbed = c.embedBatchRaw
	return c
}

// WithRequestTimeout overrides the per-attempt timeout for each
// Embeddings.New call. A value ≤ 0 is silently replaced with the
// defaultRequestTimeout (30 s) so the client never blocks forever.
// Safe to call at any time; subsequent EmbedBatch calls use the new value.
func (c *EmbeddingClient) WithRequestTimeout(d time.Duration) *EmbeddingClient {
	if d <= 0 {
		d = defaultRequestTimeout
	}
	c.requestTimeout = d
	return c
}

// WithCache attaches a content-hash cache to the client. Safe to
// call at any time — subsequent EmbedBatch calls start using the
// cache immediately. Pass nil to disable caching (reverts to
// uncached behavior).
func (c *EmbeddingClient) WithCache(cache EmbeddingCache) *EmbeddingClient {
	c.cache = cache
	return c
}

// SetRawEmbedForTest replaces the underlying API-call function with a fake.
// Test-only; production code uses the OpenAI-backed default wired in
// NewEmbeddingClient. Cross-package tests need this because rawEmbed is
// unexported, and the dedup engine holds a concrete *EmbeddingClient (not
// an interface) so a stub-by-interface substitution isn't available.
func (c *EmbeddingClient) SetRawEmbedForTest(fn func(ctx context.Context, texts []string) ([][]float32, error)) {
	c.rawEmbed = fn
}

// Model returns the embedding model name this client is pinned
// to. Used by cache-aware callers that need to compose cache
// keys.
func (c *EmbeddingClient) Model() string {
	return c.model
}

// EmbedBatch returns one []float32 per input text in the same
// order. If a content-hash cache is attached (see WithCache),
// inputs are partitioned into cache hits and misses: hits are
// served from the cache with zero API cost, misses are sent to
// OpenAI in a single batch and the results are written back to
// the cache before being returned.
//
// Retries up to 3 times with exponential backoff (1s, 4s) on
// API errors. Cache I/O errors are logged but never fail the
// call — the cache is an optimization, not a correctness layer.
func (c *EmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Partition texts into cache hits and misses. `results` keeps
	// the original order; `missIndices` remembers which slots in
	// `results` are still empty and need an API call. `missTexts`
	// is the compact list we actually send to OpenAI.
	results := make([][]float32, len(texts))
	var missIndices []int
	var missTexts []string
	hits := 0

	if c.cache != nil {
		for i, text := range texts {
			hash := TextHash(text)
			vec, err := c.cache.GetCachedEmbedding(hash, c.model)
			if err != nil {
				// Cache read failure — treat as miss, log once.
				slog.Warn("embedding cache get failed (hash)", "value0", "value0", "value1", hash[:8], "err", err)
			}
			if err == nil && vec != nil {
				results[i] = vec
				hits++
				continue
			}
			missIndices = append(missIndices, i)
			missTexts = append(missTexts, text)
		}
	} else {
		missIndices = make([]int, len(texts))
		missTexts = texts
		for i := range texts {
			missIndices[i] = i
		}
	}

	// All cache hits? Return without touching the API at all.
	if len(missTexts) == 0 {
		slog.Debug("embedding cache / hits, 0 API calls", "value0", "hits", "hits", hits, "value1", len(texts))
		return results, nil
	}

	if c.cache != nil {
		slog.Debug("embedding cache / hits, misses sent to API", "hits", hits, "total", len(texts), "misses", len(missTexts))
	}

	apiResults, err := c.rawEmbed(ctx, missTexts)
	if err != nil {
		return nil, err
	}
	if len(apiResults) != len(missTexts) {
		return nil, fmt.Errorf("embedding API returned %d results for %d inputs",
			len(apiResults), len(missTexts))
	}

	// Stitch API results back into the original positions and
	// write them to the cache for next time.
	for j, vec := range apiResults {
		origIdx := missIndices[j]
		results[origIdx] = vec
		if c.cache != nil {
			hash := TextHash(missTexts[j])
			if putErr := c.cache.PutCachedEmbedding(hash, c.model, vec); putErr != nil {
				slog.Warn("embedding cache put failed (hash)", "value0", "value0", "value1", hash[:8], "putErr", putErr)
			}
		}
	}
	return results, nil
}

// embedBatchRaw is the actual OpenAI API call with retries —
// EmbedBatch handles the cache partitioning around it. Split out
// so the caching logic can call it with just the miss set.
//
// Each attempt is wrapped in a child context bounded by c.requestTimeout
// so a hung TCP connection fails in ≤ requestTimeout (default 30 s) rather
// than blocking until the parent ctx deadline (up to 120 min op timeout).
// The parent ctx is still respected — if it is cancelled between attempts the
// retry loop exits immediately.
func (c *EmbeddingClient) embedBatchRaw(ctx context.Context, texts []string) ([][]float32, error) {
	var lastErr error
	delays := []time.Duration{1 * time.Second, 4 * time.Second}

	// Resolve the per-attempt timeout, guarding against a zero value that
	// would produce an already-expired context.
	reqTimeout := c.requestTimeout
	if reqTimeout <= 0 {
		reqTimeout = defaultRequestTimeout
	}

	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			delay := delays[attempt-1]
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		// Wrap each attempt in its own bounded child context so a hung
		// network call times out in reqTimeout rather than waiting for the
		// (potentially very long) parent op context.
		attemptCtx, attemptCancel := context.WithTimeout(ctx, reqTimeout)
		resp, err := c.client.Embeddings.New(attemptCtx, openai.EmbeddingNewParams{
			Input: openai.EmbeddingNewParamsInputUnion{
				OfArrayOfStrings: texts,
			},
			Model: openai.EmbeddingModel(c.model),
			User:  openai.String("ao-embeddings"),
		})
		attemptCancel() // always release the child context, never defer in a loop
		if err != nil {
			lastErr = fmt.Errorf("embedding attempt %d: %w", attempt+1, err)
			continue
		}

		// Allocate result slice sized to number of returned embeddings
		results := make([][]float32, len(resp.Data))
		for _, item := range resp.Data {
			idx := int(item.Index)
			if idx < 0 || idx >= len(results) {
				return nil, fmt.Errorf("embedding response index %d out of range (len=%d)", idx, len(results))
			}
			f32 := make([]float32, len(item.Embedding))
			for j, v := range item.Embedding {
				f32[j] = float32(v)
			}
			results[idx] = f32
		}
		return results, nil
	}

	return nil, lastErr
}

// EmbedOne is a convenience wrapper that embeds a single text string.
//
// EmbedOne goes through EmbedBatch, which transparently consults the
// content-hash cache attached via WithCache. So callers DO get the cache
// for free — there is no separate cache path that needs to be wired here.
// (This note exists because the question keeps coming up during reviews:
// "EmbedOne doesn't touch the cache, that's a bug." It is not. EmbedBatch
// is the single source of truth for cache logic, and EmbedOne is a thin
// shim over it.)
func (c *EmbeddingClient) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	results, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no embedding returned")
	}
	return results[0], nil
}

// BuildEmbeddingText constructs a human-readable text representation of an entity
// suitable for embedding. entityType may be "book" or "author".
//
// Prefer BuildBookEmbeddingText for books — it adds series + sequence so two
// volumes of the same series ("Foo, Book 3" vs "Foo, Book 4") produce vectors
// that actually differ. Without that, near-identical titles collapse into a
// dense cluster of false ~100% cosine matches.
func BuildEmbeddingText(entityType, title, author, narrator string) string {
	switch entityType {
	case "book":
		if narrator != "" {
			return fmt.Sprintf("%s by %s narrated by %s", title, author, narrator)
		}
		return fmt.Sprintf("%s by %s", title, author)
	case "author":
		return title
	default:
		return title
	}
}

// BuildBookEmbeddingText is the book-specific variant that incorporates series
// and sequence. Use this whenever the caller has a Book record. Including the
// series name and number in the embedding text materially separates volumes of
// a series in vector space — without it, "Reclaiming Honor by John Smith" and
// the same string for book 7 produce identical embeddings and the dedup
// engine reports a spurious 100% match across the whole series.
//
// Callers that don't have series info (e.g. external metadata candidates that
// haven't been resolved yet) should pass empty strings; the output then
// matches BuildEmbeddingText("book", ...).
func BuildBookEmbeddingText(title, author, narrator, seriesName, seriesSequence string) string {
	base := BuildEmbeddingText("book", title, author, narrator)
	switch {
	case seriesName != "" && seriesSequence != "":
		return fmt.Sprintf("%s (%s #%s)", base, seriesName, seriesSequence)
	case seriesName != "":
		return fmt.Sprintf("%s (%s)", base, seriesName)
	case seriesSequence != "":
		return fmt.Sprintf("%s (#%s)", base, seriesSequence)
	default:
		return base
	}
}

// TextHash returns the SHA-256 hex digest of the input string (64 characters).
func TextHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
