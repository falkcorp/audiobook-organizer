// file: core/kernel/identity_boot_test.go
// version: 0.1.0
// guid: 5428afc5-7971-4d76-8c20-b603d1f551f1
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/health"
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
	want := []storage.Owner{"core/audit", "core/tasks", logOwnerSpec().Owner, configOwnerSpec().Owner, secretsOwnerID, authstore.Owner, rbacstore.Owner, journal.Owner, outboxOwnerSpec().Owner, "alfred"}
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

// TestIdentityBoot_ImportersForwardToTheServiceStores: at P4b the importer a
// Cutover func holds writes to the very store instances the P5 services are
// built over, not copies: what an importer commits is readable through
// k.ident, and an imported role naming the module's declared permission is
// accepted while one naming an undeclared scope is refused (the early catalog).
func TestIdentityBoot_ImportersForwardToTheServiceStores(t *testing.T) {
	h, c := identityHost(t)
	// The checks run between P4d and P5, where a Cutover step's importers
	// still reach an unfrozen rbac; a failure is the boot's.
	c.beforeP5 = func(k *Kernel) error {
		importersForwardToTheServiceStores(t, k)
		return nil
	}
	h.close(h.mustBoot(c))
}

func importersForwardToTheServiceStores(t *testing.T, k *Kernel) {
	t.Helper()
	ctx := t.Context()
	imp := k.importers().values()
	if u, r, _ := k.importers().identity(); u != k.ident.users || r != k.ident.roles {
		t.Fatalf("the bound importers are not the identity boot's store instances: %v, %v", u, r)
	}

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

// TestIdentityBoot_P4dBindsTheJournalAndRunsSteps2To4: the switch-on boot ran
// P4d's steps. The journal component is bound to <backup dir>/<prefix>
// revocations.journal (the backup directory beside the store, the prefix
// <AppID>_), step 2 recorded where it is (backup.location.json), step 4 seeded
// the _system user, and core.backup is registered with a probe that reads the
// component.
func TestIdentityBoot_P4dBindsTheJournalAndRunsSteps2To4(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	want := filepath.Join(filepath.Dir(h.db), "backups", c.AppID+"_revocations.journal")
	if got := k.ident.journal.Path(); got != want {
		t.Fatalf("the journal is bound to %q, want %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the journal file is not on disk: %v", err)
	}
	if _, ok, err := journal.ReadLocation(h.state); err != nil || !ok {
		t.Errorf("step 2 wrote no backup.location.json: ok=%v err=%v", ok, err)
	}
	if _, err := k.ident.users.GetUser(t.Context(), auth.SystemUserID); err != nil {
		t.Errorf("step 4 did not seed the _system user: %v", err)
	}
	var found bool
	for _, cs := range k.healthReg.Snapshot().Components {
		if cs.ID == componentBackup {
			found = true
			if cs.Result.Status != health.StatusUp {
				t.Errorf("core.backup on a clean boot is %v (%s)", cs.Result.Status, cs.Result.Reason)
			}
		}
	}
	if !found {
		t.Error("core.backup is not registered")
	}
}

// TestIdentityBoot_ArchivePrefixNamesTheJournal: Config.ArchivePrefix is the
// journal's file prefix (ABO: audiobooks_pebble_), and the default is <AppID>_.
func TestIdentityBoot_ArchivePrefixNamesTheJournal(t *testing.T) {
	h, c := identityHost(t)
	c.ArchivePrefix = "gotham_pebble_"
	k := h.mustBoot(c)
	if got, want := filepath.Base(k.ident.journal.Path()), "gotham_pebble_revocations.journal"; got != want {
		t.Fatalf("journal file = %q, want %q", got, want)
	}
	if got := k.ident.journal.App(); got != "gotham_pebble_" {
		t.Errorf("journal header app = %q, want gotham_pebble_", got)
	}
}

// TestIdentityBoot_SwitchOffRunsNoP4dSteps: the ABO-style boot has no journal,
// no _system user path, and no core.backup component.
func TestIdentityBoot_SwitchOffRunsNoP4dSteps(t *testing.T) {
	h, c := identityHost(t)
	c.IdentityBoot = false
	k := h.mustBoot(c)
	if k.ident != nil {
		t.Fatalf("identity state on a switch-off boot: %+v", k.ident)
	}
	for _, cs := range k.healthReg.Snapshot().Components {
		if cs.ID == componentBackup {
			t.Error("a switch-off boot registered core.backup")
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(h.db), "backups")); err == nil {
		t.Error("a switch-off boot created the backup directory")
	}
}

// TestIdentityBoot_ABackupDirUnderAnotherNameIsResolvedAgainstTheStore: a
// relative core.backup.dir resolves against the store's parent, an absolute
// one stands, an empty one is "backups" (resolveBackupDir).
func TestResolveBackupDir(t *testing.T) {
	db := filepath.Join(string(filepath.Separator), "var", "gotham", "main.pebble")
	for _, tc := range []struct{ configured, want string }{
		{"", filepath.Join(filepath.Dir(db), "backups")},
		{"vault", filepath.Join(filepath.Dir(db), "vault")},
		{filepath.Join(string(filepath.Separator), "srv", "cave"), filepath.Join(string(filepath.Separator), "srv", "cave")},
	} {
		if got := resolveBackupDir(tc.configured, db); got != tc.want {
			t.Errorf("resolveBackupDir(%q) = %q, want %q", tc.configured, got, tc.want)
		}
	}
}

// TestIdentityBoot_AFailedStepRefusesToServe: a P4d step that errors (here step
// 2 reading the head of a store that is closed under it) returns an error
// wrapping errP4dRefuse, so the boot that calls it fails.
func TestIdentityBoot_AFailedStepRefusesToServe(t *testing.T) {
	h, c := identityHost(t)
	k := h.mustBoot(c)
	if err := k.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	err := k.runIdentityP4d(t.Context())
	if !errors.Is(err, errP4dRefuse) {
		t.Fatalf("a failed step = %v, want errP4dRefuse", err)
	}
}
