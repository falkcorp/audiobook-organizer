// file: internal/books/cutover/reach_oracle_test.go
// version: 1.0.0
// guid: c27d5a40-9e18-4b63-8f02-3a6d1e7b9c54
// last-edited: 2026-11-13

package cutover

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/audiobook-organizer/internal/server"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The reach oracle (F4-07): what every principal of 05-rbac section 12 may
// reach on R0, recorded from the R0 release binary (internal/books/rbacreach,
// scripts/r2corpus/record_r0_reach.py), against what the same principals reach
// after M3 on the migrated corpus, under the manifest's route requirements
// resolved through the atom table.
//
// This file is the books half. The core-route half (auth-off across every core
// route, the run on the registered path) is F4-08's obligation; see
// docs/agent-tasks/platform-core/seams.d/F4-07.md.

const reachDir = "internal/books/rbacreach"

// ---- the recorded R0 answers ----

type reachPrincipal struct {
	Name   string   `json:"name"`
	Kind   string   `json:"kind"` // session, key, anonymous, authoff
	User   string   `json:"user"`
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

type reachPrincipalsDoc struct {
	Principals []reachPrincipal `json:"principals"`
}

type reachR0Route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	ABSOnly bool   `json:"abs_only"`
	Status  []int  `json:"status"`
	Verdict string `json:"verdict"` // one letter per principal, in Principals' order: A allow, D deny
}

func (r reachR0Route) key() string { return r.Method + " " + r.Path }

type reachR0Task struct {
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
	Approver    string   `json:"approver"`
	Verdict     string   `json:"verdict"`
}

type reachR0Approval struct {
	RunID   string         `json:"run_id"`
	Run     map[string]any `json:"run"`
	Verdict string         `json:"verdict"` // A, D, or N (the probe did not reach the approver check)
}

type reachR0Doc struct {
	R0Tag           string            `json:"r0_tag"`
	RouteCount      int               `json:"route_count"`
	Principals      []string          `json:"principals"`
	Routes          []reachR0Route    `json:"routes"`
	UniformNotFound []string          `json:"uniform_not_found"`
	Tasks           []reachR0Task     `json:"tasks"`
	Approvals       []reachR0Approval `json:"approvals"`
}

