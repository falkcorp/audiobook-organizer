// file: internal/repairs/writer.go
// version: 1.7.0
// guid: c71e0d93-4b28-4a5f-8e6c-2f9a1d7b3e48
// last-edited: 2026-10-03

package repairs

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// ChangeTypeApplyIncomplete matches metafetch.ChangeTypeApplyIncomplete:
// UndoLastApply refuses a batch carrying it. Spelled out rather than imported
// so the framework does not depend on the metafetch service.
const ChangeTypeApplyIncomplete = "apply_incomplete"

// ChangeTypeApplyOpJournaled matches metafetch.ChangeTypeApplyOpJournaled: a
// Writer wired to an op journal (WithJournal) writes it into the history
// batch of every book this writer has journaled an operation change for, and
// UndoLastApply refuses such a batch. That book's fields are one part of a
// step whose other parts (book_file rows moved or repointed, books retired)
// only the operation revert puts back. A book the apply only Modify'd, with
// nothing journaled (the version-group-primary fixer's flag writes), gets no
// marker: its batch is undone field by field, as before.
const ChangeTypeApplyOpJournaled = "apply_op_journaled"

// BookModifier is the one book write primitive a fixer gets.
type BookModifier interface {
	ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error)
}

// HistoryRecorder records metadata-history rows.
type HistoryRecorder interface {
	RecordMetadataChange(record *database.MetadataChangeRecord) error
}

// Writer is the only write path the framework hands a fixer. It has no
// delete method on purpose: no book, and no book_file row, can be deleted
// through the Repairs lane (standing owner rule: never delete book_file rows
// as a repair). A test pins the method set.
//
// Every Modify records one metadata-history row per changed field, AFTER the
// write it describes, all under one batch id per call, so "undo last apply"
// on a book reverts that call's change to it. If a row cannot be recorded, an
// apply_incomplete marker is written so undo refuses the batch rather than
// half-reverting it. A batch on a book the Writer has journaled an operation
// change for is also marked apply_op_journaled: that apply is undone from its
// operation, never field by field. Safe for concurrent use.
type Writer struct {
	store   BookModifier
	history HistoryRecorder
	// Source / ChangeType go on every history row; BatchPrefix starts every
	// batch id.
	source, changeType, batchPrefix string
	log                             logger.LevelLogger

	// Operation journal and book_file surface (writer_files.go); nil until
	// WithJournal.
	files   BookFileWriter
	journal ChangeJournal
	opID    string
	index   *journalIndex
	touch   func()

	// credits is the book-credit surface (writer_credits.go); nil until
	// WithCredits. Its rows go through journal above.
	credits CreditStore

	// lease renews the apply's scan stand-down lease and reports whether it
	// is still held; nil when no lease is held (RunApply installs it for the
	// run, see setLease). leaseLost latches the first failed renewal.
	lease     func() bool
	leaseLost atomic.Bool
	// lockRenewEvery is how often LockWaiting renews the lease while it
	// waits; zero means defaultLockRenewEvery. RunApply sets it from
	// WaitOptions.LockRenewEvery.
	lockRenewEvery time.Duration

	writes        atomic.Int64
	historyRows   atomic.Int64
	historyFailed atomic.Int64
	journaled     atomic.Int64
}

// NewWriter builds a Writer. Its warnings go to the "repairs" subsystem
// logger (printf-style, values sanitized).
func NewWriter(store BookModifier, history HistoryRecorder, source, changeType, batchPrefix string) *Writer {
	return &Writer{store: store, history: history, source: source, changeType: changeType,
		batchPrefix: batchPrefix, log: logger.New("repairs")}
}

