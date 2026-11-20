// file: core/rbac/rbactest/storesuite.go
// version: 1.4.0
// guid: 3a7e5c1d-94b2-4f68-8d0e-2b6f1a9c7e43
// last-edited: 2026-11-19

// Package rbactest is the conformance kit of rbac.Store and rbac.Importer
// (05-rbac §3.4, R4-1; 04 §8.1 F4): StoreSuite runs every case an
// implementation must pass, the importer's included. It is test support
// (03 R4, MOD5-6): it may import any core package and no core non-test
// package imports it.
package rbactest

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Fixture is one store under test: an empty store over a fresh keyspace.
type Fixture struct {
	// Store is the role store.
	Store rbac.Store
	// Importer imports into the same keyspace, checking against the catalog
	// Harness.New was given.
	Importer rbac.Importer
	// NewBatch opens a batch every write of Store and Importer can join.
	NewBatch func() *storage.Batch
	// Corrupt stores bytes that do not decode as a role at the row of name,
	// the way a damaged or foreign record would sit there.
	Corrupt func(t *testing.T, name rbac.RoleName)
}

// Harness builds fixtures. New is called once per case; cat may be nil (the
// importer must then refuse every role).
type Harness struct {
	New func(t *testing.T, cat *rbac.Catalog) Fixture
}

// Catalog is the synthetic catalog StoreSuite checks imports against: core's
// fixed scopes plus a "gotham" module with a plain scope, an implied pair,
// a Sensitive scope and a Confining one.
func Catalog(t testing.TB) *rbac.Catalog {
	t.Helper()
	specs := append(rbac.CoreScopes(),
		rbac.PermissionSpec{Name: "gotham:patrol", Title: "Patrol", Class: rbac.ClassView},
		rbac.PermissionSpec{Name: "gotham:patrol.night", Title: "Night patrol", Implies: []rbac.Permission{"gotham:patrol"}},
		rbac.PermissionSpec{Name: "gotham:signal", Title: "Bat-Signal"},
		rbac.PermissionSpec{Name: "gotham:cave.manage", Title: "Batcave", Sensitive: true},
		rbac.PermissionSpec{Name: "gotham:relay.worker", Title: "Relay worker", Confining: true},
	)
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		t.Fatalf("rbactest: synthetic catalog: %v", err)
	}
	return cat
}

// StoreSuite runs the conformance cases of rbac.Store and rbac.Importer, and
// of rbac.ImportSealer and rbac.RoleGuarder when the store implements them.
func StoreSuite(t *testing.T, h Harness) {
	t.Helper()
	if h.New == nil {
		t.Fatal("rbactest: Harness.New is nil")
	}
	cases := []struct {
		name string
		run  func(t *testing.T, h Harness)
	}{
		{"RoundTrip", roundTrip},
		{"ScanInNameOrder", scanOrder},
		{"UpdateBumpsVersion", update},
		{"VersionRules", versionRules},
		{"ConcurrentWritersConflict", concurrentWriters},
		{"Delete", deleteRole},
		{"GetAbsent", getAbsent},
		{"UndecodableRow", undecodable},
		{"ReturnedRolesAreCopies", copies},
		{"ImportStoresCustomRole", importGood},
		{"ImportRejectsBrokenRole", importRejects},
		{"ImportRejectedStagesNothing", importStagesNothing},
		{"ImportWithoutCatalogRefuses", importNoCatalog},
		{"ImportRefusedOnceSealed", importSealed},
		{"RoleSetGuard", roleSetGuard},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, h) })
	}
}

// stamp is a fixed UTC time without a monotonic reading, so a stored and a
// read role compare equal.
func stamp() time.Time { return time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC) }

// custom is a custom role at version v.
func custom(name rbac.RoleName, v int64, perms ...rbac.Permission) *rbac.Role {
	return &rbac.Role{
		Name: name, Title: "Role " + string(name), Origin: rbac.OriginCustom,
		Added: perms, CreatedAt: stamp(), UpdatedAt: stamp(), Version: v,
	}
}

