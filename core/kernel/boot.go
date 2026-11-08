// file: core/kernel/boot.go
// version: 1.0.7
// guid: 0c2d259d-1f0d-46d8-a42f-e27deea9ec56
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/sdnotify"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/version"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The phase label values of <namespace>_kernel_boot_phase_seconds, spelled
// exactly as 06 §6.9's one label set (OPS3-16). Only the phases that exist
// at release R1 are listed; storage.go has P2, P4a and P4b.
const (
	phaseP1 = "P1"
	phaseP5 = "P5"
	phaseP6 = "P6"
	phaseP7 = "P7"
	phaseP8 = "P8"
)

// phaseTiming is one phase's duration, kept until the gauge exists: P1 runs
// before P5 builds the metrics Provider.
type phaseTiming struct {
	phase string
	took  time.Duration
}

// boot runs the phases of 06 §6.5.1 that exist, in the table's order. For
// Describe it runs P1, P5, P6 and P7, then Router.Build without P8's
// handler. Logging is built before storage opens (05-logging §4 steps 1-2).
// On error everything built is closed in reverse.
func boot(ctx context.Context, c Config, describe bool) (_ *Kernel, err error) {
	k := &Kernel{cfg: c, notifier: c.Notifier, exit: c.Exit, listen: c.Listen, lifecycle: newLifecycle()}
	if k.notifier == nil {
		k.notifier = systemdNotifier{}
	}
	if k.exit == nil {
		k.exit = os.Exit
	}
	if k.listen == nil {
		k.listen = listenHTTP
	}
	defer func() {
		if err != nil {
			if cerr := k.closeCore(ctx, time.Now().Add(defaultShutdownBudget)); cerr != nil {
				err = fmt.Errorf("%w (closing after the failed boot: %v)", err, cerr)
			}
		}
	}()
	if err := k.phase(phaseP1, func() error { return k.catalog(describe) }); err != nil {
		return nil, err
	}
	if err := k.buildLogging(describe); err != nil {
		return nil, err
	}
	// Before any module code runs, so a restart request refused before
	// readiness is logged (Lifecycle.RequestRestart).
	k.lifecycle.log = k.log
	if open, err := k.storageWanted(describe); err != nil {
		return nil, err
	} else if open {
		if err := k.phase(phaseP2, func() error { return k.openStorage(ctx) }); err != nil {
			return nil, err
		}
		if err := k.phase(phaseP4a, func() error { return k.prepareStorage(ctx) }); err != nil {
			return nil, err
		}
		if err := k.phase(phaseP4b, func() error { return k.migrateStorage(ctx) }); err != nil {
			return nil, err
		}
	}
	if err := k.phase(phaseP5, func() error { return k.services(ctx, describe) }); err != nil {
		return nil, err
	}
	if err := k.storageMetrics(ctx); err != nil {
		return nil, err
	}
	if err := k.phase(phaseP6, func() error { return k.register(ctx) }); err != nil {
		return nil, err
	}
	if err := k.phase(phaseP7, k.consume); err != nil {
		return nil, err
	}
	if describe {
		if _, err := k.router.Build(); err != nil {
			return nil, joined(problemsFrom("", err))
		}
		return k, nil
	}
	if err := k.phase(phaseP8, k.handlerPhase); err != nil {
		return nil, err
	}
	// A family refused through a Scope or the raw MeterProvider during
	// Register fails boot (05-metrics §8); Run checks again before the
	// listener for what the process-global install and the hooks' Starts
	// created.
	if err := k.familyTaken("during New"); err != nil {
		return nil, err
	}
	return k, nil
}

// phase runs one boot phase and records its duration on
// <namespace>_kernel_boot_phase_seconds{phase, step=""}, the phase total
// (06 §6.9). A phase before P5 is recorded once the gauge exists.
func (k *Kernel) phase(name string, fn func() error) error {
	start := time.Now()
	err := fn()
	k.timings = append(k.timings, phaseTiming{phase: name, took: time.Since(start)})
	k.flushTimings()
	return err
}

