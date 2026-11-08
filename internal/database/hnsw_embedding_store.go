// file: internal/database/hnsw_embedding_store.go
// version: 1.8.2
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-11-08

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
// EmbeddingStore on boot; it is not a source of truth. It persists a snapshot
// (Graph.Export/Import) so a restart can skip hydration. The server exports
// periodically in the background (ExportIfDirty, which skips when nothing has
// changed) and once more at stop when the shutdown budget allows. A snapshot is
// only ever written from a graph that is known to be a COMPLETE projection of
// the Pebble rows (see SetComplete and ErrHNSWIncomplete); exporting a graph
// that is still being hydrated would record the current Pebble row count next
// to a partial graph, and the next boot would load it as fresh.

package database

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coder/hnsw"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
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

	// gen counts mutations of graphs/meta (every successful Upsert, every Delete,
	// every snapshot import/discard). ExportIfDirty compares it with exportedGen
	// to skip exports when nothing changed. Guarded by mu.
	gen uint64
	// complete reports that graphs/meta are a complete projection of the Pebble
	// rows, so a snapshot of them is safe to reuse at the next boot. A new store
	// starts false; a snapshot import sets it, a stale discard clears it, and the
	// hydrate path sets it via SetComplete. Guarded by mu.
	complete bool

	// exportMu serializes exports (they share the .tmp paths) and guards the
	// fields below. Lock order: exportMu before mu.
	exportMu      sync.Mutex
	exportedGen   uint64        // gen read before the last successful export's truth reads
	lastExportAt  time.Time     // zero until an export has succeeded in this process
	lastExportDur time.Duration // wall time of the last successful export

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
		// A recovered library panic can leave the graph part-mutated, so count it
		// as a change: the cost is one extra export, never a missed one.
		s.gen++
		return err
	}
	if meta == nil {
		meta = map[string]string{}
	}
	// Store a defensive copy so the caller can't mutate our sidecar.
	s.meta[entityType][entityID] = copyMeta(meta)
	s.gen++
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
	s.gen++ // always: a delete removes meta even when the graph has no such key
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
	k := min(maxResults*hnswOverFetchFactor, hnswEfSearch)
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
	maps.Copy(cp, m)
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

// hnswLog logs the snapshot export and import.
var hnswLog = logger.New("database")

// ErrNoHNSWSnapshot is returned by Import when no snapshot files exist in dir.
var ErrNoHNSWSnapshot = errors.New("hnsw: no snapshot found in directory")

// ErrHNSWIncomplete is returned by the export methods when the graph is not
// known to be a complete projection of the Pebble rows (see SetComplete). A
// snapshot of a partially built graph would record the current Pebble row count
// beside the incomplete graph, and the next boot would load it as fresh.
var ErrHNSWIncomplete = errors.New("hnsw: the graph is not a complete projection of the store; snapshot not written")

// SnapshotCompleteness is implemented by a VectorANNStore whose snapshot export
// must know whether its graph is a complete projection (HNSWEmbeddingStore).
type SnapshotCompleteness interface{ SetComplete(bool) }

// SetComplete records that the graph is (or is no longer) a complete projection
// of the Pebble rows; only a complete graph is exported.
//
// A new store starts incomplete. A successful snapshot Import marks it complete,
// a stale discard marks it incomplete, and the hydrate-from-Pebble path calls
// SetComplete(false) before it starts and SetComplete(true) only when it ran to
// the end.
func (s *HNSWEmbeddingStore) SetComplete(complete bool) {
	s.mu.Lock()
	s.complete = complete
	s.mu.Unlock()
}

// Export writes the HNSW graph and metadata sidecar for each entityType to dir.
// Files: hnsw-<entityType>.bin (graph) + hnsw-<entityType>.meta.json (metadata).
// It writes no truth sidecar, so the next ImportWithStalenessCheck discards the
// result; the server uses ExportIfDirty (periodically, and at stop when the
// shutdown budget allows) so the next boot can skip hydration. Returns
// ErrHNSWIncomplete for a graph that is not a complete projection.
func (s *HNSWEmbeddingStore) Export(dir string) error {
	return s.ExportWithTruth(dir, nil)
}

// snapshotTruth is the truth-sidecar payload written next to each graph.
//
// TruthCount is the Pebble-side row count for this entity type AS OF the
// export. It is what makes the staleness check on import meaningful: the graph
// itself is a FILTERED projection of those rows (HydrateChromem drops empty
// vectors, stale-model rows, orphans whose book no longer exists, and
// non-primary versions), so the graph size and the Pebble row count are
// different quantities and comparing them directly is a category error.
type snapshotTruth struct {
	TruthCount int `json:"truth_count"`
	GraphCount int `json:"graph_count"`
}

