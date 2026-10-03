// file: internal/database/pebble_store_opchange_index.go
// version: 1.0.0
// guid: 7ce04252-7ac9-421a-ba5e-5f230bbf0ab4
// last-edited: 2026-10-03

// The opchange_by_book: secondary index over the operation-change journal.
//
//	opchange:<opID>:<changeID>                      -> OperationChange JSON (primary row)
//	opchange_by_book:<bookID>:<opID>:<changeID>     -> (empty value)
//	opchange_undecodable:<opID>:<changeID>          -> (empty value)
//
// The index key is the book id followed by the primary key with its
// "opchange:" prefix removed, so within one book the index sorts in exactly
// the primary key's order. GetBookChanges has always returned rows in primary
// key order (it was a filtered scan of the whole opchange: range); reading the
// index in key order keeps that contract byte for byte.
//
// Invariant (completeness): for every opchange row with a non-empty BookID B
// whose JSON decodes, the key opchange_by_book:B:<suffix> exists. Extras are
// allowed: the reader point-gets each row and keeps it only when the decoded
// BookID still equals the requested one, so an entry whose row is gone or has
// moved to another book costs one point read and is never returned. A MISSING
// entry would hide a row, so every writer keeps the entry in the same batch as
// the row:
//
//   - CreateOperationChange Sets the entry unconditionally (a rewrite with the
//     same BookID re-Sets the same key: still one entry), and when the caller
//     supplied an id whose stored row named another book, Deletes that book's
//     entry in the same batch (a BookID change moves it).
//   - MarkOperationChangesReverted re-Sets each rewritten row's entry.
//   - PruneOperationChanges Deletes each pruned row's entry.
//
// Nothing else writes the opchange: prefix. DeleteOperationWithLogs and
// DeleteOperationV2 never touch opchange rows, and Reset wipes the whole
// keyspace (index and sentinel included).
//
// Rows with an empty BookID are not indexed; GetBookChanges("") keeps the
// full scan. Rows whose JSON does not decode have no readable BookID; the
// backfill gives each an opchange_undecodable: marker instead, and the indexed
// reader fails closed (returns the decode error) while any marked row is still
// present and still undecodable, exactly as the full scan does.
//
// The families sit outside the "opchange:" scan range on purpose ('_' sorts
// after ':' and after ';'), so GetBookChanges' fallback scan, GetOperationChanges
// and PruneOperationChanges never walk index keys.
//
// ROLLBACK HAZARD: a binary that predates this index does not maintain it. If
// one writes opchange rows after the sentinel is set, those rows have no entry
// and GetBookChanges silently under-reports them. After any rollback-then-
// roll-forward, run maintenance.opchange-book-index-rebuild, which clears the
// sentinel (readers fall back to the full scan) and rebuilds from the start.

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

const (
	opChangeKeyPrefix          = "opchange:"
	opChangeByBookPrefix       = "opchange_by_book:"
	opChangeUndecodablePrefix  = "opchange_undecodable:"
	opChangeScanUpperBound     = "opchange;" // ':' + 1
	opChangeByBookBackfillKey  = "system:backfill:opchange_by_book_index_v1_done"
	opChangeByBookCursorKey    = "system:backfill:opchange_by_book_index_v1_cursor"
	opChangeByBookLogEveryRows = 100_000
)

// opChangeByBookBackfillChunk is how many journal rows one backfill commit
// covers. A var only so tests can exercise the multi-chunk and resume paths;
// never reassign in prod code.
var opChangeByBookBackfillChunk = 5_000

// opChangeByBookBackfillAfterChunk, when non-nil, runs after each durable
// chunk commit with the run's commit count so far. A non-nil return aborts the
// run as if the process had been cut at that point. Test-only; nil in
// production.
var opChangeByBookBackfillAfterChunk func(commits int) error

// opChangeKey is the primary key of one journal row.
func opChangeKey(operationID, changeID string) []byte {
	return []byte(opChangeKeyPrefix + operationID + ":" + changeID)
}

// opChangeByBookKey is the index key for the row at primary key primary,
// filed under bookID.
func opChangeByBookKey(bookID string, primary []byte) []byte {
	suffix := primary[len(opChangeKeyPrefix):]
	k := make([]byte, 0, len(opChangeByBookPrefix)+len(bookID)+1+len(suffix))
	k = append(k, opChangeByBookPrefix...)
	k = append(k, bookID...)
	k = append(k, ':')
	return append(k, suffix...)
}

// opChangeByBookBookPrefix is the scan prefix holding bookID's entries. A book
// id that itself continues with ':' would share it; the reader's BookID check
// drops those rows.
func opChangeByBookBookPrefix(bookID string) []byte {
	return []byte(opChangeByBookPrefix + bookID + ":")
}

