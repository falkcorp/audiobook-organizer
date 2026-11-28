// file: internal/books/cutover_view_test.go
// version: 1.0.0
// guid: 9b4d7e12-3a85-4c60-b1f9-5e2c8a0d6f37
// last-edited: 2026-11-28

package books

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 reach, ruling 1a: the View snapshot M4 stamps for an imported kind says
// what HEAD's read gate said of that kind's runs. The cells below compare the
// stamped rule with HEAD's own reader (opaccess.Reader.MayReadFixer, the code
// that decides a Repairs run's readability at HEAD), for every fixer HEAD
// registers, the retired fixers and an unknown one, against holders built from
// the 13 atoms.

type noOpRows struct{}

func (noOpRows) GetOperationV2(string) (*database.OperationV2Row, error) { return nil, nil }

// oneFixer is a fixer lookup holding one fixer that asks for perms.
type oneFixer struct {
	repairs.Fixer
	id    string
	perms []auth.Permission
}

func (f oneFixer) ID() string                             { return f.id }
func (f oneFixer) RequiredPermissions() []auth.Permission { return f.perms }
func (f oneFixer) Get(id string) (repairs.Fixer, bool)    { return f, id == f.id }

func atomSet(names ...string) []auth.Permission {
	out := make([]auth.Permission, len(names))
	for i, n := range names {
		out[i] = auth.Permission(n)
	}
	return out
}

// holderSets are the permission sets the cells try: nothing, each atom alone,
// every atom, and every atom but one (the shape an all-of rule must tell from
// all of them).
func holderSets() map[string][]auth.Permission {
	all := auth.All()
	sets := map[string][]auth.Permission{"nothing": nil, "every atom (admin)": all}
	for _, a := range all {
		sets["only "+string(a)] = []auth.Permission{a}
		sets["all but "+string(a)] = slices.DeleteFunc(slices.Clone(all), func(p auth.Permission) bool { return p == a })
	}
	return sets
}

// nativeScopes is the scopes an account holding atoms is granted: the union of
// the atoms' scopes in books' table, which is what the identity cutover writes.
func nativeScopes(t testing.TB, atoms []auth.Permission) map[rbac.Permission]bool {
	t.Helper()
	held := map[rbac.Permission]bool{}
	for _, a := range atoms {
		ss, ok := atomScopes(a)
		if !ok {
			t.Fatalf("atom %q does not resolve to scopes", a)
		}
		for _, s := range ss {
			held[s] = true
		}
	}
	return held
}

// mayView is the View rule's verdict for a holder of the scopes held: the zero
// KindView is core's default (core:tasks.view), a Requires an all-of or any-of
// over its scopes. core:admin, which implies every scope (05-rbac 4.3), is not
// in any atom's scopes, so no cell needs it.
func mayView(v tasks.KindView, held map[rbac.Permission]bool) bool {
	req := v.Requires
	if req.IsZero() {
		return held["core:tasks.view"]
	}
	switch req.Kind() {
	case rbac.KindAllOf:
		for _, p := range req.Permissions() {
			if !held[p] {
				return false
			}
		}
		return true
	case rbac.KindAnyOf:
		return slices.ContainsFunc(req.Permissions(), func(p rbac.Permission) bool { return held[p] })
	default:
		return false
	}
}

