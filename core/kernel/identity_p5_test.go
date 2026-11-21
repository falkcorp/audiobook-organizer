// file: core/kernel/identity_p5_test.go
// version: 0.1.0
// guid: c2e9a7d4-5b18-4f03-9a6e-71d4b08e3c25
// last-edited: 2026-11-21

package kernel

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Batman names only: the Batcave's modules, roles and sessions.

// depsProbe is what the probe module kept of its boot: the Deps it was handed
// and the identity events it saw.
type depsProbe struct {
	deps     module.Deps
	sibling  module.Deps // a second module in the boot, declaring nothing
	rbacEv   chan rbacChangedPayload
	invalEv  chan principalInvalidatedPayload
	signalEv chan signalLit
}

// signalLit is the payload of the probe module's own topic.
type signalLit struct {
	Reason string `json:"reason"`
}

func signalTopic() events.Topic[signalLit] {
	return events.NewTopic[signalLit]("alfred.signal.lit.v1")
}

// probeHost is identityHost with a module that keeps the Deps it was handed and
// subscribes to both identity topics, so a boot shows what P6 gave a module and
// what reaches the bus.
func probeHost(t *testing.T, switchOn bool) (*swapHost, Config, *depsProbe) {
	t.Helper()
	return probeHostDeclaring(t, switchOn, false)
}

// probeHostDeclaring is probeHost whose probe module declares AuthRefresh in
// its manifest when authRefresh is set. A second module, "batcave", always
// joins the boot and declares nothing, so a test sees both hand-outs from one
// kernel.
func probeHostDeclaring(t *testing.T, switchOn, authRefresh bool) (*swapHost, Config, *depsProbe) {
	t.Helper()
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = switchOn
	p := &depsProbe{rbacEv: make(chan rbacChangedPayload, 8), invalEv: make(chan principalInvalidatedPayload, 8), signalEv: make(chan signalLit, 8)}
	c.Modules = []module.Module{mod("batcave", func(r *module.Registrar) error {
		p.sibling = r.Deps()
		return nil
	}), storageMod("alfred", func(r *module.Registrar) error {
		p.deps = r.Deps()
		if switchOn {
			r.Topics(signalTopic().Spec(events.TopicSpec{Permission: rbac.Need("alfred:signal")}))
			r.Subscribe(
				events.On(signalTopic(), "alfred.watch-signal", func(_ context.Context, e events.Envelope[signalLit]) { p.signalEv <- e.Payload }),
				events.On(rbacChangedTopic(), "alfred.watch-rbac", func(_ context.Context, e events.Envelope[rbacChangedPayload]) { p.rbacEv <- e.Payload }),
				events.On(principalInvalidatedTopic(), "alfred.watch-sessions", func(_ context.Context, e events.Envelope[principalInvalidatedPayload]) { p.invalEv <- e.Payload }),
			)
		}
		return nil
	}, seedStep(1, "cave")).with(func(m *module.Manifest) {
		m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}}
		m.Roles = []rbac.RoleSeed{{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{"alfred:signal"}}}
		m.ImportedRoleIDs = []rbac.RoleName{"relay-worker"}
		m.AuthRefresh = authRefresh
	})}
	return h, c, p
}

// TestIdentityP5_SwitchOnBuildsServicesAndHandsDeps: the switch-on boot builds
// rbac, auth and the bus, and a module's Deps carry the identity handles (the
// read view, the admin participant, its audit logger, its event scope); with no
// AuthMethod registered AuthCompat stays nil.
func TestIdentityP5_SwitchOnBuildsServicesAndHandsDeps(t *testing.T) {
	h, c, p := probeHost(t, true)
	k := h.mustBoot(c)
	defer h.close(k)
	if k.ident == nil || k.ident.svc.Auth == nil || k.ident.svc.RBAC == nil || k.events == nil {
		t.Fatalf("P5 built no identity services: ident=%+v events=%v", k.ident, k.events)
	}
	if p.deps.Auth == nil || p.deps.AuthAdmin == nil || p.deps.Audit == nil {
		t.Fatalf("Deps lack identity handles: Auth=%v AuthAdmin=%v Audit=%v", p.deps.Auth, p.deps.AuthAdmin, p.deps.Audit)
	}
	if p.deps.AuthCompat != nil {
		t.Errorf("a module that registered no AuthMethod got AuthCompat %T", p.deps.AuthCompat)
	}
	if _, ok := p.deps.Auth.(*auth.Service); ok {
		t.Error("Deps.Auth is the raw *auth.Service: a type assertion reaches the whole service")
	}
	if _, ok := p.deps.AuthAdmin.(*auth.Service); ok {
		t.Error("Deps.AuthAdmin is the raw *auth.Service")
	}
}

