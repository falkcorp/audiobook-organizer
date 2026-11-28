// file: core/kernel/events_flip.go
// version: 0.1.0
// guid: 3e6c1a94-7b25-4d08-9fa3-5c2d84e17b60
// last-edited: 2026-11-22

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/internal/eventhttp"
	"github.com/falkcorp/mediacore/internal/executor"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/internal/outboxhttp"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// This file is the events flip's wiring (F6-03, 06 section 6.10 F6 row): the
// outbox and the realtime hub, both built over the identity boot's events bus.
// Everything here is reached only through hooks in the identity boot
// (identity_p5.go, identity_routes.go, tasks.go, supervisor.go), so an app that
// did not declare the identity flip builds none of it.
//
// The order, with the hook that runs each step:
//
//   - P2/P4a: storageOwners appends eventsOwners (outbox's c/outbox/), so the
//     keyspace exists once Prepare returns. The owner declares no claims and no
//     level step, so it raises no level (TestEventsOwners_RaiseNoStorageLevel);
//   - P5, buildIdentityServices, after auth: buildEventsFlip builds the outbox
//     and the hub over the bus's catalog. Both read the catalog lazily, so they
//     may be built before P7 declares its topics;
//   - P6: applyIdentityDeps hands each module its Deps.Outbox writer;
//   - routes, mountIdentity: mountEventsRoutes puts the stream and the outbox
//     operator routes on CoreGroup;
//   - P7, identityConsume: the modules' topics, then the journal's live tail
//     (declared once the modules' other-user predicates are collected), then
//     the hub's subscriptions (after every Declare, before Freeze), then the
//     outbox's consumer names for EVERY manifest (a disabled module's too) and
//     the handlers of the enabled ones, then Freeze, then the outbox's Freeze;
//   - P6 end, registerCoreKinds: the relay is one of core's own task kinds
//     (eventsTaskDefinitions), so
//     the executor starts for an app that declared the flip; the executor gets
//     the bus to publish core.task.* on, and the outbox gets the executor
//     (bindOutboxRuns);
//   - Run: the bus opens (openEvents), then the journal tail starts (it
//     publishes, so only after the bus is open) and the hub runs
//     (startRealtime); the nudger starts with the executor;
//   - the listener's shutdown: the hub is closed (shutdownHooks), so open
//     streams are told closed{shutdown} and the drain is not held open by them;
//   - stop: the nudger stops before the executor drains; the tail and the hub
//     stop in closeEvents, before the bus closes.
type eventsFlip struct {
	outbox *kernelOutbox
	hub    *kernelRealtime

	mu        sync.Mutex       // guards tail and the hub's run handle
	tail      *logjournal.Tail // nil until startTail; stopped by closeEvents
	hubCancel context.CancelFunc
	hubDone   chan error
}

// realtimeShutdownSlice is how long the listener's shutdown hook waits for open
// streams to end after telling each one closed{shutdown}. The hub's own writes
// are bounded by core.realtime.write_timeout (10 s by default), so a client
// that cannot take the frame is cut at this slice and not at that one.
const realtimeShutdownSlice = 2 * time.Second

// eventsOwners are the storage owners of the events flip, claimed in the store
// only for an app that declared the identity boot (whose bus the outbox hangs
// on). Registering a new owner is a one-time write in the claim ledger at the
// first P4b of a store; it carries no level (storage.OwnerSpec's Spec.Level is
// zero), so no owner's store level moves.
func (k *Kernel) eventsOwners() []storage.OwnerSpec {
	if !k.identityBootDeclared() {
		return nil
	}
	return []storage.OwnerSpec{outboxOwnerSpec()}
}

