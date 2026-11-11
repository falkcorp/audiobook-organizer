// file: core/coretest/provider_test.go
// version: 1.0.0
// guid: 49748174-3a39-474b-9142-1c213b94db56
// last-edited: 2026-11-11

package coretest_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/coretest"
	"github.com/falkcorp/mediacore/integrations"
)

var batcave = integrations.Ref{Kind: integrations.KindMetadata, Instance: "batcave"}

// TestFakeProvider covers the scripting surface a module's tests lean on.
func TestFakeProvider(t *testing.T) {
	ctx := context.Background()
	f := coretest.NewFakeProvider[string, int](batcave)
	if f.Ref() != batcave || !f.Accepts("x") || f.Health(ctx).Status != integrations.HealthOK {
		t.Fatal("defaults")
	}
	f.Answer(1, 2).Fail(integrations.ClassTransient, 503).FailWith(errors.New("odd")).Answer(9)
	if rs, err := f.Search(ctx, "a"); err != nil || len(rs) != 2 {
		t.Fatalf("step 1: %v %v", rs, err)
	}
	var ie *integrations.Error
	if _, err := f.Search(ctx, "b"); !errors.As(err, &ie) || ie.Class != integrations.ClassTransient || ie.Status != 503 {
		t.Fatalf("step 2: %v", err)
	}
	if _, err := f.Search(ctx, "c"); err == nil || err.Error() != "odd" {
		t.Fatalf("step 3: %v", err)
	}
	for range 2 { // the last step repeats
		if rs, err := f.Search(ctx, "d"); err != nil || len(rs) != 1 || rs[0] != 9 {
			t.Fatalf("step 4: %v %v", rs, err)
		}
	}
	if f.Calls() != 5 || len(f.Queries()) != 5 || f.Queries()[2] != "c" {
		t.Errorf("calls=%d queries=%v", f.Calls(), f.Queries())
	}

	f.Accepting(func(q string) bool { return q == "ok" })
	if f.Accepts("no") || !f.Accepts("ok") {
		t.Error("Accepting")
	}
	f.Script(func(_ context.Context, q string) ([]int, error) { return []int{len(q)}, nil })
	if rs, _ := f.Search(ctx, "four"); rs[0] != 4 {
		t.Errorf("Script: %v", rs)
	}

	slow := coretest.NewFakeProvider[string, int](batcave).Latency(time.Minute)
	cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := slow.Search(cctx, "x"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("latency honours the context: %v", err)
	}
	done, stop := context.WithCancel(ctx)
	stop()
	if _, err := coretest.NewFakeProvider[string, int](batcave).Search(done, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("a done context: %v", err)
	}
	if slow.MaxConcurrent() != 1 {
		t.Errorf("MaxConcurrent = %d", slow.MaxConcurrent())
	}
	if f.Close() != nil || f.Closed() != 1 {
		t.Error("Close")
	}
}

// TestProviderHTTP covers the gate kit: the loopback upstream is reachable
// through the Registry, the clock is the Registry's, and Handle swaps the
// upstream's behaviour.
func TestProviderHTTP(t *testing.T) {
	kit := coretest.ProviderHTTP(t)
	c := kit.Client(batcave, integrations.Limits{RPS: 1, Burst: 1, MaxRetries: -1})
	get := func() (int, string) {
		req, _ := http.NewRequest(http.MethodGet, kit.URL()+"/x", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(); code != 200 || body != "{}" {
		t.Errorf("default handler: %d %q", code, body)
	}
	kit.Handle(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	start := kit.Clock.Now()
	if code, _ := get(); code != http.StatusTeapot {
		t.Errorf("Handle: %d", code)
	}
	if kit.Hits() != 2 {
		t.Errorf("Hits = %d", kit.Hits())
	}
	// The second request waited for the one-token bucket on the FAKE clock.
	if s := kit.Clock.Sleeps(); len(s) == 0 || !kit.Clock.Now().After(start) {
		t.Errorf("the bucket wait was not on the fake clock: sleeps=%v", s)
	}
	if kit.Host() == "" || kit.Registry == nil {
		t.Error("kit")
	}
}