// opChangeUndecodableKey is the marker key for the undecodable row at primary.
func opChangeUndecodableKey(primary []byte) []byte {
	suffix := primary[len(opChangeKeyPrefix):]
	k := make([]byte, 0, len(opChangeUndecodablePrefix)+len(suffix))
	k = append(k, opChangeUndecodablePrefix...)
	return append(k, suffix...)
}

// stageOpChangeIndex adds the row's index entry to batch. Rows with no book
// are not indexed (GetBookChanges("") scans).
func stageOpChangeIndex(batch *pebble.Batch, bookID string, primary []byte) error {
	if bookID == "" {
		return nil
	}
	if err := batch.Set(opChangeByBookKey(bookID, primary), nil, nil); err != nil {
		return fmt.Errorf("stage opchange_by_book entry: %w", err)
	}
	return nil
}

// unstageOpChangeIndex adds the deletion of the row's index entry to batch.
func unstageOpChangeIndex(batch *pebble.Batch, bookID string, primary []byte) error {
	if bookID == "" {
		return nil
	}
	if err := batch.Delete(opChangeByBookKey(bookID, primary), nil); err != nil {
		return fmt.Errorf("stage opchange_by_book delete: %w", err)
	}
	return nil
}

// storedOpChangeBookID reads the BookID of the row at primary. found is false
// when the row does not exist or does not decode (an undecodable row has no
// readable book, so there is no entry to move).
func (p *PebbleStore) storedOpChangeBookID(primary []byte) (bookID string, found bool, err error) {
	v, closer, err := p.db.Get(primary)
	if errors.Is(err, pebble.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read existing opchange row: %w", err)
	}
	defer closer.Close()
	var old OperationChange
	if json.Unmarshal(v, &old) != nil {
		return "", false, nil
	}
	return old.BookID, true, nil
}

// opChangeByBookIndexBuilt reports whether the backfill sentinel exists. Only
// true is cached, so a long-lived process picks up a completion without a
// restart. A read error is returned, never guessed at.
func (p *PebbleStore) opChangeByBookIndexBuilt() (bool, error) {
	if p.opChangeByBookBuilt.Load() {
		return true, nil
	}
	_, closer, err := p.db.Get([]byte(opChangeByBookBackfillKey))
	switch {
	case err == nil:
		closer.Close()
		p.opChangeByBookBuilt.Store(true)
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("read %s: %w", opChangeByBookBackfillKey, err)
	}
}

// getBookChangesScan is the pre-index GetBookChanges: every opchange row,
// decoded, filtered by BookID, in primary key order. Any undecodable row fails
// the whole call.
func (p *PebbleStore) getBookChangesScan(bookID string) ([]*OperationChange, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(opChangeKeyPrefix),
		UpperBound: []byte(opChangeScanUpperBound),
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

// getBookChangesIndexed serves GetBookChanges from the index. The marker
// check, the index walk and every point read come from one snapshot, so the
// result is one consistent view of the journal, as the single-iterator scan's
// was: a row moved between books mid-call is seen under exactly one of them.
func (p *PebbleStore) getBookChangesIndexed(bookID string) ([]*OperationChange, error) {
	snap := p.db.NewSnapshot()
	defer snap.Close()

	if err := opChangeUndecodableGate(snap); err != nil {
		return nil, err
	}

	prefix := opChangeByBookBookPrefix(bookID)
	iter, err := snap.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var changes []*OperationChange
	primary := make([]byte, 0, 96)
	for iter.First(); iter.Valid(); iter.Next() {
		primary = append(append(primary[:0], opChangeKeyPrefix...), iter.Key()[len(prefix):]...)
		v, closer, err := snap.Get(primary)
		if errors.Is(err, pebble.ErrNotFound) {
			continue // stale entry: the row was pruned
		}
		if err != nil {
			return nil, err
		}
		var c OperationChange
		uerr := json.Unmarshal(v, &c)
		closer.Close()
		if uerr != nil {
			return nil, uerr
		}
		if c.BookID != bookID {
			continue // stale entry (row moved) or a "<bookID>:..." neighbour
		}
		changes = append(changes, &c)
	}
	return changes, iter.Error()
}

// opChangeUndecodableGate returns the decode error of the first marked row
// that is still present and still undecodable, matching the full scan, which
// fails on any undecodable row. A marker whose row is gone or now decodes is
// stale and ignored. The family is empty unless the journal has corruption, so
// on a healthy store this is one empty range probe.
func opChangeUndecodableGate(r pebble.Reader) error {
	lower := []byte(opChangeUndecodablePrefix)
	iter, err := r.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: prefixEnd(lower)})
	if err != nil {
		return err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		primary := append([]byte(opChangeKeyPrefix), iter.Key()[len(lower):]...)
		v, closer, err := r.Get(primary)
		if errors.Is(err, pebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		var c OperationChange
		uerr := json.Unmarshal(v, &c)
		closer.Close()
		if uerr != nil {
			return uerr
		}
	}
	return iter.Error()
}

