// file: core/coretest/provider.go
// version: 1.0.0
// guid: f6331f4a-648c-4e98-acfd-83f2e7a28010
// last-edited: 2026-11-11

package coretest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
)

// The metadata provider test kit (05-metadata M3.9): FakeProvider, the
// in-memory metadata.Provider a module tests its chain against, and
// ProviderHTTP, a providerhttp.Registry over an httptest.Server with a fake
// clock and an in-memory gate store, for tests of an adapter or of the gate.

// FakeProvider is a scripted metadata.Provider[Q, R]. It answers from a queue
// of scripted steps (the last step repeats), after an optional latency that
// honours the caller's context, and counts its calls and its concurrency. It
// implements metadata.Caps through Accepting (everything is accepted by
// default).
type FakeProvider[Q, R any] struct {
	ref integrations.Ref

	mu       sync.Mutex
	steps    []fakeStep[R]
	script   func(context.Context, Q) ([]R, error)
	latency  time.Duration
	accepts  func(Q) bool
	health   integrations.Health
	queries  []Q
	inflight int
	peak     int

	calls  atomic.Int64
	closed atomic.Int64
}

type fakeStep[R any] struct {
	rs  []R
	err error
}

// NewFakeProvider returns a provider of ref that answers with no results
// until told otherwise.
func NewFakeProvider[Q, R any](ref integrations.Ref) *FakeProvider[Q, R] {
	return &FakeProvider[Q, R]{ref: ref, health: integrations.Health{Status: integrations.HealthOK}}
}

// Answer queues one answer: the next call returns rs. The last queued step
// repeats forever.
func (f *FakeProvider[Q, R]) Answer(rs ...R) *FakeProvider[Q, R] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, fakeStep[R]{rs: rs})
	return f
}

// Fail queues one failure: the next call returns an *integrations.Error of
// the given class (status may be 0).
func (f *FakeProvider[Q, R]) Fail(class integrations.Class, status int) *FakeProvider[Q, R] {
	return f.FailWith(&integrations.Error{Ref: f.ref, Op: "search", Class: class, Status: status})
}

// FailWith queues one failure with err itself.
func (f *FakeProvider[Q, R]) FailWith(err error) *FakeProvider[Q, R] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.steps = append(f.steps, fakeStep[R]{err: err})
	return f
}

// Script replaces the queue with fn, which sees every call.
func (f *FakeProvider[Q, R]) Script(fn func(context.Context, Q) ([]R, error)) *FakeProvider[Q, R] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = fn
	return f
}

// Latency makes every call take d (or until its context ends).
func (f *FakeProvider[Q, R]) Latency(d time.Duration) *FakeProvider[Q, R] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.latency = d
	return f
}

// Accepting sets the metadata.Caps answer; nil accepts every query.
func (f *FakeProvider[Q, R]) Accepting(fn func(Q) bool) *FakeProvider[Q, R] {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepts = fn
	return f
}

// Ref is the provider's instance.
func (f *FakeProvider[Q, R]) Ref() integrations.Ref { return f.ref }

// Accepts is metadata.Caps.
func (f *FakeProvider[Q, R]) Accepts(q Q) bool {
	f.mu.Lock()
	fn := f.accepts
	f.mu.Unlock()
	return fn == nil || fn(q)
}

// Search is the scripted call.
func (f *FakeProvider[Q, R]) Search(ctx context.Context, q Q) ([]R, error) {
	f.calls.Add(1)
	f.mu.Lock()
	f.queries = append(f.queries, q)
	f.inflight++
	f.peak = max(f.peak, f.inflight)
	lat, script := f.latency, f.script
	var step fakeStep[R]
	if n := len(f.steps); n > 0 {
		step = f.steps[0]
		if n > 1 {
			f.steps = f.steps[1:]
		}
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	if lat > 0 {
		t := time.NewTimer(lat)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if script != nil {
		return script(ctx, q)
	}
	return step.rs, step.err
}

// Calls is how many times Search ran.
func (f *FakeProvider[Q, R]) Calls() int { return int(f.calls.Load()) }

// Queries are the questions Search was asked, in order.
func (f *FakeProvider[Q, R]) Queries() []Q {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Q(nil), f.queries...)
}

// MaxConcurrent is the most Search calls that were in flight at once.
func (f *FakeProvider[Q, R]) MaxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

// Health is integrations.Instance's probe.
func (f *FakeProvider[Q, R]) Health(context.Context) integrations.Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health
}

