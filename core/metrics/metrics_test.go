// file: core/metrics/metrics_test.go
// version: 1.0.3
// guid: 21f5d697-6d25-4e0b-be83-f34e89311ae5
// last-edited: 2026-11-07

package metrics

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/falkcorp/mediacore/log"
)

// newTest builds a Provider with the Prometheus exporter on and registers its
// shutdown.
func newTest(t *testing.T, o Options) *Provider {
	t.Helper()
	o.Prometheus = true
	p, err := New(context.Background(), o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	return p
}

// scrape serves one GET through the Provider's own Handler.
func scrape(t *testing.T, p *Provider) string {
	t.Helper()
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// gather returns the private registry's families by name.
func gather(t *testing.T, p *Provider) map[string]*dto.MetricFamily {
	t.Helper()
	mfs, err := p.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	out := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		out[mf.GetName()] = mf
	}
	return out
}

func labelsOf(m *dto.Metric) map[string]string {
	out := map[string]string{}
	for _, lp := range m.GetLabel() {
		out[lp.GetName()] = lp.GetValue()
	}
	return out
}

func TestTwoProvidersIsolated(t *testing.T) {
	a := newTest(t, Options{AppID: "a"})
	b := newTest(t, Options{AppID: "b"})
	ctx := context.Background()
	for p, n := range map[*Provider]int64{a: 2, b: 5} {
		c, err := p.MeterProvider().Meter("iso").Int64Counter("iso.things")
		if err != nil {
			t.Fatal(err)
		}
		c.Add(ctx, n)
	}
	for p, want := range map[*Provider]float64{a: 2, b: 5} {
		mf, ok := gather(t, p)["iso_things_total"]
		if !ok {
			t.Fatal("iso_things_total missing from a Provider's scrape")
		}
		if got := mf.GetMetric()[0].GetCounter().GetValue(); got != want {
			t.Errorf("value = %v, want %v: the scrapes are not disjoint", got, want)
		}
	}
	if tA, tB := gather(t, a)["target_info"], gather(t, b)["target_info"]; tA == nil || tB == nil ||
		labelsOf(tA.GetMetric()[0])["service_name"] != "a" || labelsOf(tB.GetMetric()[0])["service_name"] != "b" {
		t.Error("each Provider must carry its own resource on its own target_info")
	}
}

func TestGoAndProcessCollectorsPresent(t *testing.T) {
	body := scrape(t, newTest(t, Options{AppID: "a"}))
	for _, fam := range []string{"go_goroutines", "process_resident_memory_bytes", "promhttp_metric_handler_requests_total"} {
		if !strings.Contains(body, "# TYPE "+fam+" ") {
			t.Errorf("fresh Provider's scrape lacks %s", fam)
		}
	}
}

func TestScope_RejectsBadNames(t *testing.T) {
	p := newTest(t, Options{AppID: "a"})
	s, err := p.Core("ops")
	if err != nil {
		t.Fatal(err)
	}
	m := s.Meter()
	bad := map[string]func() error{
		"outside prefix":  func() error { _, err := m.Int64Counter("other.runs"); return err },
		"prefix only":     func() error { _, err := m.Int64Counter("mediacore.ops"); return err },
		"bad segment":     func() error { _, err := m.Int64Counter("mediacore.ops.Runs"); return err },
		"empty segment":   func() error { _, err := m.Int64Counter("mediacore.ops..runs"); return err },
		"counter total":   func() error { _, err := m.Int64Counter("mediacore.ops.runs_total"); return err },
		"obs counter tot": func() error { _, err := m.Float64ObservableCounter("mediacore.ops.runs.total"); return err },
		"hist no unit": func() error {
			_ = s.View("mediacore.ops.latency", 1, 2)
			_, err := m.Float64Histogram("mediacore.ops.latency")
			return err
		},
		"hist ratio unit": func() error {
			_ = s.View("mediacore.ops.share", 1, 2)
			_, err := m.Float64Histogram("mediacore.ops.share", metric.WithUnit("1"))
			return err
		},
		"hist no view": func() error {
			_, err := m.Float64Histogram("mediacore.ops.duration", metric.WithUnit("s"))
			return err
		},
		"gauge total no bracket": func() error {
			_, err := m.Int64Gauge("mediacore.ops.items_total", metric.WithUnit("1"))
			return err
		},
		"view outside prefix": func() error { return s.View("other.duration", 1) },
		"view no buckets":     func() error { return s.View("mediacore.ops.empty") },
		"view not increasing": func() error { return s.View("mediacore.ops.unsorted", 2, 1) },
	}
	for name, f := range bad {
		if err := f(); !errors.Is(err, ErrInstrumentName) {
			t.Errorf("%s: err = %v, want ErrInstrumentName", name, err)
		}
	}
	// A refused instrument is the no-op one.
	if c, _ := m.Int64Counter("mediacore.ops.runs_total"); c == nil {
		t.Error("a refused instrument must be a usable no-op, not nil")
	} else if _, isNoop := c.(noop.Int64Counter); !isNoop {
		t.Errorf("refused instrument is %T, want the no-op", c)
	}

	if err := s.View("mediacore.ops.run.duration", 0.5, 5); err != nil {
		t.Fatal(err)
	}
	good := map[string]func() error{
		"counter": func() error { _, err := m.Int64Counter("mediacore.ops.runs"); return err },
		"histogram": func() error {
			_, err := m.Float64Histogram("mediacore.ops.run.duration", metric.WithUnit("s"))
			return err
		},
		"gauge bracket": func() error {
			_, err := m.Int64Gauge("mediacore.ops.items_total", metric.WithUnit("{item}"))
			return err
		},
		"updown": func() error { _, err := m.Int64UpDownCounter("mediacore.ops.inflight"); return err },
	}
	for name, f := range good {
		if err := f(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := p.Core("ops"); !errors.Is(err, ErrScopeTaken) {
		t.Errorf("second Core(ops) = %v, want ErrScopeTaken", err)
	}
	if _, err := p.Core("Bad"); !errors.Is(err, ErrInstrumentName) {
		t.Errorf("Core(Bad) = %v, want ErrInstrumentName", err)
	}
}

func TestNamespace_CoreScopesUseAppNamespace(t *testing.T) {
	def := newTest(t, Options{AppID: "a"})
	s, err := def.Core("ops")
	if err != nil {
		t.Fatal(err)
	}
	if s.Prefix() != "mediacore.ops" {
		t.Errorf("default namespace prefix = %q, want mediacore.ops", s.Prefix())
	}

	abo := newTest(t, Options{AppID: "abo", MetricNamespace: "audiobook_organizer"})
	s, err = abo.Core("ops")
	if err != nil {
		t.Fatal(err)
	}
	if s.Prefix() != "audiobook_organizer.ops" {
		t.Errorf("prefix = %q, want audiobook_organizer.ops", s.Prefix())
	}
	c, err := s.Meter().Int64Counter("audiobook_organizer.ops.runs")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 1)
	if _, ok := gather(t, abo)["audiobook_organizer_ops_runs_total"]; !ok {
		t.Error("family audiobook_organizer_ops_runs_total missing")
	}

	for _, ns := range []string{"Bad", "1abc", "a-b", "a.b"} {
		if _, err := New(context.Background(), Options{MetricNamespace: ns}); err == nil {
			t.Errorf("New with namespace %q succeeded, want an error", ns)
		}
	}
}

func TestFamilyTaken_ModuleAndCoreShareNamespace(t *testing.T) {
	var logs bytes.Buffer
	p := newTest(t, Options{AppID: "a", MetricNamespace: "app", Modules: []string{"books"},
		Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	mod, err := p.Module("books")
	if err != nil {
		t.Fatal(err)
	}
	core, err := p.Core("ops")
	if err != nil {
		t.Fatal(err)
	}
	// One module, so the module scope is the namespace itself and its
	// "app.ops.runs" translates to the core scope's family.
	if _, err := mod.Meter().Int64Counter("app.ops.runs"); err != nil {
		t.Fatal(err)
	}
	c, err := core.Meter().Int64Counter("app.ops.runs")
	if !errors.Is(err, ErrFamilyTaken) {
		t.Fatalf("core instrument on a module-held family: err = %v, want ErrFamilyTaken", err)
	}
	if _, isNoop := c.(noop.Int64Counter); !isNoop {
		t.Errorf("refused instrument is %T, want the no-op", c)
	}
	if !slices.Contains(p.Degraded(), DegradedFamilyTaken) || !strings.Contains(logs.String(), "app_ops_runs_total") {
		t.Errorf("refusal not raised: degraded %v, logs %q", p.Degraded(), logs.String())
	}

	// The same instrument twice in one scope is one instrument.
	if _, err := mod.Meter().Int64Counter("app.ops.runs"); err != nil {
		t.Errorf("identical instrument twice: %v", err)
	}
	// Distinct families under the shared namespace register.
	if _, err := core.Meter().Int64Counter("app.ops.failures"); err != nil {
		t.Errorf("distinct family refused: %v", err)
	}

	raw := p.MeterProvider().Meter("raw")
	// Collector first, then an instrument for its name.
	if err := p.Registerer().Register(prometheus.NewCounter(prometheus.CounterOpts{Name: "app_cache_hits_total", Help: "h"})); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Int64Counter("app.cache.hits"); !errors.Is(err, ErrFamilyTaken) {
		t.Errorf("instrument on a collector-held family: err = %v, want ErrFamilyTaken", err)
	}
	// Instrument first, then a collector for its family.
	if _, err := raw.Float64Histogram("app.fetch.duration", metric.WithUnit("s")); err != nil {
		t.Fatal(err)
	}
	err = p.Registerer().Register(prometheus.NewHistogram(prometheus.HistogramOpts{Name: "app_fetch_duration_seconds", Help: "h"}))
	if !errors.Is(err, ErrFamilyTaken) {
		t.Errorf("collector on an instrument-held family: err = %v, want ErrFamilyTaken", err)
	}
	// A refused observable must not cost the others their callback.
	okObs, err := raw.Int64ObservableGauge("app.queue.depth")
	if err != nil {
		t.Fatal(err)
	}
	refusedObs, err := raw.Int64ObservableGauge("app.queue.depth", metric.WithDescription("another owner"))
	if !errors.Is(err, ErrFamilyTaken) {
		t.Fatalf("second identity for app.queue.depth: err = %v, want ErrFamilyTaken", err)
	}
	if _, err := raw.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(okObs, 4)
		return nil
	}, okObs, refusedObs); err != nil {
		t.Errorf("RegisterCallback with a refused instrument: %v", err)
	}
	if mf, ok := gather(t, p)["app_queue_depth"]; !ok || mf.GetMetric()[0].GetGauge().GetValue() != 4 {
		t.Error("the accepted observable lost its callback")
	}
}

func TestAdviceBucketsOutsideScope(t *testing.T) {
	p := newTest(t, Options{AppID: "a"})
	ctx := context.Background()
	h, err := p.MeterProvider().Meter("raw").Float64Histogram("raw.latency", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(1, 2, 3))
	if err != nil {
		t.Fatal(err)
	}
	h.Record(ctx, 1.5)
	s, err := p.Core("ops")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.View("mediacore.ops.run.duration", 0.5, 5); err != nil {
		t.Fatal(err)
	}
	sh, err := s.Meter().Float64Histogram("mediacore.ops.run.duration", metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(100, 200)) // the View wins over advice
	if err != nil {
		t.Fatal(err)
	}
	sh.Record(ctx, 1)
	fams := gather(t, p)
	for fam, want := range map[string][]float64{"raw_latency_seconds": {1, 2, 3}, "mediacore_ops_run_duration_seconds": {0.5, 5}} {
		mf, ok := fams[fam]
		if !ok {
			t.Errorf("%s missing", fam)
			continue
		}
		var got []float64
		for _, b := range mf.GetMetric()[0].GetHistogram().GetBucket() {
			got = append(got, b.GetUpperBound())
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s buckets = %v, want %v", fam, got, want)
		}
	}
}

func TestResource_ServiceNameAndSDKDefaults(t *testing.T) {
	for _, tc := range []struct {
		o    Options
		want string
	}{
		{Options{AppID: "abo", ServiceName: "audiobook-organizer"}, "audiobook-organizer"},
		{Options{AppID: "abo"}, "abo"},
	} {
		tc.o.InstanceID = "01HZXAMPLEULID0000000000000"
		tc.o.ServiceVersion = "v9.9.9"
		mf, ok := gather(t, newTest(t, tc.o))["target_info"]
		if !ok {
			t.Fatal("target_info missing")
		}
		l := labelsOf(mf.GetMetric()[0])
		if l["service_name"] != tc.want {
			t.Errorf("service_name = %q, want %q", l["service_name"], tc.want)
		}
		for _, k := range []string{"telemetry_sdk_language", "telemetry_sdk_name", "telemetry_sdk_version"} {
			if l[k] == "" {
				t.Errorf("target_info lacks %s: %v", k, l)
			}
		}
		if l["service_instance_id"] != tc.o.InstanceID || l["service_version"] != "v9.9.9" || l["deployment_environment"] != DefaultEnvironment {
			t.Errorf("target_info = %v", l)
		}
		for k := range l {
			if strings.HasPrefix(k, "host_") || strings.HasPrefix(k, "process_") || strings.HasPrefix(k, "os_") {
				t.Errorf("target_info carries detector label %s: no host or process detector may run", k)
			}
		}
	}
	mf := gather(t, newTest(t, Options{AppID: "a", Environment: "dev"}))["target_info"]
	if l := labelsOf(mf.GetMetric()[0]); l["deployment_environment"] != "dev" || len(l["service_instance_id"]) != 32 {
		t.Errorf("environment or drawn instance id wrong: %v", l)
	}
}

func TestModuleNamespace_SingleAndMulti(t *testing.T) {
	one := newTest(t, Options{MetricNamespace: "abo", Modules: []string{"books"}})
	s, err := one.Module("books")
	if err != nil {
		t.Fatal(err)
	}
	if s.Prefix() != "abo" {
		t.Errorf("single module prefix = %q, want abo", s.Prefix())
	}
	two := newTest(t, Options{MetricNamespace: "suite", Modules: []string{"tv", "movies"}})
	s, err = two.Module("tv")
	if err != nil {
		t.Fatal(err)
	}
	if s.Prefix() != "suite.tv" {
		t.Errorf("multi module prefix = %q, want suite.tv", s.Prefix())
	}
	if _, err := two.Module("music"); err == nil {
		t.Error("a module not compiled in got a scope")
	}
	if _, err := two.Module("tv"); !errors.Is(err, ErrScopeTaken) {
		t.Errorf("second Module(tv) = %v, want ErrScopeTaken", err)
	}
}

func TestModuleNamespace_StableUnderEnablement(t *testing.T) {
	// Both modules compiled in; "movies" disabled in the first boot (no scope
	// is ever created for it), enabled in the second. tv's series must not
	// move.
	families := func(createMovies bool) []string {
		p := newTest(t, Options{MetricNamespace: "suite", Modules: []string{"tv", "movies"}})
		if createMovies {
			if _, err := p.Module("movies"); err != nil {
				t.Fatal(err)
			}
		}
		tv, err := p.Module("tv")
		if err != nil {
			t.Fatal(err)
		}
		c, err := tv.Meter().Int64Counter(tv.Prefix() + ".scan.files")
		if err != nil {
			t.Fatal(err)
		}
		c.Add(context.Background(), 1)
		var out []string
		for name := range gather(t, p) {
			if strings.HasPrefix(name, "suite_") {
				out = append(out, name)
			}
		}
		slices.Sort(out)
		return out
	}
	off, on := families(false), families(true)
	if !slices.Equal(off, on) || !slices.Equal(off, []string{"suite_tv_scan_files_total"}) {
		t.Errorf("tv series changed with movies' enablement: off %v, on %v", off, on)
	}
}

// TestTranslationStrategyPinned flips a process global: neither it nor any
// sibling test that builds a Provider may call t.Parallel.
func TestTranslationStrategyPinned(t *testing.T) {
	// The test sets the deprecated process-global scheme on purpose: it is
	// what the exporter would follow if its translation strategy were not
	// pinned, so flipping it is the only way to prove the pin holds.
	//lint:ignore SA1019 reads the process-global scheme to restore it
	prev := model.NameValidationScheme
	//lint:ignore SA1019 restores the process-global scheme
	t.Cleanup(func() { model.NameValidationScheme = prev })
	names := func(scheme model.ValidationScheme) []string {
		//lint:ignore SA1019 flips the scheme the pin must ignore
		model.NameValidationScheme = scheme
		p := newTest(t, Options{AppID: "a", MetricNamespace: "abo"})
		m := p.MeterProvider().Meter("pin")
		ctx := context.Background()
		c, _ := m.Int64Counter("abo.ops.runs")
		c.Add(ctx, 1, metric.WithAttributes(attribute.String("def.id", "x")))
		h, _ := m.Float64Histogram("ai_dispatch.slot_wait", metric.WithUnit("s"))
		h.Record(ctx, 1)
		g, _ := m.Int64Gauge("abo.books_total", metric.WithUnit("{book}"))
		g.Record(ctx, 3)
		var out []string
		for name, mf := range gather(t, p) {
			if strings.HasPrefix(name, "go_") || strings.HasPrefix(name, "process_") || strings.HasPrefix(name, "promhttp_") {
				continue
			}
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					out = append(out, name+"{"+lp.GetName()+"}")
				}
			}
			out = append(out, name)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	legacy, utf8 := names(model.LegacyValidation), names(model.UTF8Validation)
	if !slices.Equal(legacy, utf8) {
		t.Errorf("names follow the global validation scheme:\nlegacy %v\nutf8   %v", legacy, utf8)
	}
	for _, want := range []string{"abo_ops_runs_total", "abo_ops_runs_total{def_id}", "ai_dispatch_slot_wait_seconds", "abo_books_total"} {
		if !slices.Contains(utf8, want) {
			t.Errorf("translated names lack %s: %v", want, utf8)
		}
	}
}

// memExporter is an in-memory OTLP metric exporter for the hook.
type memExporter struct {
	mu   sync.Mutex
	got  []metricdata.ResourceMetrics
	shut bool
}

func (e *memExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.CumulativeTemporalitySelector(k)
}

func (e *memExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *memExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, *rm)
	return nil
}

func (e *memExporter) ForceFlush(context.Context) error { return nil }

func (e *memExporter) Shutdown(context.Context) error {
	e.mu.Lock()
	e.shut = true
	e.mu.Unlock()
	return nil
}

func (e *memExporter) names() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, rm := range e.got {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				out = append(out, m.Name)
			}
		}
	}
	return out
}

