// file: core/integrations/notify/notify.go
// version: 1.0.1
// guid: 3f7a2c91-6d48-4b0e-a5c3-91e82d70b4f6
// last-edited: 2026-11-13

// Package notify is core's notification contract (05-notify): the
// channel-neutral Notification, the Notifier every channel adapter instance
// implements, the Service that records a notification for delivery in the
// caller's own batch, the global routing Rules and their stores, and the
// core.notify.deliver outbox consumer that sends each recorded delivery.
//
// A notification is published, never dispatched: Service.Publish resolves the
// enabled rules in memory and stages one durable
// core.notify.delivery.requested.v1 entry per target in the caller's batch,
// so the state change and its notification commit or fail together. The
// consumer sends on the outbox relay's worker pool under a context of its own,
// so a publisher that returns, or whose request is cancelled, never cancels a
// delivery.
//
// This package is dark until the integrations flip (F7-09): nothing in an app
// builds a Service, declares the topic or registers the consumer before it.
// The two shipped channels live in the sub-packages webhook (core.webhook) and
// mail (core.email); notifytest holds the RuleStore conformance suite.
//
// Layer L4 (03 R4): a public sub-package of integrations takes its layer. It
// holds no package-level mutable state (R5): a Service is per App.
package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/storage"
)

// Severity orders notifications; a Rule's MinSeverity is compared against it.
// The zero value, SeverityInfo, is also what a publisher that sets none gets:
// Publish then replaces it with the topic's default severity.
type Severity uint8

// The severities, lowest first.
const (
	SeverityInfo Severity = iota
	SeveritySuccess
	SeverityWarning
	SeverityError
	SeverityCritical
)

// severityNames are the wire and metric names, by Severity.
func severityNames() [5]string {
	return [5]string{"info", "success", "warning", "error", "critical"}
}

// String is the severity's name ("warning"), which a webhook body and the
// severity metric label carry.
func (s Severity) String() string {
	if names := severityNames(); int(s) < len(names) {
		return names[s]
	}
	return fmt.Sprintf("severity(%d)", uint8(s))
}

// Valid reports whether s is one of the five severities.
func (s Severity) Valid() bool { return s <= SeverityCritical }

// ParseSeverity is String's inverse. The events catalog spells a topic's
// default severity as "info", "warning" or "error"; the spelling of every
// severity is accepted.
func ParseSeverity(name string) (Severity, bool) {
	for i, n := range severityNames() {
		if n == name {
			return Severity(i), true
		}
	}
	return SeverityInfo, false
}

// Link is a labelled URL a notification points at.
type Link struct {
	Label string `json:"label"`
	URL   string `json:"url"` // absolute; relative app paths are resolved against core.http.public_url
}

// Field bounds of a Notification (05-notify §3).
const (
	MaxFields     = 32   // keys
	MaxFieldBytes = 1024 // each key and each value
)

// Notification is channel-neutral. Title and Body are plain text.
type Notification struct {
	ID       string            `json:"id"`       // ULID; set by Publish, idempotency key end to end (receivers dedupe on it)
	Topic    string            `json:"topic"`    // an EVENT topic declared in the events catalog with notify metadata, e.g. "books.book.imported.v1"
	Severity Severity          `json:"severity"` // zero = the topic's default severity from the catalog
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	Links    []Link            `json:"links,omitempty"`
	Entity   *entity.Ref       `json:"entity,omitempty"` // what it is about (03 §3.3); deep links
	Fields   map[string]string `json:"fields,omitempty"` // structured extras for webhook JSON, bounded by MaxFields and MaxFieldBytes
	At       time.Time         `json:"at"`               // zero = the time of Publish

	// App is the publishing app's ID (app.Options.AppID), which the webhook
	// body carries as "app". Publish sets it from Options.AppID, whatever the
	// caller put here. It is not in 05-notify §3's field list, which has no
	// other way to reach the adapter: Env carries no app identity.
	App string `json:"app,omitempty"`
}

// Caps are what a channel can carry.
type Caps struct {
	HTML    bool
	MaxBody int // truncate above this (with "…"); 0 = no limit
}

// Target is one resolved destination for one delivery. v1 channels are global
// (admin-configured instances); a per-user Recipient arrives with per-user
// subscriptions (deferred, 05-notify §1).
type Target struct {
	Instance integrations.Ref `json:"instance"`
}

// Notifier is what every channel adapter instance implements.
type Notifier interface {
	integrations.Instance
	Caps() Caps
	// Send delivers n to t. It returns nil on success, or an error classified
	// with integrations.ClassOf (an *integrations.Error carries the class, the
	// remote status and a Retry-After). An adapter acquires its own gate
	// ticket when it does not send through env.HTTP, which gates every request
	// itself; the consumer never acquires on its behalf.
	Send(ctx context.Context, n Notification, t Target) error
}

// Adapter builds a descriptor for app.Options.Adapters. It is the notify
// kind's typed constructor: the kind is always integrations.KindNotify,
// whatever m.Kind says, and the built instance must be a Notifier.
func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Notifier, error),
) integrations.Descriptor {
	m.Kind = integrations.KindNotify
	return integrations.NewDescriptor(m, build)
}

// Publisher records notifications for delivery. Core subsystems call the
// *Service, which implements it for every core.* topic; a module holds the
// module-scoped Publisher the kernel builds with Service.For and hands out as
// module.Deps.Notify (06).
type Publisher interface {
	// Publish validates n, resolves the enabled global Rules for n.Topic and
	// n.Severity from the in-memory rule set, and stages ONE durable
	// core.notify.delivery.requested.v1 entry per resolved target in b, so the
	// entries commit atomically with the caller's own state change. There is
	// no enqueue-side dedupe: n.ID is a fresh ULID per Publish, so two
	// entries never share a key, and receivers dedupe on
	// X-Mediacore-Delivery. b == nil: Publish writes its own batch. It never
	// blocks on channel I/O. Zero matching targets: nothing is staged (counted
	// no_rule). n.Topic must be a declared event topic whose catalog entry
	// carries notify metadata.
	Publish(ctx context.Context, b *storage.Batch, n Notification) error
}

// The errors of Publish.
var (
	ErrUndeclaredTopic = errors.New("notify: topic not in the events catalog")
	ErrNoNotifyMeta    = errors.New("notify: topic has no notify metadata")
	ErrForeignTopic    = errors.New("notify: topic belongs to another module")
	ErrFieldsTooLarge  = errors.New("notify: fields exceed bounds")
)

// checkFields enforces MaxFields and MaxFieldBytes.
func checkFields(f map[string]string) error {
	if len(f) > MaxFields {
		return fmt.Errorf("%w: %d keys, at most %d", ErrFieldsTooLarge, len(f), MaxFields)
	}
	for k, v := range f {
		if len(k) > MaxFieldBytes || len(v) > MaxFieldBytes {
			return fmt.Errorf("%w: a key or value is longer than %d bytes", ErrFieldsTooLarge, MaxFieldBytes)
		}
	}
	return nil
}

// moduleOf is the module a topic name belongs to: its first segment.
func moduleOf(topic string) string {
	m, _, _ := strings.Cut(topic, ".")
	return m
}
