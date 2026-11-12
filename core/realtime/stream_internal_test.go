// file: core/realtime/stream_internal_test.go
// version: 1.0.0
// guid: 8e5b2a63-4d17-4c90-b3f8-7a6c1d9e0245
// last-edited: 2026-11-12

package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/internal/sse"
	"github.com/falkcorp/mediacore/rbac"
)

// holder is a checker that holds exactly the permissions in it.
type holder map[rbac.Permission]bool

func (h holder) Has(s rbac.Permission) bool { return h[s] }
func (h holder) Confined() bool             { return false }
func (h holder) Concrete() []rbac.Permission {
	return nil
}
func (h holder) Satisfies(r rbac.Requirement) bool {
	if r.Kind() != rbac.KindAllOf {
		return len(h) > 0
	}
	for _, s := range r.Permissions() {
		if !h[s] {
			return false
		}
	}
	return true
}

const batScope = rbac.Permission("batcave:view")

func internalStream(t *testing.T, allowed ...string) (*stream, *sse.Entry) {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatal(err)
	}
	topic := events.NewTopic[map[string]string]("batcave.signal.lit.v1")
	if err := bus.Declare("batcave", topic.Spec(events.TopicSpec{Permission: rbac.Need(batScope), Realtime: &events.RealtimeMeta{}})); err != nil {
		t.Fatal(err)
	}
	h, err := NewHub(Options{AllowNoControlTopics: true}, Deps{
		Auth: nilRevalidator{}, Catalog: bus.Catalog(),
		Refresh: func(_ context.Context, p *auth.Principal) (*auth.Principal, error) { return p, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Subscriptions(); err != nil { // the hub's own topic table is filled
		t.Fatal(err)
	}
	spec := events.TopicSpec{Name: "batcave.signal.lit.v1", Permission: rbac.Need(batScope)}
	st := &stream{
		h: h, p: &auth.Principal{User: &auth.User{ID: "u-bruce"}, Method: auth.MethodSession, Perms: holder{batScope: true}},
		allowed: map[string]events.TopicSpec{}, explicit: map[string]struct{}{},
	}
	for _, name := range allowed {
		st.allowed[name] = spec
	}
	raw := events.RawEnvelope{Topic: spec.Name, Time: time.Unix(0, 0).UTC(), Payload: json.RawMessage(`{"lamp":"on"}`)}
	head, err := envelopeHead(raw)
	if err != nil {
		t.Fatal(err)
	}
	return st, &sse.Entry{Topic: spec.Name, Item: &item{raw: raw, head: head}}
}

type nilRevalidator struct{}

func (nilRevalidator) Revalidate(context.Context, *auth.Principal) error { return nil }

// TestDeliver_ATopicOutsideTheSetIsWithheldWhateverTheRights: the stream's own
// topic set is the first test of an entry, ahead of the principal's checker. A
// topic dropped by a narrowing stays withheld for entries already queued, even
// if the right is back by the time they are judged (the set only shrinks).
func TestDeliver_ATopicOutsideTheSetIsWithheldWhateverTheRights(t *testing.T) {
	st, e := internalStream(t) // not in the set; the checker holds the permission
	if data, ok := st.Deliver(context.Background(), e); ok {
		t.Fatalf("delivered a topic outside the stream's set: %s", data)
	}
	st, e = internalStream(t, "batcave.signal.lit.v1")
	if data, ok := st.Deliver(context.Background(), e); !ok || len(data) == 0 {
		t.Errorf("a topic in the set with the right held was withheld")
	}
	st.p.Perms = holder{} // the right is gone: withheld
	if _, ok := st.Deliver(context.Background(), e); ok {
		t.Error("delivered after the right was lost")
	}
}
