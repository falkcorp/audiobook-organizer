// file: internal/database/dedup_label.go
// version: 1.5.0
// guid: 5a0319bd-8bc4-4135-91e6-dfd43628dcc5
// last-edited: 2026-10-01

package database

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// dedupLabelPfx is the Pebble keyspace for labeled dedup examples.
// Key layout: dedup:label:<candidateID, 16-hex> → LabeledExample JSON.
const dedupLabelPfx = "dedup:label:"

// dedupLabelEntityPfx is the entity secondary index over labeled examples:
// dedup:lbe:<entityID>:<candidateID, 16-hex> → empty, one row per side
// (EntityAID and EntityBID). It lets a reader that only cares about a few
// books (the duplicate-copies and fragment fixers' Replan, under the global
// merge lock) read just those books' labels in O(k) instead of decoding the
// whole dedup:label: keyspace.
//
// It deliberately does NOT live under dedupLabelPfx ("dedup:label:e:..."):
// every whole-keyspace label scan (listLabeledExamples,
// DeleteLabeledExamplesBySource, the JSONL export) would then meet the empty
// index values and fail to decode them as examples.
//
// The index may hold stale rows (a label rewritten with other entity ids by a
// concurrent writer, a delete that could not read the old example): readers
// re-check the example itself and skip a row that no longer names the book.
// It may not MISS a row: every write stages its rows in the label's own batch,
// and migration 64 backfilled the labels written before it existed.
const dedupLabelEntityPfx = "dedup:lbe:"

func dedupLabelEntityKey(entityID string, candidateID int64) []byte {
	return []byte(fmt.Sprintf("%s%s:%016x", dedupLabelEntityPfx, entityID, uint64(candidateID)))
}

