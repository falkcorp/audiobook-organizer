// file: core/internal/sse/limits_test.go
// version: 0.1.0
// guid: 5c1f9d3e-8a2b-4e67-b0d4-7f3a6e91c2b8
// last-edited: 2026-11-20

package sse_test

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/internal/sse"
)

// liveLimits is an Options.Limits a test reloads.
type liveLimits struct{ v atomic.Pointer[sse.Limits] }

func (l *liveLimits) set(v sse.Limits) { l.v.Store(&v) }
func (l *liveLimits) get() sse.Limits  { return *l.v.Load() }

// TestLimits_CapsAreReadAtEachSubscribe: a reloaded MaxPerBucket or MaxClients
// applies to the next Subscribe; lowering one ends no open subscriber; a
// field the func leaves zero falls back to the option it was built with.
func TestLimits_CapsAreReadAtEachSubscribe(t *testing.T) {
	var live liveLimits
	live.set(sse.Limits{MaxPerBucket: 1})
	b := broker(t, func(o *sse.Options) { o.Limits = live.get }) // built with 4 clients, 2 per bucket
	open := func(bucket string) error {
		_, err := b.Subscribe(sse.Subscription{Bucket: bucket, Topics: []string{"a.x"}, Judge: &judge{}})
		return err
	}
	if err := open("u-batgirl"); err != nil {
		t.Fatal(err)
	}
	if err := open("u-batgirl"); !errors.Is(err, sse.ErrTooManyBucket) {
		t.Fatalf("second stream of a bucket at a reloaded cap of 1: %v, want ErrTooManyBucket", err)
	}
	live.set(sse.Limits{MaxPerBucket: 2})
	if err := open("u-batgirl"); err != nil {
		t.Fatalf("after raising the bucket cap to 2: %v", err)
	}
	live.set(sse.Limits{}) // zero: the built 2 per bucket and 4 clients
	if err := open("u-batgirl"); !errors.Is(err, sse.ErrTooManyBucket) {
		t.Fatalf("a zero bucket cap did not fall back to the built 2: %v", err)
	}
	live.set(sse.Limits{MaxClients: 2, MaxPerBucket: -1})
	if err := open("u-nightwing"); !errors.Is(err, sse.ErrTooManyClients) {
		t.Fatalf("a third stream at a reloaded global cap of 2: %v, want ErrTooManyClients", err)
	}
	live.set(sse.Limits{MaxClients: 3, MaxPerBucket: -1}) // -1: the built 2 per bucket
	if err := open("u-nightwing"); err != nil {
		t.Fatalf("after raising the global cap to 3: %v", err)
	}
}

// TestLimits_WriteTimeoutIsReadAtEachWrite: a write's deadline is the
// reloaded WriteTimeout at the time of that write, and a zero one falls back
// to the option the broker was built with.
func TestLimits_WriteTimeoutIsReadAtEachWrite(t *testing.T) {
	var live liveLimits
	live.set(sse.Limits{WriteTimeout: 40 * time.Second})
	b := broker(t, func(o *sse.Options) { o.WriteTimeout = 2 * time.Second; o.Limits = live.get })
	s, _ := sub(t, b, nil)
	rec, done := run(t.Context(), s)
	latest := func() time.Duration {
		t.Helper()
		rec.mu.Lock()
		defer rec.mu.Unlock()
		for _, d := range slices.Backward(rec.deadlines) {
			if !d.IsZero() { // skip the clear after a finished write
				return time.Until(d)
			}
		}
		t.Fatal("no write deadline was set")
		return 0
	}
	pub(b, "a.x", "")
	rec.awaitText(t, nonce+"-1")
	if d := latest(); d <= 30*time.Second || d > 40*time.Second {
		t.Errorf("deadline %v away, want the reloaded 40s", d)
	}
	live.set(sse.Limits{})
	pub(b, "a.x", "")
	rec.awaitText(t, nonce+"-2")
	if d := latest(); d <= 0 || d > 2*time.Second {
		t.Errorf("deadline %v away, want the built 2s once the reload is zero", d)
	}
	b.Close()
	finished(t, done)
}
