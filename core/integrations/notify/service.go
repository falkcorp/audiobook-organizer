// file: core/integrations/notify/service.go
// version: 1.0.0
// guid: d27f8b03-4a96-4e51-b0c8-6e19a3f5d724
// last-edited: 2026-11-13

package notify

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// Config is the live core.notify section (05-notify §7), read at every use so
// a change applies without a restart. A zero field takes its default.
type Config struct {
	SendTimeout time.Duration // core.notify.send_timeout, default 15s
	MaxAttempts int           // core.notify.max_attempts, default 10
	MaxAge      time.Duration // core.notify.max_age, default 24h
}

// Defaults of Config.
const (
	DefaultSendTimeout = 15 * time.Second
	DefaultMaxAttempts = 10
	DefaultMaxAge      = 24 * time.Hour
)

func (c Config) withDefaults() Config {
	if c.SendTimeout <= 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.MaxAge <= 0 {
		c.MaxAge = DefaultMaxAge
	}
	return c
}

// Options builds a Service. The kernel supplies every field at F7-09.
type Options struct {
	// AppID is app.Options.AppID: the "app" of a webhook body.
	AppID string
	// Keyspace is the c/notify/ keyspace (Owner): where rules live, and the
	// origin of the batch Publish writes when its caller passes none.
	Keyspace storage.Keyspace
	// Store holds the rules; nil is NativeRuleStore(Keyspace).
	Store RuleStore
	// Catalog is the frozen events catalog Publish and PutRule validate
	// topics against.
	Catalog events.Catalog
	// Outbox stages deliveries (through Writer("core"), the owner of the
	// delivery topic) and reports dead letters to the gauge.
	Outbox *outbox.Outbox
	// Registry leases the channel instances.
	Registry *integrations.Registry
	// Config reads the live core.notify section; nil is all defaults.
	Config func() Config
	// Metrics is the core "notify" scope; nil records nothing.
	Metrics *metrics.Scope
	Log     *slog.Logger
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Entropy feeds notification IDs; nil is crypto/rand.
	Entropy io.Reader
}

// Service records notifications for delivery and owns the routing rules. It is
// goroutine-safe.
type Service struct {
	o     Options
	store RuleStore
	w     outbox.Writer
	log   *slog.Logger
	now   func() time.Time
	inst  *instruments

	rules atomic.Pointer[ruleSet] // the in-memory rule set, replaced whole
	mu    sync.Mutex              // serialises the writers of the rule set; taken before a commit, never inside one
}

var _ Publisher = (*Service)(nil)

// ruleSet is an immutable snapshot of the rules, by ID.
type ruleSet struct{ rules []Rule }

// resolve returns the targets of a notification on topic at sev: the
// instances of every enabled rule that covers it, each once, in rule-ID order.
func (rs *ruleSet) resolve(topic string, sev Severity) []Target {
	var out []Target
	for i := range rs.rules {
		r := &rs.rules[i]
		if !r.covers(topic, sev) {
			continue
		}
		for _, ref := range r.Instances {
			if !slices.ContainsFunc(out, func(t Target) bool { return t.Instance == ref }) {
				out = append(out, Target{Instance: ref})
			}
		}
	}
	return out
}

// New builds a Service and loads its rules. A store that cannot be read
// fails New: serving an empty rule set would drop every notification.
func New(ctx context.Context, o Options) (*Service, error) {
	var missing []string
	if o.Catalog == nil {
		missing = append(missing, "Catalog")
	}
	if o.Outbox == nil {
		missing = append(missing, "Outbox")
	}
	if o.Registry == nil {
		missing = append(missing, "Registry")
	}
	if o.Keyspace.Owner() != Owner {
		missing = append(missing, "Keyspace (the c/notify/ keyspace opened for "+string(Owner)+")")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("notify: New needs Options.%v", missing)
	}
	s := &Service{o: o, store: o.Store, w: o.Outbox.Writer("core"), log: o.Log, now: o.Now}
	if s.store == nil {
		s.store = NativeRuleStore(o.Keyspace)
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.o.Entropy == nil {
		s.o.Entropy = rand.Reader
	}
	var err error
	if s.inst, err = newInstruments(o.Metrics, o.Outbox); err != nil {
		return nil, err
	}
	rules, err := s.store.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("notify: load rules: %w", err)
	}
	s.swap(rules)
	return s, nil
}

// config is the live Config with defaults applied.
func (s *Service) config() Config {
	if s.o.Config == nil {
		return Config{}.withDefaults()
	}
	return s.o.Config().withDefaults()
}

