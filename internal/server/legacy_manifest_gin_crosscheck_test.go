// file: internal/server/legacy_manifest_gin_crosscheck_test.go
// version: 0.1.0
// guid: 9d4e7a20-3c1b-4a85-b6f2-8e0d5c7a1f34
// last-edited: 2026-11-22

package server_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Under core identity every books gin route also has a manifest row, and core's
// stage 13 enforces the row's requirement before the request reaches gin. The
// chain gin then runs refuses a caller the row admits whenever the row asks
// for less, and a row that asks for less than gin did is a widening the moment
// gin stops refusing. Since B10g books serves under core identity only and the
// two gin checks that refused more than any row (the admin role by name, and
// the conditional credential guard) no longer run there, so the row is the gate
// and these tests hold it to every gate gin still carries. checkLegacyManifest
// compares the two route sets only; these tests compare what each side
// requires.
//
// What the check does not model, stated so a green run is not read as more:
//   - Handler-internal checks (an in-handler atom test, wire_repairs_routes.go's
//     fixer scopes) are not gates of the chain. The cutover package lists and
//     exercises them (TestReachRegisteredPath_HiddenHandlerChecks).
//   - Guards.Interactive and Guards.Owner are compared by presence (the chain has
//     the gate, the row has the flag), not by what the guard does.
//   - gin's admin gate is modelled as it runs under core identity
//     (wire_admin_role.go's bridgedAdminGate): the route's own manifest
//     requirement, checked through the bridge. HEAD's role-name test behind it
//     went with B10g (owner ruling 2026-11-22, B10rn STOP 1), so a row that
//     carries one of the gate kind's scopes matches it; until B10g each such row
//     was a ROLE-NAME-GATE allow-list entry.
//   - A credRouteWhen guard is no gate under core identity since B10g: the
//     route's handler decides (books' key-for-another-user refusal, B10rn STOP 2,
//     TestReachRegisteredPath_KeyForAnotherUserIsRefusedByTheHandler; the rotate
//     stub's 410 to every caller). Until B10g each was a CONDITIONAL-GUARD
//     allow-list entry.
//   - The recorder runs on the non-identity engine. Every gate constructor
//     reports itself before its s.identity branch, so the gates' places are the
//     same under WithIdentity, and this check reads each kind as the identity
//     branch builds it; the only chain that differs is the two public invite
//     routes (wire_auth_routes.go), which carry no gate in either mode.
//
// The gin side is read from the real engine: every gate constructor reports
// itself while the engine is built and server.RecordedGateChains finds, for
// each route, the gates its chain carries (gate_trace.go,
// legacy_manifest_gin_gates_test.go). The manifest side is the row, its atom
// resolved through books.AtomTable. Both are compared by what a caller must
// hold.

// adminGateScopes are the native scopes a gin admin gate stands for, by the
// route's admin kind (05-rbac §6.2; wire_admin_role.go): a read-only
// introspection route asks books:admin.view, a route that rewrites data or
// serves a credential surface asks books:admin.debug or core:admin. gin's gate
// is the admin role by name, a test the manifest cannot say, so the row must
// carry at least one scope of the set: a row that carries none of them (or is
// not a scope requirement at all) admits a caller the role test refuses.
var adminGateScopes = map[string][]rbac.Permission{
	"read-only":            {"books:admin.view"},
	"always-authenticated": {"books:admin.debug", "core:admin"},
}

// absGateName is the name of the closure servermiddleware.ABSRequireAuth builds,
// the one gate of a chain whose constructor does not report itself (it lives in
// the ABS handlers' package, which has no access to gateTraceForTest).
const absGateName = "internal/server/middleware.ABSRequireAuth.func1"

