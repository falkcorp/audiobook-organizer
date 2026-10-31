// file: internal/database/pebble_store_opchange_verify.go
// version: 1.0.0
// guid: 95b3e380-e3be-4545-a68c-842559a84058
// last-edited: 2026-10-30

// The resumable verify of the opchange_by_book: index (TODO 04-P7).
//
// WHY. The boot ensure verifies every journal row against the index before it
// trusts the index. The journal is never pruned by log retention, so that walk
// only grows, and it used to restart from the first row on every boot and had
// to finish inside the one run's Timeout. Past the Timeout every boot ended red
// and readers stayed on the full scan for good. The backfill and the rebuild
// were already resumable (a cursor committed with each chunk); this file makes
// the verify resumable the same way and, once a pass has covered the journal,
// lets later boots look only at what was appended since.
//
// STATE. Two keys, both under system:backfill:, both cleared by a rebuild
// (backfillOpChangeByBook, force) and by Reset (which wipes the keyspace):
//
//	opchange_by_book_verify_v1_cursor  JSON {cursor, rows, started}: every row
//	    at or before the primary key `cursor` was checked clean, `rows` of them,
//	    and the walk that began at the first row began at `started`.
//	opchange_by_book_verify_v1_done    "1": a verify pass has reached the end of
//	    the journal since the last reset. The cursor is then the high-water
//	    mark the next boot's tail verify starts from.
//
// The in-memory trust (opChangeByBookTrustedAt) is NOT persisted. A boot trusts
// the index only when THIS boot's pass reached the end of the journal, whether
// the pass began at the first row (a first verify, or a periodic full one) or
// at the cursor (the tail). A cursor therefore never lets a boot trust rows it
// did not look at: it only lets it skip rows an earlier boot already checked.
//
// IS THE JOURNAL APPEND-ONLY? Not in key order, so the tail verify is bounded
// rather than exact, and this is stated here because it is the one place the
// design could be read as stronger than it is:
//
//   - This binary only ever adds a row (CreateOperationChange), rewrites one in
//     place under the same key with its entry (the mark, a void, a BookID move
//     that also moves the entry), or removes one (PruneOperationChanges, with
//     its entry). A row the cursor has passed stays checked: every rewrite
//     keeps the invariant in its own batch.
//   - A row written while this binary was NOT running (a rollback to a build
//     that predates the index) is what the verify exists to find. Its key is
//     opchange:<opID>:<changeID>, and an opID is a ULID, or a ULID behind a
//     fixed prefix (opswriter.BookHistory.NewBatchID). ULIDs sort by time, so a
//     later row sorts after an earlier one WITHIN a prefix, but a row with a
//     lexically smaller prefix sorts BEFORE the cursor. The tail verify cannot
//     see such a row.
//   - So the cursor is not trusted for ever: a pass older than
//     opChangeVerifyFullEvery (7 days) is dropped and the next ensure verifies
//     from the first row again. That bounds how long a below-cursor row from a
//     rollback can sit unnoticed, at the cost of one full (resumable) verify a
//     week. A forced rebuild (dry_run=false) re-indexes every row at once.
//
// THE YIELD. A run that is about to hit its Timeout stops after a durable
// cursor and reports Yielded; the maintenance op turns that into
// registry.ErrContinueInPlace, so the SAME operation is re-queued with a fresh
// Timeout and the next attempt continues from the cursor. It is the pattern
// backfill-file-hashes and the 04-P6 boot ops (internal/walkresume,
// fix/boot-ops-first-run-resumable) use: margin = max(YieldMargin, twice the
// slowest gap between checkpoints), and never before this attempt has made
// durable progress. The cursor lives in the store rather than in the registry's
// checkpoint blob because the next boot's NEW operation must find it too, and
// because a key-ordered Pebble iterator needs no sorted collection (the reason
// walkresume sorts file paths first).

package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

const (
	opChangeByBookVerifyCursorKey = "system:backfill:opchange_by_book_verify_v1_cursor"
	opChangeByBookVerifiedKey     = "system:backfill:opchange_by_book_verify_v1_done"
)

