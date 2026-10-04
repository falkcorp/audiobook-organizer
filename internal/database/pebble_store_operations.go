// file: internal/database/pebble_store_operations.go
// version: 1.8.1
// guid: e4277998-6d7e-4f2a-9b5c-0a620a98105e
// last-edited: 2026-10-03

package database

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/oklog/ulid/v2"
)

func (p *PebbleStore) CreateOperation(id, opType string, folderPath *string) (*Operation, error) {
	op := &Operation{
		ID:         id,
		Type:       opType,
		Status:     "pending",
		Progress:   0,
		Total:      0,
		Message:    "",
		FolderPath: folderPath,
		CreatedAt:  time.Now(),
	}

	data, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}

	key := []byte(fmt.Sprintf("operation:%s", id))
	if err := p.db.Set(key, data, pebble.Sync); err != nil {
		return nil, err
	}

	return op, nil
}

func (p *PebbleStore) GetOperationByID(id string) (*Operation, error) {
	key := []byte(fmt.Sprintf("operation:%s", id))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var op Operation
	if err := json.Unmarshal(value, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (p *PebbleStore) GetRecentOperations(limit int) ([]Operation, error) {
	var operations []Operation
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		operations = append(operations, op)
	}

	sort.Slice(operations, func(i, j int) bool {
		return operations[i].CreatedAt.After(operations[j].CreatedAt)
	})

	if len(operations) > limit {
		operations = operations[:limit]
	}

	return operations, nil
}

// ListOperations returns one page of operations, newest first, along with the
// total number of operations regardless of the page.
//
// A limit <= 0 means "no limit": every operation from offset onwards is
// returned. That matches SearchBooks, which documents the same sentinel, and it
// exists because this method reads the ENTIRE "operation:" prefix into memory
// and sorts all of it before slicing out a page. Paging over a method with that
// shape costs a full scan per page, so a caller that wants everything must be
// able to say so in one call rather than walking offsets. Before the sentinel
// existed, limit == 0 computed end == offset and returned an empty page, which
// was a trap for exactly the caller that needed all rows.
func (p *PebbleStore) ListOperations(limit, offset int) ([]Operation, int, error) {
	var operations []Operation
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		operations = append(operations, op)
	}

	sort.Slice(operations, func(i, j int) bool {
		return operations[i].CreatedAt.After(operations[j].CreatedAt)
	})

	total := len(operations)
	if offset >= len(operations) {
		return []Operation{}, total, nil
	}
	end := len(operations)
	if limit > 0 {
		end = min(offset+limit, len(operations))
	}
	return operations[offset:end], total, nil
}

func (p *PebbleStore) UpdateOperationStatus(id, status string, progress, total int, message string) error {
	op, err := p.GetOperationByID(id)
	if err != nil {
		return err
	}
	if op == nil {
		return fmt.Errorf("operation not found")
	}

	op.Status = status
	op.Progress = progress
	op.Total = total
	op.Message = message

	now := time.Now()
	if status == "running" && op.StartedAt == nil {
		op.StartedAt = &now
	} else if (status == "completed" || status == "failed") && op.CompletedAt == nil {
		op.CompletedAt = &now
	}

	data, err := json.Marshal(op)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("operation:%s", id))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) UpdateOperationError(id, errorMessage string) error {
	op, err := p.GetOperationByID(id)
	if err != nil {
		return err
	}
	if op == nil {
		return fmt.Errorf("operation not found")
	}

	op.Status = "failed"
	op.ErrorMessage = &errorMessage
	now := time.Now()
	op.CompletedAt = &now

	data, err := json.Marshal(op)
	if err != nil {
		return err
	}

	key := []byte(fmt.Sprintf("operation:%s", id))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) UpdateOperationResultData(id string, resultData string) error {
	op, err := p.GetOperationByID(id)
	if err != nil {
		return err
	}
	if op == nil {
		return fmt.Errorf("operation not found: %s", id)
	}
	op.ResultData = &resultData
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(fmt.Sprintf("operation:%s", id)), data, pebble.Sync)
}

func (p *PebbleStore) AddOperationLog(operationID, level, message string, details *string) error {
	id, err := p.nextID("operationlog")
	if err != nil {
		return err
	}

	log := &OperationLog{
		ID:          id,
		OperationID: operationID,
		Level:       level,
		Message:     message,
		Details:     details,
		CreatedAt:   time.Now(),
	}

	data, err := json.Marshal(log)
	if err != nil {
		return err
	}

	// Key format: operationlog:<operation_id>:<timestamp>:<seq>
	key := []byte(fmt.Sprintf("operationlog:%s:%d:%d", operationID, log.CreatedAt.UnixNano(), id))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetOperationLogs(operationID string) ([]OperationLog, error) {
	var logs []OperationLog
	prefix := []byte(fmt.Sprintf("operationlog:%s:", operationID))

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var log OperationLog
		if err := json.Unmarshal(iter.Value(), &log); err != nil {
			continue
		}
		logs = append(logs, log)
	}

	return logs, nil
}