// Modify runs fn on the book row inside ModifyBook (so a check fn makes and
// the write are one atomic step; fn may be retried) and records history for
// every tracked field that changed. It returns the changed field names in
// database.TrackedBookFields order. An error from fn aborts the write and is
// returned as is.
func (w *Writer) Modify(bookID string, fn func(*database.Book) error) ([]string, error) {
	if err := w.beat("book " + bookID); err != nil {
		return nil, err
	}
	var before, after *database.Book
	written, err := w.store.ModifyBook(bookID, func(row *database.Book) error {
		snap, serr := database.SnapshotBook(row)
		if serr != nil {
			return serr
		}
		if ferr := fn(row); ferr != nil {
			return ferr
		}
		post, perr := database.SnapshotBook(row)
		if perr != nil {
			return perr
		}
		// Captured on every invocation, so a retried callback leaves the
		// values of the attempt that committed.
		before, after = snap, post
		return nil
	})
	if err != nil {
		return nil, err
	}
	if written == nil {
		return nil, fmt.Errorf("book %s vanished before the write", bookID)
	}
	w.writes.Add(1)
	return w.recordHistory(bookID, before, after), nil
}

func (w *Writer) recordHistory(bookID string, before, after *database.Book) []string {
	batchID := w.batchPrefix + ulid.Make().String()
	now := time.Now()
	var changed []string
	failed := 0
	for _, field := range database.TrackedBookFields() {
		oldV, oerr := database.RenderBookField(before, field)
		newV, nerr := database.RenderBookField(after, field)
		if oerr != nil || nerr != nil || oldV == newV {
			continue
		}
		changed = append(changed, field)
		oldJSON, newJSON := jsonString(oldV), jsonString(newV)
		rec := &database.MetadataChangeRecord{
			BookID:        bookID,
			Field:         field,
			PreviousValue: &oldJSON,
			NewValue:      &newJSON,
			ChangeType:    w.changeType,
			Source:        w.source,
			ChangedAt:     now,
			BatchID:       batchID,
		}
		// "Undo last apply" restores a foreign-key column from the row's refs,
		// never from the rendered value, and fails the field without them; a
		// series_id write through the Writer was not undoable until these
		// were recorded. author_id is left without refs on purpose: undo of
		// that column also needs the book_authors join (BookAuthorsKnown),
		// which the Writer does not read, and reverting the column alone
		// would leave the join naming the new author.
		if field == "series_id" {
			rec.PreviousRef = &database.MetadataChangeRef{SeriesID: copyInt(before.SeriesID)}
			rec.NewRef = &database.MetadataChangeRef{SeriesID: copyInt(after.SeriesID)}
		}
		if err := w.history.RecordMetadataChange(rec); err != nil {
			failed++
			w.log.Warn("%s: history row not recorded (the write itself committed): book_id=%s field=%s err=%s",
				w.source, logger.SanitizeLogValue(bookID), field, logger.SanitizeLogValue(err.Error()))
			continue
		}
		w.historyRows.Add(1)
	}
	if len(changed) > 0 && w.journaledBook(bookID) {
		if err := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID: bookID, Field: "apply", ChangeType: ChangeTypeApplyOpJournaled,
			Source: w.source, ChangedAt: now, BatchID: batchID,
		}); err != nil {
			// Without the marker, undo-last-apply would revert this batch's
			// fields alone; fall through to the incomplete marker, which it
			// also refuses.
			failed++
			w.log.Warn("%s: op-journaled marker not recorded: book_id=%s batch_id=%s err=%s",
				w.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(err.Error()))
		}
	}
	if failed > 0 {
		w.historyFailed.Add(int64(failed))
		if err := w.history.RecordMetadataChange(&database.MetadataChangeRecord{
			BookID: bookID, Field: "apply", ChangeType: ChangeTypeApplyIncomplete,
			Source: w.source, ChangedAt: now, BatchID: batchID,
		}); err != nil {
			w.log.Error("%s: neither the history nor the incomplete marker was recorded: book_id=%s batch_id=%s err=%s",
				w.source, logger.SanitizeLogValue(bookID), batchID, logger.SanitizeLogValue(err.Error()))
		}
	}
	return changed
}

