// file: core/internal/storagemigrate/runner.go
// version: 1.0.1
// guid: 600161b2-5090-4249-bc97-6b5f97787ee2
// last-edited: 2026-11-10

// Package storagemigrate is the runner of storage/datamigrate's data
// migrations (05-storage §5S.3.6): BootBlocking migrations run in process,
// inside the boot's migration DAG at P4b (06 §6.5.1), through
// tasks.ForEach over a persisted shard plan with persisted shard cursors,
// one batch per chunk. The Online mode is a deferred-build member
// (§5S.3.14, SIMP5-7): it is refused at registration and has no execution
// path here.
//
// The kernel builds one Runner per App (03 R5: no package state) and hands
// storage one storage.DataStep per migration (Runner.Steps); storage calls
// Runner.Run at the step's place in the DAG, before step 9, so a failed
// migration leaves the attempt's checkpoint open.
//
// The commit unit is the chunk: a chunk's writes, its held-item records,
// its quarantine records and the shard's cursor commit in ONE batch with
// storage.Sync, so a crash leaves every item of the chunk either untouched
// or done, never both and never neither, and a resume restarts exactly
// after the last committed key.
//
// Layer L4, a bridge (03 R4): it joins storage, datamigrate and tasks.
package storagemigrate

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// QuarantineWriter stages a quarantine record of the source row key (an
// absolute key) holding raw into b, the chunk's batch, for data migration
// step: the record format, its sequence and the sealing of a sensitive row
// are F3-04's (core/storage/quarantine.go), which supplies the writer
// through the kernel. sensitive is true when key lies under one of the
// step's Sensitive prefixes. Name coined by F2-06.
//
// Shard workers call it concurrently, each with its own chunk's batch, so
// it must be safe for concurrent use. It stages into b only: it commits
// nothing and allocates nothing durable outside b, so a lost chunk loses
// its records with it.
type QuarantineWriter func(ctx context.Context, b *storage.Batch, step string, key, raw []byte, reason string, sensitive bool) error

// LevelStep raises owner's level stamp to level (sidecar first, §5S.3.7)
// before a migration's first chunk. F3-04 supplies it through the kernel;
// until then a migration that would raise a stamp fails the boot before it
// writes (datamigrate.ErrLevelStepNotWired). Name coined by F2-06.
type LevelStep func(ctx context.Context, db *storage.DB, owner storage.Owner, level int) error

// Options configure a Runner. Every field but Owners has a default.
type Options struct {
	// Owners are the build's storage owners, as storage.Open receives them:
	// registration checks each migration's Source against its owner's
	// keyspace and claims, and RaisesLevel against its Spec.Level.
	Owners []storage.OwnerSpec
	// ChunkBytes and ChunkItems are core.storage.data_migration.chunk_bytes
	// and chunk_items: a chunk commits once its batch reaches ChunkBytes or
	// it has stepped ChunkItems items, whichever comes first.
	ChunkBytes int64 // 0 = 4 MiB
	ChunkItems int   // 0 = 2 000
	// Build is the binary ID the state records carry (§5S.3.7 "Binary ID").
	Build string
	// Logger nil discards.
	Logger *slog.Logger
	// Clock nil is time.Now.
	Clock func() time.Time
	// ExtendStart is the boot's start-timeout extension (Supervisor
	// ExtendStart, §5S.3.13 "Systemd"): called at every migration's start
	// and every ExtendEvery committed chunks. nil = none. The runner calls
	// it from one goroutine at a time.
	ExtendStart func(step string)
	ExtendEvery int // 0 = 16
	// Quarantine stages quarantine records; nil until F3-04, and then
	// StepWriter.Quarantine fails the chunk (ErrQuarantineNotWired).
	Quarantine QuarantineWriter
	// LevelStep raises a level stamp; nil until F3-04.
	LevelStep LevelStep
	// PlanShards is the shard count of a NEW plan for par; nil =
	// par.Workers(). A test seam: a persisted plan is reused whatever it
	// says now, which is what TestDataMigrationResumesExactly proves.
	PlanShards func(par tasks.Parallelism) int
}