func truthPath(dir, entityType string) string {
	return filepath.Join(dir, "hnsw-"+entityType+".truth.json")
}

// ExportResult reports what ExportIfDirty did.
type ExportResult struct {
	Exported bool          // a snapshot was written
	Clean    bool          // skipped: nothing changed since the last export in this process
	Duration time.Duration // wall time of the export when Exported
}

// ExportWithTruth is Export plus a record of the Pebble truth count per entity
// type, which ImportWithStalenessCheck needs to decide whether the snapshot has
// fallen behind. truth may be nil, in which case no sidecar is written and the
// next import will treat the snapshot as unverifiable and discard it.
//
// Returns ErrHNSWIncomplete, writing nothing, when the graph is not a complete
// projection of the Pebble rows (see SetComplete).
//
// The truth counts are read BEFORE the graph is serialized. Reading them after
// could record a count that includes Pebble rows the serialized graph lacks,
// which the next boot would accept as fresh (vectors silently missing); reading
// first can only make the snapshot look stale, which is safe. A truth error for
// an entity type is best-effort: that type gets no truth sidecar.
func (s *HNSWEmbeddingStore) ExportWithTruth(dir string, truth func(entityType string) (int, error)) error {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	_, err := s.exportLocked(dir, truth)
	return err
}

// ExportIfDirty exports like ExportWithTruth unless nothing has changed since
// the last successful export in this process, in which case it returns
// Clean=true without touching the disk. An empty store that has never been
// mutated is also Clean: it must never be written over a good snapshot. The
// check and the export happen under one exportMu hold, so concurrent callers
// cannot both export. An incomplete graph returns ErrHNSWIncomplete (checked
// after the clean checks, so a store that has nothing to export is not an
// error while it is still hydrating).
func (s *HNSWEmbeddingStore) ExportIfDirty(dir string, truth func(string) (int, error)) (ExportResult, error) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()

	s.mu.RLock()
	gen, empty := s.gen, len(s.graphs) == 0
	s.mu.RUnlock()
	if gen == 0 && empty {
		return ExportResult{Clean: true}, nil
	}
	if !s.lastExportAt.IsZero() && gen == s.exportedGen {
		return ExportResult{Clean: true}, nil
	}

	took, err := s.exportLocked(dir, truth)
	if err != nil {
		return ExportResult{}, err
	}
	return ExportResult{Exported: true, Duration: took}, nil
}

// LastExport returns when the last successful export finished and how long it
// took. Both are zero if no export has succeeded in this process.
func (s *HNSWEmbeddingStore) LastExport() (at time.Time, took time.Duration) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	return s.lastExportAt, s.lastExportDur
}

// pendingExport is one entity type's serialized-but-uncommitted snapshot.
type pendingExport struct {
	entityType string
	binTmp     string
	binPath    string
	metaTmp    string
	metaPath   string
	f          *os.File // open .bin.tmp, written and flushed but not yet synced
	metaBytes  []byte
	graphCount int
}

// abortPending closes and removes every temp file of an export that failed
// before its commit renames.
func abortPending(pending []*pendingExport) {
	for _, p := range pending {
		if p.f != nil {
			p.f.Close()
			p.f = nil
		}
		os.Remove(p.binTmp)
		os.Remove(p.metaTmp)
	}
}

