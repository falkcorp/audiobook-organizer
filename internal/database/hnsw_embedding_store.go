// file: internal/database/hnsw_embedding_store.go
// version: 1.6.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-07-03

// HNSW-graph vector store (coder/hnsw) — a sub-linear ANN index alternative to
// the brute-force chromem store. Selected via config.VectorIndexBackend="hnsw".
//
// # Why
//
// chromem-go performs an exhaustive O(n·d) cosine scan per query. At ~68K
// vectors × 1024 dims a dedup full-scan (one query per book) is hours of CPU.
// HNSW gives ~O(log n) search; the dependency is pure Go (zero CGo —
// viterin/vek uses Go assembly), satisfying the project's embedded-DB
// constraint.
//
// # Design
//
// coder/hnsw's Graph stores vectors keyed by a comparable key (we use the
// string entityID) with NO metadata and NO internal locking, and its v0.6.1
// Search returns nodes without distances. This store therefore adds three
// things around it:
//
//   - one *hnsw.Graph per entityType ("book", "author"), lazily created;
//   - a metadata sidecar (entityType → id → meta) for filtered search + Get;
//   - a sync.RWMutex (Search under RLock, Add/Delete under Lock), because the
//     dedup engine mirrors writes while querying.
//
// FindSimilar over-fetches limit*overFetchFactor candidates, recomputes cosine
// similarity per node (1 - CosineDistance), applies the metadata filter, then
// returns the top `limit` — the over-fetch compensates for candidates dropped
// by the filter (e.g. non-primary versions).
//
// Like chromem, this is a DERIVED in-memory index hydrated from the PebbleDB
// EmbeddingStore on boot; it is not a source of truth. (On-disk persistence via
// Graph.Export/Import is a documented follow-up.)

package database

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/coder/hnsw"
)

const (
	// hnswM is the max neighbors per node. The library default (16) gives only
	// ~72% recall@10 on our data; 32 lifts it materially. Higher M = denser
	// graph = better recall at more memory (~M edges/node/layer). 32 is a good
	// balance for a dedup index where missing a duplicate matters.
	hnswM = 32
	// hnswEfSearch is the candidate-list (beam) size during search; higher =
	// better recall at more CPU. It MUST be ≥ the number of neighbors requested
	// from Search, otherwise the beam is narrower than the result set and recall
	// collapses. FindSimilar requests limit*overFetch neighbors (≤80 in the
	// common limit≤20 case); 200 covers that with ample headroom for recall.
	hnswEfSearch = 200
	// hnswOverFetchFactor multiplies the requested limit when searching, so the
	// metadata post-filter still has enough survivors to fill `limit`. The graph
	// has no native filtering, so non-matching neighbors must be fetched then
	// dropped. Kept modest (most books are primary versions, so few are filtered)
	// to keep the search k under EfSearch.
	hnswOverFetchFactor = 4
)

// HNSWEmbeddingStore is a coder/hnsw-backed VectorANNStore.
type HNSWEmbeddingStore struct {
	mu     sync.RWMutex
	graphs map[string]*hnsw.Graph[string]          // entityType → graph
	meta   map[string]map[string]map[string]string // entityType → id → metadata
	dims   int

	// newGraphRng, when non-nil, supplies the *rand.Rand used for HNSW level
	// generation on each lazily-created graph. Production leaves it nil, so the
	// library's default time-seeded RNG (rand.NewSource(time.Now().UnixNano()))
	// is used. Tests set it to a fixed seed to make graph topology — and thus
	// recall — deterministic. Mutated graphs are only built under s.mu, so a
	// shared *rand.Rand returned here is never used concurrently.
	newGraphRng func() *rand.Rand
}

// NewHNSWEmbeddingStore creates an empty HNSW store sized for `dims`-dimensional
// vectors. dims is advisory (used to reject mismatched inserts early); the graph
// itself infers dimensionality from the first vector.
func NewHNSWEmbeddingStore(dims int) *HNSWEmbeddingStore {
	return &HNSWEmbeddingStore{
		graphs: make(map[string]*hnsw.Graph[string]),
		meta:   make(map[string]map[string]map[string]string),
		dims:   dims,
	}
}

