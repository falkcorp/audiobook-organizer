// file: internal/database/embedding_store.go
// version: 2.4.0
// last-edited: 2026-06-22
// guid: 7c4a9b2e-d831-4f5c-a07e-3b8d6e1f9c42

package database

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
	"github.com/klauspost/compress/zstd"
)

// ─── Vector encoding version constants ───────────────────────────────────────
//
// The vector blob stored in embRec.Vector carries a 1-byte version header so
// old and new code can coexist during a rolling upgrade and after rollback.
//
//	v0 (absent/legacy): raw little-endian float32 — no header byte.
//	  Encoded as:  [f32_0 LE][f32_1 LE]…  (4N bytes for N-dim vector)
//
//	v1 (T021, SPEC 3 §3): float16 + zstd block compression.
//	  Encoded as:  [0x01][zstd_compressed( [f16_0 LE][f16_1 LE]… )]
//	  (1 + zstd_len bytes — typically ~3.5–4× smaller than v0 for 3072-dim)
//
// Why float16 is safe at our threshold regime (0.85/0.95):
//
//	For OpenAI text-embedding-3-large (3072 dims) the vectors are L2-normalised
//	before storage.  Float16 has 10 bits of mantissa → ~0.1% relative error per
//	element.  Over 3072 dims these errors average out (central-limit tendency),
//	and the resulting cosine-drift is empirically |Δcos| < 1e-3 across random
//	unit-vector pairs (see TestEncodeDecodeV1_CosineDrift).  Our accept threshold
//	is 0.85 and our high-confidence threshold is 0.95; a drift of <0.001 is
//	well inside the guard band on either side and cannot flip a genuine
//	accept/reject decision.
const (
	embVecVersion0 = byte(0x00) // sentinel (not written; any blob without header is v0)
	embVecVersion1 = byte(0x01) // float16 + zstd (T021)
)

// zstdEncoder / zstdDecoder are package-level singletons; creating them is
// expensive (~1ms+) but re-using them is cheap and concurrency-safe.
var (
	zstdEnc *zstd.Encoder
	zstdDec *zstd.Decoder
)

func init() {
	var err error
	// zstd.SpeedDefault gives a good compression-ratio/speed tradeoff.
	// For embedding blobs (mostly floating-point), higher levels rarely help.
	zstdEnc, err = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		panic("embedding_store: failed to initialise zstd encoder: " + err.Error())
	}
	zstdDec, err = zstd.NewReader(nil)
	if err != nil {
		panic("embedding_store: failed to initialise zstd decoder: " + err.Error())
	}
}

// Key-space layout (PebbleDB):
//
//	emb:v:<entityType>:<entityID>   → embRec JSON      (vector + metadata)
//	emb:c:<model>:<textHash>        → raw float32 blob  (content-hash cache)
//	dedup:r:<id16hex>               → candRec JSON      (dedup candidate)
//	dedup:p:<type>:<aID>:<bID>      → id16hex           (pair uniqueness index)
//	dedup:e:<type>:<entityID>:<id16hex> → empty         (entity secondary index — both sides)
//	dedup:seq                       → [8]byte LE int64  (auto-increment counter)
//	dedup:label:<id16hex>           → LabeledExample JSON   (dataset labels)
const (
	embVecPfx      = "emb:v:"
	embCachePfx    = "emb:c:"
	dedupRecPfx    = "dedup:r:"
	dedupPairPfx   = "dedup:p:"
	dedupEntityPfx = "dedup:e:"
	dedupSeqKey    = "dedup:seq"
)

// embRec is the stored value for a vector embedding.
type embRec struct {
	TextHash  string `json:"h"`
	Vector    []byte `json:"v"` // encodeVector output (little-endian float32)
	Model     string `json:"m"`
	CreatedAt int64  `json:"c"` // Unix nanoseconds
	UpdatedAt int64  `json:"u"` // Unix nanoseconds
}

// candRec is the stored value for a dedup candidate.
//
// New fields (T015): ScoreBreakdown, Band, FormulaVersion are additive — rows
// written before T015 will decode with nil/empty values, which is correct
// (they pre-date the unified scoring pipeline). Old readers silently ignore
// the extra JSON keys, so the keyspace stays fully backward-compatible.
type candRec struct {
	EntityType string   `json:"et"`
	EntityAID  string   `json:"a"`
	EntityBID  string   `json:"b"`
	Layer      string   `json:"l"`
	Similarity *float64 `json:"sim,omitempty"`
	LLMVerdict string   `json:"lv,omitempty"`
	LLMReason  string   `json:"lr,omitempty"`
	Status     string   `json:"s"`
	CreatedAt  int64    `json:"c"` // Unix nanoseconds
	UpdatedAt  int64    `json:"u"` // Unix nanoseconds

	// Unified scoring fields (T015, SPEC 1 §3). All omitempty so pre-T015
	// rows round-trip without growing their stored size.
	ScoreBreakdown *unified.UnifiedDedupScore `json:"sb,omitempty"`
	Band           string                     `json:"band,omitempty"`
	FormulaVersion string                     `json:"fv,omitempty"`
}

