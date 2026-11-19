// file: core/internal/logjournal/tail.go
// version: 1.0.0
// guid: 4f1c7d92-8a3e-4b65-9d02-e7a5c3b81f46
// last-edited: 2026-11-19

package logjournal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/rbac"
)

// The journal's live tail (05-logging §5, "Live tail"): every record the
// journal keeps is also published, already redacted, on the bus topic
// core.log.recorded.v1, which realtime streams to a principal holding
// core:system.manage. The topic is declared by this package (TailSpec) and
// registered by the kernel with the hub, which is F6-03's; until then nothing
// calls StartTail and the SPA's log view polls the query route (05-logging
// §5, "When the surface lands").
//
// The tail is a second log tap with its own queue and goroutine rather than a
// hook in the journal tap's batch loop, so the write path is untouched and a
// slow bus costs the journal nothing: a record the tail's queue cannot take is
// dropped and counted under TailTapName, and the bus drops (counted, never
// blocks) what a subscriber's queue cannot take (05-events §5E.5.1). A publish
// runs on the tap's goroutine, one record at a time, so there is no goroutine
// per record.
//
// The log can re-enter the tail through the bus: the bus's drop warning is
// itself a log record. Its warning is limited to one a minute per subscriber
// and a rejected publish is logged once per topic, so the loop is bounded and
// cannot amplify.

const (
	// TailTopicName is the live tail's catalog name (05-events §5E.3.1: core's
	// own topic, owner "core").
	TailTopicName = "core.log.recorded.v1"
	// TailTapName is the tail's log tap: its drops are
	// log.Counters.Dropped[TailTapName], exported by the metrics bridge as
	// mediacore_log_dropped_total{sink="journal-tail"}.
	TailTapName = "journal-tail"

	// PermSystemManage is the permission that opens the tail and the query
	// route (05-logging §5). It is AuthRequired, so with authentication off
	// nobody holds it and the tail delivers nothing (05-logging §5, "Auth-off
	// reach": a deliberate tightening).
	PermSystemManage rbac.Permission = "core:system.manage"
	// PermUsersView lifts the other-user rule: it is the read side of user data
	// (05-rbac §6.1, the successor of HEAD's users.manage gate).
	PermUsersView rbac.Permission = "core:users.view"
)

// RecordView is one journal record on the wire, the payload of
// core.log.recorded.v1. It is the same view the query route returns for a
// record (loghttp's recordView: same members, same names), so REST and the
// stream agree: attributes are flat, under their dotted keys, and rendered as
// text, with a repeated key keeping its last value.
type RecordView struct {
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"` // slog level name, lower case
	Module    string            `json:"module"`
	Subsystem string            `json:"subsystem"`
	Message   string            `json:"message"`
	Attrs     map[string]string `json:"attrs"` // dotted keys, as the native store flattens them
	TraceID   string            `json:"trace_id,omitempty"`
	SpanID    string            `json:"span_id,omitempty"`
}

// TailTopic is the typed topic of the live tail.
func TailTopic() events.Topic[RecordView] { return events.NewTopic[RecordView](TailTopicName) }

// TailSpec is the catalog entry of core.log.recorded.v1 for the kernel to
// declare under owner "core" (F6-03). It is a bus topic with Realtime and never
// Durable (05-events §5E.12 TestCatalog_RejectsRealtimeOnDurable), needs
// core:system.manage, and carries the other-user rule: a subscriber without
// core:users.view is not sent a record otherUser flags (Visible) and sees
// user_id blank on every record it is sent (Redact). otherUser is the
// module-registered predicate (Registrar.LogOtherUserData); nil means no module
// registered one, so nothing is flagged and only the blanking applies.
func TailSpec(otherUser log.OtherUserPredicate) events.TopicSpec {
	return TailTopic().Spec(events.TopicSpec{
		Permission: rbac.Need(PermSystemManage),
		Visible:    tailVisible(otherUser),
		Redact:     tailRedact,
		Realtime:   &events.RealtimeMeta{},
	})
}

// mayReadUsers is rbac.Can's rule on a checker: the scope is held and the
// requirement satisfied. A nil checker holds nothing.
func mayReadUsers(who rbac.Checker) bool {
	return who != nil && who.Has(PermUsersView) && who.Satisfies(rbac.Need(PermUsersView))
}

// tailVisible is TopicSpec.Visible: the query route's Hide, applied per
// envelope. The viewer is the request principal's user ID from ctx, exactly as
// the route reads it (the hub hands Visible the stream's request context); a
// context without a principal has viewer "", which the predicate fails closed
// on by its own contract. A payload that does not decode is withheld.
func tailVisible(otherUser log.OtherUserPredicate) events.VisibleFunc {
	return func(ctx context.Context, who rbac.Checker, e events.RawEnvelope) bool {
		if mayReadUsers(who) || otherUser == nil {
			return true
		}
		var v RecordView
		if json.Unmarshal(e.Payload, &v) != nil {
			return false
		}
		rec, ok := v.record()
		if !ok {
			return false
		}
		viewer := ""
		if p, ok := auth.PrincipalFrom(ctx); ok && p.User != nil {
			viewer = string(p.User.ID)
		}
		return !otherUser(rec, viewer)
	}
}