// OpChangeByBookBackfillResult summarises one backfill run.
type OpChangeByBookBackfillResult struct {
	// Skipped is true when the sentinel was already set and nothing was done.
	Skipped bool `json:"skipped"`
	// ResumedAfter is the cursor (a primary key) an interrupted earlier run
	// left behind; empty when this run started from the beginning.
	ResumedAfter string `json:"resumed_after,omitempty"`
	Scanned      int    `json:"scanned"`
	Indexed      int    `json:"indexed"`
	// Undecodable rows got an opchange_undecodable: marker instead of an entry.
	Undecodable int `json:"undecodable"`
	Commits     int `json:"commits"`
}

// BackfillOpChangeByBookIndex builds the opchange_by_book: index once, gated by
// its sentinel, resuming from the cursor an interrupted run left. It is the
// startup migration: after the first successful run it is a logged no-op.
func (p *PebbleStore) BackfillOpChangeByBookIndex(ctx context.Context) (OpChangeByBookBackfillResult, error) {
	return p.backfillOpChangeByBook(ctx, false)
}

// RebuildOpChangeByBookIndex is the rollback runbook's repair: it deletes the
// sentinel and the cursor in one batch, so every GetBookChanges falls back to
// the full scan, then rebuilds from the first row and sets the sentinel again.
// If it is cut, the next startup backfill resumes it from its cursor. It only
// ever Sets entries and markers, so it is safe on a live store.
func (p *PebbleStore) RebuildOpChangeByBookIndex(ctx context.Context) (OpChangeByBookBackfillResult, error) {
	return p.backfillOpChangeByBook(ctx, true)
}

