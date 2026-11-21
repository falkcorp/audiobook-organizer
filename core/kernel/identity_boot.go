// file: core/kernel/identity_boot.go
// version: 0.1.0
// guid: 6c15ce56-019c-47fc-85c6-5e76877470a6
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/internal/authstore"
	"github.com/falkcorp/mediacore/internal/rbacstore"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// This file is the kernel's identity boot (F4-08 B3): the call sites that wire
// the dark identity building blocks (identity.go's buildIdentity, p4d_steps.go's
// runP4dSteps, the forwarding importers, the events bus) into the boot phases of
// 06 §6.5.1. Everything here is reached only when the app declares the identity
// flip (identityBootDeclared); an app that does not boots exactly as before.

// identityBootDeclared reports whether the app declared the identity flip
// (Config.IdentityBoot, from app.Options.IdentityBoot). It is one explicit
// flag read in one place, never derived from another field: app.Options.Auth's
// zero value is what hello passes and ABO sets Auth before its flip, so no
// field of the app's options says "the app wants core identity" except this
// one. A derived signal also failed once already (F3-07: a gate derived from
// other state broke under the crash-matrix injector). The boot phases ask this
// method and nothing else.
func (k *Kernel) identityBootDeclared() bool { return k.cfg.IdentityBoot }

// identityOwners are the three storage owners the identity boot adds to the
// store's owner list (06 §6.5.1 P2): core/auth, core/rbac and core/backup (the
// revocation journal's c/backup/ keyspace, whose spec the journal package
// owns). They are claimed in the store only when the app declared the flip, so
// an app that did not keeps the owner list, and the keyspaces, it had.
func identityOwners() []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: authstore.Owner, Spec: authstore.StorageSpec(), Enabled: true},
		{Owner: rbacstore.Owner, Spec: rbacstore.StorageSpec(), Enabled: true},
		{Owner: journal.Owner, Spec: journal.StorageSpec(), Enabled: true},
	}
}

// identityBoot is what the identity boot keeps between phases: the two native
// stores built once at the start of P4b and handed, as the same instances, to
// the importers (so a role imported at P4b is in the cache Freeze loads), the
// restore hooks and the rbac and auth services of P5.
type identityBoot struct {
	users *authstore.Store
	roles *rbacstore.Store
}

// identityModules are the RBAC declarations fed to rbac's Declarer at P5 and
// that the early catalog (buildIdentityStores) is built from: the ENABLED
// manifests' Permissions, Roles and ImportedRoleIDs, in manifest order.
//
// Disabled manifests declare nothing. 06 §6.5.1 P5 says to declare them "so
// their permissions stay known" and 05-rbac §5 item 3 says "disabled modules
// declare nothing; their scopes stay stored in roles but are inert". The two
// conflict; the kernel takes the narrower reading (a scope no enabled module
// declares is not in the catalog, so nothing ever Has it), because declaring
// more can only widen what a stored role grants. One function feeds both the
// early catalog and the Declarer, so flipping the choice is one line here.
func (k *Kernel) identityModules() []identityModule {
	var mods []identityModule
	for _, e := range k.entries {
		if !e.enabled {
			continue
		}
		mods = append(mods, identityModule{
			ID:              string(e.man.ID),
			Permissions:     e.man.Permissions,
			Roles:           e.man.Roles,
			ImportedRoleIDs: e.man.ImportedRoleIDs,
		})
	}
	return mods
}

// earlyCatalog is the scope catalog the role importer checks an imported
// role's permissions against at P4b, before rbac.New exists: core's fixed
// scopes and every declared permission. rbacstore.Options.Catalog nil refuses
// every import, and Freeze builds the service's own catalog from the same two
// sources (declareAndFreeze), so the importer and the service agree.
func earlyCatalog(mods []identityModule) (*rbac.Catalog, error) {
	specs := rbac.CoreScopes()
	for _, m := range mods {
		specs = append(specs, m.Permissions...)
	}
	cat, err := rbac.NewCatalog(specs...)
	if err != nil {
		return nil, fmt.Errorf("kernel: identity: scope catalog: %w", err)
	}
	return cat, nil
}

// buildIdentityStores is the identity boot's first step, at the start of P4b
// (06 §6.5.1 P4b: "the kernel binds each importer to its subsystem's keyspace
// before the DAG runs"): the native auth and role stores over c/auth/ and
// c/rbac/, then the three importers bound to them and to the boot's one audit
// store. It writes nothing. The secret store is open already (bindCoreStores),
// and the audit store was built at P4a.
func (k *Kernel) buildIdentityStores(ctx context.Context) error {
	if k.sec == nil {
		return errors.New("kernel: identity: the app declared the identity boot but no secret store is open (the identity boot needs the config boot: a state directory and core.storage.path)")
	}
	if k.audit == nil {
		return errors.New("kernel: identity: no audit store is built")
	}
	db := k.store.db
	aks, ok := db.Keyspace(authstore.Owner)
	if !ok {
		return fmt.Errorf("kernel: identity: no %s keyspace in the store", authstore.Owner)
	}
	rks, ok := db.Keyspace(rbacstore.Owner)
	if !ok {
		return fmt.Errorf("kernel: identity: no %s keyspace in the store", rbacstore.Owner)
	}
	cat, err := earlyCatalog(k.identityModules())
	if err != nil {
		return err
	}
	users, err := authstore.New(aks, authstore.Options{Now: bootClock(ctx)})
	if err != nil {
		return fmt.Errorf("kernel: identity: auth store: %w", err)
	}
	roles, err := rbacstore.New(rks, rbacstore.Options{Catalog: cat, Now: bootClock(ctx)})
	if err != nil {
		return fmt.Errorf("kernel: identity: role store: %w", err)
	}
	k.ident = &identityBoot{users: users, roles: roles}
	k.importers().bindIdentity(users, roles, k.audit)
	return nil
}