// tailRedact is TopicSpec.Redact: user_id, top level or inside a group
// (".user_id"), is present and empty for a subscriber without core:users.view,
// as on the query route. A payload that does not decode comes back as nothing,
// which realtime withholds.
func tailRedact(who rbac.Checker, payload json.RawMessage) json.RawMessage {
	if mayReadUsers(who) {
		return payload
	}
	var v RecordView
	if json.Unmarshal(payload, &v) != nil {
		return nil
	}
	for k := range v.Attrs {
		if k == log.KeyUserID || strings.HasSuffix(k, "."+log.KeyUserID) {
			v.Attrs[k] = ""
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}

// record is v as the log.Record a predicate is asked about. Attributes are
// text, in key order. ok is false for a level name that does not parse.
func (v RecordView) record() (log.Record, bool) {
	var lv slog.Level
	if lv.UnmarshalText([]byte(v.Level)) != nil {
		return log.Record{}, false
	}
	rec := log.Record{
		Time: v.Time, Level: lv, Module: v.Module, Subsystem: v.Subsystem,
		Message: v.Message, TraceID: v.TraceID, SpanID: v.SpanID,
	}
	for _, k := range slices.Sorted(maps.Keys(v.Attrs)) {
		rec.Attrs = append(rec.Attrs, slog.String(k, v.Attrs[k]))
	}
	return rec, true
}

// viewOf is r as the query route would return it once the journal had stored
// it: r goes through the store's own row encoding (attributes flattened, a
// zero time replaced, a time the store refuses refused), so the two views
// cannot drift. user_id is untouched: it is blanked per subscriber, at send.
func viewOf(r log.Record, now time.Time) (RecordView, bool) {
	t := r.Time
	if t.IsZero() {
		t = now
	}
	if _, err := msOf(t); err != nil {
		return RecordView{}, false
	}
	rw, err := toRow(r, t)
	if err != nil {
		return RecordView{}, false
	}
	rec, err := rw.record()
	if err != nil {
		return RecordView{}, false
	}
	v := RecordView{
		Time: rec.Time, Level: strings.ToLower(rec.Level.String()), Module: rec.Module, Subsystem: rec.Subsystem,
		Message: rec.Message, Attrs: make(map[string]string, len(rec.Attrs)), TraceID: rec.TraceID, SpanID: rec.SpanID,
	}
	for _, a := range rec.Attrs {
		v.Attrs[a.Key] = a.Value.Resolve().String()
	}
	return v, true
}

// ErrNoPublisher is StartTail's refusal of a nil publisher.
var ErrNoPublisher = errors.New("logjournal: the live tail needs an events publisher")

// TailCounters are the tail's own counters.
type TailCounters struct {
	// Published counts records handed to the bus.
	Published uint64
	// Skipped counts records the journal's own encoding would refuse (a time
	// the store cannot hold), which the query route could therefore never
	// return, so the tail does not publish them either.
	Skipped uint64
}

// Tail publishes the journal's records on core.log.recorded.v1. It is safe for
// concurrent use.
type Tail struct {
	pub       events.Publisher
	now       func() time.Time
	stopTap   func()
	published atomic.Uint64
	skipped   atomic.Uint64
}

// StartTail registers the tail's log tap on l, publishing through pub (the
// kernel's *events.Bus: the topic is core's). It uses the journal's own
// settings: with s.Enabled false nothing is registered and the Tail is inert,
// and the least severe record published is s.MinLevel, so the tail carries
// exactly what the journal keeps. Records are published whether or not the
// journal's store write succeeds.
func StartTail(l *log.Logging, pub events.Publisher, s Settings) (*Tail, error) {
	if pub == nil {
		return nil, ErrNoPublisher
	}
	level, err := ParseLevel(s.MinLevel)
	if err != nil {
		return nil, err
	}
	t := &Tail{pub: pub, now: time.Now, stopTap: func() {}}
	if !s.Enabled {
		return t, nil
	}
	t.stopTap = l.Tap(TailTapName, level, max(s.QueueSize, minQueue), t.offer)
	return t, nil
}

// offer runs on the tap's goroutine: it never blocks (events.Publish does not).
func (t *Tail) offer(ctx context.Context, r log.Record) {
	v, ok := viewOf(r, t.now())
	if !ok {
		t.skipped.Add(1)
		return
	}
	events.Publish(t.pub, ctx, TailTopic(), "", v)
	t.published.Add(1)
}

// Stop removes the tap from the logger; its queued records are discarded, as
// log's stop does. It is idempotent.
func (t *Tail) Stop() { t.stopTap() }

// Counters is the tail's current counters.
func (t *Tail) Counters() TailCounters {
	return TailCounters{Published: t.published.Load(), Skipped: t.skipped.Load()}
}
