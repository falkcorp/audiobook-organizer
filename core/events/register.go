// file: core/events/register.go
// version: 1.0.0
// guid: e015b69c-34e9-4839-985a-0aa9828dbb39
// last-edited: 2026-11-10

package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"go.opentelemetry.io/otel/metric"

	"github.com/falkcorp/mediacore/rbac"
)

// The registration errors (05-events §5E.7: closed, boot fails). Declare and
// Subscribe wrap one of them per refused entry and join the results, so
// errors.Is finds each; the kernel maps them to module problems. Each refusal
// wraps exactly one sentinel (a cause is formatted with %v, never a second
// %w), so it stays one leaf of the join and becomes one problem.
var (
	ErrTopicName             = errors.New("events: bad topic name")
	ErrDuplicateTopic        = errors.New("events: topic declared twice")
	ErrTopicNamespace        = errors.New("events: topic outside the declaring module's namespace")
	ErrNoPermission          = errors.New("events: topic without a permission")
	ErrForeignScope          = errors.New("events: topic permission outside the declaring module's namespace")
	ErrSchemaRequired        = errors.New("events: Realtime or Notify on a topic with an empty schema")
	ErrBadSchema             = errors.New("events: topic schema does not resolve")
	ErrRealtimeOnDurable     = errors.New("events: Realtime on a Durable topic: declare a companion bus topic")
	ErrSubscriptionName      = errors.New("events: bad subscription name")
	ErrDuplicateSubscription = errors.New("events: subscription name used twice")
	ErrInvalidSubscription   = errors.New("events: invalid subscription")
	ErrTopicUndeclared       = errors.New("events: subscription to a topic no module declares")
	ErrDurableTopic          = errors.New("events: bus subscription to a Durable topic")
	ErrForeignTopic          = errors.New("events: typed subscription to another module's topic")
	ErrPayloadType           = errors.New("events: payload type differs from the declared topic's")
	ErrFrozen                = errors.New("events: the bus is frozen; declarations are closed")
	ErrClosed                = errors.New("events: the bus is closed")
	ErrNotFrozen             = errors.New("events: Open before Freeze")
	ErrOpen                  = errors.New("events: the bus is already open")
)

// topicState is one declared topic as the bus holds it.
type topicState struct {
	spec     TopicSpec
	owner    string
	resolved *jsonschema.Resolved // nil when Schema is empty
	attr     metric.MeasurementOption
	subs     []*sub // set at Freeze
	hasRaw   bool   // a raw subscriber exists: the JSON form is needed
}

// Declare records moduleID's catalog entries ("core" for core's own topics),
// applying the kernel validation of 05-events §5E.3.1: the name grammar, a
// name declared twice, a topic outside moduleID's namespace, a zero
// Permission, a Permission naming another module's scope (05-rbac §4.9, R3),
// Realtime or Notify with an empty Schema, and Realtime on a Durable topic.
// Every refused entry is reported (errors joined); the accepted ones are
// recorded. Kernel-only, before Freeze.
func (b *Bus) Declare(moduleID string, specs ...TopicSpec) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen.Load() != nil {
		return ErrFrozen
	}
	var errs []error
	for _, s := range specs {
		ts, err := validateTopic(moduleID, s)
		if err == nil {
			if _, dup := b.topics[s.Name]; dup {
				err = fmt.Errorf("%w: %s", ErrDuplicateTopic, s.Name)
			}
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		b.topics[s.Name] = ts
	}
	return errors.Join(errs...)
}

