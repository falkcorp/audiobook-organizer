// file: core/kernel/sec9_viewer_test.go
// version: 0.1.0
// guid: affe9706-087a-4d44-a099-b91bf510b3ef
// last-edited: 2026-11-28

package kernel

import (
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/authz"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// SEC-9 refuses a task kind that a viewer alone could start unless the definition
// states a reason (Permissions.StartableByViewers). viewStartScopes answers it by
// evaluating the Start requirement for a principal that holds every scope a
// viewer can hold and no other. rbac.Requirement is flat (one AllOf or AnyOf of
// scopes, or Public/AnyAuthenticated), so the nesting a requirement tree could
// have does not exist to test; the cases below are every shape the type admits.

func sec9Kernel(t *testing.T) *Kernel {
	t.Helper()
	cat, err := declareCoreCatalog()
	if err != nil {
		t.Fatal(err)
	}
	return &Kernel{rbacCat: cat}
}

func TestSEC9_ViewStartScopes(t *testing.T) {
	k := sec9Kernel(t)
	const (
		view1  = "core:tasks.view"      // view-class, a seed viewer's
		view2  = "core:system.view"     // view-class, no seed role's
		action = "core:library.manage"  // action-class
		other  = "core:settings.manage" // action-class
		self   = "core:self"            // action-class, granted to the viewer seed
		mine   = "gotham:unlisted"      // a module scope the core catalog does not hold
	)
	for _, tc := range []struct {
		name   string
		req    rbac.Requirement
		scopes []rbac.Permission
		open   bool
	}{
		{"all of one view scope", rbac.AllOf(view1), []rbac.Permission{view1}, false},
		{"all of two view scopes", rbac.AllOf(view1, view2), []rbac.Permission{view1, view2}, false},
		{"all of a view and an action scope", rbac.AllOf(view2, action), nil, false},
		{"all of a view and the scope the viewer seed holds", rbac.AllOf(view1, self), []rbac.Permission{view1}, false},
		{"all of a view and an unknown scope", rbac.AllOf(view1, mine), []rbac.Permission{view1}, false},
		{"all of a view, an unknown and an action scope", rbac.AllOf(view1, mine, action), nil, false},
		{"all of an unknown scope", rbac.AllOf(mine), nil, false},
		{"all of an action scope", rbac.AllOf(action), nil, false},
		{"all of the viewer seed's action scope", rbac.AllOf(self), nil, false},
		{"any of a view and an action scope", rbac.AnyOf(view2, action), []rbac.Permission{view2}, false},
		{"any of two action scopes", rbac.AnyOf(action, other), nil, false},
		{"any of an action and an unknown scope", rbac.AnyOf(action, mine), nil, false},
		{"any of a view and an unknown scope", rbac.AnyOf(view1, mine), []rbac.Permission{view1}, false},
		{"any of two view scopes", rbac.AnyOf(view1, view2), []rbac.Permission{view1, view2}, false},
		{"public", rbac.Public, nil, true},
		{"any authenticated", rbac.AnyAuthenticated, nil, true},
		{"delegated", rbac.Delegated("the mounted handler checks"), nil, false},
		{"legacy atom", rbac.LegacyAtom("scan.trigger"), nil, false},
		{"empty all of is the zero requirement", rbac.AllOf(), nil, false},
		{"empty any of is the zero requirement", rbac.AnyOf(), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scopes, open := k.viewStartScopes(tc.req)
			if !slices.Equal(scopes, tc.scopes) || open != tc.open {
				t.Errorf("viewStartScopes(%s) = %v, open %v; want %v, open %v", tc.req, scopes, open, tc.scopes, tc.open)
			}
		})
	}
}

