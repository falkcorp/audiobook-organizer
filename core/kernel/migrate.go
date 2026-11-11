// file: core/kernel/migrate.go
// version: 1.0.2
// guid: 065a3401-6518-4976-9508-a0b36d3bcb96
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/internal/restoreswap"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// DataMigrationsConfig configures the kernel side of the BootBlocking
// data-migration runner (05-storage §5S.3.6; 06 §6.5.1 P4b). Name coined
// by F2-06.
//
// Two fields are F3-04's seams, nil until it lands (F2-06 builds only the
// runner's half of each):
//
//   - Quarantine stages the quarantine record c/storage/quarantine/<step>/<n>
//     into a chunk's batch (core/storage/quarantine.go: the record format,
//     its sequence, the sealing of a sensitive row). With it nil,
//     StepWriter.Quarantine fails the chunk with
//     datamigrate.ErrQuarantineNotWired, so no step can drop a row in
//     silence.
//   - Preflight is the cutover preflight (core/storage/preflight.go): at
//     P4a, before the checkpoint and before M7, it runs every pending
//     cutover step's Preflight and refuses the boot on a blocker
//     (§5S.3.13 "Cutover blockers"). With it nil, DataMigrations.Preflight
//     does nothing.
//
// LevelStep is F3-04's too (the level step, §5S.3.7): with it nil, a
// pending migration that would raise a stamp above the store's fails the
// boot before its first chunk (datamigrate.ErrLevelStepNotWired).
//
// One more seam belongs to the first Cutover importer over a Serialized
// family: a chunk's batch is not a migration step's exclusive batch, so a
// plain Set an importer stages into a Serialized keyspace range through
// StepWriter.Batch fails the chunk (storage.ErrBlindWrite). That importer
// needs an exclusive chunk batch or an Expect per key (todo.d).
//
// F3-04 supplies the values: Quarantine is storage.QuarantineWriter over
// the keyring's sealer (F3-07 binds it), LevelStep is LevelStep, Preflight
// is CutoverPreflight. Rewrite and Swapper are M8 (§5S.3.13 "M8: the
// rewrite"): F3-07 registers books' M8 (storage.NewRewriteStep("books",
// "books.rewrite")) with the boot's P0a swapper, whose
// restoreswap.Options.RewriteStep it sets to the same ID; Steps then
// appends M8 last, after every registered migration.
type DataMigrationsConfig struct {
	ChunkBytes  int64 // core.storage.data_migration.chunk_bytes; 0 = 4 MiB
	ChunkItems  int   // core.storage.data_migration.chunk_items; 0 = 2 000
	Build       string
	Logger      *slog.Logger
	Clock       func() time.Time
	ExtendStart func(step string) // the boot's start-timeout extension (§5S.3.13 "Systemd")
	Quarantine  func(ctx context.Context, b *storage.Batch, step string, key, raw []byte, reason string, sensitive bool) error
	LevelStep   func(ctx context.Context, db *storage.DB, owner storage.Owner, level int) error
	Preflight   func(ctx context.Context, o *storage.Opened, cutover []datamigrate.DataMigration) error
	Rewrite     *storage.RewriteStep
	Swapper     *restoreswap.Swapper
}

// DataMigrations is the kernel side of the runner, one per App (03 R5):
// the build's data migrations, validated against its storage owners, as
// the DAG nodes storage runs at P4b.
//
// The boot call is F2-16's (the F2 wiring row, which owns boot.go and
// kernel.go and adds Manifest.DataMigrations): it builds one with
// NewDataMigrations from the manifests and core's specs, puts Steps into
// the P2 open's storage.OpenOptions.DataSteps (storageOptions), calls
// Preflight at P4a before Prepare, and RecordMetrics after P5. storage then
// runs each migration at its place in the P4 DAG inside Migrate (P4b),
// after its owner's versions below Barrier and before step 9, so a failed
// migration leaves the attempt's checkpoint open. Name coined by F2-06.
type DataMigrations struct {
	cfg    DataMigrationsConfig
	runner *storagemigrate.Runner
	// rewriteRun replaces M8's Run (the dry run's copy into scratch).
	rewriteRun func(ctx context.Context, db *storage.DB) error
}

// NewDataMigrations validates migs against owners, the build's storage
// owners as P2 opens the store with them (registration, §5S.3.6 runner
// rule 1: every problem listed). The error matches
// datamigrate.ErrInvalidMigration, and tasks.ErrNoParallelism or
// datamigrate.ErrOnlineDeferred where a problem is that. Name coined by
// F2-06.
func NewDataMigrations(cfg DataMigrationsConfig, owners []storage.OwnerSpec, migs ...datamigrate.DataMigration) (*DataMigrations, error) {
	opts := storagemigrate.Options{
		Owners:      slices.Clone(owners),
		ChunkBytes:  cfg.ChunkBytes,
		ChunkItems:  cfg.ChunkItems,
		Build:       cfg.Build,
		Logger:      cfg.Logger,
		Clock:       cfg.Clock,
		ExtendStart: cfg.ExtendStart,
		Quarantine:  cfg.Quarantine,
		LevelStep:   cfg.LevelStep,
	}
	r, err := storagemigrate.New(opts, migs...)
	if err != nil {
		return nil, fmt.Errorf("kernel: data migrations: %w", err)
	}
	if rw := cfg.Rewrite; rw != nil {
		switch {
		case rw.ID == "" || rw.Owner == "":
			return nil, fmt.Errorf("kernel: data migrations: %w: M8 needs an owner and an ID", datamigrate.ErrInvalidMigration)
		case !slices.ContainsFunc(owners, func(o storage.OwnerSpec) bool { return o.Owner == rw.Owner }):
			return nil, fmt.Errorf("kernel: data migrations: %w: M8 %s's owner %s is not a storage owner", datamigrate.ErrInvalidMigration, rw.ID, rw.Owner)
		case slices.ContainsFunc(migs, func(m datamigrate.DataMigration) bool { return m.ID == rw.ID }):
			return nil, fmt.Errorf("kernel: data migrations: %w: M8 %s is also a registered migration", datamigrate.ErrInvalidMigration, rw.ID)
		}
	}
	return &DataMigrations{cfg: cfg, runner: r}, nil
}

