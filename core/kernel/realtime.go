// file: core/kernel/realtime.go
// version: 0.1.0
// guid: 7427b3b5-a5cf-4213-92c6-125a0ad9f645
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/realtime"
)

// kernelRealtime is the App's one realtime hub (05-realtime; 03 R5: a Kernel
// field, never a package global) with the kernel's side of its lifecycle. It
// is dark: nothing in boot.go or kernel.go builds one until F6-03 wires it,
// and it needs F4-08's bus, auth service and control topics to run. The order
// F6-03 follows:
//
//   - P5, after the events bus exists and the auth service is built:
//     newKernelRealtime over the bus's catalog, the core.realtime section's
//     live getter (as newKernelOutbox takes outbox settings) and the auth
//     service;
//   - P7: subscriptions, after EVERY Declare (core's, each enabled module's,
//     and the journal's live tail, logjournal.TailSpec, which is declared only
//     once P7 has collected the modules' other-user predicates) and before the
//     bus freezes. The hub reads the catalog once, so a topic declared after
//     this call is never streamed. The result goes to the bus as core's own
//     raw subscriptions (kernelEvents.subscribe with id ""). An undeclared
//     control topic is a core problem: F4-08 declares
//     core.principal.invalidated.v1 and core.rbac.changed.v1, and the kernel
//     never sets Options.AllowNoControlTopics, which would leave a revocation
//     or a role change to the next heartbeat;
//   - routes: internal/eventhttp.Register on CoreGroup with the hub;
//   - Run: run, as a lifecycle worker (it holds no goroutine of its own and
//     returns when its context ends, after closing every stream);
//   - listener: closeOnShutdown with the shutdown slice, registered with
//     http.Server.RegisterOnShutdown, so open streams are told closed{shutdown}
//     and Shutdown is not held open by them (seams.d/F6-02.md).
type kernelRealtime struct {
	hub *realtime.Hub
	log *slog.Logger
}

// realtimeRingFloor is the smallest resume ring the kernel builds. The
// journal's live tail (core.log.recorded.v1, F6-05) shares the one ring with
// every other realtime topic and, at info level, takes most of its slots, so a
// ring of core.realtime.ring_size's default (1024) would shrink every other
// topic's Last-Event-ID window to almost nothing (seams.d/F6-05.md). It is a
// stopgap: the section default lives in core_sections.go, which this slice
// does not edit; once that default is raised to at least this value (todo.d,
// after #4765) the floor never applies and is deleted. A configured value above
// it is used as is.
const realtimeRingFloor = 4096

// realtimeRingDefault is core.realtime.ring_size's section default
// (core_sections.go; pinned by TestCoreSections_Defaults). The floor raising
// it is expected and logged at info; raising any other value is an override
// of what the operator set, logged as a warning.
const realtimeRingDefault = 1024

// realtimeOptions maps core.realtime.* (05-realtime §8) onto the hub's
// Options. ring_size below realtimeRingFloor is raised to it; a negative value
// is passed through for the hub to refuse. max_coalesce_keys has no hub
// option (the bus's coalescer has no key bound, seams.d/F6-02.md; todo.d), so
// it is not read. AllowNoControlTopics stays false. The four live keys are
// also read through Options.Live by newKernelRealtime.
func realtimeOptions(s realtimeSettings) realtime.Options {
	ring := s.RingSize
	if ring >= 0 && ring < realtimeRingFloor {
		ring = realtimeRingFloor
	}
	return realtime.Options{
		RingSize:     ring,
		ClientBuffer: s.ClientBuffer,
		MaxClients:   s.MaxClients,
		MaxPerBucket: s.MaxPerBucket,
		Heartbeat:    time.Duration(s.Heartbeat),
		WriteTimeout: time.Duration(s.WriteTimeout),
		RetryAfter:   time.Duration(s.RetryAfter),
	}
}

