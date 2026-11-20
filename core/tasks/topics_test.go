// file: core/tasks/topics_test.go
// version: 0.1.0
// guid: 7d1a9e52-4c38-4b6f-8e07-a2f35b9c1d46
// last-edited: 2026-11-20

package tasks_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// taskSpecs are the three catalog entries under test, by the spec's names
// (05-events §5E.3.1 "Core topics"; 05-realtime §4.7).
func taskSpecs() map[string]events.TopicSpec {
	return map[string]events.TopicSpec{
		"core.task.run-changed.v1":  tasks.RunChangedTopicSpec(),
		"core.task.log-appended.v1": tasks.LogAppendedTopicSpec(),
		"core.task.finished.v1":     tasks.FinishedTopicSpec(),
	}
}

// TestTaskTopics_NamesAndShape pins each entry to the spec's table row: the
// name, AnyAuthenticated, bus only (no Durable), and which of Realtime and
// Notify it carries.
func TestTaskTopics_NamesAndShape(t *testing.T) {
	if tasks.RunChangedTopicName != "core.task.run-changed.v1" ||
		tasks.LogAppendedTopicName != "core.task.log-appended.v1" ||
		tasks.FinishedTopicName != "core.task.finished.v1" {
		t.Fatal("a task topic name drifted from 05-events §5E.3.1")
	}
	for name, s := range taskSpecs() {
		if s.Name != name {
			t.Errorf("Name = %q, want %q", s.Name, name)
		}
		if s.Permission.Kind() != rbac.KindAuthenticated {
			t.Errorf("%s: Permission kind = %v, want AnyAuthenticated", name, s.Permission.Kind())
		}
		if s.Durable != nil {
			t.Errorf("%s: Durable set, the task topics are bus only", name)
		}
		if s.Redact == nil {
			t.Errorf("%s: no Redact", name)
		}
		if len(s.Schema) == 0 {
			t.Errorf("%s: empty Schema", name)
		}
	}
	rc, la, fin := tasks.RunChangedTopicSpec(), tasks.LogAppendedTopicSpec(), tasks.FinishedTopicSpec()
	if rc.Realtime == nil || rc.Realtime.Coalesce <= 0 || rc.Notify != nil {
		t.Errorf("run-changed: Realtime = %+v, Notify = %+v, want Realtime{Coalesce} and no Notify", rc.Realtime, rc.Notify)
	}
	if la.Realtime == nil || la.Realtime.Coalesce != 0 || la.Notify != nil {
		t.Errorf("log-appended: Realtime = %+v, Notify = %+v, want Realtime with every line and no Notify", la.Realtime, la.Notify)
	}
	if fin.Realtime != nil || fin.Notify == nil || fin.Notify.Title == "" {
		t.Errorf("finished: Realtime = %+v, Notify = %+v, want Notify and no Realtime", fin.Realtime, fin.Notify)
	}
}

// TestTaskTopics_VisibleFailsClosed: until the wiring installs the definition's
// View rule, a plain signed-in user, even one holding core:tasks.view and
// core:users.view, and a caller with no checker, are shown no envelope on any
// of the three topics; only a holder of core:system.manage is.
func TestTaskTopics_VisibleFailsClosed(t *testing.T) {
	plain := checker{[]rbac.Permission{"core:self", "core:tasks.view", "core:users.view"}}
	manager := checker{[]rbac.Permission{"core:system.manage"}}
	for name, s := range taskSpecs() {
		if s.Visible == nil {
			t.Errorf("%s: Visible is nil, which would show it to every holder of its Permission", name)
			continue
		}
		env := events.RawEnvelope{Topic: name, Key: "run-nightwing-1", Payload: json.RawMessage(`{}`)}
		if s.Visible(t.Context(), plain, env) {
			t.Errorf("%s: shown to a plain signed-in user", name)
		}
		if s.Visible(t.Context(), nil, env) {
			t.Errorf("%s: shown to a nil checker", name)
		}
		if !s.Visible(t.Context(), manager, env) {
			t.Errorf("%s: hidden from a holder of core:system.manage", name)
		}
	}
}

// TestTaskTopics_SchemaCarriesThePayload: each Schema is the payload type's
// (a RunView for run-changed and finished, a LogAppended for log-appended).
func TestTaskTopics_SchemaCarriesThePayload(t *testing.T) {
	for name, field := range map[string]string{
		"core.task.run-changed.v1":  "Current",
		"core.task.finished.v1":     "Reason",
		"core.task.log-appended.v1": "message",
	} {
		var sc struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(taskSpecs()[name].Schema, &sc); err != nil {
			t.Fatalf("%s: Schema is not JSON: %v", name, err)
		}
		if _, ok := sc.Properties[field]; !ok {
			t.Errorf("%s: Schema has no %q property: %s", name, field, taskSpecs()[name].Schema)
		}
	}
}