// TestSEC9_RefusesWhatAViewerCanStart: through registration, a Start requirement
// a viewer alone satisfies is refused until the definition states a reason, and
// one a viewer cannot satisfy is not.
func TestSEC9_RefusesWhatAViewerCanStart(t *testing.T) {
	with := func(r rbac.Requirement, reason string) func(*tasks.TaskSpec[struct{}, struct{}]) {
		return func(s *tasks.TaskSpec[struct{}, struct{}]) {
			s.Perms.Start = r
			s.Perms.StartableByViewers = reason
		}
	}
	for _, tc := range []struct {
		name    string
		req     rbac.Requirement
		refused string
	}{
		{"view beside an action scope", rbac.AllOf("core:tasks.view", "core:tasks.manage"), ""},
		{"any of two action scopes", rbac.AnyOf("core:tasks.manage", "core:library.manage"), ""},
		{"view alone", rbac.AllOf("core:tasks.view"), "view-class"},
		{"any of a view and an action scope", rbac.AnyOf("core:tasks.view", "core:tasks.manage"), "view-class"},
		{"view beside an unknown scope", rbac.AllOf("core:tasks.view", "gotham:unlisted"), "view-class"},
		{"public", rbac.Public, "every signed-in viewer satisfies"},
		{"any authenticated", rbac.AnyAuthenticated, "every signed-in viewer satisfies"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol(with(tc.req, ""))))
			_, err := newK(t, c)
			if tc.refused == "" {
				if err != nil {
					t.Fatalf("New = %v, want the registration accepted", err)
				}
			} else if !hasKind(err, module.ProblemInvalidRegistration) || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("New = %v, want a refusal naming %q", err, tc.refused)
			}
			c, _ = taskConfig(t, gotham([]tasks.Kind{patrolKind}, patrol(with(tc.req, "a reviewed reason"))))
			mustNew(t, c)
		})
	}
}

// TestSEC9_AViewerSeedStillTripsWhatItCanSatisfy: the built-in viewer role's real
// permission set, compiled by core's own checker, satisfies no one- or two-scope
// requirement that names a view-class scope without SEC-9 finding it. The
// viewer's set is read from the catalog's DefaultGrants (core:self, the one
// action-class scope it holds, and the view scopes), so this fails if a new seed
// grant or a new view scope opens a start the rule misses.
func TestSEC9_AViewerSeedStillTripsWhatItCanSatisfy(t *testing.T) {
	k := sec9Kernel(t)
	var seed, all []rbac.Permission
	for spec := range k.rbacCat.All() {
		all = append(all, spec.Name)
		if slices.Contains(spec.DefaultGrants, rbac.RoleViewer) {
			seed = append(seed, spec.Name)
		}
	}
	if len(seed) == 0 {
		t.Fatal("the catalog grants the viewer seed nothing")
	}
	viewer := authz.ForSystem(k.rbacCat, seed)
	var heldAction []rbac.Permission
	for _, p := range seed {
		if spec, _ := k.rbacCat.Lookup(p); spec.Class != rbac.ClassView {
			heldAction = append(heldAction, p)
		}
	}
	if !slices.Equal(heldAction, []rbac.Permission{"core:self"}) {
		t.Errorf("the viewer seed's action-class scopes = %v, want only core:self: a new one widens what SEC-9 must account for", heldAction)
	}
	hasView := func(req rbac.Requirement) bool {
		return slices.ContainsFunc(req.Permissions(), func(p rbac.Permission) bool {
			spec, ok := k.rbacCat.Lookup(p)
			return ok && spec.Class == rbac.ClassView
		})
	}
	checked, satisfied := 0, 0
	check := func(req rbac.Requirement) {
		if req.IsZero() || !hasView(req) {
			return
		}
		checked++
		if !viewer.Satisfies(req) {
			return
		}
		satisfied++
		if scopes, open := k.viewStartScopes(req); len(scopes) == 0 && !open {
			t.Errorf("the viewer seed satisfies %s but SEC-9 does not trip on it", req)
		}
	}
	for i, a := range all {
		check(rbac.AllOf(a))
		for _, b := range all[i+1:] {
			check(rbac.AllOf(a, b))
			check(rbac.AnyOf(a, b))
		}
	}
	if checked == 0 || satisfied == 0 {
		t.Fatalf("the oracle checked %d requirements and the viewer satisfied %d: the cell proves nothing", checked, satisfied)
	}
}
