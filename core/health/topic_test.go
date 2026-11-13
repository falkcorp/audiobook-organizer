// file: core/health/topic_test.go
// version: 1.0.0
// guid: 5c1d9e3a-7f28-4b64-90a1-e2b8d4f6a370
// last-edited: 2026-11-13

package health

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
)

// TestStatusTopic_SpecAndDelivery: the catalog entry matches 05-health
// §5H.3.3 (name, bus only, one-second realtime coalescing, core:system.status),
// the bus accepts it, and a readiness change reaches a subscriber as exactly
// the /readyz body.
func TestStatusTopic_SpecAndDelivery(t *testing.T) {
	spec := StatusTopicSpec()
	if spec.Name != "core.system.status-changed.v1" {
		t.Errorf("Name = %q", spec.Name)
	}
	if spec.Durable != nil {
		t.Error("the status topic must be bus only (no Durable)")
	}
	if spec.Realtime == nil || spec.Realtime.Coalesce != time.Second {
		t.Errorf("Realtime = %+v, want Coalesce 1s", spec.Realtime)
	}
	if got := spec.Permission.Permissions(); !slices.Equal(got, []rbac.Permission{"core:system.status"}) {
		t.Errorf("Permission = %v, want core:system.status alone", got)
	}

	p, err := metrics.New(context.Background(), metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("events", "topic", "subscriber", "reason")
	if err != nil {
		t.Fatal(err)
	}
	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Metrics: sc})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Declare("core", spec); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	got := make(chan string, 8)
	if err := bus.Subscribe("core", events.OnRaw(StatusTopicName, "core.status-test",
		func(_ context.Context, e events.RawEnvelope) { got <- string(e.Payload) })); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })

	r := NewRegistry()
	storage := mustRegister(t, r, Check{ID: "core.storage", Critical: true, Readiness: true})
	storage.Report(Result{Status: StatusUp})
	r.SetStatusPublisher(PublishStatus(bus.Scope("core")))

	want := func(wantPayload string) {
		t.Helper()
		select {
		case p := <-got:
			if p != wantPayload {
				t.Fatalf("payload = %s, want %s", p, wantPayload)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("no delivery of %s", wantPayload)
		}
	}
	want(`{"ready":true,"not_ready":[]}`) // the current state is delivered at once

	storage.Report(Result{Status: StatusDown, Reason: "check_timeout", Detail: "dial tcp batcave: i/o timeout"})
	want(`{"ready":false,"not_ready":["core.storage:check_timeout"]}`)

	// Whatever else the registry holds, the envelope carries only the two keys.
	var keys map[string]json.RawMessage
	b, _ := json.Marshal(r.Status())
	if err := json.Unmarshal(b, &keys); err != nil || len(keys) != 2 {
		t.Errorf("payload keys = %v (%v), want exactly ready and not_ready", keys, err)
	}
}
