// file: core/kernel/identity_p5.go
// version: 0.1.0
// guid: 8b4f2c61-0d37-4e95-b6a8-3c71e9d02f54
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// This file is the identity boot's P5, P6 and P7 (06 §6.5.1): the events bus,
// rbac and auth built over the stores P4b and P4d left (buildIdentity), the
// Deps each module gets, and the P7 step that declares the topics, hands auth
// the modules' methods and commits the seed roles. Like the rest of the
// identity boot it is reached only when the app declared the flip
// (identityBootDeclared).

// buildIdentityServices is the identity boot's P5. The events bus comes first,
// because rbac's OnChange and auth's OnInvalidated publish on it; then rbac and
// auth through buildIdentity, over the P4c stores, the audit store and the
// journal component P4d bound (so no service exists before the journal does).
// It writes nothing. The services keep the journal's own Revocations: a
// revocation they journal is appended to the component runIdentityP4d started.
func (k *Kernel) buildIdentityServices(ctx context.Context) error {
	id := k.ident
	if id == nil || id.journal == nil || id.settings == nil || k.audit == nil || k.provider == nil || k.cfgBoot == nil {
		return errors.New("kernel: identity: P5 needs the stores, the audit store, the journal component (P4d), the core.auth reader and the metrics provider")
	}
	ke, err := k.buildEvents()
	if err != nil {
		return err
	}
	k.events = ke
	rbacSc, authSc, err := identityMetrics(k.provider)
	if err != nil {
		return err
	}
	binary, err := k.binaryID(ctx)
	if err != nil {
		return err
	}
	cache := 0
	if h, ok := handleOf[rbacSettings](k.cfgBoot.handles); ok {
		cache = h.Get().CacheSize
	}
	svc, err := buildIdentity(ctx, identityInputs{
		BinaryID: binary, RBACCacheSize: cache, Modules: k.identityModules(),
		Users: id.users, Roles: id.roles, Audit: k.audit, Revocations: id.journal,
		AuthOptions: k.cfg.AuthOptions, Authenticators: k.cfg.Authenticators,
		Settings:      id.settings,
		OnRoleChange:  publishRBACChanged(ke.bus),
		OnInvalidated: publishPrincipalInvalidated(ke.bus),
		// Client is httpapi's resolver, the one the routes and stage 13 use, so
		// the connection auth judges is the connection the request was throttled
		// and audited under. It is built on auth.DirectClient and only adds to it
		// (trusted proxy, address, https, link), so it is never more local than
		// the fallback auth uses when this is nil (httpapi.ClientInfoFrom).
		Client: httpapi.ClientInfoFrom,
		Reach:  k.reach, StateFiles: id.stateFiles,
		Clock:   bootClock(ctx),
		RBACLog: k.coreLog("rbac"), AuthLog: k.coreLog("auth"),
		RBACMetrics: rbacSc, AuthMetrics: authSc,
	})
	if err != nil {
		return err
	}
	id.svc = svc
	if err := k.registerAuthHealth(); err != nil {
		return err
	}
	return k.bindAccountResolver(ctx)
}

// buildEvents is the App's bus over core.events (queue size and close drain).
// Its Origin reads the actor from the request's principal; a publish with no
// principal in its context (a role change, which carries none) has none.
func (k *Kernel) buildEvents() (*kernelEvents, error) {
	var queue int
	var drain time.Duration
	if h, ok := handleOf[eventsSettings](k.cfgBoot.handles); ok {
		s := h.Get()
		queue, drain = s.QueueSize, s.CloseDrain.Std()
	}
	return newKernelEvents(k.provider, k.coreLog("events"), queue, drain, publishOrigin)
}

// publishOrigin is events.BusOptions.Origin: the publishing principal's user,
// when the context carries one. It reads no credential.
func publishOrigin(ctx context.Context) (actor, runID string) {
	if p, ok := auth.PrincipalFrom(ctx); ok && p != nil && p.User != nil {
		return string(p.User.ID), ""
	}
	return "", ""
}

// reach is auth.Deps.Reach: whether a remote caller can reach the listeners,
// from core.http. An unparsable or unknown listener is non-loopback, and a
// missing section is remote-reachable on both counts: the refusing side
// (05-authn §4.11).
func (k *Kernel) reach() auth.Reach {
	h, ok := handleOf[httpSettings](k.cfgBoot.handles)
	if !ok {
		return auth.Reach{NonLoopbackListener: true, TrustedProxy: true}
	}
	s := h.Get()
	return auth.Reach{NonLoopbackListener: !listenIsLoopback(s.Listen), TrustedProxy: len(s.TrustedProxies) > 0}
}

