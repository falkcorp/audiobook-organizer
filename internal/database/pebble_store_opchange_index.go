// file: internal/database/pebble_store_opchange_index.go
// version: 1.6.0
// guid: 7ce04252-7ac9-421a-ba5e-5f230bbf0ab4
// last-edited: 2026-10-30

// The opchange_by_book: secondary index over the operation-change journal. Its
// sibling opchange_by_list: indexes the user-content rows by the collection or
// playlist they touch (pebble_store_opchange_list_index.go).
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
// Indexable book ids: non-empty and free of ':'. Only those ids get entries.
// A book id that contained ':' would make the key ambiguous: book "a:b" with
// row op1:c1 and book "a" with row b:op1:c1 both map to
// opchange_by_book:a:b:op1:c1, so deleting one row's entry would delete the
// other's. Without ':' in the book id the first ':' after the family prefix
// always ends the book id, so every key names exactly one (book, row) pair and
// a book's scan prefix holds that book's entries only. Every writer, the
// backfill and the verify apply the same opChangeIndexable test, and
// GetBookChanges always full-scans for an id that fails it (as for "").
//
// Invariant (completeness): for every opchange row with an indexable BookID B
// whose JSON decodes, the key opchange_by_book:B:<suffix> exists. Extras are
// allowed: the reader point-gets each row and keeps it only when the decoded
// BookID still equals the requested one, so an entry whose row is gone or has
// moved to another book costs one point read and is never returned. A MISSING
// entry would hide a row, so every writer keeps the entry in the same batch as
// the row:
//
//   - CreateOperationChange Sets the entry unconditionally (a rewrite with the
//     same BookID re-Sets the same key: still one entry), and when a stored row
//     under that id named another book, Deletes that book's entry in the same
//     batch (a BookID change moves it).
//   - MarkOperationChangesReverted re-Sets each rewritten row's entry.
//   - PruneOperationChanges Deletes each pruned row's entry.
//
// Nothing else writes the opchange: prefix. DeleteOperationWithLogs and
// DeleteOperationV2 never touch opchange rows, and Reset wipes the whole
// keyspace (index and sentinel included).
//
// TRUST GATE. This binary keeps the invariant for every row it writes, but it
// cannot vouch for rows written while it was not running: a binary that
// predates the index (a rollback) writes rows with no entry, and if a backfill
// was cut, such rows can sort before its cursor and be skipped when it
// resumes. The sentinel alone therefore does not let readers use the index.
// GetBookChanges reads the index only while the store is TRUSTED for the
// current generation, and trust is set in exactly two places:
//
//   - EnsureOpChangeByBookIndex, the startup path: after the backfill, a
//     read-only verify pass must reach the end of the journal reporting
//     MissingEntries == 0 and UnmarkedUndecodable == 0 with the sentinel set.
//     The pass is resumable and, once a pass has covered the journal, later
//     boots verify only the rows after the cursor it left
//     (pebble_store_opchange_verify.go); a pass that stops for its run's
//     deadline trusts nothing. Either count non-zero is logged at ERROR and
//     answered with a rebuild, and trust follows only the rebuild's success.
//   - RebuildOpChangeByBookIndex, which clears trust before it touches the
//     sentinel and sets it only after its sentinel commit succeeds.
//
// Until then (and after a Reset, which bumps the generation, until the next
// boot) every GetBookChanges uses the full scan, which is the pre-index
// behaviour.
//
// Undecodable rows have no readable BookID, so they cannot be indexed. The
// backfill gives each an opchange_undecodable: marker instead, and the
// indexed reader returns the decode error of any marked row that is still
// present and still undecodable, as the full scan fails on any undecodable
// row. What holds exactly: every undecodable row that existed when this
// boot's verify ran (or when the last rebuild ran) is marked, because the
// verify counts unmarked ones and a non-zero count forces a rebuild before
// trust. This binary's writers only ever write valid JSON, so a row that
// becomes undecodable later can only be corrupted by something outside them;
// such a row is invisible to the indexed reader until the next boot's verify
// finds it unmarked and rebuilds, while the scan would fail on it at once.
//
// The families sit outside the "opchange:" scan range on purpose ('_' sorts
// after ':' and after ';'), so GetBookChanges' fallback scan, GetOperationChanges
// and PruneOperationChanges never walk index keys.
//
// LOCKS. opChangeIdxRunSem admits one backfill, rebuild or startup ensure at
// a time (ctx-aware, see lockOpChangeIdxRun). opChangeJournalMu is an RWMutex
// over journal writes: CreateOperationChange and MarkOperationChangesReverted
// hold the read side from their read of the stored row to their commit, and
// each PruneOperationChanges chunk holds the write side while it re-reads and
// deletes its rows, so a row rewritten during a prune is re-checked rather
// than deleted from a stale view. Lock order: a book stripe (ModifyBook) may
// be held when the journal lock is taken, never the reverse; no journal-lock
// holder takes a book lock.
//
// The whole order of a journal write that touches a collection or playlist is:
//
//	undo.LockList (per list, taken by the revert and the carry)
//	  -> collMu / userPlaylistWriteMu (the list store's own lock)
//	    -> opChangeJournalMu, read side (CreateOperationChange(s), the mark)
//	      -> journalClock.mu (one stamp, and the ceiling write when it needs one)
//
// PruneOperationChanges holds the write side of opChangeJournalMu and never
// takes the clock. The index-pass slot (opChangeIdxRunSem, the backfill and
// rebuild passes) is not part of this order: no journal writer takes it.
// The clock mutex is never held across a batch commit
// (pebble_store_journal_clock.go, SEQUENCE), so journal writers overlap their
// fsyncs.

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metrics"
)