// nonGateElements are the names of chain elements that are in no route's
// requirement: the perimeter shims, the body limits, the rate and login limits,
// a feature flag's guard, the /api redirect (setupRoutes.func4, pinned by
// number: a closure added above it renumbers it and fails the census, which
// is the point, so an inline gate in setupRoutes cannot pass as it), the event
// stream's shutdown hook. A chain element in neither this list nor absGateName
// fails TestLegacyManifest_GinChainElementsAreClassified: a gate has to be
// reported through gateTraceForTest or named here after review.
var nonGateElements = []string{
	"internal/server.perimeterSeen.func1",
	"internal/server.captureRawResponseController.func1",
	"internal/server.legacyMatchCheck.func1",
	"internal/server.(*Server).setupRoutes.func4",
	"internal/server/middleware.MaxRequestBodySize.func1",
	"internal/server/middleware.(*IPRateLimiter).Middleware.func1",
	"internal/server.loginCeilingMiddleware.func1",
	"internal/server.(*inviteRateLimits).middleware.func1",
	"internal/server.(*Server).endStreamsOnShutdown.func1",
	"internal/server/handlers/fpworker.(*Handler).flagGuard-fm",
	"internal/server/handlers/fpworker.bodyCap",
}

// isNonGate reports whether name is in nonGateElements.
func isNonGate(name string) bool {
	return slices.ContainsFunc(nonGateElements, func(p string) bool { return strings.HasSuffix(name, p) })
}

// resolveManifestReq resolves a row's requirement the way books' mount does
// (books.resolveRoutes): a legacy atom becomes the atom table's route
// requirement.
func resolveManifestReq(table []books.Atom, r rbac.Requirement) (rbac.Requirement, error) {
	if r.Kind() != rbac.KindLegacyAtom {
		return r, nil
	}
	for _, a := range table {
		if a.Name == r.Atom() {
			return a.Route, nil
		}
	}
	return rbac.Requirement{}, fmt.Errorf("atom %q is not in the atom table", r.Atom())
}

func scopeSet(r rbac.Requirement) map[rbac.Permission]bool {
	out := map[rbac.Permission]bool{}
	if r.Kind() == rbac.KindAllOf {
		for _, p := range r.Permissions() {
			out[p] = true
		}
	}
	return out
}

func gateString(g server.TracedGate) string {
	switch string(g.Kind) {
	case "perm":
		return "s.perm(" + g.Atom + ")"
	case "admin":
		return "admin gate(" + string(g.Admin) + ")"
	case "cred":
		if g.Conditional {
			return "credRouteWhen guard"
		}
		return "credRoute guard"
	default:
		return string(g.Kind) + " gate"
	}
}

// finding is one line of crossCheck's output, by the route key it is about.
type finding struct {
	kind, key, text string
}