// graphFor returns the graph for entityType, creating it if needed.
// Caller must hold s.mu (write lock).
func (s *HNSWEmbeddingStore) graphFor(entityType string) *hnsw.Graph[string] {
	g, ok := s.graphs[entityType]
	if !ok {
		g = hnsw.NewGraph[string]()
		g.Distance = hnsw.CosineDistance
		g.M = hnswM
		g.EfSearch = hnswEfSearch
		if s.newGraphRng != nil {
			g.Rng = s.newGraphRng()
		}
		s.graphs[entityType] = g
		s.meta[entityType] = make(map[string]map[string]string)
	}
	return g
}

// Upsert stores or replaces an entity's vector + metadata.
func (s *HNSWEmbeddingStore) Upsert(_ context.Context, entityType, entityID string, vec []float32, meta map[string]string) error {
	if entityID == "" {
		return fmt.Errorf("hnsw upsert: empty entityID")
	}
	if len(vec) == 0 {
		return fmt.Errorf("hnsw upsert %s: empty vector", entityID)
	}
	if s.dims > 0 && len(vec) != s.dims {
		return fmt.Errorf("hnsw upsert %s: vector dim %d != store dim %d", entityID, len(vec), s.dims)
	}
	// Reject zero-magnitude vectors: cosine of a zero vector is NaN, which would
	// poison FindSimilar's similarity sort (NaN comparisons are undefined).
	if !hasNonZeroMagnitude(vec) {
		return fmt.Errorf("hnsw upsert %s: zero-magnitude vector", entityID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.graphFor(entityType)
	// coder/hnsw v0.6.1 Graph.Add has known crash bugs: it panics "node not
	// added" (graph.go:405) on some re-insert / concurrent-mutation states, and
	// assertDims-panics on a dimension mismatch. This derived, best-effort ANN
	// index must never crash a caller — a single bad mirror cannot be allowed to
	// abort a 44K-book re-embed. Contain the library here: recover any panic and
	// surface it as an error, which callers (mirrorBookToChromem) already log and
	// continue past.
	if err := s.safeAdd(g, entityID, vec); err != nil {
		return err
	}
	if meta == nil {
		meta = map[string]string{}
	}
	// Store a defensive copy so the caller can't mutate our sidecar.
	s.meta[entityType][entityID] = copyMeta(meta)
	return nil
}

// safeAdd wraps coder/hnsw Graph.Add so a library panic becomes an error rather
// than crashing the process. Caller must hold s.mu.
func (s *HNSWEmbeddingStore) safeAdd(g *hnsw.Graph[string], entityID string, vec []float32) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hnsw add %s: recovered library panic: %v", entityID, r)
		}
	}()
	g.Add(hnsw.MakeNode(entityID, vec))
	return nil
}

// safeDelete wraps coder/hnsw Graph.Delete so a library panic becomes an error
// rather than crashing the process, mirroring safeAdd. Delete shares the same
// per-layer-invariant panic class as Add (HNSW-CRASH-2026-06-18) and runs on
// live paths (book merge/removal) from goroutines where an unrecovered panic
// kills the process. Caller must hold s.mu.
func (s *HNSWEmbeddingStore) safeDelete(g *hnsw.Graph[string], entityID string) (deleted bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hnsw delete %s: recovered library panic: %v", entityID, r)
		}
	}()
	deleted = g.Delete(entityID)
	return deleted, nil
}

// Get returns a copy of an entity's metadata, or (nil, nil) if absent.
func (s *HNSWEmbeddingStore) Get(_ context.Context, entityType, entityID string) (map[string]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byID, ok := s.meta[entityType]
	if !ok {
		return nil, nil
	}
	m, ok := byID[entityID]
	if !ok {
		return nil, nil
	}
	return copyMeta(m), nil
}

// Delete removes an entity's vector + metadata. Absent keys are a no-op.
// Deletion is best-effort: a recovered library panic from safeDelete is
// logged, not propagated, matching this file's "single bad mirror cannot
// abort" philosophy (see safeAdd) — the metadata sidecar entry is still
// removed regardless.
func (s *HNSWEmbeddingStore) Delete(_ context.Context, entityType, entityID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g, ok := s.graphs[entityType]; ok {
		if _, err := s.safeDelete(g, entityID); err != nil {
			slog.Warn("hnsw delete: recovered library panic; continuing best-effort",
				"entity_type", entityType, "entity_id", entityID, "err", err)
		}
	}
	if byID, ok := s.meta[entityType]; ok {
		delete(byID, entityID)
	}
	return nil
}

