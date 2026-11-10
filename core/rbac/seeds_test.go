// file: core/rbac/seeds_test.go
// version: 1.2.0
// guid: 5c8a2f93-1d6e-4b47-9e0a-3f7b4d2c6e81
// last-edited: 2026-11-10

package rbac_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// rawPut stores raw at the row of role name, bypassing the store's checks,
// the way a damaged or foreign record would sit there.
func rawPut(t testing.TB, e *env, name rbac.RoleName, raw []byte) {
	t.Helper()
	ctx := context.Background()
	key := keys.Join(keys.Str("role"), keys.Str(string(name)))
	b := e.roles.NewBatch()
	defer func() {
		if err := b.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()
	if _, err := b.GetForUpdate(ctx, e.roles, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if err := b.In(e.roles).Set(key, raw); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
}

// encode is the stored form of r.
func encode(t testing.TB, r rbac.Role) []byte {
	t.Helper()
	raw, err := codec.JSON[rbac.Role](1).Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestSeedMerge_CreatesTheSeedRoles (§4.8 "absent"): the first boot creates
// admin (locked, {core:admin}), editor and viewer with core's seed plus the
// module grants, and the module's roles with their full seed, restricted
// scopes included; each creation is audited.
func TestSeedMerge_CreatesTheSeedRoles(t *testing.T) {
	m := gotham()
	m.seeds[0].Permissions = append(m.seeds[0].Permissions, "gotham:cave.manage")
	f := boot(t, newEnv(t), setup{modules: []module{m}})
	admin := f.role(rbac.RoleAdmin)
	if !admin.Locked || admin.Origin != rbac.OriginSeed || admin.Version != 1 {
		t.Errorf("admin = %+v, want a locked seed role at version 1", admin)
	}
	wantPerms(t, "admin", admin.Permissions(), "core:admin")
	wantPerms(t, "editor", f.role(rbac.RoleEditor).Permissions(),
		"core:metrics.read", "core:self", "core:system.status", "core:tasks.view", "gotham:patrol", "gotham:patrol.night", "gotham:signal")
	wantPerms(t, "viewer", f.role(rbac.RoleViewer).Permissions(),
		"core:metrics.read", "core:self", "core:system.status", "core:tasks.view", "gotham:patrol")
	det := f.role("gotham.detective")
	wantPerms(t, "gotham.detective", det.Permissions(), "gotham:cave.manage", "gotham:patrol", "gotham:patrol.night")
	if len(det.PendingSeed) != 0 || det.Locked || det.Title != "Detective" {
		t.Errorf("gotham.detective = %+v, want the full seed, nothing pending", det)
	}
	wantPerms(t, "relay-worker", f.role("relay-worker").Permissions(), "gotham:relay.worker")
	for _, name := range []rbac.RoleName{rbac.RoleAdmin, rbac.RoleEditor, rbac.RoleViewer, "gotham.detective", "relay-worker"} {
		r := f.role(name)
		if r.SeedDigest == "" || r.Origin != rbac.OriginSeed {
			t.Errorf("%s = %+v, want seed bookkeeping", name, r)
		}
		if !slices.Contains(f.audit.actions(), "core.role.create "+string(name)) {
			t.Errorf("audit = %v, want core.role.create of %s", f.audit.actions(), name)
		}
	}
	if len(f.journal.kinds()) != 0 {
		t.Errorf("creating seed roles journalled %v", f.journal.kinds())
	}
}

// TestSeedMerge_NewSeedScopeReachesRole (§4.8 "digest differs"): an
// unrestricted scope a release grants reaches the existing role at once, a
// withdrawn one leaves it with a role.narrowed line, and the merge is
// audited core.role.update_seed with what it added and withdrew.
func TestSeedMerge_NewSeedScopeReachesRole(t *testing.T) {
	e := newEnv(t)
	boot(t, e, setup{modules: []module{gotham()}})
	next := gotham()
	next.specs = append(next.specs, rbac.PermissionSpec{Name: "gotham:grapple", Title: "Grapple", DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}})
	next.specs[2].DefaultGrants = nil // gotham:signal is withdrawn from editor
	next.seeds[0].Permissions = append(next.seeds[0].Permissions, "gotham:grapple")
	f := boot(t, e, setup{modules: []module{next}})
	editor := f.role(rbac.RoleEditor)
	wantPerms(t, "editor", editor.Permissions(),
		"core:metrics.read", "core:self", "core:system.status", "core:tasks.view", "gotham:grapple", "gotham:patrol", "gotham:patrol.night")
	if editor.Version != 2 || len(editor.PendingSeed) != 0 {
		t.Errorf("editor = %+v, want version 2, nothing pending", editor)
	}
	wantPerms(t, "viewer", f.role(rbac.RoleViewer).Permissions(),
		"core:metrics.read", "core:self", "core:system.status", "core:tasks.view", "gotham:grapple", "gotham:patrol")
	wantPerms(t, "gotham.detective", f.role("gotham.detective").Permissions(), "gotham:grapple", "gotham:patrol", "gotham:patrol.night")
	if got := f.journal.kinds(); !slices.Equal(got, []string{"role.narrowed editor"}) {
		t.Errorf("journal = %v, want role.narrowed of editor", got)
	}
	var seen bool
	for _, ev := range f.audit.recorded {
		if ev.Action == "core.role.update_seed" && ev.Target.ID == "editor" {
			seen = true
			if !slices.Equal(ev.Details["added"].([]string), []string{"gotham:grapple"}) || !slices.Equal(ev.Details["withdrawn"].([]string), []string{"gotham:signal"}) {
				t.Errorf("update_seed details = %v", ev.Details)
			}
		}
	}
	if !seen {
		t.Errorf("audit = %v, want core.role.update_seed of editor", f.audit.actions())
	}
	if f.role(rbac.RoleAdmin).Version != 1 {
		t.Error("a release rewrote admin")
	}
}

// TestSeedMerge_RestrictedAdditionHeldPending (§4.8, SEC-25): a Sensitive or
// settings scope added to an existing role waits in PendingSeed until an
// admin accepts it; acceptance needs the scopes, and a release withdrawing a
// pending scope drops it.
func TestSeedMerge_RestrictedAdditionHeldPending(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	boot(t, e, setup{modules: []module{gotham()}})
	next := gotham()
	next.seeds[0].Permissions = append(next.seeds[0].Permissions, "gotham:cave.manage", "gotham:settings.manage", "gotham:signal")
	p, sc := rbacScope(t)
	f := boot(t, e, setup{modules: []module{next}, metrics: sc})
	det := f.role("gotham.detective")
	wantPerms(t, "effective", det.Permissions(), "gotham:patrol", "gotham:patrol.night", "gotham:signal")
	wantPerms(t, "pending", det.PendingSeed, "gotham:cave.manage", "gotham:settings.manage")
	if v := gauge(t, p, "mediacore_rbac_pending_seed_roles", nil); v != 1 {
		t.Errorf("pending_seed_roles = %v, want 1", v)
	}
	if f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"gotham.detective"}}).Has("gotham:cave.manage") {
		t.Error("a pending scope is effective")
	}
	wantPerms(t, "patterns handed to Deps.Compile", f.comp.last(t).RolePatterns, "gotham:patrol", "gotham:patrol.night", "gotham:signal")

	// The same release again changes nothing.
	again := boot(t, e, setup{modules: []module{next}})
	if v := again.role("gotham.detective").Version; v != det.Version {
		t.Errorf("an unchanged release rewrote gotham.detective (version %d -> %d)", det.Version, v)
	}

	_, err := again.svc.AcceptSeed(ctx, again.as("gotham:patrol", "gotham:patrol.night", "gotham:signal", "gotham:settings.manage"), "gotham.detective", det.Version)
	errIs(t, "accept without the sensitive scope", err, rbac.ErrEscalation)
	// Review NIT-G: the actor must hold the whole resulting set, not just
	// what it accepts.
	_, err = again.svc.AcceptSeed(ctx, again.as("gotham:cave.manage", "gotham:settings.manage"), "gotham.detective", det.Version)
	errIs(t, "accept holding the pending scopes but not the role's", err, rbac.ErrEscalation)
	_, err = again.svc.AcceptSeed(ctx, grant(again.cat(), false, rbac.CoreAdmin), "gotham.detective", det.Version)
	errIs(t, "accept by a non-interactive admin", err, rbac.ErrEscalation)
	_, err = again.svc.AcceptSeed(ctx, again.admin(), "gotham.detective", det.Version+1)
	errIs(t, "accept at a stale version", err, rbac.ErrVersionMismatch)
	again.unchanged("gotham.detective", det.Version)
	acc, err := again.svc.AcceptSeed(ctx, again.admin(), "gotham.detective", det.Version)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	wantPerms(t, "accepted", acc.Permissions(), "gotham:cave.manage", "gotham:patrol", "gotham:patrol.night", "gotham:settings.manage", "gotham:signal")
	if len(acc.PendingSeed) != 0 || acc.Version != det.Version+1 {
		t.Errorf("accepted = %+v, want nothing pending at version %d", acc, det.Version+1)
	}
	if !slices.Contains(again.audit.actions(), "core.role.accept_seed gotham.detective") {
		t.Errorf("audit = %v, want core.role.accept_seed", again.audit.actions())
	}
	_, err = again.svc.AcceptSeed(ctx, again.admin(), "gotham.detective", acc.Version)
	errIs(t, "accept with nothing pending", err, rbac.ErrNoPendingSeed)
	again.mustCreate("custom.oracle", "gotham:signal")
	_, err = again.svc.AcceptSeed(ctx, again.admin(), "custom.oracle", 1)
	errIs(t, "accept on a custom role", err, rbac.ErrNoPendingSeed)
	_, err = again.svc.AcceptSeed(ctx, again.admin(), "custom.ghost", 1)
	errIs(t, "accept on an absent role", err, rbac.ErrUnknownRole)

	// Accepted scopes stay through later boots of the same release.
	after := boot(t, e, setup{modules: []module{next}})
	if v := after.role("gotham.detective").Version; v != acc.Version {
		t.Errorf("the boot after acceptance rewrote the role (version %d -> %d)", acc.Version, v)
	}

	// A pending scope the next release withdraws is dropped.
	g := newEnv(t)
	boot(t, g, setup{modules: []module{gotham()}})
	boot(t, g, setup{modules: []module{next}})
	h := boot(t, g, setup{modules: []module{gotham()}})
	d := h.role("gotham.detective")
	wantPerms(t, "effective after withdrawal", d.Permissions(), "gotham:patrol", "gotham:patrol.night")
	wantPerms(t, "pending after withdrawal", d.PendingSeed)
}

