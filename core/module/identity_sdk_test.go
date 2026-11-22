// file: core/module/identity_sdk_test.go
// version: 0.1.0
// guid: c9375f4a-06c8-41fb-8397-f24c920dd508
// last-edited: 2026-11-21

package module

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
)

// signalLit is the payload of the fixture topic.
type signalLit struct {
	Reason string `json:"reason"`
}

func fixtureTopic(name string) events.TopicSpec {
	return events.NewTopic[signalLit](name).Spec(events.TopicSpec{Permission: rbac.Need("batcave:signal.view")})
}

// TestRegistrar_RecordsTopicsAndSubscriptions: Topics and Subscribe record in
// call order across calls, without checking anything (the bus does at P7), and
// Close hands back copies.
func TestRegistrar_RecordsTopicsAndSubscriptions(t *testing.T) {
	r := newTestRegistrar(t, false)
	lit := events.NewTopic[signalLit]("batcave.signal.lit.v1")
	r.Topics(fixtureTopic("batcave.signal.lit.v1"))
	r.Topics(fixtureTopic("batcave.signal.dimmed.v1"), fixtureTopic("arkham.cell.opened.v1"))
	r.Subscribe(events.On(lit, "watch", func(context.Context, events.Envelope[signalLit]) {}))
	r.Subscribe(
		events.OnRaw("arkham.cell.opened.v1", "alarm", func(context.Context, events.RawEnvelope) {}),
		events.OnRaw("core.rbac.changed.v1", "refresh", func(context.Context, events.RawEnvelope) {}),
	)
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v: recording must not refuse anything", err)
	}
	rec := r.Close()
	var names []string
	for _, ts := range rec.Topics {
		names = append(names, ts.Name)
	}
	if got, want := strings.Join(names, ","), "batcave.signal.lit.v1,batcave.signal.dimmed.v1,arkham.cell.opened.v1"; got != want {
		t.Fatalf("recorded topics = %s, want %s (call order, a foreign name kept for the bus to refuse)", got, want)
	}
	if len(rec.Subscriptions) != 3 {
		t.Fatalf("recorded %d subscriptions, want 3", len(rec.Subscriptions))
	}

	// The returned slices are copies: a caller that edits them changes nothing
	// the next Close returns.
	rec.Topics[0] = events.TopicSpec{Name: "joker.tampered.v1"}
	rec.Subscriptions[0] = events.Subscription{}
	again := r.Close()
	if again.Topics[0].Name != "batcave.signal.lit.v1" || len(again.Subscriptions) != 3 {
		t.Fatalf("second Close = %d topics (first %q), want the original registrations", len(again.Topics), again.Topics[0].Name)
	}
}

// TestRegistrar_TopicsAndSubscribeAfterClose: a late call records
// ProblemRegistrarClosed naming the call and takes no effect.
func TestRegistrar_TopicsAndSubscribeAfterClose(t *testing.T) {
	var late []Problem
	r := NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
		AfterClose: func(p Problem) { late = append(late, p) }})
	r.Close()
	r.Topics(fixtureTopic("batcave.signal.lit.v1"))
	r.Subscribe(events.OnRaw("arkham.cell.opened.v1", "alarm", func(context.Context, events.RawEnvelope) {}))
	if !errors.Is(r.Err(), ErrRegistrarClosed) {
		t.Fatalf("Err = %v, want ErrRegistrarClosed", r.Err())
	}
	if len(late) != 2 || !strings.Contains(late[0].Detail, "Topics") || !strings.Contains(late[1].Detail, "Subscribe") {
		t.Fatalf("AfterClose got %v, want one problem naming Topics and one naming Subscribe", late)
	}
	if rec := r.Close(); len(rec.Topics) != 0 || len(rec.Subscriptions) != 0 {
		t.Fatalf("late calls took effect: %+v", rec)
	}
}

// TestDeps_ZeroEventsScopeDropsPublishes backs Deps.Events' doc: before the
// kernel builds the bus a module's publish is dropped, never a panic.
func TestDeps_ZeroEventsScopeDropsPublishes(t *testing.T) {
	var d Deps
	events.Publish(d.Events, context.Background(), events.NewTopic[signalLit]("batcave.signal.lit.v1"), "", signalLit{Reason: "bat signal"})
}

// compatStub is the handle the kernel's Compat hook returns.
type compatStub struct{ auth.Compat }

