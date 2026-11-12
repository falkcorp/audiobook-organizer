// file: core/library/metrics.go
// version: 1.0.2
// guid: be17e4e1-84d1-4523-8afd-27fe5f683cef
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
)

// MetricLabels are the attribute keys the core "library" scope declares
// (05-library 5L.8); the kernel passes them to Provider.Core("library", ...).
func MetricLabels() []attribute.Key {
	return []attribute.Key{"root", "mode", "did", "outcome", "reason", "kind", "owner", "state"}
}

// The values of refusals' reason label.
const (
	reasonEscape    = "escape"
	reasonExists    = "exists"
	reasonProtected = "protected"
	reasonSpace     = "space"
	reasonUnmounted = "unmounted"
)

// refusalReason names the 5L.8 refusal an error is, or "".
func refusalReason(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrEscapesRoot):
		return reasonEscape
	case errors.Is(err, fs.ErrExist):
		return reasonExists
	case errors.Is(err, ErrSourceProtected), errors.Is(err, ErrReservedPath):
		// A reserved area (a root's AppDir or Quarantine) is a protected path
		// to a caller that may not touch it; the label set is 5L.8's.
		return reasonProtected
	case errors.Is(err, ErrInsufficientSpace):
		return reasonSpace
	case errors.Is(err, ErrNotMounted):
		return reasonUnmounted
	}
	return ""
}

type transferKey struct{ mode, did, outcome string }
type watchKey struct {
	root RootID
	kind string
}

// counters are the manager's own tallies. The instruments read them when the
// registry is collected, so the hot paths only increment, and work done before
// the instruments exist (the kernel builds the manager before the metrics
// Provider) is still counted.
type counters struct {
	mu          sync.Mutex
	transfers   map[transferKey]uint64
	bytes       map[string]uint64 // by did
	hashBytes   map[RootID]uint64
	watchEvents map[watchKey]uint64
	rescans     map[watchKey]uint64 // root, reason
	refusals    map[string]uint64
	duration    atomic.Pointer[metric.Float64Histogram]
}

func newCounters() *counters {
	return &counters{
		transfers: map[transferKey]uint64{}, bytes: map[string]uint64{}, hashBytes: map[RootID]uint64{},
		watchEvents: map[watchKey]uint64{}, rescans: map[watchKey]uint64{}, refusals: map[string]uint64{},
	}
}

// recordTransfer counts one finished Transfer call.
func (c *counters) recordTransfer(mode TransferMode, res TransferResult, err error) {
	outcome, did := "ok", res.Did.String()
	reason := refusalReason(err)
	switch {
	case err == nil:
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		outcome = "canceled"
	case reason != "":
		outcome = "refused"
	default:
		outcome = "error"
	}
	if res.Did == TransferInvalid {
		did = "none"
	}
	c.mu.Lock()
	c.transfers[transferKey{mode.String(), did, outcome}]++
	if err == nil {
		c.bytes[did] += uint64(max(res.Bytes, 0))
	}
	if reason != "" {
		c.refusals[reason]++
	}
	c.mu.Unlock()
	if err == nil {
		if h := c.duration.Load(); h != nil {
			(*h).Record(context.Background(), res.Duration.Seconds(), metric.WithAttributes(attribute.String("did", did)))
		}
	}
}

// refused counts err as a refusal when it is one.
func (c *counters) refused(err error) {
	if reason := refusalReason(err); reason != "" {
		c.mu.Lock()
		c.refusals[reason]++
		c.mu.Unlock()
	}
}

func (c *counters) addHashBytes(root RootID, n int64) {
	c.mu.Lock()
	c.hashBytes[root] += uint64(max(n, 0))
	c.mu.Unlock()
}

func (c *counters) watchEvent(root RootID, kind EventKind) {
	c.mu.Lock()
	c.watchEvents[watchKey{root, kind.String()}]++
	c.mu.Unlock()
}

func (c *counters) rescan(root RootID, reason string) {
	c.mu.Lock()
	c.rescans[watchKey{root, reason}]++
	c.mu.Unlock()
}

// recordRecovery is a no-op tally hook: recovery's outcome is its task result.
func (c *counters) recordRecovery(RecoverReport) {}

type snapshot struct {
	transfers   map[transferKey]uint64
	bytes       map[string]uint64
	hashBytes   map[RootID]uint64
	watchEvents map[watchKey]uint64
	rescans     map[watchKey]uint64
	refusals    map[string]uint64
}

func (c *counters) snapshot() snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return snapshot{maps.Clone(c.transfers), maps.Clone(c.bytes), maps.Clone(c.hashBytes),
		maps.Clone(c.watchEvents), maps.Clone(c.rescans), maps.Clone(c.refusals)}
}

// transferBuckets are the seconds buckets of transfer_duration: a rename is
// milliseconds, a copy of a large audiobook minutes.
func transferBuckets() []float64 {
	return []float64{0.01, 0.05, 0.25, 1, 5, 15, 60, 300, 900, 3600}
}