// flushTimings records the buffered phase durations once the gauge exists.
func (k *Kernel) flushTimings() {
	if k.bootPhase == nil {
		return
	}
	for _, t := range k.timings {
		k.bootPhase.Record(context.Background(), t.took.Seconds(),
			metric.WithAttributes(attribute.String("phase", t.phase), attribute.String("step", "")))
	}
	k.timings = k.timings[:0]
}

// services is P5 at R1 (06 §6.5.1 P5, A8), in this fixed order: the metrics
// Provider with its Prometheus registry (05-metrics §3), the kernel's own
// scope and gauge, and the router. Logging is built before P2 (boot).
func (k *Kernel) services(ctx context.Context, describe bool) error {
	if f := k.cfg.BeforeTelemetry; f != nil && !describe {
		if err := f(k.coreLog("otel")); err != nil {
			return fmt.Errorf("kernel: before telemetry: %w", err)
		}
	}
	ids := make([]string, 0, len(k.entries))
	for _, e := range k.entries {
		ids = append(ids, string(e.man.ID))
	}
	serviceName := k.cfg.ServiceName
	if serviceName == "" {
		serviceName = k.cfg.AppID
	}
	mo := metrics.Options{
		AppID:           k.cfg.AppID,
		ServiceName:     serviceName,
		MetricNamespace: k.cfg.MetricNamespace,
		Modules:         ids,
		ServiceVersion:  k.cfg.Version,
		Prometheus:      true,
		Redactor:        k.redactor,
		Logger:          k.coreLog("metrics"),
	}
	// The app's OTLP push, span exporter and resource attributes (R1:
	// app.Options.Telemetry; F3: core.metrics and core.tracing). Describe
	// builds no exporter: it does no I/O (COR2-13).
	if t := k.cfg.Telemetry; t != nil && !describe {
		mo.OTLP, mo.Tracing = t.OTLP, t.Tracing
		mo.Environment, mo.InstanceID = t.Environment, t.InstanceID
	}
	p, err := metrics.New(ctx, mo)
	if err != nil {
		return fmt.Errorf("kernel: metrics provider: %w", err)
	}
	k.provider = p
	if k.scope, err = p.Core("kernel", "phase", "step"); err != nil {
		return fmt.Errorf("kernel: metrics scope: %w", err)
	}
	if k.bootPhase, err = k.scope.Meter().Float64Gauge(k.scope.Prefix()+".boot_phase", metric.WithUnit("s"),
		metric.WithDescription("Duration of each boot phase of the last boot, set once per boot (06 §6.9).")); err != nil {
		return fmt.Errorf("kernel: boot phase gauge: %w", err)
	}
	httpScope, err := p.Core("http")
	if err != nil {
		return fmt.Errorf("kernel: http metrics scope: %w", err)
	}
	k.router = httpapi.NewRouter(httpapi.Options{
		ServerName: serviceName,
		OpenAPI:    httpapi.OpenAPIOptions{Version: version.Version, CookieName: k.cfg.AppID + "_session"},
	}, httpapi.Deps{Log: k.coreLog("http"), Metrics: httpScope, MeterProvider: p.MeterProvider()})
	// Core's group claims /api/core/ now, before any module registers, so a
	// module's legacy prefix over it collides at P7 (05-http-api §4.9). Core
	// routes join it as their packages land.
	k.router.CoreGroup()
	return nil
}

// buildLogging builds the App's log.Logging from core.log's defaults, or
// adopts Config.Logger. Describe discards its log output: it is an offline
// generator and does no I/O (COR2-13). Until config lands (F3) no layer can
// set core.log, so the defaults (text, info, stderr) are the effective
// values, and the file output, which needs core.paths.state_dir, cannot be
// selected yet.
func (k *Kernel) buildLogging(describe bool) error {
	s := log.DefaultSettings()
	k.redactor = log.NewRedactor(s.RedactKeys)
	if k.cfg.Logger != nil {
		k.log = k.coreLog("kernel")
		return nil
	}
	level, err := log.ParseLevel(s.Level)
	if err != nil {
		return fmt.Errorf("kernel: core.log.level: %w", err)
	}
	levels := make(map[string]slog.Level, len(s.Levels))
	for scope, v := range s.Levels {
		if levels[scope], err = log.ParseLevel(v); err != nil {
			return fmt.Errorf("kernel: core.log.levels.%s: %w", scope, err)
		}
	}
	format := log.FormatText
	if s.Format == "json" {
		format = log.FormatJSON
	}
	out := k.cfg.LogOutput
	switch {
	case describe:
		out = io.Discard
	case out == nil && s.Output == "stdout":
		out = os.Stdout
	case out == nil:
		out = os.Stderr
	}
	l, err := log.New(log.Options{Output: out, Format: format, Level: level, Levels: levels,
		AddSource: s.AddSource, Redactor: k.redactor})
	if err != nil {
		return fmt.Errorf("kernel: logging: %w", err)
	}
	k.logging = l
	k.log = k.coreLog("kernel")
	return nil
}