// Steps are the P4b entry point: one storage.DataStep per migration, for
// the P2 open's storage.OpenOptions.DataSteps, then M8 when one is
// registered: last, after every migration, a cutover step that marks the
// plan's extra free space (storage.DataStep.Rewrite). Name coined by F2-06.
func (d *DataMigrations) Steps() []storage.DataStep {
	steps := d.runner.Steps()
	rw := d.cfg.Rewrite
	if rw == nil {
		return steps
	}
	after := slices.Clone(rw.After)
	for _, s := range steps {
		after = append(after, storage.StepRef{Owner: s.Owner, DataMigration: s.ID})
	}
	run := d.rewriteRun
	if run == nil {
		run = func(ctx context.Context, db *storage.DB) error {
			if d.cfg.Swapper == nil {
				return fmt.Errorf("kernel: M8 %s has no swapper (DataMigrationsConfig.Swapper)", rw.ID)
			}
			return d.cfg.Swapper.RunRewrite(ctx, db, *rw)
		}
	}
	return append(steps, storage.DataStep{Owner: rw.Owner, ID: rw.ID, After: after, Cutover: true, Rewrite: true, Run: run})
}

// Preflight is the P4a call site of the cutover preflight, before the
// checkpoint (F3-04's seam, DataMigrationsConfig.Preflight): it hands the
// hook every registered cutover step; the hook reads o's store and runs the
// pending steps' Preflight. Without the hook, or without a cutover step, it
// does nothing. Name coined by F2-06.
func (d *DataMigrations) Preflight(ctx context.Context, o *storage.Opened) error {
	if d.cfg.Preflight == nil {
		return nil
	}
	var cutover []datamigrate.DataMigration
	for _, m := range d.runner.Migrations() {
		if m.Cutover {
			cutover = append(cutover, m)
		}
	}
	if len(cutover) == 0 {
		return nil
	}
	if err := d.cfg.Preflight(ctx, o, cutover); err != nil {
		return fmt.Errorf("kernel: cutover preflight (P4a): %w", err)
	}
	return nil
}

// Stats is every migration's outcome of this boot (storagemigrate.Report).
// Name coined by F2-06.
func (d *DataMigrations) Stats(ctx context.Context, db *storage.DB) ([]storagemigrate.Stats, error) {
	return d.runner.Report(ctx, db)
}

// RecordMetrics records, once P5 has built the Provider (kernel code
// records boot metrics, never the Provider; storageMetrics' pattern):
// <ns>_storage_data_migration_items_total{migration,outcome} with this
// boot's items, <ns>_storage_data_migration_state{migration,state} (1 for
// the migration's state, 0 for the others), both on storage's scope, whose
// allowed keys include migration and outcome (storageMetrics), and
// <ns>_kernel_boot_phase_seconds{phase="P4b",step=<id>} for each migration
// the boot ran (06 §6.9). Name coined by F2-06.
func (d *DataMigrations) RecordMetrics(ctx context.Context, db *storage.DB, scope *metrics.Scope, bootPhase metric.Float64Gauge) error {
	stats, err := d.Stats(ctx, db)
	if err != nil {
		return fmt.Errorf("kernel: data migration stats: %w", err)
	}
	items, err := scope.Meter().Int64Counter(scope.Prefix()+".data_migration_items",
		metric.WithDescription("Items a data migration processed, by outcome (05-storage §5S.8)."))
	if err != nil {
		return fmt.Errorf("kernel: data migration items counter: %w", err)
	}
	state, err := scope.Meter().Int64Gauge(scope.Prefix()+".data_migration_state",
		metric.WithDescription("Each data migration's state: 1 for the state it is in, 0 for the others (05-storage §5S.3.6, §5S.8)."))
	if err != nil {
		return fmt.Errorf("kernel: data migration state gauge: %w", err)
	}
	for _, s := range stats {
		mig := attribute.String("migration", s.ID)
		for _, o := range slices.Sorted(maps.Keys(s.Items)) {
			items.Add(ctx, s.Items[o], metric.WithAttributes(mig, attribute.String("outcome", o)))
		}
		for _, st := range datamigrate.States() {
			v := int64(0)
			if st == s.State {
				v = 1
			}
			state.Record(ctx, v, metric.WithAttributes(mig, attribute.String("state", st)))
		}
		if s.Ran && bootPhase != nil {
			bootPhase.Record(ctx, s.Took.Seconds(), metric.WithAttributes(attribute.String("phase", phaseP4b), attribute.String("step", s.ID)))
		}
	}
	return nil
}