// TestSeedMerge_AdminOutsideMerge (§4.8, DATA4-11): admin is always stored as
// exactly {core:admin}, locked; a tampered row is rewritten (with a
// role.narrowed line for what it lost) and an intact one is never touched.
func TestSeedMerge_AdminOutsideMerge(t *testing.T) {
	e := newEnv(t)
	f := boot(t, e, setup{modules: []module{gotham()}})
	a := f.role(rbac.RoleAdmin)
	for range 2 {
		g := boot(t, e, setup{modules: []module{gotham()}})
		if r := g.role(rbac.RoleAdmin); r.Version != a.Version {
			t.Errorf("an intact admin was rewritten (version %d -> %d)", a.Version, r.Version)
		}
	}
	tampered := *a
	tampered.SeedPermissions = perms("core:admin", "gotham:signal")
	tampered.PendingSeed = perms("gotham:cave.manage")
	tampered.Locked = false
	tampered.Version = a.Version + 7
	rawPut(t, e, rbac.RoleAdmin, encode(t, tampered))
	g := boot(t, e, setup{modules: []module{gotham()}})
	r := g.role(rbac.RoleAdmin)
	wantPerms(t, "admin", r.Permissions(), "core:admin")
	if !r.Locked || len(r.PendingSeed) != 0 || r.Version != tampered.Version+1 {
		t.Errorf("admin = %+v, want locked, nothing pending, version %d", r, tampered.Version+1)
	}
	if got := g.journal.kinds(); !slices.Equal(got, []string{"role.narrowed admin"}) {
		t.Errorf("journal = %v, want role.narrowed of admin", got)
	}

	// A custom-origin row under the admin name is rewritten too.
	rawPut(t, e, rbac.RoleAdmin, encode(t, rbac.Role{Name: rbac.RoleAdmin, Origin: rbac.OriginCustom, Added: perms("gotham:signal"), Version: 3}))
	h := boot(t, e, setup{modules: []module{gotham()}})
	r = h.role(rbac.RoleAdmin)
	wantPerms(t, "admin after a custom row", r.Permissions(), "core:admin")
	if !r.Locked || r.Origin != rbac.OriginSeed || len(r.Added) != 0 {
		t.Errorf("admin = %+v, want the locked seed role", r)
	}
}