// crossCheck compares each manifest row with the gates of its gin route's chain.
// It returns one finding per mismatch, its text
//
//	MANIFEST-WEAKER: METHOD path: manifest <req> vs gin <gate>
//
// when the row is satisfied by a caller the gate refuses, and
//
//	MANIFEST-STRONGER: METHOD path: manifest <req> vs gin <what the chain has>
//
// when the row asks for something no gate of the chain enforces (the converse,
// which also catches a recorder that lost a gate: a chain read short would
// otherwise hide every weaker row behind it).
func crossCheck(rows []httpapi.LegacyRoute, chains map[string]server.RouteChain, table []books.Atom) []finding {
	atoms := map[string]books.Atom{}
	for _, a := range table {
		atoms[a.Name] = a
	}
	var out []finding
	add := func(kind string, lr httpapi.LegacyRoute, text string) {
		out = append(out, finding{kind: kind, key: lr.Method + " " + lr.GinPath, text: fmt.Sprintf("%s: %s %s: %s", kind, lr.Method, lr.GinPath, text)})
	}
	for _, lr := range rows {
		key := lr.Method + " " + lr.GinPath
		chain, ok := chains[key]
		if !ok {
			add("NO-CHAIN", lr, "no gin route recorded for this manifest row")
			continue
		}
		m, err := resolveManifestReq(table, lr.Perm)
		if err != nil {
			add("UNRESOLVED", lr, err.Error())
			continue
		}
		switch m.Kind() {
		case rbac.KindPublic, rbac.KindAuthenticated, rbac.KindAllOf, rbac.KindDelegated:
		default:
			add("UNSUPPORTED", lr, fmt.Sprintf("manifest requirement %s is of a kind this check does not model", m))
			continue
		}
		weaker := func(format string, args ...any) {
			add("MANIFEST-WEAKER", lr, fmt.Sprintf("manifest %s vs gin %s", m, fmt.Sprintf(format, args...)))
		}
		stronger := func(format string, args ...any) {
			add("MANIFEST-STRONGER", lr, fmt.Sprintf("manifest %s vs gin %s", m, fmt.Sprintf(format, args...)))
		}
		have := scopeSet(m)
		covered := map[rbac.Permission]bool{}
		authed, credAlways, owner := false, false, false
		absGate := slices.ContainsFunc(chain.Other, func(name string) bool { return strings.HasSuffix(name, absGateName) })
		for _, g := range chain.Gates {
			switch string(g.Kind) {
			case "auth":
				authed = true
			case "perm":
				authed = true
				a, known := atoms[g.Atom]
				if !known {
					add("UNRESOLVED", lr, fmt.Sprintf("gin s.perm(%s): atom is not in the atom table", g.Atom))
					continue
				}
				var missing []string
				for _, s := range a.Scopes {
					covered[s] = true
					if !have[s] {
						missing = append(missing, string(s))
					}
				}
				if len(missing) > 0 {
					weaker("%s (needs %s; manifest lacks %s)", gateString(g), scopesString(a.Scopes), strings.Join(missing, ", "))
				}
			case "admin":
				authed = true
				want := adminGateScopes[string(g.Admin)]
				if len(want) == 0 {
					add("UNRESOLVED", lr, fmt.Sprintf("admin gate of unknown kind %q", g.Admin))
					continue
				}
				hit := false
				for _, s := range want {
					covered[s] = true
					hit = hit || have[s]
				}
				// Under identity the gate checks the row's own requirement, so a row
				// that holds one of the kind's scopes is the gate.
				if !hit {
					weaker("%s (needs one of %s)", gateString(g), scopesString(want))
				}
			case "cred":
				if g.Conditional {
					// No gate under identity since B10g: the handler decides.
					continue
				}
				credAlways = true
				if !lr.Guards.Interactive {
					weaker("%s (row has no Guards.Interactive)", gateString(g))
				}
			case "owner":
				owner = true
				if !lr.Guards.Owner || !lr.OwnerWhenSet {
					weaker("%s (row has no Guards.Owner with OwnerWhenSet)", gateString(g))
				}
			}
		}
		if absGate && (m.Kind() != rbac.KindDelegated || !lr.OwnAuth) {
			weaker("ABSRequireAuth (row is not Delegated with OwnAuth)")
		}
		if authed && m.Kind() == rbac.KindPublic {
			weaker("an authentication gate (row is Public)")
		}
		if authed && m.Kind() == rbac.KindDelegated {
			weaker("an authentication gate (row is Delegated, which gin's own authentication is not)")
		}

		for _, s := range slices.Sorted(func(yield func(rbac.Permission) bool) {
			for s := range have {
				if !yield(s) {
					return
				}
			}
		}) {
			if !covered[s] {
				stronger("no gate of the chain asks %s (gates: %s)", s, gatesString(chain))
			}
		}
		if m.Kind() == rbac.KindAuthenticated && !authed {
			stronger("no authentication gate (gates: %s)", gatesString(chain))
		}
		if m.Kind() == rbac.KindDelegated && !absGate {
			stronger("no ABSRequireAuth in the chain")
		}
		if lr.Guards.Interactive && !credAlways {
			stronger("no credRoute guard (row has Guards.Interactive)")
		}
		if lr.Guards.Owner && !owner {
			stronger("no owner gate (row has Guards.Owner)")
		}
	}
	slices.SortFunc(out, func(a, b finding) int { return strings.Compare(a.text, b.text) })
	return out
}

