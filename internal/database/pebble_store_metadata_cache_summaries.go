// file: internal/database/pebble_store_metadata_cache_summaries.go
// version: 1.1.0
// guid: 2e7b4c90-5d16-4a8f-b3c2-8f1e6a9d0c57
// last-edited: 2026-10-09

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// metadataCacheSummaryIndex is the in-process answer to ListMetadataCacheKeys:
// one summary (book id, FetchedAt, candidate count) per "metadata_cache:" row,
// kept current through the metadata-cache change log instead of re-reading
// the keyspace.
//
// Why: ListMetadataCacheKeys decoded EVERY cache entry -- every candidate of
// 40-56k books -- to report three small fields, on every call. GET
// /audiobooks/metadata/cached (the Library chip, which reads one number)
// measured 10-42 s on production, and the same scan is the first half of the
// review snapshot's full build, the stale-candidate filter, the bulk-apply
// preview, batch claims and the cache reap op.
//
// How it stays exact: the first call scans the keyspace once, having read
// MetadataCacheGeneration BEFORE the scan. Every later call asks
// MetadataCacheChangedSince(gen) for the rows written or deleted since and
// point-reads only those. Every writer of the keyspace bumps the generation
// AFTER its commit and records the row (see MetadataCacheGeneration), so a
// write the scan or a re-read might have missed is always named on the next
// call; re-reading a row twice is harmless. When the log cannot vouch for the
// changes (Reset, overrun), the index is rebuilt from a scan. The sorted slice
// is cached and rebuilt only when something changed.
type metadataCacheSummaryIndex struct {
	mu     sync.Mutex
	built  bool
	gen    uint64
	byKey  map[string]MetadataCacheSummary // keyed by the row key's book id
	sorted []MetadataCacheSummary          // nil when byKey changed since it was built
}

// metadataCacheSummaryRow is the slice of an entry a summary needs. Decoding
// into it skips the candidates' bytes instead of copying each one into a
// json.RawMessage.
type metadataCacheSummaryRow struct {
	BookID     string     `json:"book_id"`
	Candidates []struct{} `json:"candidates"`
	FetchedAt  time.Time  `json:"fetched_at"`
	Stale      bool       `json:"stale,omitempty"`
}

func (r *metadataCacheSummaryRow) summary() MetadataCacheSummary {
	return MetadataCacheSummary{BookID: r.BookID, FetchedAt: r.FetchedAt, CandidateCount: len(r.Candidates), Stale: r.Stale}
}

// ListMetadataCacheKeys returns one summary per cached entry, ordered by
// FetchedAt descending with the book id breaking ties. Caller paginates.
//
// The order is TOTAL, which is what callers that paginate need: FetchedAt
// alone leaves rows sharing a timestamp in an order that can differ between
// calls, so a client walking offset=0,50,100 could be handed one row twice
// and never see another. Entries written by the same batch fetch routinely
// share a timestamp.
//
// Served from metadataCacheSummaryIndex. The returned slice is the caller's
// own copy.
func (p *PebbleStore) ListMetadataCacheKeys() ([]MetadataCacheSummary, error) {
	idx := &p.cacheSummaries
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.built {
		ids, upTo, ok := p.MetadataCacheChangedSince(idx.gen)
		if ok {
			if err := p.refreshMetadataCacheSummaries(idx, ids); err != nil {
				return nil, err
			}
			idx.gen = upTo
		} else {
			idx.built = false
		}
	}
	if !idx.built {
		gen := p.MetadataCacheGeneration()
		byKey, err := p.scanMetadataCacheSummaries()
		if err != nil {
			return nil, err
		}
		idx.byKey, idx.gen, idx.built, idx.sorted = byKey, gen, true, nil
	}
	if idx.sorted == nil {
		sorted := make([]MetadataCacheSummary, 0, len(idx.byKey))
		for _, s := range idx.byKey {
			sorted = append(sorted, s)
		}
		sort.Slice(sorted, func(i, j int) bool {
			if !sorted[i].FetchedAt.Equal(sorted[j].FetchedAt) {
				return sorted[i].FetchedAt.After(sorted[j].FetchedAt)
			}
			return sorted[i].BookID < sorted[j].BookID
		})
		idx.sorted = sorted
	}
	out := make([]MetadataCacheSummary, len(idx.sorted))
	copy(out, idx.sorted)
	return out, nil
}

// refreshMetadataCacheSummaries re-reads the named rows into idx. A row that
// is gone or does not decode leaves the index, exactly as the scan skips it.
// A read error is returned and leaves idx.gen where it was, so the same rows
// are named again next call.
func (p *PebbleStore) refreshMetadataCacheSummaries(idx *metadataCacheSummaryIndex, ids []string) error {
	for _, id := range ids {
		val, closer, err := p.db.Get(metadataCacheKey(id))
		if errors.Is(err, pebble.ErrNotFound) {
			if _, had := idx.byKey[id]; had {
				delete(idx.byKey, id)
				idx.sorted = nil
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("pebble get metadata_cache:%s: %w", id, err)
		}
		var row metadataCacheSummaryRow
		decErr := json.Unmarshal(val, &row)
		_ = closer.Close()
		if decErr != nil {
			if _, had := idx.byKey[id]; had {
				delete(idx.byKey, id)
				idx.sorted = nil
			}
			continue
		}
		idx.byKey[id] = row.summary()
		idx.sorted = nil
	}
	return nil
}

// scanMetadataCacheSummaries reads every "metadata_cache:" row once.
// Corrupt rows are skipped rather than failing the whole list.
func (p *PebbleStore) scanMetadataCacheSummaries() (map[string]MetadataCacheSummary, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(metadataCacheKeyPrefix),
		UpperBound: []byte("metadata_cache;"), // ';' is one byte after ':'
	})
	if err != nil {
		return nil, fmt.Errorf("new iter metadata_cache: %w", err)
	}
	defer iter.Close()

	out := make(map[string]MetadataCacheSummary)
	for iter.First(); iter.Valid(); iter.Next() {
		var row metadataCacheSummaryRow
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			continue
		}
		out[string(iter.Key()[len(metadataCacheKeyPrefix):])] = row.summary()
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate metadata_cache: %w", err)
	}
	return out, nil
}
