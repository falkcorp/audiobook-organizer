// file: core/events/helpers_test.go
// version: 1.0.0
// guid: a81be974-5b58-43f5-8e59-d161cdc733b4
// last-edited: 2026-11-10

package events_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// Fixture payloads and topics: Gotham's villains escape, Wayne Enterprises
// watches (Batman names only).

type Sighting struct {
	Villain string `json:"villain"`
	Block   int    `json:"block"`
}

type Alarm struct {
	Vault string `json:"vault"`
}

func escaped() events.Topic[Sighting] { return events.NewTopic[Sighting]("gotham.villain.escaped.v1") }
func tripped() events.Topic[Alarm]    { return events.NewTopic[Alarm]("wayne.vault.tripped.v1") }
func signal() events.Topic[Sighting]  { return events.NewTopic[Sighting]("core.signal.lit.v1") }

// spec is t's catalog entry with the owner's own permission.
func spec[T any](t events.Topic[T], perm rbac.Permission) events.TopicSpec {
	return t.Spec(events.TopicSpec{Permission: rbac.Need(perm)})
}

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *logBuf) count(substr string) int { return strings.Count(l.String(), substr) }

func (l *logBuf) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(l, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// provider builds a Provider and the core events scope, as the kernel does.
func provider(t *testing.T) (*metrics.Provider, *metrics.Scope) {
	t.Helper()
	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("events", "topic", "subscriber", "reason")
	if err != nil {
		t.Fatal(err)
	}
	return p, sc
}

// newBus is a bus with core's signal topic, gotham's escaped topic and
// wayne's tripped topic declared.
func newBus(t *testing.T, o events.BusOptions) *events.Bus {
	t.Helper()
	b, err := events.NewBus(o)
	if err != nil {
		t.Fatal(err)
	}
	must(t, b.Declare("core", spec(signal(), "core:system.view")))
	must(t, b.Declare("gotham", spec(escaped(), "gotham:view")))
	must(t, b.Declare("wayne", spec(tripped(), "wayne:view")))
	return b
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// closeBus closes b with a generous budget at cleanup.
func closeBus(t *testing.T, b *events.Bus) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := b.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

// recorder collects what a handler received.
type recorder[T any] struct {
	mu  sync.Mutex
	got []events.Envelope[T]
	ch  chan struct{}
}

func newRecorder[T any]() *recorder[T] { return &recorder[T]{ch: make(chan struct{}, 1<<16)} }

func (r *recorder[T]) handle(_ context.Context, e events.Envelope[T]) {
	r.mu.Lock()
	r.got = append(r.got, e)
	r.mu.Unlock()
	r.ch <- struct{}{}
}

func (r *recorder[T]) envelopes() []events.Envelope[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]events.Envelope[T](nil), r.got...)
}

// wait blocks until n more events arrived or fails after a timeout.
func (r *recorder[T]) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for range n {
		select {
		case <-r.ch:
		case <-deadline:
			t.Fatalf("timed out waiting for events: got %d", len(r.envelopes()))
		}
	}
}
