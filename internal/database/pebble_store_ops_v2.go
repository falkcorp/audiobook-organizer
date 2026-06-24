// file: internal/database/pebble_store_ops_v2.go
// version: 3.4.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-06-24

// pebble_store_ops_v2 implements OpsV2Store for PebbleDB (the primary production
// database). Key schema (all prefixed with "opv2:"):
//
//	opv2:def:{def_id}                               → JSON(OpDefinitionV2Row)
//	opv2:op:{op_id}                                 → JSON(OperationV2Row)
//	opv2:q:{999-priority:03d}:{ts_nano:020d}:{op_id} → op_id  (queue index)
//	opv2:act:{op_id}                                → ""      (active: queued|running)
//	opv2:state:{op_id}                              → JSON(OpStateV2Row)
//	opv2:log:{op_id}:{ts_nano:020d}:{seq:010d}      → JSON(OpLogV2Row)
//	opv2:err:{op_id}:{ts_nano:020d}                 → JSON(OpErrorV2Row)
//	opv2:strike:{def_id}:{ts_nano:020d}:{op_id}     → JSON(OpStrikeV2Row)

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// key builders

func opv2DefKey(defID string) []byte {
	return []byte("opv2:def:" + defID)
}

func opv2OpKey(opID string) []byte {
	return []byte("opv2:op:" + opID)
}

// opv2QueueKey encodes priority DESC (inverted), queued_at ASC into a
// lexicographically sortable key so a simple prefix scan returns ops in
// dispatch order without a secondary sort.
func opv2QueueKey(priority int, queuedAt time.Time, opID string) []byte {
	// 999-priority gives higher priorities a smaller prefix → sorts first.
	return []byte(fmt.Sprintf("opv2:q:%03d:%020d:%s", 999-priority, queuedAt.UnixNano(), opID))
}

func opv2ActKey(opID string) []byte {
	return []byte("opv2:act:" + opID)
}

func opv2StateKey(opID string) []byte {
	return []byte("opv2:state:" + opID)
}

func opv2LogKey(opID string, ts time.Time, seq int64) []byte {
	return []byte(fmt.Sprintf("opv2:log:%s:%020d:%010d", opID, ts.UnixNano(), seq))
}

func opv2ErrKey(opID string, ts time.Time) []byte {
	return []byte(fmt.Sprintf("opv2:err:%s:%020d", opID, ts.UnixNano()))
}

func opv2StrikeKey(defID string, ts time.Time, opID string) []byte {
	return []byte(fmt.Sprintf("opv2:strike:%s:%020d:%s", defID, ts.UnixNano(), opID))
}

// pebbleGet reads a single key and JSON-decodes into dst. Returns nil, nil if not found.
func (p *PebbleStore) pebbleGetJSON(key []byte, dst any) error {
	val, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	defer closer.Close()
	return json.Unmarshal(val, dst)
}

func (p *PebbleStore) pebbleSetJSON(key []byte, src any) error {
	data, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return p.db.Set(key, data, pebble.Sync)
}

// UpsertOpDefinitionV2 inserts or replaces a definition row.
func (p *PebbleStore) UpsertOpDefinitionV2(row OpDefinitionV2Row) error {
	return p.pebbleSetJSON(opv2DefKey(row.ID), &row)
}

