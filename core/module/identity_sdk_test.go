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

	"github.com/falkcorp/mediacore/events"
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
