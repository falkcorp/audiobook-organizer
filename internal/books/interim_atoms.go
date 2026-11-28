// file: internal/books/interim_atoms.go
// version: 0.1.0
// guid: 8c1e5d2a-47b3-4f60-9a1d-3e7b52c0d814
// last-edited: 2026-11-28

package books

import (
	"slices"

	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// interimAtoms is the atom table in the form the interim diagnostics export
// reads it (server.InterimAtom): each atom with the core scopes a request must
// all hold for it, and whether any of them is Sensitive in the catalog.
//
// The Sensitive flag is read from the same declarations the kernel registers
// (core's scopes and books' own), not restated here. A scope neither names is
// classed interactive-only, so a table row the catalog does not cover fails
// closed rather than open. server cannot import this table, hence the copy.
func interimAtoms() []server.InterimAtom {
	return interimAtomsFrom(AtomTable(), slices.Concat(rbac.CoreScopes(), booksPermissions()))
}

// interimAtomsFrom is interimAtoms over a table and the declarations that
// classify its scopes.
func interimAtomsFrom(table []Atom, specs []rbac.PermissionSpec) []server.InterimAtom {
	sensitive := map[rbac.Permission]bool{}
	known := map[rbac.Permission]bool{}
	for _, spec := range specs {
		known[spec.Name] = true
		sensitive[spec.Name] = spec.Sensitive
	}
	out := make([]server.InterimAtom, 0, len(table))
	for _, a := range table {
		row := server.InterimAtom{Atom: auth.Permission(a.Name)}
		for _, sc := range a.Scopes {
			row.Scopes = append(row.Scopes, string(sc))
			if sensitive[sc] || !known[sc] {
				row.InteractiveOnly = true
			}
		}
		out = append(out, row)
	}
	return out
}