func scopesString(s []rbac.Permission) string {
	parts := make([]string, len(s))
	for i, p := range s {
		parts[i] = string(p)
	}
	return strings.Join(parts, ", ")
}

func gatesString(c server.RouteChain) string {
	if len(c.Gates) == 0 {
		return "none"
	}
	parts := make([]string, len(c.Gates))
	for i, g := range c.Gates {
		parts[i] = gateString(g)
	}
	return strings.Join(parts, " > ")
}

func crossCheckModes() []bool { return []bool{false, true} }

func crossCheckName(abs bool) string {
	if abs {
		return "abs_on"
	}
	return "abs_off"
}

// TestLegacyManifest_NeverWeakerThanGinChain: no manifest row asks for less
// than the gates of its gin route's chain, and none asks for something the
// chain does not enforce. The row sets are the same (checkLegacyManifest);
// here every row's requirement meets its route's gates. One subtest per row, so
// a mismatch is its own named failure.
func TestLegacyManifest_NeverWeakerThanGinChain(t *testing.T) {
	table := books.AtomTable()
	for _, abs := range crossCheckModes() {
		t.Run(crossCheckName(abs), func(t *testing.T) {
			chains := server.RecordedGateChains(t, abs)
			rows := server.LegacyRoutes(abs)
			require.NotEmpty(t, rows)
			findings := crossCheck(rows, chains, table)
			byKey := map[string][]string{}
			for _, f := range findings {
				byKey[f.key] = append(byKey[f.key], f.text)
			}
			for _, lr := range rows {
				key := lr.Method + " " + lr.GinPath
				t.Run(strings.ReplaceAll(key, " ", "_"), func(t *testing.T) {
					assert.Empty(t, byKey[key], "%s", strings.Join(byKey[key], "\n"))
				})
			}
		})
	}
}

// TestLegacyManifest_GinChainElementsAreClassified: every element of every
// route's chain is a recorded gate, ABSRequireAuth, the route's own handler, or
// named in nonGateElements. A gate that does not report itself would otherwise
// refuse callers in gin without the cross-check ever seeing it.
func TestLegacyManifest_GinChainElementsAreClassified(t *testing.T) {
	for _, abs := range crossCheckModes() {
		t.Run(crossCheckName(abs), func(t *testing.T) {
			chains := server.RecordedGateChains(t, abs)
			unclassified := map[string][]string{}
			for key, c := range chains {
				for _, name := range c.Other {
					if strings.HasSuffix(name, absGateName) || isNonGate(name) {
						continue
					}
					unclassified[name] = append(unclassified[name], key)
				}
			}
			for name, keys := range unclassified {
				slices.Sort(keys)
				t.Errorf("chain element %s is not a recorded gate or a classified non-gate (route %s and %d more): report it through gateTraceForTest or add it to nonGateElements after review",
					name, keys[0], len(keys)-1)
			}
		})
	}
}

