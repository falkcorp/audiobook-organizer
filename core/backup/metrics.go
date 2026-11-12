// file: core/backup/metrics.go
// version: 1.0.0
// guid: c0d5d6a2-6e2c-4a87-8c61-90a3f6d6b2f8
// last-edited: 2026-11-12

package backup

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// ScopeAttributes are the label keys the backup scope declares: pass them to
// metrics.Provider.Core("backup", ScopeAttributes...).
var ScopeAttributes = []attribute.Key{"reason", "outcome", "phase", "format", "provenance", "level", "stage", "foreign", "kind", "cause"}

// Metrics are 05-backup §5B.8's series. A nil *Metrics records nothing, so a
// Service built without a scope runs unchanged. The two revocation-journal
// series (gaps, reapplied) are set by the journal component, which F8-04 binds
// to these methods.
type Metrics struct {
	runs, verify, restore, reapplied metric.Int64Counter
	duration                         metric.Float64Histogram
	size, lastOK, lastVerified       metric.Int64Gauge
	archives, retained, gaps         metric.Int64Gauge
}

// NewMetrics registers the families on sc (the core "backup" scope).
func NewMetrics(sc *metrics.Scope) (*Metrics, error) {
	if sc == nil {
		return nil, fmt.Errorf("backup: NewMetrics needs the core backup scope")
	}
	m, p := sc.Meter(), sc.Prefix()
	var (
		out Metrics
		err error
	)
	counter := func(dst *metric.Int64Counter, name, desc string) {
		if err == nil {
			*dst, err = m.Int64Counter(p+"."+name, metric.WithDescription(desc))
		}
	}
	gauge := func(dst *metric.Int64Gauge, name, unit, desc string) {
		if err == nil {
			*dst, err = m.Int64Gauge(p+"."+name, metric.WithUnit(unit), metric.WithDescription(desc))
		}
	}
	counter(&out.runs, "runs", "Backups started, by reason and outcome (05-backup §5B.8).")
	counter(&out.verify, "verify", "Archive verifications, by level and outcome.")
	counter(&out.restore, "restore", "Restore events by stage (staged, applied, rejected, swapped_back, reverted) and whether foreign.")
	counter(&out.reapplied, "revocation.reapplied", "Revocations re-applied, by kind and cause (boot, restore).")
	gauge(&out.size, "size", "By", "Size of the newest archive.")
	gauge(&out.lastOK, "last_success_timestamp", "s", "Unix time of the last successful backup.")
	gauge(&out.lastVerified, "last_verified_timestamp", "s", "Unix time of the last successful verification.")
	gauge(&out.archives, "archives", "", "Archives in the catalogue, by format and provenance.")
	gauge(&out.retained, "retained", "By", "Bytes the catalogue retains, sidecars included.")
	gauge(&out.gaps, "revocation.journal_gaps", "", "Open gaps in the revocation journal.")
	if err == nil {
		out.duration, err = m.Float64Histogram(p+".duration", metric.WithUnit("s"),
			metric.WithDescription("Backup phase durations (checkpoint, archive, checksum, verify)."))
	}
	if err != nil {
		return nil, fmt.Errorf("backup: metrics: %w", err)
	}
	return &out, nil
}

// Run counts one backup by reason and outcome ("ok", "error", "canceled").
func (m *Metrics) Run(ctx context.Context, reason, outcome string) {
	if m != nil {
		m.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason), attribute.String("outcome", outcome)))
	}
}

// Phase records how long one backup phase took.
func (m *Metrics) Phase(ctx context.Context, phase string, d time.Duration) {
	if m != nil {
		m.duration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("phase", phase)))
	}
}

// Verified counts one verification and, when it passed, stamps the time.
func (m *Metrics) Verified(ctx context.Context, l VerifyLevel, ok bool, at time.Time) {
	if m == nil {
		return
	}
	out := "ok"
	if !ok {
		out = "error"
	} else {
		m.lastVerified.Record(ctx, at.Unix())
	}
	m.verify.Add(ctx, 1, metric.WithAttributes(attribute.String("level", l.String()), attribute.String("outcome", out)))
}

// Restore counts a restore event; stage is staged, applied, rejected,
// swapped_back or reverted.
func (m *Metrics) Restore(ctx context.Context, stage string, foreign bool) {
	if m != nil {
		m.restore.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", stage), attribute.Bool("foreign", foreign)))
	}
}

// Created records a successful backup's size and time.
func (m *Metrics) Created(ctx context.Context, size int64, at time.Time) {
	if m != nil {
		m.size.Record(ctx, size)
		m.lastOK.Record(ctx, at.Unix())
	}
}

// Catalogue records the catalogue's shape after a reconcile: archives by
// format and provenance, and the bytes retained.
func (m *Metrics) Catalogue(ctx context.Context, byKind map[[2]string]int64, retainedBytes int64) {
	if m == nil {
		return
	}
	for k, n := range byKind {
		m.archives.Record(ctx, n, metric.WithAttributes(attribute.String("format", k[0]), attribute.String("provenance", k[1])))
	}
	m.retained.Record(ctx, retainedBytes)
}

// JournalGaps is the revocation journal's open-gap count (set by the journal component).
func (m *Metrics) JournalGaps(ctx context.Context, n int64) {
	if m != nil {
		m.gaps.Record(ctx, n)
	}
}

// Reapplied counts revocation lines re-applied (set by the boot's re-apply).
func (m *Metrics) Reapplied(ctx context.Context, kind, cause string, n int64) {
	if m != nil {
		m.reapplied.Add(ctx, n, metric.WithAttributes(attribute.String("kind", kind), attribute.String("cause", cause)))
	}
}