// Defaults of Options (05-storage §5S.7). Names coined by F2-06.
const (
	DefaultChunkBytes  = 4 << 20
	DefaultChunkItems  = 2000
	DefaultExtendEvery = 16
)

func (o *Options) defaults() {
	if o.ChunkBytes <= 0 {
		o.ChunkBytes = DefaultChunkBytes
	}
	if o.ChunkItems <= 0 {
		o.ChunkItems = DefaultChunkItems
	}
	if o.ExtendEvery <= 0 {
		o.ExtendEvery = DefaultExtendEvery
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Clock == nil {
		o.Clock = time.Now
	}
	if o.PlanShards == nil {
		o.PlanShards = func(p tasks.Parallelism) int { return p.Workers() }
	}
}

// Runner runs one build's data migrations. It is safe for one boot's
// sequential Run calls; each Run fans its shards out itself.
type Runner struct {
	opts Options
	migs []*migration // registration order
	byID map[string]*migration

	extendMu sync.Mutex // ExtendStart is called from one goroutine at a time
}

// migration is one registered data migration with what registration
// derived from it.
type migration struct {
	m datamigrate.DataMigration
	// legacy: Source lies in the owner's claims, so the source handle is its
	// LegacyRange (absolute keys); else its Keyspace (relative keys).
	legacy bool
	prefix []byte // the owner's keyspace prefix when !legacy
	// source is Source sorted and relative to the source handle; a nil End
	// is the handle's own end.
	source []storage.Range
	stats  *stats
}

// ref is the migration's StepRef.
func (mg *migration) ref() storage.StepRef {
	return storage.StepRef{Owner: mg.m.Owner, DataMigration: mg.m.ID}
}

// New validates migs against opts.Owners (registration, §5S.3.6; every
// problem listed, each wrapping datamigrate.ErrInvalidMigration, and also
// tasks.ErrNoParallelism or datamigrate.ErrOnlineDeferred (the refusal) where that is the
// cause) and returns their Runner.
func New(opts Options, migs ...datamigrate.DataMigration) (*Runner, error) {
	opts.defaults()
	r := &Runner{opts: opts, byID: map[string]*migration{}}
	owners := map[storage.Owner]storage.OwnerSpec{}
	for _, o := range opts.Owners {
		owners[o.Owner] = o
	}
	var probs []error
	for i, m := range migs {
		mg, ps := register(owners, m)
		if _, dup := r.byID[m.ID]; dup && m.ID != "" {
			ps = append(ps, fmt.Errorf("%w: data migration %d (%s): ID registered twice (an ID is unique and never reused)", datamigrate.ErrInvalidMigration, i, m.ID))
		}
		if len(ps) > 0 {
			probs = append(probs, ps...)
			continue
		}
		mg.stats = newStats()
		r.migs = append(r.migs, mg)
		r.byID[m.ID] = mg
	}
	if len(probs) > 0 {
		return nil, joinProblems(probs)
	}
	return r, nil
}

// Steps are the migrations as the boot's DAG nodes, for
// storage.OpenOptions.DataSteps, in registration order.
func (r *Runner) Steps() []storage.DataStep {
	out := make([]storage.DataStep, 0, len(r.migs))
	for _, mg := range r.migs {
		id := mg.m.ID
		out = append(out, storage.DataStep{
			Owner: mg.m.Owner, ID: id, After: slices.Clone(mg.m.After), Barrier: mg.m.Barrier, Cutover: mg.m.Cutover,
			Run: func(ctx context.Context, db *storage.DB) error { return r.Run(ctx, db, id) },
		})
	}
	return out
}

// Migrations are the registered declarations, in registration order.
func (r *Runner) Migrations() []datamigrate.DataMigration {
	out := make([]datamigrate.DataMigration, 0, len(r.migs))
	for _, mg := range r.migs {
		out = append(out, mg.m)
	}
	return out
}

// extend calls ExtendStart from one goroutine at a time.
func (r *Runner) extend(step string) {
	if r.opts.ExtendStart == nil {
		return
	}
	r.extendMu.Lock()
	defer r.extendMu.Unlock()
	r.opts.ExtendStart(step)
}
