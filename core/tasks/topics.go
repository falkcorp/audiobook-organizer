// file: core/tasks/topics.go
// version: 0.1.0
// guid: 3b7e4c1a-92d5-4f08-a6b3-5e1d8c70f294
// last-edited: 2026-11-20

package tasks

import (
	"context"
	"encoding/json"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// The three core task topics (05-events §5E.3.1 "Core topics"; 05-realtime
// §4.7). This file declares them and nothing else: no publisher exists here.
// The executor publishes them after each run commit (05-tasks §5T.4 Run, step
// 3), a change owned by F5-18; until then the catalog entries are declared by
// nothing (kernel.coreTopics lists them, and declareCore is wired by F4-08).
const (
	// RunChangedTopicName carries a RunView on every state or progress change.
	// Keyed by run ID so a coalescing window keeps the latest view per run.
	RunChangedTopicName = "core.task.run-changed.v1"
	// LogAppendedTopicName carries one LogAppended. Keyed by run ID: the SPA reads a
	// run's log on its own keyed stream (05-realtime §4.8 "Subscription set"),
	// and the activity mirror reads the run from the envelope's key.
	LogAppendedTopicName = "core.task.log-appended.v1"
	// FinishedTopicName carries the terminal RunView once per terminal
	// transition: the outcome is State and Reason. Keyed by run ID.
	FinishedTopicName = "core.task.finished.v1"
)

// manageScope is who sees a task envelope until the wiring installs the
// definition's View rule (see visibleToManagers). It is the scope 05-events
// §5E.3.1 gives core.update.found.v1 ("`core:system.manage`"), the core
// operator scope, and 05-rbac §6.1 grants it to admin.
const manageScope rbac.Permission = "core:system.manage"

// runChangedCoalesce is run-changed's realtime window. The spec says
// "Realtime{Coalesce}" without a value (05-events §5E.3.1); 250 ms is the
// window the other per-entity stream uses (books.book.changed.v1, 05-realtime
// §4.7). The terminal view always wins: coalescing keeps the latest per key.
const runChangedCoalesce = 250 * time.Millisecond

// RunChangedTopic is the typed run-changed topic. A function, not a package
// variable (R5). The publisher keys each envelope by the run's ID.
func RunChangedTopic() events.Topic[RunView] { return events.NewTopic[RunView](RunChangedTopicName) }

// LogAppended is the payload of core.task.log-appended.v1: one log line
// ("log line", 05-realtime §4.7), spelled as GET /runs/{id}/log spells it
// (lower-case keys, Fields omitted when empty). It is not LogLine itself
// because LogLine has no JSON tags and a line with no Fields marshals
// "Fields":null, which the reflected schema (an object) refuses, so the bus
// would drop every line that carries no fields; LogLine's stored shape cannot
// take tags without changing the keys already on disk. The run is the
// envelope's Key, not a field.
type LogAppended struct {
	At      time.Time         `json:"at"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

// NewLogAppended is the payload for l.
func NewLogAppended(l LogLine) LogAppended {
	return LogAppended(l) // same fields, in order; only the tags differ
}

// LogAppendedTopic is the typed log-appended topic; the publisher keys each
// envelope by the run's ID.
func LogAppendedTopic() events.Topic[LogAppended] {
	return events.NewTopic[LogAppended](LogAppendedTopicName)
}

// FinishedTopic is the typed finished topic. The payload is the run's terminal
// RunView, the same value the executor's OnTerminal hook receives (05-tasks
// §5T.4), so the notification and the bus envelope carry one outcome.
func FinishedTopic() events.Topic[RunView] { return events.NewTopic[RunView](FinishedTopicName) }

// RunChangedTopicSpec is the catalog entry of core.task.run-changed.v1: bus,
// Realtime{Coalesce}, AnyAuthenticated, Redact = RunViewRedactor() (05-events
// §5E.3.1; COR2-2, COR3-13).
//
// Visible is visibleToManagers, a fail-closed default: the spec's per-envelope
// rule is "the definition's Perms.View" (COR-20), which needs the definition
// registry, and the declaration site (kernel.coreTopics, no arguments) does
// not have one. Until the wiring replaces Visible on the returned value with
// the definition's View rule, only a holder of core:system.manage sees a task
// envelope; a plain signed-in user sees none. Replacing it is the F4-08 or
// executor wiring's job; forgetting to hides the stream, never exposes it.
func RunChangedTopicSpec() events.TopicSpec {
	return RunChangedTopic().Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated,
		Visible:    visibleToManagers,
		Redact:     RunViewRedactor(),
		Realtime:   &events.RealtimeMeta{Coalesce: runChangedCoalesce},
	})
}

// LogAppendedTopicSpec is the catalog entry of core.task.log-appended.v1: bus,
// Realtime (every line, no coalescing), AnyAuthenticated, Redact =
// LogAppendedRedactor(). The 05-events table says "as above" for the rest, so the
// same Visible default as RunChangedTopicSpec applies. 05-realtime §4.8 keeps the
// topic off the shared stream; the SPA subscribes with key=<run id>.
func LogAppendedTopicSpec() events.TopicSpec {
	return LogAppendedTopic().Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated,
		Visible:    visibleToManagers,
		Redact:     LogAppendedRedactor(),
		Realtime:   &events.RealtimeMeta{},
	})
}

// FinishedTopicSpec is the catalog entry of core.task.finished.v1: bus plus
// Notify, no Realtime ("bus + Notify (outcome in payload)", 05-events §5E.3.1),
// visibility "as run-changed", so it carries the same Redact and the same
// Visible default. The Notify marker alone sends nothing: the executor's
// kernel-wired OnTerminal hook stages the notification (COR2-3).
func FinishedTopicSpec() events.TopicSpec {
	return FinishedTopic().Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated,
		Visible:    visibleToManagers,
		Redact:     RunViewRedactor(),
		Notify:     &events.NotifyMeta{Title: "Task finished", DefaultSeverity: "info"},
	})
}

// visibleToManagers is the Visible of the three task entries until the wiring
// installs the definition's View rule: true only for a subscriber holding
// manageScope. A nil checker, or a plain signed-in user, gets false, so an
// unwired entry delivers nothing to them (fail closed).
func visibleToManagers(_ context.Context, who rbac.Checker, _ events.RawEnvelope) bool {
	return who != nil && who.Has(manageScope)
}

// RedactLogAppended is RedactRunView's rule applied to one log line (05-tasks
// §5T.3.10 Client.Log: "each line redacted by RedactRunView's rule"): the free
// text, Message and every Fields value, is blanked unless who holds
// core:users.view, because a line can name a user. At, Level and the Fields
// keys survive, so a viewer still sees that something happened and how severe
// it was. A nil who blanks, as RedactRunView does. It returns a new map and
// never writes to the caller's.
func RedactLogAppended(who rbac.Checker, l LogAppended) LogAppended {
	if who != nil && who.Has(usersView) {
		return l
	}
	l.Message = ""
	if l.Fields != nil {
		blanked := make(map[string]string, len(l.Fields))
		for k := range l.Fields {
			blanked[k] = ""
		}
		l.Fields = blanked
	}
	return l
}

// LogAppendedRedactor adapts RedactLogAppended to events.RedactFunc, the way
// RunViewRedactor adapts RedactRunView: decode, redact, re-encode, and return
// "{}" for a payload that does not decode as a LogAppended, because the stream
// must not carry a payload the rule cannot read. It is the Redact value of
// core.task.log-appended.v1.
func LogAppendedRedactor() events.RedactFunc {
	return func(who rbac.Checker, payload json.RawMessage) json.RawMessage {
		var l LogAppended
		if err := json.Unmarshal(payload, &l); err != nil {
			return json.RawMessage("{}")
		}
		out, err := json.Marshal(RedactLogAppended(who, l))
		if err != nil {
			return json.RawMessage("{}")
		}
		return out
	}
}