func (p *PebbleStore) SaveOperationSummaryLog(op *OperationSummaryLog) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("opsummary:%s", op.ID))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetOperationSummaryLog(id string) (*OperationSummaryLog, error) {
	key := []byte(fmt.Sprintf("opsummary:%s", id))
	val, closer, err := p.db.Get(key)
	if err != nil {
		if err == pebble.ErrNotFound {
			return nil, nil
		}
		return nil, err
	}
	defer closer.Close()
	var op OperationSummaryLog
	if err := json.Unmarshal(val, &op); err != nil {
		return nil, err
	}
	return &op, nil
}

func (p *PebbleStore) ListOperationSummaryLogs(limit, offset int) ([]OperationSummaryLog, error) {
	var logs []OperationSummaryLog
	prefix := []byte("opsummary:")

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: append(prefix, 0xFF),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op OperationSummaryLog
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		logs = append(logs, op)
	}

	// Sort by created_at descending.
	sort.SliceStable(logs, func(a, b int) bool {
		return logs[a].CreatedAt.After(logs[b].CreatedAt)
	})

	// Apply offset and limit
	if offset >= len(logs) {
		return nil, nil
	}
	logs = logs[offset:]
	if limit > 0 && len(logs) > limit {
		logs = logs[:limit]
	}

	return logs, nil
}

func (p *PebbleStore) SaveOperationState(opID string, state []byte) error {
	key := []byte(fmt.Sprintf("opstate:%s", opID))
	return p.db.Set(key, state, pebble.Sync)
}

func (p *PebbleStore) GetOperationState(opID string) ([]byte, error) {
	key := []byte(fmt.Sprintf("opstate:%s", opID))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

func (p *PebbleStore) SaveOperationParams(opID string, params []byte) error {
	key := []byte(fmt.Sprintf("opstate:%s:params", opID))
	return p.db.Set(key, params, pebble.Sync)
}

func (p *PebbleStore) GetOperationParams(opID string) ([]byte, error) {
	key := []byte(fmt.Sprintf("opstate:%s:params", opID))
	value, closer, err := p.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), nil
}

func (p *PebbleStore) DeleteOperationState(opID string) error {
	batch := p.db.NewBatch()
	if err := batch.Delete([]byte(fmt.Sprintf("opstate:%s", opID)), nil); err != nil {
		batch.Close()
		return err
	}
	if err := batch.Delete([]byte(fmt.Sprintf("opstate:%s:params", opID)), nil); err != nil {
		batch.Close()
		return err
	}
	return batch.Commit(pebble.Sync)
}

// DeleteOperationWithLogs removes the operation record (operation:<id>) plus all
// associated log entries (operationlog:<id>:*) in a single atomic Pebble batch.
//
// Why atomic: orphaning log lines under a deleted operation wastes disk space and
// confuses diagnostics. Grouping both deletions into one batch ensures they succeed
// or fail together with no partially-deleted state visible to readers.
func (p *PebbleStore) DeleteOperationWithLogs(id string) error {
	batch := p.db.NewBatch()
	defer batch.Close()

	// Delete the operation record itself.
	opKey := []byte(fmt.Sprintf("operation:%s", id))
	if err := batch.Delete(opKey, nil); err != nil {
		return fmt.Errorf("batch delete operation key: %w", err)
	}

	// Delete all associated log lines via prefix range iteration.
	// Key format: operationlog:<operation_id>:<timestamp_nano>:<seq>
	logPrefix := []byte(fmt.Sprintf("operationlog:%s:", id))
	logUpper := prefixEnd(logPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: logPrefix,
		UpperBound: logUpper,
	})
	if err != nil {
		return fmt.Errorf("open log iterator: %w", err)
	}
	for iter.First(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		if err := batch.Delete(k, nil); err != nil {
			iter.Close()
			return fmt.Errorf("batch delete log key: %w", err)
		}
	}
	if iterErr := iter.Error(); iterErr != nil {
		iter.Close()
		return fmt.Errorf("log iterator: %w", iterErr)
	}
	iter.Close()

	return batch.Commit(pebble.Sync)
}