// TestIdentityP5_DepsAuditAndEventsAreTheModulesOwn: the Audit a module holds
// is the namespaced logger (a core.* action is refused before it reaches the
// store), and its Events scope publishes on its own declared topic through the
// bus P7 froze and Run opens.
func TestIdentityP5_DepsAuditAndEventsAreTheModulesOwn(t *testing.T) {
	_, p := bootOpened(t)
	err := p.deps.Audit.Record(t.Context(), audit.Event{Action: "core.user.create", Outcome: audit.Success})
	if !errors.Is(err, ErrAuditNamespace) {
		t.Errorf("Deps.Audit.Record(core.*) = %v, want ErrAuditNamespace", err)
	}
	events.Publish(p.deps.Events, t.Context(), signalTopic(), "cave", signalLit{Reason: "bat signal"})
	select {
	case got := <-p.signalEv:
		if got.Reason != "bat signal" {
			t.Errorf("delivered %+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an event published through Deps.Events was not delivered")
	}
}

// TestIdentityP5_SwitchOffBuildsNothing is the ABO-style boot: no bus, no
// services, and Deps carry none of the identity handles.
func TestIdentityP5_SwitchOffBuildsNothing(t *testing.T) {
	h, c, p := probeHost(t, false)
	k := h.mustBoot(c)
	defer h.close(k)
	if k.ident != nil || k.events != nil {
		t.Fatalf("a switch-off boot built identity: ident=%+v events=%v", k.ident, k.events)
	}
	if p.deps.Auth != nil || p.deps.AuthAdmin != nil || p.deps.Audit != nil || p.deps.AuthCompat != nil {
		t.Errorf("a switch-off module got identity Deps: %+v", p.deps)
	}
}

// TestIdentityP5_P7CommitsSeedsAndFeedsImportedRoleIDs: P7 writes the seed
// roles, including the module's un-namespaced role its manifest listed in
// ImportedRoleIDs (a Declarer not told of it refuses the role and the boot).
func TestIdentityP5_P7CommitsSeedsAndFeedsImportedRoleIDs(t *testing.T) {
	h, c, _ := probeHost(t, true)
	k := h.mustBoot(c)
	defer h.close(k)
	for _, name := range []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer, "relay-worker"} {
		if _, err := k.ident.roles.GetRole(t.Context(), name); err != nil {
			t.Errorf("seed role %s is not in the store after P7: %v", name, err)
		}
	}
}

// TestIdentityP5_ImportersRefuseAfterFreeze: once P5 built and froze rbac the
// role importer refuses: an import can only land at P4b, before the catalog
// the role cache is loaded from is fixed.
func TestIdentityP5_ImportersRefuseAfterFreeze(t *testing.T) {
	h, c, _ := probeHost(t, true)
	k := h.mustBoot(c)
	defer h.close(k)
	b := k.ident.roles.NewBatch()
	defer func() { _ = b.Close() }()
	late := &rbac.Role{Name: "custom.robin", Origin: rbac.OriginCustom, Added: []rbac.Permission{"alfred:signal"}}
	if err := k.importers().values().Roles.ImportRole(b, late); err == nil {
		t.Fatal("a role import after Freeze was accepted")
	}
}

// bootOpened boots with the switch on and opens the bus the way Run's step 4
// does.
func bootOpened(t *testing.T) (*Kernel, *depsProbe) {
	t.Helper()
	h, c, p := probeHost(t, true)
	k := h.mustBoot(c)
	t.Cleanup(func() { h.close(k) })
	if err := k.openEvents(); err != nil {
		t.Fatal(err)
	}
	return k, p
}

// TestIdentityP5_RunOpensTheBusAfterTheHooksAndClosesItOnStop: an event
// published while the module hooks start (the bus is frozen, not open) is
// delivered only after the hook has started, and Run's stop closes the bus: a
// publish after Run returned reaches no subscriber.
func TestIdentityP5_RunOpensTheBusAfterTheHooksAndClosesItOnStop(t *testing.T) {
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	rec := &recorder{}
	c.Notifier = notifier{rec}
	c.Listen = func(o httpapi.ListenOptions, _ http.Handler, _ *slog.Logger) (Server, error) {
		return &fakeServer{rec: rec, stopped: make(chan struct{})}, nil
	}
	c.Exit = func(code int) { rec.add("exit:%d", code) }
	withCore(&c, "core.http.listen", "127.0.0.1:0")
	var k *Kernel
	c.Modules = []module.Module{storageMod("alfred", func(r *module.Registrar) error {
		r.Subscribe(events.On(rbacChangedTopic(), "alfred.watch", func(context.Context, events.Envelope[rbacChangedPayload]) { rec.add("deliver") }))
		r.Hook(module.Hook{Name: "alfred.cowl", Start: func(context.Context) error {
			rec.add("start")
			k.ident.svc.RBAC.AssignmentsChanged()
			return nil
		}})
		return nil
	}, seedStep(1, "cave"))}
	k = h.mustBoot(c)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- k.Run(ctx) }()
	pollFor(t, func() bool { return rec.index("deliver") >= 0 }, "the queued event is never delivered")
	if s, d := rec.index("start"), rec.index("deliver"); s < 0 || d < s {
		t.Fatalf("order = %v: delivery began before the hook started", rec.list())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	before := len(rec.matching("deliver"))
	k.ident.svc.RBAC.AssignmentsChanged()
	time.Sleep(200 * time.Millisecond)
	if got := len(rec.matching("deliver")); got != before {
		t.Errorf("an event published after stop was delivered (%d -> %d)", before, got)
	}
}

