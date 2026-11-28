// file: core/kernel/imported_roles.go
// version: 0.1.0
// guid: 8d00727e-1e78-43f9-a7d6-3b5994bc5773
// last-edited: 2026-11-21

package kernel

import (
	"fmt"
	"slices"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

// coreSeedRoles are the role IDs core seeds itself (05-rbac §3.3): the three
// an ImportedRoleIDs entry may not equal. rbac's own isCoreSeed is this same
// set (it refuses the entry again when the Declarer takes it at P5).
func coreSeedRoles() []rbac.RoleName {
	return []rbac.RoleName{rbac.RoleAdmin, rbac.RoleEditor, rbac.RoleViewer}
}

// importedRoleProblems is P1's ImportedRoleIDs rule (06 §6.5.1 P1, COR5-5): an
// entry that equals a core seed role, or that another manifest already lists,
// is a ProblemImportedRole, because two modules (or a module and core) would
// seed one role row. It reads manifests only and calls no rbac code, which has
// no store at P1 (COR2-7). A disabled module's list counts: the manifest stays
// known while the module is off. entries are in ID order, so the module named
// in a collision is the later one.
func importedRoleProblems(entries []*entry) []module.Problem {
	var probs []module.Problem
	owner := make(map[rbac.RoleName]module.ID)
	for _, e := range entries {
		for _, id := range e.man.ImportedRoleIDs {
			switch first, taken := owner[id]; {
			case slices.Contains(coreSeedRoles(), id):
				probs = append(probs, module.Problem{Module: e.man.ID, Kind: module.ProblemImportedRole,
					Detail: fmt.Sprintf("ImportedRoleIDs entry %q is a core seed role", id)})
			case taken && first != e.man.ID:
				probs = append(probs, module.Problem{Module: e.man.ID, Kind: module.ProblemImportedRole,
					Detail: fmt.Sprintf("ImportedRoleIDs entry %q is already listed by module %s", id, first)})
			case !taken:
				owner[id] = e.man.ID
			}
		}
	}
	return probs
}
