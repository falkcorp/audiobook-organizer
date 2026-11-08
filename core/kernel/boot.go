// file: core/kernel/boot.go
// version: 1.0.6
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
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/version"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// The phase label values of <namespace>_kernel_boot_phase_seconds, spelled
// exactly as 06 §6.9's one label set (OPS3-16). Only the phases that exist
// at release R1's skeleton are listed.
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
// handler. On error everything P5 built is closed in reverse.
func boot(ctx context.Context, c Config, describe bool) (_ *Kernel, err error) {
	k := &Kernel{cfg: c, notifier: c.Notifier, exit: c.Exit, listen: c.Listen, lifecycle: newLifecycle()}
	if k.notifier == nil {
		k.notifier = noopNotifier{}
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
	if err := k.phase(phaseP5, func() error { return k.services(ctx, describe) }); err != nil {
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

// services is P5 at R1 (06 §6.5.1 P5, A8), in this fixed order: logging
// (05-logging §4 step 1, which config precedes once it exists), the metrics
// Provider with its Prometheus registry (05-metrics §3), the kernel's own
// scope and gauge, and the router. Describe discards its log output: it is
// an offline generator and does no I/O (COR2-13).
func (k *Kernel) services(ctx context.Context, describe bool) error {
	if err := k.buildLogging(describe); err != nil {
		return err
	}
	// Before any module code runs, so a restart request refused before
	// readiness is logged (Lifecycle.RequestRestart).
	k.lifecycle.log = k.log
	if f := k.cfg.BeforeTelemetry; f != nil && !describe {
		if err := f(k.coreLog("otel")); err != nil {
			return fmt.Errorf("kernel: before telemetry: %w", err)
		}
	}
	if f := k.cfg.ProcessLogging; f != nil && !describe {
		stdlib := k.RootLogger().Handler()
		if k.logging != nil {
			stdlib = k.logging.Handler()
		}
		if err := f(k.RootLogger(), stdlib); err != nil {
			return fmt.Errorf("kernel: process logging: %w", err)
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
	// app.Options.R1Telemetry; F3: core.metrics and core.tracing). Describe
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
	if k.logging != nil {
		if k.logScope, err = registerLogMetrics(p, k.logging); err != nil {
			return err
		}
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

// buildLogging builds the App's log.Logging from core.log, or adopts
// Config.Logger. Until config lands (F3) no layer can set core.log, so the
// section is Config.Log when the app supplies one (release R1 only: ABO's
// log_level and its file under the state directory), otherwise the defaults
// (text, info, stderr). output=file needs core.paths.state_dir, which is
// Config.StateDir until the config layer resolves it.
func (k *Kernel) buildLogging(describe bool) error {
	s := log.DefaultSettings()
	if k.cfg.Log != nil {
		s = *k.cfg.Log
	}
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
	var format log.Format
	switch s.Format {
	case "text":
		format = log.FormatText
	case "json":
		format = log.FormatJSON
	default:
		return fmt.Errorf("kernel: core.log.format: %q is not text or json", s.Format)
	}
	out, err := k.logOutput(s, describe)
	if err != nil {
		return err
	}
	l, err := log.New(log.Options{Output: out, Format: format, Level: level, Levels: levels,
		AddSource: s.AddSource, Redactor: k.redactor})
	if err != nil {
		return errors.Join(fmt.Errorf("kernel: logging: %w", err), k.closeLogFile())
	}
	k.logging = l
	k.log = k.coreLog("kernel")
	return nil
}

// logOutput is the primary output of core.log.output: stderr or stdout (or
// Config.LogOutput, which replaces both), or the rotating file under the
// state directory. With the file, Config.LogOutput (stderr by default) also
// receives every line while core.log.file.tee_stderr is true. The file and
// its root are closed by closeCore, after Logging. Describe discards its
// output and opens nothing (COR2-13).
func (k *Kernel) logOutput(s log.Settings, describe bool) (io.Writer, error) {
	tee := k.cfg.LogOutput
	if s.Output != "stderr" && s.Output != "stdout" && s.Output != "file" {
		return nil, fmt.Errorf("kernel: core.log.output: %q is not stderr, stdout or file", s.Output)
	}
	switch {
	case describe:
		return io.Discard, nil
	case s.Output == "file":
		if tee == nil {
			tee = os.Stderr
		}
		if k.cfg.StateDir == "" {
			return nil, errors.New("kernel: core.log.output is file but there is no state directory (Config.StateDir)")
		}
		path := s.File.Path
		if path == "" {
			path = "logs/" + k.cfg.AppID + ".log"
		}
		root, err := os.OpenRoot(k.cfg.StateDir)
		if err != nil {
			return nil, fmt.Errorf("kernel: core.log.file.path: open the state directory: %w", err)
		}
		f, err := log.OpenRotatingFile(root, path, int64(s.File.MaxSizeMB)<<20, s.File.MaxFiles)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("kernel: core.log.file: %w", err), root.Close())
		}
		k.logClosers = []io.Closer{f, root}
		if !s.File.TeeStderr {
			return f, nil
		}
		return &fileTee{file: f, tee: tee}, nil
	case tee != nil:
		return tee, nil
	case s.Output == "stdout":
		return os.Stdout, nil
	default:
		return os.Stderr, nil
	}
}

// closeLogFile closes the file output and its root, once.
func (k *Kernel) closeLogFile() error {
	var errs []error
	for _, c := range k.logClosers {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	k.logClosers = nil
	return errors.Join(errs...)
}

// fileTee is the primary output with core.log.file.tee_stderr: every write
// goes to the file and to the tee, whatever the file's result (an io.MultiWriter
// would stop at the file's error). A file that closeCore already closed is not
// an error: a line logged after shutdown (a stop hook that outlived the
// budget) still reached the tee, so the pipeline does not count it as lost.
type fileTee struct {
	file *log.RotatingFile
	tee  io.Writer
}

func (t *fileTee) Write(p []byte) (int, error) {
	n, ferr := t.file.Write(p)
	_, terr := t.tee.Write(p)
	if ferr != nil && !errors.Is(ferr, os.ErrClosed) {
		return n, ferr
	}
	if ferr != nil {
		return len(p), terr
	}
	return n, nil
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

// extendStart is every module's Deps.ExtendStart.
func (k *Kernel) extendStart() func(string) {
	if k.cfg.ExtendStart != nil {
		return k.cfg.ExtendStart
	}
	return func(string) {}
}

// listenHTTP is Config.Listen's default, httpapi.Listen.
func listenHTTP(o httpapi.ListenOptions, h http.Handler, l *slog.Logger) (Server, error) {
	ln, err := httpapi.Listen(o, h, l)
	if err != nil {
		return nil, err
	}
	return ln, nil
}

// closeCore closes what P5 built, in reverse: the metrics Provider, then
// logging last (05-logging §4 step 4). It runs once, after the shutdown
// budget if need be (06 §6.5.2: storage, and so everything after it, closes
// regardless), with at least lateStopFloor to finish.
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
		if k.logging != nil {
			if err := k.logging.Close(cctx); err != nil {
				errs = append(errs, fmt.Errorf("kernel: logging close: %w", err))
			}
		}
		if err := k.closeLogFile(); err != nil {
			errs = append(errs, fmt.Errorf("kernel: log file close: %w", err))
		}
		k.closeErr = errors.Join(errs...)
	})
	return k.closeErr
}
