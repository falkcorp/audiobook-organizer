// file: core/tasks/stores.go
// version: 1.0.2
// guid: e87dda23-11e8-4a65-9769-3ac16d0b3c05
// last-edited: 2026-11-10

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
)

// Stores is everything the executor and the scheduler persist. The kernel
// builds it once at P4c (06 §6.5.1) as NativeStores over c/tasks/ (5T.6.1);
// there is no other implementation and no app-supplied store (D10). Core code
// reaches runs, clocks, the pause marker and stand-down markers only through
// these interfaces and never names a stored key; the key layout lives in
// internal/taskstore (native.go). Importer (importer.go) is the one-shot path
// that writes v1 rows into the same keyspace.
type Stores struct {
	Runs      RunStore
	Clocks    ClockStore
	Pause     PauseStore
	StandDown StandDownStore // MOD6-4
}

// RunRecord is the stored form of a run: RunView plus what only the executor
// reads. The native store persists every field.
type RunRecord struct {
	RunView
	Priority   Priority
	Params     json.RawMessage
	Result     json.RawMessage
	Error      string // fixed token plus redacted text
	Attempt    int
	FenceEpoch uint64
	Yields     int
	Seq        RunSeqs // per-run numbering of events, intents and log lines (DATA-19)
}

// RunSeqs is a run's per-page sequence counters.
type RunSeqs struct{ Event, Intent, Log uint64 }

// Expect is the compare-and-set precondition of a transition (5T.7).
type Expect struct {
	State      RunState
	Attempt    int
	FenceEpoch uint64
}