// withMetricExporter is the gRPC exporters with the metric one replaced by f.
func withMetricExporter(f func(context.Context, ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error)) exporters {
	e := grpcExporters()
	e.metric = f
	return e
}

func TestShutdownFlushes(t *testing.T) {
	exp := &memExporter{}
	mem := withMetricExporter(func(context.Context, ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error) { return exp, nil })
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		p, err := newProvider(ctx, Options{AppID: "a", OTLP: &OTLPMetrics{Endpoint: "collector.example.invalid:4317", Interval: time.Hour}}, mem)
		if err != nil {
			t.Fatal(err)
		}
		c, err := p.MeterProvider().Meter("flush").Int64Counter("flush.things")
		if err != nil {
			t.Fatal(err)
		}
		c.Add(ctx, 1)
		// No interval elapses: only the shutdown flush can deliver it.
		if err := p.Shutdown(ctx); err != nil {
			t.Fatalf("shutdown: %v", err)
		}
		if err := p.Shutdown(ctx); err != nil {
			t.Errorf("second shutdown (idempotent): %v", err)
		}
		synctest.Wait()
	})
	if !slices.Contains(exp.names(), "flush.things") || !exp.shut {
		t.Fatalf("last batch not exported on shutdown: names %v, exporter shut down %v", exp.names(), exp.shut)
	}
}

