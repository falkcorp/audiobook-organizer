// file: internal/database/hnsw_reconcile.go
// version: 1.0.0
// guid: 278c9ee2-6bbc-4472-8eef-50cef7140f62
// last-edited: 2026-11-08

// Reconciling a stale HNSW snapshot in place (review B2).
//
// A snapshot is stale when the Pebble embedding rows moved after it was
// exported, which after an unclean shutdown is every write since the last
// periodic export. Rebuilding the graph from Pebble costs about 4 ms per Add at
// 1024 dimensions (18,000 nodes: 73 s on a 10-core Mac), so instead the stale
// graph is installed and brought up to date:
//
//   - a key the graph lacks is added. coder/hnsw v0.6.1 adds NEW keys safely;
//     the reviewer's probe ran 300 of them with no panic.
//   - a key the graph holds that should no longer be indexed is tombstoned in
//     its metadata, never deleted: Delete, and a re-Add of an existing key,
//     corrupt the graph's per-layer invariants (HNSW-CRASH-2026-06-18).
//   - an indexed key whose vector changed cannot be fixed without a re-Add, so
//     it asks the caller for a full rebuild, naming the keys.
//
// The caller (the dedup engine) classifies every Pebble row with the same
// rules HydrateChromem uses and passes the verdicts in; this file only diffs
// them against the graph. Classifying every row, not only the keys missing
// from the graph, is what catches book changes in the crash window that leave
// the embedding row alone (a book deleted, merged into another version, made
// primary again, or moved to another series). The cost is the book lookups
// HydrateChromem already does, without the Adds.

package database

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/coder/hnsw"
)

// hnswTombstoneKey is the reserved metadata key that marks a tombstoned node.
// A tombstone stays in the graph and in the metadata sidecar, so it survives
// export and import; FindSimilar, Get and CountByType treat it as absent.
// Upsert rejects metadata that uses the key.
const hnswTombstoneKey = "__hnsw_tombstone"

// hnswMaxTombstoneShare is the share of a graph's nodes that may be tombstones
// after a reconcile before a full rebuild is asked for instead. Tombstones are
// still graph nodes: FindSimilar fetches limit*hnswOverFetchFactor neighbors and
// drops tombstones afterwards, so a graph that is a tenth tombstones returns
// roughly a tenth fewer usable candidates. Only a rebuild removes them.
const hnswMaxTombstoneShare = 0.10

// ErrNoReconcilePending is returned by Reconcile when no stale snapshot is
// waiting to be reconciled.
var ErrNoReconcilePending = errors.New("hnsw reconcile: no stale snapshot is pending")

func tombstoneMeta() map[string]string { return map[string]string{hnswTombstoneKey: "1"} }

func isTombstone(m map[string]string) bool { return m[hnswTombstoneKey] == "1" }

// ReconcileVerdict is the hydrate classifier's decision for one Pebble row.
type ReconcileVerdict int

const (
	// ReconcileIndex: the row belongs in the index, with ReconcileRow.Meta.
	ReconcileIndex ReconcileVerdict = iota + 1
	// ReconcileSkip: hydrate leaves this row out on purpose (empty vector,
	// stale model, orphaned book, non-primary version).
	ReconcileSkip
	// ReconcileUnknown: the classifier could not decide (a book lookup
	// error). A graph key is left as it is, and the reconciled store stays
	// incomplete, as a hydrate with lookup errors does.
	ReconcileUnknown
)

// ReconcileRow is one Pebble embedding row and its verdict.
type ReconcileRow struct {
	ID      string
	Vector  []float32
	Verdict ReconcileVerdict
	Meta    map[string]string
}

// ReconcileCounts says what Reconcile did with one entity type's rows.
type ReconcileCounts struct {
	Rows          int // Pebble rows supplied
	Kept          int // already right in the graph
	Added         int // missing from the graph, added
	Restored      int // a tombstone whose row is indexed again
	MetaRefreshed int // in the graph with the same vector, metadata updated
	Tombstoned    int // in the graph but skipped by hydrate, or gone from Pebble
	Skipped       int // skipped by hydrate and not in the graph
	LiveWrites    int // written by a live Upsert or Delete since the import; left alone
	Unknown       int // verdict ReconcileUnknown
}