// seed is a seed role at version v.
func seed(name rbac.RoleName, v int64, perms ...rbac.Permission) *rbac.Role {
	return &rbac.Role{
		Name: name, Title: "Seed " + string(name), Origin: rbac.OriginSeed,
		SeedPermissions: perms, SeedDigest: "digest", CreatedAt: stamp(), UpdatedAt: stamp(), Version: v,
	}
}

// commit stages f into a fresh batch and commits it.
func commit(t *testing.T, fx Fixture, f func(b *storage.Batch) error) error {
	t.Helper()
	b := fx.NewBatch()
	if err := f(b); err != nil {
		if cerr := b.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		return err
	}
	err := b.Commit(context.Background(), storage.Sync)
	if cerr := b.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	return err
}

// put commits PutRole(r), failing the test on error.
func put(t *testing.T, fx Fixture, r *rbac.Role) {
	t.Helper()
	if err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.PutRole(b, r) }); err != nil {
		t.Fatalf("PutRole(%s v%d): %v", r.Name, r.Version, err)
	}
}

// get reads name, failing the test on error.
func get(t *testing.T, fx Fixture, name rbac.RoleName) *rbac.Role {
	t.Helper()
	r, err := fx.Store.GetRole(context.Background(), name)
	if err != nil {
		t.Fatalf("GetRole(%s): %v", name, err)
	}
	return r
}

// scan returns every decodable role and every error ScanRoles yields.
func scan(t *testing.T, fx Fixture) ([]*rbac.Role, []error) {
	t.Helper()
	var roles []*rbac.Role
	var errs []error
	for r, err := range fx.Store.ScanRoles(context.Background()) {
		if err != nil {
			errs = append(errs, err)
			continue
		}
		roles = append(roles, r)
	}
	return roles, errs
}

