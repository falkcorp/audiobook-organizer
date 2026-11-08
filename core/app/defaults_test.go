// file: core/app/defaults_test.go
// version: 1.1.0
// guid: 0b7c4e2a-91d3-4f58-a6e0-3c8d5f2b7a19
// last-edited: 2026-11-08

package app

import (
	"bytes"
	"context"
	"errors"
	stdlog "log"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
)

// These tests install process globals, so none of them calls t.Parallel.

// syncBuffer is a bytes.Buffer safe for the pipeline's concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// gothamApp boots a zero-module App whose pipeline writes to out.
func gothamApp(t *testing.T, out *syncBuffer, mods ...module.Module) *App {
	t.Helper()
	a, err := New(t.Context(), Options{AppID: "gotham", MetricNamespace: "gotham", LogOutput: out, Env: []string{}}, mods...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Metrics().Shutdown(context.Background()) })
	return a
}

// familyNames is the sorted family names a gathers.
func familyNames(t *testing.T, a *App) []string {
	t.Helper()
	mfs, err := a.Metrics().Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mf := range mfs {
		out = append(out, mf.GetName())
	}
	return out
}

// TestInstallProcessDefaults_RestoreAndExclusive is 05-logging §10's row:
// after install the three OTel getters return the App's, an instrument made
// through the global appears in the App's scrape and slog.Default() logs
// through the pipeline (redaction included); a second install errors; restore
// puts all seven back; the OTel sink drops values.
func TestInstallProcessDefaults_RestoreAndExclusive(t *testing.T) {
	var out syncBuffer
	a := gothamApp(t, &out)

	prevSlog := slog.Default()
	prevLogOut, prevLogFlags := stdlog.Writer(), stdlog.Flags()
	prevHandler := otel.GetErrorHandler()
	prevMP, prevTP, prevProp := otel.GetMeterProvider(), otel.GetTracerProvider(), otel.GetTextMapPropagator()

	restore, err := InstallProcessDefaults(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	p := a.Metrics()
	// The App's propagator is a composite (a slice), so it is compared by value.
	if otel.GetMeterProvider() != p.MeterProvider() || otel.GetTracerProvider() != p.TracerProvider() ||
		!reflect.DeepEqual(otel.GetTextMapPropagator(), p.Propagator()) {
		t.Fatal("the OTel getters do not return the App's providers after install")
	}

	c, err := otel.GetMeterProvider().Meter("wayne/manor").Int64Counter("gotham.batsignal.lit")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 1)
	if names := familyNames(t, a); !slices.Contains(names, "gotham_batsignal_lit_total") {
		t.Fatalf("an instrument made through the global is not in the App's scrape: %v", names)
	}

	slog.Info("alfred checked the cave", "password", "robin-is-hunter2")
	stdlog.Print("lucius checked the armory")
	got := out.String()
	if !strings.Contains(got, "alfred checked the cave") || strings.Contains(got, "robin-is-hunter2") {
		t.Fatalf("slog.Default() did not log through the redacting pipeline:\n%s", got)
	}
	if !strings.Contains(got, "lucius checked the armory") {
		t.Fatalf("the standard library's log did not reach the pipeline:\n%s", got)
	}

	// The SDK sink: message and key names kept, every value dropped.
	otelLogger := logr.New(&sdkLogSink{h: newClassLimiter(time.Hour, time.Now, slog.Default())})
	otelLogger.Info("parse url", "input", "https://joker:hahaha@acehq.example:4317")
	otelLogger.Error(errors.New("bad input https://joker:hahaha@acehq.example"), "exporter")
	if got := out.String(); strings.Contains(got, "hahaha") || strings.Contains(got, "joker") ||
		!strings.Contains(got, "value keys: input") {
		t.Fatalf("the OTel SDK sink forwarded a value or lost the key names:\n%s", got)
	}

	// Exclusive: a second install, for this App or another, errors.
	if _, err := InstallProcessDefaults(a); !errors.Is(err, ErrProcessDefaultsTaken) {
		t.Fatalf("second install for the same App: %v, want ErrProcessDefaultsTaken", err)
	}
	if _, err := InstallProcessDefaults(gothamApp(t, &syncBuffer{})); !errors.Is(err, ErrProcessDefaultsTaken) {
		t.Fatalf("install for a second App: %v, want ErrProcessDefaultsTaken", err)
	}

	restore()
	restore() // idempotent
	if slog.Default() != prevSlog || stdlog.Writer() != prevLogOut || stdlog.Flags() != prevLogFlags {
		t.Fatal("restore did not put slog's default and the log package's output and flags back")
	}
	if otel.GetErrorHandler() != prevHandler || otel.GetMeterProvider() != prevMP ||
		otel.GetTracerProvider() != prevTP || !reflect.DeepEqual(otel.GetTextMapPropagator(), prevProp) {
		t.Fatal("restore did not put the OTel error handler, providers and propagator back")
	}
	// After restore another App may own the defaults.
	again, err := InstallProcessDefaults(gothamApp(t, &syncBuffer{}))
	if err != nil {
		t.Fatalf("install after restore: %v", err)
	}
	again()
}