// setLease installs the scan stand-down renewal every write beats (nil
// removes it). RunApply sets it before its workers start and clears it when
// they are done.
//
// WHY THE WRITER BEATS: the lease (5m) used to be renewed only once per row,
// at the row's start. A legitimate fragment-consolidation row that retired
// 315 books wrote for 5m34s without a renewal, the registry reaped the
// holder, and every remaining row was abandoned (2026-10-01). Renewing on
// each write keeps a row that is making progress alive however long it is,
// and keeps the registry's rule that the lease tracks progress, not a timer:
// a write that wedges never beats again, so its lease still lapses. Renewal
// is an in-memory map update (the registry throttles the marker it persists
// to once a second), so it is not throttled here; a throttle would only
// delay noticing a lost lease.
func (w *Writer) setLease(renew func() bool) {
	w.lease = renew
	w.leaseLost.Store(false)
}

// beat renews the lease before a write. Once a renewal fails the lease is
// gone for good (the registry never resurrects a lapsed holder), so every
// later write is refused without trying again. The error wraps
// ErrStandDownLost; the write it guards is not made.
func (w *Writer) beat(what string) error {
	if w.lease == nil {
		return nil
	}
	if w.leaseLost.Load() || !w.lease() {
		w.leaseLost.Store(true)
		return fmt.Errorf("%w: %s not written", ErrStandDownLost, what)
	}
	return nil
}

// Beat renews the scan stand-down lease, as every Writer write does, for a
// write the Writer cannot route: a store helper that writes directly
// (versionprimary.EnsureSinglePrimary, merge.FollowAbsorbedJournaled's
// pending-repair record and progress moves). Call it immediately before such
// a helper; an error wraps ErrStandDownLost and the helper must not run. A
// no-op when no lease is held.
func (w *Writer) Beat(what string) error { return w.beat(what) }

// defaultLockRenewEvery is how often LockWaiting renews while it waits: well
// inside the registry's 5m lease.
const defaultLockRenewEvery = 30 * time.Second

// LockWaiting takes a process-wide lock for an apply row (lock is a blocking
// acquire such as merge.LockMergeRMW, unlock its release) while keeping the
// scan stand-down lease. It renews before waiting, every lockRenewEvery while
// it waits (stamping liveness too) and once more after acquiring, so neither
// an outside holder that keeps the lock past the lease nor a lapse during the
// wait goes unnoticed. A failed renewal returns an error wrapping
// ErrStandDownLost with the lock NOT held; ctx ending returns ctx's error,
// also with the lock not held. On nil the caller holds the lock and must
// unlock it.
//
// WHY a goroutine and not a TryLock poll: the waiter joins the mutex's queue
// like any other caller, so a busy lock cannot starve it between polls. A
// waiter that gives up leaves the goroutine queued; it releases the lock the
// moment it gets it.
func (w *Writer) LockWaiting(ctx context.Context, what string, lock, unlock func()) error {
	if err := w.beat("before waiting for " + what); err != nil {
		return err
	}
	acquired := make(chan struct{})
	abandon := make(chan struct{})
	go func() {
		lock()
		select {
		case acquired <- struct{}{}:
		case <-abandon:
			unlock()
		}
	}()
	every := w.lockRenewEvery
	if every <= 0 {
		every = defaultLockRenewEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-acquired:
			if err := w.beat("after waiting for " + what); err != nil {
				unlock()
				return err
			}
			return nil
		case <-t.C:
			w.Touch()
			if err := w.beat("while waiting for " + what); err != nil {
				close(abandon)
				return err
			}
		case <-ctx.Done():
			close(abandon)
			return fmt.Errorf("waiting for %s: %w", what, ctx.Err())
		}
	}
}

// Writes is how many Modify calls committed.
func (w *Writer) Writes() int { return int(w.writes.Load()) }

// HistoryRows is how many history rows were recorded.
func (w *Writer) HistoryRows() int { return int(w.historyRows.Load()) }

// HistoryFailed is how many history rows could not be recorded.
func (w *Writer) HistoryFailed() int { return int(w.historyFailed.Load()) }

func copyInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// jsonString encodes s as a JSON string, the shape every history value has.
// Marshalling a Go string cannot fail; the fallback only keeps the linter
// honest.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return strconv.Quote(s)
	}
	return string(b)
}