// serializeSnapshot is the only part of an export that holds s.mu: it writes
// every graph through a 1 MiB buffer to its .bin.tmp and marshals the metadata
// into memory, under one RLock so they describe the same instant. Fsync, rename
// and the sidecars happen after the lock is released.
func (s *HNSWEmbeddingStore) serializeSnapshot(dir string) (pending []*pendingExport, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.complete {
		return nil, ErrHNSWIncomplete
	}
	defer func() {
		if err != nil {
			abortPending(pending)
			pending = nil
		}
	}()

	for entityType, g := range s.graphs {
		p := &pendingExport{
			entityType: entityType,
			binPath:    filepath.Join(dir, "hnsw-"+entityType+".bin"),
			metaPath:   filepath.Join(dir, "hnsw-"+entityType+".meta.json"),
			graphCount: g.Len(),
		}
		p.binTmp = p.binPath + ".tmp"
		p.metaTmp = p.metaPath + ".tmp"

		f, cerr := os.Create(p.binTmp)
		if cerr != nil {
			return pending, fmt.Errorf("hnsw export: create %s: %w", p.binTmp, cerr)
		}
		p.f = f
		pending = append(pending, p)

		// coder/hnsw's Export issues many tiny writes per node (about 92 syscalls
		// each on an unbuffered *os.File), which dominated the export time.
		bw := bufio.NewWriterSize(f, 1<<20)
		if werr := g.Export(bw); werr != nil {
			return pending, fmt.Errorf("hnsw export: write graph %s: %w", entityType, werr)
		}
		if ferr := bw.Flush(); ferr != nil {
			return pending, fmt.Errorf("hnsw export: write graph %s: %w", entityType, ferr)
		}

		m := s.meta[entityType]
		if m == nil {
			m = map[string]map[string]string{}
		}
		b, merr := json.Marshal(m)
		if merr != nil {
			return pending, fmt.Errorf("hnsw export: marshal meta %s: %w", entityType, merr)
		}
		p.metaBytes = b
	}
	return pending, nil
}

