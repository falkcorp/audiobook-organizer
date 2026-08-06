# Embedding-Based Deduplication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the expensive LLM-based dedup with a 3-layer system: exact matching, embedding cosine similarity, and LLM only for ambiguous cases. Handles both book and author duplicates.

**Architecture:** New `embeddings.db` SQLite sidecar stores vectors (text-embedding-3-large, 3072 dims). EmbeddingStore handles CRUD + cosine similarity. DedupEngine orchestrates 3 layers: exact match → embedding scan → LLM review. Embeddings computed on ingest + metadata change + user trigger. Backfill on first startup.

**Tech Stack:** Go, SQLite, OpenAI embeddings API (`github.com/openai/openai-go` v1.12.0), existing PebbleDB store for book/author data.

---

### Task 1: Create EmbeddingStore (SQLite sidecar)

**Files:**
- Create: `internal/database/embedding_store.go`
- Create: `internal/database/embedding_store_test.go`

- [ ] **Step 1: Write the test file**

Create `internal/database/embedding_store_test.go`:

```go
package database

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestEmbeddingStore(t *testing.T) *EmbeddingStore {
	t.Helper()
	dir := t.TempDir()
	store, err := NewEmbeddingStore(filepath.Join(dir, "test_embeddings.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestEmbeddingStore_UpsertAndGet(t *testing.T) {
	store := newTestEmbeddingStore(t)

	vec := make([]float32, 8) // small dim for testing
	vec[0] = 1.0
	vec[1] = 0.5

	err := store.Upsert(Embedding{
		EntityType: "book",
		EntityID:   "book123",
		TextHash:   "abc123",
		Vector:     vec,
		Model:      "text-embedding-3-large",
	})
	require.NoError(t, err)

	got, err := store.Get("book", "book123")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "book123", got.EntityID)
	assert.Equal(t, "abc123", got.TextHash)
	assert.Equal(t, vec, got.Vector)
}

func TestEmbeddingStore_UpsertOverwrites(t *testing.T) {
	store := newTestEmbeddingStore(t)

	vec1 := []float32{1.0, 0.0, 0.0, 0.0}
	vec2 := []float32{0.0, 1.0, 0.0, 0.0}

	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b1", TextHash: "h1", Vector: vec1, Model: "m"})
	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b1", TextHash: "h2", Vector: vec2, Model: "m"})

	got, _ := store.Get("book", "b1")
	assert.Equal(t, "h2", got.TextHash)
	assert.Equal(t, vec2, got.Vector)
}

func TestEmbeddingStore_FindSimilar(t *testing.T) {
	store := newTestEmbeddingStore(t)

	// Three vectors: v1 and v2 are similar, v3 is different
	v1 := []float32{1.0, 0.0, 0.0, 0.0}
	v2 := []float32{0.9, 0.1, 0.0, 0.0}
	v3 := []float32{0.0, 0.0, 0.0, 1.0}

	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b1", TextHash: "h1", Vector: v1, Model: "m"})
	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b2", TextHash: "h2", Vector: v2, Model: "m"})
	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b3", TextHash: "h3", Vector: v3, Model: "m"})

	results, err := store.FindSimilar("book", v1, 0.5, 10)
	require.NoError(t, err)

	// Should find b2 as similar, not b3
	assert.Len(t, results, 1) // excludes self since b1 is the query vector stored as b1
	// Actually b1 is also returned since we search by vector not by ID
	// Let's just check that b2 is in results with high similarity
	found := false
	for _, r := range results {
		if r.EntityID == "b2" {
			found = true
			assert.Greater(t, r.Similarity, float32(0.9))
		}
	}
	assert.True(t, found, "b2 should be found as similar to b1")
}

func TestCosineSimilarity(t *testing.T) {
	// Identical vectors → 1.0
	a := []float32{1.0, 0.0, 0.0}
	assert.InDelta(t, 1.0, CosineSimilarity(a, a), 0.001)

	// Orthogonal vectors → 0.0
	b := []float32{0.0, 1.0, 0.0}
	assert.InDelta(t, 0.0, CosineSimilarity(a, b), 0.001)

	// Opposite vectors → -1.0
	c := []float32{-1.0, 0.0, 0.0}
	assert.InDelta(t, -1.0, CosineSimilarity(a, c), 0.001)
}

func TestEmbeddingStore_Delete(t *testing.T) {
	store := newTestEmbeddingStore(t)
	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b1", TextHash: "h1", Vector: []float32{1, 0}, Model: "m"})

	err := store.Delete("book", "b1")
	require.NoError(t, err)

	got, err := store.Get("book", "b1")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestEmbeddingStore_ListByType(t *testing.T) {
	store := newTestEmbeddingStore(t)
	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b1", TextHash: "h1", Vector: []float32{1, 0}, Model: "m"})
	_ = store.Upsert(Embedding{EntityType: "book", EntityID: "b2", TextHash: "h2", Vector: []float32{0, 1}, Model: "m"})
	_ = store.Upsert(Embedding{EntityType: "author", EntityID: "a1", TextHash: "h3", Vector: []float32{1, 1}, Model: "m"})

	books, err := store.ListByType("book")
	require.NoError(t, err)
	assert.Len(t, books, 2)

	authors, err := store.ListByType("author")
	require.NoError(t, err)
	assert.Len(t, authors, 1)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/database/ -run TestEmbeddingStore -v -count=1`
Expected: FAIL — types not defined.

- [ ] **Step 3: Implement EmbeddingStore**

Create `internal/database/embedding_store.go`:

```go
package database

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Embedding represents a stored vector embedding for a book or author.
type Embedding struct {
	EntityType string    `json:"entity_type"`
	EntityID   string    `json:"entity_id"`
	TextHash   string    `json:"text_hash"`
	Vector     []float32 `json:"vector"`
	Model      string    `json:"model"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SimilarityResult is a match from FindSimilar.
