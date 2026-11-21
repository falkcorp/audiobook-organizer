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
	"reflect"
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
	if rc.Realtime == nil || rc.Realtime.Coalesce != 250*time.Millisecond || rc.Notify != nil {
		t.Errorf("run-changed: Realtime = %+v, Notify = %+v, want Realtime{Coalesce: 250ms} and no Notify", rc.Realtime, rc.Notify)
	}
	if la.Realtime == nil || la.Realtime.Coalesce != 0 || la.Notify != nil {
		t.Errorf("log-appended: Realtime = %+v, Notify = %+v, want Realtime with every line and no Notify", la.Realtime, la.Notify)
	}
	if fin.Realtime != nil || fin.Notify == nil || fin.Notify.Title != "Task finished" || fin.Notify.DefaultSeverity != "info" {
		t.Errorf("finished: Realtime = %+v, Notify = %+v, want Notify{Task finished, info} and no Realtime", fin.Realtime, fin.Notify)
	}
}

// TestTaskTopics_VisibleFailsClosed: until the wiring installs the definition's
// View rule, a plain signed-in user (even one holding core:tasks.view and
// core:users.view), a holder of core:system.manage alone (assignable to a
// custom role) and a caller with no checker are shown no envelope on any of the
// three topics; only a holder of core:admin is.
func TestTaskTopics_VisibleFailsClosed(t *testing.T) {
	plain := checker{[]rbac.Permission{"core:self", "core:tasks.view", "core:users.view"}}
	sysManager := checker{[]rbac.Permission{"core:system.manage"}}
	admin := checker{[]rbac.Permission{rbac.CoreAdmin}}
	for name, s := range taskSpecs() {
		if s.Visible == nil {
			t.Errorf("%s: Visible is nil, which would show it to every holder of its Permission", name)
			continue
		}
		env := events.RawEnvelope{Topic: name, Key: "run-nightwing-1", Payload: json.RawMessage(`{}`)}
		if s.Visible(t.Context(), plain, env) {
			t.Errorf("%s: shown to a plain signed-in user", name)
		}
		if s.Visible(t.Context(), sysManager, env) {
			t.Errorf("%s: shown to a holder of core:system.manage alone", name)
		}
		if s.Visible(t.Context(), nil, env) {
			t.Errorf("%s: shown to a nil checker", name)
		}
		if !s.Visible(t.Context(), admin, env) {
			t.Errorf("%s: hidden from a holder of core:admin", name)
		}
	}
}