// realtimeLimits is the reloadable part of core.realtime.*: max_clients,
// max_per_bucket, write_timeout and retry_after carry no reload:"restart"
// tag, so a reload must reach a running hub (05-realtime §8).
func realtimeLimits(s realtimeSettings) realtime.Limits {
	return realtime.Limits{
		MaxClients:   s.MaxClients,
		MaxPerBucket: s.MaxPerBucket,
		WriteTimeout: time.Duration(s.WriteTimeout),
		RetryAfter:   time.Duration(s.RetryAfter),
	}
}

// realtimeAuth is what the hub needs of the auth service: Revalidate on every
// heartbeat and Refresh, its Deps.Refresh. *auth.Service satisfies it.
//
// Refresh is auth's (05-authn; seams.d/F4-08-refresh.md), not the kernel's:
// only auth knows what a principal was built from (a compat key's key, the
// scopes an identity extension narrowed it to), so only auth can rebuild
// every method without widening one. The kernel passes it through as is.
type realtimeAuth interface {
	realtime.Revalidator
	Refresh(ctx context.Context, p *auth.Principal) (*auth.Principal, error)
}

var _ realtimeAuth = (*auth.Service)(nil)

// newKernelRealtime builds the hub over cat (the bus's catalog), the core
// metrics scope <namespace>.realtime, the core.realtime section read through
// settings (read now for the restart keys, and on every use for the four live
// ones, so a hot reload applies to the next stream or write) and the auth
// service a. It subscribes to nothing and starts no goroutine.
func newKernelRealtime(cat events.Catalog, settings func() realtimeSettings, a realtimeAuth,
	p *metrics.Provider, l *slog.Logger, now func() time.Time) (*kernelRealtime, error) {
	if a == nil || settings == nil {
		return nil, errors.New("kernel: realtime: the auth service and the core.realtime settings are required (build the hub after auth, F4-08)")
	}
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	s := settings()
	opt := realtimeOptions(s)
	opt.Live = func() realtime.Limits { return realtimeLimits(settings()) }
	sc, err := p.Core("realtime", "topic", "reason")
	if err != nil {
		return nil, fmt.Errorf("kernel: realtime metrics scope: %w", err)
	}
	hub, err := realtime.NewHub(opt, realtime.Deps{
		Auth: a, Catalog: cat, Log: l, Metrics: sc, Clock: now, Refresh: a.Refresh,
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: realtime: %w", err)
	}
	if opt.RingSize != s.RingSize {
		msg := "kernel: realtime: core.realtime.ring_size is below the floor the journal's live tail needs; the floor is used"
		if s.RingSize == realtimeRingDefault || s.RingSize == 0 {
			l.Info(msg, "configured", s.RingSize, "used", opt.RingSize)
		} else {
			l.Warn(msg, "configured", s.RingSize, "used", opt.RingSize)
		}
	}
	return &kernelRealtime{hub: hub, log: l}, nil
}

// subscriptions is the hub's raw subscriptions, for kernelEvents.subscribe("",
// ...) at P7 (see the type's comment for when). A refusal (an undeclared
// control topic, a second call) is a core problem, so boot fails with the
// rest of P7's problems.
func (kr *kernelRealtime) subscriptions() ([]events.Subscription, []module.Problem) {
	subs, err := kr.hub.Subscriptions()
	if err != nil {
		return nil, []module.Problem{module.NewProblem("", module.ProblemInvalidRegistration, err)}
	}
	return subs, nil
}

// run holds the hub's lifetime: it returns when ctx ends, after closing every
// stream within the hub's own budget.
func (kr *kernelRealtime) run(ctx context.Context) error {
	if err := kr.hub.Run(ctx); err != nil {
		return fmt.Errorf("kernel: realtime: %w", err)
	}
	return nil
}

// closeOnShutdown is the http.Server.RegisterOnShutdown hook: it refuses new
// streams, sends every open one core.stream.closed.v1{shutdown} and waits for
// them at most budget (RegisterOnShutdown passes no context, and the server
// runs the hook on its own goroutine). Close is idempotent, so run's own close
// after this one is harmless.
func (kr *kernelRealtime) closeOnShutdown(budget time.Duration) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		if err := kr.hub.Close(ctx); err != nil {
			kr.log.Warn("kernel: realtime: streams still open when the shutdown slice ended", "budget", budget, "err", err)
		}
	}
}
