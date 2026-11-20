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
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/realtime"
)

// kernelRealtime is the App's one realtime hub (05-realtime; 03 R5: a Kernel
// field, never a package global) with the kernel's side of its lifecycle. It
// is dark: nothing in boot.go or kernel.go builds one until F6-03 wires it,
// and it needs F4-08's bus, auth service and control topics to run. The order
// F6-03 follows:
//
//   - P5, after the events bus exists and the auth service is built:
//     newKernelRealtime over the bus's catalog, core.realtime.* from the core
//     section, the auth service and its store;
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
// topic's Last-Event-ID window to almost nothing (seams.d/F6-05.md). The floor
// is applied here because the section default lives in core_sections.go; once
// that default is raised to at least this value, the floor never applies and
// can be deleted. A configured value above it is used as is.
const realtimeRingFloor = 4096

// realtimeOptions maps core.realtime.* (05-realtime §8) onto the hub's
// Options. ring_size below realtimeRingFloor is raised to it; a negative value
// is passed through for the hub to refuse. max_coalesce_keys has no hub
// option (the bus's coalescer has no key bound, seams.d/F6-02.md), so it is
// not read. AllowNoControlTopics stays false.
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

// realtimeAuth is what the hub needs of the auth service: Revalidate on every
// heartbeat, and PrincipalFor for refreshPrincipal. *auth.Service satisfies it.
type realtimeAuth interface {
	realtime.Revalidator
	PrincipalFor(ctx context.Context, u *auth.User, m auth.Method, scopes []rbac.Permission, epoch int64) (*auth.Principal, error)
}

// realtimeCredentials is the two point reads refreshPrincipal makes: the
// stream's user and, for a key principal, the key. *authstore.Store (auth's
// store) satisfies it.
type realtimeCredentials interface {
	GetUser(ctx context.Context, id auth.UserID) (*auth.User, error)
	APIKeyByID(ctx context.Context, id string) (*auth.APIKey, error)
}

var (
	_ realtimeAuth        = (*auth.Service)(nil)
	_ realtimeCredentials = (*authstore.Store)(nil)
)

// errRefreshMethod is refreshPrincipal's answer for a principal whose method
// it cannot rebuild without widening it (see refreshPrincipal).
var errRefreshMethod = errors.New("kernel: realtime: a principal of this method cannot be refreshed")

// refreshPrincipal is the hub's Deps.Refresh (05-realtime §4.6;
// seams.d/F6-02.md): it rebuilds p's rights from the user and the credential
// as the store holds them now, so a role change reaches an open stream on its
// next heartbeat. It answers for the SAME credential: the result carries p's
// Method and CredentialID, and only its rights are used by the hub.
//
//   - A session principal (MethodSession, MethodSessionDelegated) is rebuilt
//     over the user's roles.
//   - A core key principal (MethodAPIKey) is rebuilt over the stored key's own
//     scopes, never over its owner's roles: a key the user no longer owns or
//     that is not active is a definite refusal.
//   - The auth-off principal (MethodAuthDisabled, no user) is returned as is:
//     its rights are fixed by the mode, and Revalidate ends it when the mode
//     changes.
//   - Any other method (a module's compat surface) is refused definitely
//     (errRefreshMethod), which ends its stream credential_revoked: a compat
//     key principal keeps its key in a field auth does not export, so from
//     here it cannot be told from a compat session, and rebuilding it over
//     the owner's roles would show the stream more than the key may.
//
// Store failures keep their auth.ErrStoreUnavailable (the hub retries them on
// the next heartbeat and withholds events meanwhile); a missing or undecodable
// record, and every PrincipalFor refusal, is definite. It holds no state, so
// concurrent calls (one per open stream per heartbeat, MaxClients/Heartbeat a
// second at most) are safe; each costs one or two point reads.
func refreshPrincipal(a realtimeAuth, creds realtimeCredentials) func(context.Context, *auth.Principal) (*auth.Principal, error) {
	return func(ctx context.Context, p *auth.Principal) (*auth.Principal, error) {
		if p == nil {
			return nil, fmt.Errorf("%w: no principal", auth.ErrInvalidCredentials)
		}
		if p.Method == auth.MethodAuthDisabled && p.User == nil {
			return p, nil
		}
		if p.User == nil || p.User.ID == "" {
			return nil, fmt.Errorf("%w: a %q principal with no user", auth.ErrInvalidCredentials, p.Method)
		}
		var scopes []rbac.Permission
		switch p.Method {
		case auth.MethodSession, auth.MethodSessionDelegated:
		case auth.MethodAPIKey:
			k, err := creds.APIKeyByID(ctx, p.CredentialID)
			if err != nil {
				return nil, refreshReadErr("key "+p.CredentialID, err)
			}
			switch {
			case k.Owner != p.User.ID:
				return nil, fmt.Errorf("%w: key %s is not %s's", auth.ErrRevoked, p.CredentialID, p.User.ID)
			case k.Status != auth.KeyActive:
				return nil, fmt.Errorf("%w: key %s is %s", auth.ErrRevoked, p.CredentialID, k.Status)
			case len(k.Scopes) == 0:
				return nil, fmt.Errorf("%w: key %s carries no scope", auth.ErrInvalidCredentials, p.CredentialID)
			}
			scopes = k.Scopes
		default:
			return nil, fmt.Errorf("%w: %q", errRefreshMethod, p.Method)
		}
		u, err := creds.GetUser(ctx, p.User.ID)
		if err != nil {
			return nil, refreshReadErr("user "+string(p.User.ID), err)
		}
		np, err := a.PrincipalFor(ctx, u, p.Method, scopes, u.CredentialEpoch)
		if err != nil {
			return nil, fmt.Errorf("kernel: realtime: refresh %s: %w", u.ID, err)
		}
		np.CredentialID = p.CredentialID
		return np, nil
	}
}

// refreshReadErr maps a point read's error the way auth's Revalidate does: a
// missing or undecodable record is definite, anything that is not already
// auth.ErrStoreUnavailable becomes it (retried, never a revocation).
func refreshReadErr(what string, err error) error {
	switch {
	case errors.Is(err, auth.ErrNotFound):
		return fmt.Errorf("%w: %s is gone: %w", auth.ErrRevoked, what, err)
	case errors.Is(err, auth.ErrUndecodableRecord), errors.Is(err, auth.ErrStoreUnavailable):
		return fmt.Errorf("kernel: realtime: read %s: %w", what, err)
	}
	return fmt.Errorf("%w: read %s: %w", auth.ErrStoreUnavailable, what, err)
}

// newKernelRealtime builds the hub over cat (the bus's catalog), the core
// metrics scope <namespace>.realtime, settings s, the auth service a and its
// store creds. It subscribes to nothing and starts no goroutine.
func newKernelRealtime(cat events.Catalog, s realtimeSettings, a realtimeAuth, creds realtimeCredentials,
	p *metrics.Provider, l *slog.Logger, now func() time.Time) (*kernelRealtime, error) {
	if a == nil || creds == nil {
		return nil, errors.New("kernel: realtime: the auth service and its store are required (build the hub after auth, F4-08)")
	}
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	sc, err := p.Core("realtime", "topic", "reason")
	if err != nil {
		return nil, fmt.Errorf("kernel: realtime metrics scope: %w", err)
	}
	hub, err := realtime.NewHub(realtimeOptions(s), realtime.Deps{
		Auth: a, Catalog: cat, Log: l, Metrics: sc, Clock: now, Refresh: refreshPrincipal(a, creds),
	})
	if err != nil {
		return nil, fmt.Errorf("kernel: realtime: %w", err)
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