// RunStore persists runs. Every write method that takes b STAGES into the
// caller's *storage.Batch: the implementation binds its own handle to b
// (participant binding, 05-storage §5S.3.2), so one commit carries a run row
// together with a clock advance, an outbox cursor (05-events 5E.4), an
// idempotency record or a notification (OnTerminal, 5T.4). The exceptions
// are Prune and ReplaceRows: maintenance-class methods that commit their own
// chunked batches and never join a multi-owner commit (COR5-12, DATA9-7).
// Every caller that builds such a batch builds it inside Executor.WriteRuns or
// Stager.WriteRuns, which commits it (5T.7, R9-1).
//
// No write may land on a run that changed since it was read: a progress tick
// cannot revert a cancel, and a chunk-ledger commit cannot land on a run that
// went terminal (HEAD's closed-ledger rule). The store guarantees it at commit
// (R6-1, DATA6-1, R7-3): Insert stages Expect(absent) on the run's guard key
// c/tasks/run/<id>; every other method that stages a write derived from a run
// it read — Transition, Progress, SaveCheckpoint, SaveChunk, MarkRow, Strike,
// AppendEvent, AppendIntent, PutItem, AppendLog, Delete — stages GetForUpdate
// on it, and Debounce on its bucket; the run's other keys carry no Expect of
// their own and are written only in a batch that carries the guard Expect
// (5T.6.1). Insert and Transition return ErrRunConflict at staging when their
// read no longer matches want; they never commit, so they never retry:
// WriteRuns re-runs build-and-commit under RetryOnConflict, which re-stages
// every participant (COR8-7), and every other write follows 5T.7's drop rule.
type RunStore interface { //nolint:interfacebloat // spec-mandated SDK interface (05-tasks 5T.3.13), frozen by the API-stability gate
	// Runs, queue, census, history.
	NewID(ctx context.Context) (RunID, error)
	Insert(ctx context.Context, b *storage.Batch, r RunRecord) error // record + queue entry + indexes
	Get(ctx context.Context, id RunID) (RunRecord, error)            // ErrRunNotFound
	// Transition: ErrRunConflict at staging when its read no longer matches
	// want. A commit conflict while want still matches is retried by
	// WriteRuns, which re-stages the whole batch (5T.7, R7-3, COR8-7).
	Transition(ctx context.Context, b *storage.Batch, id RunID, want Expect, next RunRecord) error
	Progress(ctx context.Context, b *storage.Batch, id RunID, done, total int, current string) error
	Queued(ctx context.Context) iter.Seq2[RunRecord, error]           // dispatch order: priority, then enqueue time
	Unfinished(ctx context.Context) iter.Seq2[RunRecord, error]       // the resume sweep's input
	List(ctx context.Context, q RunQuery) iter.Seq2[RunRecord, error] // includes terminal history, in RunFilter order
	Counts(ctx context.Context) (map[RunState]int64, error)           // derived from the state index (5T.6.1)

	// Per-run pages: transition trail, Preview intents, item outcomes, log.
	AppendEvent(ctx context.Context, b *storage.Batch, id RunID, seq uint64, e RunEvent) error
	AppendIntent(ctx context.Context, b *storage.Batch, id RunID, seq uint64, w Write, c []Change) error
	PutItem(ctx context.Context, b *storage.Batch, id RunID, key string, o ItemOutcome) error
	AppendLog(ctx context.Context, b *storage.Batch, id RunID, seq uint64, l LogLine) error
	Pages(ctx context.Context, id RunID, q PageQuery) iter.Seq2[PageEntry, error]

	// Resume state: checkpoints, the Batch/Fixer chunk ledger and item-key
	// snapshot, Fixer rows and their applied marks.
	SaveCheckpoint(ctx context.Context, b *storage.Batch, id RunID, name string, v []byte) error
	Checkpoint(ctx context.Context, id RunID, name string) ([]byte, bool, error)
	SaveChunk(ctx context.Context, b *storage.Batch, id RunID, c Chunk) error
	Chunks(ctx context.Context, id RunID) iter.Seq2[Chunk, error]
	// ReplaceRows makes the run's rows whose IDs begin with prefix exactly the
	// n rows at(0)..at(n-1) (a Fixer plan's rows, a result's class lists):
	// it clears that prefix, then writes the rows in bounded batches that it
	// commits itself (maintenance class, like Prune; HEAD's
	// ReplaceOpRowsPrefix contract, DATA9-7). An empty prefix is refused; n == 0
	// only clears. It is not atomic across batches: a caller that fails clears
	// its prefix again, and a reader checks the row count its summary
	// recorded. Each batch carries the run's guard GetForUpdate and HEAD's
	// closed-ledger rule (refused once the run is terminal). Never called
	// inside WriteRuns.
	ReplaceRows(ctx context.Context, id RunID, prefix string, n int, at func(i int) (Row, error)) error
	Rows(ctx context.Context, id RunID) iter.Seq2[Row, error]
	MarkRow(ctx context.Context, b *storage.Batch, id RunID, rowID string) error

	// Debounce buckets and watchdog strikes.
	Debounce(ctx context.Context, b *storage.Batch, d DebounceBucket) error
	DebounceBuckets(ctx context.Context) iter.Seq2[DebounceBucket, error]
	Strike(ctx context.Context, b *storage.Batch, id RunID, s Strike) error

	// Delete stages the removal of one run and every record it owns (row,
	// queue entry, indexes, pages, checkpoints, chunks, rows, errors); only
	// Client.Discard calls it, inside WriteRuns, after the executor has
	// refused an active run (FIN12-3). ErrRunNotFound; ErrRunActive when the
	// run is not terminal.
	Delete(ctx context.Context, b *storage.Batch, id RunID) error

	// Requires stamps: when a kind last completed for a subject.
	Stamp(ctx context.Context, b *storage.Batch, subject entity.Ref, k Kind, at time.Time) error
	Stamped(ctx context.Context, subject entity.Ref, k Kind) (time.Time, bool, error)

	// Maintenance: commits its own batches (above); a row is deleted only as
	// read, terminal (an Expect on the row). Called by core.tasks.prune (5T.10).
	Prune(ctx context.Context, before time.Time, limit int) (deleted int, err error) // terminal runs only, chunked
}

// Clock is one scheduled kind's durable clock (5T.3.8).
type Clock struct {
	LastDue, LastFire, LastRun time.Time
	Missed                     int
	GateClosedAt               time.Time
	ScheduleHash               string
}

// ClockStore persists schedule clocks.
type ClockStore interface {
	Get(ctx context.Context, k Kind) (Clock, bool, error)
	// Advance stages next if the stored clock still equals prev (absent when
	// had is false), checked at commit; otherwise ErrClockConflict.
	Advance(ctx context.Context, b *storage.Batch, k Kind, prev Clock, had bool, next Clock) error
}

// Pause is the executor's operator pause marker (Executor.PauseAs/UnpauseAs, Client.Pause/Unpause).
type Pause struct {
	Reason string
	By     Actor
	At     time.Time
}

