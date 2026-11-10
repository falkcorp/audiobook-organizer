// file: core/config/metrics.go
// version: 1.0.0
// guid: 9fad5524-5619-4741-b804-98f3cc369c0f
// last-edited: 2026-11-10

package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
)

// Apply outcomes, the closed label set of mediacore_config_applies_total
// (05-config §1.8).
const (
	outcomeApplied      = "applied"
	outcomeUnknownKey   = "unknown_key"
	outcomeLocked       = "locked"
	outcomeForbidden    = "forbidden"
	outcomeProtected    = "protected"
	outcomeInvalid      = "invalid"
	outcomeStorageError = "storage_error"
)

// applyOutcomes lists every outcome, so each series exists from the first
// scrape.
func applyOutcomes() []string {
	return []string{outcomeApplied, outcomeUnknownKey, outcomeLocked, outcomeForbidden, outcomeProtected, outcomeInvalid, outcomeStorageError}
}

// outcomeOf maps an Apply error to its outcome label.
func outcomeOf(err error) string {
	switch {
	case err == nil:
		return outcomeApplied
	case errors.Is(err, ErrUnknownKey):
		return outcomeUnknownKey
	case errors.Is(err, ErrForbidden):
		return outcomeForbidden
	case errors.Is(err, ErrProtected):
		return outcomeProtected
	case errors.Is(err, ErrLocked):
		return outcomeLocked
	case errors.Is(err, ErrInvalid):
		return outcomeInvalid
	}
	return outcomeStorageError
}

// storeMetrics are the store's tallies. Instrument's observable instruments
// read them at collection, so a store built before the metrics Provider
// exists (the kernel builds it at P0, the Provider at P5) still counts what
// happened before (the pattern of core/audit).
type storeMetrics struct {
	mu          sync.Mutex
	applies     map[string]uint64
	postCommit  map[Path]uint64
	panics      map[Path]uint64
	sections    []Path
	pending     atomic.Int64
	quarantined atomic.Int64
	instrument  atomic.Bool
}

func newStoreMetrics() *storeMetrics {
	return &storeMetrics{applies: map[string]uint64{}, postCommit: map[Path]uint64{}, panics: map[Path]uint64{}}
}

func (m *storeMetrics) apply(outcome string) {
	m.mu.Lock()
	m.applies[outcome]++
	m.mu.Unlock()
}

func (m *storeMetrics) postCommitFailure(sec Path) {
	m.mu.Lock()
	m.postCommit[sec]++
	m.mu.Unlock()
}

func (m *storeMetrics) subscriberPanic(sec Path) {
	m.mu.Lock()
	m.panics[sec]++
	m.mu.Unlock()
}

// setSections records the registered sections: the closed label set of the
// section-labelled counters, each exported from zero.
func (m *storeMetrics) setSections(secs []Path) {
	m.mu.Lock()
	m.sections = slices.Clone(secs)
	m.mu.Unlock()
}

func (m *storeMetrics) setRestartPending(n int) { m.pending.Store(int64(n)) }
func (m *storeMetrics) setQuarantined(n int)    { m.quarantined.Store(int64(n)) }

// snapshot copies the labelled tallies.
func (m *storeMetrics) snapshot() (applies map[string]uint64, post, panics map[Path]uint64, secs []Path) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.applies), maps.Clone(m.postCommit), maps.Clone(m.panics), slices.Clone(m.sections)
}

// Instrument registers the store's instruments on sc, the core "config"
// scope with labels outcome and section (05-config §1.8):
//
//	<ns>_config_applies_total{outcome}
//	<ns>_config_generation
//	<ns>_config_restart_pending_keys
//	<ns>_config_quarantined_overrides
//	<ns>_config_post_commit_failures_total{section}
//	<ns>_config_subscriber_panics_total{section}
//	<ns>_config_retired_names_set
//	<ns>_config_fail_closed_overrides
//
// Every outcome and every registered section is exported from zero. The
// retired-name and fail-closed gauges read 0 until F3-02 fills those rules.
// It is called once: NewStore does it when Options.Metrics is set, the kernel
// after P5 otherwise (name coined by F3-01, as core/audit's).
func (s *Store) Instrument(sc *metrics.Scope) error {
	if sc == nil {
		return errors.New("config: Instrument needs the core config scope")
	}
	if !s.met.instrument.CompareAndSwap(false, true) {
		return errors.New("config: the store is already instrumented")
	}
	m, prefix := sc.Meter(), sc.Prefix()
	applies, err := m.Int64ObservableCounter(prefix+".applies",
		metric.WithDescription("Config Apply calls, by outcome (05-config §1.8)."))
	if err != nil {
		return fmt.Errorf("config: applies counter: %w", err)
	}
	gen, err := m.Int64ObservableGauge(prefix+".generation",
		metric.WithDescription("The config store generation: successful Applies since boot."))
	if err != nil {
		return fmt.Errorf("config: generation gauge: %w", err)
	}
	pending, err := m.Int64ObservableGauge(prefix+".restart_pending_keys",
		metric.WithDescription("Keys whose stored value takes effect at the next restart."))
	if err != nil {
		return fmt.Errorf("config: restart pending gauge: %w", err)
	}
	quar, err := m.Int64ObservableGauge(prefix+".quarantined_overrides",
		metric.WithDescription("Stored overrides ignored at boot because their section failed with them."))
	if err != nil {
		return fmt.Errorf("config: quarantined gauge: %w", err)
	}
	post, err := m.Int64ObservableCounter(prefix+".post_commit_failures",
		metric.WithDescription("AfterCommit hooks that failed after a committed Apply, by section."))
	if err != nil {
		return fmt.Errorf("config: post-commit counter: %w", err)
	}
	panics, err := m.Int64ObservableCounter(prefix+".subscriber_panics",
		metric.WithDescription("OnChange subscribers that panicked and were recovered, by section."))
	if err != nil {
		return fmt.Errorf("config: subscriber panics counter: %w", err)
	}
	retired, err := m.Int64ObservableGauge(prefix+".retired_names_set",
		metric.WithDescription("Warn-class retired config names set at this boot (05-config §1.3.6)."))
	if err != nil {
		return fmt.Errorf("config: retired names gauge: %w", err)
	}
	failClosed, err := m.Int64ObservableGauge(prefix+".fail_closed_overrides",
		metric.WithDescription("FailClosed keys whose stored value beats the operator value (05-config §1.3.5)."))
	if err != nil {
		return fmt.Errorf("config: fail-closed gauge: %w", err)
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		ap, pc, pn, secs := s.met.snapshot()
		for _, oc := range applyOutcomes() {
			o.ObserveInt64(applies, int64(ap[oc]), metric.WithAttributes(attribute.String("outcome", oc)))
		}
		for _, sec := range secs {
			attrs := metric.WithAttributes(attribute.String("section", string(sec)))
			o.ObserveInt64(post, int64(pc[sec]), attrs)
			o.ObserveInt64(panics, int64(pn[sec]), attrs)
		}
		o.ObserveInt64(gen, int64(s.gen.Load()))
		o.ObserveInt64(pending, s.met.pending.Load())
		o.ObserveInt64(quar, s.met.quarantined.Load())
		o.ObserveInt64(retired, 0)
		o.ObserveInt64(failClosed, 0)
		return nil
	}, applies, gen, pending, quar, post, panics, retired, failClosed)
	if err != nil {
		return fmt.Errorf("config: metrics callback: %w", err)
	}
	return nil
}