// ReconcileReport is Reconcile's result.
type ReconcileReport struct {
	ByType map[string]ReconcileCounts
	// Changed lists "<entityType>/<id>" for each indexed key whose vector in
	// the graph differs from its Pebble row. Only a rebuild can fix those.
	Changed []string
	// AddErrors counts rows that should have been added but were not: an
	// invalid vector, or a recovered library panic.
	AddErrors     int
	FirstAddError error
	// RebuildReason is non-empty when the graph cannot be reconciled in place
	// and the caller should DiscardForRebuild and hydrate from Pebble. The
	// store then stays pending and incomplete.
	RebuildReason string
	// Complete reports that the reconcile finished and the store is now a
	// complete projection of the Pebble rows (exports resume).
	Complete bool
}

func (r *ReconcileReport) addError(err error) {
	r.AddErrors++
	if r.FirstAddError == nil {
		r.FirstAddError = err
	}
}

// unknown sums the Unknown verdicts across entity types.
func (r ReconcileReport) unknown() int {
	n := 0
	for _, c := range r.ByType {
		n += c.Unknown
	}
	return n
}

// SnapshotReconciler is implemented by a VectorANNStore that can bring a stale
// snapshot up to date in place (HNSWEmbeddingStore).
type SnapshotReconciler interface {
	// NeedsReconcile reports that a stale snapshot was imported and is
	// waiting for Reconcile.
	NeedsReconcile() bool
	// Reconcile diffs the classified Pebble rows of every entity type against
	// the graph and applies the result. See HNSWEmbeddingStore.Reconcile.
	Reconcile(ctx context.Context, truth map[string][]ReconcileRow) (ReconcileReport, error)
	// DiscardForRebuild drops every graph, leaving the store empty and
	// incomplete for a hydrate from Pebble.
	DiscardForRebuild()
}

// NeedsReconcile reports that a stale snapshot was imported and is waiting
// for Reconcile.
func (s *HNSWEmbeddingStore) NeedsReconcile() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingReconcile
}

// DiscardForRebuild drops every graph and its metadata, leaving the store
// empty, incomplete and no longer pending, so a hydrate from Pebble starts
// from empty (it re-Adds every key, which is only safe on an empty graph).
func (s *HNSWEmbeddingStore) DiscardForRebuild() {
	s.installSnapshot(map[string]*hnsw.Graph[string]{}, map[string]map[string]map[string]string{}, false, false)
}