// Embedding holds a vector embedding for a single entity.
type Embedding struct {
	EntityType string
	EntityID   string
	TextHash   string
	Vector     []float32
	Model      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// SimilarityResult pairs an entity ID with its cosine similarity score.
type SimilarityResult struct {
	EntityID   string
	Similarity float32
}

// DedupCandidate represents a potential duplicate pair.
//
// ScoreBreakdown, Band, and FormulaVersion are additive fields added in T015
// (SPEC 1 §3). Pre-T015 rows will have nil/empty values here; consumers
// should treat empty FormulaVersion as "legacy" (pre-unified-pipeline).
type DedupCandidate struct {
	ID         int64     `json:"id"`
	EntityType string    `json:"entity_type"`
	EntityAID  string    `json:"entity_a_id"`
	EntityBID  string    `json:"entity_b_id"`
	Layer      string    `json:"layer"`
	Similarity *float64  `json:"similarity,omitempty"`
	LLMVerdict string    `json:"llm_verdict,omitempty"`
	LLMReason  string    `json:"llm_reason,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`

	// Unified scoring fields (T015, SPEC 1 §3). Nil/empty on pre-T015 rows.
	ScoreBreakdown *unified.UnifiedDedupScore `json:"score_breakdown,omitempty"`
	Band           string                     `json:"band,omitempty"`
	FormulaVersion string                     `json:"formula_version,omitempty"`
}

// CandidateFilter controls ListCandidates queries.
type CandidateFilter struct {
	EntityType    string
	Status        string
	Layer         string
	MinSimilarity *float64
	MaxSimilarity *float64
	// Band filters by the unified scoring band (CERTAIN/HIGH/MEDIUM/REVIEW).
	// Empty means no band filter. The filter is evaluated on the stored Band
	// field (set by the T015/T016 unified pipeline); pre-T015 rows with an
	// empty band will not match any non-empty Band filter.
	Band   string
	Limit  int
	Offset int
}

// CandidateStat holds a count for one grouping.
type CandidateStat struct {
	EntityType string `json:"entity_type"`
	Layer      string `json:"layer"`
	Status     string `json:"status"`
	Count      int    `json:"count"`
}

// EmbeddingHealthStats contains diagnostic counts for the embedding store.
type EmbeddingHealthStats struct {
	VectorCount int64 `json:"vector_count"`
	SizeBytes   int64 `json:"size_bytes"`
}

// EmbeddingStore is a PebbleDB-backed store for vector embeddings, text-hash
// cache, and dedup candidates. It replaces the former SQLite sidecar (embeddings.db).
type EmbeddingStore struct {
	db        *pebble.DB
	owned     bool        // if true, Close() shuts down the DB
	mu        sync.Mutex  // serialises counter + pair-uniqueness operations
	closeOnce sync.Once   // guards owned Close against double-call
	closed    atomic.Bool // set on Close; makes post-close ops return errors, not panic
}

// NewEmbeddingStore creates an EmbeddingStore backed by the given PebbleDB.
// The DB is shared with the main store; Close() is a no-op.
func NewEmbeddingStore(db *pebble.DB) *EmbeddingStore {
	return &EmbeddingStore{db: db, owned: false}
}

// Close releases resources. A no-op when the DB is shared (owned=false).
// Safe to call more than once when owned — subsequent calls are no-ops.
func (s *EmbeddingStore) Close() error {
	if !s.owned {
		return nil
	}
	var closeErr error
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		closeErr = s.db.Close()
	})
	return closeErr
}

// PebbleDB returns the underlying Pebble handle. This is provided so
// adjacent stores (e.g. the split-book candidate keyspace) can share
// the same shared-database connection without re-opening it. Returns
// nil if the receiver is nil or already closed.
func (s *EmbeddingStore) PebbleDB() *pebble.DB {
	if s == nil || s.closed.Load() {
		return nil
	}
	return s.db
}

// errClosed is returned by all operations when the store has been closed.
var errClosed = fmt.Errorf("embedding store closed")

func (s *EmbeddingStore) checkClosed() error {
	if s.closed.Load() {
		return errClosed
	}
	return nil
}

// ─── Vector methods ───────────────────────────────────────────────────────────

func embVecKey(entityType, entityID string) []byte {
	return []byte(embVecPfx + entityType + ":" + entityID)
}

// Upsert inserts or replaces a vector embedding. Created-at is preserved on updates.
func (s *EmbeddingStore) Upsert(e Embedding) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	key := embVecKey(e.EntityType, e.EntityID)
	now := time.Now().UnixNano()
	createdAt := now

	existing, err := s.getEmbRec(key)
	if err != nil {
		return err
	}
	if existing != nil {
		createdAt = existing.CreatedAt
	}

	return s.setJSON(key, embRec{
		TextHash:  e.TextHash,
		Vector:    encodeVector(e.Vector),
		Model:     e.Model,
		CreatedAt: createdAt,
		UpdatedAt: now,
	})
}

// Get retrieves an embedding by entity type and ID. Returns nil, nil when not found.
func (s *EmbeddingStore) Get(entityType, entityID string) (*Embedding, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	key := embVecKey(entityType, entityID)
	rec, err := s.getEmbRec(key)
	if err != nil || rec == nil {
		return nil, err
	}
	return &Embedding{
		EntityType: entityType,
		EntityID:   entityID,
		TextHash:   rec.TextHash,
		Vector:     decodeVector(rec.Vector),
		Model:      rec.Model,
		CreatedAt:  time.Unix(0, rec.CreatedAt),
		UpdatedAt:  time.Unix(0, rec.UpdatedAt),
	}, nil
}

// Delete removes an embedding. It is a no-op if the entity does not exist.
func (s *EmbeddingStore) Delete(entityType, entityID string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	return s.db.Delete(embVecKey(entityType, entityID), pebble.Sync)
}

