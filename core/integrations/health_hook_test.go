// file: core/integrations/health_hook_test.go
// version: 0.1.0
// guid: 30706ef8-3738-4408-bd88-d4faa5de284a
// last-edited: 2026-11-28

package integrations_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
)

type hookCall struct {
	status integrations.HealthStatus
	first  bool
}

// TestOptionsHealthChanged_FirstFlag: HealthChanged fires for the first status
// observed of an instance with first set, for each later move with first
// clear, and not for a probe that leaves the status where it was; a ref deleted
// and created again is observed first again, because Delete drops its last
// health.
func TestOptionsHealthChanged_FirstFlag(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var mu sync.Mutex
	var calls []hookCall
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.HealthChanged = func(_ integrations.Ref, h integrations.Health, first bool) {
			mu.Lock()
			calls = append(calls, hookCall{h.Status, first})
			mu.Unlock()
		}
	})
	var status atomic.Int32
	status.Store(int32(integrations.HealthOK))
	e.fake.HealthFn = func(context.Context, *integrationstest.FakeInstance) integrations.Health {
		return integrations.Health{Status: integrations.HealthStatus(status.Load())}
	}
	probe := func() {
		t.Helper()
		if _, err := r.Health(ctx, signalRef); err != nil {
			t.Fatal(err)
		}
	}
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	probe()
	probe()
	status.Store(int32(integrations.HealthDown))
	probe()
	probe()
	if err := r.Delete(admin(), signalRef); err != nil {
		t.Fatal(err)
	}
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	probe()

	mu.Lock()
	defer mu.Unlock()
	want := []hookCall{
		{integrations.HealthOK, true},
		{integrations.HealthDown, false},
		{integrations.HealthDown, true},
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("HealthChanged calls = %v, want %v", calls, want)
	}
}

// TestOptionsHealthChanged_RunsUnderTheHealthLock: two concurrent probes of one
// ref cannot report in the other order from the one they were recorded in. The
// first hook call is parked; a second probe with another status must not reach
// the hook until it returns.
func TestOptionsHealthChanged_RunsUnderTheHealthLock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var (
		calls   atomic.Int32
		entered = make(chan struct{})
		release = make(chan struct{})
		once    sync.Once
		mu      sync.Mutex
		order   []integrations.HealthStatus
	)
	unpark := func() { once.Do(func() { close(release) }) }
	r := e.registry(t, nil, func(o *integrations.Options) {
		o.HealthChanged = func(_ integrations.Ref, h integrations.Health, _ bool) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			mu.Lock()
			order = append(order, h.Status)
			mu.Unlock()
		}
	})
	t.Cleanup(unpark) // before the registry's own Close, which would wait on the parked probe
	var status atomic.Int32
	status.Store(int32(integrations.HealthDown))
	e.fake.HealthFn = func(context.Context, *integrationstest.FakeInstance) integrations.Health {
		return integrations.Health{Status: integrations.HealthStatus(status.Load())}
	}
	mustPut(t, admin(), r, signal("http://192.0.2.20/"), cred("alfred-1"))
	probe := func() chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = r.Health(ctx, signalRef)
		}()
		return done
	}
	first := probe()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first probe never reached the hook")
	}
	status.Store(int32(integrations.HealthDegraded))
	second := probe()
	time.Sleep(200 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d hook calls while the first was parked, want 1: the hook runs outside the health lock", n)
	}
	unpark()
	for _, d := range []chan struct{}{first, second} {
		select {
		case <-d:
		case <-time.After(5 * time.Second):
			t.Fatal("a probe never finished")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []integrations.HealthStatus{integrations.HealthDown, integrations.HealthDegraded}; !slices.Equal(order, want) {
		t.Fatalf("reported %v, want the recorded order %v", order, want)
	}
}