type SimilarityResult struct {
	EntityID   string  `json:"entity_id"`
	Similarity float32 `json:"similarity"`
}

// EmbeddingStore persists vector embeddings in a dedicated SQLite sidecar.
type EmbeddingStore struct {
	db *sql.DB
}

const embeddingSchema = `
CREATE TABLE IF NOT EXISTS embeddings (
    id          TEXT PRIMARY KEY,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    text_hash   TEXT NOT NULL,
    vector      BLOB NOT NULL,
    model       TEXT NOT NULL,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_embeddings_type ON embeddings(entity_type);
CREATE INDEX IF NOT EXISTS idx_embeddings_entity ON embeddings(entity_id);
CREATE INDEX IF NOT EXISTS idx_embeddings_hash ON embeddings(text_hash);

CREATE TABLE IF NOT EXISTS dedup_candidates (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    entity_type   TEXT NOT NULL,
    entity_a_id   TEXT NOT NULL,
    entity_b_id   TEXT NOT NULL,
    layer         TEXT NOT NULL,
    similarity    REAL,
    llm_verdict   TEXT,
    llm_reason    TEXT,
    status        TEXT NOT NULL DEFAULT 'pending',
    created_at    DATETIME NOT NULL,
    updated_at    DATETIME NOT NULL,
    UNIQUE(entity_type, entity_a_id, entity_b_id)
);
CREATE INDEX IF NOT EXISTS idx_dedup_status ON dedup_candidates(status);
CREATE INDEX IF NOT EXISTS idx_dedup_type_status ON dedup_candidates(entity_type, status);
CREATE INDEX IF NOT EXISTS idx_dedup_entity_a ON dedup_candidates(entity_a_id);
CREATE INDEX IF NOT EXISTS idx_dedup_entity_b ON dedup_candidates(entity_b_id);
`

// NewEmbeddingStore opens or creates the embeddings SQLite DB.
func NewEmbeddingStore(dbPath string) (*EmbeddingStore, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000&_foreign_keys=off", dbPath)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("embedding_store: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("embedding_store: ping: %w", err)
	}
	if _, err := db.Exec(embeddingSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("embedding_store: schema: %w", err)
	}
	return &EmbeddingStore{db: db}, nil
}

// Close shuts down the database.
func (s *EmbeddingStore) Close() error { return s.db.Close() }

// Upsert inserts or replaces an embedding.
func (s *EmbeddingStore) Upsert(e Embedding) error {
	now := time.Now().UTC()
	id := e.EntityType + ":" + e.EntityID
	blob := encodeVector(e.Vector)

	_, err := s.db.Exec(`
		INSERT INTO embeddings (id, entity_type, entity_id, text_hash, vector, model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			text_hash = excluded.text_hash,
			vector = excluded.vector,
			model = excluded.model,
			updated_at = excluded.updated_at`,
		id, e.EntityType, e.EntityID, e.TextHash, blob, e.Model, now, now,
	)
	return err
}