// DeleteOrphanOpDefsV2 removes definition rows whose ID is not in keepIDs.
func (p *PebbleStore) DeleteOrphanOpDefsV2(keepIDs []string) error {
	keep := make(map[string]bool, len(keepIDs))
	for _, id := range keepIDs {
		keep[id] = true
	}

	prefix := []byte("opv2:def:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return err
	}
	defer iter.Close()

	var toDelete [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		k := iter.Key()
		defID := strings.TrimPrefix(string(k), "opv2:def:")
		if !keep[defID] {
			cp := make([]byte, len(k))
			copy(cp, k)
			toDelete = append(toDelete, cp)
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	for _, k := range toDelete {
		if err := p.db.Delete(k, pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}

// InsertOperationV2 inserts a new queued operation row and adds it to the queue index.
func (p *PebbleStore) InsertOperationV2(row OperationV2Row) error {
	if err := p.pebbleSetJSON(opv2OpKey(row.ID), &row); err != nil {
		return err
	}
	if row.Status == "queued" {
		if err := p.db.Set(opv2QueueKey(row.Priority, row.QueuedAt, row.ID), []byte(row.ID), pebble.Sync); err != nil {
			return err
		}
		if err := p.db.Set(opv2ActKey(row.ID), nil, pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}

// ListQueuedOperationsV2 returns queued ops ordered by priority DESC, queued_at ASC.
func (p *PebbleStore) ListQueuedOperationsV2() ([]OperationV2Row, error) {
	prefix := []byte("opv2:q:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var result []OperationV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		val := iter.Value()
		opID := string(val)
		if opID == "" {
			continue
		}
		var row OperationV2Row
		if err := p.pebbleGetJSON(opv2OpKey(opID), &row); err != nil {
			continue
		}
		if row.Status != "queued" {
			continue
		}
		result = append(result, row)
	}
	return result, iter.Error()
}

// GetOperationV2 returns a single operation by id.
func (p *PebbleStore) GetOperationV2(id string) (*OperationV2Row, error) {
	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return nil, err
	}
	if row.ID == "" {
		return nil, nil
	}
	return &row, nil
}

// UpdateOperationV2Status updates status and optional timestamps on an operation,
// maintaining the queue/active indexes as status transitions occur.
func (p *PebbleStore) UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	if row.ID == "" {
		return fmt.Errorf("opv2: operation not found: %s", id)
	}

	oldStatus := row.Status
	row.Status = status
	if startedAt != nil {
		row.StartedAt = startedAt
	}
	if completedAt != nil {
		row.CompletedAt = completedAt
	}
	if errMsg != nil {
		row.ErrorMessage = errMsg
	}

	if err := p.pebbleSetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}

	// Remove from queue index if it was queued.
	if oldStatus == "queued" {
		_ = p.db.Delete(opv2QueueKey(row.Priority, row.QueuedAt, id), pebble.Sync)
	}
	// Maintain active set.
	if status == "running" {
		_ = p.db.Set(opv2ActKey(id), nil, pebble.Sync)
	} else if status != "queued" {
		_ = p.db.Delete(opv2ActKey(id), pebble.Sync)
	}
	return nil
}

// SetOperationV2StatusIfQueued atomically transitions status only when current status is 'queued'.
// Returns true if the row was updated.
func (p *PebbleStore) SetOperationV2StatusIfQueued(id, newStatus string) (bool, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return false, err
	}
	if row.ID == "" || row.Status != "queued" {
		return false, nil
	}

	row.Status = newStatus
	if err := p.pebbleSetJSON(opv2OpKey(id), &row); err != nil {
		return false, err
	}
	_ = p.db.Delete(opv2QueueKey(row.Priority, row.QueuedAt, id), pebble.Sync)
	if newStatus != "running" {
		_ = p.db.Delete(opv2ActKey(id), pebble.Sync)
	}
	return true, nil
}

// ListActiveOperationsV2 returns ops with status 'queued' or 'running'.
func (p *PebbleStore) ListActiveOperationsV2() ([]OperationV2Row, error) {
	prefix := []byte("opv2:act:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var result []OperationV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		opID := strings.TrimPrefix(string(iter.Key()), "opv2:act:")
		var row OperationV2Row
		if err := p.pebbleGetJSON(opv2OpKey(opID), &row); err != nil || row.ID == "" {
			continue
		}
		result = append(result, row)
	}
	return result, iter.Error()
}

// CountRunningByPluginV2 returns the count of running ops for a plugin.
func (p *PebbleStore) CountRunningByPluginV2(plugin string) (int, error) {
	active, err := p.ListActiveOperationsV2()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range active {
		if r.Plugin == plugin && r.Status == "running" {
			n++
		}
	}
	return n, nil
}

// IncrementResumeCountV2 atomically increments resume_count for the given op.
func (p *PebbleStore) IncrementResumeCountV2(id string) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	row.ResumeCount++
	return p.pebbleSetJSON(opv2OpKey(id), &row)
}

// UpdateOpProgressV2 updates the progress fields and last_progress_at.
func (p *PebbleStore) UpdateOpProgressV2(id string, current, total int, message string) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	now := time.Now().UTC()
	row.ProgressCurrent = current
	row.ProgressTotal = total
	row.ProgressMessage = message
	row.LastProgressAt = &now
	return p.pebbleSetJSON(opv2OpKey(id), &row)
}