func names(rs []*rbac.Role) []rbac.RoleName {
	out := make([]rbac.RoleName, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func roundTrip(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	want := custom("custom.gcpd", 1, "gotham:patrol", "gotham:signal")
	want.Description = "Gotham City Police"
	put(t, fx, want)
	if got := get(t, fx, want.Name); !reflect.DeepEqual(got, want) {
		t.Fatalf("GetRole = %+v, want %+v", got, want)
	}
	s := seed("gotham.watch", 1, "gotham:patrol")
	s.PendingSeed = []rbac.Permission{"gotham:cave.manage"}
	s.Locked = true
	put(t, fx, s)
	if got := get(t, fx, s.Name); !reflect.DeepEqual(got, s) {
		t.Fatalf("GetRole(seed) = %+v, want %+v (every field is stored)", got, s)
	}
	// A seed role's SeedConfined flag survives the round trip, set and then
	// cleared.
	w := seed("relay-worker", 1, "gotham:relay.worker")
	w.SeedConfined = true
	put(t, fx, w)
	if got := get(t, fx, w.Name); !reflect.DeepEqual(got, w) || !got.SeedConfined {
		t.Fatalf("GetRole(SeedConfined seed) = %+v, want %+v", got, w)
	}
	cleared := seed("relay-worker", 2, "gotham:relay.worker", "gotham:patrol")
	put(t, fx, cleared)
	if got := get(t, fx, w.Name); !reflect.DeepEqual(got, cleared) || got.SeedConfined {
		t.Fatalf("GetRole(cleared seed) = %+v, want %+v", got, cleared)
	}
}

func scanOrder(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	for _, n := range []rbac.RoleName{"custom.wayne", "custom.arkham", "custom.gcpd"} {
		put(t, fx, custom(n, 1, "gotham:patrol"))
	}
	roles, errs := scan(t, fx)
	if len(errs) > 0 {
		t.Fatalf("ScanRoles errors: %v", errs)
	}
	want := []rbac.RoleName{"custom.arkham", "custom.gcpd", "custom.wayne"}
	if got := names(roles); !slices.Equal(got, want) {
		t.Fatalf("ScanRoles = %v, want %v", got, want)
	}
}

func update(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.gcpd", 1, "gotham:patrol"))
	next := custom("custom.gcpd", 2, "gotham:patrol", "gotham:signal")
	put(t, fx, next)
	got := get(t, fx, "custom.gcpd")
	if got.Version != 2 || !slices.Equal(got.Added, next.Added) {
		t.Fatalf("after update: version %d added %v, want 2 %v", got.Version, got.Added, next.Added)
	}
}

func versionRules(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.gcpd", 1, "gotham:patrol"))
	cases := []struct {
		name string
		r    *rbac.Role
		want error
	}{
		{"create over a stored role", custom("custom.gcpd", 1, "gotham:signal"), rbac.ErrRoleExists},
		{"skip a version", custom("custom.gcpd", 3, "gotham:signal"), rbac.ErrVersionMismatch},
		{"zero version", custom("custom.gcpd", 0, "gotham:signal"), rbac.ErrVersionMismatch},
		{"negative version", custom("custom.gcpd", -1, "gotham:signal"), rbac.ErrVersionMismatch},
		{"update an absent role", custom("custom.arkham", 2, "gotham:signal"), rbac.ErrVersionMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := fx.NewBatch()
			err := fx.Store.PutRole(b, c.r)
			if !errors.Is(err, c.want) {
				t.Errorf("PutRole = %v, want %v", err, c.want)
			}
			if n := b.Count(); n != 0 {
				t.Errorf("a refused PutRole staged %d writes", n)
			}
			if cerr := b.Close(); cerr != nil {
				t.Errorf("close: %v", cerr)
			}
		})
	}
	if got := get(t, fx, "custom.gcpd"); got.Version != 1 || !slices.Equal(got.Added, []rbac.Permission{"gotham:patrol"}) {
		t.Fatalf("a refused write changed the stored role: %+v", got)
	}
}

func concurrentWriters(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.gcpd", 1, "gotham:patrol"))
	first, second := fx.NewBatch(), fx.NewBatch()
	defer func() {
		for _, b := range []*storage.Batch{first, second} {
			if err := b.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		}
	}()
	if err := fx.Store.PutRole(first, custom("custom.gcpd", 2, "gotham:signal")); err != nil {
		t.Fatal(err)
	}
	if err := fx.Store.DeleteRole(second, custom("custom.gcpd", 1)); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatalf("first writer: %v", err)
	}
	if err := second.Commit(context.Background(), storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second writer read version 1 and committed after version 2: %v, want storage.ErrConflict", err)
	}
	if got := get(t, fx, "custom.gcpd"); got.Version != 2 {
		t.Fatalf("stored version %d, want the first writer's 2", got.Version)
	}

	// Two creates of one name: the second fails on its Expect (absent).
	a, b := fx.NewBatch(), fx.NewBatch()
	if err := fx.Store.PutRole(a, custom("custom.arkham", 1, "gotham:patrol")); err != nil {
		t.Fatal(err)
	}
	if err := fx.Store.PutRole(b, custom("custom.arkham", 1, "gotham:signal")); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(context.Background(), storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(context.Background(), storage.Sync); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("second create of one role: %v, want storage.ErrConflict", err)
	}
	for _, x := range []*storage.Batch{a, b} {
		if err := x.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	}
}

func deleteRole(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.gcpd", 1, "gotham:patrol"))
	b := fx.NewBatch()
	if err := fx.Store.DeleteRole(b, custom("custom.gcpd", 2)); !errors.Is(err, rbac.ErrVersionMismatch) {
		t.Errorf("DeleteRole with a newer version = %v, want ErrVersionMismatch", err)
	}
	if err := fx.Store.DeleteRole(b, custom("custom.arkham", 1)); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("DeleteRole of an absent role = %v, want ErrUnknownRole", err)
	}
	if n := b.Count(); n != 0 {
		t.Errorf("refused deletes staged %d writes", n)
	}
	if err := b.Close(); err != nil {
		t.Error(err)
	}
	if err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteRole(b, custom("custom.gcpd", 1)) }); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if _, err := fx.Store.GetRole(context.Background(), "custom.gcpd"); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Fatalf("GetRole after delete = %v, want ErrUnknownRole", err)
	}
	// A deleted name can be created again from version 1.
	put(t, fx, custom("custom.gcpd", 1, "gotham:signal"))
}

func getAbsent(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	if r, err := fx.Store.GetRole(context.Background(), "custom.joker"); !errors.Is(err, rbac.ErrUnknownRole) || r != nil {
		t.Fatalf("GetRole of an absent role = %v, %v; want nil, ErrUnknownRole", r, err)
	}
}