// opChangeIndexLog is the subsystem logger for the opchange_by_book index.
var opChangeIndexLog = logger.New("database.opchange-index")

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

// opChangeIdxWaitHeartbeat is how often lockOpChangeIdxRun reports progress
// while it waits for another index pass to finish. A var only so tests can
// shorten it.
var opChangeIdxWaitHeartbeat = 30 * time.Second

// OpChangeIndexProgress receives progress from the long opchange index passes.
// phase is "waiting" (queued behind another index pass), "backfill" (the
// ensure's resumable first build), "verify" or "rebuild"; rows is how many
// journal rows the pass has read so far. It is
// called once per backfill chunk, every opChangeVerifyProgressRows rows of a
// verify, and every opChangeIdxWaitHeartbeat while waiting. It may be nil.
type OpChangeIndexProgress func(phase string, rows int)

// opChangeVerifyProgressRows is how often a verify reports progress (and
// checks its context). A var only so tests can see a verify report without
// writing 10,000 rows; never reassign in prod code.
var opChangeVerifyProgressRows = 10_000

// opChangeByBookBackfillAfterChunk, when non-nil, runs after each durable
// chunk commit with the run's commit count so far. A non-nil return aborts the
// run as if the process had been cut at that point. Test-only; nil in
// production.
var opChangeByBookBackfillAfterChunk func(commits int) error

// opChangeKey is the primary key of one journal row.
func opChangeKey(operationID, changeID string) []byte {
	return []byte(opChangeKeyPrefix + operationID + ":" + changeID)
}

// opChangeIndexable reports whether rows naming bookID get index entries:
// the id must be non-empty and free of ':' (see the file comment for why a ':'
// would make index keys collide). Every writer, the backfill, the verify and
// GetBookChanges use this one test.
func opChangeIndexable(bookID string) bool {
	return bookID != "" && !strings.Contains(bookID, ":")
}

// opChangeByBookKey is the index key for the row at primary key primary,
// filed under bookID. Callers must have checked opChangeIndexable(bookID).
func opChangeByBookKey(bookID string, primary []byte) []byte {
	suffix := primary[len(opChangeKeyPrefix):]
	k := make([]byte, 0, len(opChangeByBookPrefix)+len(bookID)+1+len(suffix))
	k = append(k, opChangeByBookPrefix...)
	k = append(k, bookID...)
	k = append(k, ':')
	return append(k, suffix...)
}

// opChangeByBookBookPrefix is the scan prefix holding bookID's entries. For an
// indexable bookID (no ':') no other book's entry can share it.
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

