// file: internal/books/atoms.go
// version: 1.0.1
// guid: f821dd40-ccb5-403a-8b4e-f606f72723da
// last-edited: 2026-11-08

package books

import (
	"errors"
	"fmt"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Atom is one row of ABO's atom table (05-rbac §6.1): a HEAD permission atom,
// the native scopes R2's identity cutover writes for it, and the route
// requirement books' s.perm(atom) adapter demands on every route HEAD gated
// by it.
//
// The table is books' code, not core's (05-rbac §4.9, COR5-5): HEAD's atoms
// include core-bound meanings (users.manage, settings.manage) that map to
// core:* scopes, which no module may grant and which core may not name on an
// app's behalf. It is used in exactly two places: the gin mount's route
// requirements, which internal/server's manifest records as
// rbac.LegacyAtom(atom) and Register resolves through RouteRequirement before
// MountLegacy (resolveRoutes), and the identity cutover (R2). Nothing here is
// a grant; Build checks a resolved requirement's scopes against the catalog
// from F4 like any other route requirement.
type Atom struct {
	// Name is HEAD's atom ("library.view", internal/auth/permissions.go).
	Name string
	// Scopes are the native scopes the cutover writes for the atom, in the
	// order 05-rbac §6.1 lists them.
	Scopes []rbac.Permission
	// Route is the route requirement: AllOf every one of Scopes (XFILE-5,
	// 05-http-api §8.3), the atom's full HEAD reach. Zero for an atom no route
	// enforces (playlists.create).
	Route rbac.Requirement
	// ReadSide is the in-handler read-side entry: the requirement of an
	// in-handler site that only shows user-identifying text (COR2-2,
	// SEC5-14). Zero for every atom but users.manage.
	ReadSide rbac.Requirement
}

// atomRow builds a row whose route requirement is AllOf all of its scopes.
func atomRow(name string, scopes ...rbac.Permission) Atom {
	return Atom{Name: name, Scopes: scopes, Route: rbac.AllOf(scopes...)}
}

// AtomTable returns the 13 rows of 05-rbac §6.1, in the spec's order. A fresh
// slice per call.
func AtomTable() []Atom {
	usersManage := atomRow("users.manage", "core:users.manage", "core:roles.view")
	usersManage.ReadSide = rbac.Need("core:users.view")
	playlists := atomRow("playlists.create", "books:playlists.create")
	playlists.Route = rbac.Requirement{} // HEAD gates no route with it (raw/09 §1.3: 0 routes)
	return []Atom{
		atomRow("library.view", "books:view", "core:tasks.view"),
		atomRow("library.edit_metadata", "books:edit"),
		atomRow("library.delete", "books:delete"),
		atomRow("library.organize", "books:organize"),
		atomRow("scan.trigger", "books:scan"),
		atomRow("integrations.manage", "core:integrations.manage", "books:integrations.manage"),
		usersManage,
		atomRow("settings.manage", "core:settings.manage", "books:settings.manage", "core:tasks.manage",
			"core:system.view", "core:system.manage", "core:library.manage", "core:library.browse",
			"core:integrations.manage"),
		playlists,
		atomRow("collections.manage", "books:collections.manage"),
		atomRow("requests.create", "books:request"),
		atomRow("requests.approve", "books:request.approve"),
		atomRow("fingerprint.worker", "books:fingerprint.worker"),
	}
}

// RouteRequirement resolves a legacy route's recorded requirement: an
// rbac.LegacyAtom(atom) value becomes the atom's route requirement, and false
// when the atom is not in the table or no route may enforce it. Any other
// requirement is returned unchanged with true.
func RouteRequirement(r rbac.Requirement) (rbac.Requirement, bool) {
	return routeRequirement(AtomTable(), r)
}

func routeRequirement(table []Atom, r rbac.Requirement) (rbac.Requirement, bool) {
	if r.Kind() != rbac.KindLegacyAtom {
		return r, true
	}
	for _, a := range table {
		if a.Name == r.Atom() {
			return a.Route, !a.Route.IsZero()
		}
	}
	return rbac.Requirement{}, false
}

// resolveRoutes is the manifest books mounts: each route internal/server
// recorded from an s.perm(atom) chain carries the atom table's route
// requirement in place of rbac.LegacyAtom(atom), and every other route is
// unchanged (05-http-api §8.1 R1, §8.3; COR6-1). The manifest is final from
// R1 and stage 13 enforces it from F4, so a route whose atom the table lacks,
// or whose atom no route may enforce (playlists.create), is an error, one per
// route, rather than a route mounted with nothing to enforce. routes is not
// modified.
func resolveRoutes(routes []httpapi.LegacyRoute) ([]httpapi.LegacyRoute, error) {
	table := AtomTable()
	out := make([]httpapi.LegacyRoute, len(routes))
	var errs []error
	for i, lr := range routes {
		req, ok := routeRequirement(table, lr.Perm)
		if !ok {
			errs = append(errs, fmt.Errorf("%s %s: atom %q has no route requirement in the atom table", lr.Method, lr.GinPath, lr.Perm.Atom()))
			continue
		}
		lr.Perm = req
		out[i] = lr
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}
