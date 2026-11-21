// file: core/kernel/identity_events_test.go
// version: 0.1.0
// guid: 0b9d4e27-58a3-4c61-9f0e-d2a7c816b5f3
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only: Oracle's session in the Batcave.

// TestCoreTopics_CarryTheIdentityControlTopics: core's catalog lists both
// identity control topics exactly once, each observable only by
// core:system.manage and with no Realtime, Notify or Durable metadata (control
// topics are read by core's raw subscriptions, never streamed or notified).
func TestCoreTopics_CarryTheIdentityControlTopics(t *testing.T) {
	var names []string
	byName := map[string]events.TopicSpec{}
	for _, s := range coreTopics() {
		names = append(names, s.Name)
		byName[s.Name] = s
	}
	for _, want := range []string{"core.rbac.changed.v1", "core.principal.invalidated.v1"} {
		if n := countOf(names, want); n != 1 {
			t.Errorf("coreTopics has %s %d times, want 1 (%v)", want, n, names)
			continue
		}
		s := byName[want]
		if s.Realtime != nil || s.Notify != nil || s.Durable != nil {
			t.Errorf("%s carries Realtime/Notify/Durable metadata, want none", want)
		}
		if w := rbac.Need("core:system.manage"); s.Permission.String() != w.String() || s.Permission.Kind() != w.Kind() {
			t.Errorf("%s Permission = %v, want core:system.manage", want, s.Permission)
		}
	}
}

// identityFixture is a bus that declares coreTopics (the real control-topic
// specs, not stand-ins) plus the Batcave's realtime topic, with a hub built by
// newKernelRealtime, which never sets AllowNoControlTopics.
func identityFixture(t *testing.T) *rtFixture {
	t.Helper()
	ke := newTestKernelEvents(t, 0, 0)
	if probs := ke.declareCore(coreTopics()); len(probs) != 0 {
		t.Fatalf("declareCore(coreTopics()): %v", probs)
	}
	if probs := ke.declare("batcave", []events.TopicSpec{rtSighting.Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated, Realtime: &events.RealtimeMeta{},
	})}); len(probs) != 0 {
		t.Fatalf("declare: %v", probs)
	}
	kr, err := newKernelRealtime(ke.catalog(), settingsOf(realtimeSettings{}).get, &fakeAuth{},
		rtProvider(t), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return &rtFixture{ke: ke, kr: kr}
}

// TestIdentityTopics_RealtimeEndsAStreamOnAPublishedInvalidation pins the
// kernel's control topics to what realtime reads: with coreTopics declared
// the hub's subscriptions succeed (it refuses a catalog missing either
// topic), and an invalidation published through publishPrincipalInvalidated
// for the stream's user and credential ends the stream with
// closed{credential_revoked}. A payload realtime could not decode, or a topic
// name that drifted, would leave the stream open until the deadline.
func TestIdentityTopics_RealtimeEndsAStreamOnAPublishedInvalidation(t *testing.T) {
	f := identityFixture(t)
	srv := f.serve(t)
	lines := f.stream(t, srv)
	publishPrincipalInvalidated(f.ke.bus)(context.Background(), "u-oracle", "s-cave", "session.revoked")
	waitLine(t, lines, `"reason":"credential_revoked"`)
}

// rawCapture subscribes core to topic and returns the payloads it receives.
func rawCapture(t *testing.T, ke *kernelEvents, topic, name string) <-chan string {
	t.Helper()
	got := make(chan string, 4)
	if probs := ke.subscribe("", []events.Subscription{events.OnRaw(topic, name,
		func(_ context.Context, e events.RawEnvelope) { got <- string(e.Payload) })}); len(probs) != 0 {
		t.Fatalf("subscribe: %v", probs)
	}
	return got
}

func openBus(t *testing.T, ke *kernelEvents) {
	t.Helper()
	ke.freeze()
	if err := ke.open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ke.close(context.Background()) })
}

func nextPayload(t *testing.T, ch <-chan string) map[string]any {
	t.Helper()
	select {
	case p := <-ch:
		var m map[string]any
		if err := json.Unmarshal([]byte(p), &m); err != nil {
			t.Fatalf("payload %q: %v", p, err)
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no envelope delivered")
	}
	return nil
}

// TestIdentityPublishers_WireForm: core.rbac.changed.v1 carries exactly
// {"generation"}; core.principal.invalidated.v1 carries user_id, reason and
// credential_id, the last omitted when the revocation covers every credential
// of the user (realtime then ends all of the user's streams).
func TestIdentityPublishers_WireForm(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	if probs := ke.declareCore(coreTopics()); len(probs) != 0 {
		t.Fatalf("declareCore: %v", probs)
	}
	gen := rawCapture(t, ke, rbacChangedTopicName, "core.test.rbac-changed")
	inv := rawCapture(t, ke, principalInvalidatedTopicName, "core.test.principal-invalidated")
	openBus(t, ke)

	publishRBACChanged(ke.bus)(7)
	if m := nextPayload(t, gen); len(m) != 1 || m["generation"] != float64(7) {
		t.Errorf("rbac.changed payload = %v, want {generation: 7}", m)
	}

	pub := publishPrincipalInvalidated(ke.bus)
	pub(context.Background(), "u-oracle", "k-signal", "apikey.revoked")
	if m := nextPayload(t, inv); len(m) != 3 || m["user_id"] != "u-oracle" || m["credential_id"] != "k-signal" || m["reason"] != "apikey.revoked" {
		t.Errorf("invalidated payload = %v, want user_id, credential_id and reason", m)
	}
	pub(context.Background(), "u-oracle", "", "user.disabled")
	m := nextPayload(t, inv)
	if _, has := m["credential_id"]; has || m["user_id"] != "u-oracle" || m["reason"] != "user.disabled" {
		t.Errorf("user-wide invalidated payload = %v, want user_id and reason with no credential_id", m)
	}
}

// TestPublishPrincipalInvalidated_EmptyUserPublishesNothing: an invalidation
// that names no user is not published (realtime could match no stream with
// it); the next one with a user is.
func TestPublishPrincipalInvalidated_EmptyUserPublishesNothing(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	if probs := ke.declareCore(coreTopics()); len(probs) != 0 {
		t.Fatalf("declareCore: %v", probs)
	}
	inv := rawCapture(t, ke, principalInvalidatedTopicName, "core.test.principal-invalidated")
	openBus(t, ke)
	pub := publishPrincipalInvalidated(ke.bus)
	pub(context.Background(), "", "s-cave", "session.revoked")
	pub(context.Background(), "u-oracle", "s-cave", "session.revoked")
	if m := nextPayload(t, inv); m["user_id"] != "u-oracle" {
		t.Errorf("first delivered payload = %v, want the one naming u-oracle (the empty-user publish must not be sent)", m)
	}
}

// TestIdentityPublishers_BeforeFreezeReturn: a publish before the bus freezes
// (a revocation during boot, before P7) returns at once, with neither a panic
// nor a block; the bus refuses and counts it.
func TestIdentityPublishers_BeforeFreezeReturn(t *testing.T) {
	ke := newTestKernelEvents(t, 0, 0)
	if probs := ke.declareCore(coreTopics()); len(probs) != 0 {
		t.Fatalf("declareCore: %v", probs)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		publishRBACChanged(ke.bus)(1)
		publishPrincipalInvalidated(ke.bus)(context.Background(), "u-oracle", "s-cave", "session.revoked")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a publish before freeze blocked")
	}
}
