// file: core/kernel/identity_boot_test.go
// version: 0.1.0
// guid: 5428afc5-7971-4d76-8c20-b603d1f551f1
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// TestIdentityBootDeclared_IsTheFlagAlone pins the switch: the kernel decides
// from Config.IdentityBoot and from nothing else in its config.
func TestIdentityBootDeclared_IsTheFlagAlone(t *testing.T) {
	if (&Kernel{cfg: Config{IdentityBoot: true}}).identityBootDeclared() != true {
		t.Error("IdentityBoot=true does not declare the identity boot")
	}
	if (&Kernel{cfg: Config{}}).identityBootDeclared() {
		t.Error("the zero Config declares the identity boot")
	}
}

// identityHost is the swap host with the identity boot declared and a module
// that declares one permission, so the early catalog has something of the
// module's in it.
func identityHost(t *testing.T) (*swapHost, Config) {
	t.Helper()
	h := newSwapHost(t)
	c := h.envConfig(nil)
	c.IdentityBoot = true
	c.Modules = []module.Module{storageMod("alfred", nil, seedStep(1, "cave")).with(func(m *module.Manifest) {
		m.Permissions = []rbac.PermissionSpec{{Name: "alfred:signal", Title: "Signal"}}
	})}
	return h, c
}

// TestIdentityBoot_SwitchOnClaimsTheIdentityOwners: with the switch on the
// store claims c/auth/, c/rbac/ and c/backup/, after core's own and before the
// modules', and P4b builds the two stores.
func TestIdentityBoot_SwitchOnClaimsTheIdentityOwners(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	want := []storage.Owner{"core/audit", "core/tasks", configOwnerSpec().Owner, secretsOwnerID, authstore.Owner, rbacstore.Owner, journal.Owner, "alfred"}
	if got := ownerIDs(k); !slices.Equal(got, want) {
		t.Fatalf("storageOwners = %v, want %v", got, want)
	}
	for _, o := range []storage.Owner{authstore.Owner, rbacstore.Owner, journal.Owner} {
		if _, ok := k.store.db.Keyspace(o); !ok {
			t.Errorf("the switch-on boot has no %s keyspace", o)
		}
	}
	if k.ident == nil || k.ident.users == nil || k.ident.roles == nil {
		t.Fatalf("P4b built no identity stores: %+v", k.ident)
	}
}

// TestIdentityBoot_SwitchOffBuildsNone is the ABO-style boot: the switch off
// registers none of the identity owners and builds none of the identity state,
// whatever the manifests declare, and the three importers stay unbound.
func TestIdentityBoot_SwitchOffBuildsNone(t *testing.T) {
	h, c := identityHost(t)
	c.IdentityBoot = false
	k := h.mustBoot(c)
	for _, o := range ownerIDs(k) {
		if o == authstore.Owner || o == rbacstore.Owner || o == journal.Owner {
			t.Errorf("the switch-off boot registered %s", o)
		}
	}
	for _, o := range []storage.Owner{authstore.Owner, rbacstore.Owner, journal.Owner} {
		if _, ok := k.store.db.Keyspace(o); ok {
			t.Errorf("the switch-off boot has a %s keyspace", o)
		}
	}
	if k.ident != nil {
		t.Errorf("the switch-off boot built identity state: %+v", k.ident)
	}
	imp := k.importers().values()
	if err := imp.Auth.ImportInviteAllow(nil, "x@gotham.example.test"); !errors.Is(err, module.ErrImporterNotBound) {
		t.Errorf("Importers.Auth on a switch-off boot = %v, want ErrImporterNotBound", err)
	}
}

// TestIdentityBoot_NoSecretStoreFailsTheBoot: the switch on with no secret
// store (no state directory) is refused by name, not built over nothing.
func TestIdentityBoot_NoSecretStoreFailsTheBoot(t *testing.T) {
	k := &Kernel{cfg: Config{IdentityBoot: true}}
	if err := k.buildIdentityStores(context.Background()); err == nil {
		t.Fatal("buildIdentityStores with no secret store = nil")
	}
}

// TestLateImporters_IdentityRefusesBeforeBind: before bindIdentity every
// identity importer, Audit included, refuses with ErrImporterNotBound and
// stages nothing.
func TestLateImporters_IdentityRefusesBeforeBind(t *testing.T) {
	imp := (&lateImporters{}).values()
	ctx := context.Background()
	calls := map[string]error{
		"ImportUser":        imp.Auth.ImportUser(nil, &auth.User{}, ""),
		"ImportAPIKey":      imp.Auth.ImportAPIKey(nil, &auth.APIKey{}, [32]byte{}),
		"ImportIdentity":    imp.Auth.ImportIdentity(nil, &auth.ExternalIdentity{}),
		"ImportInviteAllow": imp.Auth.ImportInviteAllow(nil, "x@gotham.example.test"),
		"ImportRole":        imp.Roles.ImportRole(nil, &rbac.Role{}),
		"Audit.Record":      imp.Audit.Record(ctx, audit.Event{}),
		"Audit.RecordIn":    imp.Audit.RecordIn(ctx, nil, audit.Event{}),
	}
	for name, err := range calls {
		if !errors.Is(err, module.ErrImporterNotBound) {
			t.Errorf("%s before bind = %v, want ErrImporterNotBound", name, err)
		}
	}
	imp.Audit.Emit(ctx, audit.Event{}) // drops: it must not panic
}

// TestIdentityBoot_ImportersForwardToTheServiceStores: after P4b the importer a
// Cutover func holds writes to the very store instances the P5 services are
// built over, not copies: what an importer commits is readable through
// k.ident, and an imported role naming the module's declared permission is
// accepted while one naming an undeclared scope is refused (the early catalog).
func TestIdentityBoot_ImportersForwardToTheServiceStores(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	ctx := t.Context()
	imp := k.importers().values()

	b := k.ident.roles.NewBatch()
	good := &rbac.Role{Name: "custom.robin", Origin: rbac.OriginCustom, Added: []rbac.Permission{"alfred:signal"}}
	if err := imp.Roles.ImportRole(b, good); err != nil {
		t.Fatalf("ImportRole of a declared scope: %v", err)
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if got, err := k.ident.roles.GetRole(ctx, "custom.robin"); err != nil || got == nil {
		t.Fatalf("the imported role is not in the service's store: %v, %v", got, err)
	}
	b = k.ident.roles.NewBatch()
	bad := &rbac.Role{Name: "custom.joker", Origin: rbac.OriginCustom, Added: []rbac.Permission{"alfred:undeclared"}}
	if err := imp.Roles.ImportRole(b, bad); !errors.Is(err, rbac.ErrImportRejected) {
		t.Errorf("ImportRole of an undeclared scope = %v, want ErrImportRejected", err)
	}

	ab := k.ident.users.NewBatch()
	if err := imp.Auth.ImportInviteAllow(ab, "selina@gotham.example.test"); err != nil {
		t.Fatalf("ImportInviteAllow: %v", err)
	}
	if err := ab.Commit(ctx, storage.Sync); err != nil {
		t.Fatal(err)
	}
	if err := imp.Audit.Record(ctx, audit.Event{Action: "core.role.create", Outcome: audit.Success}); err != nil {
		t.Errorf("Importers.Audit.Record after bind: %v", err)
	}
}
