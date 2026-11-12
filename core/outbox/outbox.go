// file: core/outbox/outbox.go
// version: 1.0.1
// guid: 5b3c4366-8534-4dc4-870f-850dfab2b120
// last-edited: 2026-11-12

// Package outbox is core's durable, at-least-once event delivery (05-events
// §5E.1, §5E.4, §5E.5.2, §5E.5.3). A producer stages a message into its own
// storage batch with Stage; it exists if and only if that batch commits. One
// relay, the task kind core.outbox.relay, sequences committed messages in
// commit order into per-topic, per-partition streams and delivers them to the
// consumers modules and core declared, retrying with backoff, dead-lettering
// what cannot be delivered and deleting what every declared consumer has
// acked.
//
// Module consumers are always task runs (ConsumeIntoTask): the relay only
// enqueues, so a module's write keeps its declared effects, its ref locks and
// its fence. Consume, ConsumeTx and ConsumeRaw are core's, and
// archtest.OutboxModuleConsumersAreTasks flags a module package that calls
// them.
//
// The package is dark until F6-03: ABO declares no durable topic and no
// consumer, the kernel builds nothing from it, and the relay is registered
// with no executor to run it.
//
// Layer L4 (03 R4): it imports events (one catalog) and tasks (the relay and
// the module consumers are tasks) and the storage layer; it holds no
// package-level mutable state (R5).
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// closeBatch closes b and, when the caller has no error of its own yet, reports
// a failed close as that error: a batch is closed after its commit or discard,
// and a close that fails is a storage fault the caller should see, not drop.
// Use it as `defer closeBatch(b, &err)` with a named error result.
func closeBatch(b *storage.Batch, errp *error) {
	if cerr := b.Close(); cerr != nil && *errp == nil {
		*errp = fmt.Errorf("outbox: close batch: %w", cerr)
	}
}

// The errors of 05-events §5E.4, then the registration refusals of §5E.7.
// Each registration refusal wraps exactly one sentinel, so the kernel can map
// it to a module problem.
var (
	ErrUnknownTopic = errors.New("outbox: topic not declared")
	ErrNotOwner     = errors.New("outbox: topic belongs to another module")
	ErrPermanent    = errors.New("outbox: permanent failure") // wrap to dead-letter immediately
	ErrSchema       = errors.New("outbox: payload does not match the declared schema")

	ErrNotDurable         = errors.New("outbox: topic is not Durable")
	ErrPayloadType        = errors.New("outbox: payload type differs from the declared topic's")
	ErrPayloadTooLarge    = errors.New("outbox: payload is larger than the outbox accepts")
	ErrBadPartitions      = errors.New("outbox: a durable topic's partitions must be 1 to 99")
	ErrConsumerName       = errors.New("outbox: bad consumer name")
	ErrDuplicateConsumer  = errors.New("outbox: consumer name used twice")
	ErrConsumerUndeclared = errors.New("outbox: consumer name is not declared")
	ErrForeignTopic       = errors.New("outbox: typed consumer of another module's topic")
	ErrForeignKind        = errors.New("outbox: ConsumeIntoTask kind belongs to another module")
	ErrModeOption         = errors.New("outbox: a ConsumeIntoTask consumer needs exactly one of Live() or Preview()")
	ErrModeNotAllowed     = errors.New("outbox: Live() and Preview() belong to ConsumeIntoTask consumers only")
	ErrWhereType          = errors.New("outbox: Where of the wrong payload type")
	ErrUnorderedTx        = errors.New("outbox: Unordered() is not available on ConsumeTx or ConsumeIntoTask")
	ErrInvalidHandler     = errors.New("outbox: invalid handler")
	ErrFrozen             = errors.New("outbox: the outbox is frozen; registrations are closed")
	ErrModuleHandler      = errors.New("outbox: module consumers are task runs")
	ErrNoRuns             = errors.New("outbox: no task executor is bound")
	ErrUnknownConsumer    = errors.New("outbox: consumer unknown")
	ErrDeadNotFound       = errors.New("outbox: no such dead letter")
	// ErrDeadConflict: the dead letter kept changing under the operation (other
	// operators acting on it); list it again and retry if it is still there.
	ErrDeadConflict = errors.New("outbox: the dead letter changed under the operation")
)