// TestFamilyTaken_ThroughGlobalProvider is 05-metrics §12's row (the
// ops.zombies case of MOD9-1): after InstallProcessDefaults, an instrument
// made through otel.GetMeterProvider() in another scope with a name a core
// Scope instrument holds gets ErrFamilyTaken and a no-op; the boot fails
// before the listener opens; metrics_family_taken is raised; and the
// identical instrument made twice in one scope is one instrument.
func TestFamilyTaken_ThroughGlobalProvider(t *testing.T) {
	a := gothamApp(t, &syncBuffer{})
	restore, err := InstallProcessDefaults(a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)

	// The kernel's own core scope holds gotham.kernel.boot_phase.
	_, err = otel.GetMeterProvider().Meter("arkham/asylum").Float64Gauge("gotham.kernel.boot_phase", metric.WithUnit("s"))
	if !errors.Is(err, metrics.ErrFamilyTaken) {
		t.Fatalf("second owner through the global: %v, want ErrFamilyTaken", err)
	}
	if !slices.Contains(a.Metrics().Degraded(), metrics.DegradedFamilyTaken) {
		t.Fatalf("Degraded() = %v, want %s", a.Metrics().Degraded(), metrics.DegradedFamilyTaken)
	}

	m := otel.GetMeterProvider().Meter("wayne/enterprises")
	c1, err1 := m.Int64Counter("gotham.gadgets.built")
	c2, err2 := m.Int64Counter("gotham.gadgets.built")
	if err1 != nil || err2 != nil {
		t.Fatalf("the identical instrument twice in one scope: %v, %v", err1, err2)
	}
	c1.Add(context.Background(), 1)
	c2.Add(context.Background(), 1)

	// Boot fails: Run refuses to open the listener. No port is bound, because
	// R1Listen is never reached.
	runErr := a.Run(t.Context())
	if !errors.Is(runErr, metrics.ErrFamilyTaken) {
		t.Fatalf("Run after a refused family: %v, want ErrFamilyTaken", runErr)
	}
}

// TestFamilyTaken_DuringNew: a second owner refused while a module registers
// fails New itself (05-metrics §8 "fail-closed during boot").
func TestFamilyTaken_DuringNew(t *testing.T) {
	// A one-module app's module scope is the bare namespace, so this name is
	// in scope for the module and translates to the kernel's own family. The
	// module ignores the refusal, as ABO's call sites do; New must not.
	m := testModule{id: "wayne", register: func(r *module.Registrar) error {
		_, _ = r.Deps().Metrics.Meter().Float64Gauge("gotham.kernel.boot_phase", metric.WithUnit("s"))
		return nil
	}}
	_, err := New(t.Context(), Options{AppID: "gotham", MetricNamespace: "gotham", LogOutput: &syncBuffer{}, Env: []string{}}, m)
	if !errors.Is(err, metrics.ErrFamilyTaken) {
		t.Fatalf("New with a module re-owning a core family: %v, want ErrFamilyTaken", err)
	}
}

// TestErrorHandler_RateLimitedByClass: one line per error class per
// interval, repeats counted, digit runs normalised, endpoint secrets cut.
func TestErrorHandler_RateLimitedByClass(t *testing.T) {
	var out syncBuffer
	now := time.Unix(1_000_000, 0)
	h := &processErrorHandler{h: newClassLimiter(time.Minute, func() time.Time { return now },
		slog.New(slog.NewTextHandler(&out, nil)))}
	h.Handle(errors.New("export to https://bane:venom@blackgate.example:4317/v1?token=x failed after 3 tries"))
	h.Handle(errors.New("export to https://bane:venom@blackgate.example:4317/v1?token=x failed after 4 tries"))
	h.Handle(errors.New("a different class"))
	got := out.String()
	if strings.Count(got, "export to") != 1 || !strings.Contains(got, "a different class") {
		t.Fatalf("want one line per class:\n%s", got)
	}
	if strings.Contains(got, "venom") || strings.Contains(got, "token=x") {
		t.Fatalf("an endpoint secret reached the log:\n%s", got)
	}
	now = now.Add(2 * time.Minute)
	h.Handle(errors.New("export to https://blackgate.example:4317 failed after 9 tries"))
	if !strings.Contains(out.String(), "suppressed_since_last=1") {
		t.Fatalf("the repeat count was not reported:\n%s", out.String())
	}
}

// testModule is a module from an ID and a Register func.
type testModule struct {
	id       string
	register func(r *module.Registrar) error
}

func (m testModule) Manifest() module.Manifest {
	return module.Manifest{ID: module.ID(m.id), Version: "1.0.0", DefaultEnabled: true}
}

func (m testModule) Register(r *module.Registrar) error {
	if m.register == nil {
		return nil
	}
	return m.register(r)
}