// TestRegistrar_AuthMethodSetsAuthCompatOnDeps: with RegistrarConfig.Compat
// set, the first AuthMethod gives the registrar's Deps an AuthCompat built from
// that spec, and a second one does not replace it. Without the hook, or before
// any AuthMethod, AuthCompat is nil, and a refused spec (checked before the
// hook) gets none.
func TestRegistrar_AuthMethodSetsAuthCompatOnDeps(t *testing.T) {
	var specs []auth.MethodSpec
	handle := compatStub{}
	router := httpapi.NewRouter(httpapi.Options{}, httpapi.Deps{})
	build := func(compat func(auth.MethodSpec) auth.Compat) *Registrar {
		return NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
			HTTP: router.ModuleGroup("batcave"), Compat: compat})
	}
	hook := func(s auth.MethodSpec) auth.Compat { specs = append(specs, s); return handle }

	r := build(hook)
	if r.Deps().AuthCompat != nil {
		t.Fatal("AuthCompat is set before any AuthMethod")
	}
	r.AuthMethod(auth.MethodSpec{Method: "batcave.cowl", Interactive: true}) // refused: Interactive
	if r.Deps().AuthCompat != nil || len(specs) != 0 {
		t.Fatalf("a refused spec reached the hook: compat=%v specs=%v", r.Deps().AuthCompat, specs)
	}
	r.AuthMethod(auth.MethodSpec{Method: "batcave.signal"})
	if r.Deps().AuthCompat != handle {
		t.Fatalf("AuthCompat = %v, want the hook's handle", r.Deps().AuthCompat)
	}
	r.AuthMethod(auth.MethodSpec{Method: "batcave.beacon"})
	if len(specs) != 1 || specs[0].Method != "batcave.signal" {
		t.Errorf("hook calls = %v, want only the first method", specs)
	}

	bare := build(nil)
	bare.AuthMethod(auth.MethodSpec{Method: "batcave.signal"})
	if bare.Deps().AuthCompat != nil {
		t.Error("AuthCompat is set with no Compat hook")
	}
}

// TestRegistrar_RecordsOutboxHandlers: OutboxHandler records in call order
// across calls without checking anything (the outbox does at P7), Close hands
// back a copy, and a late call is a ProblemRegistrarClosed that takes no effect.
func TestRegistrar_RecordsOutboxHandlers(t *testing.T) {
	r := newTestRegistrar(t, false)
	h := func(name string) outbox.Handler {
		return outbox.ConsumeRaw("batcave.signal.lit.v1", name, func(context.Context, outbox.RawMessage) error { return nil })
	}
	r.OutboxHandler(h("batcave.watch"))
	r.OutboxHandler(h("batcave.alarm"), h("batcave.log"))
	if err := r.Err(); err != nil {
		t.Fatalf("Err = %v: recording must not refuse anything", err)
	}
	rec := r.Close()
	if len(rec.OutboxHandlers) != 3 {
		t.Fatalf("recorded %d outbox handlers, want 3", len(rec.OutboxHandlers))
	}
	rec.OutboxHandlers[0] = outbox.Handler{}
	if again := r.Close(); again.OutboxHandlers[0] == (outbox.Handler{}) {
		t.Fatal("Close returned the registrar's own slice: an edit by the caller changed the next Close")
	}

	var late []Problem
	c := NewRegistrar(RegistrarConfig{Context: t.Context(), Deps: Deps{Module: "batcave"},
		AfterClose: func(p Problem) { late = append(late, p) }})
	c.Close()
	c.OutboxHandler(h("batcave.watch"))
	if !errors.Is(c.Err(), ErrRegistrarClosed) || len(late) != 1 || !strings.Contains(late[0].Detail, "OutboxHandler") {
		t.Fatalf("late OutboxHandler: Err = %v, late = %v, want ErrRegistrarClosed naming OutboxHandler", c.Err(), late)
	}
	if rec := c.Close(); len(rec.OutboxHandlers) != 0 {
		t.Fatalf("a late call took effect: %+v", rec)
	}
}

// TestDeps_ZeroOutboxWriterStagesNothing backs Deps.Outbox's doc: before the
// kernel builds the outbox a module's Stage fails, never a panic.
func TestDeps_ZeroOutboxWriterStagesNothing(t *testing.T) {
	var d Deps
	if err := outbox.Stage(d.Outbox, nil, outbox.NewTopic[signalLit]("batcave.signal.lit.v1"), "k", signalLit{Reason: "bat signal"}); err == nil {
		t.Fatal("Stage through the zero Writer succeeded")
	}
}
