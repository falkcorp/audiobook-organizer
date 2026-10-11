// file: internal/database/pebble_store_activity.go
// version: 1.5.0
// guid: 2e007a48-ab98-4cd4-bd6a-f85b75de0cfa
// last-edited: 2026-10-11

package database

import (
	"bytes"
	"context"
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

// PruneChunkFunc is called after each committed prune chunk with the number of
// rows that chunk deleted. A nil PruneChunkFunc is allowed everywhere one is
// accepted. It runs on the pruning goroutine between chunks, so it must be
// quick and must not call back into the store.
type PruneChunkFunc = func(deleted int)

// logPruneChunk is how many rows one PruneOperationLogs or
// PruneSystemActivityLogs commit deletes. It matches opChangePruneChunk: a
// log or syslog row costs one delete (an opchange row costs two, itself and its
// opchange_by_book: entry), so 5,000 is the same order of batch size the opchange
// prune already runs in prod without growing the memtable or stalling a
// concurrent writer. A var only so tests can exercise multi-chunk prunes; never
// reassign in prod code.
var logPruneChunk = 5_000

// pruneExpiredChunked deletes every row under prefix for which expired(value)
// reports true, committing every logPruneChunk deletions instead of staging the
// whole backlog in one batch. A row whose value expired() cannot judge (it
// returns ok=false) is skipped, never deleted.
//
// ctx is checked before each chunk's commit and once on entry, so a cancelled
// context stops between chunks: every chunk already committed stays committed,
// the staged-but-uncommitted remainder is dropped, and the returned count is
// exactly the rows deleted by committed chunks, alongside ctx.Err() (a
// cancellation is reported, never turned into a nil error). onChunk, when
// non-nil, runs after each successful commit.
//
// Concurrency note: this is deliberately one sequential pass. Every chunk is
// disjoint from the next by construction (the iterator walks each key once), so
// there is nothing to partition, and the work is bounded by Pebble commit
// latency (Sync), not CPU; fanning out would only multiply the fsyncs the chunking
// exists to keep small.
//
// The iterator reads a snapshot taken when it opened, so deleting behind it is
// safe. On a failure it returns the rows deleted by the chunks already
// committed.
func (p *PebbleStore) pruneExpiredChunked(
	ctx context.Context, prefix, label string,
	expired func(value []byte) (isExpired bool, ok bool), onChunk PruneChunkFunc,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	prefixBytes := []byte(prefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefixBytes,
		UpperBound: append(append([]byte{}, prefixBytes...), 0xFF),
	})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	chunk := logPruneChunk
	if chunk < 1 {
		chunk = 1
	}
	batch := p.db.NewBatch()
	defer func() { batch.Close() }()

	deleted := 0
	staged := 0
	commit := func() error {
		if staged == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return fmt.Errorf("commit %s prune chunk: %w", label, err)
		}
		deleted += staged
		if onChunk != nil {
			onChunk(staged)
		}
		staged = 0
		batch.Close()
		batch = p.db.NewBatch()
		return nil
	}
	for iter.First(); iter.Valid(); iter.Next() {
		isExpired, ok := expired(iter.Value())
		if !ok || !isExpired {
			continue
		}
		if bErr := batch.Delete(iter.Key(), nil); bErr != nil {
			return deleted, fmt.Errorf("pebble batch delete %s: %w", label, bErr)
		}
		staged++
		if staged >= chunk {
			if err := commit(); err != nil {
				return deleted, err
			}
		}
	}
	if err := iter.Error(); err != nil {
		return deleted, err
	}
	if err := commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}

// PruneOperationLogs deletes operation log entries older than the given time,
// in chunks of logPruneChunk (see pruneExpiredChunked for the commit,
// cancellation and progress contract).
// Key format: operationlog:<operation_id>:<timestamp_nanos>:<seq>
func (p *PebbleStore) PruneOperationLogs(ctx context.Context, olderThan time.Time, onChunk PruneChunkFunc) (int, error) {
	olderThanNanos := olderThan.UnixNano()
	return p.pruneExpiredChunked(ctx, "operationlog:", "operationlog", func(v []byte) (bool, bool) {
		// The key's timestamp is not the entry's CreatedAt; parse the value.
		var logEntry OperationLog
		if json.Unmarshal(v, &logEntry) != nil {
			return false, false
		}
		return logEntry.CreatedAt.UnixNano() < olderThanNanos, true
	}, onChunk)
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
//
// "repair_owner_apply" (undo.ChangeTypeRepairOwnerApply) is the audit record
// that the owner himself applied an iTunes-tracked Repairs row: kept for as
// long as the store is, like any audit trail.
var OpChangeTypesKeptByPrune = map[string]bool{"repair_plan_record": true, "repair_owner_apply": true}

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
// On a failure, or when ctx is cancelled (checked before each chunk; ctx.Err()
// is returned, never swallowed), it returns the rows deleted by the chunks
// already committed. onChunk, when non-nil, runs after each committed chunk.
func (p *PebbleStore) PruneOperationChanges(ctx context.Context, olderThan time.Time, onChunk PruneChunkFunc) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
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
		// Checked before the chunk takes the journal lock, so a cancelled run
		// stops between chunks and never half-applies one.
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := p.pruneOpChangeChunk(cands, olderThan)
		deleted += n
		cands = cands[:0]
		if err == nil && n > 0 && onChunk != nil {
			onChunk(n)
		}
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

// PruneSystemActivityLogs deletes system activity log entries older than the
// given time, in chunks of logPruneChunk (see pruneExpiredChunked for the
// commit, cancellation and progress contract). syslog: rows have no other
// cleanup, so the first prune on an old install walks a months-long backlog.
// Key format: syslog:<RFC3339Nano>:<source>
func (p *PebbleStore) PruneSystemActivityLogs(ctx context.Context, olderThan time.Time, onChunk PruneChunkFunc) (int, error) {
	// RFC3339Nano contains colons (e.g. "2006-01-02T15:04:05.999999999Z07:00"),
	// so we parse the JSON value to get CreatedAt rather than parsing the key.
	return p.pruneExpiredChunked(ctx, "syslog:", "syslog", func(v []byte) (bool, bool) {
		var entry struct {
			CreatedAt time.Time `json:"created_at"`
		}
		if json.Unmarshal(v, &entry) != nil {
			return false, false
		}
		return entry.CreatedAt.Before(olderThan), true
	}, onChunk)
}
