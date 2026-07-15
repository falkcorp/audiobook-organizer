// file: internal/database/review_store.go
// version: 1.0.0
// guid: 4f2c8a91-6b3d-4e57-9a02-8d1f5c7e3b40
// last-edited: 2026-07-13

package database

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// ─── Review-queue store (PR-A1) ──────────────────────────────────────────────
//
// A generic, producer-agnostic queue of items flagged for a human decision. v1
// producer is the regroup op (Track B); A1 is pure infra. The design mirrors the
// dedup candidate store's Status/secondary-index pattern (embedding_store.go) but
// lives in its own keyspace so the two never overload each other.
//
// Key-space layout (PebbleDB):
//
//	review_item:r:<id>                     → ReviewItem JSON  (the record)
//	review_item:status:<status>:<id>       → empty            (status secondary index)
//	review_item:dedupkey:<dedupKey>        → <id>             (idempotency index)
//
// Records live under the "r:" sub-prefix so a full-record scan (prefix
// "review_item:r:") never collides with the "status:" / "dedupkey:" indexes —
// exactly the dedup:r: / dedup:s: split.
//
// Unlike the dedup status index, this keyspace is greenfield: the status index
// is authoritative from the very first row, so there is no build-flag /
// fail-open-to-full-scan machinery. A status-filtered list/count trusts the
// index directly (still re-checking the record's status on point-read to guard
// against a concurrent status change racing the scan).
const (
	reviewItemRecPfx    = "review_item:r:"
	reviewItemStatusPfx = "review_item:status:"
	reviewItemDedupPfx  = "review_item:dedupkey:"
)

// Review item status values (stringly-typed, matching dedup/house style).
const (
	ReviewStatusPending  = "pending"
	ReviewStatusApproved = "approved"
	ReviewStatusRejected = "rejected"
	ReviewStatusApplied  = "applied"
)

// ReviewItem is one thing the system has flagged for a human decision.
//
// DedupKey is the STABLE upsert target — a normalized hash of (Kind, FolderRef)
// computed by the producer. Re-running a producer's scan UPSERTs by DedupKey so
// the same folder never piles up duplicate rows and a prior human decision
// (rejected) is never resurfaced. See UpsertReviewItem for the exact idempotency
// contract.
type ReviewItem struct {
	ID        string    `json:"id"`         // ULID
	Kind      string    `json:"kind"`       // e.g. "regroup.multidisc", "regroup.anthology"
	DedupKey  string    `json:"dedup_key"`  // STABLE: hash(Kind + FolderRef) — upsert target
	FolderRef string    `json:"folder_ref"` // grandparent folder path this hold is about
	Status    string    `json:"status"`     // pending | approved | rejected | applied
	Summary   string    `json:"summary"`    // one-line human label
	Payload   string    `json:"payload"`    // JSON blob: folder, listing, proposed action, member IDs, ...
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewFilter controls ListReviewItems / list-by-status queries.
type ReviewFilter struct {
	Status string
	Kind   string
	Limit  int
	Offset int
}

// ReviewKindStat holds a count for one (Kind, Status) grouping.
type ReviewKindStat struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Count  int    `json:"count"`
}

func reviewItemRecKey(id string) []byte { return []byte(reviewItemRecPfx + id) }

func reviewItemStatusKey(status, id string) []byte {
	return []byte(reviewItemStatusPfx + status + ":" + id)
}

func reviewItemDedupIdxKey(dedupKey string) []byte {
	return []byte(reviewItemDedupPfx + dedupKey)
}

// getReviewItem is the internal point-read; returns (nil, nil) when not found.
func (p *PebbleStore) getReviewItem(id string) (*ReviewItem, error) {
	val, closer, err := p.db.Get(reviewItemRecKey(id))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get review item %s: %w", id, err)
	}
	var item ReviewItem
	unmarshalErr := json.Unmarshal(val, &item)
	closer.Close()
	if unmarshalErr != nil {
		return nil, fmt.Errorf("unmarshal review item %s: %w", id, unmarshalErr)
	}
	return &item, nil
}

