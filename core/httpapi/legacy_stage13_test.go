// file: core/httpapi/legacy_stage13_test.go
// version: 0.1.0
// guid: 8b2625a6-44f3-466e-9227-d26fc861e0f0
// last-edited: 2026-11-21

package httpapi

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// This file holds the discharge of F4-07-RANDOM-ROLES (seams.d/F4-07.md,
// "Obligations on F4-08"): the reach guard in internal/books/cutover finds
// TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits here with go/ast
// and requires every legacy atom a manifest route names to occur as a string
// literal in THIS file. That is why s13Atoms lives here and nowhere else:
// the atom names are literals only in this table, so dropping an atom from it
// reopens the obligation instead of being hidden by the same name appearing in
// another test's cases. A literal elsewhere would let it pass unnoticed.

// s13Atoms is the test's own copy of books' atom table (05-rbac §6.1): each
// HEAD atom and the native scopes whose AllOf is its route requirement. The
// atoms are string literals on purpose, and every atom a legacy manifest route
// names is here. playlists.create gates no route (zero route requirement) and
// is not in the table; the resolver answers it separately.
//
// core cannot import internal/books, so this is a copy, and a copy that nothing
// checked would make "stage 13 matches the successor" a statement about a table
// nobody ships. internal/books pins it to books.AtomTable
// (TestAtomTable_MatchesStage13RandomRolesTable), name for name and scope for
// scope, reading this file with go/ast.
func s13Atoms() map[string][]rbac.Permission {
	return map[string][]rbac.Permission{
		"library.view":          {"books:view", "core:tasks.view"},
		"library.edit_metadata": {"books:edit"},
		"library.delete":        {"books:delete"},
		"library.organize":      {"books:organize"},
		"scan.trigger":          {"books:scan"},
		"integrations.manage":   {"core:integrations.manage", "books:integrations.manage"},
		"users.manage":          {"core:users.manage", "core:roles.view"},
		"settings.manage": {"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage"},
		"collections.manage": {"books:collections.manage"},
		"requests.create":    {"books:request"},
		"requests.approve":   {"books:request.approve"},
		"fingerprint.worker": {"books:fingerprint.worker"},
	}
}

// TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits is the
// custom-role half of the reach oracle over random native roles (05-http-api
// 8.1, SEC4-2): for every legacy atom of the table and random role patterns
// (sessions, keys, delegated sessions, confined keys, partial settings.manage
// roles, wildcards, admin, auth off), stage 13 admits a LegacyAtom route
// exactly when the atom's native successor, AllOf its table scopes, admits the
// principal. The property that matters is "never wider": a role admitted by
// stage 13 that the successor refuses fails first. "Never narrower" is held
// beside it, and both outcomes must occur for every atom, so neither side is
// vacuous and a generator that only yields the empty role fails.
//
// The run is seeded and reproducible: the seed is logged and named in every
// failure. It was renamed from TestStage13_LegacyAtomRandomRolesMatchTheSuccessor
// (cells and generator unchanged, moved here so the atom literals the reach
// guard reads are only the table's).
func TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits(t *testing.T) {
	cat := s13Catalog(t)
	atoms := s13Atoms()
	names := make([]string, 0, len(atoms))
	for a := range atoms {
		names = append(names, a)
	}
	slices.Sort(names)
	var concrete []rbac.Permission
	for spec := range cat.All() {
		concrete = append(concrete, spec.Name)
	}
	slices.Sort(concrete)
	wild := []rbac.Permission{"books:*", "core:*", "core:system.*", "core:tasks.*", "books:request.*"}
	settings := atoms["settings.manage"]
	const seedHi, seedLo uint64 = 13, 2026
	rng := rand.New(rand.NewPCG(seedHi, seedLo))
	const rounds = 600
	t.Logf("seed PCG(%d, %d), %d rounds over %d atoms %v", seedHi, seedLo, rounds, len(names), names)
	pick := func(pool []rbac.Permission, n int) []rbac.Permission {
		out := make([]rbac.Permission, 0, n)
		for range n {
			out = append(out, pool[rng.IntN(len(pool))])
		}
		return out
	}
	roles := func() []rbac.Permission {
		switch rng.IntN(10) {
		case 0: // a custom role holding all but one of settings.manage's scopes
			drop := rng.IntN(len(settings))
			return append(slices.Clone(settings[:drop]), settings[drop+1:]...)
		case 1:
			return append(pick(concrete, rng.IntN(4)), rbac.CoreAdmin)
		case 2:
			return append(pick(concrete, rng.IntN(6)), pick(wild, 1+rng.IntN(2))...)
		case 3: // a whole atom's scopes, sometimes plus noise
			return append(slices.Clone(atoms[names[rng.IntN(len(names))]]), pick(concrete, rng.IntN(3))...)
		default:
			return pick(concrete, rng.IntN(12))
		}
	}
	opts := AuthzOptions{OwnerRequired: true, ResolveAtom: s13Resolver, Catalog: cat}
	admits, denies := map[string]int{}, map[string]int{}
	for i := range rounds {
		var p *auth.Principal
		switch rng.IntN(6) {
		case 0:
			p = s13Key(cat, roles(), pick(concrete, 1+rng.IntN(8))...)
		case 1:
			p = s13Delegated(cat, roles()...)
		case 2:
			p = s13Key(cat, roles(), "books:fingerprint.worker")
		case 3:
			if rng.IntN(4) == 0 {
				p = s13AuthOff(cat)
				break
			}
			fallthrough
		default:
			p = s13Session(cat, roles()...)
		}
		for _, atom := range names {
			got := Authorize(AuthzRequest{Perm: rbac.LegacyAtom(atom), Principal: p}, opts).Admitted()
			successor := p.Perms.Satisfies(rbac.AllOf(atoms[atom]...))
			if got && !successor {
				t.Fatalf("seed PCG(%d, %d) round %d: %s admitted %s (perms %v), which its successor refuses (stage 13 is wider)",
					seedHi, seedLo, i, atom, p.Method, p.Perms.Concrete())
			}
			if !got && successor {
				t.Fatalf("seed PCG(%d, %d) round %d: %s refused %s (perms %v), which its successor admits",
					seedHi, seedLo, i, atom, p.Method, p.Perms.Concrete())
			}
			if got {
				admits[atom]++
			} else {
				denies[atom]++
			}
		}
	}
	for _, atom := range names {
		if admits[atom] == 0 || denies[atom] == 0 {
			t.Errorf("%s: %d admits, %d denies over %d rounds (seed PCG(%d, %d)); both must occur",
				atom, admits[atom], denies[atom], rounds, seedHi, seedLo)
		}
	}
	t.Logf("admits %v, denies %v", admits, denies)
}