// budgetExporter is a span exporter that records how much of its shutdown
// context's deadline was left when the batcher handed it over.
type budgetExporter struct {
	left     time.Duration
	deadline bool
}

func (*budgetExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error { return nil }
func (b *budgetExporter) Shutdown(ctx context.Context) error {
	dl, ok := ctx.Deadline()
	b.deadline = ok
	if ok {
		b.left = time.Until(dl)
	}
	return nil
}

// TestShutdown_TracerBudget pins the span batcher's share of the shutdown
// deadline. A Provider with a meter reader halves it so a hung tracer cannot
// starve the metric flush; a tracing-only Provider (ABO's tracing half) has
// no metric flush and passes the whole deadline on. Before this, the
// tracing-only Provider halved too, so behind the facade's own split the
// span batcher got a quarter of the shutdown budget instead of a half.
func TestShutdown_TracerBudget(t *testing.T) {
	const deadline = 8 * time.Second
	for _, tc := range []struct {
		name       string
		prometheus bool
		want       time.Duration
	}{
		{"tracing only: the whole deadline", false, deadline},
		{"with a meter reader: half", true, deadline / 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rec := &budgetExporter{}
				e := grpcExporters()
				e.span = func(context.Context, ...otlptracegrpc.Option) (sdktrace.SpanExporter, error) { return rec, nil }
				p, err := newProvider(context.Background(), Options{
					AppID:      "a",
					Prometheus: tc.prometheus,
					Tracing:    &OTLPTracing{Endpoint: "collector.example.invalid:4317"},
					Redactor:   log.NewRedactor(nil),
				}, e)
				if err != nil {
					t.Fatal(err)
				}
				// Fake time: nothing elapses unless every goroutine blocks, so
				// the share is exact.
				ctx, cancel := context.WithTimeout(context.Background(), deadline)
				defer cancel()
				if err := p.Shutdown(ctx); err != nil {
					t.Fatalf("shutdown: %v", err)
				}
				if !rec.deadline || rec.left != tc.want {
					t.Errorf("span exporter shutdown got deadline=%v with %v left, want %v", rec.deadline, rec.left, tc.want)
				}
			})
		})
	}
}

