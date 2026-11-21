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
	// RunChangedTopicName carries a RunPayload on every state or progress change.
	// Keyed by run ID so a coalescing window keeps the latest view per run.
	RunChangedTopicName = "core.task.run-changed.v1"
	// LogAppendedTopicName carries one LogAppended. Keyed by run ID: the SPA reads a
	// run's log on its own keyed stream (05-realtime §4.8 "Subscription set"),
	// and the activity mirror reads the run from the envelope's key.
	LogAppendedTopicName = "core.task.log-appended.v1"
	// FinishedTopicName carries the terminal RunPayload once per terminal
	// transition: the outcome is State and Reason. Keyed by run ID.
	FinishedTopicName = "core.task.finished.v1"
)

// runChangedCoalesce is run-changed's realtime window. The spec says
// "Realtime{Coalesce}" without a value (05-events §5E.3.1); 250 ms is the
// window the other per-entity stream uses (books.book.changed.v1, 05-realtime
// §4.7). The terminal view always wins: coalescing keeps the latest per key.
const runChangedCoalesce = 250 * time.Millisecond

// ActorPayload is who started or acted on a run, as the wire spells it.
type ActorPayload struct {
	UserID     string `json:"user_id,omitempty"`
	AuthMethod string `json:"auth_method,omitempty"`
	System     string `json:"system,omitempty"`
}

// NewActorPayload is the wire form of a.
func NewActorPayload(a Actor) ActorPayload {
	return ActorPayload(a)
}

// RunPayload is the wire form of a RunView: the payload of
// core.task.run-changed.v1 and core.task.finished.v1, and the JSON of every
// run GET /api/core/v1/tasks/runs[/{id}] serves (taskhttp's runDTO is an alias
// of it, so the two cannot drift). Lower-case keys and the mode as a word, as
// the SPA's coreEvents client reads them. RunView itself carries no tags and
// stays that way: RunRecord embeds it and its stored rows decode through it.
// NewRunPayload is the one constructor.
type RunPayload struct {
	ID       string       `json:"id"`
	Kind     string       `json:"kind"`
	Mode     string       `json:"mode"`
	State    string       `json:"state"`
	Held     bool         `json:"held"`
	Reason   string       `json:"reason,omitempty"`
	Actor    ActorPayload `json:"actor"`
	Trigger  string       `json:"trigger"`
	Parent   string       `json:"parent,omitempty"`
	PlanRef  string       `json:"plan_ref,omitempty"`
	Created  time.Time    `json:"created"`
	Started  time.Time    `json:"started,omitzero"`
	Finished time.Time    `json:"finished,omitzero"`
	Done     int          `json:"done"`
	Total    int          `json:"total"`
	Current  string       `json:"current,omitempty"`

	// Deferred, BlockedBy and DeferredSince are RunView's read-time deferral
	// fields: why a queued run waits, behind which running run, and since when.
	Deferred      string    `json:"deferred,omitempty"`
	BlockedBy     string    `json:"blocked_by,omitempty"`
	DeferredSince time.Time `json:"deferred_since,omitzero"`
}

// NewRunPayload is the wire form of v. It does not redact: the REST handler
// calls RedactRunView first, and the stream applies RunViewRedactor.
func NewRunPayload(v RunView) RunPayload {
	return RunPayload{
		ID: string(v.ID), Kind: string(v.Kind), Mode: v.Mode.String(), State: string(v.State), Held: v.Held,
		Reason: v.Reason, Actor: NewActorPayload(v.Actor), Trigger: string(v.Trigger), Parent: string(v.Parent),
		PlanRef: string(v.PlanRef), Created: v.Created, Started: v.Started, Finished: v.Finished,
		Done: v.Done, Total: v.Total, Current: v.Current,
		Deferred: v.Deferred, BlockedBy: string(v.BlockedBy), DeferredSince: v.DeferredSince,
	}
}

// RedactRunPayload is RedactRunView on the wire form. It does not restate the
// rule or pick the fields it covers: it turns p back into the RunView it was
// made from, passes the whole view through RedactRunView and encodes the result
// with NewRunPayload. Whatever RedactRunView blanks (Current today, any free
// error text a later version adds) is therefore blanked on the stream exactly
// as GET /runs[/{id}] blanks it (runView there is
// NewRunPayload(RedactRunView(v))), and the two cannot diverge.
func RedactRunPayload(who rbac.Checker, p RunPayload) RunPayload {
	return redactPayloadWith(p, func(v RunView) RunView { return RedactRunView(who, v) })
}

