// file: internal/ai/embedding_client_test.go
// version: 1.3.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678901

package ai

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewEmbeddingClientWithOptions_Model verifies the explicit-options
// constructor pins the requested model and falls back to the default when the
// model is empty (a zero-valued config field must never yield an empty model).
func TestNewEmbeddingClientWithOptions_Model(t *testing.T) {
	c := NewEmbeddingClientWithOptions("k", "bge-m3", "http://127.0.0.1:11434/v1")
	assert.Equal(t, "bge-m3", c.Model())

	cDefault := NewEmbeddingClientWithOptions("k", "", "")
	assert.Equal(t, defaultEmbeddingModel, cDefault.Model(), "empty model must fall back to the default")
}

// TestNewEmbeddingClient_BackwardCompat verifies the legacy constructor keeps
// the default model and still honors the OPENAI_BASE_URL env (existing setups
// must not regress). We can't observe the SDK's base URL directly, but we can
// confirm construction succeeds with the env set and the model is unchanged.
func TestNewEmbeddingClient_BackwardCompat(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://example.invalid/v1")
	c := NewEmbeddingClient("k")
	assert.Equal(t, defaultEmbeddingModel, c.Model())
	// Sanity: the env path is exercised (no panic), and clearing it also works.
	_ = os.Unsetenv("OPENAI_BASE_URL")
	c2 := NewEmbeddingClient("k")
	assert.Equal(t, defaultEmbeddingModel, c2.Model())
}

func TestBuildEmbeddingText_Book(t *testing.T) {
	result := BuildEmbeddingText("book", "The Way of Kings", "Brandon Sanderson", "Michael Kramer")
	assert.Equal(t, "The Way of Kings by Brandon Sanderson narrated by Michael Kramer", result)
}

func TestBuildEmbeddingText_BookNoNarrator(t *testing.T) {
	result := BuildEmbeddingText("book", "Dune", "Frank Herbert", "")
	assert.Equal(t, "Dune by Frank Herbert", result)
}

func TestBuildEmbeddingText_Author(t *testing.T) {
	result := BuildEmbeddingText("author", "Brandon Sanderson", "", "")
	assert.Equal(t, "Brandon Sanderson", result)
}