func TestOTLPBadEndpointDegrades(t *testing.T) {
	const sentinel = "S3NT1NEL"
	for _, ep := range []string{"://x", "ftp://h:1", "nohostport", "http://host", "https://u:" + sentinel + "@h:4317"} {
		var logs bytes.Buffer
		p := newTest(t, Options{AppID: "a", Logger: slog.New(slog.NewTextHandler(&logs, nil)),
			OTLP:    &OTLPMetrics{Endpoint: ep},
			Tracing: &OTLPTracing{Endpoint: ep}, Redactor: log.NewRedactor(nil)})
		if d := p.Degraded(); !slices.Equal(d, []string{DegradedOTLPDisabled, DegradedTracingDisabled}) {
			t.Errorf("%q: Degraded = %v, want both OTLP tokens", ep, d)
		}
		c, _ := p.MeterProvider().Meter("bad").Int64Counter("bad_ep.things")
		c.Add(context.Background(), 1)
		if !strings.Contains(scrape(t, p), "bad_ep_things_total") {
			t.Errorf("%q: the scrape lost an instrument after a bad endpoint", ep)
		}
		if strings.Contains(logs.String(), sentinel) || strings.Contains(logs.String(), "nohostport") {
			t.Errorf("%q: the configured endpoint reached the log: %s", ep, logs.String())
		}
	}
	// An exporter that cannot be built degrades the same way.
	refused := withMetricExporter(func(context.Context, ...otlpmetricgrpc.Option) (sdkmetric.Exporter, error) {
		return nil, errors.New("exporter refused")
	})
	p, err := newProvider(context.Background(), Options{AppID: "a", OTLP: &OTLPMetrics{Endpoint: "127.0.0.1:4317"}}, refused)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	if d := p.Degraded(); !slices.Equal(d, []string{DegradedOTLPDisabled}) {
		t.Errorf("Degraded = %v, want [%s]", d, DegradedOTLPDisabled)
	}
	if healthy := newTest(t, Options{AppID: "a"}); len(healthy.Degraded()) != 0 {
		t.Errorf("a Provider without OTLP is degraded: %v", healthy.Degraded())
	}
}