func undecodable(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.arkham", 1, "gotham:patrol"))
	put(t, fx, custom("custom.wayne", 1, "gotham:signal"))
	fx.Corrupt(t, "custom.gcpd")
	if _, err := fx.Store.GetRole(context.Background(), "custom.gcpd"); !errors.Is(err, rbac.ErrUndecodableRole) {
		t.Fatalf("GetRole of a corrupt row = %v, want ErrUndecodableRole", err)
	}
	roles, errs := scan(t, fx)
	if len(errs) != 1 || !errors.Is(errs[0], rbac.ErrUndecodableRole) {
		t.Fatalf("ScanRoles errors = %v, want one ErrUndecodableRole", errs)
	}
	if got := names(roles); !slices.Equal(got, []rbac.RoleName{"custom.arkham", "custom.wayne"}) {
		t.Fatalf("ScanRoles skipped good rows around the corrupt one: %v", got)
	}
	b := fx.NewBatch()
	if err := fx.Store.PutRole(b, custom("custom.gcpd", 2, "gotham:patrol")); !errors.Is(err, rbac.ErrUndecodableRole) {
		t.Errorf("PutRole over a corrupt row = %v, want ErrUndecodableRole (never a blind overwrite)", err)
	}
	if n := b.Count(); n != 0 {
		t.Errorf("a refused overwrite staged %d writes", n)
	}
	if err := b.Close(); err != nil {
		t.Error(err)
	}
}

func copies(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.gcpd", 1, "gotham:patrol"))
	got := get(t, fx, "custom.gcpd")
	got.Added[0] = rbac.CoreAdmin
	if again := get(t, fx, "custom.gcpd"); again.Added[0] != "gotham:patrol" {
		t.Fatalf("mutating a returned role changed the store: %v", again.Added)
	}
}

func importGood(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	r := &rbac.Role{
		Name: "custom.gcpd_detectives", Title: "Detectives", Origin: rbac.OriginCustom,
		Added: []rbac.Permission{"gotham:signal", "gotham:patrol", "gotham:signal", "gotham:*"},
	}
	if err := commit(t, fx, func(b *storage.Batch) error { return fx.Importer.ImportRole(b, r) }); err != nil {
		t.Fatalf("ImportRole: %v", err)
	}
	got := get(t, fx, r.Name)
	want := []rbac.Permission{"gotham:*", "gotham:patrol", "gotham:signal"}
	if got.Origin != rbac.OriginCustom || got.Version != 1 || !slices.Equal(got.Added, want) ||
		got.Locked || len(got.SeedPermissions) != 0 || got.CreatedAt.IsZero() {
		t.Fatalf("imported role = %+v, want a version-1 custom role holding %v", got, want)
	}
	if !slices.Equal(r.Added, []rbac.Permission{"gotham:signal", "gotham:patrol", "gotham:signal", "gotham:*"}) {
		t.Fatalf("ImportRole changed the caller's role: %v", r.Added)
	}
}