// validateTopic applies the per-entry rules.
func validateTopic(owner string, s TopicSpec) (*topicState, error) {
	var errs []error
	ns, err := splitTopicName(s.Name)
	switch {
	case err != nil:
		errs = append(errs, err)
	case ns != owner:
		errs = append(errs, fmt.Errorf("%w: %s declared by %s", ErrTopicNamespace, s.Name, owner))
	}
	if s.schemaErr != nil {
		errs = append(errs, fmt.Errorf("%w: %s: %v", ErrBadSchema, s.Name, s.schemaErr))
	}
	errs = append(errs, checkPermission(owner, s)...)
	if (s.Realtime != nil || s.Notify != nil) && len(s.Schema) == 0 {
		errs = append(errs, fmt.Errorf("%w: %s", ErrSchemaRequired, s.Name))
	}
	if s.Realtime != nil && s.Durable != nil {
		errs = append(errs, fmt.Errorf("%s: %w", s.Name, ErrRealtimeOnDurable))
	}
	ts := &topicState{spec: s, owner: owner, attr: topicAttr(s.Name)}
	if len(s.Schema) > 0 && s.schemaErr == nil {
		var sc jsonschema.Schema
		if err := json.Unmarshal(s.Schema, &sc); err != nil {
			errs = append(errs, fmt.Errorf("%w: %s: %v", ErrBadSchema, s.Name, err))
		} else if ts.resolved, err = sc.Resolve(nil); err != nil {
			errs = append(errs, fmt.Errorf("%w: %s: %v", ErrBadSchema, s.Name, err))
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return ts, nil
}

// checkPermission is 05-rbac §4.9's rule for a topic: a zero requirement is
// refused (R6); Public and AnyAuthenticated pass; AllOf/AnyOf may name only
// the owner's namespace or core:admin; the delegated and legacy kinds are
// route-only.
func checkPermission(owner string, s TopicSpec) []error {
	req := s.Permission
	switch req.Kind() {
	case rbac.KindInvalid:
		return []error{fmt.Errorf("%w: %s", ErrNoPermission, s.Name)}
	case rbac.KindPublic, rbac.KindAuthenticated:
		return nil
	case rbac.KindAllOf, rbac.KindAnyOf:
		var errs []error
		for _, p := range req.Permissions() {
			if p != rbac.CoreAdmin && p.Namespace() != owner {
				errs = append(errs, fmt.Errorf("%w: %s requires %s", ErrForeignScope, s.Name, p))
			}
		}
		return errs
	default:
		return []error{fmt.Errorf("%w: %s: requirement %s is for legacy routes only", ErrForeignScope, s.Name, req)}
	}
}

// DeclareDisabled records that moduleID is compiled in but disabled (G1). A
// raw subscription to a topic in its namespace is allowed, receives nothing
// and is warned about at Subscribe; its topics are not in the catalog.
// Kernel-only, before Freeze and before any Subscribe that names it.
func (b *Bus) DeclareDisabled(moduleID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen.Load() == nil {
		b.disabled[moduleID] = true
	}
}

// Subscribe records moduleID's subscriptions ("core" for core's, such as the
// realtime hub). Call it after every Declare and DeclareDisabled: the target
// is checked here. Names are "<moduleID>.<name>" and unique per bus; a typed
// subscription (On) needs a topic of moduleID or of core with the same
// payload type; a raw one (OnRaw) needs a declared topic of any module, or a
// topic of a disabled module (WARN, receives nothing); neither may name a
// Durable topic. Every refused subscription is reported (errors joined).
// Kernel-only, before Freeze.
func (b *Bus) Subscribe(moduleID string, subs ...Subscription) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen.Load() != nil {
		return ErrFrozen
	}
	var errs []error
	for _, s := range subs {
		if err := b.checkSubscription(moduleID, s); err != nil {
			errs = append(errs, err)
			continue
		}
		ns, _, _ := strings.Cut(s.topic, ".")
		if b.disabled[ns] {
			b.log.Warn("events: raw subscription to a disabled module's topic receives nothing (G1)",
				"subscriber", s.name, "topic", s.topic, "module", ns)
			b.subNames[s.name] = true
			continue
		}
		b.subNames[s.name] = true
		b.subs = append(b.subs, newSub(b, s, b.topics[s.topic]))
	}
	return errors.Join(errs...)
}

// checkSubscription applies the subscription rules; b.mu is held.
func (b *Bus) checkSubscription(owner string, s Subscription) error {
	if s.err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalidSubscription, s.name, s.err)
	}
	if s.deliver == nil {
		return fmt.Errorf("%w: a Subscription not built by On or OnRaw", ErrInvalidSubscription)
	}
	if !validSubscriptionName(owner, s.name) {
		return fmt.Errorf("%w: %q: want %s.<name> with segments [a-z][a-z0-9-]*", ErrSubscriptionName, s.name, owner)
	}
	if b.subNames[s.name] {
		return fmt.Errorf("%w: %s", ErrDuplicateSubscription, s.name)
	}
	ts, ok := b.topics[s.topic]
	if !ok {
		ns, _, _ := strings.Cut(s.topic, ".")
		if s.raw && b.disabled[ns] {
			return nil
		}
		return fmt.Errorf("%w: %s subscribes to %q", ErrTopicUndeclared, s.name, s.topic)
	}
	if ts.spec.Durable != nil {
		return fmt.Errorf("%w: %s subscribes to %s; consume it through the outbox", ErrDurableTopic, s.name, s.topic)
	}
	if s.raw {
		return nil
	}
	if ts.owner != owner && ts.owner != coreOwner {
		return fmt.Errorf("%w: %s subscribes typed to %s of module %s; use OnRaw", ErrForeignTopic, s.name, s.topic, ts.owner)
	}
	if ts.spec.payload != nil && ts.spec.payload != s.payload {
		return fmt.Errorf("%w: %s handles %v, %s carries %v", ErrPayloadType, s.name, s.payload, s.topic, ts.spec.payload)
	}
	return nil
}

// validSubscriptionName is "<owner>.<seg>(.<seg>)*" with [a-z][a-z0-9-]* segments.
func validSubscriptionName(owner, name string) bool {
	rest, ok := strings.CutPrefix(name, owner+".")
	if !ok || rest == "" {
		return false
	}
	for seg := range strings.SplitSeq(rest, ".") {
		if !validSegment(seg) {
			return false
		}
	}
	return true
}

// frozenState is what Publish reads without the registration lock.
type frozenState struct {
	topics map[string]*topicState
	subs   []*sub
}

// Freeze closes the declarations (06 §6.5.1 P7: frozen when Register
// returns) and publishes the catalog to Publish. Events published from here
// until Open are queued (bounded, by each subscription's policy) and
// delivered from Open; none is delivered before it. Idempotent.
func (b *Bus) Freeze() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.frozen.Load() != nil {
		return
	}
	for _, s := range b.subs {
		ts := b.topics[s.topic]
		ts.subs = append(ts.subs, s)
		if s.raw {
			ts.hasRaw = true
		}
	}
	b.frozen.Store(&frozenState{topics: b.topics, subs: slices.Clone(b.subs)})
}

// Catalog is the read-only view of the declared topics. Before Freeze it
// shows the entries declared so far.
func (b *Bus) Catalog() Catalog { return catalog{b: b} }

type catalog struct{ b *Bus }

func (c catalog) Lookup(name string) (TopicSpec, bool) {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	ts, ok := c.b.topics[name]
	if !ok {
		return TopicSpec{}, false
	}
	return ts.spec, true
}

func (c catalog) All() iter.Seq[TopicSpec] {
	c.b.mu.Lock()
	names := slices.Sorted(maps.Keys(c.b.topics))
	specs := make([]TopicSpec, 0, len(names))
	for _, n := range names {
		specs = append(specs, c.b.topics[n].spec)
	}
	c.b.mu.Unlock()
	return slices.Values(specs)
}