// ListByType returns all embeddings for the given entity type.
func (s *EmbeddingStore) ListByType(entityType string) ([]Embedding, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	prefix := []byte(embVecPfx + entityType + ":")
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("list by type %s: %w", entityType, err)
	}
	defer iter.Close()

	typePrefix := embVecPfx + entityType + ":"
	var results []Embedding
	for iter.First(); iter.Valid(); iter.Next() {
		var rec embRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		entityID := string(iter.Key())[len(typePrefix):]
		results = append(results, Embedding{
			EntityType: entityType,
			EntityID:   entityID,
			TextHash:   rec.TextHash,
			Vector:     decodeVector(rec.Vector),
			Model:      rec.Model,
			CreatedAt:  time.Unix(0, rec.CreatedAt),
			UpdatedAt:  time.Unix(0, rec.UpdatedAt),
		})
	}
	return results, iter.Error()
}

// FindSimilar loads all embeddings of entityType, computes cosine similarity
// against query, filters by minSimilarity, and returns up to maxResults sorted
// by similarity descending.
func (s *EmbeddingStore) FindSimilar(entityType string, query []float32, minSimilarity float32, maxResults int) ([]SimilarityResult, error) {
	all, err := s.ListByType(entityType)
	if err != nil {
		return nil, err
	}

	var candidates []SimilarityResult
	for _, e := range all {
		if sim := CosineSimilarity(query, e.Vector); sim >= minSimilarity {
			candidates = append(candidates, SimilarityResult{EntityID: e.EntityID, Similarity: sim})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Similarity > candidates[j].Similarity
	})
	if maxResults > 0 && len(candidates) > maxResults {
		candidates = candidates[:maxResults]
	}
	return candidates, nil
}

// CountByType returns the number of embeddings stored for the given entity type.
func (s *EmbeddingStore) CountByType(entityType string) (int, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	prefix := []byte(embVecPfx + entityType + ":")
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return 0, fmt.Errorf("count by type %s: %w", entityType, err)
	}
	defer iter.Close()

	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n, iter.Error()
}

// ─── Cache methods ────────────────────────────────────────────────────────────

// GetCachedEmbedding looks up a cached embedding by text hash and model.
// Returns nil, nil on a cache miss.
func (s *EmbeddingStore) GetCachedEmbedding(textHash, model string) ([]float32, error) {
	if textHash == "" || model == "" {
		return nil, nil
	}
	start := time.Now()
	val, closer, err := s.db.Get([]byte(embCachePfx + model + ":" + textHash))
	metrics.ObserveCacheGetDuration("embedding", time.Since(start))
	if err == pebble.ErrNotFound {
		metrics.RecordCacheMiss("embedding", "not_found")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get cached embedding: %w", err)
	}
	vec := decodeVector(val)
	closer.Close()
	metrics.RecordCacheHit("embedding")
	return vec, nil
}

// PutCachedEmbedding stores a vector keyed by text hash and model.
// A write failure is never fatal — callers log and continue.
func (s *EmbeddingStore) PutCachedEmbedding(textHash, model string, vector []float32) error {
	if textHash == "" || model == "" || len(vector) == 0 {
		return nil
	}
	if err := s.db.Set([]byte(embCachePfx+model+":"+textHash), encodeVector(vector), pebble.Sync); err != nil {
		return err
	}
	metrics.RecordCacheSet("embedding")
	return nil
}

// ─── Dedup candidate methods ──────────────────────────────────────────────────

func dedupRecKey(id int64) []byte {
	return []byte(fmt.Sprintf("%s%016x", dedupRecPfx, id))
}

func dedupPairKey(entityType, aID, bID string) []byte {
	return []byte(dedupPairPfx + entityType + ":" + aID + ":" + bID)
}

// dedupEntityKey builds a secondary-index key for one side of a candidate pair.
// Both entity_a and entity_b get their own entry so prefix-scanning
// "dedup:e:<type>:<entityID>:" yields all candidates for that entity in O(k).
func dedupEntityKey(entityType, entityID string, id int64) []byte {
	return []byte(fmt.Sprintf("%s%s:%s:%016x", dedupEntityPfx, entityType, entityID, id))
}

// nextID reads and increments the sequential counter. Must be called with s.mu held.
// The new counter value is written into the supplied batch so counter + record land atomically.
func (s *EmbeddingStore) nextID(b *pebble.Batch) (int64, error) {
	val, closer, err := s.db.Get([]byte(dedupSeqKey))
	var id int64
	if err == pebble.ErrNotFound {
		id = 1
	} else if err != nil {
		return 0, fmt.Errorf("read dedup seq: %w", err)
	} else {
		id = int64(binary.LittleEndian.Uint64(val)) + 1
		closer.Close()
	}
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(id))
	return id, b.Set([]byte(dedupSeqKey), buf[:], nil)
}