// TestCrossCheck_SyntheticMismatches holds crossCheck itself to the shapes it
// exists to find, on hand-built rows and chains, so the rule stays pinned
// whatever the real manifest looks like: a row that asks less than the gate
// (weaker, in each gate kind), a row that asks more or names a gate the chain
// lacks (stronger, which is also what a recorder that lost a gate looks like),
// and a clean row.
func TestCrossCheck_SyntheticMismatches(t *testing.T) {
	table := books.AtomTable()
	row := func(perm rbac.Requirement) httpapi.LegacyRoute {
		return httpapi.LegacyRoute{Method: "POST", GinPath: "/api/v1/arkham", Perm: perm}
	}
	gate := func(kind, atom string) server.TracedGate {
		g := server.GateKindOf(kind)
		g.Atom = atom
		return g
	}
	chain := func(gs ...server.TracedGate) map[string]server.RouteChain {
		return map[string]server.RouteChain{"POST /api/v1/arkham": {Gates: gs}}
	}
	kinds := func(fs []finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.kind)
		}
		return out
	}
	auth := gate("auth", "")

	cases := []struct {
		name  string
		row   httpapi.LegacyRoute
		chain map[string]server.RouteChain
		want  []string
	}{
		{"exact atom row", row(rbac.LegacyAtom("users.manage")), chain(auth, gate("perm", "users.manage")), nil},
		{"row AuthRequired, gin s.perm", row(rbac.AnyAuthenticated), chain(auth, gate("perm", "users.manage")), []string{"MANIFEST-WEAKER"}},
		{"row Public, gin s.perm", row(rbac.Public), chain(auth, gate("perm", "library.view")), []string{"MANIFEST-WEAKER", "MANIFEST-WEAKER"}},
		{"row Public, gin auth only", row(rbac.Public), chain(auth), []string{"MANIFEST-WEAKER"}},
		{"row names one scope of a two-scope atom", row(rbac.AllOf("core:users.manage")), chain(auth, gate("perm", "users.manage")), []string{"MANIFEST-WEAKER"}},
		{"row names another atom's scopes", row(rbac.LegacyAtom("library.view")), chain(auth, gate("perm", "library.delete")), []string{"MANIFEST-STRONGER", "MANIFEST-STRONGER", "MANIFEST-WEAKER"}},
		{"two perm gates, row names one", row(rbac.LegacyAtom("library.view")), chain(auth, gate("perm", "library.view"), gate("perm", "library.delete")), []string{"MANIFEST-WEAKER"}},
		{"admin gate, row AuthRequired", row(rbac.AnyAuthenticated), chain(auth, server.AdminGateOf("read-only")), []string{"MANIFEST-WEAKER"}},
		{"admin gate, row carries its scope", row(rbac.AllOf("books:admin.view")), chain(auth, server.AdminGateOf("read-only")), nil},
		{"gin has no gate, row asks a scope", row(rbac.AllOf("books:view")), chain(), []string{"MANIFEST-STRONGER"}},
		{"recorder lost the perm gate", row(rbac.LegacyAtom("library.view")), chain(auth), []string{"MANIFEST-STRONGER", "MANIFEST-STRONGER"}},
		{"public row, no gate", row(rbac.Public), chain(), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := kinds(crossCheck([]httpapi.LegacyRoute{tc.row}, tc.chain, table))
			slices.Sort(got)
			slices.Sort(tc.want)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("credRoute guard, row lacks Interactive", func(t *testing.T) {
		lr := row(rbac.AnyAuthenticated)
		assert.Equal(t, []string{"MANIFEST-WEAKER"}, kinds(crossCheck([]httpapi.LegacyRoute{lr}, chain(auth, gate("cred", "")), table)))
		lr.Guards.Interactive = true
		assert.Empty(t, crossCheck([]httpapi.LegacyRoute{lr}, chain(auth, gate("cred", "")), table))
	})
	t.Run("conditional credential guard is no gate under identity", func(t *testing.T) {
		cond := gate("cred", "")
		cond.Conditional = true
		assert.Empty(t, crossCheck([]httpapi.LegacyRoute{row(rbac.AnyAuthenticated)}, chain(auth, cond), table))
	})
	t.Run("owner gate, row lacks Owner", func(t *testing.T) {
		lr := row(rbac.AnyAuthenticated)
		assert.Equal(t, []string{"MANIFEST-WEAKER"}, kinds(crossCheck([]httpapi.LegacyRoute{lr}, chain(auth, gate("owner", "")), table)))
	})
	t.Run("row with no chain", func(t *testing.T) {
		assert.Equal(t, []string{"NO-CHAIN"}, kinds(crossCheck([]httpapi.LegacyRoute{row(rbac.Public)}, map[string]server.RouteChain{}, table)))
	})
}
