// file: internal/books/identity_m3.go
// version: 0.1.0
// guid: 9d2e5b84-7a13-4c60-8f9e-31b6c7a0d452
// last-edited: 2026-11-21

package books

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// identityDecl is everything M3's config takes that does not come from the
// booted core: the catalog the kernel builds for the boot (core's scopes and
// books', earlyCatalog), the atom table, HEAD's four seed roles with the native
// sets core's seed grants them, and the ambient list (05-rbac §6.3). It is built
// and checked once, in Options, so a declaration core would refuse stops the boot
// before anything opens; the cutover func then only adds the importers.
type identityDecl struct {
	catalog *rbac.Catalog
	atoms   []m3.AtomRow
	seeds   []m3.SeedRole
	ambient []rbac.Permission
}

// ambientScopes is the ambient scope list: written into every imported key and
// custom role that is not Confined (05-rbac §6.3).
func ambientScopes() []rbac.Permission {
	return []rbac.Permission{"core:self", "core:metrics.read", scopeReading}
}

// buildIdentityDecl assembles the declaration. The seed sets are not typed in a
// second time: core's seed for editor and viewer is the DefaultGrants of the
// catalog (rbac.buildSeeds), so the sets M3 intersects a key against are read
// from the same catalog and cannot drift from what core seeds.
func buildIdentityDecl() (*identityDecl, error) {
	cat, err := rbac.NewCatalog(append(rbac.CoreScopes(), booksPermissions()...)...)
	if err != nil {
		return nil, fmt.Errorf("books: core's scopes and books' do not make a catalog: %w", err)
	}
	grants := map[rbac.RoleName][]rbac.Permission{}
	for spec := range cat.All() {
		for _, role := range spec.DefaultGrants {
			grants[role] = append(grants[role], spec.Name)
		}
	}
	seeds := []m3.SeedRole{{ID: "admin", Permissions: []rbac.Permission{rbac.CoreAdmin}}}
	for _, role := range []rbac.RoleName{"editor", "viewer"} {
		perms := slices.Clone(grants[role])
		if len(perms) == 0 {
			return nil, fmt.Errorf("books: the catalog grants the %s seed role nothing", role)
		}
		slices.Sort(perms)
		seeds = append(seeds, m3.SeedRole{ID: string(role), Permissions: perms})
	}
	for _, r := range booksRoles() {
		seeds = append(seeds, m3.SeedRole{ID: string(r.Name), Permissions: slices.Clone(r.Permissions)})
	}
	atoms := make([]m3.AtomRow, 0, len(AtomTable()))
	for _, a := range AtomTable() {
		atoms = append(atoms, m3.AtomRow{Name: a.Name, Scopes: slices.Clone(a.Scopes)})
	}
	return &identityDecl{catalog: cat, atoms: atoms, seeds: seeds, ambient: ambientScopes()}, nil
}

// readback is m3.NativeReader over the two reads core's late importers expose
// (kernel lateAuth.GetUser and lateRoles.GetRole); the interfaces module.Importers
// declares have no read half, so each is asserted from the importer value.
type readback struct {
	users interface {
		GetUser(context.Context, coreauth.UserID) (*coreauth.User, error)
	}
	roles interface {
		GetRole(context.Context, rbac.RoleName) (*rbac.Role, error)
	}
}

func (r readback) GetUser(ctx context.Context, id coreauth.UserID) (*coreauth.User, error) {
	return r.users.GetUser(ctx, id)
}

func (r readback) GetRole(ctx context.Context, name rbac.RoleName) (*rbac.Role, error) {
	return r.roles.GetRole(ctx, name)
}

// errNoIdentityImporters is the cutover func's refusal of importers that do not
// carry what M3 needs. The kernel always hands its late forwarders, so this is a
// wiring fault: it is recorded and refused at Register, never a panic.
var errNoIdentityImporters = errors.New("books: the importers the kernel handed the cutover cannot stage the identity step")

// m3Config is M3's config over the importers the kernel hands the cutover func.
func (d *identityDecl) m3Config(imp module.Importers) (m3.Config, error) {
	users, uok := imp.Auth.(interface {
		GetUser(context.Context, coreauth.UserID) (*coreauth.User, error)
	})
	roles, rok := imp.Roles.(interface {
		GetRole(context.Context, rbac.RoleName) (*rbac.Role, error)
	})
	if imp.Auth == nil || imp.Roles == nil || imp.Legacy == nil || !uok || !rok {
		return m3.Config{}, errNoIdentityImporters
	}
	return m3.Config{
		StepID:    cutover.IdentityCutoverID,
		Atoms:     d.atoms,
		SeedRoles: d.seeds,
		Catalog:   d.catalog,
		Ambient:   d.ambient,
		Auth:      imp.Auth,
		Roles:     imp.Roles,
		Native:    readback{users: users, roles: roles},
		Audit:     imp.Audit,
		Legacy:    func() storage.Reader { return imp.Legacy(cutover.BooksOwner) },
		Now:       time.Now,
		KeyTTL:    handlers.DefaultAPIKeyTTL,
		Logger:    logger.FromContext(context.Background()).With("subsystem", "books-cutover-m3"),
	}, nil
}
