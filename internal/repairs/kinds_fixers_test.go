// file: internal/repairs/kinds_fixers_test.go
// version: 1.1.0
// guid: 9e5b2d71-4a8c-4e03-b6f9-1d3c7a0e5b84
// last-edited: 2026-11-12

package repairs_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// These tests build the real maintenance plugin's registry, as that package's
// own tests do, and ask what the 22 fixers it registers at this base become.

// atomScopes is books' atom table, the resolver the kernel hands KindDeps.
func atomScopes(atom auth.Permission) ([]rbac.Permission, bool) {
	for _, a := range books.AtomTable() {
		if a.Name == string(atom) && len(a.Scopes) > 0 {
			return a.Scopes, true
		}
	}
	return nil, false
}

// noDeps is a plugin with no deps at all: building the registry reads none, and
// the tests here only ask what the fixers become. (What the fixers read when
// they run is TestFixers_PlanReplanAndApplyReadOnlyTheShellStores in the
// maintenance package, with working stores.)
type noDeps struct{ maintenance.ServerDeps }

// retiredAtBase are the fixers `52e732a28` retired after `ac4368bdb`: the
// brief's 25 are 22 at this base.
var retiredAtBase = []string{"maintenance.normalize-letter-l-ordinals", "maintenance.relink-stale-series", "dedup.split-books"}

func registryAt(t *testing.T) *repairs.Registry {
	t.Helper()
	return maintenance.New(noDeps{}).Repairs()
}

func TestRepairsKinds_Count(t *testing.T) {
	fixers := registryAt(t).List()
	require.Len(t, fixers, 22, "the maintenance plugin registers 22 fixers at this base (25 at ac4368bdb less the 3 retired since)")
	defs := repairs.Kinds(fixers, repairs.KindDeps{Scopes: atomScopes})
	require.Len(t, defs, len(fixers))

	seen := map[tasks.Kind]bool{}
	for i, d := range defs {
		want := tasks.Kind(repairs.KindPrefix + fixers[i].ID())
		require.Equal(t, want, d.Kind(), "kinds follow the lane's order")
		_, err := tasks.ParseKind(string(d.Kind()))
		require.NoError(t, err, "%s is a valid kind", d.Kind())
		require.False(t, seen[d.Kind()], "kinds are distinct: %s", d.Kind())
		seen[d.Kind()] = true
		require.Equal(t, tasks.ShapeFixer, d.Shape())
		require.Empty(t, tasks.Problems(d), "%s has no constructor problem", d.Kind())
	}
	for _, id := range retiredAtBase {
		require.False(t, seen[tasks.Kind(repairs.KindPrefix+id)], "%s is retired", id)
	}
	// The fixer id to kind table, in the lane's order, for the PR description.
	for i, f := range fixers {
		t.Logf("fixer %s ==> %s", f.ID(), defs[i].Kind())
	}
}

func TestRepairsKinds_Conformance(t *testing.T) {
	fixers := registryAt(t).List()
	taskstest.Conformance(t, repairs.Kinds(fixers, repairs.KindDeps{Scopes: atomScopes})...)
}

// Every fixer keeps HEAD's gate on its kind: starting, approving and reading
// the plan need the scopes of the permissions it names, and an admin-only
// fixer is not left at the default.
func TestRepairsKinds_EveryFixersGateResolves(t *testing.T) {
	fixers := registryAt(t).List()
	defs := repairs.Kinds(fixers, repairs.KindDeps{Scopes: atomScopes})
	for i, f := range fixers {
		c := tasks.CommonOf(defs[i])
		var want []rbac.Permission
		for _, atom := range repairs.RequiredPermissions(f) {
			s, ok := atomScopes(atom)
			require.True(t, ok, "%s: atom %s resolves", f.ID(), atom)
			want = append(want, s...)
		}
		require.ElementsMatch(t, want, c.Perms.Start.Permissions(), f.ID())
		require.ElementsMatch(t, want, c.Perms.Approve.Permissions(), f.ID())
		require.ElementsMatch(t, want, c.Perms.View.Permissions(), f.ID())
	}
}