// buildEventsFlip is the flip's P5 step: the outbox over c/outbox/ and the hub
// over the bus's catalog and the auth service. Neither starts anything or
// writes. It runs after buildEvents and after auth, because the hub revalidates
// principals through the auth service and refreshes them through its Refresh.
func (k *Kernel) buildEventsFlip() error {
	id := k.ident
	if k.events == nil || id == nil || id.svc.Auth == nil || k.store == nil || k.store.db == nil || k.healthReg == nil || k.cfgBoot == nil {
		return errors.New("kernel: events flip: P5 needs the bus, the auth service, the open store, the health registry and the config handles")
	}
	oh, ok := handleOf[outboxSettings](k.cfgBoot.handles)
	if !ok {
		return errors.New("kernel: events flip: the core.outbox section is not registered")
	}
	rh, ok := handleOf[realtimeSettings](k.cfgBoot.handles)
	if !ok {
		return errors.New("kernel: events flip: the core.realtime section is not registered")
	}
	rep, err := k.healthReg.Register(outboxHealthCheck())
	if err != nil {
		return fmt.Errorf("kernel: events flip: health: %w", err)
	}
	now := time.Now
	ko, err := newKernelOutbox(k.store.db, k.provider, k.events.catalog(), func() outboxSettings { return *oh.Get() }, rep, k.coreLog("outbox"), now)
	if err != nil {
		return err
	}
	kr, err := newKernelRealtime(k.events.catalog(), func() realtimeSettings { return *rh.Get() }, id.svc.Auth, k.provider, k.coreLog("realtime"), now)
	if err != nil {
		return err
	}
	k.events.flip = &eventsFlip{outbox: ko, hub: kr}
	return nil
}

// outboxWriter is module.Deps.Outbox for id: the zero Writer (which stages
// nothing and fails every Stage) for a kernel that built no outbox.
func (k *Kernel) outboxWriter(id module.ID) outbox.Writer {
	if k.events == nil || k.events.flip == nil {
		return outbox.Writer{}
	}
	return k.events.flip.outbox.writer(id)
}

// otherUserPredicate is the one predicate the journal's live tail applies: a
// record names another user's data when any enabled module's
// Registrar.LogOtherUserData says so. nil when no module registered one (the
// tail then flags nothing, as TailSpec documents).
func (k *Kernel) otherUserPredicate() log.OtherUserPredicate {
	var preds []log.OtherUserPredicate
	for _, e := range k.entries {
		if e.enabled && e.rec.OtherUserData != nil {
			preds = append(preds, e.rec.OtherUserData)
		}
	}
	switch len(preds) {
	case 0:
		return nil
	case 1:
		return preds[0]
	}
	return func(rec log.Record, viewer string) bool {
		for _, p := range preds {
			if p(rec, viewer) {
				return true
			}
		}
		return false
	}
}

// declareTail declares core.log.recorded.v1 with the modules' predicate. It
// must run after every module's Registrar.LogOtherUserData is recorded (P6) and
// before the bus freezes: declared earlier with nil, the tail never flags
// another user's line (seams.d/F6-05.md).
func (k *Kernel) declareTail() []module.Problem {
	return k.events.declareCore([]events.TopicSpec{logjournal.TailSpec(k.otherUserPredicate())})
}

// hubSubscriptions are the hub's raw subscriptions, recorded as core's own.
// Run after every Declare (the hub reads the catalog once) and before Freeze.
func (k *Kernel) hubSubscriptions() []module.Problem {
	subs, probs := k.events.flip.hub.subscriptions()
	if len(probs) > 0 {
		return probs
	}
	return k.events.subscribe("", subs)
}

// outboxConsume declares every manifest's consumer names (enabled or not, so a
// disabled module's messages are retained), then registers the enabled
// modules' handlers. Freeze is separate (freezeOutbox): it follows the bus's.
func (k *Kernel) outboxConsume() []module.Problem {
	ko := k.events.flip.outbox
	names := make(map[module.ID][]string, len(k.entries))
	for _, e := range k.entries {
		if len(e.man.OutboxConsumers) > 0 {
			names[e.man.ID] = e.man.OutboxConsumers
		}
	}
	probs := ko.declareConsumers(names)
	for _, e := range k.entries {
		if e.enabled && len(e.rec.OutboxHandlers) > 0 {
			probs = append(probs, ko.register(e.man.ID, e.rec.OutboxHandlers)...)
		}
	}
	return probs
}

// freezeOutbox closes the outbox's registration, after the bus froze and every
// handler registered: the relay reads the registered set when it starts.
func (k *Kernel) freezeOutbox() []module.Problem { return k.events.flip.outbox.freeze() }