// UpsertReviewItem inserts or idempotently updates a review item keyed by its
// stable DedupKey. It is the producer entry point.
//
// Idempotency contract (the critical invariant — a re-scan must be a no-op for
// already-decided items):
//
//   - New DedupKey → create a fresh row (ULID assigned if item.ID is empty),
//     Status defaulted to "pending" when empty, CreatedAt = UpdatedAt = now.
//   - Existing DedupKey, current Status == "pending" → update Summary, Payload,
//     and UpdatedAt only. Kind/FolderRef/CreatedAt/Status are preserved (Kind and
//     FolderRef are what DedupKey hashes, so they cannot legitimately change).
//   - Existing DedupKey, current Status != "pending" (approved/rejected/applied)
//     → FULL no-op: the stored row (including UpdatedAt) is returned untouched, so
//     a re-scan never un-rejects a rejected hold or re-summarizes a decided one.
//
// The dedupkey → id index is written once on create and never deleted in A1, so
// "rejected is remembered": a rejected folder stays rejected across re-scans.
func (p *PebbleStore) UpsertReviewItem(item ReviewItem) (ReviewItem, error) {
	if item.DedupKey == "" {
		return ReviewItem{}, fmt.Errorf("review item: DedupKey is required for upsert")
	}
	if item.Kind == "" {
		return ReviewItem{}, fmt.Errorf("review item: Kind is required")
	}

	// Serialize upserts so two producers writing the same DedupKey concurrently
	// cannot both miss the index and create duplicate rows.
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	// Look up the existing row by DedupKey.
	idxVal, idxCloser, err := p.db.Get(reviewItemDedupIdxKey(item.DedupKey))
	if err != nil && err != pebble.ErrNotFound {
		return ReviewItem{}, fmt.Errorf("review item dedupkey lookup: %w", err)
	}
	now := time.Now().UTC()

	if err == pebble.ErrNotFound {
		// New row.
		id := item.ID
		if id == "" {
			id, err = newULID()
			if err != nil {
				return ReviewItem{}, fmt.Errorf("review item: generate id: %w", err)
			}
		}
		status := item.Status
		if status == "" {
			status = ReviewStatusPending
		}
		rec := ReviewItem{
			ID:        id,
			Kind:      item.Kind,
			DedupKey:  item.DedupKey,
			FolderRef: item.FolderRef,
			Status:    status,
			Summary:   item.Summary,
			Payload:   item.Payload,
			CreatedAt: now,
			UpdatedAt: now,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return ReviewItem{}, fmt.Errorf("marshal review item: %w", err)
		}
		b := p.db.NewBatch()
		defer b.Close()
		if err := b.Set(reviewItemRecKey(id), data, nil); err != nil {
			return ReviewItem{}, err
		}
		if err := b.Set(reviewItemDedupIdxKey(rec.DedupKey), []byte(id), nil); err != nil {
			return ReviewItem{}, err
		}
		if err := b.Set(reviewItemStatusKey(rec.Status, id), nil, nil); err != nil {
			return ReviewItem{}, err
		}
		if err := b.Commit(pebble.Sync); err != nil {
			return ReviewItem{}, err
		}
		return rec, nil
	}

	// Existing row.
	existingID := string(idxVal)
	idxCloser.Close()
	existing, err := p.getReviewItem(existingID)
	if err != nil {
		return ReviewItem{}, err
	}
	if existing == nil {
		// Dangling dedupkey index (record gone). Treat as a fresh insert reusing
		// the mapped ID so the index and record reconverge.
		status := item.Status
		if status == "" {
			status = ReviewStatusPending
		}
		rec := ReviewItem{
			ID:        existingID,
			Kind:      item.Kind,
			DedupKey:  item.DedupKey,
			FolderRef: item.FolderRef,
			Status:    status,
			Summary:   item.Summary,
			Payload:   item.Payload,
			CreatedAt: now,
			UpdatedAt: now,
		}
		data, err := json.Marshal(rec)
		if err != nil {
			return ReviewItem{}, fmt.Errorf("marshal review item: %w", err)
		}
		b := p.db.NewBatch()
		defer b.Close()
		if err := b.Set(reviewItemRecKey(existingID), data, nil); err != nil {
			return ReviewItem{}, err
		}
		if err := b.Set(reviewItemStatusKey(rec.Status, existingID), nil, nil); err != nil {
			return ReviewItem{}, err
		}
		if err := b.Commit(pebble.Sync); err != nil {
			return ReviewItem{}, err
		}
		return rec, nil
	}

	// A decided item is never touched by a re-scan — full no-op.
	if existing.Status != ReviewStatusPending {
		return *existing, nil
	}

	// Pending item: refresh only the mutable presentation fields.
	existing.Summary = item.Summary
	existing.Payload = item.Payload
	existing.UpdatedAt = now
	data, err := json.Marshal(existing)
	if err != nil {
		return ReviewItem{}, fmt.Errorf("marshal review item: %w", err)
	}
	// Status is unchanged (still pending) so the status index needs no update.
	if err := p.db.Set(reviewItemRecKey(existing.ID), data, pebble.Sync); err != nil {
		return ReviewItem{}, err
	}
	return *existing, nil
}