// brokenImports are roles ImportRole must refuse.
func brokenImports() map[string]*rbac.Role {
	ok := func(name rbac.RoleName, perms ...rbac.Permission) *rbac.Role {
		return &rbac.Role{Name: name, Origin: rbac.OriginCustom, Added: perms}
	}
	withSeed := ok("custom.alfred", "gotham:patrol")
	withSeed.SeedPermissions = []rbac.Permission{"gotham:patrol"}
	locked := ok("custom.alfred", "gotham:patrol")
	locked.Locked = true
	pending := ok("custom.alfred", "gotham:patrol")
	pending.PendingSeed = []rbac.Permission{"gotham:signal"}
	seedOrigin := ok("custom.alfred", "gotham:patrol")
	seedOrigin.Origin = rbac.OriginSeed
	noOrigin := ok("custom.alfred", "gotham:patrol")
	noOrigin.Origin = 0
	seedConfined := ok("custom.alfred", "gotham:relay.worker")
	seedConfined.SeedConfined = true
	return map[string]*rbac.Role{
		"unknown scope":          ok("custom.alfred", "gotham:patrol", "gotham:batmobile"),
		"unknown namespace":      ok("custom.alfred", "metropolis:*"),
		"malformed scope":        ok("custom.alfred", "Gotham:Patrol"),
		"global wildcard":        ok("custom.alfred", "*:*"),
		"seed name admin":        ok("admin", "gotham:patrol"),
		"seed name editor":       ok("editor", "gotham:patrol"),
		"imported seed id":       ok("fp-worker", "gotham:relay.worker"),
		"module seed name":       ok("gotham.watch", "gotham:patrol"),
		"bare custom prefix":     ok("custom.", "gotham:patrol"),
		"custom name upper case": ok("custom.Alfred", "gotham:patrol"),
		"no permission":          ok("custom.alfred"),
		"core admin":             ok("custom.alfred", "gotham:patrol", rbac.CoreAdmin),
		"core wildcard":          ok("custom.alfred", "core:*"),
		"core admin wildcard":    ok("custom.alfred", "core:admin.*"),
		"seed permissions":       withSeed,
		"locked":                 locked,
		"pending seed":           pending,
		"seed origin":            seedOrigin,
		"zero origin":            noOrigin,
		"SeedConfined flag":      seedConfined,
	}
}

func importRejects(t *testing.T, h Harness) {
	for name, r := range brokenImports() {
		t.Run(name, func(t *testing.T) {
			fx := h.New(t, Catalog(t))
			b := fx.NewBatch()
			err := fx.Importer.ImportRole(b, r)
			if !errors.Is(err, rbac.ErrImportRejected) {
				t.Errorf("ImportRole = %v, want ErrImportRejected", err)
			}
			if n := b.Count(); n != 0 {
				t.Errorf("a rejected import staged %d writes", n)
			}
			if err := b.Close(); err != nil {
				t.Error(err)
			}
			if _, err := fx.Store.GetRole(context.Background(), r.Name); !errors.Is(err, rbac.ErrUnknownRole) {
				t.Errorf("a rejected import is readable: %v", err)
			}
		})
	}
	t.Run("already stored", func(t *testing.T) {
		fx := h.New(t, Catalog(t))
		put(t, fx, custom("custom.alfred", 1, "gotham:signal"))
		b := fx.NewBatch()
		err := fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.alfred", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:patrol"}})
		if !errors.Is(err, rbac.ErrImportRejected) || !errors.Is(err, rbac.ErrRoleExists) {
			t.Errorf("ImportRole over a stored role = %v, want ErrImportRejected wrapping ErrRoleExists", err)
		}
		if n := b.Count(); n != 0 {
			t.Errorf("staged %d writes", n)
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
		if got := get(t, fx, "custom.alfred"); !slices.Equal(got.Added, []rbac.Permission{"gotham:signal"}) {
			t.Errorf("the stored role changed: %v", got.Added)
		}
	})
}

// importStagesNothing: the cutover imports a chunk in one batch and
// quarantines a refused row, so a refusal must leave the batch committable,
// with no write and no precondition of its own.
func importStagesNothing(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	put(t, fx, custom("custom.alfred", 1, "gotham:signal"))
	err := commit(t, fx, func(b *storage.Batch) error {
		if err := fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.alfred", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:patrol"}}); !errors.Is(err, rbac.ErrImportRejected) {
			t.Errorf("import over a stored role = %v, want ErrImportRejected", err)
		}
		if err := fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.joker", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:batmobile"}}); !errors.Is(err, rbac.ErrImportRejected) {
			t.Errorf("import of an unknown scope = %v, want ErrImportRejected", err)
		}
		return fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.robin", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:patrol"}})
	})
	if err != nil {
		t.Fatalf("the chunk with two refused rows did not commit: %v", err)
	}
	get(t, fx, "custom.robin")
}

func importNoCatalog(t *testing.T, h Harness) {
	fx := h.New(t, nil)
	b := fx.NewBatch()
	err := fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.robin", Origin: rbac.OriginCustom, Added: []rbac.Permission{"core:self"}})
	if !errors.Is(err, rbac.ErrImportRejected) {
		t.Errorf("ImportRole with no catalog = %v, want ErrImportRejected", err)
	}
	if n := b.Count(); n != 0 {
		t.Errorf("staged %d writes", n)
	}
	if err := b.Close(); err != nil {
		t.Error(err)
	}
}

