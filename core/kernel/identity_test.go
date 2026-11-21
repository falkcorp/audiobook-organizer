// file: core/kernel/identity_test.go
// version: 0.1.0
// guid: a1b7bea9-3522-42de-9bd5-51268800863e
// last-edited: 2026-11-20

package kernel

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/auth/password"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"go.opentelemetry.io/otel/attribute"
)

// idJournal is the revocation journal component as buildIdentity sees it: an
// injected value. onAppend, when set, runs once, on the first Append of the
// kind it names, inside the revoking write's batch before it commits (the
// concurrency test's window), without the journal's lock held.
type idJournal struct {
	mu    sync.Mutex
	lines []journal.Revocation

	hookKind journal.Kind
	hook     func()
	fired    bool
}

func (j *idJournal) Append(_ context.Context, b *storage.Batch, r journal.Revocation) error {
	if b == nil {
		return errors.New("Append without the revoking batch")
	}
	j.mu.Lock()
	j.lines = append(j.lines, r)
	var hook func()
	if j.hook != nil && !j.fired && r.Kind == j.hookKind {
		j.fired, hook = true, j.hook
	}
	j.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (j *idJournal) Gap(b *storage.Batch, _ time.Time) error {
	if b == nil {
		return errors.New("Gap without the revoking batch")
	}
	return nil
}

// setHook arms the one-shot hook.
func (j *idJournal) setHook(kind journal.Kind, fn func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.hookKind, j.hook, j.fired = kind, fn, false
}

// idFx is the P4c stores over one in-memory storage.DB, as a boot has them
// when P5 calls buildIdentity.
type idFx struct {
	t     *testing.T
	users *authstore.Store
	roles *rbacstore.Store
	audit *audit.Store
	jrnl  *idJournal
	now   time.Time
}

func newIDFx(t *testing.T) *idFx {
	t.Helper()
	db := bootAuditDB(t, auditOwnerSpec(),
		storage.OwnerSpec{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
		storage.OwnerSpec{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true})
	f := &idFx{t: t, jrnl: &idJournal{}, now: time.Date(2026, 11, 20, 21, 0, 0, 0, time.UTC)}
	aks, ok := db.Keyspace(authstore.Owner)
	if !ok {
		t.Fatal("no identity keyspace")
	}
	rks, ok := db.Keyspace(rbacstore.Owner)
	if !ok {
		t.Fatal("no role keyspace")
	}
	var err error
	if f.users, err = authstore.New(aks, authstore.Options{Now: f.clock}); err != nil {
		t.Fatal(err)
	}
	if f.roles, err = rbacstore.New(rks, rbacstore.Options{}); err != nil {
		t.Fatal(err)
	}
	if f.audit, err = buildAudit(context.Background(), db, nil, f.clock); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *idFx) clock() time.Time { return f.now }

// gothamModule is a fixture module's declarations: a view scope granted to
// the core seeds, a module seed role and an imported, un-namespaced one.
func gothamModule() identityModule {
	return identityModule{
		ID: "gotham",
		Permissions: []rbac.PermissionSpec{
			{Name: "gotham:patrol", Title: "Patrol", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
			{Name: "gotham:relay.worker", Title: "Relay worker", Confining: true},
		},
		Roles: []rbac.RoleSeed{
			{Name: "gotham.detective", Title: "Detective", Permissions: []rbac.Permission{"gotham:patrol"}},
			{Name: "relay-worker", Title: "Relay worker", Permissions: []rbac.Permission{"gotham:relay.worker"}},
		},
		ImportedRoleIDs: []rbac.RoleName{"relay-worker"},
	}
}

// inputs is a complete identityInputs over the fixture's stores, with the
// declared core.auth defaults and a cheap hasher.
func (f *idFx) inputs() identityInputs {
	f.t.Helper()
	def := declaredAuth(f.t)
	read, err := authSettingsReader(func() *authSettings { return def }, func(config.Path) bool { return false }, "gotham")
	if err != nil {
		f.t.Fatal(err)
	}
	return identityInputs{
		BinaryID:    "gotham-test",
		Modules:     []identityModule{gothamModule()},
		Users:       f.users,
		Roles:       f.roles,
		Audit:       f.audit,
		Revocations: f.jrnl,
		Settings:    read,
		Hasher:      password.New(password.Options{Time: 1, MemoryKiB: 64}),
		Clock:       f.clock,
	}
}

// build runs buildIdentity over in, failing the test on error.
func (f *idFx) build(in identityInputs) identityServices {
	f.t.Helper()
	svc, err := buildIdentity(context.Background(), in)
	if err != nil {
		f.t.Fatalf("buildIdentity: %v", err)
	}
	return svc
}

// putUser stores an active-or-not user holding roles behind the services.
func (f *idFx) putUser(name string, st auth.UserStatus, roles ...rbac.RoleName) *auth.User {
	f.t.Helper()
	u := &auth.User{ID: auth.UserID("u-" + name), Username: name, Roles: roles, Status: st,
		CreatedAt: f.now, UpdatedAt: f.now, Version: 1}
	f.commitUsers(func(b *storage.Batch) error { return f.users.PutUser(b, u) })
	return f.reload(u.ID)
}

// bump rewrites id's record with change applied, as another writer would.
func (f *idFx) bump(id auth.UserID, change func(*auth.User)) {
	f.t.Helper()
	u := f.reload(id)
	change(u)
	u.Version++
	f.commitUsers(func(b *storage.Batch) error { return f.users.PutUser(b, u) })
}

func (f *idFx) reload(id auth.UserID) *auth.User {
	f.t.Helper()
	u, err := f.users.GetUser(context.Background(), id)
	if err != nil {
		f.t.Fatalf("GetUser %s: %v", id, err)
	}
	return u
}

func (f *idFx) commitUsers(stage func(b *storage.Batch) error) {
	f.t.Helper()
	b := f.users.NewBatch()
	defer func() { _ = b.Close() }()
	if err := stage(b); err != nil {
		f.t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); err != nil {
		f.t.Fatal(err)
	}
}

// activeAdmins counts the active users holding a role that grants core:admin
// by its committed definition.
func (f *idFx) activeAdmins(rb *rbac.Service) int {
	f.t.Helper()
	ctx := context.Background()
	n := 0
	for u, err := range f.users.ScanUsers(ctx) {
		if err != nil {
			f.t.Fatal(err)
		}
		if u.Status != auth.StatusActive {
			continue
		}
		for _, name := range u.Roles {
			r, err := rb.Role(ctx, name)
			if err != nil {
				continue
			}
			if rbac.GrantsAdmin(r.Permissions()) {
				n++
				break
			}
		}
	}
	return n
}

// TestIdentity_BuildsOverInMemoryStores: the assembly builds rbac and auth
// over the P4c stores, declares the module, freezes, and the result serves:
// the catalog holds core's fixed scopes and the module's, P7's CommitSeeds
// writes the module's seed roles, and auth reports no users on an empty
// store.
func TestIdentity_BuildsOverInMemoryStores(t *testing.T) {
	f := newIDFx(t)
	svc := f.build(f.inputs())
	cat := svc.RBAC.Catalog()
	if cat == nil {
		t.Fatal("the rbac service is not frozen")
	}
	for _, p := range []rbac.Permission{"core:admin", "core:users.manage", "gotham:patrol", "gotham:relay.worker"} {
		if _, ok := cat.Lookup(p); !ok {
			t.Errorf("catalog lacks %s", p)
		}
	}
	if svc.Auth.HasUsers() {
		t.Error("HasUsers on an empty store")
	}
	if err := svc.RBAC.CommitSeeds(context.Background()); err != nil {
		t.Fatalf("CommitSeeds over the assembled service: %v", err)
	}
	for _, name := range []rbac.RoleName{rbac.RoleAdmin, "gotham.detective", "relay-worker"} {
		if _, err := svc.RBAC.Role(context.Background(), name); err != nil {
			t.Errorf("role %s after CommitSeeds: %v", name, err)
		}
	}
}

// TestIdentity_RefusesMissingInputs: each required input missing refuses the
// build, by name, before anything is built. The stores and the audit store
// are concrete pointers: a nil one would pass rbac's and auth's interface
// nil checks as a non-nil interface, so the kernel checks them itself.
func TestIdentity_RefusesMissingInputs(t *testing.T) {
	f := newIDFx(t)
	for name, drop := range map[string]func(*identityInputs){
		"Users":       func(in *identityInputs) { in.Users = nil },
		"Roles":       func(in *identityInputs) { in.Roles = nil },
		"Audit":       func(in *identityInputs) { in.Audit = nil },
		"Revocations": func(in *identityInputs) { in.Revocations = nil },
		"Settings":    func(in *identityInputs) { in.Settings = nil },
	} {
		in := f.inputs()
		drop(&in)
		svc, err := buildIdentity(context.Background(), in)
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("without %s: %v, want a refusal naming it", name, err)
		}
		if svc.RBAC != nil || svc.Auth != nil {
			t.Errorf("without %s: services returned with the error", name)
		}
	}
}

// TestIdentity_DeclarationProblemFailsTheBuild: a module that declares a
// scope outside its namespace refuses the build; nothing is returned.
func TestIdentity_DeclarationProblemFailsTheBuild(t *testing.T) {
	f := newIDFx(t)
	in := f.inputs()
	bad := gothamModule()
	bad.Permissions = append(bad.Permissions, rbac.PermissionSpec{Name: "arkham:cells", Title: "Cells"})
	in.Modules = []identityModule{bad}
	svc, err := buildIdentity(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "gotham") {
		t.Fatalf("a foreign-namespace scope: %v, want a declaration refusal naming the module", err)
	}
	if svc.RBAC != nil || svc.Auth != nil {
		t.Error("services returned with the error")
	}
	// A seed role the manifest does not import is refused too.
	in = f.inputs()
	noImport := gothamModule()
	noImport.ImportedRoleIDs = nil
	in.Modules = []identityModule{noImport}
	if _, err := buildIdentity(context.Background(), in); err == nil {
		t.Error("an un-namespaced seed role without ImportedRoleIDs built")
	}
}

// TestIdentity_SettingsReachTheService: the Settings input is what auth.New
// reads. A core.auth.mode no mode matches refuses the build (auth's
// ErrBadInput), so a build that dropped the reader would pass it.
func TestIdentity_SettingsReachTheService(t *testing.T) {
	f := newIDFx(t)
	in := f.inputs()
	var err error
	bogus := declaredAuth(t)
	bogus.Mode = "bogus"
	in.Settings, err = authSettingsReader(func() *authSettings { return bogus }, func(config.Path) bool { return false }, "gotham")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildIdentity(context.Background(), in); !errors.Is(err, auth.ErrBadInput) {
		t.Fatalf("mode bogus: %v, want auth.ErrBadInput", err)
	}
}

// TestIdentity_DisabledAllowRemoteFollowsProvenance: with core.auth.mode
// disabled on a remote-reachable install, the declared default of
// disabled_allow_remote leaves auth's Options.DefaultDisabledAllowRemote in
// charge (ABO's true builds), while an operator false refuses even then.
func TestIdentity_DisabledAllowRemoteFollowsProvenance(t *testing.T) {
	f := newIDFx(t)
	off := declaredAuth(t)
	off.Mode = "disabled"
	reader := func(explicitRemote bool) func() auth.Settings {
		r, err := authSettingsReader(func() *authSettings { return off }, func(p config.Path) bool { return explicitRemote && p == authDisabledAllowRemoteKey }, "gotham")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	remote := func() auth.Reach { return auth.Reach{NonLoopbackListener: true} }

	in := f.inputs()
	in.Settings, in.Reach = reader(false), remote
	in.AuthOptions.DefaultDisabledAllowRemote = true
	if _, err := buildIdentity(context.Background(), in); err != nil {
		t.Errorf("declared default, app default true: %v, want auth off to build", err)
	}
	in.Settings = reader(true) // an operator's explicit false
	if _, err := buildIdentity(context.Background(), in); !errors.Is(err, auth.ErrAuthDisabledRemote) {
		t.Errorf("explicit false, app default true: %v, want ErrAuthDisabledRemote", err)
	}
	in.Settings = reader(false)
	in.AuthOptions.DefaultDisabledAllowRemote = false
	if _, err := buildIdentity(context.Background(), in); !errors.Is(err, auth.ErrAuthDisabledRemote) {
		t.Errorf("declared default, app default false: %v, want ErrAuthDisabledRemote", err)
	}
}

// idOracle is an identity extension asserting a fixed assertion.
type idOracle struct{ a *auth.Assertion }

func (idOracle) Name() string                                          { return "oracle" }
func (o idOracle) Authenticate(*http.Request) (*auth.Assertion, error) { return o.a, nil }

// TestIdentity_ExtensionMaxAgeReachesStage12: core.auth.extension_max_age,
// through the reader and the assembled service, bounds an extension's
// principal that asserts no expiry (seams.d/F4-08-refresh.md "Not yet
// wired"). Signed in three hours ago: a configured 2h refuses it as expired;
// zero and a negative value take auth's 24h default and bind it; a
// configured 30h binds one signed in 25h ago, which the default would refuse.
func TestIdentity_ExtensionMaxAgeReachesStage12(t *testing.T) {
	f := newIDFx(t)
	oracleUser := f.putUser("oracle", auth.StatusActive, rbac.RoleViewer)
	def := declaredAuth(t)
	for _, c := range []struct {
		name    string
		maxAge  time.Duration
		ago     time.Duration
		expired bool
	}{
		{"configured 2h, 3h ago", 2 * time.Hour, 3 * time.Hour, true},
		{"zero takes the 24h default", 0, 3 * time.Hour, false},
		{"negative takes the 24h default", -5 * time.Hour, 3 * time.Hour, false},
		{"zero, 25h ago, past the default", 0, 25 * time.Hour, true},
		{"configured 30h, 25h ago", 30 * time.Hour, 25 * time.Hour, false},
	} {
		in := f.inputs()
		var err error
		s := *def
		s.ExtensionMaxAge = dur(c.maxAge)
		in.Settings, err = authSettingsReader(func() *authSettings { return &s }, func(config.Path) bool { return false }, "gotham")
		if err != nil {
			t.Fatal(err)
		}
		in.Authenticators = []auth.Extension{{
			Authenticator: idOracle{a: &auth.Assertion{User: oracleUser.ID, CredentialID: "belfry", AuthTime: f.now.Add(-c.ago)}},
			Order:         150, Spec: auth.MethodSpec{Method: "ext:oracle", Interactive: true},
		}}
		svc := f.build(in)
		var (
			p      *auth.Principal
			fail   auth.Failure
			failed bool
		)
		h := svc.Auth.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			p, _ = auth.PrincipalFrom(r.Context())
			fail, failed = auth.FailureFrom(r.Context())
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		switch {
		case c.expired && (p != nil || !failed || fail.Code != auth.CodeExpired):
			t.Errorf("%s: bound %+v, refusal %+v; want the expired refusal", c.name, p, fail)
		case !c.expired && (p == nil || p.Method != "ext:oracle"):
			t.Errorf("%s: bound %+v, refusal %+v; want the ext:oracle principal", c.name, p, fail)
		}
	}
}

// TestIdentity_MetricsScopes: identityMetrics builds both core scopes, the
// auth one declaring the extension label key (seams.d/F4-03.md), and the
// assembly takes them.
func TestIdentity_MetricsScopes(t *testing.T) {
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(ctx) })
	rs, as, err := identityMetrics(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"owner", "scope", "result", "origin"} {
		if !slices.ContainsFunc(rs.Allowed(), func(a attribute.Key) bool { return string(a) == k }) {
			t.Errorf("rbac scope lacks label %s", k)
		}
	}
	for _, k := range []string{"method", "outcome", "surface", "imported", "reason", "extension"} {
		if !slices.ContainsFunc(as.Allowed(), func(a attribute.Key) bool { return string(a) == k }) {
			t.Errorf("auth scope lacks label %s", k)
		}
	}
	f := newIDFx(t)
	in := f.inputs()
	in.RBACMetrics, in.AuthMetrics = rs, as
	f.build(in)
	if _, _, err := identityMetrics(p); err == nil {
		t.Error("a second identityMetrics on one Provider registered the core scopes twice")
	}
}

// lastAdminScene is the race's starting state: Bruce holds the admin seed
// role, Barbara holds custom.oracle, a custom role granting core:admin.
// Removing core:admin from custom.oracle is safe alone (Bruce stays), and
// demoting Bruce is safe alone (Barbara stays); both together would leave no
// active admin.
type lastAdminScene struct {
	svc     identityServices
	bruce   *auth.User
	barbara *auth.User
	oracle  *rbac.Role
	admin   rbac.Checker
	actor   *auth.Principal // Barbara's session: demotes Bruce
}

func newLastAdminScene(t *testing.T, f *idFx) lastAdminScene {
	t.Helper()
	ctx := context.Background()
	sc := lastAdminScene{svc: f.build(f.inputs())}
	if err := sc.svc.RBAC.CommitSeeds(ctx); err != nil {
		t.Fatal(err)
	}
	sc.admin = sc.svc.RBAC.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{rbac.RoleAdmin}, Interactive: true})
	var err error
	if sc.oracle, err = sc.svc.RBAC.CreateRole(ctx, sc.admin, rbac.RoleInput{
		Name: "custom.oracle", Title: "Oracle", Permissions: []rbac.Permission{"core:admin"},
	}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	sc.bruce = f.putUser("bruce-wayne", auth.StatusActive, rbac.RoleAdmin)
	sc.barbara = f.putUser("barbara-gordon", auth.StatusActive, "custom.oracle")
	if sc.actor, err = sc.svc.Auth.PrincipalFor(ctx, sc.barbara, auth.MethodSession, nil, sc.barbara.CredentialEpoch); err != nil {
		t.Fatal(err)
	}
	if n := f.activeAdmins(sc.svc.RBAC); n != 2 {
		t.Fatalf("%d active admins at the start, want 2", n)
	}
	return sc
}

