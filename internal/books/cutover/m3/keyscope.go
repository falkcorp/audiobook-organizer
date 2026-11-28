// file: internal/books/cutover/m3/keyscope.go
// version: 0.1.0
// guid: 8f2c6e41-d3a9-4b57-9e10-74b5a2c8d6f3
// last-edited: 2026-11-22

package m3

import (
	"errors"
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/rbac"
)

// KeyScoper is the one rule that turns a HEAD API key's atoms into core
// scopes (05-rbac §6.3). Two callers share it, so they cannot drift: M3, for
// every key it imports, and books' POST /api/v1/auth/api-keys under core
// identity (F4-08 B10z, internal/books identity wiring), for every key created
// after the flip. A HEAD request that names the same atoms for the same owner
// therefore gets the same scopes whether HEAD minted the key before the
// cutover or the flipped build mints it after. It lives here, beside its
// first caller, because this package is a leaf that imports core only, so
// internal/books can hand it to the server without either importing the
// other's packages.
type KeyScoper struct {
	catalog *rbac.Catalog
	ambient []rbac.Permission
	atoms   map[string][]rbac.Permission
	names   []string          // atom names in table order
	every   []rbac.Permission // the union of every row's scopes, table order
}

// NewKeyScoper builds the rule over books' atom table, core's catalog and
// the ambient scope list (core:self, core:metrics.read, books:reading).
func NewKeyScoper(atoms []AtomRow, catalog *rbac.Catalog, ambient []rbac.Permission) (*KeyScoper, error) {
	switch {
	case len(atoms) == 0:
		return nil, errors.New("m3: Config.Atoms is books' atom table")
	case catalog == nil:
		return nil, errors.New("m3: Config.Catalog is core's permission catalog")
	case len(ambient) == 0:
		return nil, errors.New("m3: Config.Ambient is the ambient scope list")
	}
	k := &KeyScoper{catalog: catalog, ambient: slices.Clone(ambient), atoms: map[string][]rbac.Permission{}}
	for _, a := range atoms {
		if a.Name == "" || len(a.Scopes) == 0 {
			return nil, fmt.Errorf("m3: atom %q has no scopes", a.Name)
		}
		if _, dup := k.atoms[a.Name]; dup {
			return nil, fmt.Errorf("m3: atom %q is listed twice", a.Name)
		}
		k.atoms[a.Name] = slices.Clone(a.Scopes)
		k.names = append(k.names, a.Name)
		for _, s := range a.Scopes {
			if !slices.Contains(k.every, s) {
				k.every = append(k.every, s)
			}
		}
	}
	return k, nil
}

// Scopes is a key's native scopes (05-rbac §6.3): the native scopes of its
// atoms (an empty list, HEAD's "all the owner's role permissions", is the
// owner's set) intersected with the owner's native role set as a role-only
// checker compiles it, plus the ambient scopes unless that set is Confined.
// Only when the owner's roles hold core:admin does a key holding every atom,
// or with an empty list, get core:admin and every row's scopes. An atom
// outside the table grants nothing and is returned. owner may name patterns
// (a role's stored permissions) or concrete scopes (a compiled checker's).
func (k *KeyScoper) Scopes(atoms []string, owner []rbac.Permission) (scopes []rbac.Permission, unknown []string, err error) {
	known, unknown := k.scopesOf(atoms)
	admin := rbac.GrantsAdmin(owner)
	every := len(atoms) > 0
	for _, n := range k.names {
		if !slices.Contains(atoms, n) {
			every = false
			break
		}
	}
	var base []rbac.Permission
	switch {
	case admin && (len(atoms) == 0 || every):
		base = append([]rbac.Permission{rbac.CoreAdmin}, k.every...)
	case len(atoms) == 0:
		base = k.expand(owner)
	default:
		base = known
	}
	if !admin {
		held := k.closure(k.expand(owner))
		base = slices.DeleteFunc(slices.Clone(base), func(s rbac.Permission) bool { return !slices.Contains(held, s) })
	}
	out, err := rbac.WithAmbient(k.catalog, base, k.ambient...)
	return out, unknown, err
}

// Atoms is the HEAD atoms a key holding scopes can exercise, in table order:
// every atom when scopes grant core:admin, otherwise each atom whose native
// scopes all lie in the implication closure of scopes. It is how a legacy
// route shows a core key's reach in HEAD's vocabulary; it is not the inverse
// of Scopes (an empty-scope request comes back as the atoms of the owner's
// set, and ambient scopes name no atom of their own).
func (k *KeyScoper) Atoms(scopes []rbac.Permission) []string {
	admin := rbac.GrantsAdmin(scopes)
	held := k.closure(k.expand(scopes))
	out := []string{}
	for _, name := range k.names {
		if admin || !slices.ContainsFunc(k.atoms[name], func(s rbac.Permission) bool { return !slices.Contains(held, s) }) {
			out = append(out, name)
		}
	}
	return out
}

// scopesOf is the union of the native scopes of atoms, deduplicated in table
// order (the implication closure is the catalog's, at check time), and the
// atoms the table does not know, in the order given. A repeated atom counts once.
func (k *KeyScoper) scopesOf(atoms []string) (scopes []rbac.Permission, unknown []string) {
	for _, name := range k.names {
		if slices.Contains(atoms, name) {
			for _, s := range k.atoms[name] {
				if !slices.Contains(scopes, s) {
					scopes = append(scopes, s)
				}
			}
		}
	}
	for _, a := range atoms {
		if _, ok := k.atoms[a]; !ok && !slices.Contains(unknown, a) {
			unknown = append(unknown, a)
		}
	}
	return scopes, unknown
}

// expand is the concrete scopes the catalog holds under perms (patterns allowed).
func (k *KeyScoper) expand(perms []rbac.Permission) []rbac.Permission {
	var out []rbac.Permission
	for _, p := range perms {
		for _, c := range k.catalog.Expand(p) {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	return out
}

// closure is scopes plus every scope they imply, transitively.
func (k *KeyScoper) closure(scopes []rbac.Permission) []rbac.Permission {
	out := slices.Clone(scopes)
	for i := 0; i < len(out); i++ {
		spec, ok := k.catalog.Lookup(out[i])
		if !ok {
			continue
		}
		for _, imp := range spec.Implies {
			if !slices.Contains(out, imp) {
				out = append(out, imp)
			}
		}
	}
	return out
}