// listenIsLoopback reports whether a listen address is loopback only: a
// loopback IP or "localhost". An empty host (":8080"), an unspecified address
// and any other name listen beyond loopback, and so does an address that does
// not parse.
func listenIsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return a.IsLoopback()
}

// ErrAuditNamespace refuses a module's audit event whose action is outside the
// module's namespace "<ID>." (Deps.Audit, 05-rbac §3.5).
var ErrAuditNamespace = errors.New("kernel: audit action outside the module's namespace")

// moduleAudit is Deps.Audit for one module: the boot's audit store behind two
// rules. The action must be in the module's namespace "<ID>." (core.* actions
// are core's, another module's are its own), and the actor is the one the
// context carries (audit.WithActor, bound by stage 12): an Event.Actor the
// module set is dropped, so a module cannot record an event as another user.
type moduleAudit struct {
	id     module.ID
	prefix string
	inner  audit.Logger
	log    *slog.Logger
}

func newModuleAudit(id module.ID, inner audit.Logger, log *slog.Logger) audit.Logger {
	return moduleAudit{id: id, prefix: string(id) + ".", inner: inner, log: log}
}

func (m moduleAudit) scoped(e audit.Event) (audit.Event, error) {
	if !strings.HasPrefix(e.Action, m.prefix) {
		return e, fmt.Errorf("%w: %q is not in %q", ErrAuditNamespace, e.Action, m.prefix)
	}
	e.Actor = audit.Actor{}
	return e, nil
}

func (m moduleAudit) Record(ctx context.Context, e audit.Event) error {
	e, err := m.scoped(e)
	if err != nil {
		return err
	}
	return m.inner.Record(ctx, e)
}

func (m moduleAudit) RecordIn(ctx context.Context, b *storage.Batch, e audit.Event) error {
	e, err := m.scoped(e)
	if err != nil {
		return err
	}
	return m.inner.RecordIn(ctx, b, e)
}

// Emit drops an event outside the module's namespace: it returns nothing, so
// the drop is logged, by module and action only (no event payload).
func (m moduleAudit) Emit(ctx context.Context, e audit.Event) {
	e, err := m.scoped(e)
	if err != nil {
		m.log.WarnContext(ctx, "kernel: a module's audit event outside its namespace was dropped", "module", string(m.id), "action", e.Action)
		return
	}
	m.inner.Emit(ctx, e)
}

// identitiesOnly and adminOnly hide *auth.Service behind the one interface a
// module is entitled to: a type assertion on the handle reaches nothing else.
type identitiesOnly struct{ auth.Identities }
type adminOnly struct{ auth.Admin }

// errCompatNotReady refuses a compat call made before P7 handed auth the
// method.
var errCompatNotReady = errors.New("kernel: the auth compat method is not registered yet (P7)")

// lateCompat is the Deps.AuthCompat of a module that registered a method. The
// module's Register returns before P7 hands auth the method, so the handle it
// holds is bound later; until then every call is refused.
type lateCompat struct {
	mu    sync.RWMutex
	inner auth.Compat
}

func (c *lateCompat) bind(h auth.Compat) {
	c.mu.Lock()
	c.inner = h
	c.mu.Unlock()
}

func (c *lateCompat) get() (auth.Compat, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.inner == nil {
		return nil, errCompatNotReady
	}
	return c.inner, nil
}

func (c *lateCompat) VerifyPassword(ctx context.Context, username, password string, client auth.ClientInfo) (*auth.User, error) {
	h, err := c.get()
	if err != nil {
		return nil, err
	}
	return h.VerifyPassword(ctx, username, password, client)
}

func (c *lateCompat) AuthenticateKey(ctx context.Context, raw string, client auth.ClientInfo) (*auth.Principal, error) {
	h, err := c.get()
	if err != nil {
		return nil, err
	}
	return h.AuthenticateKey(ctx, raw, client)
}

func (c *lateCompat) PrincipalFor(ctx context.Context, u *auth.User, scopes []rbac.Permission, epoch int64, client auth.ClientInfo) (*auth.Principal, error) {
	h, err := c.get()
	if err != nil {
		return nil, err
	}
	return h.PrincipalFor(ctx, u, scopes, epoch, client)
}

func (c *lateCompat) CheckCredentialFor(p *auth.Principal, perm rbac.Requirement) error {
	h, err := c.get()
	if err != nil {
		return err
	}
	return h.CheckCredentialFor(p, perm)
}