// stageOpChangeIndex adds the row's index entry to batch. Rows whose book id
// is not indexable get none (GetBookChanges scans for those ids).
func stageOpChangeIndex(batch *GatedBatch, bookID string, primary []byte) error {
	if !opChangeIndexable(bookID) {
		return nil
	}
	if err := batch.Set(opChangeByBookKey(bookID, primary), nil, nil); err != nil {
		return fmt.Errorf("stage opchange_by_book entry: %w", err)
	}
	return nil
}

// unstageOpChangeIndex adds the deletion of the row's index entry to batch.
func unstageOpChangeIndex(batch *GatedBatch, bookID string, primary []byte) error {
	if !opChangeIndexable(bookID) {
		return nil
	}
	if err := batch.Delete(opChangeByBookKey(bookID, primary), nil); err != nil {
		return fmt.Errorf("stage opchange_by_book delete: %w", err)
	}
	return nil
}

// opChangeByBookIndexBuilt reports whether the backfill sentinel exists. A
// positive read is cached against the current generation, so a long-lived
// process picks up a completion without a restart, and a rebuild (which bumps
// the generation after its sentinel delete commits) invalidates every cached
// positive, including one a reader stored from a Get that raced the delete:
// that reader stored the old generation, which no longer matches. A read
// error is returned, never guessed at.
func (p *PebbleStore) opChangeByBookIndexBuilt() (bool, error) {
	gen := p.opChangeByBookGen.Load()
	if p.opChangeByBookBuiltAt.Load() == gen+1 {
		return true, nil
	}
	_, closer, err := p.db.Get([]byte(opChangeByBookBackfillKey))
	switch {
	case err == nil:
		closer.Close()
		p.opChangeByBookBuiltAt.Store(gen + 1)
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("read %s: %w", opChangeByBookBackfillKey, err)
	}
}

// opChangeByBookIndexTrusted reports whether readers may use the index: this
// process verified it (or rebuilt it) for the current generation. See the
// TRUST GATE section of the file comment. No I/O.
func (p *PebbleStore) opChangeByBookIndexTrusted() bool {
	return p.opChangeByBookTrustedAt.Load() == p.opChangeByBookGen.Load()+1
}

// publishOpChangeTrust mirrors the trust state into the
// opchange_by_book_index_trusted gauge. Call it after every change to
// opChangeByBookTrustedAt or opChangeByBookGen. The gauge is process-global,
// so with several stores in one process (tests) the last writer wins.
func (p *PebbleStore) publishOpChangeTrust() {
	metrics.SetOpChangeByBookIndexTrusted(p.opChangeByBookIndexTrusted())
}

// opChangeByBookIndexUsable is GetBookChanges' gate: trusted this boot AND the
// sentinel present. Trust is never set without the sentinel, and every path
// that deletes the sentinel clears trust first, so the sentinel read is a
// second guard, not the first.
func (p *PebbleStore) opChangeByBookIndexUsable() (bool, error) {
	if !p.opChangeByBookIndexTrusted() {
		return false, nil
	}
	return p.opChangeByBookIndexBuilt()
}

// OpChangeByBookIndexUsable reports whether GetBookChanges reads the
// opchange_by_book index right now (trusted this boot and the sentinel
// present) rather than scanning every opchange row. A caller choosing
// between one GetBookChanges per book and one full ScanOperationChanges pass
// asks this. A failed sentinel read answers false (the scan is never wrong).
func (p *PebbleStore) OpChangeByBookIndexUsable() bool {
	ok, err := p.opChangeByBookIndexUsable()
	return err == nil && ok
}