// isResumableOpStatus reports whether an operation row in this status should be
// handed to the startup resume sweep (server_lifecycle.go
// resumeInterruptedOperations).
//
// MATCH THE PREFIX, NOT A LIST. This used to be an inline
// running/queued/interrupted comparison, while the registry mints a whole family
// of interrupted_* variants: interrupted_quiesced (registry.go, returned for
// EVERY ResumePolicy except ResumeDrop), interrupted_dropped,
// interrupted_restart, interrupted_ask. None of those matched, so the sweep was
// blind to exactly the rows it exists to resume - a library.scan killed by a
// deploy on 2026-08-17 sat at interrupted_quiesced and never came back.
//
// legacyStatusFor carries this same warning for this same reason. Matching the
// leading "interrupted" means a seventh variant works without anyone
// remembering this function exists.
func isResumableOpStatus(status string) bool {
	return status == "running" || status == "queued" ||
		strings.HasPrefix(status, "interrupted")
}

func (p *PebbleStore) GetInterruptedOperations() ([]Operation, error) {
	var ops []Operation
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		if isResumableOpStatus(op.Status) {
			ops = append(ops, op)
		}
	}
	return ops, nil
}

func (p *PebbleStore) CreateOperationResult(result *OperationResult) error {
	result.CreatedAt = time.Now()
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf("op_result:%s:%s", result.OperationID, result.BookID))
	return p.db.Set(key, data, pebble.Sync)
}

func (p *PebbleStore) GetOperationResults(operationID string) ([]OperationResult, error) {
	prefix := []byte(fmt.Sprintf("op_result:%s:", operationID))
	upperBound := make([]byte, len(prefix))
	copy(upperBound, prefix)
	upperBound[len(upperBound)-1]++
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []OperationResult
	for iter.First(); iter.Valid(); iter.Next() {
		var r OperationResult
		if err := json.Unmarshal(iter.Value(), &r); err != nil {
			continue
		}
		results = append(results, r)
	}
	return results, nil
}