// UpdateOpPhaseV2 sets or clears current_phase on an operation.
func (p *PebbleStore) UpdateOpPhaseV2(id string, phase *string) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	row.CurrentPhase = phase
	return p.pebbleSetJSON(opv2OpKey(id), &row)
}

// UpdateOpCheckpointV2 sets last_checkpoint_at and updates high_water_progress.
func (p *PebbleStore) UpdateOpCheckpointV2(id string, newHWM int) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	now := time.Now().UTC()
	row.LastCheckpointAt = &now
	if newHWM > row.HighWaterProgress {
		row.HighWaterProgress = newHWM
	}
	return p.pebbleSetJSON(opv2OpKey(id), &row)
}

// UpsertOpStateV2 inserts or replaces the checkpoint state for an operation.
func (p *PebbleStore) UpsertOpStateV2(row OpStateV2Row) error {
	return p.pebbleSetJSON(opv2StateKey(row.OperationID), &row)
}

// GetOpStateV2 returns the state blob for an op, or nil if not found.
func (p *PebbleStore) GetOpStateV2(opID string) (*OpStateV2Row, error) {
	var row OpStateV2Row
	if err := p.pebbleGetJSON(opv2StateKey(opID), &row); err != nil {
		return nil, err
	}
	if row.OperationID == "" {
		return nil, nil
	}
	return &row, nil
}

// DeleteOpStateV2 removes the state blob for an op.
func (p *PebbleStore) DeleteOpStateV2(opID string) error {
	return p.db.Delete(opv2StateKey(opID), pebble.Sync)
}

// UpdateOperationV2Params replaces the params blob on an operation row.
// Used by resumeRestart to inject checkpoint state before re-dispatch.
func (p *PebbleStore) UpdateOperationV2Params(id string, params []byte) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	if row.ID == "" {
		return fmt.Errorf("opv2: operation not found: %s", id)
	}
	row.Params = string(params)
	return p.pebbleSetJSON(opv2OpKey(id), &row)
}