func newTopicBus(t *testing.T) *events.Bus {
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
	bus, err := events.NewBus(events.BusOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Metrics: sc})
	if err != nil {
		t.Fatal(err)
	}
	return bus
}

// TestTaskTopics_DeclareAndDeliver: the bus accepts the three entries as core
// topics, a keyed publish of each typed payload reaches a raw subscriber under
// the run's key, and the delivered JSON decodes back to the published value.
func TestTaskTopics_DeclareAndDeliver(t *testing.T) {
	bus := newTopicBus(t)
	if err := bus.Declare("core", tasks.RunChangedTopicSpec(), tasks.LogAppendedTopicSpec(), tasks.FinishedTopicSpec()); err != nil {
		t.Fatalf("Declare: %v", err)
	}
	got := make(chan events.RawEnvelope, 8)
	for _, name := range []string{tasks.RunChangedTopicName, tasks.LogAppendedTopicName, tasks.FinishedTopicName} {
		if err := bus.Subscribe("core", events.OnRaw(name, "core.topics-test."+name,
			func(_ context.Context, e events.RawEnvelope) { got <- e })); err != nil {
			t.Fatalf("Subscribe %s: %v", name, err)
		}
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })

	view := tasks.RunView{ID: "run-nightwing-1", State: tasks.StateSucceeded, Done: 3, Total: 3, Current: "Dick Grayson"}
	line := tasks.NewLogAppended(tasks.LogLine{At: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Level: "info", Message: "Bat-Signal lit", Fields: map[string]string{"by": "Gordon"}})
	scope := bus.Scope("core")
	events.Publish(scope, context.Background(), tasks.RunChangedTopic(), string(view.ID), view)
	events.Publish(scope, context.Background(), tasks.LogAppendedTopic(), string(view.ID), line)
	events.Publish(scope, context.Background(), tasks.FinishedTopic(), string(view.ID), view)

	seen := map[string]events.RawEnvelope{}
	for len(seen) < 3 {
		select {
		case e := <-got:
			seen[e.Topic] = e
		case <-time.After(5 * time.Second):
			t.Fatalf("got %d of 3 envelopes", len(seen))
		}
	}
	for name, e := range seen {
		if e.Key != string(view.ID) {
			t.Errorf("%s: Key = %q, want the run ID", name, e.Key)
		}
	}
	for _, name := range []string{tasks.RunChangedTopicName, tasks.FinishedTopicName} {
		var v tasks.RunView
		if err := json.Unmarshal(seen[name].Payload, &v); err != nil || v != view {
			t.Errorf("%s: payload = %s (%v), want %+v", name, seen[name].Payload, err, view)
		}
	}
	var l tasks.LogAppended
	if err := json.Unmarshal(seen[tasks.LogAppendedTopicName].Payload, &l); err != nil || l.Message != line.Message || l.Fields["by"] != "Gordon" || !l.At.Equal(line.At) {
		t.Errorf("log-appended payload = %s (%v)", seen[tasks.LogAppendedTopicName].Payload, err)
	}
}