// Reconcile brings a stale snapshot up to date with the Pebble rows. truth
// maps each entity type to every one of its Pebble rows, classified by the
// hydrate rules; it must name every entity type the store holds, or nothing is
// applied and an error is returned.
//
// Order of work:
//
//  1. Under one read lock, every indexed row is compared with the graph. If
//     any indexed key's vector changed, nothing is applied and RebuildReason
//     names the count (Changed lists the keys).
//  2. Each row is applied under its own write-lock hold, so FindSimilar and
//     live writes interleave with a long reconcile: a missing indexed key is
//     added, a graph key the row says to skip is tombstoned, a tombstone whose
//     row is indexed again is restored, stale metadata is refreshed.
//  3. Every graph key with no Pebble row is tombstoned.
//  4. A damaged graph, or one whose tombstones exceed hnswMaxTombstoneShare of
//     its nodes, asks for a rebuild. Otherwise the store stops being pending,
//     and is complete unless a row was Unknown or failed to add.
//
// A key that a live Upsert or Delete wrote after the import is left alone at
// every step: the live write is newer than the Pebble rows the caller read.
// A cancelled ctx returns the partial report and ctx.Err(); the store stays
// pending and incomplete.
func (s *HNSWEmbeddingStore) Reconcile(ctx context.Context, truth map[string][]ReconcileRow) (ReconcileReport, error) {
	report := ReconcileReport{ByType: make(map[string]ReconcileCounts, len(truth))}

	s.mu.RLock()
	if !s.pendingReconcile {
		s.mu.RUnlock()
		return report, ErrNoReconcilePending
	}
	for _, entityType := range slices.Sorted(maps.Keys(s.graphs)) {
		if _, ok := truth[entityType]; !ok {
			s.mu.RUnlock()
			return report, fmt.Errorf("hnsw reconcile: no Pebble rows supplied for entity type %q", entityType)
		}
	}
	report.Changed = s.changedKeysLocked(truth)
	s.mu.RUnlock()
	if len(report.Changed) > 0 {
		report.RebuildReason = fmt.Sprintf("%d indexed key(s) have a different vector than the snapshot", len(report.Changed))
		return report, nil
	}

	seen := make(map[string]map[string]struct{}, len(truth))
	for _, entityType := range slices.Sorted(maps.Keys(truth)) {
		rows := truth[entityType]
		c := ReconcileCounts{Rows: len(rows)}
		ids := make(map[string]struct{}, len(rows))
		for i, row := range rows {
			if i%512 == 0 {
				if err := ctx.Err(); err != nil {
					report.ByType[entityType] = c
					return report, err
				}
			}
			ids[row.ID] = struct{}{}
			s.mu.Lock()
			s.reconcileRowLocked(entityType, row, &c, &report)
			s.mu.Unlock()
		}
		seen[entityType] = ids
		report.ByType[entityType] = c
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entityType := range slices.Sorted(maps.Keys(s.meta)) {
		c := report.ByType[entityType]
		for id, m := range s.meta[entityType] {
			if _, ok := seen[entityType][id]; ok {
				continue
			}
			if _, live := s.touched[entityType][id]; live {
				continue
			}
			if isTombstone(m) {
				continue
			}
			s.setMetaLocked(entityType, id, tombstoneMeta())
			s.gen++
			c.Tombstoned++
		}
		report.ByType[entityType] = c
	}

	report.RebuildReason = s.rebuildReasonLocked(report)
	if report.RebuildReason != "" {
		return report, nil
	}
	s.pendingReconcile = false
	s.touched = nil
	s.complete = report.unknown() == 0 && report.AddErrors == 0
	report.Complete = s.complete
	return report, nil
}

// changedKeysLocked returns "<entityType>/<id>" for every indexed row whose
// key is in the graph with a different vector, skipping live writes. Caller
// must hold s.mu (read lock).
func (s *HNSWEmbeddingStore) changedKeysLocked(truth map[string][]ReconcileRow) []string {
	var changed []string
	for _, entityType := range slices.Sorted(maps.Keys(truth)) {
		g, ok := s.graphs[entityType]
		if !ok {
			continue
		}
		for _, row := range truth[entityType] {
			if row.Verdict != ReconcileIndex {
				continue
			}
			if _, live := s.touched[entityType][row.ID]; live {
				continue
			}
			if stored, in := g.Lookup(row.ID); in && !sameVector(stored, row.Vector) {
				changed = append(changed, entityType+"/"+row.ID)
			}
		}
	}
	return changed
}

// reconcileRowLocked applies one row. Caller must hold s.mu (write lock) for
// the whole call, so no live write lands between the Lookup and the Add.
func (s *HNSWEmbeddingStore) reconcileRowLocked(entityType string, row ReconcileRow, c *ReconcileCounts, report *ReconcileReport) {
	if _, live := s.touched[entityType][row.ID]; live {
		c.LiveWrites++
		return
	}
	var stored hnsw.Vector
	inGraph := false
	if g, ok := s.graphs[entityType]; ok {
		stored, inGraph = g.Lookup(row.ID)
	}

	switch row.Verdict {
	case ReconcileSkip:
		switch {
		case !inGraph:
			c.Skipped++
		case isTombstone(s.meta[entityType][row.ID]):
			c.Kept++
		default:
			s.setMetaLocked(entityType, row.ID, tombstoneMeta())
			s.gen++
			c.Tombstoned++
		}

	case ReconcileIndex:
		if inGraph {
			// The pre-pass found no changed vector and live writes are
			// skipped above, so this only fires if the caller's rows hold
			// two different vectors for one key.
			if !sameVector(stored, row.Vector) {
				report.Changed = append(report.Changed, entityType+"/"+row.ID)
				return
			}
			want := metaOrEmpty(row.Meta)
			cur := s.meta[entityType][row.ID]
			switch {
			case isTombstone(cur):
				s.setMetaLocked(entityType, row.ID, want)
				s.gen++
				c.Restored++
			case !maps.Equal(cur, want):
				s.setMetaLocked(entityType, row.ID, want)
				s.gen++
				c.MetaRefreshed++
			default:
				c.Kept++
			}
			return
		}
		if err := s.checkVector("hnsw reconcile", row.ID, row.Vector); err != nil {
			report.addError(err)
			return
		}
		if _, reserved := row.Meta[hnswTombstoneKey]; reserved {
			report.addError(fmt.Errorf("hnsw reconcile %s: metadata key %q is reserved", row.ID, hnswTombstoneKey))
			return
		}
		g := s.graphFor(entityType)
		if err := s.safeAdd(g, row.ID, row.Vector); err != nil {
			s.gen++
			s.markDamaged(entityType, row.ID, err)
			report.addError(err)
			return
		}
		s.setMetaLocked(entityType, row.ID, metaOrEmpty(row.Meta))
		s.gen++
		c.Added++

	default: // ReconcileUnknown, or a verdict this store does not know
		c.Unknown++
	}
}

// rebuildReasonLocked returns why the reconciled graph should be rebuilt
// instead of kept, or "". Caller must hold s.mu.
func (s *HNSWEmbeddingStore) rebuildReasonLocked(report ReconcileReport) string {
	if len(report.Changed) > 0 {
		return fmt.Sprintf("%d indexed key(s) have a different vector than the snapshot", len(report.Changed))
	}
	if s.damaged {
		return "a recovered library panic may have damaged the graph"
	}
	for _, entityType := range slices.Sorted(maps.Keys(s.graphs)) {
		n := s.graphs[entityType].Len()
		t := s.tombstones[entityType]
		if n > 0 && float64(t) > hnswMaxTombstoneShare*float64(n) {
			return fmt.Sprintf("tombstones are %d of the %d %s nodes, over the %.0f%% that FindSimilar recall tolerates",
				t, n, entityType, hnswMaxTombstoneShare*100)
		}
	}
	return ""
}

// snapshotKeysConsistent reports whether each decoded graph's metadata lists
// exactly the graph's keys. Reconcile reads the graph's key set from the
// metadata (coder/hnsw does not expose its keys), so a snapshot that fails
// this cannot be reconciled.
func snapshotKeysConsistent(graphs map[string]*hnsw.Graph[string], meta map[string]map[string]map[string]string) error {
	for _, entityType := range slices.Sorted(maps.Keys(graphs)) {
		g, m := graphs[entityType], meta[entityType]
		if len(m) != g.Len() {
			return fmt.Errorf("%s: metadata lists %d keys, the graph holds %d", entityType, len(m), g.Len())
		}
		for id := range m {
			if _, ok := g.Lookup(id); !ok {
				return fmt.Errorf("%s: metadata key %q is not in the graph", entityType, id)
			}
		}
	}
	return nil
}

// sameVector reports whether two vectors hold the same embedding at the
// precision Pebble stores it. The EmbeddingStore keeps vectors as float16
// (embVecVersion1), but the live embed paths mirror the embedding API's full
// float32 vector into the graph, so a graph vector and its unchanged Pebble row
// differ below float16 precision. Comparing the float16 encodings treats those
// as equal, and any real re-embed as changed. A hydrated key, and a legacy
// float32 (v0) row, compare equal trivially.
func sameVector(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if float32ToFloat16(a[i]) != float32ToFloat16(b[i]) {
			return false
		}
	}
	return true
}

var _ SnapshotReconciler = (*HNSWEmbeddingStore)(nil)
