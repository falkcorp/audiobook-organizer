// file: core/kernel/cutover.go
// version: 1.0.0
// guid: aa530b78-4ec9-41a1-85a3-d0075f9bcdff
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// The kernel half of F3-04's cutover machinery (05-storage §5S.3.13): the
// P4a preflight hook (DataMigrationsConfig.Preflight), the boot's exit-code
// mapping, the data-migration runner's level-step seam, the quarantine's
// health and metric, and the offline quarantine commands' operations.
// Nothing here runs in a boot until F2-16 wires DataMigrations and F3-07
// registers the cutover steps: every entry point is a seam the wiring sets.

// ExitConfig is the exit status of a boot refused for a reason the operator
// must act on before any restart can help (sysexits EX_CONFIG): the cutover
// blocked (storage.ErrCutoverBlocked), the store rewrite swapped back twice
// (storage.ErrRewriteSwappedBack), and a store too new for this build
// (*storage.LevelTooNewError, 05-storage §5S.3.7). A supervisor that restarts
// on failure must not restart on it.
const ExitConfig = 78

// ExitCode maps a boot's error to the process's exit status: 0 for nil,
// ExitConfig for the refusals above, 1 for any other error. The cmd layer
// (F2-16) exits with it.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, storage.ErrCutoverBlocked) || errors.Is(err, storage.ErrRewriteSwappedBack) {
		return ExitConfig
	}
	if _, ok := errors.AsType[*storage.LevelTooNewError](err); ok {
		return ExitConfig
	}
	return 1
}

// LevelStep is the data-migration runner's level-step seam
// (DataMigrationsConfig.LevelStep): storage's level step procedure over the
// boot's store (§5S.3.7; (*storage.DB).RaiseLevel).
func LevelStep(ctx context.Context, db *storage.DB, owner storage.Owner, level int) error {
	return db.RaiseLevel(ctx, owner, level)
}

// CutoverPreflight is the P4a cutover preflight (DataMigrationsConfig.
// Preflight; 06 §6.5.1 P4 (a); §5S.4 step 5a). Before the headroom check and
// the checkpoint, and only when the cutover has not started on o's store
// (storage.(*Opened).CutoverStarted: no owner's level at M7's target and no
// cutover step with a record in c/storage/dm/), it calls every cutover
// step's Preflight over its source, read-only. Any blocker refuses the boot
// with a *storage.CutoverBlockedError (errors.Is storage.ErrCutoverBlocked;
// ExitCode 78) having written nothing, and logs the report: each blocker's
// step, key, family, reason and fix, never a value, then the line naming
// `storage cutover-blockers`. A resuming boot skips it. log nil discards.
func CutoverPreflight(log *slog.Logger) func(ctx context.Context, o *storage.Opened, cutover []datamigrate.DataMigration) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return func(ctx context.Context, o *storage.Opened, cutover []datamigrate.DataMigration) error {
		blockers, skipped, err := cutoverBlockers(ctx, o, cutover)
		switch {
		case err != nil:
			return err
		case skipped:
			log.Info("kernel: cutover preflight skipped: the cutover has started on this store")
			return nil
		case len(blockers) == 0:
			log.Info("kernel: cutover preflight passed", "steps", len(cutover))
			return nil
		}
		refusal := &storage.CutoverBlockedError{Blockers: blockers}
		for _, line := range refusal.Report() {
			log.Error("kernel: " + line)
		}
		return refusal
	}
}

// cutoverBlockers runs every cutover step's Preflight over o's store unless
// the cutover started (skipped). It writes nothing.
func cutoverBlockers(ctx context.Context, o *storage.Opened, cutover []datamigrate.DataMigration) (blockers []storage.CutoverBlocker, skipped bool, err error) {
	steps := make([]storage.CutoverStep, 0, len(cutover))
	for _, m := range cutover {
		steps = append(steps, storage.CutoverStep{Owner: m.Owner, ID: m.ID, RaisesLevel: m.RaisesLevel})
	}
	started, err := o.CutoverStarted(ctx, steps)
	if err != nil {
		return nil, false, fmt.Errorf("kernel: cutover preflight: %w", err)
	}
	if started {
		return nil, true, nil
	}
	for _, m := range cutover {
		if m.Preflight == nil {
			continue
		}
		src, err := o.PreflightSource(m.Owner)
		if err != nil {
			return nil, false, fmt.Errorf("kernel: cutover preflight of %s: %w", m.ID, err)
		}
		found, err := m.Preflight(ctx, src)
		if err != nil {
			return nil, false, fmt.Errorf("kernel: cutover preflight of %s: %w", m.ID, err)
		}
		for _, b := range found {
			step := b.Step
			if step == "" {
				step = m.ID
			}
			blockers = append(blockers, storage.CutoverBlocker{Step: step, Key: b.Key, Family: b.Family, Reason: b.Reason, Fix: b.Fix})
		}
	}
	return blockers, false, nil
}