// TestImportedKindView_RepairsKindsMatchHEADsReader: for each fixer id, each
// holder set, the stamped rule and HEAD's reader agree.
func TestImportedKindView_RepairsKindsMatchHEADsReader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	viewOf := importedKindView(headFixers)
	ids := []string{"batcave.penguin-sweep", "maintenance.relink-stale-series", "dedup.split-books"}
	for _, f := range headRegistry().List() {
		ids = append(ids, f.ID())
	}
	if len(ids) < 10 {
		t.Fatalf("only %d fixer ids to compare, want HEAD's registry plus the retired and unknown ones", len(ids))
	}
	sets := holderSets()
	cells := 0
	for _, id := range ids {
		kind := tasks.Kind("books.repairs." + id)
		view, stamp, err := viewOf(kind)
		if err != nil || !stamp {
			t.Fatalf("%s: ViewOf = %+v, stamp %v, %v; want a stamped rule", kind, view, stamp, err)
		}
		for name, held := range sets {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/x", nil)
			head := opaccess.NewRepairs(headFixers(), noOpRows{}, func(_ *gin.Context, p auth.Permission) bool {
				return slices.Contains(held, p)
			}).For(c).MayReadFixer("repairs.apply", id)
			native := mayView(view, nativeScopes(t, held))
			if head != native {
				t.Errorf("%s, holder %q: HEAD's reader says %v, the stamped rule %s says %v", kind, name, head, view.Requires, native)
			}
			cells++
		}
	}
	t.Logf("%d (fixer, holder) cells compared", cells)
}

// TestImportedKindView_OtherKindsTakeTheDefault: a kind that is not a Repairs
// fixer is stamped unrestricted (HEAD's MayRead is true for it), and core's own
// kinds are left to core.
func TestImportedKindView_OtherKindsTakeTheDefault(t *testing.T) {
	viewOf := importedKindView(headFixers)
	for _, k := range []tasks.Kind{"books.library.scan", "books.retired.batcave-sweep", "books.dedup.book-scan"} {
		v, stamp, err := viewOf(k)
		if err != nil || !stamp || !v.Requires.IsZero() {
			t.Errorf("%s: ViewOf = %+v, stamp %v, %v; want the unrestricted default, stamped", k, v, stamp, err)
		}
	}
	for _, k := range []tasks.Kind{"core.storage.compact", "core.diagnostics.export"} {
		if _, stamp, err := viewOf(k); err != nil || stamp {
			t.Errorf("%s: stamp %v, %v; want it left to core", k, stamp, err)
		}
	}
	// A library.view holder reads the default, one without it does not.
	if !mayView(tasks.KindView{}, nativeScopes(t, atomSet("library.view"))) || mayView(tasks.KindView{}, nativeScopes(t, atomSet("library.edit_metadata"))) {
		t.Error("the default rule is not 'library.view reads, others do not'")
	}
}

// TestImportedKindView_EveryAtomResolves is the "HEAD's gate can be told" check:
// a fixer's gate is made of atoms, and each one must resolve to scopes or the
// translation would silently narrow. It names the atom that does not.
func TestImportedKindView_EveryAtomResolves(t *testing.T) {
	var bad []string
	for _, a := range auth.All() {
		if _, ok := atomScopes(a); !ok {
			bad = append(bad, string(a))
		}
	}
	for _, f := range headRegistry().List() {
		for _, a := range repairs.RequiredPermissions(f) {
			if _, ok := atomScopes(a); !ok {
				bad = append(bad, fmt.Sprintf("%s (fixer %s)", a, f.ID()))
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("atoms with no native scopes, so HEAD's gate for them cannot be stamped: %s", strings.Join(bad, ", "))
	}
}

// TestImportedKindView_UnresolvedAtomIsAnErrorNotAnOpenKind: a fixer whose
// atom the table lacks fails the lookup, never records "unrestricted" and never
// drops the atom (which would be a narrower rule than HEAD's, and for an
// any-of-style mistake a wider one). The fixer also holds a resolving atom, so
// skipping the unresolved one would still leave a plausible requirement.
func TestImportedKindView_UnresolvedAtomIsAnErrorNotAnOpenKind(t *testing.T) {
	_, err := fixerView(oneFixer{id: "batcave.grapnel", perms: atomSet("library.edit_metadata", "batcave.nonexistent")}, "batcave.grapnel")
	if err == nil {
		t.Fatal("fixerView of an atom the table does not hold returned no error")
	}
}