// GetOperationResultsPage returns a page of results and the total count.
// PebbleDB has no SQL so we load all keys (key-only scan for count) then
// read only the needed slice. For typical operation sizes this is fast;
// very large operations (5 000+) still benefit because the caller no
// longer marshals and transmits the entire payload to the client.
func (p *PebbleStore) GetOperationResultsPage(operationID string, limit, offset int) ([]OperationResult, int, error) {
	all, err := p.GetOperationResults(operationID)
	if err != nil {
		return nil, 0, err
	}
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	end := total
	if limit > 0 && offset+limit < total {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

func (p *PebbleStore) GetRecentCompletedOperations(limit int) ([]Operation, error) {
	// Scan all operations, collect completed/failed, sort by time, take limit
	prefix := []byte("operation:")
	upperBound := make([]byte, len(prefix))
	copy(upperBound, prefix)
	upperBound[len(upperBound)-1]++
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var ops []Operation
	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		if op.Status == "completed" || op.Status == "failed" {
			ops = append(ops, op)
		}
	}

	// Sort by CreatedAt descending
	sort.Slice(ops, func(i, j int) bool {
		return ops[i].CreatedAt.After(ops[j].CreatedAt)
	})

	if len(ops) > limit {
		ops = ops[:limit]
	}
	return ops, nil
}

// CreateOperationChange stores an operation change in PebbleDB, together with
// its opchange_by_book: index entry in the same batch
// (pebble_store_opchange_index.go), committed with pebble.Sync. When the
// caller supplies an id, the call may rewrite an existing row: if that row
// named a different book, its old entry is deleted in the same batch, so the
// entry moves with the BookID. The new entry is Set unconditionally, so a
// rewrite self-heals a missing one. Two concurrent rewrites of one id can at
// worst leave an extra entry, which the reader drops; neither can leave the
// stored row without its entry.
//
// The read of the stored row through the commit runs under the read side of
// opChangeJournalMu, so a concurrent PruneOperationChanges chunk (write side)
// sees either the old row or the rewritten one, never deletes the rewrite
// from a stale view. Writers do not exclude each other.
func (p *PebbleStore) CreateOperationChange(change *OperationChange) error {
	supplied := change.ID != ""
	if !supplied {
		change.ID = ulid.Make().String()
	}
	change.CreatedAt = time.Now()
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	key := opChangeKey(change.OperationID, change.ID)
	batch := p.db.NewBatch()
	defer batch.Close()
	p.opChangeJournalMu.RLock()
	defer p.opChangeJournalMu.RUnlock()
	// An id minted here cannot name a stored row, so it skips the read. Almost
	// every caller in the tree supplies its own id (usually a fresh ULID), and
	// the store cannot tell a fresh id from a reused one, so in practice
	// nearly every call pays one point read; for a fresh id it is a miss.
	if supplied {
		oldBook, found, err := p.storedOpChangeBookID(key)
		if err != nil {
			return err
		}
		if found && oldBook != change.BookID {
			if err := unstageOpChangeIndex(batch, oldBook, key); err != nil {
				return err
			}
		}
	}
	if err := batch.Set(key, data, nil); err != nil {
		return err
	}
	if err := stageOpChangeIndex(batch, change.BookID, key); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

// GetOperationChanges returns all changes for a given operation.
func (p *PebbleStore) GetOperationChanges(operationID string) ([]*OperationChange, error) {
	prefix := []byte(fmt.Sprintf("opchange:%s:", operationID))
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: prefixEnd(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var changes []*OperationChange
	for iter.First(); iter.Valid(); iter.Next() {
		var c OperationChange
		if err := json.Unmarshal(iter.Value(), &c); err != nil {
			return nil, err
		}
		changes = append(changes, &c)
	}
	return changes, iter.Error()
}

// GetBookChanges returns all changes for a given book, in primary key order
// (operation id, then change id). Once this process has verified (or rebuilt)
// the opchange_by_book: index and its sentinel is set, it reads the index and
// point-gets each row; until then, and always for a book id that is not
// indexable (empty, or containing ':'), it scans and decodes every opchange
// row. Both paths return the same rows in the same order, and both fail on an
// undecodable row (see the TRUST GATE section of pebble_store_opchange_index.go
// for exactly when that holds).
func (p *PebbleStore) GetBookChanges(bookID string) ([]*OperationChange, error) {
	if opChangeIndexable(bookID) {
		usable, err := p.opChangeByBookIndexUsable()
		if err != nil {
			return nil, err
		}
		if usable {
			return p.getBookChangesIndexed(bookID)
		}
	}
	return p.getBookChangesScan(bookID)
}

// opChangeMarkAfterScan, when non-nil, runs in MarkOperationChangesReverted
// after the unlocked scan and before it takes the read lock. Test-only; nil in
// production.
var opChangeMarkAfterScan func()

// MarkOperationChangesReverted marks the listed changes of an operation as
// reverted. IDs that are not changes of this operation are ignored; rows not
// listed are left untouched. Every rewritten row and its re-Set
// opchange_by_book: entry commit in one batch, so the marks land together.
//
// The scan that finds the wanted rows runs outside opChangeJournalMu (it is a
// full read of the operation's rows). Under the read side the function then
// point-gets only the wanted keys, re-decodes each, and commits, so the mark
// is built from the row as it is now, not as the scan saw it. A row that
// vanished between the scan and the lock (a prune) is skipped, not
// resurrected: the same as an id the scan never returned. A row that is
// already reverted by then is skipped too. A prune either removed the row
// before the point read, or re-checks the rewritten row after the commit.
func (p *PebbleStore) MarkOperationChangesReverted(operationID string, changeIDs []string) error {
	if len(changeIDs) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(changeIDs))
	for _, id := range changeIDs {
		want[id] = struct{}{}
	}
	changes, err := p.GetOperationChanges(operationID)
	if err != nil {
		return err
	}
	var keys [][]byte
	for _, c := range changes {
		if _, ok := want[c.ID]; ok && c.RevertedAt == nil {
			keys = append(keys, opChangeKey(c.OperationID, c.ID))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if opChangeMarkAfterScan != nil {
		opChangeMarkAfterScan()
	}
	p.opChangeJournalMu.RLock()
	defer p.opChangeJournalMu.RUnlock()
	batch := p.db.NewBatch()
	defer batch.Close()
	staged := 0
	now := time.Now()
	for _, key := range keys {
		v, closer, err := p.db.Get(key)
		if errors.Is(err, pebble.ErrNotFound) {
			continue // pruned since the scan
		}
		if err != nil {
			return fmt.Errorf("re-read opchange row to mark reverted: %w", err)
		}
		var c OperationChange
		uerr := json.Unmarshal(v, &c)
		closer.Close()
		if uerr != nil {
			return uerr
		}
		if c.RevertedAt != nil {
			continue
		}
		c.RevertedAt = &now
		data, err := json.Marshal(&c)
		if err != nil {
			return err
		}
		if err := batch.Set(key, data, nil); err != nil {
			return err
		}
		if err := stageOpChangeIndex(batch, c.BookID, key); err != nil {
			return err
		}
		staged++
	}
	if staged == 0 {
		return nil
	}
	return batch.Commit(pebble.Sync)
}