// GetReviewItem retrieves a single review item by ID. Returns (nil, nil) when
// not found.
func (p *PebbleStore) GetReviewItem(id string) (*ReviewItem, error) {
	return p.getReviewItem(id)
}

// ListReviewItems returns a paginated slice of review items matching the filter,
// plus the total count of matches (before pagination). Results are ordered
// newest-first (CreatedAt desc, ID desc tie-break) so the freshest holds surface
// at the top of the queue.
//
// When f.Status != "" the status secondary index drives the scan; otherwise a
// full record scan is used. The Kind filter is always applied in Go.
func (p *PebbleStore) ListReviewItems(f ReviewFilter) ([]ReviewItem, int, error) {
	var all []ReviewItem
	var err error
	if f.Status != "" {
		all, err = p.listReviewItemsByStatusIndex(f.Status)
	} else {
		all, err = p.listAllReviewItems()
	}
	if err != nil {
		return nil, 0, err
	}

	// Apply the Kind filter (status is already satisfied by the chosen scan).
	if f.Kind != "" {
		filtered := all[:0:0]
		for _, it := range all {
			if it.Kind == f.Kind {
				filtered = append(filtered, it)
			}
		}
		all = filtered
	}

	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})

	total := len(all)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	start := f.Offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

func (p *PebbleStore) listAllReviewItems() ([]ReviewItem, error) {
	prefix := []byte(reviewItemRecPfx)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, fmt.Errorf("list review items: %w", err)
	}
	defer iter.Close()

	var out []ReviewItem
	for iter.First(); iter.Valid(); iter.Next() {
		var item ReviewItem
		if err := json.Unmarshal(iter.Value(), &item); err != nil {
			continue
		}
		out = append(out, item)
	}
	return out, iter.Error()
}

func (p *PebbleStore) listReviewItemsByStatusIndex(status string) ([]ReviewItem, error) {
	prefix := []byte(reviewItemStatusPfx + status + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return nil, fmt.Errorf("list review items by status: %w", err)
	}
	defer iter.Close()

	pfxLen := len(prefix)
	var out []ReviewItem
	for iter.First(); iter.Valid(); iter.Next() {
		id := string(iter.Key()[pfxLen:])
		item, err := p.getReviewItem(id)
		if err != nil {
			return nil, err
		}
		if item == nil {
			continue // dangling index row — tolerated, never an error.
		}
		// Re-check status: a status change that raced this scan could leave a
		// stale index row pointing at a now-different record.
		if item.Status != status {
			continue
		}
		out = append(out, *item)
	}
	return out, iter.Error()
}