// MaxPayloadBytes is the most a staged payload may encode to: the outbox
// holds messages in memory by the chunk, so one message is bounded.
const MaxPayloadBytes = 1 << 20

// RelayKind is the task kind of the relay (05-events §5E.5.3).
const RelayKind tasks.Kind = "core.outbox.relay"

// ComponentID is the relay's health component.
const ComponentID health.ComponentID = "core.outbox"

// Config is core.outbox.* (05-events §5E.9), read through Options.Config each
// time it is needed, so a hot reload takes effect on the next use.
type Config struct {
	MaxAttempts          int           // core.outbox.max_attempts
	BackoffMin           time.Duration // core.outbox.backoff_min
	BackoffMax           time.Duration // core.outbox.backoff_max
	RelayWorkers         int           // core.outbox.relay_workers (next run)
	RelayRotate          time.Duration // core.outbox.relay_rotate (restart)
	RelayEnabled         bool          // core.outbox.relay_enabled
	HandlerTimeout       time.Duration // core.outbox.handler_timeout
	IntakePoll           time.Duration // core.outbox.intake_poll
	UnorderedParallelism int           // core.outbox.unordered_parallelism
	OrphanRetention      time.Duration // core.outbox.orphan_retention
}

// DefaultConfig is the table of 05-events §5E.9.
func DefaultConfig() Config {
	return Config{
		MaxAttempts: 20, BackoffMin: time.Second, BackoffMax: 5 * time.Minute, RelayWorkers: 4,
		RelayRotate: time.Hour, RelayEnabled: true, HandlerTimeout: 30 * time.Second, IntakePoll: time.Second,
		UnorderedParallelism: 4, OrphanRetention: 7 * 24 * time.Hour,
	}
}

// normalized fills every field a caller left unusable with its default, so a
// zero or negative value can never make the relay spin, divide by zero or
// retry forever.
func (c Config) normalized() Config {
	d := DefaultConfig()
	if c.MaxAttempts < 1 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = d.BackoffMin
	}
	if c.BackoffMax < c.BackoffMin {
		c.BackoffMax = max(d.BackoffMax, c.BackoffMin)
	}
	if c.RelayWorkers < 1 {
		c.RelayWorkers = d.RelayWorkers
	}
	if c.RelayRotate <= 0 {
		c.RelayRotate = d.RelayRotate
	}
	if c.HandlerTimeout <= 0 {
		c.HandlerTimeout = d.HandlerTimeout
	}
	if c.IntakePoll <= 0 {
		c.IntakePoll = d.IntakePoll
	}
	if c.UnorderedParallelism < 1 {
		c.UnorderedParallelism = d.UnorderedParallelism
	}
	if c.OrphanRetention <= 0 {
		c.OrphanRetention = d.OrphanRetention
	}
	return c
}

// Runs is what the outbox asks of the task executor: Tx.StartRun's
// Executor.StartIn, the one commit path of a batch that carries a run store
// write (Executor.WriteRuns), and the nudge's Start. tasks.Executor satisfies
// it. It is bound late (BindRuns): the kernel builds the outbox before the
// executor exists.
type Runs interface {
	StartIn(ctx context.Context, b *storage.Batch, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error)
	WriteRuns(ctx context.Context, open func() *storage.Batch, stage func(ctx context.Context, b *storage.Batch) error) error
	Start(ctx context.Context, kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error)
}

// Options builds an Outbox.
type Options struct {
	// Keyspace is the c/outbox/ keyspace (outboxstore.Owner's). Required.
	Keyspace storage.Keyspace
	// Catalog is the App's topic catalog; the outbox reads the Durable entries.
	// Required.
	Catalog events.Catalog
	// Config is core.outbox.*; nil takes DefaultConfig.
	Config func() Config
	// CoreConsumers is the kernel's static core consumer list (05-events
	// §5E.4): the only names a core consumer may take.
	CoreConsumers []string
	// Log is the logger; nil discards.
	Log *slog.Logger
	// Now is the clock; nil is time.Now. Every stamp and every backoff comes
	// from it, so a test under synctest runs on the bubble's clock.
	Now func() time.Time
	// Rand returns a float in [0, 1) for backoff jitter; nil is math/rand/v2.
	Rand func() float64
	// Health is the core.outbox component's reporter; nil reports nothing.
	Health health.Reporter
	// Instruments are the outbox metric families (NewInstruments); nil
	// records nothing.
	Instruments *Instruments
}

