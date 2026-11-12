// file: core/events/topic.go
// version: 1.1.0
// guid: 7bc84272-6a57-423a-8a0b-1db875b4b7d2
// last-edited: 2026-11-12

// Package events is core's in-process, notify-only event bus and the one
// topic catalog of an App (05-events §5E.1, §5E.3). A topic is declared once,
// by its owning module or by core, with its permission and its optional
// notification and realtime metadata; outbox, notify and realtime read the
// catalog and add nothing to it (SIMP-5).
//
// The bus may drop: a subscriber has one bounded FIFO and one goroutine, a
// full queue drops by policy and counts the drop, a panicking handler is
// recovered and keeps its subscription, and Publish never blocks and never
// returns an error (§5E.5.1). Anything whose loss would leave state wrong goes
// through the outbox instead (G4, §5E.5.4).
//
// Layer L4 (03 R4): events imports rbac for TopicSpec.Permission and nothing
// that imports events back; NotifyMeta and RealtimeMeta are plain data, so
// events imports neither notify nor realtime.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/internal/jsonschemax"
	"github.com/falkcorp/mediacore/rbac"
)

// Topic names a typed event stream. Name grammar (03 §3.6):
//
//	"<moduleID>.<noun>.<verb-past>.v<N>", segments [a-z][a-z0-9-]*, N >= 1;
//	<noun> may carry one qualifier segment ("download.grab").
//
// e.g. "books.book.imported.v1", "core.task.run-changed.v1",
// "core.integration.health.changed.v1". A Topic value is inert until its
// owner declares it.
type Topic[T any] struct{ name string }

// NewTopic returns the topic named name. The name is checked when the topic
// is declared, not here: a Topic value is inert until then.
func NewTopic[T any](name string) Topic[T] { return Topic[T]{name: name} }

// Name is the topic's name.
func (t Topic[T]) Name() string { return t.name }

// Spec returns s with Name and Schema filled from t and T (the one JSON Schema
// reflector, internal/jsonschemax at L0 per R4; SIMP-14, COR3-13). The caller
// supplies the rest. A payload type the reflector refuses leaves Schema empty
// and makes the declaration fail with that error.
func (t Topic[T]) Spec(s TopicSpec) TopicSpec {
	s.Name = t.name
	s.payload = reflect.TypeFor[T]()
	s.Schema, s.schemaErr = reflectSchema[T]()
	return s
}

// reflectSchema renders T's payload schema as JSON.
func reflectSchema[T any]() (json.RawMessage, error) {
	sc, err := jsonschemax.For[T](jsonschemax.Base())
	if err != nil {
		return nil, fmt.Errorf("events: payload schema of %v: %w", reflect.TypeFor[T](), err)
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return nil, fmt.Errorf("events: payload schema of %v: %w", reflect.TypeFor[T](), err)
	}
	return b, nil
}

// TopicSpec is the single catalog entry for a topic.
type TopicSpec struct {
	Name       string
	Schema     json.RawMessage  // payload JSON Schema; the versioned contract raw subscribers and raw consumers validate against
	Permission rbac.Requirement // who may observe it on any surface (realtime, notify rules, HTTP); zero value rejected
	Visible    VisibleFunc      // optional per-envelope filter on top of Permission (e.g. a task definition's View)
	Redact     RedactFunc       // optional per-subscriber payload reduction, applied after Visible (COR2-2)
	Durable    *Durable         // non-nil: an outbox topic (5E.4); nil: bus only
	Notify     *NotifyMeta      // non-nil: may be emitted as a notification (meaning owned by 05-notify)
	Realtime   *RealtimeMeta    // non-nil: exposed as a realtime stream (meaning owned by 05-realtime)

	// payload is T when the entry came from Topic[T].Spec, so a typed
	// publisher or subscriber of another type is refused; nil otherwise.
	payload reflect.Type
	// schemaErr is the reflector's refusal of T, reported by Declare.
	schemaErr error
}

// PayloadType is T when the entry came from Topic[T].Spec (outbox.Topic[T].Spec
// goes through it), nil for an entry built by hand. The outbox reads it to
// refuse a typed stage or consumer whose payload type differs from the
// declared topic's (05-events §5E.4), as the bus does for a typed subscriber.
func (s TopicSpec) PayloadType() reflect.Type { return s.payload }

// VisibleFunc is an optional per-envelope filter on top of Permission.
type VisibleFunc func(ctx context.Context, who rbac.Checker, e RawEnvelope) bool

// RedactFunc reduces a payload for one subscriber, e.g. blanks user-identifying
// text the subscriber may not read. Only the topic's owner sets it, in the
// catalog entry, so the one declaration carries it (SIMP-5): the realtime
// stream (05-realtime) applies it after Visible, and an owner's HTTP read model
// of the same data calls the same function (05-tasks RedactRunView; its
// catalog value is the adapter tasks.RunViewRedactor(), COR3-13).
// 05-realtime's RedactFunc is this type.
type RedactFunc func(who rbac.Checker, payload json.RawMessage) json.RawMessage

// Durable marks an outbox topic (05-events §5E.4).
type Durable struct {
	Partitions int // ordering domains; default 16; fixed for the topic's lifetime
}

// NotifyMeta is plain data, so events imports neither notify nor realtime.
// NotifyMeta alone produces no notification (COR2-3): it marks the topic as
// notifiable, and the owner emits each notification through 05-notify's
// outbox-backed Publish (modules: module.Deps.Notify; core: the owning
// service), staged in the same batch as the state change. No bus subscriber
// routes notifications.
type NotifyMeta struct {
	Title           string
	DefaultSeverity string // "info" | "warning" | "error"
}

// RealtimeMeta is plain data read by 05-realtime.
type RealtimeMeta struct {
	Coalesce time.Duration // latest per Key within the window; 0 = every envelope
}

// Catalog is the kernel-built, read-only view of every declared topic of every
// enabled module, plus the declared-but-disabled set (for G1 warnings).
type Catalog interface {
	Lookup(name string) (TopicSpec, bool)
	All() iter.Seq[TopicSpec]
}

// coreOwner is the namespace and owner of core's own topics and subscribers.
const coreOwner = "core"

// splitTopicName checks the topic name grammar and returns its module
// namespace (the first segment).
func splitTopicName(name string) (string, error) {
	segs := strings.Split(name, ".")
	if len(segs) != 4 && len(segs) != 5 {
		return "", fmt.Errorf("%w: %q: want <moduleID>.<noun>[.<qualifier>].<verb-past>.v<N>", ErrTopicName, name)
	}
	for _, s := range segs[:len(segs)-1] {
		if !validSegment(s) {
			return "", fmt.Errorf("%w: %q: segment %q must match [a-z][a-z0-9-]*", ErrTopicName, name, s)
		}
	}
	if !validVersion(segs[len(segs)-1]) {
		return "", fmt.Errorf("%w: %q: the last segment must be v<N> with N >= 1", ErrTopicName, name)
	}
	return segs[0], nil
}

// validSegment is [a-z][a-z0-9-]*.
func validSegment(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// validVersion is v<N>, N >= 1 without a leading zero.
func validVersion(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