// swap publishes rules as the in-memory set, ordered by ID.
func (s *Service) swap(rules []Rule) {
	rs := &ruleSet{rules: make([]Rule, 0, len(rules))}
	for _, r := range rules {
		rs.rules = append(rs.rules, r.clone())
	}
	slices.SortFunc(rs.rules, func(a, b Rule) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	s.rules.Store(rs)
}

// Publish implements Publisher.
func (s *Service) Publish(ctx context.Context, b *storage.Batch, n Notification) error {
	spec, ok := s.o.Catalog.Lookup(n.Topic)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUndeclaredTopic, n.Topic)
	}
	if spec.Notify == nil {
		return fmt.Errorf("%w: %q", ErrNoNotifyMeta, n.Topic)
	}
	if err := checkFields(n.Fields); err != nil {
		return err
	}
	if !n.Severity.Valid() {
		return fmt.Errorf("%w: severity %d is not a severity", integrations.ErrInvalid, uint8(n.Severity))
	}
	if n.Severity == SeverityInfo {
		if def, ok := ParseSeverity(spec.Notify.DefaultSeverity); ok {
			n.Severity = def
		}
	}
	now := s.now()
	id, err := newULID(now, s.o.Entropy)
	if err != nil {
		return err
	}
	n.ID, n.App = id, s.o.AppID
	if n.At.IsZero() {
		n.At = now
	}
	n.Fields = cloneFields(n.Fields)
	n.Links = slices.Clone(n.Links)

	targets := s.rules.Load().resolve(n.Topic, n.Severity)
	if len(targets) == 0 {
		// Nobody asked to hear it: nothing is staged, so there is no batch to
		// write even when the caller passed none.
		s.inst.published(n.Topic, n.Severity)
		s.inst.targets(n.Topic, resultNoRule, 1)
		return nil
	}
	stage := func(b *storage.Batch) error {
		for _, t := range targets {
			// The key is the target, so one channel's deliveries share a
			// partition and a slow channel does not sit in another's.
			if err := outbox.Stage(s.w, b, DeliveryTopic(), t.Instance.String(), Delivery{Notification: n, Target: t}); err != nil {
				return fmt.Errorf("notify: stage the delivery to %s: %w", t.Instance, err)
			}
		}
		return nil
	}
	if b != nil {
		if err := stage(b); err != nil {
			return err
		}
	} else if err := s.commitOwn(ctx, stage); err != nil {
		return err
	}
	// Counted when staged: a caller's batch may still fail to commit, which
	// loses the notification with the state change it described.
	s.inst.published(n.Topic, n.Severity)
	s.inst.targets(n.Topic, resultResolved, len(targets))
	return nil
}

// commitOwn runs fill in a batch Publish owns and commits it.
func (s *Service) commitOwn(ctx context.Context, fill func(*storage.Batch) error) error {
	b := s.o.Keyspace.NewBatch()
	defer b.Close()
	if err := fill(b); err != nil {
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("notify: commit the deliveries: %w", err)
	}
	return nil
}

func cloneFields(f map[string]string) map[string]string {
	if len(f) == 0 {
		return nil
	}
	out := make(map[string]string, len(f))
	for k, v := range f {
		out[k] = v
	}
	return out
}

// For returns the Publisher for one module: it refuses (ErrForeignTopic) any
// topic outside "<module>.", so a module can notify only about its own
// topics. Kernel-only; modules never see the *Service.
func (s *Service) For(module string) Publisher { return modulePublisher{s: s, module: module} }

type modulePublisher struct {
	s      *Service
	module string
}

func (p modulePublisher) Publish(ctx context.Context, b *storage.Batch, n Notification) error {
	if p.module == "" || moduleOf(n.Topic) != p.module {
		return fmt.Errorf("%w: %q is not a topic of module %q", ErrForeignTopic, n.Topic, p.module)
	}
	return p.s.Publish(ctx, b, n)
}

// manager is the principal of ctx when it holds core:integrations.manage.
func manager(ctx context.Context) (*auth.Principal, error) {
	p, ok := auth.PrincipalFrom(ctx)
	if !ok || p.Perms == nil {
		return nil, fmt.Errorf("%w: no authenticated principal", integrations.ErrForbidden)
	}
	if !p.Perms.Has(integrations.ManagePerm) {
		return nil, fmt.Errorf("%w: %s is required", integrations.ErrForbidden, integrations.ManagePerm)
	}
	return p, nil
}

// ruleCommitAttempts bounds the retries of a rule write that lost a race to
// another writer of the store.
const ruleCommitAttempts = 5