// pollFor polls cond for up to ten seconds.
func pollFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal(msg)
}

// TestIdentityP5_OnRoleChangePublishesOnTheBus: an rbac write reaches a
// subscriber of core.rbac.changed.v1 with the new generation.
func TestIdentityP5_OnRoleChangePublishesOnTheBus(t *testing.T) {
	k, p := bootOpened(t)
	k.ident.svc.RBAC.AssignmentsChanged()
	select {
	case got := <-p.rbacEv:
		if got.Generation == 0 {
			t.Errorf("core.rbac.changed.v1 carried generation %d", got.Generation)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("an rbac change published nothing on the bus")
	}
}

// TestIdentityP5_OnInvalidatedPublishesOnTheBus: a revoking auth write reaches
// a subscriber of core.principal.invalidated.v1, keyed by the user, with the
// reason and no credential material.
func TestIdentityP5_OnInvalidatedPublishesOnTheBus(t *testing.T) {
	k, p := bootOpened(t)
	now := time.Now().UTC()
	u := &auth.User{ID: "u-bruce", Username: "bruce", Status: auth.StatusActive, CreatedAt: now, UpdatedAt: now, Version: 1}
	b := k.ident.users.NewBatch()
	if err := k.ident.users.PutUser(b, u); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(t.Context(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	issued, err := k.ident.svc.Auth.IssueSession(t.Context(), u, auth.OriginPassword, false, auth.ClientInfo{})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := k.ident.svc.Auth.PrincipalFor(t.Context(), u, auth.MethodSession, nil, u.CredentialEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.ident.svc.Auth.RevokeAllSessions(t.Context(), actor, u.ID, ""); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}
	select {
	case got := <-p.invalEv:
		if got.UserID != "u-bruce" || got.CredentialID != issued.PublicID || got.Reason == "" {
			t.Errorf("core.principal.invalidated.v1 = %+v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a revoking write published nothing on the bus")
	}
}

// TestIdentityP5_AuthCompatIsBoundAtP7: a module that registered an AuthMethod
// holds an AuthCompat from Register on, which refuses until P7 hands auth the
// method (RegisterCompat) and forwards after; one that registered none holds
// nil.
func TestIdentityP5_AuthCompatIsBoundAtP7(t *testing.T) {
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	var held, bare module.Deps
	var duringRegister auth.Compat
	c.Modules = []module.Module{
		storageMod("alfred", func(r *module.Registrar) error {
			r.AuthMethod(auth.MethodSpec{Method: "alfred.signal"})
			held = r.Deps()
			duringRegister = held.AuthCompat
			return nil
		}, seedStep(1, "cave")),
		storageMod("lucius", func(r *module.Registrar) error { bare = r.Deps(); return nil }, seedStep(1, "vault")),
	}
	k := h.mustBoot(c)
	defer h.close(k)
	if held.AuthCompat == nil || duringRegister == nil {
		t.Fatal("a module that registered an AuthMethod holds no AuthCompat")
	}
	if bare.AuthCompat != nil {
		t.Errorf("a module with no AuthMethod holds AuthCompat %T", bare.AuthCompat)
	}
	_, err := held.AuthCompat.AuthenticateKey(t.Context(), "mck_garbage", auth.ClientInfo{})
	if errors.Is(err, errCompatNotReady) {
		t.Fatal("AuthCompat is still unbound after P7")
	}
	if err == nil {
		t.Error("a garbage key authenticated")
	}
}

// recordingAudit is the audit.Logger a moduleAudit wraps: it keeps what reached
// it.
type recordingAudit struct{ got []audit.Event }

func (r *recordingAudit) Record(_ context.Context, e audit.Event) error {
	r.got = append(r.got, e)
	return nil
}
func (r *recordingAudit) RecordIn(_ context.Context, _ *storage.Batch, e audit.Event) error {
	r.got = append(r.got, e)
	return nil
}
func (r *recordingAudit) Emit(_ context.Context, e audit.Event) { r.got = append(r.got, e) }

// TestModuleAudit_RefusesAnotherNamespaceAndDropsTheActor: a module's audit
// logger passes only "<ID>." actions and never an Actor the module set.
func TestModuleAudit_RefusesAnotherNamespaceAndDropsTheActor(t *testing.T) {
	inner := &recordingAudit{}
	var logged bytes.Buffer
	l := newModuleAudit("alfred", inner, slog.New(slog.NewTextHandler(&logged, nil)))
	foreign := audit.Event{Action: "core.user.create", Outcome: audit.Success}
	if err := l.Record(t.Context(), foreign); !errors.Is(err, ErrAuditNamespace) {
		t.Errorf("Record(core.*) = %v, want ErrAuditNamespace", err)
	}
	if err := l.RecordIn(t.Context(), nil, foreign); !errors.Is(err, ErrAuditNamespace) {
		t.Errorf("RecordIn(core.*) = %v, want ErrAuditNamespace", err)
	}
	l.Emit(t.Context(), foreign)
	if err := l.Record(t.Context(), audit.Event{Action: "alfredo.thing", Outcome: audit.Success}); !errors.Is(err, ErrAuditNamespace) {
		t.Errorf("Record(alfredo.*) = %v: a prefix of the module ID is not its namespace", err)
	}
	if len(inner.got) != 0 {
		t.Fatalf("a foreign action reached the store: %+v", inner.got)
	}
	if !strings.Contains(logged.String(), "core.user.create") {
		t.Errorf("the dropped Emit left no log line: %q", logged.String())
	}
	own := audit.Event{Action: "alfred.signal", Outcome: audit.Success, Actor: audit.Actor{UserID: "u-joker"}}
	if err := l.Record(t.Context(), own); err != nil {
		t.Fatal(err)
	}
	l.Emit(t.Context(), own)
	if len(inner.got) != 2 {
		t.Fatalf("own-namespace events recorded = %d, want 2", len(inner.got))
	}
	for _, e := range inner.got {
		if e.Actor != (audit.Actor{}) {
			t.Errorf("the module's Actor reached the store: %+v", e.Actor)
		}
	}
}

// TestLateCompat_RefusesUntilBound: the handle a module holds before P7 refuses
// every call, and forwards after bind.
func TestLateCompat_RefusesUntilBound(t *testing.T) {
	c := &lateCompat{}
	if _, err := c.VerifyPassword(t.Context(), "bruce", "x", auth.ClientInfo{}); !errors.Is(err, errCompatNotReady) {
		t.Errorf("VerifyPassword before bind = %v", err)
	}
	if _, err := c.AuthenticateKey(t.Context(), "x", auth.ClientInfo{}); !errors.Is(err, errCompatNotReady) {
		t.Errorf("AuthenticateKey before bind = %v", err)
	}
	if _, err := c.PrincipalFor(t.Context(), &auth.User{}, nil, 0, auth.ClientInfo{}); !errors.Is(err, errCompatNotReady) {
		t.Errorf("PrincipalFor before bind = %v", err)
	}
	if err := c.CheckCredentialFor(&auth.Principal{}, rbac.Requirement{}); !errors.Is(err, errCompatNotReady) {
		t.Errorf("CheckCredentialFor before bind = %v", err)
	}
	f := &fakeCompat{}
	c.bind(f)
	if _, err := c.VerifyPassword(t.Context(), "bruce", "x", auth.ClientInfo{}); err != nil || f.calls != 1 {
		t.Errorf("VerifyPassword after bind: err=%v calls=%d", err, f.calls)
	}
}

type fakeCompat struct{ calls int }

func (f *fakeCompat) VerifyPassword(context.Context, string, string, auth.ClientInfo) (*auth.User, error) {
	f.calls++
	return nil, nil
}
func (f *fakeCompat) AuthenticateKey(context.Context, string, auth.ClientInfo) (*auth.Principal, error) {
	f.calls++
	return nil, nil
}
func (f *fakeCompat) PrincipalFor(context.Context, *auth.User, []rbac.Permission, int64, auth.ClientInfo) (*auth.Principal, error) {
	f.calls++
	return nil, nil
}
func (f *fakeCompat) CheckCredentialFor(*auth.Principal, rbac.Requirement) error {
	f.calls++
	return nil
}

// TestListenIsLoopback pins the reach the auth service reads when auth is off.
func TestListenIsLoopback(t *testing.T) {
	for listen, want := range map[string]bool{
		"127.0.0.1:8080": true, "[::1]:8080": true, "localhost:8080": true,
		":8080": false, "0.0.0.0:8080": false, "192.0.2.10:8080": false,
		"[::]:8080": false, "bat.example:8080": false, "": false, "garbage": false,
	} {
		if got := listenIsLoopback(listen); got != want {
			t.Errorf("listenIsLoopback(%q) = %v, want %v", listen, got, want)
		}
	}
}