// TestSeedMerge_RefusesForeignRows: a seed name holding a role that is not a
// seed role, or a row that does not decode, fails CommitSeeds (the boot)
// and writes nothing.
func TestSeedMerge_RefusesForeignRows(t *testing.T) {
	ctx := context.Background()
	for name, raw := range map[string]func(t testing.TB) []byte{
		"undecodable": func(testing.TB) []byte { return []byte("not a role") },
		"unknown origin": func(t testing.TB) []byte {
			return encode(t, rbac.Role{Name: "gotham.detective", Origin: 9, Version: 1})
		},
		"custom origin": func(t testing.TB) []byte {
			return encode(t, rbac.Role{Name: "gotham.detective", Origin: rbac.OriginCustom, Added: perms("gotham:signal"), Version: 1})
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			rawPut(t, e, "gotham.detective", raw(t))
			f := build(t, e, setup{})
			f.declare(gotham())
			if err := f.svc.Freeze(ctx); err != nil {
				t.Fatalf("Freeze: %v", err)
			}
			gen := f.svc.Generation()
			if err := f.svc.CommitSeeds(ctx); err == nil {
				t.Fatal("CommitSeeds over a foreign row succeeded")
			}
			if _, err := f.store.GetRole(ctx, rbac.RoleEditor); !errors.Is(err, rbac.ErrUnknownRole) {
				t.Errorf("a failed CommitSeeds wrote editor: %v", err)
			}
			// The audit fake records on RecordIn; the records sat in the
			// batch that never committed, as the roles did.
			if f.svc.Generation() != gen {
				t.Error("a failed CommitSeeds bumped the generation")
			}
		})
	}
	errIs(t, "CommitSeeds before Freeze", build(t, newEnv(t), setup{}).svc.CommitSeeds(ctx), rbac.ErrNotFrozen)
}