func reachReadJSON(t testing.TB, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(reachDir), name))
	if err != nil {
		t.Fatalf("the recorded R0 answers: %v (regenerate with scripts/r2corpus/record_r0_reach.py)", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

func reachLoad(t testing.TB) (*reachPrincipalsDoc, *reachR0Doc) {
	t.Helper()
	var pd reachPrincipalsDoc
	var vd reachR0Doc
	reachReadJSON(t, "principals.json", &pd)
	reachReadJSON(t, "verdicts.json", &vd)
	names := make([]string, len(pd.Principals))
	for i, p := range pd.Principals {
		names[i] = p.Name
	}
	if !slices.Equal(names, vd.Principals) {
		t.Fatalf("principals.json and verdicts.json list different principals:\n%v\n%v", names, vd.Principals)
	}
	for _, r := range vd.Routes {
		if len(r.Verdict) != len(names) {
			t.Fatalf("%s: %d verdict cells for %d principals", r.key(), len(r.Verdict), len(names))
		}
	}
	if vd.RouteCount != len(vd.Routes) {
		t.Fatalf("verdicts.json says %d routes and holds %d", vd.RouteCount, len(vd.Routes))
	}
	return &pd, &vd
}

// ---- seeding the principals into the migrated corpus ----

// reachKeyID is the deterministic HEAD key ID of the i-th key principal, a
// valid ULID the corpus has no row for.
func reachKeyID(i int) string { return fmt.Sprintf("01M7R2%020d", i) }

func reachKeyRaw(i int) string { return fmt.Sprintf("reach-oracle-fixture-%02d", i) }

// reachSeed is the overlay store plus one HEAD key row per key principal,
// owned by the principal's user. keyID maps a principal name to its key's ID;
// owner maps a username to the user ID the corpus holds.
type reachSeed struct {
	keyID map[string]string
	owner map[string]string
}

func (s *reachSeed) option(principals []reachPrincipal) func(*idRig) {
	return func(r *idRig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			ctx := context.Background()
			s.keyID, s.owner = map[string]string{}, map[string]string{}
			var rows []seedRow
			for i, p := range principals {
				if p.Kind != "key" {
					continue
				}
				if _, ok := s.owner[p.User]; !ok {
					raw, err := lr.Get(ctx, []byte("idx:user:username:"+strings.ToLower(p.User)))
					if err != nil || len(raw) == 0 {
						t.Fatalf("the overlay has no user %q: %v", p.User, err)
					}
					s.owner[p.User] = string(raw)
				}
				id := reachKeyID(i)
				s.keyID[p.Name] = id
				rows = append(rows, headKeyRows(id, s.owner[p.User], reachKeyRaw(i), p.Scopes, "active")...)
			}
			tmp := &idRig{}
			idRows(rows...)(tmp)
			tmp.mutate(t, lr)
		}
	}
}

// ---- the R2 side: each principal's checker, each route's gate ----

// reachWho is one principal as R2 evaluates it. chk is nil for the anonymous
// principal, which holds nothing.
type reachWho struct {
	name string
	chk  *reachChecker
}

// reachRolePatterns are the patterns of the roles a migrated user holds: a
// seed role's native set from the cutover build's seed, a custom role's whole
// set from what M3 imported.
func reachRolePatterns(t testing.TB, snap *idSnap, roles []rbac.RoleName) []rbac.Permission {
	t.Helper()
	seeds := map[string][]rbac.Permission{}
	for _, s := range idSeedRoles() {
		seeds[s.ID] = s.Permissions
	}
	var out []rbac.Permission
	for _, name := range roles {
		if p, ok := seeds[string(name)]; ok {
			out = append(out, p...)
			continue
		}
		role, ok := snap.Roles[string(name)]
		if !ok {
			t.Fatalf("a migrated user holds role %q, which is neither a seed role nor an imported one", name)
		}
		out = append(out, role.Added...)
	}
	return out
}

// reachWhos builds the checkers of every principal from what M3 left.
func reachWhos(t testing.TB, snap *idSnap, seed *reachSeed, principals []reachPrincipal, cat *rbac.Catalog) []reachWho {
	t.Helper()
	out := make([]reachWho, len(principals))
	for i, p := range principals {
		out[i].name = p.Name
		switch p.Kind {
		case "anonymous":
		case "authoff":
			out[i].chk = reachAuthDisabled(cat)
		case "session":
			u, ok := snap.Users[seed.ownerOf(t, snap, p.User)]
			if !ok {
				t.Fatalf("%s: M3 left no user %q", p.Name, p.User)
			}
			out[i].chk = reachCompile(cat, reachRolePatterns(t, snap, u.User.Roles), nil, true)
		case "key":
			k, ok := snap.Keys[seed.keyID[p.Name]]
			if !ok {
				t.Fatalf("%s: M3 left no key %s", p.Name, seed.keyID[p.Name])
			}
			u, ok := snap.Users[string(k.Key.Owner)]
			if !ok {
				t.Fatalf("%s: M3 left no owner %s", p.Name, k.Key.Owner)
			}
			if len(k.Key.Scopes) == 0 {
				t.Fatalf("%s: M3 wrote a key with no scopes (R3-1: never empty)", p.Name)
			}
			out[i].chk = reachCompile(cat, reachRolePatterns(t, snap, u.User.Roles), slices.Clone(k.Key.Scopes), false)
		default:
			t.Fatalf("%s: unknown principal kind %q", p.Name, p.Kind)
		}
	}
	return out
}

// ownerOf is the user ID of a username: the corpus's for a user a key owns,
// else found in the migrated users.
func (s *reachSeed) ownerOf(t testing.TB, snap *idSnap, username string) string {
	t.Helper()
	if id, ok := s.owner[username]; ok {
		return id
	}
	for id, u := range snap.Users {
		if strings.EqualFold(u.User.Username, username) {
			s.owner[username] = id
			return id
		}
	}
	t.Fatalf("M3 left no user named %q", username)
	return ""
}

// reachGate is what stage 13 asks of one route.
type reachGate struct {
	delegated   bool // OwnAuth: enforced inside the ABS handler, outside this oracle
	req         rbac.Requirement
	interactive bool // Guards.Interactive: a key and the auth-disabled principal are refused first
}

// reachUnenforced are the atoms no route enforces: books' table gives them a
// zero route requirement, and a route naming one is an error (resolveRoutes).
var reachUnenforced = map[string]bool{"playlists.create": true}

// reachResolve is books' routeRequirement and resolveRoutes over atoms: a
// LegacyAtom becomes AllOf every scope of the atom's row. A route whose atom
// the table lacks, or no route may enforce, is an error, never a gate.
func reachResolve(atoms []m3.AtomRow, lr httpapi.LegacyRoute) (reachGate, error) {
	g := reachGate{req: lr.Perm, interactive: lr.Guards.Interactive, delegated: lr.OwnAuth}
	if lr.Perm.Kind() == rbac.KindDelegated {
		g.delegated = true
	}
	if lr.Perm.Kind() != rbac.KindLegacyAtom {
		if !g.delegated && lr.Perm.IsZero() {
			return g, fmt.Errorf("%s %s: no requirement", lr.Method, lr.GinPath)
		}
		return g, nil
	}
	for _, a := range atoms {
		if a.Name != lr.Perm.Atom() {
			continue
		}
		if reachUnenforced[a.Name] {
			break
		}
		g.req = rbac.AllOf(a.Scopes...)
		if g.req.IsZero() {
			return g, fmt.Errorf("%s %s: atom %q resolves to no requirement", lr.Method, lr.GinPath, a.Name)
		}
		return g, nil
	}
	return g, fmt.Errorf("%s %s: atom %q has no route requirement in the atom table", lr.Method, lr.GinPath, lr.Perm.Atom())
}

// allows is stage 13's decision: the interactive guard first (a key and
// auth-off never pass it), then the requirement. A Public route is open to
// every principal, the anonymous one included.
func (g reachGate) allows(w reachWho) bool {
	if g.req.Kind() == rbac.KindPublic {
		return true
	}
	if w.chk == nil {
		return false
	}
	if g.interactive && !w.chk.interactive {
		return false
	}
	return w.chk.satisfies(g.req)
}

// reachR2 is the books half of R2's verdicts: one letter per principal, per
// manifest route, in whos' order. delegated lists the OwnAuth routes left out.
type reachR2 struct {
	verdict   map[string]string
	delegated []string
}

// reachEvaluate resolves every manifest route through atoms and evaluates it
// for every principal. It reads no store: the principals' checkers and the atom
// table are its whole input, so the same function runs under a changed table.
func reachEvaluate(atoms []m3.AtomRow, routes []httpapi.LegacyRoute, whos []reachWho) (*reachR2, error) {
	out := &reachR2{verdict: map[string]string{}}
	var errs []string
	for _, lr := range routes {
		key := lr.Method + " " + lr.GinPath
		g, err := reachResolve(atoms, lr)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if g.delegated {
			out.delegated = append(out.delegated, key)
			continue
		}
		var b strings.Builder
		for _, w := range whos {
			if g.allows(w) {
				b.WriteByte('A')
			} else {
				b.WriteByte('D')
			}
		}
		out.verdict[key] = b.String()
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%d routes do not resolve:\n%s", len(errs), strings.Join(errs, "\n"))
	}
	sort.Strings(out.delegated)
	return out, nil
}

// ---- the diff ----

// reachCell is one (route, principal) where R0 and R2 disagree.
type reachCell struct {
	route     string
	principal string
	r0, r2    byte
}

func (c reachCell) String() string {
	return fmt.Sprintf("%s / %s: R0 %c, R2 %c", c.route, c.principal, c.r0, c.r2)
}

// reachDiffResult is the whole comparison: cells that differ, routes only one
// side has, and the count of cells compared.
type reachDiffResult struct {
	cells     []reachCell
	r0Only    []string
	r2Only    []string
	compared  int
	delegated []string // on R2 and R0, but enforced inside the ABS handler
}

// reachDiff compares R0's recorded verdicts with R2's. It is pure: both sides
// are values. Routes only one side has are listed, never skipped; a delegated
// route is counted and listed.
func reachDiff(r0 *reachR0Doc, r2 *reachR2) reachDiffResult {
	res := reachDiffResult{delegated: r2.delegated}
	seen := map[string]bool{}
	deleg := map[string]bool{}
	for _, k := range r2.delegated {
		deleg[k] = true
	}
	for _, r := range r0.Routes {
		k := r.key()
		seen[k] = true
		if deleg[k] {
			continue
		}
		v2, ok := r2.verdict[k]
		if !ok {
			res.r0Only = append(res.r0Only, k)
			continue
		}
		for i := range r0.Principals {
			res.compared++
			if r.Verdict[i] != v2[i] {
				res.cells = append(res.cells, reachCell{route: k, principal: r0.Principals[i], r0: r.Verdict[i], r2: v2[i]})
			}
		}
	}
	for k := range r2.verdict {
		if !seen[k] {
			res.r2Only = append(res.r2Only, k)
		}
	}
	sort.Strings(res.r0Only)
	sort.Strings(res.r2Only)
	return res
}

// reachManifest is books' ABS-on manifest, the one the route dump covers.
func reachManifest() []httpapi.LegacyRoute { return server.LegacyRoutes(true) }

// ---- running the oracle ----

// reachMigrate runs M3 over the seeded overlay with atoms as the table it
// migrates by, and returns what it left. The seed and the snapshot are what
// reachEvaluate's principals are built from.
func reachMigrate(t testing.TB, pd *reachPrincipalsDoc, atoms []m3.AtomRow) (*idSnap, *reachSeed) {
	t.Helper()
	seed := &reachSeed{}
	rig := newIDRig(t, "overlay", seed.option(pd.Principals), func(r *idRig) {
		r.config = func(c *m3.Config) { c.Atoms = atoms }
	})
	snap, err := rig.run(t)
	if err != nil {
		t.Fatalf("M3 over the overlay corpus: %v", err)
	}
	if snap.State != "done" {
		t.Fatalf("M3 left the cutover in state %q, want done", snap.State)
	}
	return snap, seed
}

// reachCompare is the whole oracle for one pair of atom tables: m3Atoms is the
// table M3 migrates by, routeAtoms the table every route resolves through. They
// are one table, except in the mutation checks that change one and not the other.
func reachCompare(t testing.TB, pd *reachPrincipalsDoc, vd *reachR0Doc, m3Atoms, routeAtoms []m3.AtomRow) (reachDiffResult, []reachWho) {
	t.Helper()
	snap, seed := reachMigrate(t, pd, m3Atoms)
	whos := reachWhos(t, snap, seed, pd.Principals, idCatalog(t))
	r2, err := reachEvaluate(routeAtoms, reachManifest(), whos)
	if err != nil {
		t.Fatal(err)
	}
	return reachDiff(vd, r2), whos
}