// coreLog is core's logger for one subsystem (module=core, 05-logging §3).
func (k *Kernel) coreLog(subsystem string) *slog.Logger {
	if k.logging != nil {
		return k.logging.Scope("core", subsystem)
	}
	if k.cfg.Logger != nil {
		return k.cfg.Logger.With(log.KeyModule, "core", log.KeySubsystem, subsystem)
	}
	return slog.New(slog.DiscardHandler)
}

// moduleLog is a module's Deps.Log: Logging.Module(id), or Config.Logger
// with the module attribute.
func (k *Kernel) moduleLog(id module.ID) *slog.Logger {
	if k.logging != nil {
		return k.logging.Module(string(id))
	}
	return k.cfg.Logger.With(log.KeyModule, string(id))
}

// coreOwner is the owner id of ExtendStart calls the kernel makes for core's
// own steps (storage's open and checkpoint): no module hook runs under it.
const coreOwner module.ID = "core"

// extendStart is module id's Deps.ExtendStart. It does two things with
// sdnotify.StartupStepBudget (HEAD's semantics: the deadline moves to at least
// that far from now, never closer). It extends the start window of id's hook
// Start if one is running, so the kernel's own core.lifecycle.start_timeout
// does not abandon a step that is making progress, and it asks the service
// manager for the same time over NOTIFY_SOCKET. Config.ExtendStart replaces
// the second in tests. A failed request is logged and never fails startup:
// under Type=simple there is no socket at all.
func (k *Kernel) extendStart(id module.ID) func(string) {
	return func(step string) {
		k.startWin.extend(id, sdnotify.StartupStepBudget)
		send := k.cfg.ExtendStart
		if send == nil {
			send = func(string) {
				if err := sdnotify.ExtendTimeout(sdnotify.StartupStepBudget); err != nil {
					k.log.Warn("kernel: systemd start-timeout extension failed", "module", id, "step", step, "err", err)
				}
			}
		}
		send(step)
	}
}

// listenHTTP is Config.Listen's default, httpapi.Listen.
func listenHTTP(o httpapi.ListenOptions, h http.Handler, l *slog.Logger) (Server, error) {
	ln, err := httpapi.Listen(o, h, l)
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// closeCore closes what boot built, in reverse: the metrics Provider, the
// store, then logging last (05-logging §4 step 4). It runs once, after the
// shutdown budget if need be (06 §6.5.2: storage, and so everything after
// it, closes regardless), with at least lateStopFloor to finish. Before the
// store closes it waits, within that floor, for the hook Stops still
// running in the background (awaitLateStops); without a store there is
// nothing for them to lose, so it does not wait.
func (k *Kernel) closeCore(ctx context.Context, deadline time.Time) error {
	k.closeOnce.Do(func() {
		cctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), atLeastFloor(deadline))
		defer cancel()
		var errs []error
		if k.provider != nil {
			if err := k.provider.Shutdown(cctx); err != nil {
				errs = append(errs, fmt.Errorf("kernel: metrics shutdown: %w", err))
			}
		}
		if k.store != nil {
			k.awaitLateStops(cctx)
		}
		if err := k.closeStorage(); err != nil {
			errs = append(errs, err)
		}
		if k.logging != nil {
			if err := k.logging.Close(cctx); err != nil {
				errs = append(errs, fmt.Errorf("kernel: logging close: %w", err))
			}
		}
		k.closeErr = errors.Join(errs...)
	})
	return k.closeErr
}