// TestSeedMerge_Idempotent (§4.8): a second CommitSeeds and a boot with the
// same declarations write nothing; over random release sequences the merge
// keeps its invariants: every seed scope reaches the role (effective or
// pending), nothing effective is outside the seed, a pending scope is a
// restricted addition (the pool has no Confining scope, so no role is ever
// confined and nothing else may wait), admin stays {core:admin}, and
// committing again is a no-op.
func TestSeedMerge_Idempotent(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	f := boot(t, e, setup{modules: []module{gotham()}})
	gen, recorded := f.svc.Generation(), len(f.audit.actions())
	if err := f.svc.CommitSeeds(ctx); err != nil {
		t.Fatal(err)
	}
	if f.svc.Generation() != gen || len(f.audit.actions()) != recorded {
		t.Error("a second CommitSeeds wrote")
	}
	versions := map[rbac.RoleName]int64{}
	for r, err := range f.svc.Roles(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		versions[r.Name] = r.Version
	}
	g := boot(t, e, setup{modules: []module{gotham()}})
	for name, v := range versions {
		g.unchanged(name, v)
	}
	if len(g.audit.actions()) != 0 || len(g.journal.kinds()) != 0 {
		t.Errorf("an unchanged boot audited %v, journalled %v", g.audit.actions(), g.journal.kinds())
	}

	pool := []rbac.PermissionSpec{
		{Name: "gotham:patrol", Class: rbac.ClassView},
		{Name: "gotham:signal"},
		{Name: "gotham:grapple"},
		{Name: "gotham:cave.manage", Sensitive: true},
		{Name: "gotham:vault", AuthRequired: true},
		{Name: "gotham:settings.manage"},
		{Name: "gotham:integrations.manage"},
	}
	restricted := map[rbac.Permission]bool{"gotham:cave.manage": true, "gotham:vault": true, "gotham:settings.manage": true, "gotham:integrations.manage": true}
	rng := rand.New(rand.NewPCG(7, 20261110))
	held := 0
	for seq := range 12 {
		e := newEnv(t)
		var prev map[rbac.RoleName][]rbac.Permission
		for release := range 6 {
			m := module{id: "gotham"}
			var detective []rbac.Permission
			for _, spec := range pool {
				if !restricted[spec.Name] && rng.IntN(2) == 0 {
					spec.DefaultGrants = []rbac.RoleName{rbac.RoleEditor}
				}
				m.specs = append(m.specs, spec)
				if rng.IntN(2) == 0 {
					detective = append(detective, spec.Name)
				}
			}
			m.seeds = []rbac.RoleSeed{{Name: "gotham.detective", Title: "Detective", Permissions: detective}}
			f := boot(t, e, setup{modules: []module{m}})
			where := "sequence " + strconv.Itoa(seq) + " release " + strconv.Itoa(release)
			seeds := map[rbac.RoleName][]rbac.Permission{
				"gotham.detective": normalized(detective),
				rbac.RoleEditor:    editorSeed(m),
			}
			for name, seed := range seeds {
				r := f.role(name)
				eff, pend := r.Permissions(), normalized(r.PendingSeed)
				if got := normalized(append(slices.Clone(eff), pend...)); !slices.Equal(got, seed) {
					t.Fatalf("%s: %s effective %v + pending %v, want the seed %v", where, name, eff, pend, seed)
				}
				held += len(pend)
				for _, p := range pend {
					if slices.Contains(eff, p) {
						t.Fatalf("%s: %s holds %s both effective and pending", where, name, p)
					}
					if !restricted[p] {
						t.Fatalf("%s: %s holds an unrestricted %s pending", where, name, p)
					}
					if prev == nil {
						t.Fatalf("%s: a freshly created %s holds %s pending", where, name, p)
					}
					if slices.Contains(prev[name], p) {
						t.Fatalf("%s: %s held %s effective before and now only pending", where, name, p)
					}
				}
			}
			wantPerms(t, where+" admin", f.role(rbac.RoleAdmin).Permissions(), "core:admin")
			before := f.svc.Generation()
			if err := f.svc.CommitSeeds(ctx); err != nil {
				t.Fatal(err)
			}
			if f.svc.Generation() != before {
				t.Fatalf("%s: a second CommitSeeds wrote", where)
			}
			prev = map[rbac.RoleName][]rbac.Permission{}
			for name := range seeds {
				prev[name] = f.role(name).Permissions()
			}
			// Now and then an admin accepts what is pending.
			if d := f.role("gotham.detective"); len(d.PendingSeed) > 0 && rng.IntN(3) == 0 {
				acc, err := f.svc.AcceptSeed(ctx, f.admin(), d.Name, d.Version)
				if err != nil {
					t.Fatalf("%s: accept: %v", where, err)
				}
				prev["gotham.detective"] = acc.Permissions()
			}
		}
	}
	if held == 0 {
		t.Fatal("no release held a scope: the property ran without its interesting case")
	}
}