// opChangeVerifyFullEvery is how long a verify's coverage is trusted to stand
// in for a fresh full walk (see "IS THE JOURNAL APPEND-ONLY?"). A var only so
// tests can shorten it; never reassign in prod code.
var opChangeVerifyFullEvery = 7 * 24 * time.Hour

// opChangeVerifyNow is the clock the periodic full verify reads. Test-only
// override; time.Now in production.
var opChangeVerifyNow = time.Now

// errOpChangeStoreReset ends a verify whose store was Reset underneath it, so
// it writes no cursor into the wiped keyspace.
var errOpChangeStoreReset = errors.New("opchange_by_book verify: the store was reset during the pass")

// OpChangeEnsureOptions configures EnsureOpChangeByBookIndexWith.
type OpChangeEnsureOptions struct {
	// Progress is as EnsureOpChangeByBookIndex's. The verify reports rows as an
	// absolute position: rows earlier runs already checked plus this run's.
	Progress OpChangeIndexProgress
	// VerifyTotal, when non-nil, is called once per verify pass, before its
	// first row, with the number of rows the whole pass covers (rows an earlier
	// run checked plus rows ahead of the cursor), so a caller can report a
	// determinate progress. It costs a key-order count of the rows ahead.
	VerifyTotal func(total int)
	// YieldMargin, when positive and ctx has a deadline, makes the verify stop
	// at a durable cursor once less than max(YieldMargin, 2 x the slowest gap
	// between checkpoints) of the deadline is left. Zero never yields.
	YieldMargin time.Duration
}

// opChangeVerifyState is the persisted resume point of the verify.
type opChangeVerifyState struct {
	Cursor  string    `json:"cursor,omitempty"`
	Rows    int       `json:"rows"`
	Started time.Time `json:"started"`
}

// readOpChangeVerifyState returns the persisted resume point. A record that is
// unreadable or does not name an opchange: key is dropped, not trusted: the
// verify then starts from the first row, which is always correct.
func (p *PebbleStore) readOpChangeVerifyState() (opChangeVerifyState, bool, error) {
	var st opChangeVerifyState
	v, closer, err := p.db.Get([]byte(opChangeByBookVerifyCursorKey))
	if errors.Is(err, pebble.ErrNotFound) {
		return st, false, nil
	}
	if err != nil {
		return st, false, fmt.Errorf("read %s: %w", opChangeByBookVerifyCursorKey, err)
	}
	raw := bytes.Clone(v)
	closer.Close()
	if err := json.Unmarshal(raw, &st); err != nil || (st.Cursor != "" && !bytes.HasPrefix([]byte(st.Cursor), []byte(opChangeKeyPrefix))) || st.Rows < 0 {
		opChangeIndexLog.Warn("opchange-index-verify: ignoring an unreadable verify cursor; verifying from the first row")
		return opChangeVerifyState{}, false, nil
	}
	return st, true, nil
}

