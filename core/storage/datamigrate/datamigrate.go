// file: core/storage/datamigrate/datamigrate.go
// version: 1.0.3
// guid: 1796b752-c949-4f2a-9ec8-9f61fcf89823
// last-edited: 2026-11-12

// Package datamigrate declares data migrations: rewrites too large for one
// batch, run in chunks over a persisted shard plan with persisted shard
// cursors, so a crash or a stop resumes exactly where the last chunk
// committed (05-storage §5S.3.6, D-S8). Modules declare them in
// module.Manifest.DataMigrations (06 §6.4.2); core subsystems hand them to
// the kernel with their storage.Spec. The runner is
// core/internal/storagemigrate; the kernel builds it per App and hands each
// migration to storage as a node of the boot's migration DAG
// (storage.DataStep), which runs it in process at P4b (06 §6.5.1).
//
// BootBlocking is the only mode built. The Online mode is a deferred-build
// member (§5S.3.14, SIMP5-7): its constant and the Online clauses below are
// its contract at proof depth, and the runner refuses an Online migration at
// registration.
//
// Layer L4 (03 R4): it imports storage, entity, tasks and rbac, so the
// parallelism declaration is a real tasks.Parallelism (SIMP-17).
package datamigrate

import (
	"context"
	"errors"
	"strconv"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

type Mode uint8

const (
	ModeInvalid  Mode = iota
	BootBlocking      // a DAG node of the boot (§5S.3.5); a status listener reports progress (05-health)
	Online            // deferred-build member (§5S.3.14, SIMP5-7): a background task while serving; Additive, covered by read adapters
)

// String is the mode's name: "boot-blocking", "online" (deferred), or
// "invalid(<n>)".
func (m Mode) String() string {
	switch m {
	case BootBlocking:
		return "boot-blocking"
	case Online:
		return "online"
	default:
		return "invalid(" + strconv.Itoa(int(m)) + ")"
	}
}

type DataMigration struct {
	ID              string // "<owner>.<name>", unique, stable forever; <name> is [a-z][a-z0-9-]*; a module's kind is "<module>.datamigrate.<name>"
	Owner           storage.Owner
	Description     string
	Source          []storage.Range         // absolute ranges in the owner's keyspace/claims to visit; a cutover step: retired claims only; Online: Serialized ranges only
	Parallelism     tasks.Parallelism       // required (R7): tasks.CPU(), tasks.Fixed(n, reason), tasks.Sequential(reason)
	Mode            Mode                    // zero rejected
	Compat          storage.MigrationCompat // zero rejected; Breaking ⇒ BootBlocking and RaisesLevel > 0
	RaisesLevel     int                     // stamped (sidecar first) before the first chunk writes (§5S.3.7); a cutover step's level is already raised by M7 (§5S.3.13)
	Writes          []entity.Ref            // deferred-build member with Online (SIMP5-7): the resource kinds Step writes (Module and Kind set, ID empty); attribution only (COR4-5)
	RewritesInPlace bool                    // Step writes keys inside Source
	After           []storage.StepRef
	Barrier         int      // owner Version that may not run before this completes (0 = none)
	Cutover         bool     // an R2 cutover step (§5S.3.13): Breaking, BootBlocking, Source inside retired claims; Hold refused, Quarantine and Batch allowed
	Sensitive       []string // Cutover only: source prefixes whose quarantined rows may hold credentials (flagged in health and the listing)
	Residual        [][]byte // Cutover only: exact keys in a books ClaimReadWrite claim that Step rewrites in place (M2: the settings blob), each in a later cutover step's Source (§5S.3.13, DATA-1)
	// Preflight (Cutover only) reads the step's Source, writes nothing and
	// returns every row that must stop the upgrade instead of being
	// quarantined (D13: an unconvertible user, API key or OAuth identity, an
	// account merge in progress). The kernel runs it for every pending
	// cutover step before the checkpoint and before M7 (§5S.3.13 "Cutover
	// blockers").
	Preflight func(ctx context.Context, r storage.Reader) ([]Blocker, error)
	// FixBlocker and DeleteBlocker (Cutover only) stage into b the
	// replacement of the one blocking row key names by an operator-supplied
	// value (refused unless it then converts), or its deletion with the rows
	// its owner spec says go with it; they return the IDs they removed. Only
	// `storage cutover-blockers fix` and `delete` call them.
	FixBlocker    func(ctx context.Context, b *storage.Batch, key, value []byte) error
	DeleteBlocker func(ctx context.Context, b *storage.Batch, key []byte) (ids []string, err error)
	Step          func(ctx context.Context, kv storage.KV, w *StepWriter) error
	Verify        func(ctx context.Context, r storage.Reader) (VerifyReport, error) // invariant gate after the last shard
}

// StepWriter accumulates a chunk's writes; the runner commits them with the
// shard's cursor in ONE batch every core.storage.data_migration.chunk_bytes
// (default 4 MiB) or chunk_items (default 2 000).
//
// Its fields are the runner's chunk, seen through two interfaces (Sink and
// CutoverSink), so the runner, which lives in core/internal/storagemigrate,
// can build one (NewStepWriter) while Step sees only these methods. Keys
// are relative to the migration's source
// handle: the owner's keyspace when Source lies in it, the owner's legacy
// range (absolute keys) when Source lies in its claims.
//
// A chunk writes each key at most once, through any of the methods below
// (ErrKeyWrittenTwice): a precondition is checked against the committed
// state at the chunk's commit, so two items' SetIfAbsent of one key would
// both pass and the second write would replace the first. Across chunks the
// preconditions catch it (ErrConflict); a plain Set of one key by two items
// of different chunks is a step defect the runner cannot see.
type StepWriter struct {
	s   Sink
	cut CutoverSink
}

// Set and Delete are refused (ErrNotSerialized) in an Online migration, whose
// every write carries a precondition (COR4-5); deferred with that mode.
func (w *StepWriter) Set(key, value []byte) error { return w.sink().Set(key, value) }

// Delete stages the deletion of key (see Set).
func (w *StepWriter) Delete(key []byte) error { return w.sink().Delete(key) }

// SetIfAbsent stages Expect(key, nil) at chunk commit.
func (w *StepWriter) SetIfAbsent(key, value []byte) error { return w.sink().SetIfAbsent(key, value) }

// SetIfUnchanged stages Expect(key, expect) at chunk commit.
func (w *StepWriter) SetIfUnchanged(key, expect, value []byte) error {
	return w.sink().SetIfUnchanged(key, expect, value)
}

// DeleteIfUnchanged stages Expect(key, expect) + Delete at chunk commit.
func (w *StepWriter) DeleteIfUnchanged(key, expect []byte) error {
	return w.sink().DeleteIfUnchanged(key, expect)
}

// Hold records and skips; held items listed, never silently dropped.
func (w *StepWriter) Hold(key []byte, reason string) { w.sink().Hold(key, reason) }

// Count counts outcome: "converted", "already-current", ...
func (w *StepWriter) Count(outcome string) { w.sink().Count(outcome) }

// Quarantine copies the raw bytes of a source row that does not decode, or
// that an importer refused, to c/storage/quarantine/ and deletes the source
// key, both in the chunk's batch (§5S.3.13). Cutover only; outcome "quarantined".
func (w *StepWriter) Quarantine(key, raw []byte, reason string) error {
	return w.cutover().Quarantine(key, raw, reason)
}

// Batch is the chunk's batch for an importer's participant staging (§5S.3.2):
// the importer binds its own handle, and its writes commit with the chunk and
// the shard cursor. Cutover only.
//
// The batch is indexed: a read through it sees what the chunk staged before,
// so an importer that reads before it writes (an API key's owner staged by an
// earlier item of the same chunk, a run the batch already holds) works however
// the runner cut the chunks. The index costs memory for one chunk's entries.
//
// The batch is for staging. A step that commits or closes it fails its
// chunk with a StepError wrapping storage.ErrBatchDone, and what it
// committed is durable without the shard's cursor: a step defect. Writes
// staged through it, including through its origin (the Source handle), are
// outside the unvisited-write rule and the one-write-per-key rule: an
// importer writes only the handles it binds. A keyspace write in a
// Serialized range needs an Expect (storage.ErrBlindWrite): a chunk's batch
// is not a migration step's exclusive batch, a seam an importer over a
// Serialized family will need.
func (w *StepWriter) Batch() *storage.Batch { return w.cutover().Batch() }

// sink is the writer's chunk. A StepWriter only a runner can build has one;
// the zero value has none, and every call on it is a defect: a write that
// reached no chunk would be dropped in silence, so it panics instead.
func (w *StepWriter) sink() Sink {
	if w == nil || w.s == nil {
		panic(outsideChunk)
	}
	return w.s
}

// cutover is the writer's chunk for the two Cutover-only methods; a writer
// without one panics as sink does.
func (w *StepWriter) cutover() CutoverSink {
	if w == nil || w.cut == nil {
		panic(outsideChunk)
	}
	return w.cut
}

const outsideChunk = "datamigrate: StepWriter used outside a runner's chunk (build one with NewStepWriter)"

// Sink is a StepWriter's chunk for the methods every mode has (§5S.3.6
// lists them first: the writes, Hold and Count). The runner implements it,
// one per chunk, and enforces every rule those methods state (the
// unvisited-write refusal, the one-write-per-key rule, Hold refused in a
// Cutover step, the commit unit). Name coined by F2-06.
type Sink interface {
	Set(key, value []byte) error
	Delete(key []byte) error
	SetIfAbsent(key, value []byte) error
	SetIfUnchanged(key, expect, value []byte) error
	DeleteIfUnchanged(key, expect []byte) error
	Hold(key []byte, reason string)
	Count(outcome string)
}

// CutoverSink is a StepWriter's chunk for the two methods §5S.3.6 marks
// "Cutover only": Quarantine and Batch. The runner's chunk implements both
// interfaces and refuses these two itself in any other step
// (ErrCutoverOnly), so a misuse fails the chunk instead of panicking on a
// missing half. Name coined by F2-06.
type CutoverSink interface {
	Quarantine(key, raw []byte, reason string) error
	Batch() *storage.Batch
}

// NewStepWriter is the StepWriter over one chunk, for the runner: s takes
// the every-mode methods and cut the Cutover-only ones (the runner passes
// its chunk as both). Name coined by F2-06.
func NewStepWriter(s Sink, cut CutoverSink) *StepWriter { return &StepWriter{s: s, cut: cut} }

type VerifyReport struct {
	Read, Written, Held, Quarantined int64
	Failures                         []string // bounded sample
	// OrphanedRefs counts, per referencing books family, the references to
	// an ID whose source row was quarantined or removed as a blocker; they
	// are reported (health storage_quarantine), never a failure (DATA-2).
	OrphanedRefs map[string]int64
}

// Blocker is one source row that stops the upgrade (D13). Values are never
// carried: the report names the row, never its bytes.
type Blocker struct {
	Step   string // the data migration ID
	Key    []byte // the source key, absolute
	Family string // its §5S.9.1 family
	Reason string // decode error, the importer's refusal, or "merge in progress"
	Fix    string // the operator's way out, e.g. "finish or undo the merge on the pre-freeze release" or "storage cutover-blockers fix|delete"
}

// The states of a data migration's record c/storage/dm/<id> (§5S.3.6
// "Execution contract"). StateDone is storage.DataMigrationDone, the one
// state the boot's DAG reads, and is never re-entered (DATA2-15). Names
// coined by F2-06.
const (
	// StatePending: registered, not started. Never written: an absent
	// record is pending.
	StatePending = "pending"
	// StateRunning: the shard plan is persisted and shards are running.
	StateRunning = "running"
	// StateVerifying: every shard reached its end; Verify runs next.
	StateVerifying = "verifying"
	// StateDone: Verify passed. Never re-entered.
	StateDone = storage.DataMigrationDone
	// StateFailed: Verify failed; its report is kept beside the record and
	// the owner's Barrier versions stay blocked. The next boot re-verifies.
	StateFailed = "failed"
	// StateHeld: pending, but this boot defers the step (its owner is
	// disabled, or an edge waits on an absent owner). Reported, never
	// written.
	StateHeld = "held"
)

// States is every state, in the order the state gauge lists them. Name
// coined by F2-06.
func States() []string {
	return []string{StatePending, StateRunning, StateVerifying, StateDone, StateFailed, StateHeld}
}

// Outcomes the runner counts itself (Step counts its own with
// StepWriter.Count). Names coined by F2-06.
const (
	OutcomeHeld        = "held"
	OutcomeQuarantined = "quarantined"
)

// Errors of the declarations and the runner's StepWriter. Names coined by
// F2-06.
var (
	// ErrOnlineDeferred refuses an Online migration at registration: the
	// mode is a deferred-build member (§5S.3.14, SIMP5-7).
	ErrOnlineDeferred = errors.New("datamigrate: the Online mode is a deferred-build member (SIMP5-7); only BootBlocking is built")
	// ErrQuarantineNotWired: StepWriter.Quarantine was called while the
	// kernel has no quarantine writer (F3-04 supplies it). The chunk fails,
	// so a step can never drop a row in silence.
	ErrQuarantineNotWired = errors.New("datamigrate: quarantine writer not wired")
	// ErrCutoverOnly: Quarantine or Batch outside a Cutover step.
	ErrCutoverOnly = errors.New("datamigrate: Quarantine and Batch are for Cutover steps only")
	// ErrHoldInCutover: Hold in a Cutover step, whose retired range must
	// drain (§5S.3.13).
	ErrHoldInCutover = errors.New("datamigrate: Hold is refused in a Cutover step (a retired range must drain)")
	// ErrUnvisitedWrite: a write into the part of a shard's range no worker
	// has visited yet, by a migration that does not declare RewritesInPlace
	// (§5S.3.6 "Own writes are never revisited").
	ErrUnvisitedWrite = errors.New("datamigrate: write into the unvisited part of the source")
	// ErrKeyWrittenTwice: a chunk wrote one key twice (two of its items, or
	// one item twice). A precondition is checked against the committed
	// state only, so the second write would silently replace the first.
	ErrKeyWrittenTwice = errors.New("datamigrate: a chunk wrote one key twice")
	// ErrInvalidMigration: a declaration the runner refuses at registration.
	ErrInvalidMigration = errors.New("datamigrate: invalid data migration")
	// ErrLevelStepNotWired: a pending migration raises its owner's level
	// above the store's stamp and the kernel has no level step (F3-04
	// supplies it). The boot fails before the first chunk.
	ErrLevelStepNotWired = errors.New("datamigrate: level step not wired")
)