// TestSeedMerge_ConfinedFlagIsSticky (§4.8, §4.10; review round 2): the
// merge keeps a worker role confined through a stored flag, not by reading
// its current permissions against the current catalog. A confining release
// sets Role.Confined; a release that flips the worker scope to
// non-Confining, empties the role or re-adds the worker scope cannot clear
// it; only an AcceptSeed that lands a non-Confining scope does. A plain role
// whose scopes a release renames is never confined, so the new scope lands.
func TestSeedMerge_ConfinedFlagIsSticky(t *testing.T) {
	ctx := context.Background()
	view := rbac.PermissionSpec{Name: "books:fingerprint.view", Title: "View fingerprints"}
	withSeed := func(m module, seed ...string) module {
		m.seeds = []rbac.RoleSeed{{Name: "fp-worker", Title: "Fingerprint worker", Permissions: perms(seed...)}}
		return m
	}
	worker := func(t *testing.T, f *fixture) *rbac.Role {
		t.Helper()
		w := f.role("fp-worker")
		if f.svc.Compile(rbac.CheckerInput{Roles: []rbac.RoleName{"fp-worker"}}).Has("books:fingerprint.view") {
			t.Fatalf("fp-worker = %v: a worker role gained a non-Confining scope unaccepted", w.Permissions())
		}
		return w
	}

	t.Run("created confined", func(t *testing.T) {
		f := boot(t, newEnv(t), setup{modules: []module{books()}})
		if !f.role("fp-worker").Confined {
			t.Error("fp-worker created without the Confined flag")
		}
		for _, name := range []rbac.RoleName{rbac.RoleAdmin, rbac.RoleEditor, rbac.RoleViewer} {
			if f.role(name).Confined {
				t.Errorf("%s is flagged Confined", name)
			}
		}
	})

	// Probe E: the worker scope stops being Confining; the flag stays and
	// the next release's plain addition waits.
	t.Run("Confining flipped off", func(t *testing.T) {
		e := newEnv(t)
		boot(t, e, setup{modules: []module{books()}})
		flip := books()
		flip.specs[0].Confining = false
		for range 2 {
			w := worker(t, boot(t, e, setup{modules: []module{flip}}))
			wantPerms(t, "after the flip", w.Permissions(), "books:fingerprint.worker")
			if !w.Confined {
				t.Fatal("the flip cleared the Confined flag")
			}
		}
		add := withSeed(books(view), "books:fingerprint.worker", "books:fingerprint.view")
		add.specs[0].Confining = false
		for range 2 {
			w := worker(t, boot(t, e, setup{modules: []module{add}}))
			wantPerms(t, "pending after the follow-on release", w.PendingSeed, "books:fingerprint.view")
		}
	})

	// Probe G2: emptied, then the worker scope re-added: a Confining
	// addition to an existing role is restricted (§4.8), so it waits too.
	t.Run("emptied, then the worker scope re-added", func(t *testing.T) {
		e := newEnv(t)
		boot(t, e, setup{modules: []module{books()}})
		w := worker(t, boot(t, e, setup{modules: []module{withSeed(books())}}))
		wantPerms(t, "emptied", w.Permissions())
		for range 2 {
			w = worker(t, boot(t, e, setup{modules: []module{books()}}))
			wantPerms(t, "effective after the re-add", w.Permissions())
			wantPerms(t, "pending after the re-add", w.PendingSeed, "books:fingerprint.worker")
			if !w.Confined {
				t.Fatal("the re-add cleared the Confined flag")
			}
		}
	})

	t.Run("a confining release flags an existing plain role", func(t *testing.T) {
		e := newEnv(t)
		boot(t, e, setup{modules: []module{withSeed(books(view), "books:fingerprint.view")}})
		f := boot(t, e, setup{modules: []module{withSeed(books(view), "books:fingerprint.worker")}})
		if w := f.role("fp-worker"); !w.Confined {
			t.Fatalf("fp-worker = %+v after a confining release, want the Confined flag", w)
		}
		w := worker(t, boot(t, e, setup{modules: []module{withSeed(books(view), "books:fingerprint.view")}}))
		wantPerms(t, "pending (the withdrawn worker scope is dropped)", w.PendingSeed, "books:fingerprint.view")
	})

	t.Run("AcceptSeed of a non-Confining scope clears the flag", func(t *testing.T) {
		e := newEnv(t)
		boot(t, e, setup{modules: []module{books()}})
		f := boot(t, e, setup{modules: []module{withSeed(books(view), "books:fingerprint.view")}})
		w := f.role("fp-worker")
		wantPerms(t, "pending", w.PendingSeed, "books:fingerprint.view")
		acc, err := f.svc.AcceptSeed(ctx, f.admin(), "fp-worker", w.Version)
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		if acc.Confined || f.role("fp-worker").Confined {
			t.Fatal("accepting a non-Confining scope kept the Confined flag")
		}
		shelf := rbac.PermissionSpec{Name: "books:shelf", Title: "Shelf"}
		g := boot(t, e, setup{modules: []module{withSeed(books(view, shelf), "books:fingerprint.view", "books:shelf")}})
		wantPerms(t, "after the next release", g.role("fp-worker").Permissions(), "books:fingerprint.view", "books:shelf")
	})

	t.Run("AcceptSeed of a Confining scope keeps the flag", func(t *testing.T) {
		e := newEnv(t)
		relay := rbac.PermissionSpec{Name: "books:fingerprint.relay", Title: "Relay", Confining: true}
		boot(t, e, setup{modules: []module{books(relay)}})
		f := boot(t, e, setup{modules: []module{withSeed(books(relay), "books:fingerprint.worker", "books:fingerprint.relay")}})
		w := f.role("fp-worker")
		wantPerms(t, "pending", w.PendingSeed, "books:fingerprint.relay")
		acc, err := f.svc.AcceptSeed(ctx, f.admin(), "fp-worker", w.Version)
		if err != nil {
			t.Fatalf("accept: %v", err)
		}
		if !acc.Confined {
			t.Fatal("accepting a Confining scope cleared the Confined flag")
		}
		g := worker(t, boot(t, e, setup{modules: []module{withSeed(books(relay, view), "books:fingerprint.view")}}))
		wantPerms(t, "pending after a plain release", g.PendingSeed, "books:fingerprint.view")
	})

	// Probe F: a plain module role whose scopes a release renames is not
	// confined, so the renamed scope lands at once.
	t.Run("plain role renamed", func(t *testing.T) {
		e := newEnv(t)
		boot(t, e, setup{modules: []module{gotham()}})
		ren := gotham()
		ren.specs = []rbac.PermissionSpec{
			{Name: "gotham:beat", Title: "Beat", Class: rbac.ClassView, DefaultGrants: []rbac.RoleName{rbac.RoleEditor, rbac.RoleViewer}},
			{Name: "gotham:signal", Title: "Bat-Signal", DefaultGrants: []rbac.RoleName{rbac.RoleEditor}},
			{Name: "gotham:relay.worker", Title: "Relay worker", Confining: true},
		}
		ren.seeds = []rbac.RoleSeed{
			{Name: "gotham.detective", Title: "Detective", Permissions: perms("gotham:beat")},
			{Name: "relay-worker", Title: "Relay worker", Permissions: perms("gotham:relay.worker")},
		}
		for range 2 {
			f := boot(t, e, setup{modules: []module{ren}})
			d := f.role("gotham.detective")
			wantPerms(t, "detective after the rename", d.Permissions(), "gotham:beat")
			wantPerms(t, "detective pending", d.PendingSeed)
			if d.Confined {
				t.Error("a plain role is flagged Confined")
			}
		}
	})
}

// editorSeed is core's editor seed plus m's grants to editor.
func editorSeed(m module) []rbac.Permission {
	out := perms("core:metrics.read", "core:self", "core:system.status", "core:tasks.view")
	for _, spec := range m.specs {
		if slices.Contains(spec.DefaultGrants, rbac.RoleEditor) {
			out = append(out, spec.Name)
		}
	}
	return normalized(out)
}

// normalized is ps sorted and de-duplicated; nil when empty.
func normalized(ps []rbac.Permission) []rbac.Permission {
	if len(ps) == 0 {
		return nil
	}
	out := slices.Clone(ps)
	slices.Sort(out)
	return slices.Compact(out)
}