// stripOracle removes core:admin from custom.oracle through rbac.
func (sc lastAdminScene) stripOracle(ctx context.Context) error {
	_, err := sc.svc.RBAC.UpdateRole(ctx, sc.admin, "custom.oracle",
		rbac.RoleInput{Title: "Oracle", Permissions: []rbac.Permission{"core:users.view"}}, sc.oracle.Version)
	return err
}

// demoteBruce takes the admin role from Bruce through auth.
func (sc lastAdminScene) demoteBruce(ctx context.Context) error {
	return sc.svc.Auth.SetRoles(ctx, sc.actor, sc.bruce.ID, []rbac.RoleName{rbac.RoleViewer})
}

// TestIdentity_LastAdminEditAloneSucceeds is the positive control: with no
// concurrent writer, the assembled rbac service strips core:admin from
// custom.oracle (Bruce keeps one active admin). A Holders adapter that is nil
// or always fails would refuse it.
func TestIdentity_LastAdminEditAloneSucceeds(t *testing.T) {
	f := newIDFx(t)
	sc := newLastAdminScene(t, f)
	if err := sc.stripOracle(context.Background()); err != nil {
		t.Fatalf("strip core:admin from custom.oracle alone: %v", err)
	}
	if n := f.activeAdmins(sc.svc.RBAC); n != 1 {
		t.Errorf("%d active admins, want 1 (Bruce)", n)
	}
}