// writeFileSynced writes data to path and fsyncs it before closing.
func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// exportLocked is the body of every export. The caller must hold s.exportMu,
// which serializes exports because they share the .tmp paths. It returns the
// wall time of the whole export and, on success, records exportedGen,
// lastExportAt and lastExportDur.
//
// exportedGen is the gen read BEFORE the truth reads, not the one serialized.
// A mutation that lands between the truth reads and the serialization is in
// the snapshot but not in its truth count, so the next boot finds the snapshot
// stale; recording the earlier gen keeps that mutation dirty, and the next
// ExportIfDirty writes a snapshot whose count includes it.
func (s *HNSWEmbeddingStore) exportLocked(dir string, truth func(entityType string) (int, error)) (time.Duration, error) {
	start := time.Now()

	// Cheap early refusal, the gen this export will record, and the
	// entity-type list the truth reads need. The authoritative completeness
	// check is the one inside serializeSnapshot.
	s.mu.RLock()
	if !s.complete {
		s.mu.RUnlock()
		return 0, ErrHNSWIncomplete
	}
	gen := s.gen
	types := make([]string, 0, len(s.graphs))
	for et := range s.graphs {
		types = append(types, et)
	}
	s.mu.RUnlock()

	// Truth FIRST, with no lock held. See ExportWithTruth.
	type truthReading struct {
		count int
		err   error
	}
	var truths map[string]truthReading
	if truth != nil {
		truths = make(map[string]truthReading, len(types))
		for _, et := range types {
			c, terr := truth(et)
			truths[et] = truthReading{count: c, err: terr}
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("hnsw export: mkdir: %w", err)
	}

	pending, err := s.serializeSnapshot(dir)
	if err != nil {
		return 0, err
	}

	// Each entity type is written to temp files first, then renamed into place
	// only after every temp file has been written and synced (ARCH-2). A crash
	// or write failure before the renames therefore cannot leave a truncated
	// file at a final path, and a previously committed good snapshot is never
	// clobbered by a failed export attempt.
	for _, p := range pending {
		if err := writeFileSynced(p.metaTmp, p.metaBytes); err != nil {
			abortPending(pending)
			return 0, fmt.Errorf("hnsw export: write meta %s: %w", p.entityType, err)
		}
		if err := p.f.Sync(); err != nil {
			abortPending(pending)
			return 0, fmt.Errorf("hnsw export: sync graph %s: %w", p.entityType, err)
		}
		cerr := p.f.Close()
		p.f = nil
		if cerr != nil {
			abortPending(pending)
			return 0, fmt.Errorf("hnsw export: close graph %s: %w", p.entityType, cerr)
		}
	}

	// Drop the previous truth sidecars before the new graph lands, so a crash
	// between the renames and the new sidecar leaves "unverifiable, discard"
	// rather than an old count paired with a new graph.
	for _, p := range pending {
		_ = os.Remove(truthPath(dir, p.entityType))
	}

	// Every temp file is on disk and synced: commit by renaming into place.
	// os.Rename is atomic on POSIX filesystems.
	for _, p := range pending {
		if err := os.Rename(p.binTmp, p.binPath); err != nil {
			abortPending(pending)
			return 0, fmt.Errorf("hnsw export: rename graph %s: %w", p.entityType, err)
		}
		if err := os.Rename(p.metaTmp, p.metaPath); err != nil {
			abortPending(pending)
			return 0, fmt.Errorf("hnsw export: rename meta %s: %w", p.entityType, err)
		}
	}

	// Truth sidecars. Written last and best-effort: a missing sidecar makes the
	// next import discard this snapshot (safe), whereas failing the whole export
	// would throw away a good graph over a bookkeeping file.
	for _, p := range pending {
		if truth == nil {
			continue
		}
		tr, ok := truths[p.entityType]
		if !ok {
			// The graph for this type was created after the truth reads began.
			continue
		}
		if tr.err != nil {
			hnswLog.Warn("hnsw export: truth count for %s unavailable; snapshot will not be reusable: %v", p.entityType, tr.err)
			continue
		}
		tb, merr := json.Marshal(snapshotTruth{TruthCount: tr.count, GraphCount: p.graphCount})
		if merr != nil {
			hnswLog.Warn("hnsw export: marshal truth sidecar for %s failed: %v", p.entityType, merr)
			continue
		}
		tTmp := truthPath(dir, p.entityType) + ".tmp"
		if werr := writeFileSynced(tTmp, tb); werr != nil {
			os.Remove(tTmp)
			hnswLog.Warn("hnsw export: write truth sidecar for %s failed: %v", p.entityType, werr)
			continue
		}
		if rerr := os.Rename(tTmp, truthPath(dir, p.entityType)); rerr != nil {
			os.Remove(tTmp)
			hnswLog.Warn("hnsw export: rename truth sidecar for %s failed: %v", p.entityType, rerr)
		}
	}

	took := time.Since(start)
	s.exportedGen = gen
	s.lastExportAt = time.Now()
	s.lastExportDur = took
	hnswLog.Info("hnsw: snapshot exported to %s: %d entity type(s) in %s", dir, len(pending), took)
	return took, nil
}

// loadSnapshot decodes every snapshot in dir into local maps without touching
// the store. Returns ErrNoHNSWSnapshot if no snapshot files exist.
//
// It is all-or-nothing (ARCH-2): any hard failure (bin open/parse, or a meta
// read/unmarshal error that isn't "file not found") returns before the caller
// installs anything, so the existing hydrate-from-Pebble fallback (which runs
// when CountByType==0) actually runs instead of operating on a partially
// installed graph. A snapshot whose dimension no longer matches is skipped (the
// existing discard path).
func (s *HNSWEmbeddingStore) loadSnapshot(dir string) (map[string]*hnsw.Graph[string], map[string]map[string]map[string]string, int, error) {
	entries, err := filepath.Glob(filepath.Join(dir, "hnsw-*.bin"))
	if err != nil || len(entries) == 0 {
		return nil, nil, 0, ErrNoHNSWSnapshot
	}

	newGraphs := map[string]*hnsw.Graph[string]{}
	newMeta := map[string]map[string]map[string]string{}

	for _, binPath := range entries {
		base := filepath.Base(binPath)
		entityType := strings.TrimPrefix(strings.TrimSuffix(base, ".bin"), "hnsw-")

		f, err := os.Open(binPath)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("hnsw import: open %s: %w", binPath, err)
		}
		g := hnsw.NewGraph[string]()
		// Distance/M/EfSearch are restored from the binary by g.Import itself;
		// no need to pre-set them. bufio.NewReader is required because
		// coder/hnsw's varint decoder calls io.ByteReader.ReadByte, which
		// os.File does not implement.
		if err := g.Import(bufio.NewReader(f)); err != nil {
			f.Close()
			return nil, nil, 0, fmt.Errorf("hnsw import: read graph %s: %w", entityType, err)
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
			return nil, nil, 0, fmt.Errorf("hnsw import: read meta %s: %w", entityType, err)
		}
		var m map[string]map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, nil, 0, fmt.Errorf("hnsw import: unmarshal meta %s: %w", entityType, err)
		}
		newMeta[entityType] = m
	}
	return newGraphs, newMeta, len(entries), nil
}

// installSnapshot swaps in decoded graphs/meta, bumps gen, and sets the
// completeness flag, all under one write lock.
func (s *HNSWEmbeddingStore) installSnapshot(graphs map[string]*hnsw.Graph[string], meta map[string]map[string]map[string]string, complete bool) {
	s.mu.Lock()
	s.graphs = graphs
	s.meta = meta
	s.gen++
	s.complete = complete
	s.mu.Unlock()
}

