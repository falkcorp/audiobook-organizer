// file: internal/books/identity_declarations.go
// version: 0.1.0
// guid: 5a93e0c7-6d21-4f48-b7a3-12c9d084e6f5
// last-edited: 2026-11-21

package books

import (
	"slices"
	"strings"

	"github.com/falkcorp/mediacore/rbac"
)

// What books declares to core's rbac for the identity boot (05-rbac §6.1, §6.2):
// its permission specs, its one seed role and the imported role ID that seed
// role keeps. The kernel builds the catalog from core's scopes and these
// (earlyCatalog), and M3 is checked against the same catalog (m3Config), so the
// scopes an atom, a seed set or the ambient list names must all be declared
// here or boot refuses. They are Manifest fields only while the identity
// predicate is on (Manifest), so a boot without the flip declares nothing.

const (
	scopeReading           rbac.Permission = "books:reading"
	scopeAdminView         rbac.Permission = "books:admin.view"
	scopeAdminDebug        rbac.Permission = "books:admin.debug"
	scopeFingerprintWorker rbac.Permission = "books:fingerprint.worker"
	// importedRoleFPWorker is HEAD's remote-worker role, whose ID stored user rows
	// name: the one un-namespaced seed role books keeps (Manifest.ImportedRoleIDs).
	importedRoleFPWorker rbac.RoleName = "fp-worker"
)

// headSeedAtoms are the atoms HEAD's seed gave the two roles whose books
// permissions are DefaultGrants (internal/auth/seed.go editorPermissions and
// viewerPermissions, 05-rbac §4.8's table): admin holds every atom, which
// core:admin covers, and fp-worker is books' own seed role.
var headSeedAtoms = map[rbac.RoleName][]string{
	"editor": {
		"library.view", "library.edit_metadata", "library.delete", "library.organize",
		"scan.trigger", "playlists.create", "requests.create", "requests.approve",
	},
	"viewer": {"library.view", "requests.create"},
}

// roleBooksScopes is the books-namespace scopes of a seed role: what the atom
// table maps its HEAD atoms to, plus books:reading (own progress, which every
// signed-in seed role keeps, 05-rbac §6.3's ambient list).
func roleBooksScopes(role rbac.RoleName) []rbac.Permission {
	var out []rbac.Permission
	for _, name := range headSeedAtoms[role] {
		for _, a := range AtomTable() {
			if a.Name != name {
				continue
			}
			for _, s := range a.Scopes {
				if strings.HasPrefix(string(s), "books:") && !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
	}
	return append(out, scopeReading)
}

// booksPermissions is books' catalog entries: every books-namespace scope an atom
// names, plus books:reading, books:admin.view and books:admin.debug (§6.1). The
// default grants into core's editor and viewer follow HEAD's seed through the
// atom table; the restricted scopes (the Sensitive, the AuthRequired, the
// Confining and the settings and integrations managers) are never a default
// grant, as core's declarer requires (05-rbac §4.9).
func booksPermissions() []rbac.PermissionSpec {
	var names []rbac.Permission
	for _, a := range AtomTable() {
		for _, s := range a.Scopes {
			if strings.HasPrefix(string(s), "books:") && !slices.Contains(names, s) {
				names = append(names, s)
			}
		}
	}
	for _, s := range []rbac.Permission{scopeReading, scopeAdminView, scopeAdminDebug} {
		if !slices.Contains(names, s) {
			names = append(names, s)
		}
	}
	specs := make([]rbac.PermissionSpec, 0, len(names))
	for _, n := range names {
		spec := rbac.PermissionSpec{Name: n, Title: permissionTitle(n), Description: permissionDescription(n)}
		for _, role := range []rbac.RoleName{"editor", "viewer"} {
			if slices.Contains(roleBooksScopes(role), n) {
				spec.DefaultGrants = append(spec.DefaultGrants, role)
			}
		}
		switch n {
		case "books:view", scopeAdminView:
			spec.Class = rbac.ClassView
		case "books:edit", "books:delete", "books:organize":
			spec.Implies = []rbac.Permission{"books:view"}
		case "books:request.approve":
			spec.Implies = []rbac.Permission{"books:request"}
		case scopeAdminDebug:
			spec.AuthRequired, spec.Sensitive = true, true
		case scopeFingerprintWorker:
			spec.Confining = true
		}
		specs = append(specs, spec)
	}
	return specs
}

// booksRoles is books' seed roles: HEAD's fp-worker, holding the one confining
// scope and nothing else (no core:self, 05-rbac §6.2).
func booksRoles() []rbac.RoleSeed {
	return []rbac.RoleSeed{{
		Name:        importedRoleFPWorker,
		Title:       "Fingerprint worker",
		Description: "The service user a remote fingerprint worker authenticates as.",
		Permissions: []rbac.Permission{scopeFingerprintWorker},
	}}
}

// booksImportedRoleIDs are the un-namespaced seed role IDs books keeps.
func booksImportedRoleIDs() []rbac.RoleName { return []rbac.RoleName{importedRoleFPWorker} }

func permissionTitle(p rbac.Permission) string {
	_, action, _ := strings.Cut(string(p), ":")
	return "Books: " + action
}

func permissionDescription(p rbac.Permission) string {
	_, action, _ := strings.Cut(string(p), ":")
	return "The books " + action + " permission (05-rbac §6.1)."
}