// FindSimilar returns up to maxResults nearest neighbors by cosine similarity,
// restricted to entities whose metadata matches every key/value in filter.
func (s *HNSWEmbeddingStore) FindSimilar(
	_ context.Context,
	entityType string,
	query []float32,
	maxResults int,
	filter map[string]string,
) ([]ChromemSimilarityResult, error) {
	if maxResults <= 0 {
		maxResults = 20
	}
	// Guard the query dimension and return an error (don't panic): coder/hnsw's
	// Search panics on a dimension mismatch, and dedup queries run from
	// background goroutines where an unrecovered panic crashes the process. This
	// also matches the chromem backend, which returns an error for the same input.
	if len(query) == 0 {
		return nil, fmt.Errorf("hnsw findsimilar: empty query vector")
	}
	if s.dims > 0 && len(query) != s.dims {
		return nil, fmt.Errorf("hnsw findsimilar: query dim %d != store dim %d", len(query), s.dims)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	g, ok := s.graphs[entityType]
	if !ok || g.Len() == 0 {
		return nil, nil
	}

	// Over-fetch so the metadata filter has enough survivors. Cap at graph size,
	// and at EfSearch: the search beam can't return more good neighbors than its
	// width, so requesting k > EfSearch would silently degrade recall.
	k := maxResults * hnswOverFetchFactor
	if k > hnswEfSearch {
		k = hnswEfSearch
	}
	if k > g.Len() {
		k = g.Len()
	}
	nodes := g.Search(query, k)

	byID := s.meta[entityType]
	out := make([]ChromemSimilarityResult, 0, len(nodes))
	for _, n := range nodes {
		m := byID[n.Key]
		if !metadataMatches(m, filter) {
			continue
		}
		// v0.6.1 Search returns no score — recompute cosine similarity.
		sim := 1 - hnsw.CosineDistance(query, n.Value)
		if math.IsNaN(float64(sim)) {
			// Defensive: a zero-magnitude stored vector would yield NaN, which
			// makes the similarity sort undefined. Upsert already rejects those,
			// so this is belt-and-suspenders.
			continue
		}
		out = append(out, ChromemSimilarityResult{
			EntityID:   n.Key,
			Similarity: sim,
			Metadata:   copyMeta(m),
		})
	}

	// hnsw.Search already returns nearest-first, but recomputed scores + the
	// filter make an explicit sort safest. Stable so equal scores keep graph order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if len(out) > maxResults {
		out = out[:maxResults]
	}
	return out, nil
}

// CountByType returns the number of indexed entities of the given type.
func (s *HNSWEmbeddingStore) CountByType(_ context.Context, entityType string) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.graphs[entityType]
	if !ok {
		return 0, nil
	}
	return g.Len(), nil
}

// Close is a no-op for the in-memory store.
func (s *HNSWEmbeddingStore) Close() error { return nil }

// hasNonZeroMagnitude reports whether vec has any non-zero component (a proxy
// for non-zero L2 magnitude — sufficient to keep cosine from producing NaN).
func hasNonZeroMagnitude(vec []float32) bool {
	for _, x := range vec {
		if x != 0 {
			return true
		}
	}
	return false
}

