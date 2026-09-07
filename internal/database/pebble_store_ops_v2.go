// file: internal/database/pebble_store_ops_v2.go
// version: 3.15.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f
// last-edited: 2026-09-07

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
	"log/slog"
	"sort"
	"strings"
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
// Guarded by recoverPebbleClosed (see its doc): every opv2 method funneling
// through this helper returns pebble.ErrClosed as an error instead of panicking.
func (p *PebbleStore) pebbleGetJSON(key []byte, dst any) (err error) {
	defer recoverPebbleClosed("pebbleGetJSON", &err)
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

// pebbleSetJSON JSON-encodes src and writes it at key. Guarded like pebbleGetJSON.
func (p *PebbleStore) pebbleSetJSON(key []byte, src any) (err error) {
	defer recoverPebbleClosed("pebbleSetJSON", &err)
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
func (p *PebbleStore) DeleteOrphanOpDefsV2(keepIDs []string) (err error) {
	defer recoverPebbleClosed("DeleteOrphanOpDefsV2", &err)
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
func (p *PebbleStore) InsertOperationV2(row OperationV2Row) (err error) {
	defer recoverPebbleClosed("InsertOperationV2", &err)
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
func (p *PebbleStore) ListQueuedOperationsV2() (rows []OperationV2Row, err error) {
	// Guarded like ListWaitingDepsOps: this is the read the dispatcher's 100ms
	// ticker performs; on a leaked registry it hits the closed store first.
	defer recoverPebbleClosed("ListQueuedOperationsV2", &err)
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
//
// The row write and the queue/active index maintenance are committed as a
// single Pebble batch. They used to be separate Set/Delete calls: a
// concurrent ListActiveOperationsV2 scan (which does not take opsMu) could
// then observe the op's "opv2:act:" index entry still present *after* the
// row itself had already been rewritten with a terminal status — i.e. a
// caller could read back {status: "completed", still in the active index}.
// EnqueueOp's ConcurrencyKey dedup treats anything ListActiveOperationsV2
// returns as "still active" and hands the caller that op's ID instead of
// enqueuing a new one; for ops like itunes.import, whose Run bridges a
// caller-supplied legacy v1 op ID, that silently drops the new request's
// legacy row in "queued" forever, and integration tests polling it via
// require.Eventually time out with "Condition never satisfied" (root cause
// of the TestITunesImport_SkipDuplicates flake; the row-then-index write
// order made the window trivially easy to hit under load). Making the row
// update and the index maintenance atomic means readers never observe the
// index and the row disagreeing.
func (p *PebbleStore) UpdateOperationV2Status(id, status string, startedAt, completedAt *time.Time, errMsg *string) (err error) {
	defer recoverPebbleClosed("UpdateOperationV2Status", &err)
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

	data, err := json.Marshal(&row)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(opv2OpKey(id), data, nil); err != nil {
		return err
	}

	// Maintain queue index.
	if oldStatus == "queued" && status != "queued" {
		if err := batch.Delete(opv2QueueKey(row.Priority, row.QueuedAt, id), nil); err != nil {
			return err
		}
	} else if status == "queued" && oldStatus != "queued" {
		// resumeRestart transitions running→queued; must re-add the index entry
		// or ListQueuedOperationsV2 never sees the op and it stalls forever.
		if err := batch.Set(opv2QueueKey(row.Priority, row.QueuedAt, id), []byte(id), nil); err != nil {
			return err
		}
	}
	// Maintain active set.
	if status == "running" {
		if err := batch.Set(opv2ActKey(id), nil, nil); err != nil {
			return err
		}
	} else if status != "queued" {
		if err := batch.Delete(opv2ActKey(id), nil); err != nil {
			return err
		}
	}
	return batch.Commit(pebble.Sync)
}

// ResetOperationV2ForResume flips an interrupted op back to "queued" for a
// ResumeRestart-style resume, and — unlike UpdateOperationV2Status — CLEARS the
// CompletedAt timestamp (and the stale interrupt error) back to nil.
//
// This exists because UpdateOperationV2Status treats a nil timestamp as "leave
// unchanged", so it can never un-set CompletedAt. On interrupt the row got a
// CompletedAt stamped; on resume that stamp has to go, or the row stays excluded
// from the Active-Operations timeline (ListOperationsV2Since admits a row only if
// CompletedAt == nil || QueuedAt is inside the window), leaving a genuinely
// running resumed op invisible in the UI. QueuedAt is intentionally left
// untouched: it feeds restart-strike accounting (checkInfiniteRestart) and the
// queue index key, and clearing CompletedAt alone is sufficient for visibility.
//
// Index maintenance mirrors UpdateOperationV2Status's status=="queued" path: the
// queue index entry is (re-)added when the op was not already queued, and the
// active-set entry is left alone (a queued op is not active).
func (p *PebbleStore) ResetOperationV2ForResume(id string) (err error) {
	defer recoverPebbleClosed("ResetOperationV2ForResume", &err)
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
	row.Status = "queued"
	row.CompletedAt = nil
	row.ErrorMessage = nil

	data, err := json.Marshal(&row)
	if err != nil {
		return err
	}

	batch := p.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(opv2OpKey(id), data, nil); err != nil {
		return err
	}
	if oldStatus != "queued" {
		if err := batch.Set(opv2QueueKey(row.Priority, row.QueuedAt, id), []byte(id), nil); err != nil {
			return err
		}
	}
	// A resumed op is queued, not active: drop any stale active-set entry.
	if err := batch.Delete(opv2ActKey(id), nil); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// SetOperationV2Result stores an operation's final result payload on its v2 row.
//
// Read-modify-write under opsMu, mirroring UpdateOperationV2Status. Unlike that
// method this touches no secondary index — result data does not participate in the
// queue or active sets — so it is a plain Set rather than a batch.
//
// A missing row is an error, not a no-op. The v1 twin of this method
// (UpdateOperationResultData) behaves the same way, and roughly half its callers
// discard the error; do not repeat that here.
func (p *PebbleStore) SetOperationV2Result(id string, resultData string) (err error) {
	defer recoverPebbleClosed("SetOperationV2Result", &err)
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil {
		return err
	}
	if row.ID == "" {
		return fmt.Errorf("opv2: operation not found: %s", id)
	}

	row.ResultData = &resultData

	data, err := json.Marshal(&row)
	if err != nil {
		return err
	}
	return p.db.Set(opv2OpKey(id), data, pebble.Sync)
}

// SetOperationV2StatusIfQueued atomically transitions status only when current status is 'queued'.
// Returns true if the row was updated. Row write + index maintenance are
// committed as a single batch for the same reason described on
// UpdateOperationV2Status: separate writes let a concurrent
// ListActiveOperationsV2 scan observe a row whose status no longer matches
// its active-index membership.
func (p *PebbleStore) SetOperationV2StatusIfQueued(id, newStatus string) (updated bool, err error) {
	defer recoverPebbleClosed("SetOperationV2StatusIfQueued", &err)
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
	// Stamp CompletedAt whenever this transition takes the row out of the live
	// states. CompletedAt is the CANONICAL liveness signal in this store --
	// ListOperationsV2Since keeps a row forever on `CompletedAt == nil`, and the
	// timeline handler counts it as in-flight on the same test -- so a terminal
	// status written without a stamp produces a row that is dead to the worker
	// and alive to every reader. That is not hypothetical: a canceled
	// maintenance.transcribe-book-intros op sat in the UI's "Active Operations"
	// panel from 2026-06-26 to 2026-09-07 with completed_at null, and no user
	// action could clear it.
	//
	// This uses the explicit terminal ALLOWLIST rather than the complement of the
	// live states. An earlier revision of this function used the complement
	// (`!= "running" && != "queued"`) on the reasoning that ListOperationsV2Since
	// :631 avoids a status list, so this should too. That reasoning does not
	// transfer, because a reader and a writer have opposite safe defaults: a
	// reader that misses a terminal status under-reports deadness, and the row
	// lingers visibly until someone complains, whereas a writer that misses a
	// LIVE status stamps a row the resume machinery still owns -- silently, and
	// the result looks like completed work. The complement got that backwards:
	// "interrupted_quiesced" (resumable, see isResumableV2Status) and
	// "waiting_deps" are both live and both would have been stamped. Only the
	// fact that the two real call sites (registry.go:990, :999) pass a literal
	// "canceled" kept that from being reachable.
	if isTerminalV2Status(newStatus) && row.CompletedAt == nil {
		now := time.Now().UTC()
		row.CompletedAt = &now
	}
	data, err := json.Marshal(&row)
	if err != nil {
		return false, err
	}

	batch := p.db.NewBatch()
	defer batch.Close()
	if err := batch.Set(opv2OpKey(id), data, nil); err != nil {
		return false, err
	}
	if err := batch.Delete(opv2QueueKey(row.Priority, row.QueuedAt, id), nil); err != nil {
		return false, err
	}
	if newStatus != "running" {
		if err := batch.Delete(opv2ActKey(id), nil); err != nil {
			return false, err
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

// ListActiveOperationsV2 returns ops with status 'queued' or 'running'.
func (p *PebbleStore) ListActiveOperationsV2() (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("ListActiveOperationsV2", &err)
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
	if current > row.HighWaterProgress {
		// high_water_progress is the high-water mark of PROGRESS -- what its name
		// says, and how checkInfiniteRestart (registry/worker.go) reads it when
		// deciding whether a repeatedly-resumed op has accomplished anything.
		//
		// Until 2026-08-23 its only writer was UpdateOpCheckpointV2 below, so it
		// actually meant "progress as of the last Checkpoint call" and stayed
		// permanently 0 for every op that reports progress without checkpointing.
		// That is every maintenance job -- maintenance.ProgressReporter has no
		// Checkpoint method to call -- plus metadata.candidate-fetch. All of them
		// were therefore force-dropped at resume_count>=3 no matter how many
		// thousands of items they had actually completed.
		//
		// The row is already being written here, so this costs nothing.
		row.HighWaterProgress = current
	}
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
func (p *PebbleStore) DeleteOpStateV2(opID string) (err error) {
	defer recoverPebbleClosed("DeleteOpStateV2", &err)
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
func (p *PebbleStore) AppendOpLogsV2(rows []OpLogV2Row) (err error) {
	defer recoverPebbleClosed("AppendOpLogsV2", &err)
	if len(rows) == 0 {
		return nil
	}
	batch := p.db.NewBatch()
	defer batch.Close()
	for _, row := range rows {
		seq := p.opsLogSeq.Add(1)
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
func (p *PebbleStore) ListOperationsV2Since(since time.Time, limit int) (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("ListOperationsV2Since", &err)
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
		// The window bounds HISTORY, not live work. An operation that has not
		// completed is current by definition, however long ago it was queued —
		// filtering on QueuedAt alone meant an op simply had to RUN longer than
		// the window to disappear from its own timeline. A library.scan running
		// 1h50m returned an empty timeline in production on 2026-08-16 while it
		// was logging once a second, and an empty list reads as "nothing is
		// running."
		//
		// Keyed on CompletedAt rather than a set of status strings on purpose: a
		// status list has to be updated every time a new terminal state is added,
		// and silently under-reports until someone remembers.
		if row.CompletedAt == nil || !row.QueuedAt.Before(since) {
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
func (p *PebbleStore) GetOpLogsV2(opID string, limit int) (rows []OpLogV2Row, err error) {
	defer recoverPebbleClosed("GetOpLogsV2", &err)
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
func (p *PebbleStore) GetOpCompletion(sub OpSubject, opType string) (rev uint64, found bool, err error) {
	defer recoverPebbleClosed("GetOpCompletion", &err)
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
func (p *PebbleStore) ListFileCompletions(sub OpSubject, opType string) (res map[string]uint64, err error) {
	defer recoverPebbleClosed("ListFileCompletions", &err)
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

// recoverPebbleClosed is a deferred guard applied to EVERY opv2 read/write in
// this file (via the shared pebbleGetJSON/pebbleSetJSON helpers plus each
// method that touches p.db or a batch directly). The op registry drives these
// accesses from background goroutines — the DepsScheduler sweep ticker, the
// dispatcher's 100ms cycle, dbReporter progress/log flushes, worker status
// writes. If a registry is torn down without Shutdown (or an access races a
// Close in a way the registry-side drain cannot see), pebble PANICS ErrClosed
// from Get/Set/NewIter/Commit instead of returning an error, killing the whole
// process (PEBBLE-CLOSED-SWEEPTICK-RESIDUAL family; legs observed via
// ListWaitingDepsOps, ListQueuedOperationsV2, and UpdateOpProgressV2). Recover
// ONLY that sentinel (errors.Is(pebble.ErrClosed)) and surface it as an
// error — all registry callers already log-and-skip on error. Any other panic
// is re-raised so real bugs are not masked. Precedent: HNSWEmbeddingStore
// safeAdd (commit 5b90d2f6) containing library panics at the store boundary.
func recoverPebbleClosed(op string, errp *error) {
	if rec := recover(); rec != nil {
		recErr, ok := rec.(error)
		if !ok || !errors.Is(recErr, pebble.ErrClosed) {
			panic(rec)
		}
		slog.Warn("pebble: read on closed store; returning error instead of panicking (likely a registry torn down without Shutdown)",
			"op", op, "error", recErr)
		*errp = fmt.Errorf("%s: %w", op, recErr)
	}
}

// ListWaitingDepsOps returns all OperationV2Row entries whose Status is
// "waiting_deps".  No status index exists, so this scans all opv2:op: rows.
func (p *PebbleStore) ListWaitingDepsOps() (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("ListWaitingDepsOps", &err)
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

// isResumableV2Status reports whether a v2 operation row in this status should be
// handed to the registry's startup resume sweep (registry.resumeAfterStartup).
//
// WHY THIS EXISTS AT ALL — READ BEFORE "SIMPLIFYING" IT BACK TO THE ACTIVE INDEX.
// The sweep used to take its candidates from ListActiveOperationsV2, which reads
// the opv2:act: index. UpdateOperationV2Status DELETES that index key for any
// status that is not running/queued, so the instant a shutdown stamps
// "interrupted_quiesced" the row leaves the index and the sweep can never see it
// again. Boot then logs "no active ops to resume", which reads like "nothing was
// interrupted" rather than "I am structurally blind to interrupted rows".
//
// That is not hypothetical: a library.scan killed by a deploy on 2026-08-17 sat
// at interrupted_quiesced and never came back, and it happened AGAIN on
// 2026-08-24 (op 01M0RZPXBFCWVQQ1N2PGTE8C04, 25,880 files, stranded ~4h). The
// 2026-08-17 fix widened the *v1* predicate (isResumableOpStatus in
// pebble_store_operations.go) to match the "interrupted" prefix — but library.scan
// is v2-native and takes the registry path, which never consults that predicate.
// Widening a predicate cannot fix a sweep that does not read it.
//
// interrupted_dropped and interrupted_ask are deliberately EXCLUDED: both are
// decisions the sweep itself already made on a previous boot (ResumePolicy=drop,
// or "awaiting user decision"). Re-including them would relitigate a settled
// outcome on every restart, and interrupted_ask would resume without the user
// ever answering. Only interrupted_quiesced — the status minted for every policy
// that is NOT drop — is genuinely unfinished business.
func isResumableV2Status(status string) bool {
	return status == "queued" || status == "running" || status == "interrupted_quiesced"
}

// isTerminalV2Status reports whether a v2 status means the operation is finished
// for good: no worker holds it, no resume sweep will pick it up, and nothing is
// waiting on a user decision.
//
// This is an explicit allowlist, NOT the complement of the live states, and the
// direction is the point. Every caller is a WRITER that stamps completed_at, so
// the cost of being wrong is asymmetric:
//
//   - Miss a terminal status -> the repair stamps fewer rows than it could. The
//     leftover row stays visible in Active Operations and someone reports it.
//   - Miss a LIVE status -> the repair stamps a row that the scheduler, the
//     dependency waiter, or the startup resume sweep still owns. That is silent,
//     and the row then reads as finished work that never ran.
//
// So a status that is not listed here is treated as live. Adding a genuinely new
// terminal state means adding it here; forgetting to costs visibility, not work.
// (Contrast ListOperationsV2Since below, which is a READER and takes the opposite
// default on purpose -- see its comment.)
//
// The excluded-but-terminal-looking cases, verified against their write sites:
// "interrupted_quiesced" (registry.go:1263 via worker.go:264) is resumable;
// "interrupted_ask" (resume.go:383) is waiting on a user; "interrupted_restart"
// (server_lifecycle.go:121) is a resume marker. All three already pass a non-nil
// completedAt at their write site, so excluding them here costs nothing.
func isTerminalV2Status(status string) bool {
	switch status {
	case "completed", "failed", "canceled", "interrupted_dropped":
		return true
	default:
		return false
	}
}

// RepairOpsV2MissingCompletedAt stamps completed_at on every operation row that
// holds a terminal status but has completed_at null, and returns the number of
// rows actually written.
//
// Such a row is dead to the worker and alive to every reader -- CompletedAt is
// the canonical liveness signal in this store (ListOperationsV2Since:631) -- so
// it sits in the UI's Active Operations panel forever with no user action able
// to clear it. Before 2026-09-07 the only producer was
// SetOperationV2StatusIfQueued, which wrote a terminal status without a stamp;
// every other writer of a terminal status passes a non-nil completedAt. That
// bug is fixed at the source, and this is the repair for rows it already made.
//
// The scan reads the opv2:op: keyspace rather than the opv2:act: index because
// the affected rows are exactly the ones the index has already dropped
// (SetOperationV2StatusIfQueued deletes the act key on the same write that
// leaves completed_at null). There is no age cutoff and no row cap: the oldest
// known instance had been stuck for 73 days, which is precisely the row a
// "recent 500" query would miss.
func (p *PebbleStore) RepairOpsV2MissingCompletedAt() (repaired int, err error) {
	defer recoverPebbleClosed("RepairOpsV2MissingCompletedAt", &err)

	// Collect candidates WITHOUT opsMu held: this scans the whole op keyspace,
	// and holding the ops write lock across it would stall every in-flight
	// progress update. The predicate is re-checked under the lock below, so a
	// stale candidate is harmless.
	prefix := []byte("opv2:op:")
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return 0, err
	}
	var candidates []string
	for iter.First(); iter.Valid(); iter.Next() {
		var row OperationV2Row
		if err := json.Unmarshal(iter.Value(), &row); err != nil || row.ID == "" {
			continue
		}
		if isTerminalV2Status(row.Status) && row.CompletedAt == nil {
			candidates = append(candidates, row.ID)
		}
	}
	iterErr := iter.Error()
	if closeErr := iter.Close(); closeErr != nil && iterErr == nil {
		iterErr = closeErr
	}
	if iterErr != nil {
		return 0, iterErr
	}

	for _, id := range candidates {
		// Re-check the FULL predicate under the lock, not just the null test. A
		// row can go canceled -> resumed -> queued between the two passes, and
		// ResetOperationV2ForResume clears CompletedAt as part of that, so a bare
		// `CompletedAt == nil` check would pass and stamp a live queued row.
		if p.stampCompletedAtIfPhantom(id) {
			repaired++
		}
	}
	return repaired, nil
}

// stampCompletedAtIfPhantom stamps completed_at on one row if it still holds a
// terminal status with a null completed_at. Reports whether it wrote.
func (p *PebbleStore) stampCompletedAtIfPhantom(id string) bool {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()

	var row OperationV2Row
	if err := p.pebbleGetJSON(opv2OpKey(id), &row); err != nil || row.ID == "" {
		return false
	}
	if !isTerminalV2Status(row.Status) || row.CompletedAt != nil {
		return false
	}
	now := time.Now().UTC()
	row.CompletedAt = &now
	return p.pebbleSetJSON(opv2OpKey(id), &row) == nil
}

// ListResumableOperationsV2 returns the rows the startup resume sweep should
// consider: queued, running, and interrupted_quiesced.
//
// Unlike ListActiveOperationsV2 this scans the whole opv2:op: keyspace rather
// than the opv2:act: index, because the rows that most need resuming are exactly
// the ones the index has dropped. ListActiveOperationsV2 is deliberately left
// alone — four other callers (the scheduler's in-flight guard, the AI
// same-mode guard, the enqueue dedupe, and CountRunningByPluginV2) depend on it
// meaning strictly "queued or running", and a quiesced row from a week ago must
// not read as in-flight to any of them.
func (p *PebbleStore) ListResumableOperationsV2() (rows []OperationV2Row, err error) {
	defer recoverPebbleClosed("ListResumableOperationsV2", &err)
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
		// A row this scan cannot read is an operation that will never resume, so
		// it is reported rather than silently skipped. The sibling scans here
		// (ListActiveOperationsV2, ListWaitingDepsOps) drop a bad row in silence,
		// and that is defensible for them: a missing row reads as "not in flight"
		// or "not waiting", which is the safe direction. For THIS caller the safe
		// direction is the opposite one -- a skipped row is indistinguishable from
		// the blindness this method was added to cure, and would look identical in
		// the logs ("no resumable ops").
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			slog.Warn("pebble: ListResumableOperationsV2 skipping undecodable op row; "+
				"if this row was interrupted it will never be resumed",
				"key", string(iter.Key()), "error", err)
			continue
		}
		if row.ID == "" {
			slog.Warn("pebble: ListResumableOperationsV2 skipping op row with no id",
				"key", string(iter.Key()), "status", row.Status)
			continue
		}
		if isResumableV2Status(row.Status) {
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
func (p *PebbleStore) PromoteToQueued(id string) (err error) {
	defer recoverPebbleClosed("PromoteToQueued", &err)
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
func (p *PebbleStore) AddToBatchBucket(opType string, sub OpSubject) (err error) {
	defer recoverPebbleClosed("AddToBatchBucket", &err)
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
func (p *PebbleStore) ListBatchBucket(opType string) (entries []BatchBucketEntry, err error) {
	defer recoverPebbleClosed("ListBatchBucket", &err)
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
func (p *PebbleStore) ClearBatchBucket(opType string, subs []OpSubject) (err error) {
	defer recoverPebbleClosed("ClearBatchBucket", &err)
	for _, sub := range subs {
		if err := p.db.Delete(batchBucketKey(opType, sub), pebble.Sync); err != nil {
			return err
		}
	}
	return nil
}