// TestIdentity_LastAdminRoleEditLosesToAConcurrentDemotion is the
// deterministic interleave: rbac's role edit reads the holders (Bruce and
// Barbara, so the edit would keep Bruce), and before it commits, from inside
// its batch (the journal Append of role.narrowed runs after the last-admin
// read), auth demotes Bruce, which commits because custom.oracle still grants
// core:admin to Barbara. The role edit's Commit must then fail on the user
// record the adapter guarded, and its retry refuses with ErrLastAdmin.
// Without the adapter's guard the edit commits and no active admin is left.
func TestIdentity_LastAdminRoleEditLosesToAConcurrentDemotion(t *testing.T) {
	f := newIDFx(t)
	sc := newLastAdminScene(t, f)
	ctx := context.Background()
	var demoteErr error
	f.jrnl.setHook(journal.RoleNarrowed, func() { demoteErr = sc.demoteBruce(ctx) })
	err := sc.stripOracle(ctx)
	if demoteErr != nil {
		t.Fatalf("the demotion inside the role edit's window: %v", demoteErr)
	}
	if !errors.Is(err, rbac.ErrLastAdmin) {
		t.Errorf("role edit after the concurrent demotion: %v, want rbac.ErrLastAdmin", err)
	}
	if n := f.activeAdmins(sc.svc.RBAC); n != 1 {
		t.Errorf("%d active admins after the race, want 1 (Barbara through custom.oracle)", n)
	}
}