// Outbox is one per App, kernel-held. It is safe for concurrent use.
type Outbox struct {
	store  *outboxstore.Store
	cat    events.Catalog
	cfgFn  func() Config
	log    *slog.Logger
	now    func() time.Time
	rnd    func() float64
	health *healthState
	inst   *Instruments
	core   map[string]bool // the static core consumer list

	ids ulidGen

	mu       sync.Mutex // registration state
	frozen   atomic.Bool
	consumer map[string]*consumerReg // by name
	declared map[string]bool         // every declared consumer name: manifests, enabled or not, and core's list
	byModule map[string]map[string]bool

	runs atomic.Pointer[runsBox]

	wake       chan struct{} // the intake nudge (a buffered 1)
	startReq   chan struct{} // asks RunNudger to start a relay (a buffered 1)
	running    atomic.Bool   // a relay run is in its loop
	opsEpoch   atomic.Uint64 // bumped by every operator action; the relay forgets what it knew
	sweepEvery time.Duration // retention sweep interval
	hooks      hooks
	faults     faultLimiter // one logged line a minute per repeating failure, for the relay and RefreshStatus alike
	topicsMu   sync.Mutex
	topicsMem  map[string]*topicInfo
}

type runsBox struct{ r Runs }

// New builds the outbox over the c/outbox/ keyspace. It writes nothing.
func New(o Options) (*Outbox, error) {
	if o.Catalog == nil {
		return nil, errors.New("outbox: New needs the topic catalog")
	}
	if o.Keyspace.Owner() != outboxstore.Owner {
		return nil, fmt.Errorf("outbox: New needs the %s keyspace, got owner %q", outboxstore.Owner, o.Keyspace.Owner())
	}
	ob := &Outbox{
		store: outboxstore.New(o.Keyspace), cat: o.Catalog, log: o.Log, now: o.Now, rnd: o.Rand,
		cfgFn: o.Config, inst: o.Instruments, health: newHealthState(o.Health, nil),
		consumer: map[string]*consumerReg{}, declared: map[string]bool{}, byModule: map[string]map[string]bool{},
		core: map[string]bool{}, wake: make(chan struct{}, 1), startReq: make(chan struct{}, 1),
		sweepEvery: 30 * time.Second, topicsMem: map[string]*topicInfo{},
	}
	if ob.log == nil {
		ob.log = slog.New(slog.DiscardHandler)
	}
	if ob.now == nil {
		ob.now = time.Now
	}
	if ob.rnd == nil {
		ob.rnd = randFloat
	}
	for _, n := range o.CoreConsumers {
		ob.core[n] = true
		ob.declared[n] = true
	}
	ob.ids.now = ob.now
	ob.health.now = ob.now
	return ob, nil
}

// Config is the current core.outbox.* values, normalized.
func (o *Outbox) config() Config {
	if o.cfgFn == nil {
		return DefaultConfig()
	}
	return o.cfgFn().normalized()
}

// BindRuns binds the task executor, once it exists. The relay and the
// consumers that start runs need it; staging does not.
func (o *Outbox) BindRuns(r Runs) { o.runs.Store(&runsBox{r: r}) }

func (o *Outbox) runsOrNil() Runs {
	if b := o.runs.Load(); b != nil {
		return b.r
	}
	return nil
}