// AppendOpLogsV2 bulk-inserts log rows.
func (p *PebbleStore) AppendOpLogsV2(rows []OpLogV2Row) error {
	if len(rows) == 0 {
		return nil
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	for _, row := range rows {
		seq := atomic.AddInt64(&p.opsLogSeq, 1)
		key := opv2LogKey(row.OperationID, row.CreatedAt, seq)
		data, err := json.Marshal(&row)
		if err != nil {
			return err
		}
		if err := batch.Set(key, data, nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// InsertOpErrorV2 inserts a single error record.
func (p *PebbleStore) InsertOpErrorV2(row OpErrorV2Row) error {
	return p.pebbleSetJSON(opv2ErrKey(row.OperationID, row.OccurredAt), &row)
}

// InsertOpStrikeV2 appends a row to op_strikes_v2.
func (p *PebbleStore) InsertOpStrikeV2(row OpStrikeV2Row) error {
	return p.pebbleSetJSON(opv2StrikeKey(row.DefID, row.OccurredAt, row.OperationID), &row)
}

// ListOperationsV2Since returns operations queued at or after `since`, ordered
// by started_at DESC NULLS LAST, queued_at DESC, up to `limit` rows.
func (p *PebbleStore) ListOperationsV2Since(since time.Time, limit int) ([]OperationV2Row, error) {
	if limit <= 0 {
		limit = 200
	}
	prefix := []byte("opv2:op:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var all []OperationV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		var row OperationV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			continue
		}
		if !row.QueuedAt.Before(since) {
			all = append(all, row)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool {
		si, sj := all[i].StartedAt, all[j].StartedAt
		// NULLS LAST: nil StartedAt sorts after non-nil.
		if si == nil && sj == nil {
			return all[i].QueuedAt.After(all[j].QueuedAt)
		}
		if si == nil {
			return false
		}
		if sj == nil {
			return true
		}
		if !si.Equal(*sj) {
			return si.After(*sj)
		}
		return all[i].QueuedAt.After(all[j].QueuedAt)
	})

	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// GetOpLogsV2 returns up to `limit` log lines for the given operation, ordered by created_at ASC.
// A limit ≤ 0 returns all rows.
func (p *PebbleStore) GetOpLogsV2(opID string, limit int) ([]OpLogV2Row, error) {
	prefix := []byte("opv2:log:" + opID + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var result []OpLogV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		var row OpLogV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			continue
		}
		result = append(result, row)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	if limit > 0 && len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

// ── UOS dependency-scheduling (Task 2) ──────────────────────────────────────
//
// Keyspace:
//
//	op:deprev:<subjectType>:<subjectID>               → JSON {"rev":N}
//	op:completion:<subjectType>:<subjectID>:<opType>  → JSON {"rev":N} (book-level)
//	op:completion:<subjectType>:<subjectID>:<opType>:<fileID> → JSON {"rev":N} (file)

// depRevKey returns the PebbleDB key for the dep_rev counter of sub.
func depRevKey(sub OpSubject) []byte {
	return []byte("op:deprev:" + sub.Type + ":" + sub.ID)
}

// completionKey returns the PebbleDB key for a completion record.
// fileID == "" → book-level; fileID != "" → per-file.
func completionKey(sub OpSubject, opType, fileID string) []byte {
	base := "op:completion:" + sub.Type + ":" + sub.ID + ":" + opType
	if fileID != "" {
		return []byte(base + ":" + fileID)
	}
	return []byte(base)
}

// opDepRevValue is the JSON envelope stored at dep_rev and completion keys.
type opDepRevValue struct {
	Rev uint64 `json:"rev"`
}

// GetDepRev returns the current dep_rev counter for sub, or 0 if never bumped.
func (p *PebbleStore) GetDepRev(sub OpSubject) (uint64, error) {
	var v opDepRevValue
	if err := p.pebbleGetJSON(depRevKey(sub), &v); err != nil {
		return 0, err
	}
	return v.Rev, nil
}

// BumpDepRev atomically increments the dep_rev counter for sub and returns the
// new value.  Protected by opsMu to prevent concurrent read-increment-write races.
func (p *PebbleStore) BumpDepRev(sub OpSubject) (uint64, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var v opDepRevValue
	if err := p.pebbleGetJSON(depRevKey(sub), &v); err != nil {
		return 0, err
	}
	v.Rev++
	if err := p.pebbleSetJSON(depRevKey(sub), &v); err != nil {
		return 0, err
	}
	return v.Rev, nil
}

// RecordOpCompletion stores a completion record for opType on sub at depRev.
// fileID == "" → book-level completion; non-empty → per-file completion.
func (p *PebbleStore) RecordOpCompletion(sub OpSubject, opType, fileID string, depRev uint64) error {
	return p.pebbleSetJSON(completionKey(sub, opType, fileID), &opDepRevValue{Rev: depRev})
}

// GetOpCompletion retrieves the stored depRev for a book-level completion record.
// Returns (rev, true, nil) when found, (0, false, nil) when absent.
// Uses a direct key-existence check (not the zero-value sentinel from pebbleGetJSON)
// so that rev=0 completions (recorded before any BumpDepRev) are correctly found.
func (p *PebbleStore) GetOpCompletion(sub OpSubject, opType string) (uint64, bool, error) {
	val, closer, err := p.db.Get(completionKey(sub, opType, ""))
	if errors.Is(err, pebble.ErrNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer closer.Close()
	var v opDepRevValue
	if err := json.Unmarshal(val, &v); err != nil {
		return 0, false, err
	}
	return v.Rev, true, nil
}

// ListFileCompletions returns a map of fileID→depRev for all per-file completion
// records for opType on sub.
func (p *PebbleStore) ListFileCompletions(sub OpSubject, opType string) (map[string]uint64, error) {
	// Per-file keys have the form: op:completion:<type>:<id>:<opType>:<fileID>
	// The book-level key (no fileID suffix) must be excluded.
	bookLevelKey := string(completionKey(sub, opType, ""))
	prefix := []byte(bookLevelKey + ":")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	result := make(map[string]uint64)
	for iter.First(); iter.Valid(); iter.Next() {
		// Key format after the book-level prefix + ":" is the fileID.
		fileID := strings.TrimPrefix(string(iter.Key()), bookLevelKey+":")
		if fileID == "" {
			continue
		}
		var v opDepRevValue
		if err := json.Unmarshal(iter.Value(), &v); err != nil {
			continue
		}
		result[fileID] = v.Rev
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return result, nil
}

// ListWaitingDepsOps returns all OperationV2Row entries whose Status is
// "waiting_deps".  No status index exists, so this scans all opv2:op: rows.
func (p *PebbleStore) ListWaitingDepsOps() ([]OperationV2Row, error) {
	prefix := []byte("opv2:op:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var result []OperationV2Row
	for iter.First(); iter.Valid(); iter.Next() {
		var row OperationV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			continue
		}
		if row.Status == "waiting_deps" {
			result = append(result, row)
		}
	}
	return result, iter.Error()
}

// PromoteToQueued atomically transitions an operation from "waiting_deps" to
// "queued", writing both the row JSON and the opv2:q: queue-index key
// (same encoding as InsertOperationV2 for a queued op) so that
// ListQueuedOperationsV2 can discover the promoted op.
//
// The opv2:act: active-set key is intentionally NOT written here: that key is
// added when the dispatcher transitions the op to "running", matching the
// normal InsertOperationV2→UpdateOperationV2Status("running") lifecycle.
//
// Returns an error if the op does not exist or its current status is not
// "waiting_deps".
func (p *PebbleStore) PromoteToQueued(id string) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	if row.ID == "" {
		return fmt.Errorf("opv2: PromoteToQueued: operation not found: %s", id)
	}
	if row.Status != "waiting_deps" {
		return fmt.Errorf("opv2: PromoteToQueued: expected status %q, got %q for op %s",
			"waiting_deps", row.Status, id)
	}

	row.Status = "queued"
	if err := p.pebbleSetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}

	// Write the queue-index key so ListQueuedOperationsV2 can find this op.
	// Mirror the exact encoding used by InsertOperationV2.
	if err := p.db.Set(opv2QueueKey(row.Priority, row.QueuedAt, id), []byte(id), pebble.Sync); err != nil {
		return err
	}
	return nil
}

// ── M3 batch bucket (journaled pending subjects) ────────────────────────────
//
// Keyspace:
//
//	op:batch:<opType>:<subjectType>:<subjectID> → JSON(BatchBucketEntry)
//
// One key per (opType, subjectType, subjectID) triple. The value carries the
// wall-clock nanosecond timestamp of first addition so the registry can anchor
// BatchMaxWait correctly across restarts.

// batchBucketKey returns the PebbleDB key for a single bucket entry.
func batchBucketKey(opType string, sub OpSubject) []byte {
	return []byte("op:batch:" + opType + ":" + sub.Type + ":" + sub.ID)
}

// batchBucketPrefix returns the prefix for all entries in a given op-type bucket.
func batchBucketPrefix(opType string) []byte {
	return []byte("op:batch:" + opType + ":")
}

// AddToBatchBucket adds sub to the persistent pending bucket for opType.
// Idempotent: if an entry already exists the call is a no-op (preserving AddedAt).
func (p *PebbleStore) AddToBatchBucket(opType string, sub OpSubject) error {
	key := batchBucketKey(opType, sub)
	// Check for existing entry to preserve AddedAt.
	_, closer, err := p.db.Get(key)
	if err == nil {
		closer.Close()
		return nil // already present — idempotent
	}
	if !errors.Is(err, pebble.ErrNotFound) {
		return err
	}

	entry := BatchBucketEntry{
		Sub:     sub,
		AddedAt: time.Now().UnixNano(),
	}
	data, err := json.Marshal(&entry)
	if err != nil {
		return err
	}
	return p.db.Set(key, data, pebble.Sync)
}

// ListBatchBucket returns all pending subjects for opType.
// Returns an empty slice (not an error) when no bucket exists.
func (p *PebbleStore) ListBatchBucket(opType string) ([]BatchBucketEntry, error) {
	prefix := batchBucketPrefix(opType)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var result []BatchBucketEntry
	for iter.First(); iter.Valid(); iter.Next() {
		var entry BatchBucketEntry
		if err := json.Unmarshal(iter.Value(), &entry); err != nil {
			continue
		}
		result = append(result, entry)
	}
	return result, iter.Error()
}

// ClearBatchBucket removes the given subjects from the bucket for opType.
// Subjects not present in the bucket are silently skipped.
func (p *PebbleStore) ClearBatchBucket(opType string, subs []OpSubject) error {
	for _, sub := range subs {
		if err := p.db.Delete(batchBucketKey(opType, sub), pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}