// Close counts the Close.
func (f *FakeProvider[Q, R]) Close() error {
	f.closed.Add(1)
	return nil
}

// Closed is how many times Close ran.
func (f *FakeProvider[Q, R]) Closed() int { return int(f.closed.Load()) }

// ProviderClock is the fake clock of ProviderHTTP: Sleep advances it at once
// and records the wait, so a back-off or a bucket wait is asserted, never
// slept.
type ProviderClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

// providerClockStart is where a ProviderClock begins: a fixed instant, so a
// test's times (and the quota day) never depend on the wall clock.
func providerClockStart() time.Time { return time.Date(2039, 4, 1, 22, 0, 0, 0, time.UTC) }

// at returns the clock's time, starting it on first use. c.mu is held.
func (c *ProviderClock) at() time.Time {
	if c.t.IsZero() {
		c.t = providerClockStart()
	}
	return c.t
}

// Now is the clock's time.
func (c *ProviderClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at()
}

// Advance moves the clock forward by d.
func (c *ProviderClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.at().Add(d)
}

// Sleep is providerhttp.Options.Sleep.
func (c *ProviderClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sleeps = append(c.sleeps, d)
	c.t = c.at().Add(d)
	return nil
}

// Sleeps are the waits Sleep was asked for, in order.
func (c *ProviderClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// ProviderHTTPKit is what ProviderHTTP builds.
type ProviderHTTPKit struct {
	// Registry is the gate under test. Its LANAllow admits Server, so an
	// instance built through Registry.Outbound reaches it without Local.
	Registry *providerhttp.Registry
	// Server is the loopback upstream; its handler is set with Handle.
	Server *httptest.Server
	// Clock drives the Registry: now, back-off and bucket waits.
	Clock *ProviderClock

	mu      sync.Mutex
	handler http.HandlerFunc
	hits    atomic.Int64
}

// ProviderHTTP is a providerhttp.Registry over an httptest.Server with a fake
// clock, no jitter and an in-memory store (05-metadata M3.9). mut edits the
// Options before the Registry is built (hold limits, Disabled, a Resolver).
// The server answers 200 with "{}" until Handle says otherwise.
func ProviderHTTP(t testing.TB, mut ...func(*providerhttp.Options)) *ProviderHTTPKit {
	t.Helper()
	k := &ProviderHTTPKit{
		Clock: &ProviderClock{},
	}
	k.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.hits.Add(1)
		k.mu.Lock()
		h := k.handler
		k.mu.Unlock()
		if h == nil {
			_, _ = w.Write([]byte("{}"))
			return
		}
		h(w, r)
	}))
	t.Cleanup(k.Server.Close)
	o := providerhttp.Options{
		Now:      k.Clock.Now,
		Sleep:    k.Clock.Sleep,
		Jitter:   func(time.Duration) time.Duration { return 0 },
		LANAllow: func() []string { return []string{k.Host()} },
	}
	for _, m := range mut {
		m(&o)
	}
	r, err := providerhttp.New(o)
	if err != nil {
		t.Fatalf("coretest.ProviderHTTP: %v", err)
	}
	t.Cleanup(r.Close)
	k.Registry = r
	return k
}

// Handle sets the upstream's handler.
func (k *ProviderHTTPKit) Handle(h http.HandlerFunc) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.handler = h
}

// Hits is how many requests reached the upstream.
func (k *ProviderHTTPKit) Hits() int64 { return k.hits.Load() }

// URL is the upstream's base URL.
func (k *ProviderHTTPKit) URL() string { return k.Server.URL }

// Host is the upstream's "host:port".
func (k *ProviderHTTPKit) Host() string { return k.Server.Listener.Addr().String() }

// Client builds ref's client with lim and the upstream trusted for private
// addresses (it is on loopback).
func (k *ProviderHTTPKit) Client(ref integrations.Ref, lim integrations.Limits) *providerhttp.Client {
	return k.Registry.Client(ref, providerhttp.Policy{Limits: lim, Private: func(string) bool { return true }})
}