// compatFor is RegistrarConfig.Compat for module id: the late handle of its
// first registered method, kept by method name until P7 binds it.
func (k *Kernel) compatFor(id module.ID) func(auth.MethodSpec) auth.Compat {
	if k.ident == nil {
		return nil
	}
	return func(spec auth.MethodSpec) auth.Compat {
		c := &lateCompat{}
		k.ident.mu.Lock()
		defer k.ident.mu.Unlock()
		if k.ident.compat == nil {
			k.ident.compat = map[auth.Method]*lateCompat{}
		}
		k.ident.compat[spec.Method] = c
		return c
	}
}

// applyIdentityDeps fills the identity handles of module id's Deps (P6): the
// read-only identity view, the actor-checked admin participant, the module's
// audit logger and its event scope, and Refresh for the module whose manifest
// declares AuthRefresh, and RegisterLifecycle for the one that declares
// AuthLifecycle. AuthCompat is not set here: it is the
// Registrar's, set when the module registers a method (compatFor). Nothing is
// set for an app that did not declare the flip, or in Describe.
func (k *Kernel) applyIdentityDeps(d *module.Deps, man module.Manifest) {
	id := man.ID
	if k.ident == nil || k.ident.svc.Auth == nil || k.events == nil {
		return
	}
	d.Auth = identitiesOnly{k.ident.svc.Auth}
	d.AuthAdmin = adminOnly{k.ident.svc.Auth}
	if man.AuthRefresh {
		d.AuthRefresh = k.ident.svc.Auth.Refresh
	}
	if man.AuthLifecycle {
		d.AuthLifecycle = k.ident.svc.Auth.RegisterLifecycle
	}
	d.Audit = newModuleAudit(id, k.audit, k.coreLog("audit"))
	d.Events = k.events.scope(id)
}

// identityConsume is the identity boot's P7, after consume checked the
// registrations. In this order: the bus's catalog (core's topics, then every
// enabled module's, a disabled module recorded), every subscription, Freeze;
// each enabled module's methods handed to auth (RegisterCompat) and the late
// handles bound; then rbac's CommitSeeds, which may publish (core.rbac.changed)
// and so runs after the bus froze. Every problem of the first two is returned
// together; a CommitSeeds error fails the boot.
func (k *Kernel) identityConsume(ctx context.Context) error {
	id := k.ident
	if id == nil || id.svc.Auth == nil || k.events == nil {
		return errors.New("kernel: identity: P7 needs the services P5 built")
	}
	var probs []module.Problem
	probs = append(probs, k.events.declareCore(coreTopics())...)
	for _, e := range k.entries {
		if e.enabled {
			probs = append(probs, k.events.declare(e.man.ID, e.rec.Topics)...)
		} else {
			k.events.declareDisabled(e.man.ID)
		}
	}
	for _, e := range k.entries {
		if e.enabled {
			probs = append(probs, k.events.subscribe(e.man.ID, e.rec.Subscriptions)...)
		}
	}
	probs = append(probs, k.registerCompat()...)
	if len(probs) > 0 {
		return joined(probs)
	}
	k.events.freeze()
	if err := id.svc.RBAC.CommitSeeds(ctx); err != nil {
		return fmt.Errorf("kernel: identity: seed roles: %w", err)
	}
	return nil
}

// registerCompat hands auth each enabled module's recorded methods and binds
// the late handle the module holds for its first one. A method auth refuses
// (not "<ID>.<name>", a second hand-off, a duplicate) is a ProblemAuthMethod:
// the Registrar checked the shape already, so this is the cross-module rule.
func (k *Kernel) registerCompat() []module.Problem {
	var probs []module.Problem
	for _, e := range k.entries {
		if !e.enabled {
			continue
		}
		for _, spec := range e.rec.AuthMethods {
			h, err := k.ident.svc.Auth.RegisterCompat(string(e.man.ID), spec)
			if err != nil {
				probs = append(probs, module.NewProblem(e.man.ID, module.ProblemAuthMethod, err))
				continue
			}
			k.ident.mu.Lock()
			late := k.ident.compat[spec.Method]
			k.ident.mu.Unlock()
			if late != nil {
				late.bind(h)
			}
		}
	}
	return probs
}

// openEvents starts the bus's delivery (06 §6.5.2 Run step 4: after module
// hooks and workers started, before the executor). A kernel without a bus does
// nothing.
func (k *Kernel) openEvents() error {
	if k.events == nil {
		return nil
	}
	return k.events.open()
}

// closeEvents drains the bus within what remains of deadline and
// core.events.close_drain (Run's stop: after the executor drained, before the
// workers and hooks stop; closeCore calls it again as a no-op). Safe to call
// twice and on a bus that never opened.
func (k *Kernel) closeEvents(ctx context.Context, deadline time.Time) error {
	if k.events == nil {
		return nil
	}
	ctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), atLeastFloor(deadline))
	defer cancel()
	return k.events.close(ctx)
}