// DeclareConsumers records the consumer names a module's manifest lists
// (Manifest.OutboxConsumers), enabled or disabled: they are known while the
// module is off, so retention keeps its messages (05-events §5E.5.2). Call it
// for every manifest of the build before Register.
func (o *Outbox) DeclareConsumers(moduleID string, names ...string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.frozen.Load() {
		return ErrFrozen
	}
	var errs []error
	for _, n := range names {
		if !validConsumerName(moduleID, n) {
			errs = append(errs, fmt.Errorf("%w: %q: want %s.<name> with segments [a-z][a-z0-9-]*", ErrConsumerName, n, moduleID))
			continue
		}
		if o.declared[n] {
			errs = append(errs, fmt.Errorf("%w: %s is declared twice", ErrDuplicateConsumer, n))
			continue
		}
		o.declared[n] = true
		if o.byModule[moduleID] == nil {
			o.byModule[moduleID] = map[string]bool{}
		}
		o.byModule[moduleID][n] = true
	}
	return errors.Join(errs...)
}

// validConsumerName is "<owner>.<seg>(.<seg>)*" with [a-z][a-z0-9-]* segments.
func validConsumerName(owner, name string) bool {
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

// Freeze closes registration (frozen when Register returns, 05-events §5E.6).
// It refuses a durable topic whose partition count the key layout cannot
// hold, so the failure is at boot and not at the first message. Idempotent.
func (o *Outbox) Freeze() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.frozen.Load() {
		return nil
	}
	var errs []error
	for t := range o.cat.All() {
		if t.Durable == nil {
			continue
		}
		if !outboxstore.ValidPartitions(outboxstore.Partitions(t.Durable.Partitions)) {
			errs = append(errs, fmt.Errorf("%w: %s declares %d", ErrBadPartitions, t.Name, t.Durable.Partitions))
		}
	}
	o.frozen.Store(true)
	return errors.Join(errs...)
}

// Declared reports whether name is a declared consumer: listed by a manifest
// (enabled or disabled), on the static core list, or registered.
func (o *Outbox) Declared(name string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.declared[name]
}

// ConsumerNames lists the registered consumers, sorted.
func (o *Outbox) ConsumerNames() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.consumer))
	for n := range o.consumer {
		out = append(out, n)
	}
	slices.Sort(out)
	return out
}

// consumers is the registered consumers sorted by name.
func (o *Outbox) consumers() []*consumerReg {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*consumerReg, 0, len(o.consumer))
	for _, c := range o.consumer {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b *consumerReg) int { return strings.Compare(a.name, b.name) })
	return out
}

// Hooks are the crash-injection seams the tests drive; they are never set in
// production. A seam runs inside the step it names, after that step's last
// durable write staged and before the next one's.
type hooks struct {
	mu sync.Mutex
	// BeforeIntakeCommit runs with the intake batch staged, before it commits.
	beforeIntakeCommit func(topic string)
	// AfterIntakeCommit runs once the intake batch committed.
	afterIntakeCommit func(topic string)
	// AfterHandler runs when a handler returned and its ack is not yet staged.
	afterHandler func(consumer string, seq uint64)
	// BeforeAckCommit runs with an ack staged, before it commits.
	beforeAckCommit func(consumer string, seq uint64)
	// AfterAckCommit runs once an ack committed.
	afterAckCommit func(consumer string, seq uint64)
	// BeforeDeadCommit runs with an operator's retry or discard of a dead letter
	// staged, before it commits.
	beforeDeadCommit func(consumer string, seq uint64)
	// BeforeRetentionBatch runs once a retention sweep decided what to delete and
	// before the batch that deletes it re-reads the cursors it rests on (pp is -1
	// for an orphan cursor dropped on its own).
	beforeRetentionBatch func(topic string, pp int)
}

func (h *hooks) get() hooks {
	h.mu.Lock()
	defer h.mu.Unlock()
	return hooks{beforeIntakeCommit: h.beforeIntakeCommit, afterIntakeCommit: h.afterIntakeCommit,
		afterHandler: h.afterHandler, beforeAckCommit: h.beforeAckCommit, afterAckCommit: h.afterAckCommit,
		beforeDeadCommit: h.beforeDeadCommit, beforeRetentionBatch: h.beforeRetentionBatch}
}

// topicInfo is a durable topic's resolved view.
type topicInfo struct {
	name       string
	partitions int
	spec       events.TopicSpec
	check      *schemaCheck
}