// UpsertCandidate inserts or updates a dedup candidate pair.
//
// Layer precedence (exact > llm > embedding): a more-specific layer never gets
// downgraded by a less-specific one. LLM fields are always updated when non-empty.
// Pairs are canonicalised before insert (lexicographically smaller ID is always A).
func (s *EmbeddingStore) UpsertCandidate(c DedupCandidate) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if c.EntityAID > c.EntityBID {
		c.EntityAID, c.EntityBID = c.EntityBID, c.EntityAID
	}
	if c.Status == "" {
		c.Status = "pending"
	}
	now := time.Now().UnixNano()

	s.mu.Lock()
	defer s.mu.Unlock()

	pairKey := dedupPairKey(c.EntityType, c.EntityAID, c.EntityBID)
	existingIDBytes, closer, err := s.db.Get(pairKey)
	if err != nil && err != pebble.ErrNotFound {
		return fmt.Errorf("upsert candidate pair lookup: %w", err)
	}

	b := s.db.NewBatch()
	defer b.Close()

	if err == pebble.ErrNotFound {
		// New pair — assign sequential ID.
		id, err := s.nextID(b)
		if err != nil {
			return err
		}
		rec := candRec{
			EntityType:     c.EntityType,
			EntityAID:      c.EntityAID,
			EntityBID:      c.EntityBID,
			Layer:          c.Layer,
			Similarity:     c.Similarity,
			LLMVerdict:     c.LLMVerdict,
			LLMReason:      c.LLMReason,
			Status:         c.Status,
			CreatedAt:      now,
			UpdatedAt:      now,
			ScoreBreakdown: c.ScoreBreakdown,
			Band:           c.Band,
			FormulaVersion: c.FormulaVersion,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal candidate: %w", err)
		}
		if err := b.Set(dedupRecKey(id), data, nil); err != nil {
			return err
		}
		if err := b.Set(pairKey, []byte(fmt.Sprintf("%016x", id)), nil); err != nil {
			return err
		}
		// Entity secondary index — both sides, so prefix-scan by entity is O(k).
		if err := b.Set(dedupEntityKey(c.EntityType, c.EntityAID, id), nil, nil); err != nil {
			return err
		}
		if err := b.Set(dedupEntityKey(c.EntityType, c.EntityBID, id), nil, nil); err != nil {
			return err
		}
		return b.Commit(pebble.Sync)
	}

	// Existing pair — update in-place.
	idHex := string(existingIDBytes)
	closer.Close()
	id, err := strconv.ParseInt(idHex, 16, 64)
	if err != nil {
		return fmt.Errorf("parse candidate id %q: %w", idHex, err)
	}

	val, existingCloser, err := s.db.Get(dedupRecKey(id))
	if err != nil {
		return fmt.Errorf("read existing candidate %d: %w", id, err)
	}
	var existing candRec
	if err := json.Unmarshal(val, &existing); err != nil {
		existingCloser.Close()
		return fmt.Errorf("unmarshal existing candidate %d: %w", id, err)
	}
	existingCloser.Close()

	// Layer precedence mirrors the SQL ON CONFLICT logic:
	//   exact  → never overwritten (regardless of incoming layer)
	//   llm    → protected against embedding, but exact still upgrades it
	//   others → always overwritten with incoming layer + similarity
	//
	// T015 addition: a row that already has a non-empty FormulaVersion (i.e.
	// it was scored by the unified pipeline) is never downgraded by a legacy
	// writer that lacks a FormulaVersion — formula-versioned data is always
	// more trustworthy than pre-unified segment-era scores.
	protected := existing.Layer == "exact" ||
		(existing.Layer == "llm" && c.Layer != "exact") ||
		(existing.FormulaVersion != "" && c.FormulaVersion == "")
	if !protected {
		existing.Layer = c.Layer
		existing.Similarity = c.Similarity
		// Carry forward unified-scoring fields when the incoming write has them.
		if c.ScoreBreakdown != nil {
			existing.ScoreBreakdown = c.ScoreBreakdown
		}
		if c.Band != "" {
			existing.Band = c.Band
		}
		if c.FormulaVersion != "" {
			existing.FormulaVersion = c.FormulaVersion
		}
	}
	if c.LLMVerdict != "" {
		existing.LLMVerdict = c.LLMVerdict
	}
	if c.LLMReason != "" {
		existing.LLMReason = c.LLMReason
	}
	existing.Status = c.Status
	existing.UpdatedAt = now

	data, err := json.Marshal(existing)
	if err != nil {
		return fmt.Errorf("marshal updated candidate: %w", err)
	}
	if err := b.Set(dedupRecKey(id), data, nil); err != nil {
		return err
	}
	// Idempotent: ensure entity index entries exist (backfills pre-index records).
	if err := b.Set(dedupEntityKey(existing.EntityType, existing.EntityAID, id), nil, nil); err != nil {
		return err
	}
	if err := b.Set(dedupEntityKey(existing.EntityType, existing.EntityBID, id), nil, nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// GetCandidateByID retrieves a single candidate by its ID.
// Returns nil, nil when not found.
func (s *EmbeddingStore) GetCandidateByID(id int64) (*DedupCandidate, error) {
	val, closer, err := s.db.Get(dedupRecKey(id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get candidate %d: %w", id, err)
	}
	var rec candRec
	if err := json.Unmarshal(val, &rec); err != nil {
		closer.Close()
		return nil, fmt.Errorf("unmarshal candidate %d: %w", id, err)
	}
	closer.Close()
	c := candRecToCandidate(id, rec)
	return &c, nil
}

// ListCandidates returns a paginated list of dedup candidates matching the filter
// along with the total count of matching rows.
func (s *EmbeddingStore) ListCandidates(f CandidateFilter) ([]DedupCandidate, int, error) {
	if err := s.checkClosed(); err != nil {
		return nil, 0, err
	}
	prefix := []byte(dedupRecPfx)
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, 0, fmt.Errorf("list candidates: %w", err)
	}
	defer iter.Close()

	var all []DedupCandidate
	for iter.First(); iter.Valid(); iter.Next() {
		idHex := string(iter.Key())[len(dedupRecPfx):]
		id, err := strconv.ParseInt(idHex, 16, 64)
		if err != nil {
			continue
		}
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		c := candRecToCandidate(id, rec)

		if f.EntityType != "" && c.EntityType != f.EntityType {
			continue
		}
		if f.Status != "" && c.Status != f.Status {
			continue
		}
		if f.Layer != "" && c.Layer != f.Layer {
			continue
		}
		if f.MinSimilarity != nil && (c.Similarity == nil || *c.Similarity < *f.MinSimilarity) {
			continue
		}
		if f.MaxSimilarity != nil && (c.Similarity == nil || *c.Similarity > *f.MaxSimilarity) {
			continue
		}
		if f.Band != "" && c.Band != f.Band {
			continue
		}
		all = append(all, c)
	}
	if err := iter.Error(); err != nil {
		return nil, 0, fmt.Errorf("list candidates scan: %w", err)
	}

	// Sort by similarity descending (mirrors SQL ORDER BY COALESCE(similarity,0) DESC).
	sort.Slice(all, func(i, j int) bool {
		si, sj := 0.0, 0.0
		if all[i].Similarity != nil {
			si = *all[i].Similarity
		}
		if all[j].Similarity != nil {
			sj = *all[j].Similarity
		}
		return si > sj
	})

	total := len(all)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	start := f.Offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

// ListCandidatesForEntity returns all dedup candidates that involve the given
// entity on either side, using the "dedup:e:" secondary index for O(k) lookup
// instead of a full-table scan. status="" returns all statuses.
func (s *EmbeddingStore) ListCandidatesForEntity(entityType, entityID, status string) ([]DedupCandidate, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	prefix := []byte(dedupEntityPfx + entityType + ":" + entityID + ":")
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("entity index scan: %w", err)
	}
	defer iter.Close()

	pfxLen := len(dedupEntityPfx) + len(entityType) + 1 + len(entityID) + 1
	var results []DedupCandidate
	for iter.First(); iter.Valid(); iter.Next() {
		idHex := string(iter.Key()[pfxLen:])
		id, err := strconv.ParseInt(idHex, 16, 64)
		if err != nil {
			continue
		}
		val, closer, err := s.db.Get(dedupRecKey(id))
		if err == pebble.ErrNotFound {
			continue // stale index entry — tolerated, cleaned up on next UpsertCandidate
		}
		if err != nil {
			return nil, fmt.Errorf("entity candidate fetch %d: %w", id, err)
		}
		var rec candRec
		unmarshalErr := json.Unmarshal(val, &rec)
		closer.Close()
		if unmarshalErr != nil {
			continue
		}
		if status != "" && rec.Status != status {
			continue
		}
		results = append(results, candRecToCandidate(id, rec))
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("entity candidate scan: %w", err)
	}
	return results, nil
}

// BackfillEntityIndex writes "dedup:e:" index entries for any candidate that
// was stored before the entity index was introduced. Safe to call repeatedly —
// writes are idempotent. Returns the number of candidates processed.
func (s *EmbeddingStore) BackfillEntityIndex() (int, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	prefix := []byte(dedupRecPfx)
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return 0, fmt.Errorf("backfill entity index scan: %w", err)
	}
	defer iter.Close()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		idHex := string(iter.Key())[len(dedupRecPfx):]
		id, err := strconv.ParseInt(idHex, 16, 64)
		if err != nil {
			continue
		}
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		b := s.db.NewBatch()
		_ = b.Set(dedupEntityKey(rec.EntityType, rec.EntityAID, id), nil, nil)
		_ = b.Set(dedupEntityKey(rec.EntityType, rec.EntityBID, id), nil, nil)
		if err := b.Commit(pebble.Sync); err != nil {
			b.Close()
			return count, fmt.Errorf("backfill entity index write %d: %w", id, err)
		}
		b.Close()
		count++
	}
	if err := iter.Error(); err != nil {
		return count, fmt.Errorf("backfill entity index: %w", err)
	}
	return count, nil
}