// Import loads HNSW graphs and metadata sidecars from dir.
// Returns ErrNoHNSWSnapshot if no snapshot files exist.
//
// Import is all-or-nothing (ARCH-2): entries are decoded into local maps
// first, and s.graphs/s.meta are only replaced after every entity type has
// either parsed successfully or been legitimately skipped for a dimension
// mismatch. Any other hard failure aborts the whole call before touching
// s.graphs/s.meta at all. A successful import marks the store complete: the
// snapshot was only ever written from a complete graph.
func (s *HNSWEmbeddingStore) Import(dir string) error {
	graphs, meta, n, err := s.loadSnapshot(dir)
	if err != nil {
		return err
	}
	s.installSnapshot(graphs, meta, true)
	hnswLog.Info("hnsw: snapshot imported from %s: %d entity type(s)", dir, n)
	return nil
}

// ImportWithStalenessCheck is Import with a staleness guard (ARCH-1): after
// decoding the on-disk snapshot, it compares each entity type's recorded Pebble
// row count (the truth sidecar) against a caller-supplied current truth count
// (e.g. EmbeddingStore.CountByType, the emb: keyspace's source-of-truth
// count). The HNSW snapshot fast-path otherwise skips Pebble hydration
// unconditionally — after any unclean shutdown, every vector upserted since
// the last export would be silently and permanently missing from the graph. If
// the counts differ for any entity type at all (there is no tolerance), the
// ENTIRE snapshot (all entity types) is discarded, not just the stale one, so
// the caller's existing hydrate-from-Pebble fallback (which runs when
// CountByType("book")==0) runs normally; no new hydration mechanism is
// introduced here. A fresh snapshot leaves the store complete; a discard leaves
// it empty and incomplete.
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
	graphs, meta, n, err := s.loadSnapshot(dir)
	if err != nil {
		return err
	}
	if truth == nil {
		s.installSnapshot(graphs, meta, true)
		hnswLog.Info("hnsw: snapshot imported from %s: %d entity type(s)", dir, n)
		return nil
	}

	// The staleness check runs on the decoded local graphs, so the Pebble truth
	// reads happen with no store lock held and the swap below is one atomic step
	// (the store is never briefly "complete" with a stale graph installed).
	stale := false
	for entityType, g := range graphs {
		currentTruth, err := truth(entityType)
		if err != nil {
			// Can't verify against the truth source; don't treat this entity
			// type as stale on an unrelated error.
			continue
		}

		// Compare the CURRENT Pebble row count against the count recorded when
		// this snapshot was exported — not against the graph size.
		//
		// The graph is a FILTERED projection of the Pebble rows: HydrateChromem
		// skips empty vectors, stale-model rows, orphans whose book row is gone,
		// and non-primary versions. Measured in production on 2026-08-29 the
		// graph held 17,706 of 39,658 book rows (44.6%), so the old
		// `g.Len() < truthCount` test was true by construction on every boot.
		// The snapshot was therefore discarded and fully rehydrated EVERY
		// restart, which is exactly the cost the snapshot exists to avoid, and
		// the warning that fired was a permanent fixture rather than a signal.
		recorded, rerr := readSnapshotTruth(dir, entityType)
		if rerr != nil {
			hnswLog.Warn("hnsw import: no usable truth sidecar for %s; discarding snapshot (the next export will write one): %v", entityType, rerr)
			stale = true
			continue
		}
		if currentTruth != recorded.TruthCount {
			slog.Info("hnsw import: pebble row count moved since export; discarding snapshot (will rehydrate from pebble)",
				"entity_type", entityType, "recorded_truth", recorded.TruthCount,
				"current_truth", currentTruth, "graph_count", g.Len())
			stale = true
		}
	}
	if stale {
		s.installSnapshot(map[string]*hnsw.Graph[string]{}, map[string]map[string]map[string]string{}, false)
		return nil
	}
	s.installSnapshot(graphs, meta, true)
	hnswLog.Info("hnsw: snapshot imported from %s: %d entity type(s)", dir, n)
	return nil
}

// readSnapshotTruth loads the truth sidecar for one entity type. A missing or
// unparseable sidecar is an error, which the caller treats as "unverifiable,
// therefore stale" — the safe direction, and self-healing because the next
// export writes a fresh one.
func readSnapshotTruth(dir, entityType string) (snapshotTruth, error) {
	var st snapshotTruth
	b, err := os.ReadFile(truthPath(dir, entityType))
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("unmarshal truth sidecar: %w", err)
	}
	return st, nil
}

// Compile-time assertions.
var (
	_ VectorANNStore       = (*HNSWEmbeddingStore)(nil)
	_ SnapshotCompleteness = (*HNSWEmbeddingStore)(nil)
)
