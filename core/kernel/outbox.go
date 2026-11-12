// file: core/kernel/outbox.go
// version: 1.0.0
// guid: 61c0d9a4-2e7b-4b83-9f15-d3a8e0c47b92
// last-edited: 2026-11-12

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/health"
	"github.com/falkcorp/mediacore/internal/outboxstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/outbox"
	"github.com/falkcorp/mediacore/storage"
)

// kernelOutbox is the App's one outbox.Outbox (05-events §5E.4 to §5E.13; 03
// R5: a Kernel field, never a package global) with the kernel's side of its
// lifecycle. It is dark: nothing in boot.go or kernel.go builds one until F6-03
// adds the storage owner and wires Manifest.OutboxConsumers,
// Registrar.OutboxHandler and Deps.Outbox. The order they follow:
//
//   - P2/P4a: storageOwners appends outboxOwnerSpec(), so the c/outbox/
//     keyspace exists once Prepare returns;
//   - P5, after the metrics Provider and the health registry exist and after
//     the events bus froze its catalog (the outbox reads the Durable entries):
//     newKernelOutbox; the core.outbox health component is registered from
//     outboxHealthCheck();
//   - P7: declareConsumers with every manifest's OutboxConsumers (enabled or
//     not), then register for core and each enabled module's handlers, then
//     freeze; each step returns module problems, so a bad registration fails
//     boot with the rest of P7's problems;
//   - building Deps: writer(id) is the module's staging handle (Deps.Outbox);
//   - Run, once the executor exists: ko.ob.BindRuns(executor) (staging needs no
//     executor; the relay and the consumers that start runs do), the executor
//     registers ko.ob.RelayDefinition() (no wrapper here: tasklint's
//     unregistered-kind rule reads a wrapper nobody calls as an orphan kind),
//     then startNudger;
//     the relay's own schedule tick starts the first run (Every(1m).OnStart());
//   - stop: stopNudger before the executor drains; the relay run is the
//     executor's to stop, and an unfinished batch is simply not committed.
type kernelOutbox struct {
	ob  *outbox.Outbox
	log *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// outboxOwnerID is core/outbox's owner in the storage claim ledger: the owner
// of the c/outbox/ keyspace (05-events §5E.5.2, 05-storage §5S.3.1).
const outboxOwnerID = outboxstore.Owner

// outboxOwnerSpec is the storage claim of core/outbox: the c/outbox/ keyspace
// with seq/, cur/ and dead/ Serialized and nothing else (no claims, no migrations).
// Registering a new owner takes a one-time write in the claim ledger at P4b:
// that belongs to the flip (F6-03) that appends this spec to storageOwners.
func outboxOwnerSpec() storage.OwnerSpec {
	return storage.OwnerSpec{Owner: outboxOwnerID, Spec: outboxstore.StorageSpec(), Enabled: true}
}

// coreOutboxConsumers is the static list of core's consumer names (05-events
// §5E.4): the only names a core consumer may take. core.notify.deliver is the
// notify service's delivery consumer (05-notify); a later core consumer is one
// more name here, in the same change that registers it.
func coreOutboxConsumers() []string { return []string{"core.notify.deliver"} }

// outboxHealthCheck is the core.outbox component: push-only, neither Critical
// nor Readiness (a stuck outbox degrades the App; it does not take it out of
// rotation), Degraded while a dead letter exists, while the relay is turned
// off, and while delivery is stopped on a row of a newer build.
func outboxHealthCheck() health.Check {
	return health.Check{ID: outbox.ComponentID, Title: "Outbox"}
}

// outboxConfig reads core.outbox.* (05-events §5E.9) into the outbox's Config.
func outboxConfig(s outboxSettings) outbox.Config {
	return outbox.Config{
		MaxAttempts:          s.MaxAttempts,
		BackoffMin:           time.Duration(s.BackoffMin),
		BackoffMax:           time.Duration(s.BackoffMax),
		RelayWorkers:         s.RelayWorkers,
		RelayRotate:          time.Duration(s.RelayRotate),
		RelayEnabled:         s.RelayEnabled,
		HandlerTimeout:       time.Duration(s.HandlerTimeout),
		IntakePoll:           time.Duration(s.IntakePoll),
		UnorderedParallelism: s.UnorderedParallelism,
		OrphanRetention:      time.Duration(s.OrphanRetention),
	}
}

// newKernelOutbox builds the outbox over db's c/outbox/ keyspace and the core
// metrics scope <namespace>.outbox. settings is read each time the outbox
// needs a value, so a hot reload takes effect (relay_rotate and the relay
// definition's Timeout are read once, as 05-events §5E.9 says). rep is the
// reporter the registry returned for outboxHealthCheck. It writes nothing.
// A missing keyspace means outboxOwnerSpec was not among the owners the store
// opened with, which is F6-03's wiring and is reported by name.
func newKernelOutbox(db *storage.DB, p *metrics.Provider, cat events.Catalog, settings func() outboxSettings,
	rep health.Reporter, l *slog.Logger, now func() time.Time) (*kernelOutbox, error) {
	if db == nil {
		return nil, errors.New("kernel: outbox: no storage handle (build the outbox after P4a)")
	}
	ks, ok := db.Keyspace(outboxOwnerID)
	if !ok {
		return nil, fmt.Errorf("kernel: outbox: owner %s has no keyspace: append outboxOwnerSpec() to storageOwners (F6-03)", outboxOwnerID)
	}
	sc, err := p.Core("outbox", "topic", "consumer", "state")
	if err != nil {
		return nil, fmt.Errorf("kernel: outbox metrics scope: %w", err)
	}
	inst, err := outbox.NewInstruments(sc)
	if err != nil {
		return nil, fmt.Errorf("kernel: outbox metrics: %w", err)
	}
	var cfg func() outbox.Config
	if settings != nil {
		cfg = func() outbox.Config { return outboxConfig(settings()) }
	}
	ob, err := outbox.New(outbox.Options{
		Keyspace: ks, Catalog: cat, Config: cfg, CoreConsumers: coreOutboxConsumers(),
		Log: l, Now: now, Health: rep, Instruments: inst,
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: outbox: %w", err)
	}
	return &kernelOutbox{ob: ob, log: l}, nil
}

// outboxKinds maps the outbox's registration errors to the ProblemKind naming
// the same rule; anything else is ProblemInvalidRegistration.
func outboxKinds() []struct {
	err  error
	kind module.ProblemKind
} {
	return []struct {
		err  error
		kind module.ProblemKind
	}{
		{outbox.ErrConsumerUndeclared, module.ProblemOutboxConsumerUndeclared},
		{outbox.ErrConsumerName, module.ProblemOutboxConsumerUndeclared},
		{outbox.ErrUnknownTopic, module.ProblemTopicUndeclared},
	}
}

// outboxProblems turns a joined registration error into one Problem per leaf,
// each keeping its outbox sentinel for errors.Is.
func outboxProblems(owner module.ID, err error) []module.Problem {
	var probs []module.Problem
	for _, leaf := range leaves(err) {
		kind := module.ProblemInvalidRegistration
		for _, ok := range outboxKinds() {
			if errors.Is(leaf, ok.err) {
				kind = ok.kind
				break
			}
		}
		probs = append(probs, module.NewProblem(owner, kind, leaf))
	}
	return probs
}

// declareConsumers records every manifest's OutboxConsumers, enabled or not,
// before any registration: a disabled module's consumer names stay known, so
// its messages are kept (05-events §5E.5.2). consumers maps a module to its
// manifest's names.
func (ko *kernelOutbox) declareConsumers(consumers map[module.ID][]string) []module.Problem {
	var probs []module.Problem
	for id, names := range consumers {
		probs = append(probs, outboxProblems(id, ko.ob.DeclareConsumers(string(id), names...))...)
	}
	return probs
}

// register records one module's handlers (core's with id ""), after every
// consumer name is declared.
func (ko *kernelOutbox) register(id module.ID, hs []outbox.Handler) []module.Problem {
	owner := string(id)
	if id == "" {
		owner = "core"
	}
	return outboxProblems(id, ko.ob.Register(owner, hs...))
}

// freeze closes registration (frozen when Register returns, 05-events §5E.6)
// and refuses a durable topic whose partition count the key layout cannot hold.
func (ko *kernelOutbox) freeze() []module.Problem { return outboxProblems("", ko.ob.Freeze()) }

// writer is module.Deps.Outbox for id: the staging handle that stamps id as the
// producer and refuses a topic id does not own.
func (ko *kernelOutbox) writer(id module.ID) outbox.Writer { return ko.ob.Writer(string(id)) }

// startNudger launches the one goroutine that starts the relay when a Stage
// nudges and none runs. ctx is the App's lifecycle context. A second call is
// a no-op while the first runs.
func (ko *kernelOutbox) startNudger(ctx context.Context) {
	ko.mu.Lock()
	defer ko.mu.Unlock()
	if ko.cancel != nil {
		return
	}
	ctx, ko.cancel = context.WithCancel(ctx)
	ko.done = make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		ko.ob.RunNudger(ctx)
	}(ko.done)
}

// stopNudger ends the nudger and waits for it, bounded by ctx.
func (ko *kernelOutbox) stopNudger(ctx context.Context) error {
	ko.mu.Lock()
	cancel, done := ko.cancel, ko.done
	ko.cancel, ko.done = nil, nil
	ko.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("kernel: outbox nudger did not stop: %w", ctx.Err())
	}
}