// copyMeta returns a shallow copy of m (nil for a nil input), so callers never
// receive a reference to the store's internal metadata sidecar.
func copyMeta(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// metadataMatches reports whether m satisfies every key/value in filter.
// A nil/empty filter matches everything; a filter key absent from m fails.
func metadataMatches(m, filter map[string]string) bool {
	if len(filter) == 0 {
		return true
	}
	for k, want := range filter {
		if got, ok := m[k]; !ok || got != want {
			return false
		}
	}
	return true
}

// ErrNoHNSWSnapshot is returned by Import when no snapshot files exist in dir.
var ErrNoHNSWSnapshot = errors.New("hnsw: no snapshot found in directory")

// Export writes the HNSW graph and metadata sidecar for each entityType to dir.
// Files: hnsw-<entityType>.bin (graph) + hnsw-<entityType>.meta.json (metadata).
// Called on clean shutdown (SIGTERM/SIGINT) so the next boot can skip hydration.
func (s *HNSWEmbeddingStore) Export(dir string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("hnsw export: mkdir: %w", err)
	}

	// Each entity type is written to temp files first, then renamed into place
	// only after both the .bin and .meta.json writes succeed (ARCH-2). A crash
	// or write failure mid-export therefore cannot leave a truncated file at
	// the final path, and a previously-committed good snapshot for that entity
	// type is never clobbered by a failed export attempt.
	for entityType, g := range s.graphs {
		binPath := filepath.Join(dir, "hnsw-"+entityType+".bin")
		binTmpPath := binPath + ".tmp"
		f, err := os.Create(binTmpPath)
		if err != nil {
			return fmt.Errorf("hnsw export: create %s: %w", binTmpPath, err)
		}
		if err := g.Export(f); err != nil {
			f.Close()
			os.Remove(binTmpPath)
			return fmt.Errorf("hnsw export: write graph %s: %w", entityType, err)
		}
		if err := f.Sync(); err != nil {
			f.Close()
			os.Remove(binTmpPath)
			return fmt.Errorf("hnsw export: sync graph %s: %w", entityType, err)
		}
		if err := f.Close(); err != nil {
			os.Remove(binTmpPath)
			return fmt.Errorf("hnsw export: close graph %s: %w", entityType, err)
		}

		metaPath := filepath.Join(dir, "hnsw-"+entityType+".meta.json")
		metaTmpPath := metaPath + ".tmp"
		m := s.meta[entityType]
		if m == nil {
			m = map[string]map[string]string{}
		}
		b, err := json.Marshal(m)
		if err != nil {
			os.Remove(binTmpPath)
			return fmt.Errorf("hnsw export: marshal meta %s: %w", entityType, err)
		}
		if err := os.WriteFile(metaTmpPath, b, 0o644); err != nil {
			os.Remove(binTmpPath)
			return fmt.Errorf("hnsw export: write meta %s: %w", entityType, err)
		}

		// Both temp files are on disk and synced/written successfully — commit
		// by renaming into place. os.Rename is atomic on POSIX filesystems.
		if err := os.Rename(binTmpPath, binPath); err != nil {
			os.Remove(binTmpPath)
			os.Remove(metaTmpPath)
			return fmt.Errorf("hnsw export: rename graph %s: %w", entityType, err)
		}
		if err := os.Rename(metaTmpPath, metaPath); err != nil {
			os.Remove(metaTmpPath)
			return fmt.Errorf("hnsw export: rename meta %s: %w", entityType, err)
		}
	}
	slog.Info("hnsw: snapshot exported", "dir", dir, "entity_types", len(s.graphs))
	return nil
}

// Import loads HNSW graphs and metadata sidecars from dir.
// Returns ErrNoHNSWSnapshot if no snapshot files exist.
//
// Import is all-or-nothing (ARCH-2): entries are decoded into local maps
// first, and s.graphs/s.meta are only replaced after every entity type has
// either parsed successfully or been legitimately skipped for a dimension
// mismatch (the existing discard path, unchanged in effect). Any other hard
// failure (bin open/parse, or a meta read/unmarshal error that isn't "file
// not found") aborts the whole call and returns before touching s.graphs/
// s.meta at all, so the caller's existing hydrate-from-Pebble fallback (which
// runs when CountByType==0) actually runs instead of operating on a partially
// installed graph.
func (s *HNSWEmbeddingStore) Import(dir string) error {
	entries, err := filepath.Glob(filepath.Join(dir, "hnsw-*.bin"))
	if err != nil || len(entries) == 0 {
		return ErrNoHNSWSnapshot
	}

	newGraphs := map[string]*hnsw.Graph[string]{}
	newMeta := map[string]map[string]map[string]string{}

	for _, binPath := range entries {
		base := filepath.Base(binPath)
		entityType := strings.TrimPrefix(strings.TrimSuffix(base, ".bin"), "hnsw-")

		f, err := os.Open(binPath)
		if err != nil {
			return fmt.Errorf("hnsw import: open %s: %w", binPath, err)
		}
		g := hnsw.NewGraph[string]()
		// Distance/M/EfSearch are restored from the binary by g.Import itself;
		// no need to pre-set them. bufio.NewReader is required because
		// coder/hnsw's varint decoder calls io.ByteReader.ReadByte, which
		// os.File does not implement.
		if err := g.Import(bufio.NewReader(f)); err != nil {
			f.Close()
			return fmt.Errorf("hnsw import: read graph %s: %w", entityType, err)
		}
		f.Close()
		// Discard a snapshot whose dimension no longer matches the configured
		// store dimension — e.g. after switching the embedding backend from
		// OpenAI text-embedding-3-large (3072) to local bge-m3 (1024). Loading
		// the stale graph would make the coder/hnsw library PANIC the moment a
		// new-dimension vector is added (graph.go assertDims). The index is a
		// derived structure; skipping the snapshot lets it rebuild empty at the
		// new dimension via hydration + re-embed.
		if s.dims > 0 && g.Len() > 0 && g.Dims() != s.dims {
			slog.Warn("hnsw import: snapshot dimension mismatch; discarding stale snapshot (index will rebuild at new dimension)",
				"entity_type", entityType, "snapshot_dims", g.Dims(), "config_dims", s.dims)
			continue
		}
		newGraphs[entityType] = g

		metaPath := filepath.Join(dir, "hnsw-"+entityType+".meta.json")
		b, err := os.ReadFile(metaPath)
		if err != nil {
			if os.IsNotExist(err) {
				newMeta[entityType] = map[string]map[string]string{}
				continue
			}
			return fmt.Errorf("hnsw import: read meta %s: %w", entityType, err)
		}
		var m map[string]map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("hnsw import: unmarshal meta %s: %w", entityType, err)
		}
		newMeta[entityType] = m
	}

	s.mu.Lock()
	s.graphs = newGraphs
	s.meta = newMeta
	s.mu.Unlock()

	slog.Info("hnsw: snapshot imported", "dir", dir, "entity_types", len(entries))
	return nil
}

