// file: core/audit/metrics.go
// version: 1.0.0
// guid: ffb1852e-0096-4611-a141-42810be96120
// last-edited: 2026-11-10

package audit

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// The values of audit_write_errors_total's path label (05-rbac §11).
const (
	pathSync  = "sync"
	pathAsync = "async"
)

// recordKey is one series of audit_records_total.
type recordKey struct {
	namespace string
	outcome   Outcome
}

// counters are the store's own tallies. The instruments of Instrument read
// them when the registry is collected, so nothing is recorded on the write
// path but an increment, and a record written before the instruments exist
// (the kernel builds the store before P4b, the metrics Provider at P5) is
// still counted.
type counters struct {
	mu      sync.Mutex
	records map[recordKey]uint64

	dropped    atomic.Uint64
	syncErrs   atomic.Uint64
	asyncErrs  atomic.Uint64
	pruned     atomic.Uint64 // nothing prunes until the tasks flip registers core.audit.prune
	instrument atomic.Bool
}

// record counts one record by the namespace of its action and its outcome.
func (c *counters) record(r Record) {
	k := recordKey{namespace: namespaceOf(r.Action), outcome: r.Outcome}
	c.mu.Lock()
	if c.records == nil {
		c.records = map[recordKey]uint64{}
	}
	c.records[k]++
	c.mu.Unlock()
}

// drop counts n events lost: a full queue, a stopped store, a refused event,
// a lost batch.
func (c *counters) drop(n int) {
	if n > 0 {
		c.dropped.Add(uint64(n))
	}
}

// writeError counts one failed write on path.
func (c *counters) writeError(path string) {
	if path == pathAsync {
		c.asyncErrs.Add(1)
		return
	}
	c.syncErrs.Add(1)
}

// snapshotRecords copies the records series.
func (c *counters) snapshotRecords() map[recordKey]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[recordKey]uint64, len(c.records))
	maps.Copy(out, c.records)
	return out
}

// Instrument registers the store's instruments on sc, the core "audit" scope
// (05-rbac §11; labels namespace, outcome and path), as observables over the
// store's counters:
//
//	<ns>_audit_records_total{namespace,outcome}
//	<ns>_audit_dropped_total
//	<ns>_audit_queue_depth
//	<ns>_audit_write_errors_total{path}
//	<ns>_audit_pruned_total
//
// Both paths of write_errors are always exported, so a dashboard sees a zero
// before the first failure. audit_pruned_total stays 0 until the tasks flip
// registers core.audit.prune. It is called once: New does it when Deps.Metrics
// is set, the kernel after P5 when the store was built before the Provider
// existed.
func (s *Store) Instrument(sc *metrics.Scope) error {
	if sc == nil {
		return errors.New("audit: Instrument needs the core audit scope")
	}
	if !s.ctr.instrument.CompareAndSwap(false, true) {
		return errors.New("audit: the store is already instrumented")
	}
	m, prefix := sc.Meter(), sc.Prefix()
	records, err := m.Int64ObservableCounter(prefix+".records",
		metric.WithDescription("Audit records written, by the namespace of the action and the outcome (05-rbac §11)."))
	if err != nil {
		return fmt.Errorf("audit: records counter: %w", err)
	}
	dropped, err := m.Int64ObservableCounter(prefix+".dropped",
		metric.WithDescription("Async audit events lost: a full queue, a stopped store, a refused event or a failed batch (05-rbac §7.3)."))
	if err != nil {
		return fmt.Errorf("audit: dropped counter: %w", err)
	}
	depth, err := m.Int64ObservableGauge(prefix+".queue_depth",
		metric.WithDescription("Async audit events waiting for the writer."))
	if err != nil {
		return fmt.Errorf("audit: queue depth gauge: %w", err)
	}
	werrs, err := m.Int64ObservableCounter(prefix+".write_errors",
		metric.WithDescription("Failed audit writes, by path: sync (Record, RecordIn) or async (the writer)."))
	if err != nil {
		return fmt.Errorf("audit: write errors counter: %w", err)
	}
	pruned, err := m.Int64ObservableCounter(prefix+".pruned",
		metric.WithDescription("Audit records deleted by retention."))
	if err != nil {
		return fmt.Errorf("audit: pruned counter: %w", err)
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for k, n := range s.ctr.snapshotRecords() {
			o.ObserveInt64(records, int64(n), metric.WithAttributes(
				attribute.String("namespace", k.namespace), attribute.String("outcome", string(k.outcome))))
		}
		o.ObserveInt64(dropped, int64(s.ctr.dropped.Load()))
		o.ObserveInt64(depth, int64(len(s.q)))
		o.ObserveInt64(werrs, int64(s.ctr.syncErrs.Load()), metric.WithAttributes(attribute.String("path", pathSync)))
		o.ObserveInt64(werrs, int64(s.ctr.asyncErrs.Load()), metric.WithAttributes(attribute.String("path", pathAsync)))
		o.ObserveInt64(pruned, int64(s.ctr.pruned.Load()))
		return nil
	}, records, dropped, depth, werrs, pruned)
	if err != nil {
		return fmt.Errorf("audit: metrics callback: %w", err)
	}
	return nil
}