// CountReviewItems returns the number of review items with the given status.
// A blank status counts every review item. The status index makes the common
// pending-count an O(k) index scan with no record reads.
func (p *PebbleStore) CountReviewItems(status string) (int, error) {
	var prefix []byte
	if status == "" {
		prefix = []byte(reviewItemRecPfx)
	} else {
		prefix = []byte(reviewItemStatusPfx + status + ":")
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return 0, fmt.Errorf("count review items: %w", err)
	}
	defer iter.Close()

	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n, iter.Error()
}

// ReviewStatsByKind returns a per-(Kind, Status) breakdown of every review item,
// sorted by Kind then Status. Callers that want only the pending breakdown
// filter on Status == ReviewStatusPending.
func (p *PebbleStore) ReviewStatsByKind() ([]ReviewKindStat, error) {
	items, err := p.listAllReviewItems()
	if err != nil {
		return nil, err
	}
	type key struct{ kind, status string }
	counts := map[key]int{}
	for _, it := range items {
		counts[key{it.Kind, it.Status}]++
	}
	stats := make([]ReviewKindStat, 0, len(counts))
	for k, c := range counts {
		stats = append(stats, ReviewKindStat{Kind: k.kind, Status: k.status, Count: c})
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Kind != stats[j].Kind {
			return stats[i].Kind < stats[j].Kind
		}
		return stats[i].Status < stats[j].Status
	})
	return stats, nil
}

// SetReviewItemStatus transitions a review item to a new status and moves its
// status-index row accordingly. Returns (nil, nil) when the item does not exist
// so callers can respond 404. The dedupkey index is never touched (DedupKey is
// stable), so a rejected item stays remembered across future producer re-scans.
func (p *PebbleStore) SetReviewItemStatus(id, status string) (*ReviewItem, error) {
	if status == "" {
		return nil, fmt.Errorf("review item: status is required")
	}
	item, err := p.getReviewItem(id)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, nil
	}
	oldStatus := item.Status
	item.Status = status
	item.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("marshal review item: %w", err)
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(reviewItemRecKey(id), data, nil); err != nil {
		return nil, err
	}
	if oldStatus != "" && oldStatus != status {
		if err := b.Delete(reviewItemStatusKey(oldStatus, id), nil); err != nil {
			return nil, err
		}
	}
	if err := b.Set(reviewItemStatusKey(status, id), nil, nil); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return item, nil
}

// DeleteReviewItem removes a review item and ALL its index rows (record, status
// index, dedup index). Idempotent: deleting a missing id is a no-op returning nil.
//
// Deleting the dedup index means a future producer re-scan of that folder can create
// a FRESH hold — exactly what the regroup reconcile wants for a hold whose folder is
// no longer a candidate. Because this forgets a remembered decision, callers that
// must PRESERVE one (rejected-is-remembered) must only delete PENDING items; the
// regroup reconcile enforces that.
func (p *PebbleStore) DeleteReviewItem(id string) error {
	// Serialize against UpsertReviewItem, which reads the dedup index to decide
	// create-vs-update: deleting that index concurrently could let a duplicate slip
	// in. Same mutex, same reason as Upsert.
	p.reviewMu.Lock()
	defer p.reviewMu.Unlock()

	item, err := p.getReviewItem(id)
	if err != nil {
		return err
	}
	if item == nil {
		return nil // idempotent no-op
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Delete(reviewItemRecKey(id), nil); err != nil {
		return err
	}
	if item.Status != "" {
		if err := b.Delete(reviewItemStatusKey(item.Status, id), nil); err != nil {
			return err
		}
	}
	if item.DedupKey != "" {
		if err := b.Delete(reviewItemDedupIdxKey(item.DedupKey), nil); err != nil {
			return err
		}
	}
	return b.Commit(pebble.Sync)
}
