// file: internal/database/pebble_store_activity.go
// version: 1.3.0
// guid: 2e007a48-ab98-4cd4-bd6a-f85b75de0cfa
// last-edited: 2026-10-03

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// AddSystemActivityLog stores a log entry from a housekeeping goroutine.
func (p *PebbleStore) AddSystemActivityLog(source, level, message string) error {
	key := fmt.Sprintf("syslog:%s:%s", time.Now().Format(time.RFC3339Nano), source)
	val := SystemActivityLog{
		Source:    source,
		Level:     level,
		Message:   message,
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(val)
	if err != nil {
		return err
	}
	return p.db.Set([]byte(key), data, pebble.Sync)
}

// GetSystemActivityLogs retrieves recent system activity log entries.
func (p *PebbleStore) GetSystemActivityLogs(source string, limit int) ([]SystemActivityLog, error) {
	prefix := []byte("syslog:")
	upperBound := append(append([]byte{}, prefix...), 0xFF)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upperBound,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var logs []SystemActivityLog
	for iter.Last(); iter.Valid(); iter.Prev() {
		var l SystemActivityLog
		if err := json.Unmarshal(iter.Value(), &l); err != nil {
			continue
		}
		if source != "" && l.Source != source {
			continue
		}
		logs = append(logs, l)
		if len(logs) >= limit {
			break
		}
	}
	return logs, nil
}

// PruneOperationLogs deletes operation log entries older than the given time.
// Key format: operationlog:<operation_id>:<timestamp_nanos>:<seq>
func (p *PebbleStore) PruneOperationLogs(olderThan time.Time) (int, error) {
	prefix := "operationlog:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	olderThanNanos := olderThan.UnixNano()
	for iter.First(); iter.Valid(); iter.Next() {
		// Key: operationlog:<opID>:<nanos>:<seq>
		// Parse the JSON value to get CreatedAt.
		var logEntry OperationLog
		if jsonErr := json.Unmarshal(iter.Value(), &logEntry); jsonErr != nil {
			continue
		}
		if logEntry.CreatedAt.UnixNano() < olderThanNanos {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete operationlog: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}

// opChangePruneChunk is how many journal rows one PruneOperationChanges commit
// deletes. A var only so tests can exercise multi-chunk prunes; never
// reassign in prod code.
var opChangePruneChunk = 5_000

// opChangePruneBeforeFlush, when non-nil, runs after a prune chunk's
// candidates are collected and before the chunk takes the journal lock to
// re-check and delete them. Test-only; nil in production.
var opChangePruneBeforeFlush func()

// opChangePruneBeforeCommit, when non-nil, runs inside pruneOpChangeChunk's
// locked section, after every candidate was re-read and staged and just before
// the batch commits. Test-only; nil in production.
var opChangePruneBeforeCommit func()

// OpChangeTypesKeptByPrune are change types PruneOperationChanges never
// deletes, whatever their age. "repair_plan_record" (undo.ChangeTypeRepairPlanRecord,
// spelled out here because undo imports this package) is the decision an
// interrupted Repairs apply started from: while it stands, a later plan
// continues or holds that run, and once it aged out the folder would be
// planned afresh around another survivor, splitting one work into two live
// books. One row per apply run, so keeping them costs little.
var OpChangeTypesKeptByPrune = map[string]bool{"repair_plan_record": true}

// PruneOperationChanges deletes operation change entries older than the given
// time, each with its opchange_by_book: index entry. Undecodable rows are
// skipped (never deleted), as before, and so is every row of a type in
// OpChangeTypesKeptByPrune.
// Key format: opchange:<operation_id>:<ulid>
//
// It commits every opChangePruneChunk rows, so no batch grows with the
// journal; each row and its index entry are always in the same batch. The
// iterator only nominates candidates: each chunk takes the write side of
// opChangeJournalMu, re-reads every candidate from the store, and deletes it
// only if it still decodes and is still older than the cutoff, unstaging the
// entry of the BookID it has NOW. A row rewritten under the same id after the
// iterator read it (CreateOperationChange with a supplied id, which holds the
// read side from its read to its commit) therefore survives with its entry.
// On a failure it returns the rows deleted by the chunks already committed.
func (p *PebbleStore) PruneOperationChanges(olderThan time.Time) (int, error) {
	prefixBytes := []byte(opChangeKeyPrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	chunk := opChangePruneChunk
	if chunk < 1 {
		chunk = 1
	}
	deleted := 0
	cands := make([][]byte, 0, chunk)
	flush := func() error {
		if len(cands) == 0 {
			return nil
		}
		n, err := p.pruneOpChangeChunk(cands, olderThan)
		deleted += n
		cands = cands[:0]
		return err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var change OperationChange
		if jsonErr := json.Unmarshal(iter.Value(), &change); jsonErr != nil {
			continue
		}
		if !change.CreatedAt.Before(olderThan) || OpChangeTypesKeptByPrune[change.ChangeType] {
			continue
		}
		cands = append(cands, bytes.Clone(iter.Key()))
		if len(cands) >= chunk {
			if err := flush(); err != nil {
				return deleted, err
			}
		}
	}
	if err := iter.Error(); err != nil {
		return deleted, err
	}
	if err := flush(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// pruneOpChangeChunk re-checks each candidate under the journal write lock and
// deletes the ones still older than olderThan, with their index entries, in
// one Sync batch. It returns how many it deleted (zero on error: nothing in
// the batch committed).
func (p *PebbleStore) pruneOpChangeChunk(keys [][]byte, olderThan time.Time) (int, error) {
	if opChangePruneBeforeFlush != nil {
		opChangePruneBeforeFlush()
	}
	p.opChangeJournalMu.Lock()
	defer p.opChangeJournalMu.Unlock()
	batch := p.db.NewBatch()
	defer batch.Close()
	n := 0
	for _, k := range keys {
		v, closer, err := p.db.Get(k)
		if errors.Is(err, pebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("re-read opchange row for prune: %w", err)
		}
		var change OperationChange
		uerr := json.Unmarshal(v, &change)
		closer.Close()
		if uerr != nil || !change.CreatedAt.Before(olderThan) || OpChangeTypesKeptByPrune[change.ChangeType] {
			continue // now undecodable (never deleted), rewritten since, or kept by type
		}
		if err := batch.Delete(k, nil); err != nil {
			return 0, fmt.Errorf("pebble batch delete opchange: %w", err)
		}
		// The row's opchange_by_book: entry goes in the same batch
		// (pebble_store_opchange_index.go).
		if err := unstageOpChangeIndex(batch, change.BookID, k); err != nil {
			return 0, err
		}
		n++
	}
	if n == 0 {
		return 0, nil
	}
	if opChangePruneBeforeCommit != nil {
		opChangePruneBeforeCommit()
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("commit opchange prune chunk: %w", err)
	}
	return n, nil
}

// PruneSystemActivityLogs deletes system activity log entries older than the given time.
// Key format: syslog:<RFC3339Nano>:<source>
func (p *PebbleStore) PruneSystemActivityLogs(olderThan time.Time) (int, error) {
	prefix := "syslog:"
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	deleted := 0
	batch := p.db.NewBatch()
	defer batch.Close()

	// Key format: syslog:<RFC3339Nano>:<source>
	// RFC3339Nano contains colons (e.g. "2006-01-02T15:04:05.999999999Z07:00"),
	// so we parse the JSON value to get CreatedAt rather than parsing the key.
	for iter.First(); iter.Valid(); iter.Next() {
		var entry struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if jsonErr := json.Unmarshal(iter.Value(), &entry); jsonErr != nil {
			continue
		}
		if entry.CreatedAt.Before(olderThan) {
			if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
				return 0, fmt.Errorf("pebble batch delete syslog: %w", bErr)
			}
			deleted++
		}
	}
	if deleted > 0 {
		return deleted, batch.Commit(pebble.Sync)
	}
	return 0, nil
}