// TestTaskTopics_LogLineWithoutFieldsDelivered: the common line has no Fields,
// and it must pass the topic's schema (a LogLine would marshal "Fields":null
// and the bus would drop it). The envelope reaches a raw subscriber, and the
// payload carries no "fields" key.
func TestTaskTopics_LogLineWithoutFieldsDelivered(t *testing.T) {
	bus := newTopicBus(t)
	if err := bus.Declare("core", tasks.LogAppendedTopicSpec()); err != nil {
		t.Fatal(err)
	}
	got := make(chan events.RawEnvelope, 1)
	if err := bus.Subscribe("core", events.OnRaw(tasks.LogAppendedTopicName, "core.no-fields",
		func(_ context.Context, e events.RawEnvelope) { got <- e })); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	events.Publish(bus.Scope("core"), context.Background(), tasks.LogAppendedTopic(), "run-robin-1",
		tasks.NewLogAppended(tasks.LogLine{At: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Level: "info", Message: "Bat-Signal lit"}))
	select {
	case e := <-got:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(e.Payload, &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["fields"]; ok {
			t.Errorf("payload has a fields key: %s", e.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a line with no Fields was dropped")
	}
}

// TestTaskTopics_WrongPayloadTypeRefused: a typed publish of a payload that is
// not the topic's type is counted and dropped, so a subscriber sees nothing.
func TestTaskTopics_WrongPayloadTypeRefused(t *testing.T) {
	bus := newTopicBus(t)
	if err := bus.Declare("core", tasks.LogAppendedTopicSpec()); err != nil {
		t.Fatal(err)
	}
	got := make(chan events.RawEnvelope, 2)
	if err := bus.Subscribe("core", events.OnRaw(tasks.LogAppendedTopicName, "core.wrong-type",
		func(_ context.Context, e events.RawEnvelope) { got <- e })); err != nil {
		t.Fatal(err)
	}
	bus.Freeze()
	if err := bus.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bus.Close(context.Background()) })
	// A RunView on the log topic: same name, wrong payload type.
	events.Publish(bus.Scope("core"), context.Background(),
		events.NewTopic[tasks.RunView](tasks.LogAppendedTopicName), "run-nightwing-1", tasks.RunView{ID: "run-nightwing-1"})
	// A correct publish after it proves the subscriber is live and the bad one never arrived first.
	events.Publish(bus.Scope("core"), context.Background(), tasks.LogAppendedTopic(), "run-nightwing-1", tasks.LogAppended{Message: "ok"})
	select {
	case e := <-got:
		var l tasks.LogAppended
		if err := json.Unmarshal(e.Payload, &l); err != nil || l.Message != "ok" {
			t.Fatalf("first delivery = %s, want the well-typed line", e.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing delivered")
	}
	select {
	case e := <-got:
		t.Fatalf("the wrong-typed publish was delivered: %s", e.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRedactLogAppended: core:users.view keeps the line whole; without it (or with
// no checker) Message and every Fields value are blanked and the rest stays.
func TestRedactLogAppended(t *testing.T) {
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	l := tasks.LogAppended{At: at, Level: "warn", Message: "Selina Kyle signed in", Fields: map[string]string{"user": "selina.kyle@example.com"}}

	if got := tasks.RedactLogAppended(checker{[]rbac.Permission{"core:users.view"}}, l); got.Message != l.Message || got.Fields["user"] != l.Fields["user"] {
		t.Errorf("holder of core:users.view: %+v", got)
	}
	for name, who := range map[string]rbac.Checker{"nil": nil, "tasks.view only": checker{[]rbac.Permission{"core:tasks.view"}}} {
		got := tasks.RedactLogAppended(who, l)
		if got.Message != "" || got.Fields["user"] != "" {
			t.Errorf("%s: free text kept: %+v", name, got)
		}
		if _, ok := got.Fields["user"]; !ok || got.Level != "warn" || !got.At.Equal(at) {
			t.Errorf("%s: structure lost: %+v", name, got)
		}
	}
	if l.Message == "" || l.Fields["user"] == "" {
		t.Error("RedactLogAppended mutated its argument's map")
	}
	if got := tasks.RedactLogAppended(nil, tasks.LogAppended{Message: "x"}); got.Fields != nil {
		t.Errorf("nil Fields became %v", got.Fields)
	}
}

// TestLogAppendedRedactor: the RedactFunc form decodes, redacts and re-encodes,
// and blanks an undecodable payload to "{}".
func TestLogAppendedRedactor(t *testing.T) {
	r := tasks.LogAppendedRedactor()
	payload, err := json.Marshal(tasks.LogAppended{Level: "info", Message: "Harvey Dent paged"})
	if err != nil {
		t.Fatal(err)
	}
	var got tasks.LogAppended
	if err := json.Unmarshal(r(checker{[]rbac.Permission{"core:tasks.view"}}, payload), &got); err != nil || got.Message != "" || got.Level != "info" {
		t.Errorf("without core:users.view: %+v (%v)", got, err)
	}
	if err := json.Unmarshal(r(checker{[]rbac.Permission{"core:users.view"}}, payload), &got); err != nil || got.Message != "Harvey Dent paged" {
		t.Errorf("with core:users.view: %+v (%v)", got, err)
	}
	if out := r(checker{[]rbac.Permission{"core:users.view"}}, json.RawMessage(`[1,2]`)); string(out) != "{}" {
		t.Errorf("undecodable payload = %s, want {}", out)
	}
}

// TestRunChangedRedact_IsTheRunViewRedactor: the redactor on run-changed and
// finished blanks Current for a subscriber without core:users.view, so the
// stream and GET /runs agree (COR2-2).
func TestRunChangedRedact_IsTheRunViewRedactor(t *testing.T) {
	payload, err := json.Marshal(tasks.RunView{ID: "r1", Current: "Oswald Cobblepot"})
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]events.TopicSpec{"run-changed": tasks.RunChangedTopicSpec(), "finished": tasks.FinishedTopicSpec()} {
		var v tasks.RunView
		if err := json.Unmarshal(s.Redact(checker{nil}, payload), &v); err != nil || v.Current != "" || v.ID != "r1" {
			t.Errorf("%s: %+v (%v)", name, v, err)
		}
	}
}