// instrument registers the instruments on sc, the core "library" scope
// (05-library 5L.8):
//
//	<ns>_library_transfers_total{mode,did,outcome}
//	<ns>_library_transfer_bytes_total{did}
//	<ns>_library_transfer_duration_seconds{did}
//	<ns>_library_hash_bytes_total{root}
//	<ns>_library_root_avail_bytes{root}
//	<ns>_library_root_state{root,state}
//	<ns>_library_watch_events_total{root,kind}
//	<ns>_library_watch_rescans_total{root,reason}
//	<ns>_library_quarantined{owner}
//	<ns>_library_refusals_total{reason}
//
// The counters and gauges are observables over the manager's tallies and the
// live registry; the duration is a histogram recorded when a transfer ends.
func (m *manager) instrument(sc *metrics.Scope) error {
	mt, prefix := sc.Meter(), sc.Prefix()
	fail := func(what string, err error) error { return fmt.Errorf("library: %s: %w", what, err) }
	hname := prefix + ".transfer_duration"
	if err := sc.View(hname, transferBuckets()...); err != nil {
		return fail("duration view", err)
	}
	hist, err := mt.Float64Histogram(hname, metric.WithUnit("s"),
		metric.WithDescription("Duration of successful transfers, by what the transfer did."))
	if err != nil {
		return fail("duration histogram", err)
	}
	m.ctr.duration.Store(&hist)
	transfers, err := mt.Int64ObservableCounter(prefix+".transfers",
		metric.WithDescription("Transfers by requested mode, what they did, and outcome."))
	if err != nil {
		return fail("transfers counter", err)
	}
	tbytes, err := mt.Int64ObservableCounter(prefix+".transfer_bytes",
		metric.WithDescription("Bytes delivered by successful transfers, by what they did."))
	if err != nil {
		return fail("transfer bytes counter", err)
	}
	hbytes, err := mt.Int64ObservableCounter(prefix+".hash_bytes",
		metric.WithDescription("Bytes hashed by Manager.Hash, by root."))
	if err != nil {
		return fail("hash bytes counter", err)
	}
	avail, err := mt.Int64ObservableGauge(prefix+".root_avail_bytes",
		metric.WithDescription("Space available to the service on the filesystem of each root."))
	if err != nil {
		return fail("avail gauge", err)
	}
	state, err := mt.Int64ObservableGauge(prefix+".root_state",
		metric.WithDescription("1 for each root's current state (ok, unmounted, disabled, degraded), else 0."))
	if err != nil {
		return fail("state gauge", err)
	}
	wevents, err := mt.Int64ObservableCounter(prefix+".watch_events",
		metric.WithDescription("Watch events delivered, by root and kind."))
	if err != nil {
		return fail("watch events counter", err)
	}
	rescans, err := mt.Int64ObservableCounter(prefix+".watch_rescans",
		metric.WithDescription("Rescan events, by root and why events were lost (overflow, add_failed, poll)."))
	if err != nil {
		return fail("watch rescans counter", err)
	}
	quarantined, err := mt.Int64ObservableGauge(prefix+".quarantined",
		metric.WithDescription("Items held in quarantine, by owner."))
	if err != nil {
		return fail("quarantined gauge", err)
	}
	refusals, err := mt.Int64ObservableCounter(prefix+".refusals",
		metric.WithDescription("Operations library refused, by reason."))
	if err != nil {
		return fail("refusals counter", err)
	}
	_, err = mt.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		s := m.ctr.snapshot()
		for k, n := range s.transfers {
			o.ObserveInt64(transfers, int64(n), metric.WithAttributes(
				attribute.String("mode", k.mode), attribute.String("did", k.did), attribute.String("outcome", k.outcome)))
		}
		for did, n := range s.bytes {
			o.ObserveInt64(tbytes, int64(n), metric.WithAttributes(attribute.String("did", did)))
		}
		for root, n := range s.hashBytes {
			o.ObserveInt64(hbytes, int64(n), metric.WithAttributes(attribute.String("root", string(root))))
		}
		for k, n := range s.watchEvents {
			o.ObserveInt64(wevents, int64(n), metric.WithAttributes(attribute.String("root", string(k.root)), attribute.String("kind", k.kind)))
		}
		for k, n := range s.rescans {
			o.ObserveInt64(rescans, int64(n), metric.WithAttributes(attribute.String("root", string(k.root)), attribute.String("reason", k.kind)))
		}
		for reason, n := range s.refusals {
			o.ObserveInt64(refusals, int64(n), metric.WithAttributes(attribute.String("reason", reason)))
		}
		reg := m.reg.Load()
		for _, id := range reg.order {
			r := reg.byID[id]
			root := attribute.String("root", string(id))
			if u, err := r.Usage(ctx); err == nil {
				o.ObserveInt64(avail, int64(min(u.Avail, 1<<62)), metric.WithAttributes(root))
			}
			cur := r.state(ctx)
			for st := stateOK; st <= stateDegraded; st++ {
				v := int64(0)
				if st == cur {
					v = 1
				}
				o.ObserveInt64(state, v, metric.WithAttributes(root, attribute.String("state", st.String())))
			}
		}
		counts := map[storage.Owner]int64{}
		for _, r := range reg.byID {
			counts[r.spec.Owner] += 0 // an owner with a root exports 0 until something is held
		}
		if recs, err := m.heldByOwner(ctx); err == nil {
			maps.Copy(counts, recs)
		}
		for owner, n := range counts {
			o.ObserveInt64(quarantined, n, metric.WithAttributes(attribute.String("owner", string(owner))))
		}
		return nil
	}, transfers, tbytes, hbytes, avail, state, wevents, rescans, quarantined, refusals)
	if err != nil {
		return fail("callback", err)
	}
	return nil
}