// redactPayloadWith is RedactRunPayload over any rule: p as a RunView, through
// rule, back to the wire form. Every field takes the round trip, so a field
// the rule blanks is blank in the result.
func redactPayloadWith(p RunPayload, rule func(RunView) RunView) RunPayload {
	return NewRunPayload(rule(p.runView()))
}

// runView is the RunView NewRunPayload made p from: its inverse for every value
// NewRunPayload writes. A mode word NewRunPayload never writes reads as unset
// and so leaves as "unset": the redacted payload carries no text the rule did
// not see.
func (p RunPayload) runView() RunView {
	return RunView{
		ID: RunID(p.ID), Kind: Kind(p.Kind), Mode: modeOf(p.Mode), State: RunState(p.State), Held: p.Held,
		Reason: p.Reason, Actor: Actor(p.Actor), Trigger: Trigger(p.Trigger), Parent: RunID(p.Parent),
		PlanRef: RunID(p.PlanRef), Created: p.Created, Started: p.Started, Finished: p.Finished,
		Done: p.Done, Total: p.Total, Current: p.Current,
		Deferred: p.Deferred, BlockedBy: RunID(p.BlockedBy), DeferredSince: p.DeferredSince,
	}
}

// modeOf is the Mode whose String is word; anything else is the zero Mode,
// whose String is "unset".
func modeOf(word string) Mode {
	switch word {
	case ModePreview.String():
		return ModePreview
	case ModeLive.String():
		return ModeLive
	default:
		return 0
	}
}

// RunChangedTopic is the typed run-changed topic. A function, not a package
// variable (R5). The publisher keys each envelope by the run's ID and sends
// NewRunPayload(view).
func RunChangedTopic() events.Topic[RunPayload] {
	return events.NewTopic[RunPayload](RunChangedTopicName)
}

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

// FinishedTopic is the typed finished topic. The payload is the wire form of
// the run's terminal RunView, the value the executor's OnTerminal hook
// receives (05-tasks §5T.4), so the notification and the bus envelope carry
// one outcome.
func FinishedTopic() events.Topic[RunPayload] { return events.NewTopic[RunPayload](FinishedTopicName) }

// RunChangedTopicSpec is the catalog entry of core.task.run-changed.v1: bus,
// Realtime{Coalesce}, AnyAuthenticated, Redact = RunViewRedactor() (05-events
// §5E.3.1; COR2-2, COR3-13).
//
// Visible is visibleToAdmins, a fail-closed default: the spec's per-envelope
// rule is "the definition's Perms.View" (COR-20), which needs the definition
// registry, and the declaration site (kernel.coreTopics, no arguments) does
// not have one. Until the wiring replaces Visible on the returned value with
// the definition's View rule, only a holder of core:admin sees a task
// envelope; any other signed-in user sees none. Replacing it is the F4-08 or
// executor wiring's job; forgetting to hides the stream, never exposes it.
func RunChangedTopicSpec() events.TopicSpec {
	return RunChangedTopic().Spec(events.TopicSpec{
		Permission: rbac.AnyAuthenticated,
		Visible:    visibleToAdmins,
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
		Visible:    visibleToAdmins,
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
		Visible:    visibleToAdmins,
		Redact:     RunViewRedactor(),
		Notify:     &events.NotifyMeta{Title: "Task finished", DefaultSeverity: "info"},
	})
}

// visibleToAdmins is the Visible of the three task entries until the wiring
// installs the definition's View rule: true only for a subscriber holding
// rbac.CoreAdmin ("Perms.View or admin", the coordinator ruling of 2026-11-20;
// core:admin has no custom-role spread the way core:system.manage does, so a
// forgotten swap cannot show every run to a role built for something else). A
// nil checker, or any other signed-in user, gets false, so an unwired entry
// delivers nothing to them (fail closed).
func visibleToAdmins(_ context.Context, who rbac.Checker, _ events.RawEnvelope) bool {
	return who != nil && who.Has(rbac.CoreAdmin)
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