// eventsTaskDefinitions are the core task kinds of the flip: the outbox relay.
// registerCoreKinds registers them with core's own kinds when the flip is
// built, even with the tasks switch (app.Options.TasksBoot) off, so
// registering the relay is what makes the start rule hold (tasks.go
// tasksNeeded) for an app that declared the identity flip.
func (k *Kernel) eventsTaskDefinitions() []tasks.Definition {
	if k.events == nil || k.events.flip == nil {
		return nil
	}
	return []tasks.Definition{k.events.flip.outbox.ob.RelayDefinition()}
}

// eventsPublisher is executor.Options.Events: the bus the executor publishes
// core.task.* on. A true nil (not a typed one) when the app has no bus, which
// the executor reads as "publish nothing".
func (k *Kernel) eventsPublisher() events.Publisher {
	if k.events == nil || k.events.bus == nil {
		return nil
	}
	return k.events.bus
}

// bindOutboxRuns hands the outbox the executor it starts runs through, once the
// executor is built. Staging needs none; the relay's nudge and the consumers
// that start runs do.
func (k *Kernel) bindOutboxRuns(x tasks.Executor) {
	if k.events == nil || k.events.flip == nil {
		return
	}
	k.events.flip.outbox.ob.BindRuns(x)
}

// outboxStartProblems is P7's check of every registered ConsumeIntoTask: its
// kind is a registered definition that the consumer's owner module's system
// actor may start from the outbox (executor.CheckOutboxStart: the same rule
// Executor.StartIn applies, Perms.OutboxSystemStart included). Without it the
// failure would come at the first message, as ErrNotPermitted or ErrUnknownKind,
// neither permanent, and the message would be retried and then dead-lettered:
// a path-update follow-up the books write already committed would be lost. A
// consumer whose kind no enabled module registered fails the same way, so a
// build that wires the consumer before its kind (the module's Registrar.Tasks)
// does not boot.
func (k *Kernel) outboxStartProblems(x tasks.Executor) []module.Problem {
	if k.events == nil || k.events.flip == nil {
		return nil
	}
	var probs []module.Problem
	for _, c := range k.events.flip.outbox.ob.TaskConsumers() {
		if err := executor.CheckOutboxStart(x, c.Owner, c.Kind); err != nil {
			probs = append(probs, module.NewProblem(module.ID(c.Owner), module.ProblemInvalidRegistration,
				fmt.Errorf("outbox consumer %s starts %s, which its owner's system actor may not start from the outbox (its messages would dead-letter): %w", c.Name, c.Kind, err)))
		}
	}
	return probs
}

// startOutboxNudger starts the goroutine that starts the relay when a Stage
// nudges and none runs. It runs with the executor (startTasks), after P7's
// freeze, so the relay never sees fewer consumers than the build has.
func (k *Kernel) startOutboxNudger(ctx context.Context) {
	if k.events == nil || k.events.flip == nil {
		return
	}
	k.events.flip.outbox.startNudger(ctx)
}

// stopOutboxNudger stops the nudger before the executor drains.
func (k *Kernel) stopOutboxNudger(ctx context.Context) error {
	if k.events == nil || k.events.flip == nil {
		return nil
	}
	return k.events.flip.outbox.stopNudger(ctx)
}

// startTail starts the journal's live tail on the open bus, with the journal's
// own settings (seams.d/F6-05.md: after Freeze and Open, once). A kernel with a
// Config.Logger instead of a pipeline has no log tap to register and serves no
// tail.
func (k *Kernel) startTail() error {
	if k.events == nil || k.events.flip == nil || k.logging == nil || k.cfgBoot == nil {
		return nil
	}
	h, ok := handleOf[logjournal.Settings](k.cfgBoot.handles)
	if !ok {
		return errors.New("kernel: events flip: the core.log.journal section is not registered")
	}
	t, err := logjournal.StartTail(k.logging, k.events.bus, *h.Get())
	if err != nil {
		return fmt.Errorf("kernel: events flip: journal tail: %w", err)
	}
	k.events.flip.mu.Lock()
	k.events.flip.tail = t
	k.events.flip.mu.Unlock()
	return nil
}