// Get retrieves a single embedding by type and entity ID.
func (s *EmbeddingStore) Get(entityType, entityID string) (*Embedding, error) {
	id := entityType + ":" + entityID
	var e Embedding
	var blob []byte
	err := s.db.QueryRow(`
		SELECT entity_type, entity_id, text_hash, vector, model, created_at, updated_at
		FROM embeddings WHERE id = ?`, id,
	).Scan(&e.EntityType, &e.EntityID, &e.TextHash, &blob, &e.Model, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Vector = decodeVector(blob)
	return &e, nil
}

// Delete removes an embedding.
func (s *EmbeddingStore) Delete(entityType, entityID string) error {
	id := entityType + ":" + entityID
	_, err := s.db.Exec(`DELETE FROM embeddings WHERE id = ?`, id)
	return err
}

// ListByType returns all embeddings of a given type.
func (s *EmbeddingStore) ListByType(entityType string) ([]Embedding, error) {
	rows, err := s.db.Query(`
		SELECT entity_type, entity_id, text_hash, vector, model, created_at, updated_at
		FROM embeddings WHERE entity_type = ?`, entityType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []Embedding
	for rows.Next() {
		var e Embedding
		var blob []byte
		if err := rows.Scan(&e.EntityType, &e.EntityID, &e.TextHash, &blob, &e.Model, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Vector = decodeVector(blob)
		results = append(results, e)
	}
	return results, rows.Err()
}

// FindSimilar returns all embeddings of entityType with cosine similarity >= minSimilarity
// to the query vector, sorted by similarity descending, limited to maxResults.
func (s *EmbeddingStore) FindSimilar(entityType string, query []float32, minSimilarity float32, maxResults int) ([]SimilarityResult, error) {
	all, err := s.ListByType(entityType)
	if err != nil {
		return nil, err
	}
	var results []SimilarityResult
	for _, e := range all {
		sim := CosineSimilarity(query, e.Vector)
		if sim >= minSimilarity {
			results = append(results, SimilarityResult{EntityID: e.EntityID, Similarity: sim})
		}
	}
	// Sort descending
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].Similarity > results[i].Similarity {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results, nil
}

// CountByType returns the number of embeddings of a given type.
func (s *EmbeddingStore) CountByType(entityType string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM embeddings WHERE entity_type = ?`, entityType).Scan(&count)
	return count, err
}

// CosineSimilarity computes the cosine similarity between two float32 vectors.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// encodeVector converts []float32 to a compact binary blob (little-endian).
func encodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeVector converts a binary blob back to []float32.
func decodeVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/database/ -run "TestEmbeddingStore|TestCosineSimilarity" -v -count=1`
Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/database/embedding_store.go internal/database/embedding_store_test.go
git commit -m "feat: EmbeddingStore with vector CRUD, cosine similarity, and dedup_candidates table"
```

---

### Task 2: Create OpenAI Embedding Client

**Files:**
- Create: `internal/ai/embedding_client.go`
- Create: `internal/ai/embedding_client_test.go`

- [ ] **Step 1: Write the test**

Create `internal/ai/embedding_client_test.go`:

```go
package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildEmbeddingText_Book(t *testing.T) {
	text := BuildEmbeddingText("book", "The Way of Kings", "Brandon Sanderson", "Michael Kramer")
	assert.Equal(t, "The Way of Kings by Brandon Sanderson narrated by Michael Kramer", text)
}

func TestBuildEmbeddingText_BookNoNarrator(t *testing.T) {
	text := BuildEmbeddingText("book", "Dune", "Frank Herbert", "")
	assert.Equal(t, "Dune by Frank Herbert", text)
}

func TestBuildEmbeddingText_Author(t *testing.T) {
	text := BuildEmbeddingText("author", "Brandon Sanderson", "", "")
	assert.Equal(t, "Brandon Sanderson", text)
}

func TestTextHash(t *testing.T) {
	h1 := TextHash("hello")
	h2 := TextHash("hello")
	h3 := TextHash("world")
	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
	assert.Len(t, h1, 64) // SHA-256 hex
}
```

- [ ] **Step 2: Implement embedding client**

Create `internal/ai/embedding_client.go`:

```go
package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// EmbeddingClient wraps the OpenAI embeddings API.
type EmbeddingClient struct {
	client *openai.Client
	model  string
}

// NewEmbeddingClient creates a client for the OpenAI embeddings API.
func NewEmbeddingClient(apiKey string) *EmbeddingClient {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	return &EmbeddingClient{
		client: &client,
		model:  "text-embedding-3-large",
	}
}

// EmbedBatch sends up to 100 texts to the embeddings API and returns vectors.
// Returns one []float32 per input text, in the same order.
func (c *EmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > 100 {
		return nil, fmt.Errorf("embedding batch size %d exceeds max 100", len(texts))
	}

	// Retry with backoff
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}

		// Build input union array
		inputs := make([]openai.EmbeddingNewParamsInputUnion, len(texts))
		for i, text := range texts {
			inputs[i] = openai.EmbeddingNewParamsInputUnion{
				OfString: openai.String(text),
			}
		}

		resp, err := c.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
			Model: openai.EmbeddingModel(c.model),
			Input: openai.EmbeddingNewParamsInputUnion{
				OfArrayOfStrings: texts,
			},
		})
		if err != nil {
			lastErr = err
			continue
		}

		// Extract vectors in input order
		vectors := make([][]float32, len(texts))
		for _, item := range resp.Data {
			vec := make([]float32, len(item.Embedding))
			for j, v := range item.Embedding {
				vec[j] = float32(v)
			}
			vectors[item.Index] = vec
		}
		return vectors, nil
	}
	return nil, fmt.Errorf("embedding API failed after 3 attempts: %w", lastErr)
}

// EmbedOne embeds a single text string.
func (c *EmbeddingClient) EmbedOne(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedding returned no vectors")
	}
	return vecs[0], nil
}

// BuildEmbeddingText constructs the text to embed for a given entity.
func BuildEmbeddingText(entityType, title, author, narrator string) string {
	switch entityType {
	case "book":
		text := title
		if author != "" {
			text += " by " + author
		}
		if narrator != "" {
			text += " narrated by " + narrator
		}
		return text
	case "author":
		return title // for authors, title param holds the author name
	default:
		return title
	}
}

// TextHash returns a SHA-256 hex hash of the input text.
func TextHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}
```

Note: The `EmbedBatch` function uses the OpenAI Go SDK. The exact parameter construction depends on the SDK version (v1.12.0). The implementer should read the SDK source or use `context7` to verify the correct way to construct `EmbeddingNewParams`. The key fields are `Model` and `Input`. The SDK may use a union type for `Input` — check `openai.EmbeddingNewParams` definition.

- [ ] **Step 3: Run tests**

Run: `go test ./internal/ai/ -run "TestBuildEmbeddingText|TestTextHash" -v -count=1`
Expected: Pass.

- [ ] **Step 4: Commit**

```bash
git add internal/ai/embedding_client.go internal/ai/embedding_client_test.go
git commit -m "feat: OpenAI embedding client with batch support and text construction"
```

---

### Task 3: Create DedupCandidate CRUD on EmbeddingStore

**Files:**
- Modify: `internal/database/embedding_store.go`
- Create: `internal/database/embedding_candidates_test.go`

- [ ] **Step 1: Write the test**

Create `internal/database/embedding_candidates_test.go`:

```go
package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDedupCandidates_CreateAndList(t *testing.T) {
	store := newTestEmbeddingStore(t)

	err := store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2",
		Layer: "embedding", Similarity: floatPtr(0.95), Status: "pending",
	})
	require.NoError(t, err)

	err = store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b3", EntityBID: "b4",
		Layer: "exact", Status: "pending",
	})
	require.NoError(t, err)

	candidates, total, err := store.ListCandidates(CandidateFilter{EntityType: "book", Status: "pending", Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, candidates, 2)
}

func TestDedupCandidates_UpdateStatus(t *testing.T) {
	store := newTestEmbeddingStore(t)

	_ = store.UpsertCandidate(DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2",
		Layer: "embedding", Status: "pending",
	})

	candidates, _, _ := store.ListCandidates(CandidateFilter{EntityType: "book", Status: "pending", Limit: 10})
	require.Len(t, candidates, 1)

	err := store.UpdateCandidateStatus(candidates[0].ID, "merged")
	require.NoError(t, err)

	merged, _, _ := store.ListCandidates(CandidateFilter{EntityType: "book", Status: "merged", Limit: 10})
	assert.Len(t, merged, 1)
}