// opChangeVerifiedSentinel reports whether a verify pass has reached the end of
// the journal since the last reset.
func (p *PebbleStore) opChangeVerifiedSentinel() (bool, error) {
	_, closer, err := p.db.Get([]byte(opChangeByBookVerifiedKey))
	switch {
	case err == nil:
		closer.Close()
		return true, nil
	case errors.Is(err, pebble.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("read %s: %w", opChangeByBookVerifiedKey, err)
	}
}

// clearOpChangeVerifyState drops the cursor and the verified sentinel.
func (p *PebbleStore) clearOpChangeVerifyState() error {
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Delete([]byte(opChangeByBookVerifyCursorKey), nil); err != nil {
		return err
	}
	if err := b.Delete([]byte(opChangeByBookVerifiedKey), nil); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// saveOpChangeVerifyState commits the resume point, and with done also the
// verified sentinel, in one Sync batch. It refuses when the store was reset
// since gen was read (see errOpChangeStoreReset).
func (p *PebbleStore) saveOpChangeVerifyState(gen uint64, st opChangeVerifyState, done bool) error {
	if p.opChangeByBookGen.Load() != gen {
		return errOpChangeStoreReset
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set([]byte(opChangeByBookVerifyCursorKey), data, nil); err != nil {
		return err
	}
	if done {
		if err := b.Set([]byte(opChangeByBookVerifiedKey), []byte("1"), nil); err != nil {
			return err
		}
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit opchange_by_book verify cursor: %w", err)
	}
	return nil
}

// opChangeVerifyYieldDue reports whether a run with this much deadline left
// should stop at its checkpoint. No deadline, or no margin, never yields.
func opChangeVerifyYieldDue(ctx context.Context, margin, slowestGap time.Duration) bool {
	if margin <= 0 {
		return false
	}
	dl, ok := ctx.Deadline()
	if !ok {
		return false
	}
	return time.Until(dl) < max(margin, 2*slowestGap)
}

// opChangeVerifyHook runs at every progress boundary of a verify, after last (a
// primary key) and every row before it were checked. It returns true to stop
// the walk there.
type opChangeVerifyHook func(rep *OpChangeByBookIndexReport, last []byte) (stop bool, err error)

// opChangeVerifyRun is one walk over the journal.
type opChangeVerifyRun struct {
	// from is the cursor: rows at or before it are not read. nil starts at the
	// first row.
	from []byte
	// baseRows is how many rows earlier runs checked, so progress is absolute.
	baseRows int
	hook     opChangeVerifyHook
	progress OpChangeIndexProgress
	// total, when non-nil, receives baseRows + the rows ahead of from.
	total func(total int)
}

// opChangeVerifyOutcome is what a walk learned beyond the report.
type opChangeVerifyOutcome struct {
	// last is the last row fully checked, nil when none was.
	last []byte
	// reachedEnd is true when the walk read the journal to its end (as opposed
	// to being stopped by its hook).
	reachedEnd bool
}

// verifyOpChangeByBook is the read-only full verify (the preview and the
// operator's check): every journal row, from one snapshot, no state read or
// written.
func (p *PebbleStore) verifyOpChangeByBook(ctx context.Context, progress OpChangeIndexProgress) (OpChangeByBookIndexReport, error) {
	built, err := p.opChangeByBookIndexBuilt()
	if err != nil {
		return OpChangeByBookIndexReport{}, err
	}
	rep, out, err := p.verifyOpChangeRange(ctx, opChangeVerifyRun{progress: progress})
	rep.SentinelSet = built
	rep.VerifiedRows = rep.Rows
	rep.Complete = err == nil && out.reachedEnd && rep.MissingEntries == 0 && rep.UnmarkedUndecodable == 0
	return rep, err
}

// verifyOpChangeRange walks the journal after run.from from one snapshot and
// checks every row against the index. One sequential pass with a point read per
// row: the work is Pebble reads in a single instance, as in the backfill. It
// checks ctx and calls progress and run.hook every opChangeVerifyProgressRows
// rows.
func (p *PebbleStore) verifyOpChangeRange(ctx context.Context, run opChangeVerifyRun) (OpChangeByBookIndexReport, opChangeVerifyOutcome, error) {
	var (
		rep OpChangeByBookIndexReport
		out opChangeVerifyOutcome
	)
	snap := p.db.NewSnapshot()
	defer snap.Close()
	lower := []byte(opChangeKeyPrefix)
	if len(run.from) > 0 {
		lower = append(bytes.Clone(run.from), 0) // the first key strictly after the cursor
	}
	bounds := &pebble.IterOptions{LowerBound: lower, UpperBound: []byte(opChangeScanUpperBound)}

	if run.total != nil {
		ahead, err := countOpChangeRows(ctx, snap, bounds, run.progress)
		if err != nil {
			return rep, out, err
		}
		run.total(run.baseRows + ahead)
	}

	iter, err := snap.NewIter(bounds)
	if err != nil {
		return rep, out, err
	}
	defer iter.Close()
	present := func(key []byte) (bool, error) {
		_, closer, err := snap.Get(key)
		if errors.Is(err, pebble.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		closer.Close()
		return true, nil
	}
	checkRow := func(k, v []byte) error {
		var c OperationChange
		if json.Unmarshal(v, &c) != nil {
			rep.Undecodable++
			ok, err := present(opChangeUndecodableKey(k))
			if err != nil {
				return err
			}
			if !ok {
				rep.UnmarkedUndecodable++
			}
			return nil
		}
		if !opChangeIndexable(c.BookID) {
			return nil
		}
		rep.Indexable++
		ok, err := present(opChangeByBookKey(c.BookID, k))
		if err != nil {
			return err
		}
		if !ok {
			rep.MissingEntries++
			if len(rep.SampleMissing) < opChangeByBookSampleCap {
				rep.SampleMissing = append(rep.SampleMissing, string(k))
			}
		}
		return nil
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if rep.Rows%opChangeVerifyProgressRows == 0 {
			if err := ctx.Err(); err != nil {
				return rep, out, err
			}
			if rep.Rows > 0 {
				if run.progress != nil {
					run.progress("verify", run.baseRows+rep.Rows)
				}
				if run.hook != nil {
					stop, err := run.hook(&rep, out.last)
					if err != nil {
						return rep, out, err
					}
					if stop {
						return rep, out, nil
					}
				}
			}
		}
		rep.Rows++
		k := iter.Key()
		if err := checkRow(k, iter.Value()); err != nil {
			return rep, out, err
		}
		out.last = append(out.last[:0], k...)
	}
	if err := iter.Error(); err != nil {
		return rep, out, err
	}
	out.reachedEnd = true
	return rep, out, nil
}

// countOpChangeRows counts the journal rows in bounds from snap. It reads keys
// only (no decode, no point read), so it costs a small fraction of the verify
// it sizes. progress (may be nil) hears phase "count" every
// opChangeVerifyProgressRows rows so the op's liveness clock stays fresh.
func countOpChangeRows(ctx context.Context, snap gatedReader, bounds *pebble.IterOptions, progress OpChangeIndexProgress) (int, error) {
	iter, err := snap.NewIter(bounds)
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		if n%opChangeVerifyProgressRows == 0 {
			if err := ctx.Err(); err != nil {
				return n, err
			}
			if progress != nil && n > 0 {
				progress("count", n)
			}
		}
		n++
	}
	return n, iter.Error()
}

// countLiveOpChangeUndecodable counts the opchange_undecodable: markers whose
// row is still present and still undecodable: the undecodable rows of the whole
// journal, which a tail verify (that read only the rows after its cursor) can
// not count itself. The marker family is complete for every row a clean verify
// has covered (it counts unmarked ones and a non-zero count forces a rebuild).
func (p *PebbleStore) countLiveOpChangeUndecodable() (int, error) {
	lower := []byte(opChangeUndecodablePrefix)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: prefixEnd(lower)})
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		primary := append([]byte(opChangeKeyPrefix), iter.Key()[len(lower):]...)
		v, closer, err := p.db.Get(primary)
		if errors.Is(err, pebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return 0, err
		}
		var c OperationChange
		bad := json.Unmarshal(v, &c) != nil
		closer.Close()
		if bad {
			n++
		}
	}
	return n, iter.Error()
}

