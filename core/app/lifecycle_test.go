// file: core/app/lifecycle_test.go
// version: 1.0.1
// guid: 7e3a9c51-2f8b-4d06-b1c4-5a9e0d7f3b28
// last-edited: 2026-11-08

package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
)

// recorder is the ordered event list one Run produces.
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// fakeServer is an R1Server that records Serve and Shutdown.
type fakeServer struct {
	rec  *recorder
	stop chan struct{}
	once sync.Once
}

func (s *fakeServer) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 8484} }

func (s *fakeServer) Serve() error {
	s.rec.add("serve")
	<-s.stop
	return nil
}

func (s *fakeServer) Shutdown(context.Context) error {
	s.rec.add("listener drain")
	s.once.Do(func() { close(s.stop) })
	return nil
}

// TestRun_StopOrderThroughR1Listen is the R1-15 stop-order test (06 §6.5.2,
// 05-health §5H.4) through app.Options.R1Listen, the seam ABO's listener
// stack takes: hooks start in order, then workers, then R1Values once and the
// listener; a cancelled ctx (main's SIGTERM) drains the listener first, then
// cancels the workers, then stops the hooks in exact reverse, all within the
// budget. The listener receives R1Values' address and TLS files.
func TestRun_StopOrderThroughR1Listen(t *testing.T) {
	rec := &recorder{}
	hook := func(name string) module.Hook {
		return module.Hook{Name: name,
			Start: func(context.Context) error { rec.add(name + " start"); return nil },
			Stop: func(ctx context.Context) error {
				if _, ok := ctx.Deadline(); !ok {
					return errors.New("stop ctx carries no deadline")
				}
				rec.add(name + " stop")
				return nil
			}}
	}
	m := testModule{id: "wayne", register: func(r *module.Registrar) error {
		r.Hook(hook("wayne.manor"))
		r.Hook(hook("wayne.cave"))
		r.Worker(module.Worker{Name: "patrol", Policy: module.DegradeOnExit, Run: func(ctx context.Context) error {
			rec.add("worker start")
			<-ctx.Done()
			rec.add("worker stop")
			return nil
		}})
		return nil
	}}
	var gotOpts httpapi.ListenOptions
	listening := make(chan struct{})
	a, err := New(t.Context(), Options{
		AppID: "gotham", LogOutput: &syncBuffer{}, Env: []string{},
		R1Values: func() R1Values {
			rec.add("r1values")
			return R1Values{Listen: "192.0.2.1:8484", TLSCertFile: "batcave.crt", TLSKeyFile: "batcave.key",
				ShutdownBudget: 5 * time.Second}
		},
		R1Listen: func(o httpapi.ListenOptions, h http.Handler, _ *slog.Logger) (R1Server, error) {
			if h == nil {
				return nil, errors.New("no root handler")
			}
			gotOpts = o
			rec.add("listen")
			close(listening)
			return &fakeServer{rec: rec, stop: make(chan struct{})}, nil
		},
	}, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Metrics().Shutdown(context.Background()) })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-listening:
	case <-time.After(10 * time.Second):
		t.Fatal("the listener never opened")
	}
	// Wait until Serve and the worker are running before the stop.
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(rec.list(), "serve") || !slices.Contains(rec.list(), "worker start") {
		if time.Now().After(deadline) {
			t.Fatalf("serve or the worker never started: %v", rec.list())
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the stop took %s, over the 5s budget", took)
	}
	if gotOpts.Addr != "192.0.2.1:8484" || gotOpts.TLSCertFile != "batcave.crt" || gotOpts.TLSKeyFile != "batcave.key" {
		t.Fatalf("R1Listen got %+v, want R1Values' address and TLS files", gotOpts)
	}

	got := rec.list()
	// The worker goroutine's first line races R1Values and Serve (the
	// supervisor launches it before step 7 and does not wait); everything else
	// is ordered.
	want := []string{"wayne.manor start", "wayne.cave start", "r1values", "listen",
		"listener drain", "worker stop", "wayne.cave stop", "wayne.manor stop"}
	var ordered []string
	for _, e := range got {
		if e != "worker start" && e != "serve" {
			ordered = append(ordered, e)
		}
	}
	if !slices.Equal(ordered, want) {
		t.Fatalf("start/stop order:\n got  %v\n want %v (full: %v)", ordered, want, got)
	}
}

// TestTelemetry_TracingReachesProvider: Options.Telemetry's span exporter
// reaches the kernel's Provider, so the TracerProvider InstallProcessDefaults
// makes global is the SDK's, not the no-op one, and its OTLP push and
// resource attributes are the app's (production tracing survives the move to
// the App's Provider). Without Telemetry the TracerProvider is the no-op.
func TestTelemetry_TracingReachesProvider(t *testing.T) {
	base := Options{AppID: "gotham", LogOutput: &syncBuffer{}, Env: []string{}}
	off, err := New(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = off.Metrics().Shutdown(context.Background()) })
	if _, sdk := off.Metrics().TracerProvider().(*sdktrace.TracerProvider); sdk {
		t.Fatal("tracing is on without Telemetry")
	}

	on := base
	on.Telemetry = &Telemetry{Tracing: &metrics.OTLPTracing{Endpoint: "http://192.0.2.1:4317"},
		Environment: "gotham-staging", InstanceID: "01JWAYNE0000000000000000"}
	a, err := New(t.Context(), on)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = a.Metrics().Shutdown(ctx)
	})
	if _, sdk := a.Metrics().TracerProvider().(*sdktrace.TracerProvider); !sdk {
		t.Fatalf("TracerProvider = %T, want the SDK's with Telemetry.Tracing set (degraded: %v)",
			a.Metrics().TracerProvider(), a.Metrics().Degraded())
	}
	if slices.Contains(a.Metrics().Degraded(), metrics.DegradedTracingDisabled) {
		t.Fatalf("tracing degraded: %v", a.Metrics().Degraded())
	}
}