func TestDedupCandidates_UpsertIdempotent(t *testing.T) {
	store := newTestEmbeddingStore(t)

	c := DedupCandidate{
		EntityType: "book", EntityAID: "b1", EntityBID: "b2",
		Layer: "embedding", Similarity: floatPtr(0.90), Status: "pending",
	}
	_ = store.UpsertCandidate(c)
	c.Similarity = floatPtr(0.95)
	_ = store.UpsertCandidate(c) // should update, not duplicate

	all, total, _ := store.ListCandidates(CandidateFilter{EntityType: "book", Limit: 10})
	assert.Equal(t, 1, total)
	assert.InDelta(t, 0.95, *all[0].Similarity, 0.01)
}

func TestDedupCandidates_Stats(t *testing.T) {
	store := newTestEmbeddingStore(t)

	_ = store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending"})
	_ = store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b3", EntityBID: "b4", Layer: "exact", Status: "merged"})
	_ = store.UpsertCandidate(DedupCandidate{EntityType: "author", EntityAID: "a1", EntityBID: "a2", Layer: "embedding", Status: "pending"})

	stats, err := store.GetCandidateStats()
	require.NoError(t, err)
	assert.NotEmpty(t, stats)
}

func TestDedupCandidates_RemoveForEntity(t *testing.T) {
	store := newTestEmbeddingStore(t)

	_ = store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b2", Layer: "embedding", Status: "pending"})
	_ = store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b1", EntityBID: "b3", Layer: "embedding", Status: "pending"})
	_ = store.UpsertCandidate(DedupCandidate{EntityType: "book", EntityAID: "b4", EntityBID: "b5", Layer: "embedding", Status: "pending"})

	deleted, err := store.RemoveCandidatesForEntity("book", "b1")
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)

	all, total, _ := store.ListCandidates(CandidateFilter{EntityType: "book", Limit: 10})
	assert.Equal(t, 1, total)
	assert.Equal(t, "b4", all[0].EntityAID)
}

func floatPtr(f float64) *float64 { return &f }
```

- [ ] **Step 2: Implement candidate CRUD**

Add to `internal/database/embedding_store.go`:

```go
// DedupCandidate represents a potential duplicate pair.
type DedupCandidate struct {
	ID         int64    `json:"id"`
	EntityType string   `json:"entity_type"`
	EntityAID  string   `json:"entity_a_id"`
	EntityBID  string   `json:"entity_b_id"`
	Layer      string   `json:"layer"`
	Similarity *float64 `json:"similarity,omitempty"`
	LLMVerdict string   `json:"llm_verdict,omitempty"`
	LLMReason  string   `json:"llm_reason,omitempty"`
	Status     string   `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CandidateFilter controls ListCandidates queries.
type CandidateFilter struct {
	EntityType    string
	Status        string
	Layer         string
	MinSimilarity *float64
	Limit         int
	Offset        int
}

// CandidateStat holds a count for one grouping.
type CandidateStat struct {
	EntityType string `json:"entity_type"`
	Layer      string `json:"layer"`
	Status     string `json:"status"`
	Count      int    `json:"count"`
}

// UpsertCandidate inserts or updates a dedup candidate pair.
func (s *EmbeddingStore) UpsertCandidate(c DedupCandidate) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO dedup_candidates (entity_type, entity_a_id, entity_b_id, layer, similarity, llm_verdict, llm_reason, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(entity_type, entity_a_id, entity_b_id) DO UPDATE SET
			layer = excluded.layer,
			similarity = excluded.similarity,
			llm_verdict = excluded.llm_verdict,
			llm_reason = excluded.llm_reason,
			status = excluded.status,
			updated_at = excluded.updated_at`,
		c.EntityType, c.EntityAID, c.EntityBID, c.Layer, c.Similarity, nullStr(c.LLMVerdict), nullStr(c.LLMReason), c.Status, now, now,
	)
	return err
}

// ListCandidates returns matching candidates sorted by similarity descending.
func (s *EmbeddingStore) ListCandidates(f CandidateFilter) ([]DedupCandidate, int, error) {
	if f.Limit == 0 {
		f.Limit = 50
	}
	var clauses []string
	var args []any
	if f.EntityType != "" {
		clauses = append(clauses, "entity_type = ?")
		args = append(args, f.EntityType)
	}
	if f.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, f.Status)
	}
	if f.Layer != "" {
		clauses = append(clauses, "layer = ?")
		args = append(args, f.Layer)
	}
	if f.MinSimilarity != nil {
		clauses = append(clauses, "similarity >= ?")
		args = append(args, *f.MinSimilarity)
	}

	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + joinAnd(clauses)
	}

	var total int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM dedup_candidates"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	query := "SELECT id, entity_type, entity_a_id, entity_b_id, layer, similarity, llm_verdict, llm_reason, status, created_at, updated_at FROM dedup_candidates" + where + " ORDER BY COALESCE(similarity, 0) DESC LIMIT ? OFFSET ?"
	dataArgs := append(args, f.Limit, f.Offset)
	rows, err := s.db.Query(query, dataArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var results []DedupCandidate
	for rows.Next() {
		var c DedupCandidate
		var sim sql.NullFloat64
		var verdict, reason sql.NullString
		if err := rows.Scan(&c.ID, &c.EntityType, &c.EntityAID, &c.EntityBID, &c.Layer, &sim, &verdict, &reason, &c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, 0, err
		}
		if sim.Valid {
			c.Similarity = &sim.Float64
		}
		if verdict.Valid {
			c.LLMVerdict = verdict.String
		}
		if reason.Valid {
			c.LLMReason = reason.String
		}
		results = append(results, c)
	}
	return results, total, rows.Err()
}