// stopTail removes the tail's tap. It runs before the bus closes, so no record
// is offered to a closed bus. Idempotent.
func (k *Kernel) stopTail() {
	f := k.flip()
	if f == nil {
		return
	}
	f.mu.Lock()
	t := f.tail
	f.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// startRealtime runs the hub as a lifecycle goroutine, on a context of its own
// that stopRealtime ends: the hub's Run returns when its context ends, after
// closing every stream within its own budget. A second call does nothing.
func (k *Kernel) startRealtime() {
	f := k.flip()
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hubCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	f.hubCancel, f.hubDone = cancel, done
	go func() { done <- f.hub.run(ctx) }()
}

// stopRealtime ends the hub's Run and waits for it, bounded by ctx. Safe to
// call twice and before startRealtime.
func (k *Kernel) stopRealtime(ctx context.Context) error {
	f := k.flip()
	if f == nil {
		return nil
	}
	f.mu.Lock()
	cancel, done := f.hubCancel, f.hubDone
	f.hubCancel, f.hubDone = nil, nil
	f.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("kernel: realtime hub did not stop: %w", ctx.Err())
	}
}

// flip is the events flip's state, nil for a kernel that built none.
func (k *Kernel) flip() *eventsFlip {
	if k.events == nil {
		return nil
	}
	return k.events.flip
}

// shutdownHooks are ListenOptions.OnShutdown: closing the hub tells every open
// stream closed{shutdown}, so the listener's drain is not held open by them.
func (k *Kernel) shutdownHooks() []func() {
	if k.events == nil || k.events.flip == nil {
		return nil
	}
	return []func(){k.events.flip.hub.closeOnShutdown(realtimeShutdownSlice)}
}

// mountEventsRoutes registers the flip's routes on r's CoreGroup: the stream
// (GET /api/core/v1/events, eventhttp) and the outbox operator routes
// (outboxhttp). It runs after mountIdentityRoutes, in the same step, so both
// take the stages 12 and 13 mountIdentity set. Every state change the operator
// routes make is audited through the boot's audit store, and refused (503) when
// it cannot be.
func (k *Kernel) mountEventsRoutes() error {
	f := k.flip()
	if f == nil || k.audit == nil {
		return errors.New("kernel: events routes: the events flip and the audit store are required")
	}
	return mountEventsRoutes(k.router, f.hub.hub, f.outbox.ob, k.events.catalog(), k.audit, k.coreLog("outboxhttp"))
}

// describeEventsRoutes is Describe's counterpart of mountEventsRoutes: the same
// routes over inputs that cannot serve, so the OpenAPI document carries them.
// Nothing here is ever called.
func (k *Kernel) describeEventsRoutes() error {
	return mountEventsRoutes(k.router, new(realtime.Hub), new(outbox.Outbox), nil, new(audit.Store), nil)
}

func mountEventsRoutes(r *httpapi.Router, hub *realtime.Hub, ob outboxhttp.Operator, cat events.Catalog, rec audit.Logger, l *slog.Logger) error {
	g := r.CoreGroup()
	if err := eventhttp.Register(g, eventhttp.Deps{Hub: hub}); err != nil {
		return fmt.Errorf("kernel: events routes: eventhttp: %w", err)
	}
	outboxhttp.Register(g, ob, cat, rec, outboxhttp.WithLogger(l))
	return nil
}

// taskViewRule is the View requirement of every registered task kind, as the
// executor's mayView reads it, for the Visible of the run-carrying task topics
// (tasks.WithViewRule). It is built from the registrations at declare time, so
// a kind of a disabled module is not in it and takes core:tasks.view, the same
// default Client.Get applies to a run whose definition is not loaded.
func (k *Kernel) taskViewRule() func(tasks.Kind) (rbac.Requirement, bool) {
	defs := k.taskDefinitions()
	view := make(map[tasks.Kind]rbac.Requirement, len(defs))
	for _, d := range defs {
		view[d.Kind()] = tasks.CommonOf(d).Perms.View
	}
	return func(kind tasks.Kind) (rbac.Requirement, bool) {
		r, ok := view[kind]
		return r, ok
	}
}
