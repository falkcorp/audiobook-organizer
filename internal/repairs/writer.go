// file: internal/repairs/writer.go
// version: 2.0.1
// guid: c71e0d93-4b28-4a5f-8e6c-2f9a1d7b3e48
// last-edited: 2026-10-29

package repairs

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opswriter"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// ChangeTypeApplyIncomplete matches metafetch.ChangeTypeApplyIncomplete:
// UndoLastApply refuses a batch carrying it. Spelled out rather than imported
// so the framework does not depend on the metafetch service.
const ChangeTypeApplyIncomplete = opswriter.ChangeTypeApplyIncomplete

// ChangeTypeApplyOpJournaled matches metafetch.ChangeTypeApplyOpJournaled: a
// Writer wired to an op journal (WithJournal) writes it into the history
// batch of every book this writer has journaled an operation change for, and
// UndoLastApply refuses such a batch. That book's fields are one part of a
// step whose other parts (book_file rows moved or repointed, books retired)
// only the operation revert puts back. A book the apply only Modify'd, with
// nothing journaled (the version-group-primary fixer's flag writes), gets no
// marker: its batch is undone field by field, as before.
const ChangeTypeApplyOpJournaled = opswriter.ChangeTypeApplyOpJournaled

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
	// core is the fenced write path this Writer wraps
	// (internal/operations/opswriter): the fence and stand-down gate every
	// write passes, the book write that captures "previous" inside the
	// store's critical section, and the metadata-history recording.
	core  *opswriter.Writer
	store BookModifier
	// Source goes on every journal row.
	source string
	log    logger.LevelLogger

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

	// fieldStates is the metadata field-state surface (writer_locks.go); nil
	// until WithFieldStates. Its locks are journaled through journal above.
	fieldStates FieldStateStore

	// tags is the book_tag surface (writer_tags.go); nil until WithTags. Its
	// rows are journaled through journal above.
	tags TagStore
	// tagsOnly: the apply's fixer is BookTagsOnly; every non-tag method
	// refuses (restrictToTags).
	tagsOnly atomic.Bool
	// noWrites: the apply's fixer is NoScanStandDown and runs without the
	// scan stand-down; every write method refuses (restrictToNothing).
	noWrites atomic.Bool

	// userState is the user listening-state surface (writer_userstate.go);
	// nil until WithUserState. Its writes are journaled through journal
	// above.
	userState UserStateStore

	// lockRenewEvery is how often LockWaiting renews the lease while it
	// waits; zero means defaultLockRenewEvery. RunApply sets it from
	// WaitOptions.LockRenewEvery.
	lockRenewEvery time.Duration

	// provenance is the run's plan op and actor (writer_provenance.go); set by
	// RunApply for the run.
	provenance atomic.Pointer[ApplyProvenance]

	writes    atomic.Int64
	journaled atomic.Int64
}

// NewWriter builds a Writer. Its warnings go to the "repairs" subsystem
// logger (printf-style, values sanitized).
func NewWriter(store BookModifier, history HistoryRecorder, source, changeType, batchPrefix string) *Writer {
	w := &Writer{store: store, source: source, log: logger.New("repairs")}
	w.core = opswriter.New(opswriter.Config{
		Mode:          ops.Live,
		Books:         store,
		History:       opswriter.NewBookHistory(history, source, changeType, batchPrefix, w.log),
		JournaledBook: w.journaledBook,
		LeaseLostErr:  ErrStandDownLost,
		// No intent store: every journaled step is written to the op
		// journal BEFORE its write (writer_files.go, JOURNAL FIRST), which
		// is what a resumed repairs.apply replays.
	})
	return w
}

// Modify runs fn on the book row inside ModifyBook (so a check fn makes and
// the write are one atomic step; fn may be retried) and records history for
// every tracked field that changed. It returns the changed field names in
// database.TrackedBookFields order. An error from fn aborts the write and is
// returned as is.
func (w *Writer) Modify(bookID string, fn func(*database.Book) error) ([]string, error) {
	if err := w.denyTagsOnly("Modify"); err != nil {
		return nil, err
	}
	changed, err := w.core.ModifyBook(context.Background(), bookID, fn)
	if err != nil {
		return nil, err
	}
	w.writes.Add(1)
	return changed.Fields, nil
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
func (w *Writer) setLease(renew func() bool) { w.core.SetLease(renew) }

// setLeaseValid installs the non-renewing lease check beat uses on a fenced
// write (nil removes it). RunApply installs it beside setLease.
//
// WHY A SEPARATE CHECK: a fenced write must not renew. The renewal is the
// run's progress heartbeat, and a canceled or abandoned run that keeps
// calling the Writer would otherwise keep its stand-down lease alive -- and
// the library scan parked -- for as long as it keeps trying.
func (w *Writer) setLeaseValid(valid func() bool) { w.core.SetLeaseValid(valid) }

// setFence installs the run's write permit; nil clears it. beat checks it
// before the lease, so a fenced Writer refuses every write.
func (w *Writer) setFence(check func() error) { w.core.SetFence(check) }

// beat checks the run's fence, then renews the lease, before a write. A
// revoked fence refuses with an error wrapping whatever the check returned
// (opswriter.ErrFenced), and never renews the lease. Once a renewal fails the
// lease is gone for good (the registry never resurrects a lapsed holder), so
// every later write is refused without trying again; that error wraps
// ErrStandDownLost. Either way the write it guards is not made.
//
// Every write method of the Writer reaches beat: Modify, ModifyCredits,
// SetPrimaryAuthor, SetUserState, AddBookTag, Recompute and LockWaiting
// directly, and every journaled step (Journal, Step, JournalStep(s),
// RepointBookFile, MoveBookFiles, SetTrackNumber, the iTunes-path clears,
// LockFields) through journalRow.
func (w *Writer) beat(what string) error { return w.core.Gate(what) }

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
func (w *Writer) HistoryRows() int { return w.core.History().Rows() }

// HistoryFailed is how many history rows could not be recorded.
func (w *Writer) HistoryFailed() int { return w.core.History().Failed() }

// jsonString encodes s as a JSON string, the shape every history value has.
func jsonString(s string) string { return opswriter.JSONString(s) }