// backfillOpChangeByBook is one streaming pass over the opchange: rows.
//
// CONCURRENCY: deliberately a single sequential pass, not a worker pool. The
// per-row work is a small JSON decode plus one batched Set into the same Pebble
// instance, so the pass is bound by Pebble's own read and commit throughput,
// and a resumable cursor needs rows committed in key order. Each chunk opens a
// fresh iterator at the cursor, so no iterator (and the sstables it pins) is
// held for the whole journal, and memory is bounded by one chunk's batch.
//
// Race with live writes: the pass only Sets entries and markers; it never
// writes or deletes a row. Every row changed after the pass read it was written
// by this binary, which maintains the row's entry in the row's own batch; every
// row not changed is visited. So completeness holds when the sentinel is set.
// The one artefact is an extra entry (a row read under book A, then moved to B
// or pruned, gets A's entry re-Set by this pass), which the reader drops.
//
// Resume: each chunk's entries and the cursor naming its last row commit in one
// pebble.Sync batch, so the cursor never runs ahead of a durable entry. The
// sentinel is written (and the cursor deleted) only after the last chunk.
func (p *PebbleStore) backfillOpChangeByBook(ctx context.Context, force bool) (OpChangeByBookBackfillResult, error) {
	var res OpChangeByBookBackfillResult
	start := time.Now()
	mode := "backfill"
	if force {
		mode = "rebuild"
	}

	p.opChangeIdxRunMu.Lock()
	defer p.opChangeIdxRunMu.Unlock()

	if force {
		reset := p.db.NewBatch()
		if err := reset.Delete([]byte(opChangeByBookBackfillKey), nil); err != nil {
			reset.Close()
			return res, err
		}
		if err := reset.Delete([]byte(opChangeByBookCursorKey), nil); err != nil {
			reset.Close()
			return res, err
		}
		// Drop the cached flag before the commit lands so no reader that
		// starts after this point trusts the index; one already inside
		// getBookChangesIndexed reads an index that is a superset of what it
		// was a moment ago.
		p.opChangeByBookBuilt.Store(false)
		err := reset.Commit(pebble.Sync)
		reset.Close()
		if err != nil {
			return res, fmt.Errorf("clear opchange_by_book sentinel: %w", err)
		}
	} else {
		built, err := p.opChangeByBookIndexBuilt()
		if err != nil {
			slog.Error("opchange-index-backfill: cannot read sentinel, aborting", "err", err)
			return res, err
		}
		if built {
			slog.Info("opchange-index-backfill: already complete, skipping", "sentinel", opChangeByBookBackfillKey)
			res.Skipped = true
			return res, nil
		}
	}

	lower := []byte(opChangeKeyPrefix)
	switch v, closer, err := p.db.Get([]byte(opChangeByBookCursorKey)); {
	case err == nil:
		cursor := bytes.Clone(v)
		closer.Close()
		if !bytes.HasPrefix(cursor, lower) {
			return res, fmt.Errorf("opchange_by_book cursor %q is not an opchange key", cursor)
		}
		res.ResumedAfter = string(cursor)
		lower = append(cursor, 0) // the first key strictly after the cursor
	case errors.Is(err, pebble.ErrNotFound):
	default:
		return res, fmt.Errorf("read opchange_by_book cursor: %w", err)
	}
	slog.Info("opchange-index-backfill: starting", "mode", mode,
		"chunk", opChangeByBookBackfillChunk, "resumed_after", res.ResumedAfter)

	upper := []byte(opChangeScanUpperBound)
	chunk := opChangeByBookBackfillChunk
	if chunk < 1 {
		chunk = 1
	}
	nextLog := opChangeByBookLogEveryRows
	for {
		if err := ctx.Err(); err != nil {
			slog.Warn("opchange-index-backfill: canceled, sentinel NOT set; resumes next run",
				"scanned", res.Scanned, "commits", res.Commits)
			return res, err
		}
		n, last, err := p.opChangeBackfillChunk(lower, upper, chunk, &res)
		if err != nil {
			slog.Error("opchange-index-backfill: failed, sentinel NOT set", "mode", mode,
				"scanned", res.Scanned, "commits", res.Commits, "err", err)
			return res, err
		}
		if n == 0 {
			break
		}
		res.Commits++
		if res.Scanned >= nextLog {
			slog.Info("opchange-index-backfill: progress", "scanned", res.Scanned,
				"indexed", res.Indexed, "elapsed", time.Since(start).Round(time.Second).String())
			nextLog += opChangeByBookLogEveryRows
		}
		if opChangeByBookBackfillAfterChunk != nil {
			if err := opChangeByBookBackfillAfterChunk(res.Commits); err != nil {
				return res, err
			}
		}
		if n < chunk {
			break
		}
		lower = append(last, 0)
	}

	done := p.db.NewBatch()
	defer done.Close()
	if err := done.Set([]byte(opChangeByBookBackfillKey), []byte("1"), nil); err != nil {
		return res, err
	}
	if err := done.Delete([]byte(opChangeByBookCursorKey), nil); err != nil {
		return res, err
	}
	// Every chunk committed with pebble.Sync before this, so every entry the
	// sentinel vouches for is already durable.
	if err := done.Commit(pebble.Sync); err != nil {
		slog.Error("opchange-index-backfill: sentinel commit failed", "scanned", res.Scanned, "err", err)
		return res, err
	}
	res.Commits++
	p.opChangeByBookBuilt.Store(true)
	if res.Undecodable > 0 {
		slog.Error("opchange-index-backfill: opchange rows cannot be decoded; GetBookChanges fails "+
			"for every book until each is rewritten or removed, as it did before the index",
			"undecodable", res.Undecodable)
	}
	slog.Info("opchange-index-backfill: complete", "mode", mode, "scanned", res.Scanned,
		"indexed", res.Indexed, "undecodable", res.Undecodable, "commits", res.Commits,
		"duration", time.Since(start).Round(time.Millisecond).String())
	return res, nil
}

// opChangeBackfillChunk indexes up to chunk rows in [lower, upper) and commits
// their entries together with the cursor in one Sync batch. It returns how many
// rows it read and the last row's key (owned by the caller).
func (p *PebbleStore) opChangeBackfillChunk(lower, upper []byte, chunk int, res *OpChangeByBookBackfillResult) (int, []byte, error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return 0, nil, err
	}
	batch := p.db.NewBatch()
	defer batch.Close()

	n, indexed, undecodable := 0, 0, 0
	var last []byte
	for iter.First(); iter.Valid() && n < chunk; iter.Next() {
		k := iter.Key()
		var c OperationChange
		if json.Unmarshal(iter.Value(), &c) != nil {
			if err := batch.Set(opChangeUndecodableKey(k), nil, nil); err != nil {
				iter.Close()
				return 0, nil, err
			}
			undecodable++
		} else if c.BookID != "" {
			if err := batch.Set(opChangeByBookKey(c.BookID, k), nil, nil); err != nil {
				iter.Close()
				return 0, nil, err
			}
			indexed++
		}
		n++
		last = append(last[:0], k...)
	}
	iterErr := iter.Error()
	if cerr := iter.Close(); iterErr == nil {
		iterErr = cerr
	}
	if iterErr != nil {
		return 0, nil, iterErr
	}
	if n == 0 {
		return 0, nil, nil
	}
	if err := batch.Set([]byte(opChangeByBookCursorKey), last, nil); err != nil {
		return 0, nil, err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, nil, fmt.Errorf("chunk commit: %w", err)
	}
	res.Scanned += n
	res.Indexed += indexed
	res.Undecodable += undecodable
	return n, last, nil
}
