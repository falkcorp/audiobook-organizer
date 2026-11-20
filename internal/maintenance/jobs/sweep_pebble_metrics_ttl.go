// file: internal/maintenance/jobs/sweep_pebble_metrics_ttl.go
// version: 1.4.1
// guid: b8c9d0e1-f2a3-0008-1234-000000000008
// last-edited: 2026-11-09

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

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

func init() { maintenance.Register(&sweepPebbleMetricsTTLJob{}) }

// metricsSweeper is the one call the job makes: delete the expired snapshots.
type metricsSweeper interface {
	SweepExpiredMetrics() (int64, error)
}

// resolveMetricsSweeper is the store's sweeper, or nil when it has none.
//
// A store that offers SweepExpiredMetrics itself is used as it stands. The
// books module's job store does: it cannot hand the job the Pebble store (it has
// no Unwrap and no promoted store, so that every write it makes is stamped), and
// the sweep is one such write, so it carries the sweep as a gated method of its
// own. Any other store resolves to the metrics store the way the job always did:
// NewPebbleMetricsStoreFromStore, which looks through the decorator chain
// internally. This job runs through server.maintenance_job_op, which hands it the
// Bleve search-index decorator in production, and a bare `store.(*PebbleStore)`
// took the skip branch on every prod run, so expired snapshots were never swept
// and nothing surfaced it, because skipping is a legitimate outcome on a
// non-Pebble backend. Constructing through internal/books/store also keeps
// *PebbleStore out of this package.
//
// The nil check is explicit: a nil *PebbleMetricsStore converted to the
// interface is a non-nil sweeper.
func resolveMetricsSweeper(store any) metricsSweeper {
	if s, ok := database.AsCapability[metricsSweeper](store); ok {
		return s
	}
	if m := database.NewPebbleMetricsStoreFromStore(store); m != nil {
		return m
	}
	return nil
}

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
	}{DryRun: true} // preview by default (owner 2026-09-25); send dry_run=false to apply
}

// Run sweeps expired Pebble metrics entries.
//
// The store parameter must be a *database.PebbleStore; if the server is running
// a different backend (SQLite or mock) the job is a no-op so CI tests pass.
func (j *sweepPebbleMetricsTTLJob) Run(
	ctx context.Context,
	store maintenance.JobStore,
	reporter maintenance.ProgressReporter,
	dryRun bool,
) error {
	sweeper := resolveMetricsSweeper(store)
	if sweeper == nil {
		// Not a Pebble backend — no-op (test double or SQLite fallback).
		slog.Info("sweep-pebble-metrics-ttl: store is not a PebbleStore; skipping")
		reporter.Log("info", "Store is not PebbleStore — skipped", nil)
		return nil
	}

	if dryRun {
		reporter.Log("info", "dry-run: would sweep expired Pebble metrics snapshots", nil)
		slog.Info("[sweep-pebble-metrics-ttl] dry-run: no deletions performed")
		return nil
	}

	slog.Info("[sweep-pebble-metrics-ttl] starting TTL sweep")
	deleted, err := sweeper.SweepExpiredMetrics()
	if err != nil {
		return fmt.Errorf("sweep-pebble-metrics-ttl: %w", err)
	}

	slog.Info("[sweep-pebble-metrics-ttl] sweep complete", "deleted", deleted)
	msg := fmt.Sprintf("Swept %d expired metrics snapshot(s)", deleted)
	reporter.Log("info", msg, nil)
	reporter.SetTotal(int(deleted))
	return nil
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *sweepPebbleMetricsTTLJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