// UpdateCandidateStatus updates the status of a single candidate by ID.
func (s *EmbeddingStore) UpdateCandidateStatus(id int64, status string) error {
	return s.updateCandidate(id, func(rec *candRec) {
		rec.Status = status
		rec.UpdatedAt = time.Now().UnixNano()
	})
}

// UpdateCandidateScore persists a new ScoreBreakdown, Band, and
// FormulaVersion onto an existing candidate. Called by Engine.Rescore
// when apply=true to commit re-computed scores without re-collecting signals.
func (s *EmbeddingStore) UpdateCandidateScore(id int64, score *unified.UnifiedDedupScore, band, formulaVersion string) error {
	return s.updateCandidate(id, func(rec *candRec) {
		rec.ScoreBreakdown = score
		rec.Band = band
		rec.FormulaVersion = formulaVersion
		rec.UpdatedAt = time.Now().UnixNano()
	})
}

// UpdateCandidateLLM stores the LLM verdict and reason for a candidate and
// sets the layer to 'llm'.
func (s *EmbeddingStore) UpdateCandidateLLM(id int64, verdict, reason string) error {
	return s.updateCandidate(id, func(rec *candRec) {
		rec.LLMVerdict = verdict
		rec.LLMReason = reason
		rec.Layer = "llm"
		rec.UpdatedAt = time.Now().UnixNano()
	})
}

func (s *EmbeddingStore) updateCandidate(id int64, mutFn func(*candRec)) error {
	val, closer, err := s.db.Get(dedupRecKey(id))
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("update candidate read %d: %w", id, err)
	}
	var rec candRec
	if err := json.Unmarshal(val, &rec); err != nil {
		closer.Close()
		return fmt.Errorf("update candidate unmarshal %d: %w", id, err)
	}
	closer.Close()

	mutFn(&rec)

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("update candidate marshal %d: %w", id, err)
	}
	return s.db.Set(dedupRecKey(id), data, pebble.Sync)
}

// DeleteCandidate removes a single candidate row by ID.
func (s *EmbeddingStore) DeleteCandidate(id int64) error {
	val, closer, err := s.db.Get(dedupRecKey(id))
	if err == pebble.ErrNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete candidate read %d: %w", id, err)
	}
	var rec candRec
	if err := json.Unmarshal(val, &rec); err != nil {
		closer.Close()
		return fmt.Errorf("delete candidate unmarshal %d: %w", id, err)
	}
	closer.Close()

	b := s.db.NewBatch()
	defer b.Close()
	_ = b.Delete(dedupRecKey(id), nil)
	_ = b.Delete(dedupPairKey(rec.EntityType, rec.EntityAID, rec.EntityBID), nil)
	_ = b.Delete(dedupEntityKey(rec.EntityType, rec.EntityAID, id), nil)
	_ = b.Delete(dedupEntityKey(rec.EntityType, rec.EntityBID, id), nil)
	return b.Commit(pebble.Sync)
}

