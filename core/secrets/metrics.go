// file: core/secrets/metrics.go
// version: 1.0.0
// guid: 0016918d-8e4a-4a34-a4a9-160f9b1d415a
// last-edited: 2026-11-10

package secrets

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// The op label of secrets_operations_total (05-config §2.8).
const (
	opGet    = "get"
	opPut    = "put"
	opDelete = "delete"
)

// The outcome label of secrets_operations_total.
const (
	outcomeOK             = "ok"
	outcomeNotSet         = "not_set"
	outcomeKeyUnavailable = "key_unavailable"
	outcomeCorrupt        = "corrupt"
	outcomeOutOfScope     = "out_of_scope"
	outcomeError          = "error"
)

// opKey is one series of secrets_operations_total.
type opKey struct{ op, outcome string }

// outcomeOf is err's outcome label.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return outcomeOK
	case errors.Is(err, ErrNotSet):
		return outcomeNotSet
	case errors.Is(err, ErrKeyUnavailable):
		return outcomeKeyUnavailable
	case errors.Is(err, ErrCorrupt):
		return outcomeCorrupt
	case errors.Is(err, ErrOutOfScope):
		return outcomeOutOfScope
	}
	return outcomeError
}

// counters are the store's operation tallies. Instrument's observables read
// them at collection, so an operation before the instruments exist (the
// kernel opens the store at P4b, the Provider at P5) is still counted.
type counters struct {
	mu  sync.Mutex
	ops map[opKey]uint64
	reg metric.Registration
}

// op counts one operation by its outcome.
func (c *counters) op(op string, err error) {
	k := opKey{op: op, outcome: outcomeOf(err)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ops == nil {
		c.ops = map[opKey]uint64{}
	}
	c.ops[k]++
}

// snapshot copies the operation series, every op and outcome present (zero
// until counted), so a dashboard sees each series before its first event.
func (c *counters) snapshot() map[opKey]uint64 {
	out := map[opKey]uint64{}
	for _, op := range []string{opGet, opPut, opDelete} {
		for _, oc := range []string{outcomeOK, outcomeNotSet, outcomeKeyUnavailable, outcomeCorrupt, outcomeOutOfScope, outcomeError} {
			out[opKey{op: op, outcome: oc}] = 0
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	maps.Copy(out, c.ops)
	return out
}

// unregister drops the metrics callback (Close).
func (c *counters) unregister() error {
	c.mu.Lock()
	reg := c.reg
	c.reg = nil
	c.mu.Unlock()
	if reg == nil {
		return nil
	}
	if err := reg.Unregister(); err != nil {
		return fmt.Errorf("secrets: unregister the metrics callback: %w", err)
	}
	return nil
}

// keyringCounts is secrets_keyring_keys: the loaded keys by use and status,
// a key RetireUnused retired counted as retired, every pair present.
func (s *Store) keyringCounts() (map[[2]string]int64, bool) {
	out := map[[2]string]int64{}
	for _, use := range []string{useData, useDerive} {
		for _, st := range []string{statusActive, statusVerify, statusRetired} {
			out[[2]string{use, st}] = 0
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, false
	}
	for _, k := range s.ring.file.Keys {
		st := k.Status
		if s.retired[k.ID] {
			st = statusRetired
		}
		out[[2]string{k.Use, st}]++
	}
	return out, true
}

// Instrument registers the store's instruments on sc, the core "secrets"
// scope (labels op, outcome, use, status; 05-config §2.8), as observables:
//
//	<ns>_secrets_operations_total{op,outcome}
//	<ns>_secrets_undecryptable
//	<ns>_secrets_keyring_keys{use,status}
//
// It is called once: Open does it when Options.Metrics is set, the kernel
// after P5 otherwise (a name coined by F3-03). Close unregisters the
// callback.
func (s *Store) Instrument(sc *metrics.Scope) error {
	if sc == nil {
		return errors.New("secrets: Instrument needs the core secrets scope")
	}
	s.ctr.mu.Lock()
	defer s.ctr.mu.Unlock()
	if s.ctr.reg != nil {
		return errors.New("secrets: the store is already instrumented")
	}
	m, prefix := sc.Meter(), sc.Prefix()
	ops, err := m.Int64ObservableCounter(prefix+".operations",
		metric.WithDescription("Secret reads and writes, by operation and outcome (05-config §2.8)."))
	if err != nil {
		return fmt.Errorf("secrets: operations counter: %w", err)
	}
	undecryptable, err := m.Int64ObservableGauge(prefix+".undecryptable",
		metric.WithDescription("Stored secrets that cannot be read and must be re-entered (05-config §2.5)."))
	if err != nil {
		return fmt.Errorf("secrets: undecryptable gauge: %w", err)
	}
	keyCount, err := m.Int64ObservableGauge(prefix+".keyring_keys",
		metric.WithDescription("Keys in the loaded keyring, by use and status."))
	if err != nil {
		return fmt.Errorf("secrets: keyring keys gauge: %w", err)
	}
	reg, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		for k, n := range s.ctr.snapshot() {
			o.ObserveInt64(ops, int64(n), metric.WithAttributes(
				attribute.String("op", k.op), attribute.String("outcome", k.outcome)))
		}
		counts, open := s.keyringCounts()
		if !open {
			return nil
		}
		for k, n := range counts {
			o.ObserveInt64(keyCount, n, metric.WithAttributes(
				attribute.String("use", k[0]), attribute.String("status", k[1])))
		}
		bad, err := s.Undecryptable(ctx)
		if errors.Is(err, errClosed) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("secrets: count the undecryptable secrets: %w", err)
		}
		o.ObserveInt64(undecryptable, int64(len(bad)))
		return nil
	}, ops, undecryptable, keyCount)
	if err != nil {
		return fmt.Errorf("secrets: metrics callback: %w", err)
	}
	s.ctr.reg = reg
	return nil
}