// lockOpChangeIdxRun takes the one-at-a-time slot for index passes, giving up
// when ctx ends. While it waits it reports phase "waiting" every
// opChangeIdxWaitHeartbeat, so an op queued behind a long startup verify or
// rebuild keeps its liveness clock fresh. The returned func releases the slot.
func (p *PebbleStore) lockOpChangeIdxRun(ctx context.Context, progress OpChangeIndexProgress) (func(), error) {
	p.opChangeIdxRunOnce.Do(func() { p.opChangeIdxRunSem = make(chan struct{}, 1) })
	release := func() { <-p.opChangeIdxRunSem }
	select {
	case p.opChangeIdxRunSem <- struct{}{}:
		return release, nil
	default:
	}
	tick := time.NewTicker(opChangeIdxWaitHeartbeat)
	defer tick.Stop()
	for {
		select {
		case p.opChangeIdxRunSem <- struct{}{}:
			return release, nil
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for another opchange_by_book index pass: %w", ctx.Err())
		case <-tick.C:
			if progress != nil {
				progress("waiting", 0)
			}
		}
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

// getBookChangesIndexed serves GetBookChanges from the index. bookID must be
// indexable. The marker
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
			continue // stale entry: the row moved to another book
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
func opChangeUndecodableGate(r gatedReader) error {
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
// its sentinel, resuming from the cursor an interrupted run left. It never
// sets trust: readers stay on the full scan until EnsureOpChangeByBookIndex
// (the startup path, which calls this and then verifies) or a rebuild trusts
// the index.
func (p *PebbleStore) BackfillOpChangeByBookIndex(ctx context.Context) (OpChangeByBookBackfillResult, error) {
	unlock, err := p.lockOpChangeIdxRun(ctx, nil)
	if err != nil {
		return OpChangeByBookBackfillResult{}, err
	}
	defer unlock()
	return p.backfillOpChangeByBook(ctx, false, nil)
}

// RebuildOpChangeByBookIndex is the rollback runbook's repair: it clears
// trust, deletes the sentinel and the cursor in one batch, so every
// GetBookChanges falls back to the full scan, then rebuilds from the first row,
// sets the sentinel again and, only on success, trusts the index. If it is cut,
// the next startup ensure resumes it from its cursor and verifies before
// trusting. It only ever Sets entries and markers, so it is safe on a live
// store. progress (may be nil) is called once per committed chunk, and while
// waiting behind another index pass.
func (p *PebbleStore) RebuildOpChangeByBookIndex(ctx context.Context, progress OpChangeIndexProgress) (OpChangeByBookBackfillResult, error) {
	unlock, err := p.lockOpChangeIdxRun(ctx, progress)
	if err != nil {
		return OpChangeByBookBackfillResult{}, err
	}
	defer unlock()
	return p.backfillOpChangeByBook(ctx, true, progress)
}

// OpChangeByBookEnsureResult summarises one EnsureOpChangeByBookIndex run.
type OpChangeByBookEnsureResult struct {
	Backfill OpChangeByBookBackfillResult `json:"backfill"`
	Verify   OpChangeByBookIndexReport    `json:"verify"`
	// Rebuilt is true when the verify found missing entries or unmarked
	// undecodable rows and a rebuild ran; Rebuild is its result.
	Rebuilt bool                         `json:"rebuilt"`
	Rebuild OpChangeByBookBackfillResult `json:"rebuild"`
	// Trusted is true when GetBookChanges may now read the index.
	Trusted bool `json:"trusted"`
	// Yielded is true when the verify stopped for the run's deadline with its
	// cursor durable and the index NOT yet trusted: the caller is expected to
	// run the ensure again (the op returns registry.ErrContinueInPlace), and
	// that run continues from the cursor.
	Yielded bool `json:"yielded,omitempty"`
}

// EnsureOpChangeByBookIndex is EnsureOpChangeByBookIndexWith with only a
// progress callback: it never yields, so the verify must finish in this call.
func (p *PebbleStore) EnsureOpChangeByBookIndex(ctx context.Context, progress OpChangeIndexProgress) (OpChangeByBookEnsureResult, error) {
	return p.EnsureOpChangeByBookIndexWith(ctx, OpChangeEnsureOptions{Progress: progress})
}

// EnsureOpChangeByBookIndexWith is the startup path (the boot-enqueued
// maintenance.opchange-book-index-rebuild {"ensure": true} run): it runs (or
// resumes, or skips) the one-time backfill, then a resumable verify, and trusts
// the index only when a verify pass has reached the end of the journal with the
// sentinel set, no missing entry and no unmarked undecodable row. Otherwise it
// logs at ERROR, rebuilds, and trusts the index only if the rebuild succeeds. It
// holds the index-pass slot throughout, so a rebuild op cannot interleave with
// it. opts.Progress (may be nil) is passed to every step: the slot wait, the
// backfill, the verify and the rebuild, so an op running it keeps its liveness
// clock fresh.
//
// The verify is incremental (pebble_store_opchange_verify.go): it continues from
// the cursor an earlier run left, so a journal too big to verify inside one
// run's Timeout is finished across runs, and once a pass has covered the
// journal a later boot checks only what was appended since. With
// opts.YieldMargin set it stops before the deadline at a durable cursor and the
// result reports Yielded, with Trusted false and a nil error.
//
// The generation is read once, under the slot, before the verify starts, and
// trust is stored against that value: a Reset during the verify bumps the
// generation, so the stored trust no longer matches and readers stay on the
// scan.
func (p *PebbleStore) EnsureOpChangeByBookIndexWith(ctx context.Context, opts OpChangeEnsureOptions) (OpChangeByBookEnsureResult, error) {
	var out OpChangeByBookEnsureResult
	progress := opts.Progress
	unlock, err := p.lockOpChangeIdxRun(ctx, progress)
	if err != nil {
		return out, err
	}
	defer unlock()

	out.Backfill, err = p.backfillOpChangeByBook(ctx, false, progress)
	if err != nil {
		return out, err
	}
	gen := p.opChangeByBookGen.Load()
	if !out.Backfill.Skipped {
		// The index was (re)built in this call, or a cut build was resumed: a
		// verify cursor from before it vouches for an index that no longer
		// exists (a Reset wipes the sentinel and the journal, and a stale cursor
		// would make the next verify skip rows it never looked at).
		if err := p.clearOpChangeVerifyState(); err != nil {
			return out, fmt.Errorf("clear opchange_by_book verify state: %w", err)
		}
	}
	out.Verify, err = p.verifyOpChangeByBookResumable(ctx, opts, gen)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			opChangeIndexLog.Info("opchange-index-ensure: verify interrupted by shutdown; GetBookChanges reads the full scan until the next boot verifies: err=%s",
				logger.SanitizeLogValue(err.Error()))
			return out, err
		}
		opChangeIndexLog.Error("opchange-index-ensure: verify failed; GetBookChanges stays on the full scan: err=%s", logger.SanitizeLogValue(err.Error()))
		return out, err
	}
	rep := out.Verify
	if rep.Yielded {
		out.Yielded = true
		logOpChangeVerifyYield(rep)
		return out, nil
	}
	if rep.SentinelSet && rep.Complete && rep.MissingEntries == 0 && rep.UnmarkedUndecodable == 0 {
		p.opChangeByBookTrustedAt.Store(gen + 1)
		p.publishOpChangeTrust()
		out.Trusted = p.opChangeByBookIndexTrusted()
		opChangeIndexLog.Info("opchange-index-ensure: index verified, readers now use it: rows_this_run=%d verified_rows=%d resumed_after=%s indexable=%d undecodable=%d trusted=%t",
			rep.Rows, rep.VerifiedRows, logger.SanitizeLogValue(rep.ResumedAfter), rep.Indexable, rep.Undecodable, out.Trusted)
		return out, nil
	}
	opChangeIndexLog.Error("opchange-index-ensure: index does not cover the journal (rows written without entries, "+
		"e.g. by a binary that predates the index); rebuilding, GetBookChanges stays on the full scan until it finishes: "+
		"sentinel_set=%t missing_entries=%d unmarked_undecodable=%d (a lower bound: the verify stops at the first window with one) sample_missing=%s",
		rep.SentinelSet, rep.MissingEntries, rep.UnmarkedUndecodable,
		logger.SanitizeLogValue(fmt.Sprintf("%v", rep.SampleMissing)))
	out.Rebuilt = true
	out.Rebuild, err = p.backfillOpChangeByBook(ctx, true, progress)
	if err != nil {
		return out, err
	}
	out.Trusted = p.opChangeByBookIndexTrusted()
	return out, nil
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
// Trust: a non-force run never sets it. A force run (rebuild) zeroes it
// before the sentinel delete and sets it after the sentinel commit succeeds.
//
// The caller must hold the index-pass slot (lockOpChangeIdxRun).
func (p *PebbleStore) backfillOpChangeByBook(ctx context.Context, force bool, progress OpChangeIndexProgress) (OpChangeByBookBackfillResult, error) {
	var res OpChangeByBookBackfillResult
	start := time.Now()
	mode := "backfill"
	if force {
		mode = "rebuild"
	}

	var forceGen uint64 // the generation a rebuild bumped to; see the done batch
	if force {
		// Readers leave the index before the sentinel goes: the gate checks
		// trust first.
		p.opChangeByBookTrustedAt.Store(0)
		p.publishOpChangeTrust()
		reset := p.db.NewBatch()
		if err := reset.Delete([]byte(opChangeByBookBackfillKey), nil); err != nil {
			reset.Close()
			return res, err
		}
		if err := reset.Delete([]byte(opChangeByBookCursorKey), nil); err != nil {
			reset.Close()
			return res, err
		}
		// The verify's cursor and verified sentinel go with the index they
		// vouched for (pebble_store_opchange_verify.go).
		for _, k := range []string{opChangeByBookVerifyCursorKey, opChangeByBookVerifiedKey} {
			if err := reset.Delete([]byte(k), nil); err != nil {
				reset.Close()
				return res, err
			}
		}
		err := reset.Commit(pebble.Sync)
		reset.Close()
		if err != nil {
			return res, fmt.Errorf("clear opchange_by_book sentinel: %w", err)
		}
		// Bump the generation only after the delete is durable: a reader
		// that Got the sentinel before the commit cached the old generation,
		// which this invalidates, and every later read misses the sentinel.
		// A call already inside getBookChangesIndexed finishes on an index
		// that is no worse than it was before the rebuild started.
		forceGen = p.opChangeByBookGen.Add(1)
	} else {
		built, err := p.opChangeByBookIndexBuilt()
		if err != nil {
			opChangeIndexLog.Error("opchange-index-backfill: cannot read sentinel, aborting: err=%s", logger.SanitizeLogValue(err.Error()))
			return res, err
		}
		if built {
			opChangeIndexLog.Info("opchange-index-backfill: already complete, skipping: sentinel=%s", opChangeByBookBackfillKey)
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
	opChangeIndexLog.Info("opchange-index-backfill: starting: mode=%s chunk=%d resumed_after=%s",
		mode, opChangeByBookBackfillChunk, logger.SanitizeLogValue(res.ResumedAfter))

	upper := []byte(opChangeScanUpperBound)
	chunk := max(opChangeByBookBackfillChunk, 1)
	nextLog := opChangeByBookLogEveryRows
	var lastKey []byte // the last row any chunk of this run read
	for {
		if err := ctx.Err(); err != nil {
			opChangeIndexLog.Warn("opchange-index-backfill: canceled, sentinel NOT set; resumes next run: scanned=%d commits=%d",
				res.Scanned, res.Commits)
			return res, err
		}
		n, last, err := p.opChangeBackfillChunk(lower, upper, chunk, &res)
		if err != nil {
			opChangeIndexLog.Error("opchange-index-backfill: failed, sentinel NOT set: mode=%s scanned=%d commits=%d err=%s",
				mode, res.Scanned, res.Commits, logger.SanitizeLogValue(err.Error()))
			return res, err
		}
		if n == 0 {
			break
		}
		lastKey = append(lastKey[:0], last...)
		res.Commits++
		if progress != nil {
			progress(mode, res.Scanned)
		}
		if res.Scanned >= nextLog {
			opChangeIndexLog.Info("opchange-index-backfill: progress: scanned=%d indexed=%d elapsed=%s",
				res.Scanned, res.Indexed, time.Since(start).Round(time.Second).String())
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
	if force {
		// A rebuild visited every row after clearing trust and the verify state
		// above, so it covers the journal as a verify pass would: record the
		// last row it read as the verify cursor, and the next boot looks only
		// at the rows after it. The generation check is the verify's own: a
		// Reset since the clear wiped the journal and the index with it.
		if p.opChangeByBookGen.Load() == forceGen {
			st := opChangeVerifyState{Rows: res.Scanned, Started: opChangeVerifyNow()}
			if len(lastKey) > 0 {
				st.Cursor = string(lastKey)
			}
			data, err := json.Marshal(st)
			if err != nil {
				return res, err
			}
			if err := done.Set([]byte(opChangeByBookVerifyCursorKey), data, nil); err != nil {
				return res, err
			}
			if err := done.Set([]byte(opChangeByBookVerifiedKey), []byte("1"), nil); err != nil {
				return res, err
			}
		}
	}
	// Every chunk committed with pebble.Sync before this, so every entry the
	// sentinel vouches for is already durable.
	if err := done.Commit(pebble.Sync); err != nil {
		opChangeIndexLog.Error("opchange-index-backfill: sentinel commit failed: scanned=%d err=%s", res.Scanned, logger.SanitizeLogValue(err.Error()))
		return res, err
	}
	res.Commits++
	gen := p.opChangeByBookGen.Load()
	p.opChangeByBookBuiltAt.Store(gen + 1)
	if force {
		// The rebuild visited every row after clearing trust, and every row
		// written since was written by this binary with its entry, so the
		// index is complete for this generation.
		p.opChangeByBookTrustedAt.Store(gen + 1)
		p.publishOpChangeTrust()
	}
	if res.Undecodable > 0 {
		opChangeIndexLog.Error("opchange-index-backfill: opchange rows cannot be decoded; GetBookChanges fails "+
			"for every book until each is rewritten or removed, as it did before the index: undecodable=%d",
			res.Undecodable)
	}
	opChangeIndexLog.Info("opchange-index-backfill: complete: mode=%s scanned=%d indexed=%d undecodable=%d commits=%d duration=%s",
		mode, res.Scanned, res.Indexed, res.Undecodable, res.Commits,
		time.Since(start).Round(time.Millisecond).String())
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
		} else if opChangeIndexable(c.BookID) {
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

// opChangeByBookSampleCap bounds the sample list in the verify report.
const opChangeByBookSampleCap = 50

// OpChangeByBookIndexReport is the result of VerifyOpChangeByBookIndex.
type OpChangeByBookIndexReport struct {
	SentinelSet bool `json:"sentinel_set"`
	Rows        int  `json:"rows"`
	// Indexable rows decode and name an indexable book (non-empty, no ':'),
	// so they must have an entry.
	Indexable int `json:"indexable"`
	// MissingEntries are indexable rows with no entry: an index read would
	// not see them, so a non-zero count keeps readers on the scan and forces
	// a rebuild (EnsureOpChangeByBookIndex). This is the count a rebuild fixes.
	MissingEntries int `json:"missing_entries"`
	// Undecodable rows; UnmarkedUndecodable have no opchange_undecodable:
	// marker, so the indexed reader does not fail closed on them.
	Undecodable         int `json:"undecodable"`
	UnmarkedUndecodable int `json:"unmarked_undecodable"`
	// SampleMissing holds up to opChangeByBookSampleCap primary keys.
	SampleMissing []string `json:"sample_missing,omitempty"`

	// The fields below describe a resumable verify (the boot ensure's). The
	// read-only VerifyOpChangeByBookIndex walks every row, so there Rows ==
	// VerifiedRows, ResumedAfter is empty and Complete is true on success.
	//
	// ResumedAfter is the cursor (a primary key) the walk began after; empty
	// when it began at the first row. Rows counts the rows THIS run read.
	// VerifiedRows is the position: rows earlier runs checked plus Rows.
	ResumedAfter string `json:"resumed_after,omitempty"`
	VerifiedRows int    `json:"verified_rows"`
	// PriorCoverage is true when an earlier pass had reached the end of the
	// journal (the verified sentinel), so this run was a tail verify of the rows
	// appended since.
	PriorCoverage bool `json:"prior_coverage,omitempty"`
	// Complete is true when the walk reached the end of the journal clean. Only
	// a Complete verify lets the ensure trust the index.
	Complete bool `json:"complete"`
	// Yielded is true when the walk stopped for the run's deadline with its
	// cursor durable (see OpChangeEnsureOptions.YieldMargin).
	Yielded bool `json:"yielded,omitempty"`
}

// VerifyOpChangeByBookIndex checks every journal row against the index from
// one snapshot. Read-only; it neither takes the index-pass slot nor changes
// trust. One sequential pass with a point read per row: the work is Pebble
// reads in a single instance, as in the backfill. progress (may be nil) is
// called every opChangeVerifyProgressRows rows.
func (p *PebbleStore) VerifyOpChangeByBookIndex(ctx context.Context, progress OpChangeIndexProgress) (OpChangeByBookIndexReport, error) {
	return p.verifyOpChangeByBook(ctx, progress)
}