// MarkCandidatesAsMergedForEntity sets status="merged" on every candidate row
// that references the given entity on either side (entity_a_id OR entity_b_id),
// regardless of current layer or status — *except* rows whose status is already
// "merged", which are left untouched so the returned count reflects only newly
// transitioned rows.
//
// Use case (MAYDEPLOY-B3): when a Merge operation collapses book B into book A,
// any other dedup candidate that still references book B (e.g. a separate row
// comparing book B vs book C) becomes a stale "orphan" — clicking Merge on it
// would fail because book B is gone. Marking those rows as merged here causes
// the candidates UI to drop them on its next refresh, instead of the user
// having to dismiss each one manually.
//
// Returns the number of rows whose status was newly changed.
func (s *EmbeddingStore) MarkCandidatesAsMergedForEntity(entityType, entityID string) (int, error) {
	if entityType == "" || entityID == "" {
		return 0, nil
	}

	prefix := []byte(dedupRecPfx)
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return 0, fmt.Errorf("mark candidates merged for entity: %w", err)
	}

	type target struct {
		id  int64
		rec candRec
	}
	var targets []target
	for iter.First(); iter.Valid(); iter.Next() {
		idHex := string(iter.Key())[len(dedupRecPfx):]
		id, err := strconv.ParseInt(idHex, 16, 64)
		if err != nil {
			continue
		}
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.EntityType != entityType {
			continue
		}
		if rec.EntityAID != entityID && rec.EntityBID != entityID {
			continue
		}
		if rec.Status == "merged" {
			continue
		}
		targets = append(targets, target{id: id, rec: rec})
	}
	iter.Close()
	if err := iter.Error(); err != nil {
		return 0, err
	}

	now := time.Now().UnixNano()
	b := s.db.NewBatch()
	defer b.Close()
	for _, t := range targets {
		t.rec.Status = "merged"
		t.rec.UpdatedAt = now
		data, err := json.Marshal(t.rec)
		if err != nil {
			return 0, fmt.Errorf("mark candidates merged marshal %d: %w", t.id, err)
		}
		if err := b.Set(dedupRecKey(t.id), data, nil); err != nil {
			return 0, fmt.Errorf("mark candidates merged set %d: %w", t.id, err)
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("mark candidates merged commit: %w", err)
	}
	return len(targets), nil
}

// RemoveCandidatesForEntity deletes all candidate rows that involve the given
// entity (as either entity_a or entity_b). Returns the number of rows deleted.
func (s *EmbeddingStore) RemoveCandidatesForEntity(entityType, entityID string) (int, error) {
	prefix := []byte(dedupRecPfx)
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return 0, fmt.Errorf("remove candidates for entity: %w", err)
	}

	type target struct {
		id  int64
		rec candRec
	}
	var targets []target
	for iter.First(); iter.Valid(); iter.Next() {
		idHex := string(iter.Key())[len(dedupRecPfx):]
		id, err := strconv.ParseInt(idHex, 16, 64)
		if err != nil {
			continue
		}
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.EntityType == entityType && (rec.EntityAID == entityID || rec.EntityBID == entityID) {
			targets = append(targets, target{id: id, rec: rec})
		}
	}
	iter.Close()
	if err := iter.Error(); err != nil {
		return 0, err
	}

	b := s.db.NewBatch()
	defer b.Close()
	for _, t := range targets {
		_ = b.Delete(dedupRecKey(t.id), nil)
		_ = b.Delete(dedupPairKey(t.rec.EntityType, t.rec.EntityAID, t.rec.EntityBID), nil)
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return 0, err
	}
	return len(targets), nil
}

// CanonicalizeCandidates rewrites existing rows so each logical pair has
// exactly one entry with entity_a_id < entity_b_id lexicographically.
// Returns (rewritten, deleted) counts for logging.
func (s *EmbeddingStore) CanonicalizeCandidates() (rewritten, deleted int, err error) {
	prefix := []byte(dedupRecPfx)
	upper := prefixUpperBound(prefix)

	iter, iterErr := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if iterErr != nil {
		return 0, 0, fmt.Errorf("canonicalize scan: %w", iterErr)
	}

	type nonCanon struct {
		id  int64
		rec candRec
	}
	var targets []nonCanon
	for iter.First(); iter.Valid(); iter.Next() {
		idHex := string(iter.Key())[len(dedupRecPfx):]
		id, err := strconv.ParseInt(idHex, 16, 64)
		if err != nil {
			continue
		}
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		if rec.EntityAID > rec.EntityBID {
			targets = append(targets, nonCanon{id: id, rec: rec})
		}
	}
	iter.Close()
	if err := iter.Error(); err != nil {
		return 0, 0, err
	}

	for _, t := range targets {
		canonA, canonB := t.rec.EntityBID, t.rec.EntityAID // swapped = canonical order
		canonPairKey := dedupPairKey(t.rec.EntityType, canonA, canonB)
		oldPairKey := dedupPairKey(t.rec.EntityType, t.rec.EntityAID, t.rec.EntityBID)

		_, existingCloser, existingErr := s.db.Get(canonPairKey)
		if existingErr == pebble.ErrNotFound {
			// No canonical row — swap in place.
			t.rec.EntityAID = canonA
			t.rec.EntityBID = canonB
			t.rec.UpdatedAt = time.Now().UnixNano()
			data, err := json.Marshal(t.rec)
			if err != nil {
				return rewritten, deleted, fmt.Errorf("canonicalize marshal: %w", err)
			}
			b := s.db.NewBatch()
			_ = b.Delete(oldPairKey, nil)
			_ = b.Set(canonPairKey, []byte(fmt.Sprintf("%016x", t.id)), nil)
			_ = b.Set(dedupRecKey(t.id), data, nil)
			if err := b.Commit(pebble.Sync); err != nil {
				b.Close()
				return rewritten, deleted, fmt.Errorf("canonicalize swap: %w", err)
			}
			b.Close()
			rewritten++
		} else if existingErr == nil {
			// Canonical row already exists — delete the non-canonical duplicate.
			existingCloser.Close()
			b := s.db.NewBatch()
			_ = b.Delete(dedupRecKey(t.id), nil)
			_ = b.Delete(oldPairKey, nil)
			if err := b.Commit(pebble.Sync); err != nil {
				b.Close()
				return rewritten, deleted, fmt.Errorf("canonicalize delete: %w", err)
			}
			b.Close()
			deleted++
		} else {
			return rewritten, deleted, fmt.Errorf("canonicalize check canonical: %w", existingErr)
		}
	}
	return rewritten, deleted, nil
}