// TestIdentity_LastAdminRaceNeverLeavesNoAdmin races the two writes freely,
// many times, under -race: whichever commits first, the other refuses, and
// an active admin always remains.
func TestIdentity_LastAdminRaceNeverLeavesNoAdmin(t *testing.T) {
	const rounds = 24
	for i := range rounds {
		f := newIDFx(t)
		sc := newLastAdminScene(t, f)
		ctx := context.Background()
		start := make(chan struct{})
		var wg sync.WaitGroup
		var editErr, demoteErr error
		wg.Go(func() { <-start; editErr = sc.stripOracle(ctx) })
		wg.Go(func() { <-start; demoteErr = sc.demoteBruce(ctx) })
		close(start)
		wg.Wait()
		if n := f.activeAdmins(sc.svc.RBAC); n == 0 {
			t.Fatalf("round %d: no active admin left (edit %v, demotion %v)", i, editErr, demoteErr)
		}
		if editErr == nil && demoteErr == nil {
			t.Fatalf("round %d: both writes landed", i)
		}
		for what, err := range map[string]error{"edit": editErr, "demotion": demoteErr} {
			if err != nil && !errors.Is(err, rbac.ErrLastAdmin) && !errors.Is(err, auth.ErrLastAdmin) {
				t.Errorf("round %d: %s refused with %v, want a last-admin refusal", i, what, err)
			}
		}
	}
}