// TestTaskTopics_SchemaCarriesThePayload: each Schema is the payload type's
// (a RunPayload for run-changed and finished, a LogAppended for log-appended).
func TestTaskTopics_SchemaCarriesThePayload(t *testing.T) {
	for name, field := range map[string]string{
		"core.task.run-changed.v1":  "current",
		"core.task.finished.v1":     "reason",
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

	view := tasks.NewRunPayload(tasks.RunView{ID: "run-nightwing-1", Kind: "batcave.sweep", Mode: tasks.ModeLive, State: tasks.StateSucceeded, Done: 3, Total: 3, Current: "Dick Grayson"})
	line := tasks.NewLogAppended(tasks.LogLine{At: time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Level: "info", Message: "Bat-Signal lit", Fields: map[string]string{"by": "Gordon"}})
	scope := bus.Scope("core")
	events.Publish(scope, context.Background(), tasks.RunChangedTopic(), view.ID, view)
	events.Publish(scope, context.Background(), tasks.LogAppendedTopic(), view.ID, line)
	events.Publish(scope, context.Background(), tasks.FinishedTopic(), view.ID, view)

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
		if e.Key != view.ID {
			t.Errorf("%s: Key = %q, want the run ID", name, e.Key)
		}
	}
	for _, name := range []string{tasks.RunChangedTopicName, tasks.FinishedTopicName} {
		var v tasks.RunPayload
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
	// A RunPayload on the log topic: same name, wrong payload type.
	events.Publish(bus.Scope("core"), context.Background(),
		events.NewTopic[tasks.RunPayload](tasks.LogAppendedTopicName), "run-nightwing-1", tasks.RunPayload{ID: "run-nightwing-1"})
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

// TestRunViewRedactor_NeverCarriesTheBlocker: the realtime stream has a
// subscriber's checker and a payload, and no way to ask whether that subscriber
// may view the blocking run's kind, so a run-changed or finished payload never
// carries blocked_by, whoever subscribes (even a holder of every scope). What
// describes the run itself, the deferral reason and when it began, stays. GET
// /runs gets the blocker only from Client.Get/Runs, which decide it per caller.
func TestRunViewRedactor_NeverCarriesTheBlocker(t *testing.T) {
	since := time.Date(2026, 11, 20, 9, 2, 0, 0, time.UTC)
	payload, err := json.Marshal(tasks.NewRunPayload(tasks.RunView{
		ID: "run-robin-1", State: tasks.StateQueued,
		Deferred: "writeset", BlockedBy: "run-robin-0", DeferredSince: since,
	}))
	if err != nil {
		t.Fatal(err)
	}
	redact := tasks.RunViewRedactor()
	for name, who := range map[string]rbac.Checker{
		"nil":             nil,
		"tasks.view only": checker{[]rbac.Permission{"core:tasks.view"}},
		"every scope":     checker{[]rbac.Permission{"core:tasks.view", "core:tasks.manage", "core:users.view", "core:system.manage"}},
	} {
		var got tasks.RunPayload
		if err := json.Unmarshal(redact(who, payload), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.BlockedBy != "" {
			t.Errorf("%s: the stream carries blocked_by %q", name, got.BlockedBy)
		}
		if got.Deferred != "writeset" || !got.DeferredSince.Equal(since) {
			t.Errorf("%s: the run's own deferral was lost: %+v", name, got)
		}
	}
	if got := tasks.RedactRunPayload(nil, tasks.NewRunPayload(tasks.RunView{BlockedBy: "run-robin-0"})); got.BlockedBy != "" {
		t.Errorf("RedactRunPayload kept blocked_by %q", got.BlockedBy)
	}
}

// TestTaskTopics_SpecRedactIsARedactor: each entry's own Redact, the value the
// hub applies, blanks the user-identifying text for a subscriber without
// core:users.view and keeps it for a holder, on all three topics (a pass-through
// Redact fails the first half, an always-blank one the second).
func TestTaskTopics_SpecRedactIsARedactor(t *testing.T) {
	run, err := json.Marshal(tasks.NewRunPayload(tasks.RunView{ID: "r1", State: tasks.StateRunning, Current: "Oswald Cobblepot"}))
	if err != nil {
		t.Fatal(err)
	}
	log, err := json.Marshal(tasks.NewLogAppended(tasks.LogLine{Level: "info", Message: "Oswald Cobblepot paged", Fields: map[string]string{"who": "oswald"}}))
	if err != nil {
		t.Fatal(err)
	}
	plain := checker{[]rbac.Permission{"core:tasks.view"}}
	holder := checker{[]rbac.Permission{"core:users.view"}}
	for name, c := range map[string]struct {
		payload json.RawMessage
		text    func(json.RawMessage) (string, error)
	}{
		"core.task.run-changed.v1": {run, func(b json.RawMessage) (string, error) {
			var v tasks.RunPayload
			err := json.Unmarshal(b, &v)
			return v.Current, err
		}},
		"core.task.finished.v1": {run, func(b json.RawMessage) (string, error) {
			var v tasks.RunPayload
			err := json.Unmarshal(b, &v)
			return v.Current, err
		}},
		"core.task.log-appended.v1": {log, func(b json.RawMessage) (string, error) {
			var v tasks.LogAppended
			err := json.Unmarshal(b, &v)
			return v.Message + "|" + v.Fields["who"], err
		}},
	} {
		redact := taskSpecs()[name].Redact
		if got, err := c.text(redact(plain, c.payload)); err != nil || (got != "" && got != "|") {
			t.Errorf("%s: without core:users.view text = %q (%v)", name, got, err)
		}
		if got, err := c.text(redact(nil, c.payload)); err != nil || (got != "" && got != "|") {
			t.Errorf("%s: nil checker text = %q (%v)", name, got, err)
		}
		if got, err := c.text(redact(holder, c.payload)); err != nil || got == "" || got == "|" {
			t.Errorf("%s: a holder of core:users.view lost the text: %q (%v)", name, got, err)
		}
	}
	// The run id and state survive the blanking.
	var v tasks.RunPayload
	if err := json.Unmarshal(taskSpecs()["core.task.run-changed.v1"].Redact(plain, run), &v); err != nil || v.ID != "r1" || v.State != "running" {
		t.Errorf("redacted run = %+v (%v)", v, err)
	}
}

// TestRunPayload_WireKeys pins the run payload's keys, as literals, to what the
// SPA's coreEvents client reads (id, kind, state, done, total, current; web/src/
// services/coreEvents/types.ts) and REST serves, with the mode as a word: the
// exact key set of a payload with every field set, the actor's own keys, and
// the times' spelling. A renamed tag fails here rather than renaming the wire
// on both REST and the stream at once.
func TestRunPayload_WireKeys(t *testing.T) {
	created := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	started, finished := created.Add(time.Minute), created.Add(time.Hour)
	b, err := json.Marshal(tasks.NewRunPayload(tasks.RunView{
		ID: "run-robin-1", Kind: "batcave.sweep", Mode: tasks.ModePreview, State: tasks.StateSucceeded, Held: true,
		Done: 2, Total: 5, Current: "the cowl", Reason: "r", Parent: "p", PlanRef: "pl", Trigger: tasks.TriggerManual,
		Actor:   tasks.Actor{UserID: "bruce", AuthMethod: "session", System: "core.scheduler"},
		Created: created, Started: started, Finished: finished,
		Deferred: "writeset", BlockedBy: "run-robin-0", DeferredSince: created.Add(2 * time.Minute),
	}))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"id": "run-robin-1", "kind": "batcave.sweep", "mode": "preview", "state": "succeeded", "held": true,
		"done": 2.0, "total": 5.0, "current": "the cowl", "reason": "r", "parent": "p", "plan_ref": "pl", "trigger": "manual",
		"created": "2026-11-20T09:00:00Z", "started": "2026-11-20T09:01:00Z", "finished": "2026-11-20T10:00:00Z",
		"deferred": "writeset", "blocked_by": "run-robin-0", "deferred_since": "2026-11-20T09:02:00Z",
		"actor": map[string]any{"user_id": "bruce", "auth_method": "session", "system": "core.scheduler"}}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("wire form =\n%v\nwant exactly\n%v", m, want)
	}
	// Zero times and empty optional fields stay off the wire.
	b, err = json.Marshal(tasks.NewRunPayload(tasks.RunView{ID: "run-robin-2", Created: created}))
	if err != nil {
		t.Fatal(err)
	}
	m = nil
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"started", "finished", "reason", "parent", "plan_ref", "current", "deferred", "blocked_by", "deferred_since"} {
		if _, ok := m[k]; ok {
			t.Errorf("an empty %s is on the wire: %s", k, b)
		}
	}
	if a, ok := m["actor"].(map[string]any); !ok || len(a) != 0 {
		t.Errorf("an empty actor = %v, want {}: %s", m["actor"], b)
	}
}