// PauseStore persists the pause marker.
type PauseStore interface {
	Get(ctx context.Context) (Pause, bool, error)
	Set(ctx context.Context, b *storage.Batch, p *Pause) error // nil clears
}

// StandDown is the persisted marker of one live stand-down hold (5T.3.7). It
// exists only so that the next boot can tell that a holder died mid-work:
// the resume sweep then leaves the runs it quiesced interrupted_quiesced for
// that boot (HEAD's boot consult, the v2 registry's resume.go:45-60).
type StandDown struct {
	Key      Key
	Holder   RunID
	Quiesced []RunID
	Expires  time.Time // lease expiry when last staged
}

// StandDownStore persists stand-down markers (MOD6-4).
type StandDownStore interface {
	List(ctx context.Context) iter.Seq2[StandDown, error]
	Set(ctx context.Context, b *storage.Batch, key Key, s *StandDown) error // nil clears
}

// RunEvent is one transition of a run's state trail (v3 Transition minus its
// seq, which the page key carries). Fields coined by F2-01.
type RunEvent struct {
	From, To   RunState
	Event      string // claim | complete | fail | cancel | quiesce | interrupt | resume | decide | ...
	Reason     string // stop / wait / interrupted reason token
	Attempt    int
	FenceEpoch uint64
	Actor      *Actor // set for an actor-driven transition (cancel, decide, approve)
	At         time.Time
}

// ItemOutcome is a failed or skipped item of a run (v3 ItemOutcome).
type ItemOutcome struct {
	Key     string // item key or label
	Outcome string // failed | skipped:<reason>
	Error   string
	Chunk   int
}

// LogLine is one line of a run's log (v3 LogLine).
type LogLine struct {
	At      time.Time
	Level   string // info | warn | error
	Message string
	Fields  map[string]string
}

// PageKind names one of a run's pages. Coined by F2-01.
type PageKind uint8

const (
	PageEvents  PageKind = iota + 1 // c/tasks/page/<run_id>/evt/
	PageIntents                     // c/tasks/page/<run_id>/intent/
	PageItems                       // c/tasks/page/<run_id>/item/
	PageLog                         // c/tasks/page/<run_id>/log/
)

// PageQuery selects entries of one page of a run. Fields coined by F2-01.
type PageQuery struct {
	Page    PageKind
	After   string // keyset cursor: PageEntry.Key of the last entry read; "" = from the start
	Limit   int    // 0 = 100; above 1000 clamped to 1000
	Outcome string // PageItems only: an Outcome prefix ("skipped" matches every skipped:<reason>); "" = all
}

// PageEntry is one entry of a run's page; the field matching Page is set.
// Fields coined by F2-01.
type PageEntry struct {
	Page    PageKind
	Key     string // the entry's cursor (its seq, or an item's key)
	Seq     uint64 // events, intents and log lines
	Event   RunEvent
	Write   Write    // PageIntents
	Changes []Change // PageIntents
	Item    ItemOutcome
	Log     LogLine
}

// Chunk is one chunk of a Batch/Fixer run's ledger and item-key snapshot.
// Fields coined by F2-01.
type Chunk struct {
	N        int      // chunk number
	Keys     []string // Snapshot: the chunk's frozen item keys, in order
	First    string   // AppendOnly: the completed key range
	Last     string
	Complete bool
	Done     int64
	Failed   int64
	Skipped  int64
	Counters map[string]int64
	At       time.Time
}

// DebounceBucket is one kind's pending debounced starts (v2 Batchable).
// Fields coined by F2-01.
type DebounceBucket struct {
	Kind     Kind
	Bucket   string // the bucket within the kind ("" = the kind's one bucket)
	Subjects []entity.Ref
	Params   json.RawMessage
	First    time.Time // first arrival: MaxWait counts from here
	Last     time.Time // latest arrival: Window counts from here
}

// Strike is one watchdog strike (v2 op_strikes). Fields coined by F2-01.
type Strike struct {
	Kind   string // stuck | never_reported | uncheckpointed | infinite_restart
	Detail string
	At     time.Time
}

var (
	ErrRunNotFound   = errors.New("tasks: run not found")
	ErrRunConflict   = errors.New("tasks: run changed since read")
	ErrClockConflict = errors.New("tasks: schedule clock changed since read")
	// ErrRunActive: Discard of a run that is not terminal or that a live
	// worker holds (409; FIN12-3).
	ErrRunActive = errors.New("tasks: run is active")
)
