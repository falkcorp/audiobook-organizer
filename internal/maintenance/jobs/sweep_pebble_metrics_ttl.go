// file: internal/maintenance/jobs/sweep_pebble_metrics_ttl.go
// version: 1.1.0
// guid: b8c9d0e1-f2a3-0008-1234-000000000008
// last-edited: 2026-07-31

// Package jobs — maintenance job: sweep expired Pebble metrics snapshots.
//
// WHY: PebbleMetricsStore has no built-in per-key TTL (unlike NutsDB which
// handled expiry automatically). This job calls PebbleMetricsStore.SweepExpiredMetrics
// to delete snapshots whose embedded ExpiresAt has passed, preserving the 30-day
// retention window. Run it on the same schedule as other cleanup tasks.
package jobs

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&sweepPebbleMetricsTTLJob{}) }

type sweepPebbleMetricsTTLJob struct{}

func (j *sweepPebbleMetricsTTLJob) ID() string       { return "sweep-pebble-metrics-ttl" }
func (j *sweepPebbleMetricsTTLJob) Name() string     { return "Sweep Pebble Metrics TTL" }
func (j *sweepPebbleMetricsTTLJob) Category() string { return "cleanup" }
func (j *sweepPebbleMetricsTTLJob) Description() string {
	return "Delete expired cache-stats snapshots from the Pebble metrics store (30-day TTL)"
}
func (j *sweepPebbleMetricsTTLJob) CanResume() bool { return false }
func (j *sweepPebbleMetricsTTLJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: false}
}

// Run sweeps expired Pebble metrics entries.
//
// The store parameter must be a *database.PebbleStore; if the server is running
// a different backend (SQLite or mock) the job is a no-op so CI tests pass.
func (j *sweepPebbleMetricsTTLJob) Run(
	ctx context.Context,
	store database.Store,
	reporter maintenance.ProgressReporter,
	dryRun bool,
) error {
	// AsPebbleStore, not a bare assertion: this job runs through
	// server.maintenance_job_op, which resolves Server.Store() at op-run time and
	// therefore hands us the Bleve search-index decorator in production. A bare
	// `store.(*database.PebbleStore)` failed against that wrapper, so this job took
	// the skip branch below on every prod run and expired metrics snapshots were
	// never swept. Nothing surfaced it because skipping is a legitimate outcome on
	// a non-Pebble backend.
	ps := database.AsPebbleStore(store)
	if ps == nil {
		// Not a Pebble backend — no-op (test double or SQLite fallback).
		slog.Info("sweep-pebble-metrics-ttl: store is not a PebbleStore; skipping")
		reporter.Log("info", "Store is not PebbleStore — skipped", nil)
		return nil
	}

	metricsStore := database.NewPebbleMetricsStore(ps.DB())

	if dryRun {
		reporter.Log("info", "dry-run: would sweep expired Pebble metrics snapshots", nil)
		slog.Info("[sweep-pebble-metrics-ttl] dry-run: no deletions performed")
		return nil
	}

	slog.Info("[sweep-pebble-metrics-ttl] starting TTL sweep")
	deleted, err := metricsStore.SweepExpiredMetrics()
	if err != nil {
		return fmt.Errorf("sweep-pebble-metrics-ttl: %w", err)
	}

	slog.Info("[sweep-pebble-metrics-ttl] sweep complete", "deleted", deleted)
	msg := fmt.Sprintf("Swept %d expired metrics snapshot(s)", deleted)
	reporter.Log("info", msg, nil)
	reporter.SetTotal(int(deleted))
	return nil
}