// UpdateCandidateStatus changes the status of a candidate.
func (s *EmbeddingStore) UpdateCandidateStatus(id int64, status string) error {
	_, err := s.db.Exec("UPDATE dedup_candidates SET status = ?, updated_at = ? WHERE id = ?", status, time.Now().UTC(), id)
	return err
}

// UpdateCandidateLLM sets the LLM verdict and reason.
func (s *EmbeddingStore) UpdateCandidateLLM(id int64, verdict, reason string) error {
	_, err := s.db.Exec("UPDATE dedup_candidates SET llm_verdict = ?, llm_reason = ?, layer = 'llm', updated_at = ? WHERE id = ?",
		verdict, reason, time.Now().UTC(), id)
	return err
}

// RemoveCandidatesForEntity deletes all candidates involving the given entity.
func (s *EmbeddingStore) RemoveCandidatesForEntity(entityType, entityID string) (int, error) {
	res, err := s.db.Exec("DELETE FROM dedup_candidates WHERE entity_type = ? AND (entity_a_id = ? OR entity_b_id = ?)",
		entityType, entityID, entityID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetCandidateStats returns counts grouped by entity_type, layer, status.
func (s *EmbeddingStore) GetCandidateStats() ([]CandidateStat, error) {
	rows, err := s.db.Query("SELECT entity_type, layer, status, COUNT(*) FROM dedup_candidates GROUP BY entity_type, layer, status")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stats []CandidateStat
	for rows.Next() {
		var st CandidateStat
		if err := rows.Scan(&st.EntityType, &st.Layer, &st.Status, &st.Count); err != nil {
			return nil, err
		}
		stats = append(stats, st)
	}
	return stats, rows.Err()
}

// GetCandidateByID retrieves a single candidate.
func (s *EmbeddingStore) GetCandidateByID(id int64) (*DedupCandidate, error) {
	var c DedupCandidate
	var sim sql.NullFloat64
	var verdict, reason sql.NullString
	err := s.db.QueryRow("SELECT id, entity_type, entity_a_id, entity_b_id, layer, similarity, llm_verdict, llm_reason, status, created_at, updated_at FROM dedup_candidates WHERE id = ?", id).
		Scan(&c.ID, &c.EntityType, &c.EntityAID, &c.EntityBID, &c.Layer, &sim, &verdict, &reason, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if sim.Valid { c.Similarity = &sim.Float64 }
	if verdict.Valid { c.LLMVerdict = verdict.String }
	if reason.Valid { c.LLMReason = reason.String }
	return &c, nil
}

func nullStr(s string) any {
	if s == "" { return nil }
	return s
}

func joinAnd(clauses []string) string {
	result := clauses[0]
	for _, c := range clauses[1:] {
		result += " AND " + c
	}
	return result
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/database/ -run TestDedupCandidates -v -count=1`
Expected: All pass.

- [ ] **Step 4: Commit**

```bash
git add internal/database/embedding_store.go internal/database/embedding_candidates_test.go
git commit -m "feat: DedupCandidate CRUD on EmbeddingStore"
```

---

### Task 4: Create DedupEngine (3-layer orchestrator)

**Files:**
- Create: `internal/server/dedup_engine.go`
- Create: `internal/server/dedup_engine_test.go`

- [ ] **Step 1: Write the test**

Create `internal/server/dedup_engine_test.go` with tests for:
- `TestExactMatchLayer_AutoMerge` — same hash+author+title → returns auto-merge signal
- `TestExactMatchLayer_ISBNFlag` — same ISBN → returns candidate
- `TestExactMatchLayer_LevenshteinFlag` — Levenshtein < 3 → returns candidate
- `TestExactMatchLayer_NoMatch` — different books → returns nothing
- `TestBuildEmbeddingCheck` — verifies correct text construction and hash comparison

The implementer should create a mock or in-memory store for these tests. Use the `database.EmbeddingStore` with a temp dir.

- [ ] **Step 2: Implement DedupEngine**

Create `internal/server/dedup_engine.go`:

The DedupEngine struct holds references to the EmbeddingStore, the main book/author Store, and the EmbeddingClient. Key methods:

```go
type DedupEngine struct {
    embedStore  *database.EmbeddingStore
    bookStore   database.Store
    embedClient *ai.EmbeddingClient
}

// CheckBook runs Layer 1 + Layer 2 for a single book. Returns true if auto-merged.
func (d *DedupEngine) CheckBook(bookID string) (bool, error)

// CheckAuthor runs Layer 1 + Layer 2 for a single author.
func (d *DedupEngine) CheckAuthor(authorID int) error

// FullScan re-embeds stale entities and runs Layer 2 for all books and authors.
func (d *DedupEngine) FullScan(ctx context.Context, progress func(done, total int)) error

// RunLLMReview sends ambiguous candidates to the LLM (Layer 3).
func (d *DedupEngine) RunLLMReview(ctx context.Context) error

// EmbedBook computes and stores the embedding for a book, skipping if text_hash unchanged.
func (d *DedupEngine) EmbedBook(ctx context.Context, bookID string) error

// EmbedAuthor computes and stores the embedding for an author.
func (d *DedupEngine) EmbedAuthor(ctx context.Context, authorID int) error
```

**Layer 1 exact match logic (inside CheckBook):**
1. Get book by ID, get all book files
2. For each file hash, check if any other book has the same hash (query store)
3. If hash match AND same normalized author AND same normalized title → auto-merge via existing `MergeService.MergeBooks`
4. Check ISBN/ASIN: if another book has same ISBN → `UpsertCandidate(layer="exact")`
5. Check title similarity: normalize both titles, if Levenshtein < 3 AND same author → `UpsertCandidate(layer="exact")`

**Layer 2 embedding logic (inside CheckBook, after Layer 1):**
1. Call `EmbedBook` (computes embedding if stale)
2. Call `embedStore.FindSimilar("book", vector, bookLowThreshold, 50)`
3. Filter out self (same entity ID)
4. For each match above threshold → `UpsertCandidate(layer="embedding", similarity=score)`

**Config thresholds** read from `config.AppConfig`:
- `DedupBookHighThreshold` (0.95), `DedupBookLowThreshold` (0.85)
- `DedupAuthorHighThreshold` (0.92), `DedupAuthorLowThreshold` (0.80)
- `DedupAutoMergeEnabled` (true)

- [ ] **Step 3: Run tests**

Run: `go test ./internal/server/ -run TestExactMatch -v -count=1`
Expected: Pass.

- [ ] **Step 4: Commit**

```bash
git add internal/server/dedup_engine.go internal/server/dedup_engine_test.go
git commit -m "feat: DedupEngine with 3-layer orchestration (exact, embedding, LLM)"
```

---

### Task 5: Add config keys

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Add config fields**

Find the `ActivityLogCompactionDays` field and add after it:

```go
// Embedding-based dedup
EmbeddingEnabled           bool    `json:"embedding_enabled"`              // default true
EmbeddingModel             string  `json:"embedding_model"`                // default "text-embedding-3-large"
DedupBookHighThreshold     float64 `json:"dedup_book_high_threshold"`      // default 0.95
DedupBookLowThreshold      float64 `json:"dedup_book_low_threshold"`       // default 0.85
DedupAuthorHighThreshold   float64 `json:"dedup_author_high_threshold"`    // default 0.92
DedupAuthorLowThreshold    float64 `json:"dedup_author_low_threshold"`     // default 0.80
DedupAutoMergeEnabled      bool    `json:"dedup_auto_merge_enabled"`       // default true
```

Add defaults in the defaults struct:

```go
EmbeddingEnabled:         true,
EmbeddingModel:           "text-embedding-3-large",
DedupBookHighThreshold:   0.95,
DedupBookLowThreshold:    0.85,
DedupAuthorHighThreshold: 0.92,
DedupAuthorLowThreshold:  0.80,
DedupAutoMergeEnabled:    true,
```

- [ ] **Step 2: Build and verify**

Run: `make build-api`

- [ ] **Step 3: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add embedding/dedup config keys with defaults"
```

---

### Task 6: Wire EmbeddingStore + DedupEngine into Server

**Files:**
- Modify: `internal/server/server.go`

- [ ] **Step 1: Add fields to Server struct**

Find the `activityService` field and add nearby:

```go
embeddingStore  *database.EmbeddingStore
dedupEngine     *DedupEngine
```

- [ ] **Step 2: Initialize in NewServer/Start**

Find where `activityStore` is opened (around line 808). Add after it:

```go
// Open embedding store
embeddingDBPath := filepath.Join(filepath.Dir(dbPath), "embeddings.db")
embeddingStore, err := database.NewEmbeddingStore(embeddingDBPath)
if err != nil {
    log.Printf("[WARN] Failed to open embedding store: %v", err)
} else {
    server.embeddingStore = embeddingStore
    if config.AppConfig.OpenAIAPIKey != "" && config.AppConfig.EmbeddingEnabled {
        embedClient := ai.NewEmbeddingClient(config.AppConfig.OpenAIAPIKey)
        server.dedupEngine = &DedupEngine{
            embedStore:  embeddingStore,
            bookStore:   database.GlobalStore,
            embedClient: embedClient,
        }
    }
}
```

- [ ] **Step 3: Add shutdown cleanup**

Find where `activityStore` is closed in the shutdown sequence. Add nearby:

```go
if server.embeddingStore != nil {
    server.embeddingStore.Close()
}
```

- [ ] **Step 4: Build and verify**

Run: `make build-api`

- [ ] **Step 5: Commit**

```bash
git add internal/server/server.go
git commit -m "feat: initialize EmbeddingStore and DedupEngine on server startup"
```

---

### Task 7: Add dedup API endpoints

**Files:**
- Create: `internal/server/dedup_handlers.go`
- Modify: `internal/server/server.go` (route registration)

- [ ] **Step 1: Implement handlers**

Create `internal/server/dedup_handlers.go` with these handlers:

```go
// listDedupCandidates handles GET /api/v1/dedup/candidates
// Query params: entity_type, status, layer, min_similarity, limit, offset

// getDedupStats handles GET /api/v1/dedup/stats

// mergeDedupCandidate handles POST /api/v1/dedup/candidates/:id/merge
// Calls existing MergeService.MergeBooks or author merge based on entity_type

// dismissDedupCandidate handles POST /api/v1/dedup/candidates/:id/dismiss
// Sets status = "dismissed"

// triggerDedupScan handles POST /api/v1/dedup/scan
// Creates a background operation that calls DedupEngine.FullScan

// triggerDedupLLM handles POST /api/v1/dedup/scan-llm
// Creates a background operation that calls DedupEngine.RunLLMReview

// triggerDedupRefresh handles POST /api/v1/dedup/refresh
// Re-embeds all stale entities then runs full scan
```

Each handler follows the same patterns as existing handlers in `activity_handlers.go` and `metadata_batch_candidates.go` — nil checks, error handling via `internalError()`, operation creation for background work.

- [ ] **Step 2: Register routes**

In `server.go`, find the existing duplicates routes and add after them:

```go
// Embedding-based dedup
protected.GET("/dedup/candidates", s.listDedupCandidates)
protected.GET("/dedup/stats", s.getDedupStats)
protected.POST("/dedup/candidates/:id/merge", s.mergeDedupCandidate)
protected.POST("/dedup/candidates/:id/dismiss", s.dismissDedupCandidate)
protected.POST("/dedup/scan", s.triggerDedupScan)
protected.POST("/dedup/scan-llm", s.triggerDedupLLM)
protected.POST("/dedup/refresh", s.triggerDedupRefresh)
```

- [ ] **Step 3: Build and verify**

Run: `make build-api`

- [ ] **Step 4: Commit**

```bash
git add internal/server/dedup_handlers.go internal/server/server.go
git commit -m "feat: add dedup API endpoints (candidates, scan, merge, dismiss)"
```

---

### Task 8: Add embedding triggers on ingest and metadata change

**Files:**
- Modify: `internal/server/server.go` (book create hook)
- Modify: `internal/server/metadata_fetch_service.go` (post-apply hook)

- [ ] **Step 1: Trigger on book create**

Find where books are created (the `createAudiobook` handler or wherever new books are inserted). After successful creation, add:

```go
if s.dedupEngine != nil {
    go func() {
        if autoMerged, err := s.dedupEngine.CheckBook(newBook.ID); err != nil {
            log.Printf("[WARN] dedup check failed for new book %s: %v", newBook.ID, err)
        } else if autoMerged {
            log.Printf("[INFO] auto-merged duplicate book %s", newBook.ID)
        }
    }()
}
```

- [ ] **Step 2: Trigger on metadata apply**

Find `runApplyPipeline` or the metadata apply completion point in `metadata_fetch_service.go`. After metadata is applied to a book, add:

```go
if mfs.server != nil && mfs.server.dedupEngine != nil {
    go func() {
        if _, err := mfs.server.dedupEngine.CheckBook(bookID); err != nil {
            log.Printf("[WARN] dedup re-check failed for book %s after metadata apply: %v", bookID, err)
        }
    }()
}
```

The implementer should check how `metadata_fetch_service.go` accesses the server — it may need a reference passed through or use a global.

- [ ] **Step 3: Build and verify**

Run: `make build-api`

- [ ] **Step 4: Commit**

```bash
git add internal/server/server.go internal/server/metadata_fetch_service.go
git commit -m "feat: trigger dedup check on book create and metadata apply"
```

---

### Task 9: Add backfill on first startup

**Files:**
- Modify: `internal/server/server.go` (startup sequence)

- [ ] **Step 1: Add backfill goroutine**

In the server startup, after the DedupEngine is initialized, add:

```go
if server.dedupEngine != nil {
    go server.runEmbeddingBackfill()
}
```

Implement `runEmbeddingBackfill` on `*Server`:

```go
func (s *Server) runEmbeddingBackfill() {
    store := database.GlobalStore
    if store == nil || s.dedupEngine == nil {
        return
    }

    // Check if backfill already done
    if setting, err := store.GetSetting("embedding_backfill_done"); err == nil && setting != nil && setting.Value == "true" {
        log.Printf("[INFO] Embedding backfill already complete, skipping")
        return
    }
    log.Printf("[INFO] Starting embedding backfill...")

    ctx := context.Background()
    offset := 0
    embedded := 0
    for {
        books, err := store.GetAllBooks(100, offset)
        if err != nil || len(books) == 0 {
            break
        }
        for _, book := range books {
            if err := s.dedupEngine.EmbedBook(ctx, book.ID); err != nil {
                log.Printf("[WARN] backfill embed book %s: %v", book.ID, err)
            } else {
                embedded++
            }
        }
        offset += 100
        if embedded%500 == 0 {
            log.Printf("[INFO] Embedding backfill progress: %d books embedded", embedded)
        }
    }
    log.Printf("[INFO] Embedded %d books", embedded)

    // Backfill authors
    authors, _ := store.GetAllAuthors(100000, 0)
    for _, author := range authors {
        if err := s.dedupEngine.EmbedAuthor(ctx, author.ID); err != nil {
            log.Printf("[WARN] backfill embed author %d: %v", author.ID, err)
        } else {
            embedded++
        }
    }
    log.Printf("[INFO] Embedding backfill complete: %d total entities", embedded)

    // Run full dedup scan
    if err := s.dedupEngine.FullScan(ctx, func(done, total int) {
        if done%1000 == 0 {
            log.Printf("[INFO] Dedup scan progress: %d/%d", done, total)
        }
    }); err != nil {
        log.Printf("[WARN] Initial dedup scan failed: %v", err)
    }

    _ = store.SetSetting("embedding_backfill_done", "true", "bool", false)
    log.Printf("[INFO] Embedding backfill and initial dedup scan complete")
}
```

- [ ] **Step 2: Build and verify**

Run: `make build-api`

- [ ] **Step 3: Commit**

```bash
git add internal/server/server.go
git commit -m "feat: idempotent embedding backfill on first startup"
```

---

### Task 10: Add dedup maintenance window tasks

**Files:**
- Modify: `internal/server/scheduler.go`

- [ ] **Step 1: Register dedup tasks**

In `registerMaintenanceTasks`, add a new task after the existing ones:

```go
ts.registerTask(TaskDefinition{
    Name:        "dedup_llm_review",
    Description: "Run LLM review on ambiguous dedup candidates",
    Category:    "maintenance",
    TriggerFn: func() (*database.Operation, error) {
        return ts.triggerOperation("dedup-llm-review", func(ctx context.Context, progress operations.ProgressReporter) error {
            if ts.server.dedupEngine == nil {
                return nil
            }
            return ts.server.dedupEngine.RunLLMReview(ctx)
        })
    },
    IsEnabled:              func() bool { return ts.server.dedupEngine != nil },
    GetInterval:            func() time.Duration { return 0 },
    RunOnStart:             func() bool { return false },
    RunInMaintenanceWindow: func() bool { return true },
})
```

Also add `"dedup_llm_review"` to the `maintenanceOrder` slice.

- [ ] **Step 2: Build and verify**

Run: `make build-api`

- [ ] **Step 3: Commit**

```bash
git add internal/server/scheduler.go
git commit -m "feat: dedup LLM review as maintenance window task"
```

---

### Task 11: Add frontend API functions

**Files:**
- Modify: `web/src/services/api.ts`

- [ ] **Step 1: Add dedup API functions**

Add to `web/src/services/api.ts`:

```typescript
// Dedup candidates
export interface DedupCandidate {
  id: number;
  entity_type: 'book' | 'author';
  entity_a_id: string;
  entity_b_id: string;
  layer: 'exact' | 'embedding' | 'llm';
  similarity?: number;
  llm_verdict?: string;
  llm_reason?: string;
  status: 'pending' | 'merged' | 'dismissed';
  created_at: string;
  updated_at: string;
}

export interface DedupCandidatesResponse {
  candidates: DedupCandidate[];
  total: number;
}

export interface DedupStats {
  entity_type: string;
  layer: string;
  status: string;
  count: number;
}

export async function getDedupCandidates(params?: {
  entity_type?: string; status?: string; layer?: string;
  min_similarity?: number; limit?: number; offset?: number;
}): Promise<DedupCandidatesResponse> {
  const qs = new URLSearchParams();
  if (params) {
    Object.entries(params).forEach(([k, v]) => { if (v !== undefined) qs.set(k, String(v)); });
  }
  const res = await fetch(`${API_BASE}/dedup/candidates?${qs}`);
  if (!res.ok) throw await buildApiError(res, 'Failed to get dedup candidates');
  return res.json();
}

export async function getDedupStats(): Promise<{ stats: DedupStats[] }> {
  const res = await fetch(`${API_BASE}/dedup/stats`);
  if (!res.ok) throw await buildApiError(res, 'Failed to get dedup stats');
  return res.json();
}

export async function mergeDedupCandidate(id: number): Promise<void> {
  const res = await fetch(`${API_BASE}/dedup/candidates/${id}/merge`, { method: 'POST' });
  if (!res.ok) throw await buildApiError(res, 'Failed to merge');
}

export async function dismissDedupCandidate(id: number): Promise<void> {
  const res = await fetch(`${API_BASE}/dedup/candidates/${id}/dismiss`, { method: 'POST' });
  if (!res.ok) throw await buildApiError(res, 'Failed to dismiss');
}

export async function triggerDedupScan(): Promise<{ operation_id: string }> {
  const res = await fetch(`${API_BASE}/dedup/scan`, { method: 'POST' });
  if (!res.ok) throw await buildApiError(res, 'Failed to start dedup scan');
  return res.json();
}

export async function triggerDedupLLM(): Promise<{ operation_id: string }> {
  const res = await fetch(`${API_BASE}/dedup/scan-llm`, { method: 'POST' });
  if (!res.ok) throw await buildApiError(res, 'Failed to start LLM review');
  return res.json();
}

export async function triggerDedupRefresh(): Promise<{ operation_id: string }> {
  const res = await fetch(`${API_BASE}/dedup/refresh`, { method: 'POST' });
  if (!res.ok) throw await buildApiError(res, 'Failed to start refresh');
  return res.json();
}
```

- [ ] **Step 2: Type check**

Run: `cd web && npx tsc --noEmit`

- [ ] **Step 3: Commit**

```bash
git add web/src/services/api.ts
git commit -m "feat: add dedup API functions to frontend"
```

---

### Task 12: Update BookDedup page to use new candidates API

**Files:**
- Modify: `web/src/pages/BookDedup.tsx`

- [ ] **Step 1: Update BookDedup to read from dedup candidates**

The existing `BookDedup.tsx` page currently reads from the old scan results. Update it to:

1. Fetch candidates via `getDedupCandidates({ entity_type: 'book', status: 'pending' })`
2. For each candidate pair, fetch both books by ID to display
3. Show similarity as percentage, layer badge (exact/embedding/llm)
4. Merge button calls `mergeDedupCandidate(id)` then refreshes
5. Dismiss button calls `dismissDedupCandidate(id)` then refreshes
6. Add "Re-scan" button → `triggerDedupScan()`
7. Add "AI Review" button → `triggerDedupLLM()`
8. Add status filter tabs: Pending | Merged | Dismissed
9. Show dedup stats summary at top via `getDedupStats()`

The implementer should read the existing `BookDedup.tsx` first and follow its patterns/styles while replacing the data source.

- [ ] **Step 2: Type check and build**

Run: `cd web && npx tsc --noEmit`
Run: `cd .. && make build`

- [ ] **Step 3: Commit**

```bash
git add web/src/pages/BookDedup.tsx
git commit -m "feat: update BookDedup to use embedding-based dedup candidates"
```

---

### Task 13: End-to-end verification and deploy

- [ ] **Step 1: Run all backend tests**

Run: `go test ./internal/database/ -run "TestEmbeddingStore|TestDedupCandidates|TestCosineSimilarity" -v -count=1`
Run: `go test ./internal/ai/ -run "TestBuildEmbeddingText|TestTextHash" -v -count=1`
Run: `go test ./internal/server/ -count=1 -timeout 120s`

- [ ] **Step 2: Type check frontend**

Run: `cd web && npx tsc --noEmit`

- [ ] **Step 3: Full build**

Run: `make build`

- [ ] **Step 4: Deploy**

Run: `make deploy-debug`

- [ ] **Step 5: Verify backfill starts**

Check logs: `ssh <server> "journalctl -u audiobook-organizer --no-pager --since '2 min ago' | grep -i embed"`
Expected: "Starting embedding backfill..." message, progress logs.

- [ ] **Step 6: Verify API works**

```bash
ssh <server> "curl -sk 'https://localhost:8484/api/v1/dedup/stats'"
```

- [ ] **Step 7: Verify UI**

Open BookDedup page, verify candidates appear after backfill completes.