func TestBuildBookEmbeddingText(t *testing.T) {
	cases := []struct {
		name, title, author, narrator, series, seq, want string
	}{
		{
			name:  "series and sequence",
			title: "Words of Radiance", author: "Brandon Sanderson", narrator: "Michael Kramer",
			series: "Stormlight Archive", seq: "2",
			want: "Words of Radiance by Brandon Sanderson narrated by Michael Kramer (Stormlight Archive #2)",
		},
		{
			name:  "series only",
			title: "Words of Radiance", author: "Brandon Sanderson", narrator: "",
			series: "Stormlight Archive", seq: "",
			want: "Words of Radiance by Brandon Sanderson (Stormlight Archive)",
		},
		{
			name:  "sequence only",
			title: "Words of Radiance", author: "Brandon Sanderson", narrator: "",
			series: "", seq: "2",
			want: "Words of Radiance by Brandon Sanderson (#2)",
		},
		{
			name:  "neither falls back to base",
			title: "Dune", author: "Frank Herbert", narrator: "",
			series: "", seq: "",
			want: "Dune by Frank Herbert",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildBookEmbeddingText(tc.title, tc.author, tc.narrator, tc.series, tc.seq)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTextHash(t *testing.T) {
	h1 := TextHash("hello world")
	h2 := TextHash("hello world")
	h3 := TextHash("different input")

	assert.Equal(t, h1, h2, "same input should produce same hash")
	assert.NotEqual(t, h1, h3, "different input should produce different hash")
	assert.Len(t, h1, 64, "SHA-256 hex digest should be 64 characters")
}

// fakeEmbeddingCache is a thread-safe in-memory EmbeddingCache
// for tests. Records every Get/Put so tests can assert call
// counts and keys.
type fakeEmbeddingCache struct {
	mu       sync.Mutex
	entries  map[string][]float32
	getCalls int
	putCalls int
	getErr   error
}

func newFakeCache() *fakeEmbeddingCache {
	return &fakeEmbeddingCache{entries: make(map[string][]float32)}
}

func (f *fakeEmbeddingCache) key(hash, model string) string { return model + ":" + hash }

func (f *fakeEmbeddingCache) GetCachedEmbedding(hash, model string) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.entries[f.key(hash, model)], nil
}

func (f *fakeEmbeddingCache) PutCachedEmbedding(hash, model string, vec []float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putCalls++
	if f.entries == nil {
		f.entries = make(map[string][]float32)
	}
	f.entries[f.key(hash, model)] = vec
	return nil
}

// newClientWithFakeAPI returns an EmbeddingClient whose API seam
// is replaced with a fake that returns one deterministic vector
// per input and counts calls. Model is "test" so cache keys
// stay isolated from production entries.
func newClientWithFakeAPI() (*EmbeddingClient, *int) {
	calls := 0
	c := &EmbeddingClient{model: "test"}
	c.rawEmbed = func(ctx context.Context, texts []string) ([][]float32, error) {
		calls++
		out := make([][]float32, len(texts))
		for i, text := range texts {
			// Return a trivially-unique vector per text so the
			// test can verify the right vectors reached the
			// right slots without depending on real embedding
			// math.
			out[i] = []float32{float32(len(text)), float32(i)}
		}
		return out, nil
	}
	return c, &calls
}

func TestEmbedBatch_NoCache_CallsAPIOnce(t *testing.T) {
	c, apiCalls := newClientWithFakeAPI()

	results, err := c.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, 1, *apiCalls, "no cache → one API call")
}

func TestEmbedBatch_AllHits_ZeroAPICalls(t *testing.T) {
	c, apiCalls := newClientWithFakeAPI()
	cache := newFakeCache()
	// Pre-populate the cache for every input.
	cache.entries[cache.key(TextHash("a"), "test")] = []float32{1, 2, 3}
	cache.entries[cache.key(TextHash("b"), "test")] = []float32{4, 5, 6}
	c.WithCache(cache)

	results, err := c.EmbedBatch(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, []float32{1, 2, 3}, results[0])
	assert.Equal(t, []float32{4, 5, 6}, results[1])
	assert.Equal(t, 0, *apiCalls, "all cache hits → zero API calls")
	assert.Equal(t, 0, cache.putCalls, "hits should not trigger puts")
}

func TestEmbedBatch_MixedHitsAndMisses_APICalledOnceForMisses(t *testing.T) {
	c, apiCalls := newClientWithFakeAPI()
	cache := newFakeCache()
	// Pre-populate the cache for inputs 0 and 2 but not 1 and 3.
	cache.entries[cache.key(TextHash("hit-0"), "test")] = []float32{10}
	cache.entries[cache.key(TextHash("hit-2"), "test")] = []float32{20}
	c.WithCache(cache)

	inputs := []string{"hit-0", "miss-1", "hit-2", "miss-3"}
	results, err := c.EmbedBatch(context.Background(), inputs)
	require.NoError(t, err)
	require.Len(t, results, 4)
	// Hits were served from the cache at their original positions.
	assert.Equal(t, []float32{10}, results[0])
	assert.Equal(t, []float32{20}, results[2])
	// Misses got fresh vectors from the fake API seam.
	assert.NotNil(t, results[1])
	assert.NotNil(t, results[3])
	// Exactly one batched API call for the two misses.
	assert.Equal(t, 1, *apiCalls)
	// Both misses got written back to the cache.
	assert.Equal(t, 2, cache.putCalls)
	_, got1 := cache.entries[cache.key(TextHash("miss-1"), "test")]
	_, got3 := cache.entries[cache.key(TextHash("miss-3"), "test")]
	assert.True(t, got1, "miss-1 should be cached after API call")
	assert.True(t, got3, "miss-3 should be cached after API call")
}

func TestEmbedBatch_CacheGetError_FallsBackToAPI(t *testing.T) {
	// A cache read failure should never fail the call — the client
	// treats it as a miss and proceeds to the API. Production
	// behavior: cache is an optimization, not a correctness layer.
	c, apiCalls := newClientWithFakeAPI()
	cache := newFakeCache()
	cache.getErr = errors.New("simulated cache read failure")
	c.WithCache(cache)

	results, err := c.EmbedBatch(context.Background(), []string{"a", "b"})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, 1, *apiCalls)
}

func TestEmbedBatch_EmptyInput_NoAPICallNoError(t *testing.T) {
	c, apiCalls := newClientWithFakeAPI()
	c.WithCache(newFakeCache())

	results, err := c.EmbedBatch(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, results)
	assert.Equal(t, 0, *apiCalls)
}