// GetCandidateStats returns row counts grouped by entity_type, layer, and status.
func (s *EmbeddingStore) GetCandidateStats() ([]CandidateStat, error) {
	prefix := []byte(dedupRecPfx)
	upper := prefixUpperBound(prefix)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("get candidate stats: %w", err)
	}
	defer iter.Close()

	type statKey struct{ entityType, layer, status string }
	counts := map[statKey]int{}

	for iter.First(); iter.Valid(); iter.Next() {
		var rec candRec
		if err := json.Unmarshal(iter.Value(), &rec); err != nil {
			continue
		}
		counts[statKey{rec.EntityType, rec.Layer, rec.Status}]++
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	stats := make([]CandidateStat, 0, len(counts))
	for k, cnt := range counts {
		stats = append(stats, CandidateStat{EntityType: k.entityType, Layer: k.layer, Status: k.status, Count: cnt})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].EntityType != stats[j].EntityType {
			return stats[i].EntityType < stats[j].EntityType
		}
		if stats[i].Layer != stats[j].Layer {
			return stats[i].Layer < stats[j].Layer
		}
		return stats[i].Status < stats[j].Status
	})
	return stats, nil
}

// HealthStats returns diagnostic counters for the embedding store.
func (s *EmbeddingStore) HealthStats() (EmbeddingHealthStats, error) {
	books, err := s.CountByType("book")
	if err != nil {
		return EmbeddingHealthStats{}, err
	}
	authors, err := s.CountByType("author")
	if err != nil {
		return EmbeddingHealthStats{}, err
	}
	return EmbeddingHealthStats{VectorCount: int64(books + authors)}, nil
}

// ─── Internal helpers ─────────────────────────────────────────────────────────

func (s *EmbeddingStore) getEmbRec(key []byte) (*embRec, error) {
	val, closer, err := s.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read embedding record: %w", err)
	}
	var rec embRec
	if err := json.Unmarshal(val, &rec); err != nil {
		closer.Close()
		return nil, fmt.Errorf("unmarshal embedding record: %w", err)
	}
	closer.Close()
	return &rec, nil
}

func (s *EmbeddingStore) setJSON(key []byte, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return s.db.Set(key, data, pebble.Sync)
}

func candRecToCandidate(id int64, rec candRec) DedupCandidate {
	return DedupCandidate{
		ID:             id,
		EntityType:     rec.EntityType,
		EntityAID:      rec.EntityAID,
		EntityBID:      rec.EntityBID,
		Layer:          rec.Layer,
		Similarity:     rec.Similarity,
		LLMVerdict:     rec.LLMVerdict,
		LLMReason:      rec.LLMReason,
		Status:         rec.Status,
		CreatedAt:      time.Unix(0, rec.CreatedAt),
		UpdatedAt:      time.Unix(0, rec.UpdatedAt),
		ScoreBreakdown: rec.ScoreBreakdown,
		Band:           rec.Band,
		FormulaVersion: rec.FormulaVersion,
	}
}

// prefixUpperBound returns the smallest key strictly greater than all keys with
// the given prefix, suitable as PebbleDB UpperBound.
func prefixUpperBound(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper[:i+1]
		}
	}
	return nil // all bytes were 0xFF; no upper bound
}

// ─── Math helpers (package-level, shared with dedup engine) ──────────────────

// CosineSimilarity computes the cosine similarity between two vectors using
// float64 internally for precision. Returns 0 when either vector has zero norm.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

// encodeVector serialises a float32 slice to the v1 wire format:
//
//	[0x01][zstd_compressed( [f16_0 LE][f16_1 LE]… )]
//
// All new writes use v1.  See the version-constant block at the top of this
// file for the rationale behind float16 being safe at our scoring thresholds.
func encodeVector(v []float32) []byte {
	// Encode each float32 as a little-endian float16 (IEEE 754 half-precision).
	f16buf := make([]byte, len(v)*2)
	for i, f := range v {
		binary.LittleEndian.PutUint16(f16buf[i*2:], float32ToFloat16(f))
	}

	// Compress the float16 block with zstd.
	compressed := zstdEnc.EncodeAll(f16buf, nil)

	// Prepend the version byte.
	out := make([]byte, 1+len(compressed))
	out[0] = embVecVersion1
	copy(out[1:], compressed)
	return out
}

