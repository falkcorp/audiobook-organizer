// file: core/kernel/logmetrics.go
// version: 1.1.0
// guid: 6a3e8c14-9d52-4b70-b1f6-e4c05d27a9b8
// last-edited: 2026-11-13

package kernel

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
)

// logMetricsOption configures registerLogMetrics. It is unexported: the one
// option is for the kernel's own wiring (F5-18), which hands the bridge the
// journal tap's counters once the tap exists.
type logMetricsOption func(*logMetricsConfig)

type logMetricsConfig struct {
	journal func() logjournal.TapCounters
}

// withJournalCounters names the journal tap's counters as the source of the two
// journal families. Without it the families observe 0: the series exist from
// boot, as 05-logging §9 lists them, and a journal that is not running reads as
// no errors and no lag, not as an absent series.
func withJournalCounters(f func() logjournal.TapCounters) logMetricsOption {
	return func(c *logMetricsConfig) { c.journal = f }
}

// registerLogMetrics is the bridge of 05-logging §9 (log cannot import
// metrics, 03 R4): it takes the core scope mediacore.log (the app's namespace
// applies) and registers observable counters that read l.Counters() when the
// registry is collected:
//
//	<ns>_log_records_total{level,module}
//	<ns>_log_dropped_total{sink}
//	<ns>_log_redactions_total{kind}
//	<ns>_log_journal_write_errors_total
//	<ns>_log_journal_lag_seconds
//
// The first three read l.Counters(); the journal's two read the journal tap's
// own counters (logjournal.TapCounters), which are not part of log.Counters
// because the tap lives in core/internal/logjournal, above log. Nothing is
// recorded on the logging path: the pipeline's own atomics are the source, so a
// record costs no more than it did.
func registerLogMetrics(p *metrics.Provider, l *log.Logging, opts ...logMetricsOption) (*metrics.Scope, error) {
	var cfg logMetricsConfig
	for _, o := range opts {
		o(&cfg)
	}
	sc, err := p.Core("log", "level", "module", "sink", "kind")
	if err != nil {
		return nil, fmt.Errorf("kernel: log metrics scope: %w", err)
	}
	m, prefix := sc.Meter(), sc.Prefix()
	records, err := m.Int64ObservableCounter(prefix+".records",
		metric.WithDescription("Log records that passed their level gate, by level and module (the process default logger is module \"process\", an empty label would read as absent)."))
	if err != nil {
		return nil, fmt.Errorf("kernel: log records counter: %w", err)
	}
	dropped, err := m.Int64ObservableCounter(prefix+".dropped",
		metric.WithDescription("Log records lost, by sink: primary, or a tap's name."))
	if err != nil {
		return nil, fmt.Errorf("kernel: log dropped counter: %w", err)
	}
	redactions, err := m.Int64ObservableCounter(prefix+".redactions",
		metric.WithDescription("Redactions applied to log attributes and messages, by kind."))
	if err != nil {
		return nil, fmt.Errorf("kernel: log redactions counter: %w", err)
	}
	journalErrors, err := m.Int64ObservableCounter(prefix+".journal_write_errors",
		metric.WithDescription("Journal batches the store refused; those records were not journaled (the journal fails open)."))
	if err != nil {
		return nil, fmt.Errorf("kernel: log journal write errors counter: %w", err)
	}
	journalLag, err := m.Float64ObservableGauge(prefix+".journal_lag", metric.WithUnit("s"),
		metric.WithDescription("Age of the oldest log record the journal has accepted and not yet written; 0 when nothing waits."))
	if err != nil {
		return nil, fmt.Errorf("kernel: log journal lag gauge: %w", err)
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		var jc logjournal.TapCounters
		if cfg.journal != nil {
			jc = cfg.journal()
		}
		o.ObserveInt64(journalErrors, int64(jc.WriteErrors))
		o.ObserveFloat64(journalLag, jc.Lag.Seconds())
		c := l.Counters()
		for mod, byLevel := range c.Records {
			for lv, n := range byLevel {
				o.ObserveInt64(records, int64(n), metric.WithAttributes(
					attribute.String("level", levelLabel(lv)), attribute.String("module", moduleLabel(mod))))
			}
		}
		for sink, n := range c.Dropped {
			o.ObserveInt64(dropped, int64(n), metric.WithAttributes(attribute.String("sink", sink)))
		}
		for kind, n := range c.Redactions {
			o.ObserveInt64(redactions, int64(n), metric.WithAttributes(attribute.String("kind", kind)))
		}
		return nil
	}, records, dropped, redactions, journalErrors, journalLag)
	if err != nil {
		return nil, fmt.Errorf("kernel: log metrics callback: %w", err)
	}
	return sc, nil
}

// levelLabel is the level as the pipeline's lines render it: slog's names from
// debug up, TRACE and TRACE+n below (log.replaceLevel's rule; the test
// compares the two).
// moduleLabel is the module label of a records series: the process default
// logger counts under the empty module, which Prometheus reads as an absent
// label, so it is named log.ProcessLabel.
func moduleLabel(module string) string {
	if module == "" {
		return log.ProcessLabel
	}
	return module
}

func levelLabel(lv slog.Level) string {
	switch {
	case lv >= log.LevelDebug:
		return lv.String()
	case lv == log.LevelTrace:
		return "TRACE"
	default:
		return fmt.Sprintf("TRACE%+d", int(lv-log.LevelTrace))
	}
}