// recordingHandler is an otel.ErrorHandler that keeps every error's text.
type recordingHandler struct {
	mu   sync.Mutex
	errs []string
}

func (h *recordingHandler) Handle(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs = append(h.errs, err.Error())
}

func (h *recordingHandler) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.errs, "\n")
}

// TestNew_ProcessDefaults_SDKSinkBeforeProvider: with Options.ProcessDefaults,
// New installs the value-dropping SDK logger and the rate-limited error
// handler before it builds the Provider. Building the trace exporter makes
// the SDK read OTEL_EXPORTER_OTLP_* and report the bad values raw (as
// ("parse headers", "input", <value>)); none of that reaches the logger and
// handler that were installed before New (stand-ins for the SDK's defaults,
// which write to stderr), and the App's pipeline gets the message and the
// key names only. InstallProcessDefaults then adopts the pair, a second
// install errors, a second App's New fails without touching them, and
// restore puts back what was there before New.
func TestNew_ProcessDefaults_SDKSinkBeforeProvider(t *testing.T) {
	const sentinel = "Joker-OTLP-credential-sentinel"
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", sentinel)                    // no '=': "parse headers"
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://["+sentinel) // "parse url"
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", sentinel)                    // "parse duration"

	var before syncBuffer
	beforeHandler := &recordingHandler{}
	prevHandler := otel.GetErrorHandler()
	otel.SetLogger(stdr.New(stdlog.New(&before, "", 0)))
	otel.SetErrorHandler(beforeHandler)
	t.Cleanup(func() {
		otel.SetErrorHandler(prevHandler)
		otel.SetLogger(stdr.New(stdlog.New(os.Stderr, "", stdlog.LstdFlags|stdlog.Lshortfile)))
	})

	var out syncBuffer
	opts := Options{AppID: "gotham", MetricNamespace: "gotham", LogOutput: &out, Env: []string{}, ProcessDefaults: true,
		R1Telemetry: &R1Telemetry{Tracing: &metrics.OTLPTracing{Endpoint: "http://192.0.2.1:4317"}}}
	a, err := New(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Metrics().Shutdown(ctx)
	})

	got := out.String()
	for name, s := range map[string]string{"the logger before New": before.String(),
		"the error handler before New": beforeHandler.String(), "the App's pipeline": got} {
		if strings.Contains(s, sentinel) {
			t.Errorf("the hostile OTLP env value reached %s:\n%s", name, s)
		}
	}
	if !strings.Contains(got, "values dropped") || !strings.Contains(got, "parse headers") {
		t.Fatalf("the SDK's report of the bad env did not reach the App's sink (the test is blind):\n%s", got)
	}

	if _, err := New(t.Context(), Options{AppID: "arkham", MetricNamespace: "arkham", LogOutput: &syncBuffer{},
		Env: []string{}, ProcessDefaults: true}); !errors.Is(err, ErrProcessDefaultsTaken) {
		t.Fatalf("a second App's New with ProcessDefaults: %v, want ErrProcessDefaultsTaken", err)
	}
	if otel.GetErrorHandler() != otel.ErrorHandler(a.otel.handler) {
		t.Fatal("the refused New replaced the first App's error handler")
	}

	restore, err := InstallProcessDefaults(a)
	if err != nil {
		t.Fatalf("InstallProcessDefaults after a ProcessDefaults New: %v", err)
	}
	if _, err := InstallProcessDefaults(a); !errors.Is(err, ErrProcessDefaultsTaken) {
		t.Fatalf("second install: %v, want ErrProcessDefaultsTaken", err)
	}
	restore()
	if otel.GetErrorHandler() != otel.ErrorHandler(beforeHandler) {
		t.Fatalf("restore left %T, want the handler installed before New", otel.GetErrorHandler())
	}
}

// TestNew_ProcessDefaults_RestoredWhenBootFails: a New that installed the SDK
// pair and then fails puts back the handler that was there before.
func TestNew_ProcessDefaults_RestoredWhenBootFails(t *testing.T) {
	prevHandler := otel.GetErrorHandler()
	beforeHandler := &recordingHandler{}
	otel.SetErrorHandler(beforeHandler)
	t.Cleanup(func() {
		otel.SetErrorHandler(prevHandler)
		otel.SetLogger(stdr.New(stdlog.New(os.Stderr, "", stdlog.LstdFlags|stdlog.Lshortfile)))
	})
	m := testModule{id: "wayne", register: func(*module.Registrar) error { return errors.New("Bane broke the module") }}
	if _, err := New(t.Context(), Options{AppID: "gotham", MetricNamespace: "gotham", LogOutput: &syncBuffer{},
		Env: []string{}, ProcessDefaults: true}, m); err == nil {
		t.Fatal("New with a failing Register succeeded")
	}
	if otel.GetErrorHandler() != otel.ErrorHandler(beforeHandler) {
		t.Fatalf("a failed New left %T installed", otel.GetErrorHandler())
	}
}
