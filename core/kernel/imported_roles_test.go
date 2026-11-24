// file: core/kernel/imported_roles_test.go
// version: 0.1.0
// guid: 4dd5f8a6-94cf-4e5d-b32f-e080233ea648
// last-edited: 2026-11-21

package kernel

import (
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
)

func withImported(ids ...rbac.RoleName) func(*module.Manifest) {
	return func(m *module.Manifest) { m.ImportedRoleIDs = ids }
}

// TestImportedRoleProblems is P1's ImportedRoleIDs rule (06 §6.5.1 P1, COR5-5):
// a core seed role ID or an ID two manifests list is ProblemImportedRole, a
// disabled module's list counts, and a role one manifest alone lists is fine.
func TestImportedRoleProblems(t *testing.T) {
	off := func(m *module.Manifest) { m.DefaultEnabled = false }
	cases := []struct {
		name string
		mods []testModule
		want []string // "<module>: <Detail>", in order
	}{
		{"nobody lists one", []testModule{mod("batcave", nil), mod("gcpd", nil)}, nil},
		{"one module lists a role", []testModule{mod("batcave", nil).with(withImported("fp-worker"))}, nil},
		{"two distinct roles", []testModule{
			mod("batcave", nil).with(withImported("fp-worker")),
			mod("gcpd", nil).with(withImported("night-shift")),
		}, nil},
		{"one module lists a role twice", []testModule{mod("batcave", nil).with(withImported("fp-worker", "fp-worker"))}, nil},
		{"two manifests list one role", []testModule{
			mod("batcave", nil).with(withImported("fp-worker")),
			mod("gcpd", nil).with(withImported("night-shift", "fp-worker")),
		}, []string{`gcpd: ImportedRoleIDs entry "fp-worker" is already listed by module batcave`}},
		{"a disabled manifest still reserves its role", []testModule{
			mod("batcave", nil).with(withImported("fp-worker")).with(off),
			mod("gcpd", nil).with(withImported("fp-worker")),
		}, []string{`gcpd: ImportedRoleIDs entry "fp-worker" is already listed by module batcave`}},
		{"every core seed role", []testModule{
			mod("batcave", nil).with(withImported(rbac.RoleAdmin, rbac.RoleEditor, rbac.RoleViewer)),
		}, []string{`batcave: ImportedRoleIDs entry "admin" is a core seed role`, `batcave: ImportedRoleIDs entry "editor" is a core seed role`, `batcave: ImportedRoleIDs entry "viewer" is a core seed role`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var entries []*entry
			for _, m := range tc.mods {
				entries = append(entries, &entry{mod: m, man: m.Manifest()})
			}
			probs := importedRoleProblems(entries)
			if len(probs) != len(tc.want) {
				t.Fatalf("problems = %v, want %d", probs, len(tc.want))
			}
			for i, p := range probs {
				got := string(p.Module) + ": " + p.Detail
				if p.Kind != module.ProblemImportedRole || got != tc.want[i] {
					t.Errorf("problem %d = %s %q, want an ImportedRole problem %q", i, p.Kind, got, tc.want[i])
				}
			}
		})
	}
}

// TestImportedRoleProblems_RefuseBoot: the rule runs in P1, so a colliding
// manifest list fails New with ProblemImportedRole (ErrImportedRole), and a
// list nothing collides with boots.
func TestImportedRoleProblems_RefuseBoot(t *testing.T) {
	k, err := newK(t, testConfig(t,
		mod("batcave", nil).with(withImported("fp-worker")),
		mod("gcpd", nil).with(withImported("fp-worker"))))
	if k != nil || !hasKind(err, module.ProblemImportedRole) || !errors.Is(err, module.ErrImportedRole) {
		t.Fatalf("New = %v, %v; want nil and an ImportedRole problem", k, err)
	}
	mustNew(t, testConfig(t, mod("batcave", nil).with(withImported("fp-worker"))))
}
