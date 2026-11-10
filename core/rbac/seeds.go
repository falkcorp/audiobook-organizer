// file: core/rbac/seeds.go
// version: 1.1.0
// guid: c4e81f27-3a9d-4b6c-8f15-0d7a2e9b4c63
// last-edited: 2026-11-10

package rbac

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/storage"
)

// seedDef is the seed of one seed role, computed at Freeze (§4.8): core's
// seed for the role, every DefaultGrant naming it and, for a module role, the
// module's RoleSeed.Permissions.
type seedDef struct {
	name        RoleName
	title       string
	description string
	perms       []Permission // sorted, de-duplicated
	locked      bool         // admin: outside the merge
}

// adminSeed is the locked admin role. It is outside the merge and always
// stored as exactly {core:admin} (§4.8, DATA4-11).
func adminSeed() seedDef {
	return seedDef{
		name: RoleAdmin, title: "Administrator", description: "Every permission",
		perms: []Permission{CoreAdmin}, locked: true,
	}
}

// buildSeeds computes the seed of every seed role from the catalog and the
// module declarations, admin first and the rest by name, and checks the rules
// of §4.8 and §4.9 that need the catalog:
//
//   - every seed permission names something the catalog holds (ErrUnknownScope);
//   - no seed role is declared twice (ErrRoleExists);
//   - a DefaultGrant names editor, viewer or a seed role some module declared
//     (ErrUnknownRole);
//   - a confined seed role holds only concrete Confining scopes
//     (ErrConfiningSeed): a non-Confining scope, from its own seed or from a
//     DefaultGrant, would end its confinement
//     (TestSeed_NoNonConfiningScopeOnConfinedRole), and a wildcard could
//     reach one later. A role is confined here when its RoleSeed names a
//     concrete Confining scope or is Confined (a wildcard that today expands
//     only to Confining scopes); the wildcard case is refused because the
//     wildcard is not concrete, so a later release declaring a non-Confining
//     scope under it cannot widen the role silently. The final seed adds
//     only DefaultGrants, which never carry a Confining scope
//     (ErrRestrictedGrant), so it is Confined only if the RoleSeed is.
//
// Core's own seeds for editor and viewer (core:self, core:tasks.view,
// core:system.status, core:metrics.read) are the DefaultGrants of
// CoreScopes, so they reach the roles through the same path as a module's.
func buildSeeds(cat *Catalog, decls []*moduleDecl) ([]seedDef, error) {
	grants := map[RoleName][]Permission{}
	for spec := range cat.All() {
		for _, g := range spec.DefaultGrants {
			grants[g] = append(grants[g], spec.Name)
		}
	}
	var errs []error
	defs := map[RoleName]seedDef{}
	confining := map[RoleName]bool{}
	for _, d := range decls {
		for _, rs := range d.seeds {
			if _, dup := defs[rs.Name]; dup || isCoreSeed(rs.Name) {
				errs = append(errs, fmt.Errorf("%w: seed role %s is declared twice", ErrRoleExists, rs.Name))
				continue
			}
			if err := CheckRolePermissions(cat, rs.Permissions); err != nil {
				errs = append(errs, fmt.Errorf("seed role %s: %w", rs.Name, err))
			}
			defs[rs.Name] = seedDef{name: rs.Name, title: rs.Title, description: rs.Description, perms: rs.Permissions}
			if Confined(cat, rs.Permissions) {
				confining[rs.Name] = true
			}
			for _, p := range rs.Permissions {
				if spec, ok := cat.Lookup(p); ok && spec.Confining {
					confining[rs.Name] = true
				}
			}
		}
	}
	defs[RoleEditor] = seedDef{name: RoleEditor, title: "Editor", description: "Uses and changes the library"}
	defs[RoleViewer] = seedDef{name: RoleViewer, title: "Viewer", description: "Uses the library"}
	for g, perms := range grants {
		def, ok := defs[g]
		if !ok {
			errs = append(errs, fmt.Errorf("%w: a DefaultGrant names %q, which no module declares as a seed role", ErrUnknownRole, g))
			continue
		}
		def.perms = append(slices.Clone(def.perms), perms...)
		defs[g] = def
	}
	out := []seedDef{adminSeed()}
	for _, def := range defs {
		def.perms = normalize(def.perms)
		if confining[def.name] {
			for _, p := range def.perms {
				spec, ok := cat.Lookup(p)
				if !ok || !spec.Confining {
					errs = append(errs, fmt.Errorf("%w: %s would hold %s", ErrConfiningSeed, def.name, p))
				}
			}
		}
		out = append(out, def)
	}
	slices.SortFunc(out[1:], func(a, b seedDef) int { return strings.Compare(string(a.name), string(b.name)) })
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// seedDigest is the sha256 of the sorted permissions, one per line, hex.
func seedDigest(perms []Permission) string {
	var buf []byte
	for _, p := range normalize(perms) {
		buf = append(append(buf, p...), '\n')
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// seedDelta is what one merge changed in a role's effective seed.
type seedDelta struct {
	created   bool
	added     []Permission // now effective
	held      []Permission // PendingSeed after the merge
	withdrawn []Permission // no longer effective
}

// confinedForMerge reports whether the merge treats a stored seed role as
// confined: its effective set is Confined (05-rbac §4.10), or it is empty
// and holds a pending scope that no flag restricts, which only the
// confinement rule below can have held. The second case is the role a
// release emptied by withdrawing its last Confining scope while its new
// scopes wait: without it, the next boot would see an empty, unconfined role
// and apply them, and the merge would be neither safe nor idempotent.
func confinedForMerge(cat *Catalog, cur *Role, old []Permission) bool {
	if Confined(cat, old) {
		return true
	}
	if len(old) > 0 {
		return false
	}
	for _, p := range cur.PendingSeed {
		if !flagRestricted(cat, p) {
			return true
		}
	}
	return false
}

// flagRestricted reports whether p is or reaches a Sensitive, AuthRequired or
// Confining scope or a *:settings.manage / *:integrations.manage (§4.8), or
// reaches nothing the catalog holds (nothing to judge it by).
func flagRestricted(cat *Catalog, p Permission) bool {
	exp := cat.Expand(p)
	if len(exp) == 0 {
		return true
	}
	for _, c := range exp {
		spec, _ := cat.Lookup(c)
		if restrictedScope(spec) {
			return true
		}
	}
	return false
}

// restrictedAddition reports whether adding p to a role that already exists
// must wait for an admin's acceptance (§4.8, SEC-25): p is flagRestricted, or
// the role is confined (confinedForMerge) and p is not a concrete Confining
// scope, which would end the confinement. The last rule is F4-01's: §4.9's
// ErrConfiningSeed keeps a release from declaring such a seed, and this
// keeps a stored role from crossing over when a release replaces a confined
// seed with an unconfined one.
func restrictedAddition(cat *Catalog, confined bool, p Permission) bool {
	if confined {
		spec, ok := cat.Lookup(p)
		if !ok || !spec.Confining {
			return true
		}
	}
	return flagRestricted(cat, p)
}

// mergeSeed computes a seed role's stored state after the merge (§4.8). cur
// is the stored role, nil when absent. It reports whether anything changed;
// an unchanged role is not written. A stored role under a seed name that is
// not a seed role (other than admin, which is always rewritten) is refused:
// the merge would otherwise take over an operator's role.
func mergeSeed(cat *Catalog, def seedDef, cur *Role, now time.Time) (*Role, seedDelta, bool, error) {
	if cur == nil {
		r := &Role{
			Name: def.name, Title: def.title, Description: def.description, Origin: OriginSeed,
			SeedPermissions: slices.Clone(def.perms), SeedDigest: seedDigest(def.perms),
			Locked: def.locked, CreatedAt: now, UpdatedAt: now, Version: 1,
		}
		return r, seedDelta{created: true, added: slices.Clone(def.perms)}, true, nil
	}
	if cur.Origin != OriginSeed && !def.locked {
		return nil, seedDelta{}, false, fmt.Errorf("%w: stored role %s is not a seed role (origin %d)", ErrRoleExists, def.name, cur.Origin)
	}
	old := normalize(cur.Permissions())
	var next []Permission
	var pending []Permission
	if def.locked {
		next = slices.Clone(def.perms)
	} else {
		confined := confinedForMerge(cat, cur, old)
		for _, p := range minus(def.perms, old) {
			if restrictedAddition(cat, confined, p) {
				pending = append(pending, p)
			}
		}
		pending = normalize(pending)
		next = minus(def.perms, pending)
	}
	delta := seedDelta{added: minus(next, old), held: pending, withdrawn: minus(old, next)}
	same := cur.Origin == OriginSeed && cur.Locked == def.locked &&
		slices.Equal(normalize(cur.SeedPermissions), next) &&
		slices.Equal(normalize(cur.PendingSeed), pending) &&
		cur.SeedDigest == seedDigest(next) &&
		cur.Title == def.title && cur.Description == def.description &&
		(!def.locked || len(cur.Added) == 0)
	if same {
		return cur, delta, false, nil
	}
	r := cur.clone()
	r.Title, r.Description, r.Origin, r.Locked = def.title, def.description, OriginSeed, def.locked
	r.SeedPermissions, r.PendingSeed, r.SeedDigest = next, pending, seedDigest(next)
	if def.locked {
		r.Added = nil
	}
	r.UpdatedAt = now
	r.Version = cur.Version + 1
	return r, delta, true, nil
}

// CommitSeeds writes the seed roles computed at Freeze into the store (§4.8
// table; 06 §6.5.1 P7, "seed roles reconciled through rbac's store"). It is
// coined by F4-01: 05-rbac §5 commits the seed batch at P7, and the spec's
// method set has no call for it. Every seed role is merged in one batch: a
// created role is audited core.role.create, a merged one core.role.update_seed
// with what the release added, held and withdrew, and a withdrawal is
// journalled role.narrowed. An unchanged role is not written, so a second
// call writes nothing. A stored seed role that does not decode, or a role
// under a seed name that is not a seed role, fails the call (and the boot).
func (s *Service) CommitSeeds(ctx context.Context) error {
	st, err := s.frozen()
	if err != nil {
		return err
	}
	return s.write(ctx, func(b *storage.Batch) ([]*roleChange, error) {
		now := s.now().UTC()
		var changes []*roleChange
		for _, def := range st.seeds {
			cur, err := s.deps.Store.GetRole(ctx, def.name)
			if errors.Is(err, ErrUnknownRole) {
				cur, err = nil, nil
			}
			if err != nil {
				return nil, fmt.Errorf("rbac: seed role %s: %w", def.name, err)
			}
			next, delta, changed, err := mergeSeed(st.cat, def, cur, now)
			if err != nil {
				return nil, err
			}
			if !changed {
				continue
			}
			if len(delta.withdrawn) > 0 {
				if err := s.revoke(ctx, b, journal.Revocation{Kind: journal.RoleNarrowed, Subject: roleRef(def.name), Detail: permStrings(delta.withdrawn)}); err != nil {
					return nil, err
				}
			}
			if err := s.deps.Store.PutRole(b, next); err != nil {
				return nil, fmt.Errorf("rbac: seed role %s: %w", def.name, err)
			}
			action, details := "core.role.update_seed", map[string]any{
				"added": permStrings(delta.added), "held": permStrings(delta.held), "withdrawn": permStrings(delta.withdrawn),
			}
			if delta.created {
				action, details = "core.role.create", map[string]any{"permissions": permStrings(next.SeedPermissions), "seed": true}
			}
			if err := s.record(ctx, b, action, def.name, details); err != nil {
				return nil, err
			}
			if len(delta.held) > 0 {
				s.log.Warn("rbac: restricted seed permissions held for an administrator's acceptance", "role", string(def.name), "held", permStrings(delta.held))
			}
			changes = append(changes, &roleChange{name: def.name, role: next})
		}
		return changes, nil
	})
}

// AcceptSeed makes a seed role's PendingSeed effective (§4.8, audited
// core.role.accept_seed). It is the one write allowed on a seed role; admin
// answers ErrLockedRole, a custom role or a seed role with nothing pending
// ErrNoPendingSeed. The actor must hold every permission of the resulting
// set, as UpdateRole requires.
func (s *Service) AcceptSeed(ctx context.Context, actor Checker, id RoleName, ifVersion int64) (*Role, error) {
	st, err := s.frozen()
	if err != nil {
		return nil, err
	}
	var out *Role
	err = s.write(ctx, func(b *storage.Batch) ([]*roleChange, error) {
		cur, err := s.deps.Store.GetRole(ctx, id)
		if err != nil {
			return nil, err
		}
		switch {
		case cur.Locked || id == RoleAdmin:
			return nil, fmt.Errorf("%w: %s", ErrLockedRole, id)
		case cur.Origin != OriginSeed || len(cur.PendingSeed) == 0:
			return nil, fmt.Errorf("%w: %s", ErrNoPendingSeed, id)
		case cur.Version != ifVersion:
			return nil, fmt.Errorf("%w: %s is at version %d, not %d", ErrVersionMismatch, id, cur.Version, ifVersion)
		}
		accepted := normalize(cur.PendingSeed)
		perms := union(cur.SeedPermissions, accepted)
		if err := CheckRolePermissions(st.cat, perms); err != nil {
			return nil, err
		}
		if err := s.escalation(ctx, st.cat, actor, id, perms); err != nil {
			return nil, err
		}
		next := cur.clone()
		next.SeedPermissions, next.SeedDigest, next.PendingSeed = perms, seedDigest(perms), nil
		next.UpdatedAt = s.now().UTC()
		next.Version = cur.Version + 1
		if err := s.deps.Store.PutRole(b, next); err != nil {
			return nil, err
		}
		if err := s.record(ctx, b, "core.role.accept_seed", id, map[string]any{"accepted": permStrings(accepted)}); err != nil {
			return nil, err
		}
		out = next
		return []*roleChange{{name: id, role: next}}, nil
	})
	if err != nil {
		return nil, err
	}
	return out.clone(), nil
}
