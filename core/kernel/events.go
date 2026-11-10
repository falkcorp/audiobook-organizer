// file: core/kernel/events.go
// version: 1.0.0
// guid: ee756d64-c170-47a6-bd01-d4954cdaee38
// last-edited: 2026-11-10

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
)

// kernelEvents is the App's one events.Bus and its catalog (05-events §5E.6;
// 03 R5: a Kernel field, never a package global), with the kernel's side of
// the bus lifecycle. It is dark: nothing in boot.go or kernel.go builds one
// until F4-08 wires Registrar.Topics, Registrar.Subscribe and Deps.Events
// (06 §6.4.10 F4 row; 05-events §5E.13 Order (1)). The order F4-08 follows:
//
//   - P5: newKernelEvents, with core.events.queue_size and
//     core.events.close_drain from the core section (F2 declares it dark);
//   - P7: declareCore, then declare for every enabled module and
//     declareDisabled for every disabled one (all topics first), then
//     subscribe for core and each enabled module, then freeze. Each step
//     returns module problems, so a bad catalog entry or subscription fails
//     boot with the rest of P7's problems;
//   - P5/P6, building Deps: scope(id) is module.Deps.Events;
//   - Run step (4), after module hooks and workers start and before the
//     executor: open (06 §6.5.2);
//   - stop, after the executor drain and before workers and hooks stop:
//     close, bounded by min(core.events.close_drain, the remaining budget).
type kernelEvents struct {
	bus        *events.Bus
	log        *slog.Logger
	closeDrain time.Duration
}

// defaultCloseDrain is core.events.close_drain's default (05-events §5E.9).
const defaultCloseDrain = 2 * time.Second

// newKernelEvents builds the bus over the core metrics scope
// <namespace>.events. queueSize 0 and closeDrain 0 take the defaults (256,
// 2 s). origin reads a publish's actor and task run from its context; nil
// until the owners of those values (auth at F4-08, tasks at F5) land.
func newKernelEvents(p *metrics.Provider, l *slog.Logger, queueSize int, closeDrain time.Duration,
	origin func(ctx context.Context) (actor, runID string)) (*kernelEvents, error) {
	sc, err := p.Core("events", "topic", "subscriber", "reason")
	if err != nil {
		return nil, fmt.Errorf("kernel: events metrics scope: %w", err)
	}
	bus, err := events.NewBus(events.BusOptions{Logger: l, Metrics: sc, QueueSize: queueSize, Origin: origin})
	if err != nil {
		return nil, fmt.Errorf("kernel: events bus: %w", err)
	}
	if closeDrain <= 0 {
		closeDrain = defaultCloseDrain
	}
	return &kernelEvents{bus: bus, log: l, closeDrain: closeDrain}, nil
}

// coreTopics are the catalog entries core declares. Each is registered only
// when its owner lands (05-events §5E.3.1 "Core topics"): core.rbac.changed.v1
// with F4-08, the core.task.* topics with the tasks flip (F5), the backup,
// integration-health and auth topics with their owning rows. None exists yet.
func coreTopics() []events.TopicSpec { return nil }

// declareCore declares core's topics; a refusal is a core problem (Module "").
func (ke *kernelEvents) declareCore(topics []events.TopicSpec) []module.Problem {
	return eventProblems("", ke.bus.Declare("core", topics...))
}

// declare declares one enabled module's Registrar.Topics.
func (ke *kernelEvents) declare(id module.ID, topics []events.TopicSpec) []module.Problem {
	return eventProblems(id, ke.bus.Declare(string(id), topics...))
}

// declareDisabled records a disabled module (G1): raw subscriptions to its
// topics boot with a warning and receive nothing.
func (ke *kernelEvents) declareDisabled(id module.ID) { ke.bus.DeclareDisabled(string(id)) }

// subscribe records one module's Registrar.Subscribe (or core's, id ""),
// after every topic is declared.
func (ke *kernelEvents) subscribe(id module.ID, subs []events.Subscription) []module.Problem {
	owner := string(id)
	if id == "" {
		owner = "core"
	}
	return eventProblems(id, ke.bus.Subscribe(owner, subs...))
}

// freeze closes the catalog and the subscription set (frozen when Register
// returns, 05-events §5E.6).
func (ke *kernelEvents) freeze() { ke.bus.Freeze() }

// scope is module.Deps.Events for id.
func (ke *kernelEvents) scope(id module.ID) events.Scope { return ke.bus.Scope(string(id)) }

// catalog is the read-only catalog notify, realtime and the topics route read.
func (ke *kernelEvents) catalog() events.Catalog { return ke.bus.Catalog() }

// open starts delivery (06 §6.5.2 Run step (4)).
func (ke *kernelEvents) open() error {
	if err := ke.bus.Open(); err != nil {
		return fmt.Errorf("kernel: events: %w", err)
	}
	return nil
}

// close drains the bus within min(core.events.close_drain, ctx's deadline):
// 05-health §5H.4 slices the shutdown budget, and ctx carries what is left.
func (ke *kernelEvents) close(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, ke.closeDrain)
	defer cancel()
	if err := ke.bus.Close(ctx); err != nil {
		return fmt.Errorf("kernel: events: %w", err)
	}
	return nil
}

// eventKinds maps the bus's registration errors to the ProblemKind naming the
// same rule; anything else is ProblemInvalidRegistration.
func eventKinds() []struct {
	err  error
	kind module.ProblemKind
} {
	return []struct {
		err  error
		kind module.ProblemKind
	}{
		{events.ErrTopicNamespace, module.ProblemTopicNamespace},
		{events.ErrForeignTopic, module.ProblemTopicNamespace},
		{events.ErrTopicUndeclared, module.ProblemTopicUndeclared},
		{events.ErrDurableTopic, module.ProblemTopicUndeclared},
		{events.ErrForeignScope, module.ProblemPermissionNamespace},
	}
}

// eventProblems turns a joined registration error into one Problem per leaf,
// each keeping its events sentinel for errors.Is.
func eventProblems(owner module.ID, err error) []module.Problem {
	var probs []module.Problem
	for _, leaf := range leaves(err) {
		kind := module.ProblemInvalidRegistration
		for _, ek := range eventKinds() {
			if errors.Is(leaf, ek.err) {
				kind = ek.kind
				break
			}
		}
		probs = append(probs, module.NewProblem(owner, kind, leaf))
	}
	return probs
}