// importSealed: an importer that implements rbac.ImportSealer refuses every
// import once sealed, with an error wrapping ErrImportRejected and ErrFrozen,
// staging nothing; a role imported before the seal stays. An importer that
// does not implement it skips the case (the service then has nothing to
// seal, and the kernel's boot order alone keeps imports before Freeze).
func importSealed(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	sealer, ok := fx.Importer.(rbac.ImportSealer)
	if !ok {
		t.Skip("the importer does not implement rbac.ImportSealer")
	}
	if err := commit(t, fx, func(b *storage.Batch) error {
		return fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.alfred", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:patrol"}})
	}); err != nil {
		t.Fatalf("ImportRole before the seal: %v", err)
	}
	sealer.SealImports()
	b := fx.NewBatch()
	err := fx.Importer.ImportRole(b, &rbac.Role{Name: "custom.robin", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:signal"}})
	if !errors.Is(err, rbac.ErrImportRejected) || !errors.Is(err, rbac.ErrFrozen) {
		t.Errorf("ImportRole once sealed = %v, want ErrImportRejected wrapping ErrFrozen", err)
	}
	if n := b.Count(); n != 0 {
		t.Errorf("a sealed import staged %d writes", n)
	}
	if err := b.Close(); err != nil {
		t.Error(err)
	}
	if _, err := fx.Store.GetRole(context.Background(), "custom.robin"); !errors.Is(err, rbac.ErrUnknownRole) {
		t.Errorf("a sealed import is readable: %v", err)
	}
	get(t, fx, "custom.alfred")
}

// roleSetGuard: a store that implements rbac.RoleGuarder keeps one role-set
// generation that EVERY write of a role row raises (create, edit, delete,
// import) in the batch the write commits in, and stages, on request, an
// Expect that it is still the value a caller read. So a caller that read the
// generation before it read any role finds out at its own Commit
// (storage.ErrConflict) that some role was written since, whichever write it
// was, whether the role existed when it read it, and even when the role came
// back byte for byte as it was. A refused write, or one whose own Commit
// failed, raises nothing. A store that does not implement it skips the case
// (rbac.Service.RoleSetGeneration then answers ErrNoRoleGuard).
func roleSetGuard(t *testing.T, h Harness) {
	fx := h.New(t, Catalog(t))
	guarder, ok := fx.Store.(rbac.RoleGuarder)
	if !ok {
		t.Skip("the store does not implement rbac.RoleGuarder")
	}
	ctx := context.Background()
	generation := func(t *testing.T) uint64 {
		t.Helper()
		g, err := guarder.RoleSetGeneration(ctx)
		if err != nil {
			t.Fatalf("RoleSetGeneration: %v", err)
		}
		return g
	}
	// guarded stages a guard at the generation now, runs write, and returns
	// the guarding batch's Commit error and whether the generation moved.
	guarded := func(t *testing.T, write func(t *testing.T)) (commitErr error, moved bool) {
		t.Helper()
		g := generation(t)
		b := fx.NewBatch()
		defer func() { _ = b.Close() }()
		if err := guarder.GuardRoleSet(b, g); err != nil {
			t.Fatalf("GuardRoleSet(%d): %v", g, err)
		}
		write(t)
		commitErr = b.Commit(ctx, storage.Sync)
		return commitErr, generation(t) != g
	}

	if g := generation(t); g != 0 {
		t.Fatalf("an empty role set is at generation %d, want 0", g)
	}
	// Generation 0 is guardable too: the row is absent, and the first role
	// ever written moves it.
	if err := commit(t, fx, func(b *storage.Batch) error { return guarder.GuardRoleSet(b, 0) }); err != nil {
		t.Fatalf("a guard over the untouched empty role set: %v", err)
	}
	if err, moved := guarded(t, func(t *testing.T) { put(t, fx, custom("custom.wayne", 1, "core:admin")) }); !errors.Is(err, storage.ErrConflict) || !moved {
		t.Fatalf("the first role ever written: guard commit = %v, generation moved = %v, want storage.ErrConflict and true", err, moved)
	}

	t.Run("no role write in between", func(t *testing.T) {
		g := generation(t)
		if err, moved := guarded(t, func(t *testing.T) {
			if _, err := fx.Store.GetRole(ctx, "custom.wayne"); err != nil {
				t.Fatal(err)
			}
		}); err != nil || moved {
			t.Fatalf("reads between the guard and its commit: commit = %v, generation moved = %v, want nil and false", err, moved)
		}
		if again := generation(t); again != g {
			t.Fatalf("a guard changed the generation: %d to %d", g, again)
		}
		if got := get(t, fx, "custom.wayne"); got.Version != 1 {
			t.Fatalf("a guard wrote the role: version %d", got.Version)
		}
	})

	t.Run("every kind of role write moves it", func(t *testing.T) {
		kinds := []struct {
			name  string
			write func(t *testing.T)
		}{
			{"create", func(t *testing.T) { put(t, fx, custom("custom.gordon", 1, "core:self")) }},
			{"edit", func(t *testing.T) {
				cur := get(t, fx, "custom.gordon")
				put(t, fx, custom("custom.gordon", cur.Version+1, "core:self", "core:users.view"))
			}},
			{"delete", func(t *testing.T) {
				cur := get(t, fx, "custom.gordon")
				if err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteRole(b, cur) }); err != nil {
					t.Fatal(err)
				}
			}},
			{"import", func(t *testing.T) {
				r := &rbac.Role{Name: "custom.robin", Origin: rbac.OriginCustom, Added: []rbac.Permission{"gotham:patrol"}}
				if err := commit(t, fx, func(b *storage.Batch) error { return fx.Importer.ImportRole(b, r) }); err != nil {
					t.Fatalf("ImportRole: %v", err)
				}
			}},
		}
		for _, k := range kinds {
			if err, moved := guarded(t, k.write); !errors.Is(err, storage.ErrConflict) || !moved {
				t.Errorf("%s: guard commit = %v, generation moved = %v, want storage.ErrConflict and true", k.name, err, moved)
			}
		}
	})

	t.Run("a role read as absent, then created", func(t *testing.T) {
		if err, _ := guarded(t, func(t *testing.T) {
			if _, err := fx.Store.GetRole(ctx, "custom.ghost"); !errors.Is(err, rbac.ErrUnknownRole) {
				t.Fatalf("GetRole of a role never written = %v", err)
			}
			put(t, fx, custom("custom.ghost", 1, "core:admin"))
		}); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("guard commit after the absent role appeared = %v, want storage.ErrConflict", err)
		}
	})

	t.Run("deleted and put back byte for byte", func(t *testing.T) {
		// A per-role version check passes this: same name, same version, same
		// bytes. The generation does not.
		put(t, fx, custom("custom.alfred", 1, "core:self"))
		cur := get(t, fx, "custom.alfred")
		if err, _ := guarded(t, func(t *testing.T) {
			if err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteRole(b, cur) }); err != nil {
				t.Fatal(err)
			}
			put(t, fx, custom("custom.alfred", 1, "core:self"))
		}); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("guard commit after delete and recreate = %v, want storage.ErrConflict", err)
		}
		if back := get(t, fx, "custom.alfred"); !reflect.DeepEqual(back, cur) {
			t.Fatalf("the recreated role differs from the original: %+v vs %+v", back, cur)
		}
	})

	t.Run("a stale generation", func(t *testing.T) {
		g := generation(t)
		put(t, fx, custom("custom.nightwing", 1, "core:self"))
		if err := commit(t, fx, func(b *storage.Batch) error { return guarder.GuardRoleSet(b, g) }); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("a guard at generation %d with %d stored = %v, want storage.ErrConflict", g, generation(t), err)
		}
	})

	t.Run("a refused write raises nothing", func(t *testing.T) {
		g := generation(t)
		b := fx.NewBatch()
		if err := fx.Store.PutRole(b, custom("custom.nightwing", 9, "core:self")); !errors.Is(err, rbac.ErrVersionMismatch) {
			t.Fatalf("PutRole at a wrong version = %v", err)
		}
		_ = b.Close()
		if err := commit(t, fx, func(b *storage.Batch) error { return fx.Store.DeleteRole(b, custom("custom.ghost2", 1)) }); !errors.Is(err, rbac.ErrUnknownRole) {
			t.Fatalf("DeleteRole of an absent role = %v", err)
		}
		if now := generation(t); now != g {
			t.Fatalf("refused writes moved the generation: %d to %d", g, now)
		}
	})

	t.Run("a write whose commit failed raises nothing", func(t *testing.T) {
		g := generation(t)
		first, second := fx.NewBatch(), fx.NewBatch()
		defer func() { _ = first.Close(); _ = second.Close() }()
		if err := fx.Store.PutRole(first, custom("custom.oracle", 1, "core:self")); err != nil {
			t.Fatal(err)
		}
		if err := fx.Store.PutRole(second, custom("custom.oracle", 1, "core:users.view")); err != nil {
			t.Fatal(err)
		}
		if err := first.Commit(ctx, storage.Sync); err != nil {
			t.Fatal(err)
		}
		if err := second.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("the second writer of one role = %v, want storage.ErrConflict", err)
		}
		if now := generation(t); now != g+1 {
			t.Fatalf("two writers, one commit: generation %d to %d, want %d", g, now, g+1)
		}
	})

	t.Run("two roles in one batch", func(t *testing.T) {
		if err, moved := guarded(t, func(t *testing.T) {
			if err := commit(t, fx, func(b *storage.Batch) error {
				if err := fx.Store.PutRole(b, custom("custom.batgirl", 1, "core:self")); err != nil {
					return err
				}
				return fx.Store.PutRole(b, custom("custom.huntress", 1, "core:self"))
			}); err != nil {
				t.Fatalf("two role writes in one batch: %v", err)
			}
		}); !errors.Is(err, storage.ErrConflict) || !moved {
			t.Fatalf("guard commit after a two-role batch = %v, moved = %v, want storage.ErrConflict and true", err, moved)
		}
	})

	t.Run("a claim raises it, and a guard read before it conflicts", func(t *testing.T) {
		g := generation(t)
		reader := fx.NewBatch()
		defer func() { _ = reader.Close() }()
		if err := guarder.GuardRoleSet(reader, g); err != nil {
			t.Fatal(err)
		}
		before := get(t, fx, "custom.wayne")
		if err := commit(t, fx, func(b *storage.Batch) error { return guarder.ClaimRoleSet(b, g) }); err != nil {
			t.Fatalf("a claim at the generation now: %v", err)
		}
		if now := generation(t); now != g+1 {
			t.Fatalf("a claim moved the generation %d to %d, want %d", g, now, g+1)
		}
		if after := get(t, fx, "custom.wayne"); !reflect.DeepEqual(after, before) {
			t.Fatalf("a claim changed a role: %+v vs %+v", after, before)
		}
		if err := reader.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("Commit of a guard read before the claim = %v, want storage.ErrConflict", err)
		}
		if err := commit(t, fx, func(b *storage.Batch) error { return guarder.ClaimRoleSet(b, g) }); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("a claim over a generation read before the first = %v, want storage.ErrConflict", err)
		}
	})

	t.Run("the first guard of a batch wins", func(t *testing.T) {
		g1 := generation(t)
		b := fx.NewBatch()
		defer func() { _ = b.Close() }()
		if err := guarder.GuardRoleSet(b, g1); err != nil {
			t.Fatal(err)
		}
		put(t, fx, custom("custom.signal", 1, "core:self"))
		if err := guarder.GuardRoleSet(b, generation(t)); err != nil {
			t.Fatalf("a second guard at the generation now: %v", err)
		}
		if err := b.Commit(ctx, storage.Sync); !errors.Is(err, storage.ErrConflict) {
			t.Fatalf("Commit with the first guard stale = %v, want storage.ErrConflict: a second guard must not replace it", err)
		}
	})

	t.Run("nil batch", func(t *testing.T) {
		if err := guarder.GuardRoleSet(nil, 0); err == nil {
			t.Fatal("GuardRoleSet without a batch succeeded")
		}
		if err := guarder.ClaimRoleSet(nil, 0); err == nil {
			t.Fatal("ClaimRoleSet without a batch succeeded")
		}
	})
}