// ImportWithStalenessCheck wraps Import with a staleness guard (ARCH-1): after
// a successful on-disk import, it compares each imported entity type's
// in-memory graph count against a caller-supplied Pebble-side truth count
// (e.g. EmbeddingStore.CountByType, the emb: keyspace's source-of-truth
// count). The HNSW snapshot fast-path otherwise skips Pebble hydration
// unconditionally — after any unclean shutdown, every vector upserted since
// the last clean-shutdown Export would be silently and permanently missing
// from the graph. If the imported graph undercounts the truth for any entity
// type at all (any undercount is unsafe — there is no positive tolerance),
// the ENTIRE imported snapshot (all entity types) is discarded, not just the
// stale one, so the caller's existing hydrate-from-Pebble fallback (which
// runs when CountByType("book")==0) runs normally; no new hydration
// mechanism is introduced here.
//
// Discarding all-or-nothing (rather than per-entity-type) is deliberate:
// dedup/lifecycle.go's PostInit gates hydration on the "book" count alone,
// and internal/dedup/engine.go's HydrateChromem re-populates BOTH "book" and
// "author" together when it runs. A per-type discard that kept a fresh
// "author" graph while discarding a stale "book" graph would cause
// HydrateChromem to Add authors on top of the already-populated author
// graph — the same Delete+Add per-layer-invariant class of bug
// (HNSW-CRASH-2026-06-18) that safeAdd/safeDelete guard against, just
// reached via a different path. Discarding every entity type when any one is
// stale keeps hydration's "populate from empty" precondition intact.
//
// truth may be nil, in which case the staleness check is skipped entirely
// (e.g. callers/tests without a Pebble store handy) and Import's result is
// returned unchanged.
func (s *HNSWEmbeddingStore) ImportWithStalenessCheck(dir string, truth func(entityType string) (int, error)) error {
	if err := s.Import(dir); err != nil {
		return err
	}
	if truth == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stale := false
	for entityType, g := range s.graphs {
		truthCount, err := truth(entityType)
		if err != nil {
			// Can't verify against the truth source; don't treat this entity
			// type as stale on an unrelated error.
			continue
		}
		if g.Len() < truthCount {
			slog.Warn("hnsw import: snapshot undercounts pebble truth; discarding entire imported snapshot (will rehydrate from pebble)",
				"entity_type", entityType, "graph_count", g.Len(), "truth_count", truthCount)
			stale = true
		}
	}
	if stale {
		s.graphs = map[string]*hnsw.Graph[string]{}
		s.meta = map[string]map[string]map[string]string{}
	}
	return nil
}

// Compile-time assertion.
var _ VectorANNStore = (*HNSWEmbeddingStore)(nil)
