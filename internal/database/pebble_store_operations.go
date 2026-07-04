// file: internal/database/pebble_store_operations.go
// version: 1.0.0
// guid: e4277998-6d7e-4f2a-9b5c-0a620a98105e
// last-edited: 2026-07-03

package database

import (
	"encoding/json"
	"fmt"
	"sort"
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
	end := offset + limit
	if end > len(operations) {
		end = len(operations)
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

	// Sort by created_at descending
	for i := 0; i < len(logs)-1; i++ {
		for j := i + 1; j < len(logs); j++ {
			if logs[j].CreatedAt.After(logs[i].CreatedAt) {
				logs[i], logs[j] = logs[j], logs[i]
			}
		}
	}

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

func (p *PebbleStore) DeleteOperationsByStatus(statuses []string) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	statusSet := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		statusSet[s] = true
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte("operation:"),
		UpperBound: []byte("operation:~"),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	for iter.First(); iter.Valid(); iter.Next() {
		var op Operation
		if err := json.Unmarshal(iter.Value(), &op); err != nil {
			continue
		}
		if statusSet[op.Status] {
			if err := batch.Delete(iter.Key(), nil); err != nil {
				batch.Close()
				return 0, fmt.Errorf("pebble batch delete operation: %w", err)
			}
			deleted++
		}
	}
	if deleted > 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return 0, err
		}
	} else {
		batch.Close()
	}
	return deleted, nil
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
		if op.Status == "running" || op.Status == "queued" || op.Status == "interrupted" {
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

// CreateOperationChange stores an operation change in PebbleDB.
func (p *PebbleStore) CreateOperationChange(change *OperationChange) error {
	if change.ID == "" {
		change.ID = ulid.Make().String()
	}
	change.CreatedAt = time.Now()
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("opchange:%s:%s", change.OperationID, change.ID)
	return p.db.Set([]byte(key), data, pebble.Sync)
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

// GetBookChanges returns all changes for a given book.
func (p *PebbleStore) GetBookChanges(bookID string) ([]*OperationChange, error) {
	prefix := []byte("opchange:")
	upperBound := []byte("opchange;") // ':' + 1 = ';'
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
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
		if c.BookID == bookID {
			changes = append(changes, &c)
		}
	}
	return changes, iter.Error()
}

// RevertOperationChanges marks all changes for an operation as reverted.
func (p *PebbleStore) RevertOperationChanges(operationID string) error {
	changes, err := p.GetOperationChanges(operationID)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, c := range changes {
		if c.RevertedAt == nil {
			c.RevertedAt = &now
			data, err := json.Marshal(c)
			if err != nil {
				return err
			}
			key := fmt.Sprintf("opchange:%s:%s", c.OperationID, c.ID)
			if err := p.db.Set([]byte(key), data, pebble.Sync); err != nil {
				return err
			}
		}
	}
	return nil
}