// decodeVector deserialises a vector blob produced by encodeVector (v0 or v1).
//
// v0 (no header byte / header byte != 0x01): raw little-endian float32 — kept
//
//	forever for backward compatibility with blobs written before T021.
//
// v1 (header byte 0x01): float16 + zstd.  Decompresses then dequantises back
//
//	to float32 for use in the in-memory cosine engine (chromem hydration path).
func decodeVector(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}

	// Detect v1: first byte is exactly 0x01 AND the remainder is a valid zstd frame.
	// We use a length guard: a valid 1-dim float16 vector would be at least
	// 1 (header) + zstd_min_frame (4 bytes) = 5 bytes; anything shorter is v0.
	if len(b) >= 5 && b[0] == embVecVersion1 {
		decompressed, err := zstdDec.DecodeAll(b[1:], nil)
		if err == nil && len(decompressed)%2 == 0 {
			// Valid v1 blob — dequantise float16 → float32.
			n := len(decompressed) / 2
			v := make([]float32, n)
			for i := range v {
				v[i] = float16ToFloat32(binary.LittleEndian.Uint16(decompressed[i*2:]))
			}
			return v
		}
		// Fall through to v0 path if the zstd decode fails (shouldn't happen in
		// normal operation, but keeps the decoder resilient against corruption).
	}

	// v0 path: raw little-endian float32.
	n := len(b) / 4
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// encodeVectorV0 serialises a float32 slice to the legacy v0 wire format
// (raw little-endian float32, no version header).  Used by tests to plant
// legacy rows and by the re-encode op to detect rows that need upgrading.
func encodeVectorV0(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// isVectorV1 returns true when the blob was encoded with the v1 scheme.
// Used by the re-encode op to skip already-upgraded rows.
func isVectorV1(b []byte) bool {
	return len(b) >= 5 && b[0] == embVecVersion1
}

// EncodeVectorExported is the exported wrapper for encodeVector.
// Used by the dedup.emb-reencode op in internal/plugins/dedup.
func EncodeVectorExported(v []float32) []byte { return encodeVector(v) }

// DecodeVectorExported is the exported wrapper for decodeVector.
// Used by the dedup.emb-reencode op in internal/plugins/dedup.
func DecodeVectorExported(b []byte) []float32 { return decodeVector(b) }

// IsVectorV1Exported is the exported wrapper for isVectorV1.
// Used by the dedup.emb-reencode op to skip already-upgraded rows.
func IsVectorV1Exported(b []byte) bool { return isVectorV1(b) }

// ─── IEEE 754 half-precision (float16) conversion ────────────────────────────
//
// We implement f32↔f16 locally so we can keep the dep graph lean.  The
// algorithm is the standard bit-manipulation approach (Jeroen van der Zijp,
// 2010) adapted from several public-domain Go implementations.
//
// Precision: 10-bit mantissa + 5-bit exponent.  The maximum representable
// value is 65504.  Normalised OpenAI embeddings are all in [-1, 1], safely
// within f16 range.  Subnormals are preserved correctly; ±Inf and NaN
// round-trip correctly.

// float32ToFloat16 converts an IEEE 754 single-precision float to half-precision.
func float32ToFloat16(f float32) uint16 {
	bits := math.Float32bits(f)
	sign := (bits >> 16) & 0x8000
	exp := int32((bits>>23)&0xFF) - 127 + 15
	mant := bits & 0x007FFFFF

	switch {
	case exp <= 0:
		// Subnormal or underflow — flush to signed zero.
		if exp < -10 {
			return uint16(sign)
		}
		// Subnormal: shift mantissa right, include the implicit leading 1.
		mant = (mant | 0x00800000) >> uint(1-exp)
		// Round (tie-to-even).
		if mant&0x00001000 != 0 {
			mant += 0x00002000
		}
		return uint16(sign | (mant >> 13))
	case exp >= 31:
		// Overflow → ±Inf.
		if exp == 255-127+15 && mant != 0 {
			// NaN — preserve a NaN bit pattern.
			return uint16(sign | 0x7C00 | (mant >> 13))
		}
		return uint16(sign | 0x7C00)
	}
	// Round mantissa from 23-bit to 10-bit (round-half-to-even).
	if mant&0x00001000 != 0 {
		mant += 0x00002000
		if mant&0x00800000 != 0 {
			// Mantissa overflow: increment exponent.
			mant = 0
			exp++
			if exp >= 31 {
				return uint16(sign | 0x7C00)
			}
		}
	}
	return uint16(sign | uint32(exp)<<10 | (mant >> 13))
}

// float16ToFloat32 converts an IEEE 754 half-precision value to single-precision.
func float16ToFloat32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32((h >> 10) & 0x1F)
	mant := uint32(h & 0x03FF)

	switch exp {
	case 0:
		if mant == 0 {
			// ±Zero.
			return math.Float32frombits(sign)
		}
		// Subnormal f16 → normalised f32.
		for mant&0x0400 == 0 {
			mant <<= 1
			exp--
		}
		exp++ // bias adjustment
		mant &^= 0x0400
		fallthrough
	default:
		// Normal number: re-bias exponent (f16 bias=15, f32 bias=127).
		return math.Float32frombits(sign | (exp+112)<<23 | mant<<13)
	case 31:
		// ±Inf or NaN.
		return math.Float32frombits(sign | 0x7F800000 | mant<<13)
	}
}

// vectorEncodeRatio returns the compression ratio achieved by v1 encoding
// compared to v0 for the given vector.  Used in tests and logging.
func vectorEncodeRatio(v []float32) float64 {
	v0 := encodeVectorV0(v)
	v1 := encodeVector(v)
	if len(v1) == 0 {
		return 0
	}
	return float64(len(v0)) / float64(len(v1))
}