// verifyOpChangeByBookResumable is the verify of the boot ensure. It reads the
// resume point, walks from it to the end of the journal (or until the deadline
// says to yield), moves the cursor with each checkpoint, and on reaching the
// end with a clean report records the pass as complete. The caller holds the
// index-pass slot and read gen under it.
//
// The report is clean-or-not for the rows it read: MissingEntries or
// UnmarkedUndecodable above zero stops the walk at its next boundary (the
// rebuild that follows re-indexes everything and resets the resume point, so
// finishing the walk would only delay it), and the counts are then a lower
// bound.
func (p *PebbleStore) verifyOpChangeByBookResumable(ctx context.Context, opts OpChangeEnsureOptions, gen uint64) (OpChangeByBookIndexReport, error) {
	var rep OpChangeByBookIndexReport
	built, err := p.opChangeByBookIndexBuilt()
	if err != nil {
		return rep, err
	}
	rep.SentinelSet = built
	if !built {
		// Not reachable after a backfill that returned nil; answer "not
		// verified" and let the caller rebuild.
		return rep, nil
	}
	st, have, err := p.readOpChangeVerifyState()
	if err != nil {
		return rep, err
	}
	rep.PriorCoverage, err = p.opChangeVerifiedSentinel()
	if err != nil {
		return rep, err
	}
	now := opChangeVerifyNow()
	if have && now.Sub(st.Started) > opChangeVerifyFullEvery {
		opChangeIndexLog.Info("opchange-index-verify: the last full walk began over %s ago; verifying from the first row again "+
			"(a row a rolled-back binary wrote below the cursor is only found by a full walk): started=%s",
			opChangeVerifyFullEvery.String(), st.Started.Format(time.RFC3339))
		if err := p.clearOpChangeVerifyState(); err != nil {
			return rep, err
		}
		st, have = opChangeVerifyState{}, false
		rep.PriorCoverage = false
	}
	if !have {
		st = opChangeVerifyState{Started: now}
	}
	rep.ResumedAfter = st.Cursor

	var (
		yielded bool
		prev    = time.Now()
		slowest time.Duration
		base    = st.Rows
	)
	hook := func(r *OpChangeByBookIndexReport, last []byte) (bool, error) {
		if r.MissingEntries > 0 || r.UnmarkedUndecodable > 0 {
			return true, nil
		}
		next := opChangeVerifyState{Cursor: string(last), Rows: base + r.Rows, Started: st.Started}
		if err := p.saveOpChangeVerifyState(gen, next, false); err != nil {
			return false, err
		}
		t := time.Now()
		slowest = max(slowest, t.Sub(prev))
		prev = t
		if opChangeVerifyYieldDue(ctx, opts.YieldMargin, slowest) {
			yielded = true
			return true, nil
		}
		return false, nil
	}
	walked, out, err := p.verifyOpChangeRange(ctx, opChangeVerifyRun{
		from: []byte(st.Cursor), baseRows: base, hook: hook, progress: opts.Progress, total: opts.VerifyTotal,
	})
	walked.SentinelSet = true
	walked.PriorCoverage = rep.PriorCoverage
	walked.ResumedAfter = rep.ResumedAfter
	walked.VerifiedRows = base + walked.Rows
	if err != nil {
		return walked, err
	}
	if walked.MissingEntries > 0 || walked.UnmarkedUndecodable > 0 {
		return walked, nil
	}
	if yielded {
		walked.Yielded = true
		return walked, nil
	}
	final := opChangeVerifyState{Cursor: st.Cursor, Rows: walked.VerifiedRows, Started: st.Started}
	if len(out.last) > 0 {
		final.Cursor = string(out.last)
	}
	if err := p.saveOpChangeVerifyState(gen, final, true); err != nil {
		return walked, err
	}
	// The tail walk read only the rows after the cursor, so its own count is
	// not the journal's; the marker family is.
	if walked.Undecodable, err = p.countLiveOpChangeUndecodable(); err != nil {
		return walked, err
	}
	walked.Complete = true
	return walked, nil
}

// logOpChangeVerifyYield is the Info line of a verify that stopped for its
// deadline.
func logOpChangeVerifyYield(rep OpChangeByBookIndexReport) {
	opChangeIndexLog.Info("opchange-index-ensure: verify yielding before the run's deadline; the cursor is durable and the "+
		"same operation continues from it: verified_rows=%d rows_this_run=%d resumed_after=%s",
		rep.VerifiedRows, rep.Rows, logger.SanitizeLogValue(rep.ResumedAfter))
}