// QuarantineDetail is the storage_quarantine part of core.storage's health
// (05-storage §5S.3.13 "Quarantine"; §5S.5): a warning carried in the
// result's Detail, which only the authenticated health route shows, never in
// its Reason, which unauthenticated probes see. It names, per step, the
// records and the sealed sensitive ones, and the orphaned references each
// referencing family reports (Verify's OrphanedRefs, which the caller sums
// from the runner's reports). "" when nothing is quarantined and nothing is
// orphaned: the warning clears.
func QuarantineDetail(counts map[string]storage.QuarantineCount, orphaned map[string]int64) string {
	var parts []string
	for _, s := range storage.QuarantineSteps(counts) {
		c := counts[s]
		parts = append(parts, fmt.Sprintf("step=%s rows=%d sensitive=%d", s, c.Rows, c.Sensitive))
	}
	var refs int64
	for _, n := range orphaned {
		refs += n
	}
	if len(parts) == 0 && refs == 0 {
		return ""
	}
	return fmt.Sprintf("storage_quarantine: %s orphaned_refs=%d (storage quarantine list)", strings.Join(parts, "; "), refs)
}

// StorageHealth is core.storage's result once the store is up: Up, with the
// quarantine's warning in Detail when there is one (QuarantineDetail). The
// wiring (F3-07) reports it after P4b and after a purge.
func StorageHealth(ctx context.Context, db *storage.DB, orphaned map[string]int64, now time.Time) (health.Result, error) {
	counts, err := db.QuarantineCounts(ctx)
	if err != nil {
		return health.Result{}, fmt.Errorf("kernel: quarantine counts: %w", err)
	}
	return health.Result{Status: health.StatusUp, Detail: QuarantineDetail(counts, orphaned), At: now}, nil
}

// QuarantineGauge records <ns>_storage_quarantine_rows{step, sensitive} on
// storage's scope (§5S.8): per step, the sealed sensitive rows and the
// others. It remembers every step it has recorded, so a step whose records
// are gone (a purge) records 0 for both instead of keeping its last value;
// a step with none of one kind records 0 for it. The wiring (F3-07) builds
// one per process and records after P4b and after a purge. Name coined by
// F3-04.
type QuarantineGauge struct {
	g    metric.Int64Gauge
	mu   sync.Mutex
	seen map[string]bool
}

// NewQuarantineGauge registers the gauge on scope.
func NewQuarantineGauge(scope *metrics.Scope) (*QuarantineGauge, error) {
	g, err := scope.Meter().Int64Gauge(scope.Prefix()+".quarantine_rows",
		metric.WithDescription("Quarantined rows of the cutover steps, by step and whether the row was sealed as sensitive (05-storage §5S.8)."))
	if err != nil {
		return nil, fmt.Errorf("kernel: quarantine rows gauge: %w", err)
	}
	return &QuarantineGauge{g: g, seen: map[string]bool{}}, nil
}

// Record records db's quarantine counts.
func (q *QuarantineGauge) Record(ctx context.Context, db *storage.DB) error {
	counts, err := db.QuarantineCounts(ctx)
	if err != nil {
		return fmt.Errorf("kernel: quarantine counts: %w", err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, s := range storage.QuarantineSteps(counts) {
		q.seen[s] = true
	}
	for _, s := range slices.Sorted(maps.Keys(q.seen)) {
		c := counts[s]
		step := attribute.String("step", s)
		q.g.Record(ctx, int64(c.Sensitive), metric.WithAttributes(step, attribute.String("sensitive", "true")))
		q.g.Record(ctx, int64(c.Rows-c.Sensitive), metric.WithAttributes(step, attribute.String("sensitive", "false")))
	}
	return nil
}

// QuarantineList is `storage quarantine list` over a read-only offline
// open: every record (one step's when step is set). The caller prints keys,
// reasons and sizes, never a value.
func (o *Offline) QuarantineList(ctx context.Context, step string) ([]storage.QuarantineEntry, error) {
	return o.db.Quarantine(ctx, step)
}

// QuarantineExport is `storage quarantine export`: the records with their
// values as JSON Lines to w, sealed rows opened through unseal.
func (o *Offline) QuarantineExport(ctx context.Context, step string, unseal storage.QuarantineUnsealer, w io.Writer) (int, error) {
	return o.db.ExportQuarantine(ctx, step, unseal, w)
}

// QuarantinePurge is `storage quarantine purge --step <id> --confirm` over a
// read-write offline open: step's records and their sealed secret records
// deleted in one batch with the core.storage.quarantine_purge audit event
// (PurgeQuarantine). by names the operator for the event.
func (o *Offline) QuarantinePurge(ctx context.Context, step string, del storage.QuarantineSecretDeleter, by string) (int, error) {
	return PurgeQuarantine(ctx, o.db, step, del, by, o.k.coreLog("storage"))
}

// PurgeQuarantine deletes step's quarantine on db, audited in the same
// batch: the audit store is core/audit's keyspace on db, so a store opened
// without it refuses the purge before it deletes anything (fail-closed,
// audit.Logger's rule).
func PurgeQuarantine(ctx context.Context, db *storage.DB, step string, del storage.QuarantineSecretDeleter, by string, log *slog.Logger) (int, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	as, err := buildAudit(ctx, db, log, time.Now)
	if err != nil {
		return 0, fmt.Errorf("kernel: quarantine purge is audited: %w", err)
	}
	n, err := db.PurgeQuarantine(ctx, step, del, func(b *storage.Batch, rows int) error {
		return as.RecordIn(ctx, b, audit.Event{
			Action:  "core.storage.quarantine_purge",
			Outcome: audit.Success,
			Actor:   audit.Actor{Username: by, Method: "offline"},
			Reason:  "operator_purge",
			Details: map[string]any{"step": step, "rows": rows},
		})
	})
	if err != nil {
		return 0, fmt.Errorf("kernel: quarantine purge of %s: %w", step, err)
	}
	log.Info("kernel: quarantine purged", "step", step, "rows", n, "by", by)
	return n, nil
}