// PutRule validates r and stores it, then replaces the in-memory rule set.
// It needs core:integrations.manage, and the caller must satisfy the declared
// Permission of every topic r names (a "<module>.*" pattern: of every
// notifiable topic it covers), because a shared channel delivers to whoever
// reads it. The refusals wrap integrations.ErrForbidden (403) and
// integrations.ErrInvalid (422); nothing is written then.
func (s *Service) PutRule(ctx context.Context, r Rule) error {
	who, err := manager(ctx)
	if err != nil {
		return err
	}
	specs, err := validateRule(r, s.o.Catalog)
	if err != nil {
		return err
	}
	if err := authorize(who.Perms, r.ID, specs); err != nil {
		return err
	}
	r = r.clone()
	return s.write(ctx, func(ctx context.Context, b *storage.Batch) error { return s.store.Put(ctx, b, r) },
		func(rules []Rule) []Rule {
			return append(slices.DeleteFunc(rules, func(x Rule) bool { return x.ID == r.ID }), r)
		})
}

// DeleteRule removes rule id; ErrRuleNotFound if there is none. It needs
// core:integrations.manage.
func (s *Service) DeleteRule(ctx context.Context, id string) error {
	if _, err := manager(ctx); err != nil {
		return err
	}
	return s.write(ctx, func(ctx context.Context, b *storage.Batch) error { return s.store.Delete(ctx, b, id) },
		func(rules []Rule) []Rule { return slices.DeleteFunc(rules, func(x Rule) bool { return x.ID == id }) })
}

// write commits stage in a batch of its own, retrying a conflict, and then
// replaces the in-memory set with edit applied to it. The mutex orders
// writers in this process and is held across the commit, so two writers never
// swap their sets out of order; no reader takes it.
func (s *Service) write(ctx context.Context, stage func(context.Context, *storage.Batch) error, edit func([]Rule) []Rule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := storage.RetryOnConflict(ctx, ruleCommitAttempts, func(ctx context.Context) error {
		b := s.o.Keyspace.NewBatch()
		defer b.Close()
		if err := stage(ctx, b); err != nil {
			return err
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return err
	}
	s.swap(edit(slices.Clone(s.rules.Load().rules)))
	return nil
}

// Rules yields the rules in ID order from the in-memory set. The error is
// ctx's, once it ends.
func (s *Service) Rules(ctx context.Context) iter.Seq2[Rule, error] {
	rs := s.rules.Load()
	return func(yield func(Rule, error) bool) {
		for _, r := range rs.rules {
			if err := ctx.Err(); err != nil {
				yield(Rule{}, err)
				return
			}
			if !yield(r.clone(), nil) {
				return
			}
		}
	}
}

// TestTopic is the topic of the notification SendTest sends. It names no
// catalog entry: a test send is never published.
const TestTopic = "core.notify.test.v1"

// SendTest sends a fixed test notification to inst, straight through its
// channel and outside the outbox, and reports what a Test of the instance
// would: nil, or an *integrations.Error holding the class and the remote
// status only. The channel's own error text and any remote body stay in the
// instance's log; the caller is a settings screen. It needs
// core:integrations.manage.
func (s *Service) SendTest(ctx context.Context, inst integrations.Ref) error {
	if _, err := manager(ctx); err != nil {
		return err
	}
	if inst.Kind != integrations.KindNotify {
		return fmt.Errorf("%w: %s is not a notify instance", integrations.ErrInvalid, inst)
	}
	nr, release, err := integrations.Lease[Notifier](ctx, s.o.Registry, inst)
	if err != nil {
		return s.testFailure(ctx, inst, err)
	}
	defer release()
	now := s.now()
	id, err := newULID(now, s.o.Entropy)
	if err != nil {
		return err
	}
	n := Notification{
		ID: id, Topic: TestTopic, Severity: SeverityInfo, App: s.o.AppID, At: now,
		Title:  "Test notification",
		Body:   "This is a test notification. Nothing has happened and nothing needs doing.",
		Fields: map[string]string{"test": "true"},
	}
	sctx, cancel := context.WithTimeout(ctx, s.config().SendTimeout)
	defer cancel()
	if err := nr.Send(sctx, n, Target{Instance: inst}); err != nil {
		return s.testFailure(ctx, inst, err)
	}
	return nil
}

// testFailure reduces err to its class, status and Retry-After.
func (s *Service) testFailure(ctx context.Context, inst integrations.Ref, err error) error {
	out := &integrations.Error{Ref: inst, Op: "send_test", Class: integrations.ClassOf(ctx, err)}
	if e, ok := errors.AsType[*integrations.Error](err); ok {
		out.Status, out.RetryAfter = e.Status, e.RetryAfter
	}
	switch {
	case errors.Is(err, integrations.ErrDisabled):
		out.Err = integrations.ErrDisabled
	case errors.Is(err, integrations.ErrNotConfigured):
		out.Err = integrations.ErrNotConfigured
	case errors.Is(err, integrations.ErrUnknownAdapter):
		out.Err = integrations.ErrUnknownAdapter
	}
	return out
}