// stageLabelEntityIndex sets (del=false) or deletes (del=true) ex's two
// entity-index rows in b.
func stageLabelEntityIndex(b *pebble.Batch, ex *LabeledExample, del bool) error {
	for _, id := range []string{ex.EntityAID, ex.EntityBID} {
		if id == "" {
			continue
		}
		k := dedupLabelEntityKey(id, ex.CandidateID)
		var err error
		if del {
			err = b.Delete(k, nil)
		} else {
			err = b.Set(k, nil, nil)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// readLabeledExampleRaw reads candidateID's example; nil when absent or
// unreadable (the index maintenance that uses it is best-effort on the OLD
// value only).
func (s *EmbeddingStore) readLabeledExampleRaw(candidateID int64) *LabeledExample {
	val, closer, err := s.db.Get(dedupLabelKey(candidateID))
	if err != nil {
		return nil
	}
	defer func() { _ = closer.Close() }()
	var ex LabeledExample
	if json.Unmarshal(val, &ex) != nil {
		return nil
	}
	return &ex
}

// dedupLabelKey renders the fixed-width key for a candidate ID so that range
// scans over the prefix return rows in a stable order.
func dedupLabelKey(candidateID int64) []byte {
	return []byte(fmt.Sprintf("%s%016x", dedupLabelPfx, uint64(candidateID)))
}

// BookFeatures captures the per-book evidence a judge needs. Computed by the
// dataset feature builder, snapshotted at capture time.
type BookFeatures struct {
	Title               string   `json:"title"`
	Author              string   `json:"author"`
	PrimaryPath         string   `json:"primary_path"`
	TotalDurationSec    float64  `json:"total_duration_sec"`
	FileCount           int      `json:"file_count"`
	HasCover            bool     `json:"has_cover"`
	FilesExist          bool     `json:"files_exist"`
	RecordingIDs        []string `json:"recording_ids,omitempty"`
	ITunesPIDPresent    bool     `json:"itunes_pid_present"`
	WholeBookSigPresent bool     `json:"whole_book_sig_present"`
	// FileSizeBytes is the largest known file size for the book (max over its
	// BookFiles, falling back to the book-level size). Mirrors the engine's
	// hasPlausibleAudio signal so the dataset can tell a genuine but unscanned
	// copy (large file, zero duration) from a stub/placeholder (tiny file).
	FileSizeBytes int64 `json:"file_size_bytes"`
	// ASIN is the book's Amazon/Audible ID ("" when the book has none).
	ASIN string `json:"asin,omitempty"`
	// VersionGroupID links books that are versions of the same work ("" when ungrouped).
	VersionGroupID string `json:"version_group_id,omitempty"`
}

// LabeledExample is one labeled dedup candidate pair plus the features behind
// the label. Stored at dedup:label:<candidateID>.
type LabeledExample struct {
	CandidateID int64  `json:"candidate_id"`
	EntityAID   string `json:"entity_a_id"`
	EntityBID   string `json:"entity_b_id"`

	Layer string `json:"layer"`
	Band  string `json:"band,omitempty"`
	// Score is 0 when no ScoreBreakdown is available; populated from the candidate's UnifiedDedupScore.Score when present.
	Score          float64         `json:"score"`
	ScoreBreakdown json.RawMessage `json:"score_breakdown,omitempty"`
	Similarity     *float64        `json:"similarity,omitempty"`

	A BookFeatures `json:"a"`
	B BookFeatures `json:"b"`

	DurationRatio     float64 `json:"duration_ratio"`
	FolderRelation    string  `json:"folder_relation"` // unrelated|same_dir|a_ancestor_of_b|b_ancestor_of_a (sibling_parts: planned, not yet produced)
	SharesRecordingID bool    `json:"shares_recording_id"`
	SignatureRelation string  `json:"signature_relation"` // unknown|match|disjoint|a_contains_b|b_contains_a

	Label          string `json:"label"`        // true_dup|not_dup|unsure
	LabelSource    string `json:"label_source"` // rule|itunes_attr|human|llm_judge
	LabelReason    string `json:"label_reason"`
	DecidedAt      string `json:"decided_at,omitempty"` // RFC3339; caller-stamped
	FormulaVersion string `json:"formula_version,omitempty"`
}

// LabeledExampleFilter narrows ListLabeledExamples / CountLabeledExamples.
// Empty fields are ignored. Filtering is in-memory over the prefix scan, which
// is fine at dataset scale (tens of thousands of rows).
type LabeledExampleFilter struct {
	Label             string
	LabelSource       string
	Band              string
	FolderRelation    string
	SignatureRelation string
	Limit             int
	Offset            int
}

func (f LabeledExampleFilter) matches(ex *LabeledExample) bool {
	if f.Label != "" && ex.Label != f.Label {
		return false
	}
	if f.LabelSource != "" && ex.LabelSource != f.LabelSource {
		return false
	}
	if f.Band != "" && ex.Band != f.Band {
		return false
	}
	if f.FolderRelation != "" && ex.FolderRelation != f.FolderRelation {
		return false
	}
	if f.SignatureRelation != "" && ex.SignatureRelation != f.SignatureRelation {
		return false
	}
	return true
}

// UpsertLabeledExample writes (or overwrites) a labeled example and its
// entity-index rows (dedupLabelEntityPfx) in one batch. When an overwritten
// example named other entities, their rows are dropped in the same batch.
// No lock is taken (dataset backfill writes labels at volume, and a lock held
// across a Sync commit would serialize it — the #19 shape): two racing
// overwrites can at worst leave a stale index row, which readers skip.
func (s *EmbeddingStore) UpsertLabeledExample(ex LabeledExample) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	data, err := json.Marshal(ex)
	if err != nil {
		return fmt.Errorf("marshal labeled example %d: %w", ex.CandidateID, err)
	}
	b := s.db.NewBatch()
	defer func() { _ = b.Close() }()
	if old := s.readLabeledExampleRaw(ex.CandidateID); old != nil &&
		(old.EntityAID != ex.EntityAID || old.EntityBID != ex.EntityBID) {
		if err := stageLabelEntityIndex(b, old, true); err != nil {
			return err
		}
	}
	if err := b.Set(dedupLabelKey(ex.CandidateID), data, nil); err != nil {
		return err
	}
	if err := stageLabelEntityIndex(b, &ex, false); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// GetLabeledExample returns the example for a candidate, or nil if absent.
func (s *EmbeddingStore) GetLabeledExample(candidateID int64) (*LabeledExample, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	val, closer, err := s.db.Get(dedupLabelKey(candidateID))
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get labeled example %d: %w", candidateID, err)
	}
	defer func() { _ = closer.Close() }()
	var ex LabeledExample
	if err := json.Unmarshal(val, &ex); err != nil {
		return nil, fmt.Errorf("unmarshal labeled example %d: %w", candidateID, err)
	}
	return &ex, nil
}

// ListLabeledExamples returns examples matching the filter (prefix scan). A
// corrupt row is skipped; a caller that must honour every verdict uses
// ListLabeledExamplesStrict.
func (s *EmbeddingStore) ListLabeledExamples(f LabeledExampleFilter) ([]LabeledExample, error) {
	return s.listLabeledExamples(f, false)
}

// ListLabeledExamplesStrict is ListLabeledExamples failing on a corrupt row
// instead of skipping it: a reader that vetoes a write on a label (the
// duplicate-copies fixer's not_dup check) cannot tell a skipped not_dup from
// no label, so it must not plan past one.
func (s *EmbeddingStore) ListLabeledExamplesStrict(f LabeledExampleFilter) ([]LabeledExample, error) {
	return s.listLabeledExamples(f, true)
}

func (s *EmbeddingStore) listLabeledExamples(f LabeledExampleFilter, strict bool) ([]LabeledExample, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	prefix := []byte(dedupLabelPfx)
	upper := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return nil, fmt.Errorf("list labeled examples: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var out []LabeledExample
	skipped := 0
	for iter.First(); iter.Valid(); iter.Next() {
		var ex LabeledExample
		if err := json.Unmarshal(iter.Value(), &ex); err != nil {
			if strict {
				return nil, fmt.Errorf("labeled example %q is unreadable: %w", iter.Key(), err)
			}
			continue // skip a corrupt row rather than abort the scan
		}
		if !f.matches(&ex) {
			continue
		}
		if f.Offset > 0 && skipped < f.Offset {
			skipped++
			continue
		}
		out = append(out, ex)
		if f.Limit > 0 && len(out) >= f.Limit {
			break
		}
	}
	return out, iter.Error()
}

// DeleteLabeledExample removes a single labeled example by candidate ID.
// A no-op (no error) if the key does not exist.
func (s *EmbeddingStore) DeleteLabeledExample(candidateID int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	b := s.db.NewBatch()
	defer func() { _ = b.Close() }()
	if old := s.readLabeledExampleRaw(candidateID); old != nil {
		if err := stageLabelEntityIndex(b, old, true); err != nil {
			return err
		}
	}
	if err := b.Delete(dedupLabelKey(candidateID), nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// DeleteLabeledExamplesBySource deletes every labeled example whose
// LabelSource matches one of the given sources, returning the count deleted.
// Used by dedup.rebuild-gold-labels to wipe mechanically-derived labels
// ("rule", "auto_high_conf") before reinserting a freshly computed set, while
// leaving "human"-sourced (and any other) rows untouched.
func (s *EmbeddingStore) DeleteLabeledExamplesBySource(sources ...string) (int, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	want := make(map[string]struct{}, len(sources))
	for _, src := range sources {
		want[src] = struct{}{}
	}

	prefix := []byte(dedupLabelPfx)
	upper := prefixUpperBound(prefix)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: upper})
	if err != nil {
		return 0, fmt.Errorf("delete labeled examples by source: %w", err)
	}

	var keys [][]byte
	var exs []LabeledExample
	for iter.First(); iter.Valid(); iter.Next() {
		var ex LabeledExample
		if err := json.Unmarshal(iter.Value(), &ex); err != nil {
			continue // skip a corrupt row rather than abort the scan
		}
		if _, ok := want[ex.LabelSource]; !ok {
			continue
		}
		keys = append(keys, append([]byte(nil), iter.Key()...))
		exs = append(exs, ex)
	}
	if err := iter.Error(); err != nil {
		_ = iter.Close()
		return 0, err
	}
	if err := iter.Close(); err != nil {
		return 0, err
	}

	for i, k := range keys {
		b := s.db.NewBatch()
		err := stageLabelEntityIndex(b, &exs[i], true)
		if err == nil {
			err = b.Delete(k, nil)
		}
		if err == nil {
			err = b.Commit(pebble.Sync)
		}
		_ = b.Close()
		if err != nil {
			return i, fmt.Errorf("delete labeled example key %q: %w", k, err)
		}
	}
	return len(keys), nil
}

// CountLabeledExamples counts examples matching the filter (Limit/Offset ignored).
func (s *EmbeddingStore) CountLabeledExamples(f LabeledExampleFilter) (int, error) {
	if err := s.checkClosed(); err != nil {
		return 0, err
	}
	cf := f
	cf.Limit, cf.Offset = 0, 0
	list, err := s.ListLabeledExamples(cf)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// ListLabeledExamplesForEntitiesStrict returns every labeled example that
// names any of ids on either side and matches f (Limit/Offset ignored), read
// through the entity index (dedupLabelEntityPfx) in O(k) per id rather than
// the whole dedup:label: keyspace. Strict on what it reaches: an index key it
// cannot parse or an example it cannot decode fails the read, since either
// may be a verdict on one of these books. An index row whose example is gone,
// or no longer names the book, is stale and skipped; a corrupt example of an
// unrelated book is never read at all. Each example is returned once.
func (s *EmbeddingStore) ListLabeledExamplesForEntitiesStrict(ids []string, f LabeledExampleFilter) ([]LabeledExample, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	f.Limit, f.Offset = 0, 0
	seen := map[int64]bool{}
	var out []LabeledExample
	for _, id := range ids {
		if id == "" {
			continue
		}
		prefix := []byte(dedupLabelEntityPfx + id + ":")
		iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
		if err != nil {
			return nil, fmt.Errorf("label entity index scan %s: %w", id, err)
		}
		var cands []int64
		for iter.First(); iter.Valid(); iter.Next() {
			hexID := string(iter.Key()[len(prefix):])
			n, perr := strconv.ParseUint(hexID, 16, 64)
			if perr != nil {
				_ = iter.Close()
				return nil, fmt.Errorf("label entity index key %q is unreadable: %w", iter.Key(), perr)
			}
			cands = append(cands, int64(n))
		}
		ierr := iter.Error()
		_ = iter.Close()
		if ierr != nil {
			return nil, fmt.Errorf("label entity index scan %s: %w", id, ierr)
		}
		for _, cid := range cands {
			if seen[cid] {
				continue
			}
			val, closer, err := s.db.Get(dedupLabelKey(cid))
			if err == pebble.ErrNotFound {
				continue // stale index row
			}
			if err != nil {
				return nil, fmt.Errorf("get labeled example %d: %w", cid, err)
			}
			var ex LabeledExample
			uerr := json.Unmarshal(val, &ex)
			_ = closer.Close()
			if uerr != nil {
				return nil, fmt.Errorf("labeled example %d (indexed under %s) is unreadable: %w", cid, id, uerr)
			}
			if ex.EntityAID != id && ex.EntityBID != id {
				continue // stale index row: the example names other books now
			}
			seen[cid] = true
			if f.matches(&ex) {
				out = append(out, ex)
			}
		}
	}
	return out, nil
}

// LabelIndexBackfill reports BackfillLabelEntityIndex's pass.
type LabelIndexBackfill struct {
	Indexed, Unreadable int
}

// BackfillLabelEntityIndex writes the entity-index rows of every labeled
// example (dedupLabelEntityPfx), for the labels stored before the index
// existed. Idempotent. An example it cannot decode cannot be indexed and is
// counted as Unreadable: the whole-keyspace strict read (a fixer's Plan)
// still fails on it, so it is never silently dropped from a plan. Writes go
// out in NoSync batches of labelBackfillBatch rows with a final Sync, so a
// boot-time run over the whole keyspace does not fsync per row.
func (s *EmbeddingStore) BackfillLabelEntityIndex() (LabelIndexBackfill, error) {
	var res LabelIndexBackfill
	if err := s.checkClosed(); err != nil {
		return res, err
	}
	prefix := []byte(dedupLabelPfx)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixUpperBound(prefix)})
	if err != nil {
		return res, fmt.Errorf("backfill label entity index scan: %w", err)
	}
	defer func() { _ = iter.Close() }()
	b := s.db.NewBatch()
	pending, batches := 0, 0
	for iter.First(); iter.Valid(); iter.Next() {
		var ex LabeledExample
		if json.Unmarshal(iter.Value(), &ex) != nil {
			res.Unreadable++
			continue
		}
		if err := stageLabelEntityIndex(b, &ex, false); err != nil {
			_ = b.Close()
			return res, err
		}
		res.Indexed++
		if pending++; pending >= labelBackfillBatch {
			if err := b.Commit(pebble.NoSync); err != nil {
				_ = b.Close()
				return res, fmt.Errorf("backfill label entity index write: %w", err)
			}
			_ = b.Close()
			b, pending = s.db.NewBatch(), 0
			batches++
			logDedupBackfillProgress("labels", batches, res.Indexed, res.Unreadable)
		}
	}
	if err := iter.Error(); err != nil {
		_ = b.Close()
		return res, fmt.Errorf("backfill label entity index: %w", err)
	}
	err = b.Commit(pebble.Sync)
	_ = b.Close()
	if err != nil {
		return res, fmt.Errorf("backfill label entity index write: %w", err)
	}
	return res, nil
}

// labelBackfillBatch is how many rows a backfill commits per batch.
const labelBackfillBatch = 1000

// dedupBackfillLog is where migration 64 (the dedup entity-index backfills)
// reports. A var so a test can capture it.
var dedupBackfillLog logger.Logger = logger.New("database.migration-064")

// dedupBackfillProgressEvery is how many committed batches pass between two
// progress lines: 50 batches is 50,000 rows, a few seconds of a boot that
// would otherwise be silent for tens of seconds. A var so a test can lower it.
var dedupBackfillProgressEvery = 50

// logDedupBackfillProgress logs a backfill's running totals every
// dedupBackfillProgressEvery committed batches.
func logDedupBackfillProgress(what string, batches, indexed, unreadable int) {
	if dedupBackfillProgressEvery <= 0 || batches%dedupBackfillProgressEvery != 0 {
		return
	}
	dedupBackfillLog.Info("migration 64: %s backfill in progress: %d indexed, %d unreadable so far", what, indexed, unreadable)
}
