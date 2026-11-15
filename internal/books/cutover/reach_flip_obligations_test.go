// file: internal/books/cutover/reach_flip_obligations_test.go
// version: 2.3.0
// guid: 0b7d4e92-6a35-4c18-9f40-d82c1a5e7b36
// last-edited: 2026-11-15

package cutover

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// The flip's obligations (round-3 MINOR-C, redesigned in round 5 for MINOR-D).
//
// Books may mount with LegacyAuthCore only when this package's oracle has
// nothing left that it knows it does not compare. Six things are obligations of
// F4-08 (or the handler migration); each is a row of reachObligations with a
// NAMED discharge test and a bar. Nobody keeps a list of the open ones: the open
// set is computed (reachOpenObligations) and reachFlipProblems fails at the flip
// once per open row.
//
// A row's bar is the set of cases a discharge must cover (cells, routes, atoms),
// computed here from the recording, the diff and the route table, never from a
// hand-edited list and never from the name of a function that happens to hold a
// row. A row clears only when both hold:
//
//  1. a test function with the row's name exists in the row's directory, found
//     with go/ast: no receiver, a *testing.T parameter, it asserts something
//     (a t.Fatal/Error/Run call, or a call that is handed t) and it never skips;
//  2. the coverage it declares meets the bar: for a test in this package, the
//     Covers function it registers in reachDischarges (F4-08 appends to that
//     slice in an init in the discharge test's file); for a test in another
//     directory, the bar's items that occur as string literals in the file that
//     holds it. A row with exact set must be covered exactly (a replay compares
//     every delegated cell, no more and no fewer).
//
// What this does NOT prove, plainly: declared coverage is a claim, not a proof.
// A test can declare the full bar and assert nothing about it, and nothing here
// runs the test. What the design removes is the cheap ways to close a row: the
// rename that moved the evidence (the round-4 C1: HANDLER-REFUSALS read a row
// function's name, so renaming the function emptied the bar while the cells
// still differed), the predicate swapped at one of two call sites (C2: the
// owner predicate and the owner-set model are one function now, reachOwnerAssured),
// and the permanent facts of the recording that no discharge could change. The
// barrier that remains is review of the change that registers a discharge, and
// the seams file (seams.d/F4-07.md) naming every row and its test.

// reachFlipObligation is one open obligation, as the flip's message names it.
type reachFlipObligation struct {
	name      string
	discharge string
}

// reachObligation is one row: what must be proven before the flip, by which
// test, and the bar the proof must meet.
type reachObligation struct {
	name string // F4-07-..., as seams.d/F4-07.md and the todo fragment name it
	// discharge says what discharges it and where it is stated.
	discharge string
	// dir is where the discharge test lives, relative to this package, and test
	// its name. "." means this package, whose test declares its coverage through
	// reachDischarges.
	dir, test string
	// unit names what the bar counts, for the messages.
	unit string
	// exact: the declared coverage must equal the bar, not only contain it.
	exact bool
	// bar computes the cases the discharge must cover.
	bar func(in reachObligationInput) []string
}

// reachAuthOffName is the auth-disabled principal in the recording's table.
const reachAuthOffName = "authoff"

// reachCoreOwnerRoutes are the owner routes core's own router carries, which no
// manifest holds. POST /api/core/v1/system/factory-reset is the successor of the
// R0-only reset pair (05-rbac 6.2); TestReachObligations_TheCoreOwnerRouteIsStillCores
// holds the claim to core's source.
var reachCoreOwnerRoutes = []string{"POST /api/core/v1/system/factory-reset"}

const reachCoreOwnerSource = "../../../core/internal/storagehttp/routes.go"

// reachObligations are the rows. The row names are held to the seams file in both
// directions (reachObligationProblems), so a row cannot be dropped without the
// seams losing its statement too.
var reachObligations = []reachObligation{
	{
		name:      "F4-07-ABS-REPLAY",
		discharge: "seams.d/F4-07.md, Obligations on F4-08, ABS replay: replay R0's recording of the delegated ABS routes against the registered path after M3 moves keys and roles into core, and state F-1's outcome on that surface",
		dir:       ".", test: "TestReachRegisteredPath_AbsReplay", unit: "delegated cells", exact: true,
		bar: reachBarABSReplay,
	},
	{
		name:      "F4-07-HANDLER-REFUSALS",
		discharge: "seams.d/F4-07.md, Obligations on F4-08, handler refusals: prove the auth-off handler refusals on the registered path (port TestReachHeadHandlers_RefuseTheAuthDisabledPrincipal, or delete the credential routes)",
		dir:       ".", test: "TestReachRegisteredPath_HandlerRefusals", unit: "auth-off cells",
		bar: reachBarHandlerRefusals,
	},
	{
		name:      "F4-07-HIDDEN-HANDLER-CHECKS",
		discharge: "seams.d/F4-07.md, Obligations on F4-08, hidden handler checks: prove each in-handler permission check site (reachHiddenSites) on the registered path, with a real id, on every route that reaches it",
		dir:       ".", test: "TestReachRegisteredPath_HiddenHandlerChecks", unit: "in-handler site routes",
		bar: reachBarHiddenHandlerChecks,
	},
	{
		name:      "F4-07-OWNER-SET",
		discharge: "seams.d/F4-07.md, Obligations on F4-08, owner set: with internal/cfaccess configured and an owner set, assert on the registered path that a non-owner admin session is refused on every owner route",
		dir:       ".", test: "TestReachRegisteredPath_OwnerSet", unit: "owner cases",
		bar: reachBarOwnerSet,
	},
	{
		name:      "F4-07-OWNER-ALLOW",
		discharge: "seams.d/F4-07.md, Obligations on F4-08, owner allow: on every owner route the Access-verified owner who holds the requirement is admitted and one who lacks it is refused; R0's allow side is unrecorded (the recording boot has no Access verifier and no issuer), so both are asserted on the registered path",
		dir:       ".", test: "TestReachRegisteredPath_OwnerAllow", unit: "owner cases",
		bar: reachBarOwnerAllow,
	},
	{
		name:      "F4-07-RANDOM-ROLES",
		discharge: "seams.d/F4-07.md, Obligations on F4-08, random roles: for random native roles, stage 13 admits a legacy route only if the route's successor admits it (core/httpapi, the stage-13 decision); R0 has no role CRUD, so no recording holds it",
		dir:       "../../../core/httpapi", test: "TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits", unit: "legacy atoms",
		bar: reachBarRandomRoles,
	},
}

// reachDischarge is a discharge test's registration in this package: the row it
// closes, the test (which must exist in this package, found with go/ast) and the
// coverage it declares. Covers must come from the same derivation the test's own
// loop uses, so a test and its declaration cannot drift apart unnoticed; it is
// still a claim (see the comment at the top of this file).
type reachDischarge struct {
	Obligation string
	Test       string
	Covers     func() []string
}

// reachDischarges are the registered discharges of this package's tests. F4-08
// appends in an init in the file that holds the discharge test. Empty today: every
// row is open.
var reachDischarges []reachDischarge

// reachObligationInput is what the bars and the discharge lookup read.
type reachObligationInput struct {
	vd       *reachR0Doc
	res      reachDiffResult
	manifest []httpapi.LegacyRoute
	// assured is the owner model's predicate (reachOwnerAssured), the same
	// function reachOwnerSetProblems judges the recording with.
	assured    func(string) bool
	discharges []reachDischarge
	// find locates a test function in a directory; reachFindTest in production.
	find func(dir, name string) (*reachFoundTest, error)
}

func reachSorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reachBarABSReplay: every recorded cell of every route that is both recorded
// abs_only and still delegated on R2: the cells nothing else compares. The 11
// Public-gated abs_only routes are compared by the oracle and are not in it.
func reachBarABSReplay(in reachObligationInput) []string {
	deleg := map[string]bool{}
	for _, k := range in.res.delegated {
		deleg[k] = true
	}
	set := map[string]bool{}
	for _, r := range in.vd.Routes {
		if !r.ABSOnly || !deleg[r.key()] {
			continue
		}
		for _, p := range in.vd.Principals {
			set[r.key()+"|"+p] = true
		}
	}
	return reachSorted(set)
}

// reachBarHandlerRefusals: the auth-off cells the router admits and R0's handler
// refused, on routes that are not Public. Read from the diff itself, so renaming
// or moving the rows that claim them changes nothing; the cells go only when the
// routes are deleted or tightened so R2 stops differing. A Public route (login,
// refresh) answers the probe's dummy credentials the same way for everyone: that
// is a recorder limit, not a handler's refusal of the auth-off principal.
func reachBarHandlerRefusals(in reachObligationInput) []string {
	public := map[string]bool{}
	for _, lr := range in.manifest {
		if lr.Public || lr.Perm.Kind() == rbac.KindPublic {
			public[lr.Method+" "+lr.GinPath] = true
		}
	}
	set := map[string]bool{}
	for _, c := range in.res.cells {
		if c.principal == reachAuthOffName && c.r0 == 'D' && c.r2 == 'A' && !public[c.route] {
			set[c.route+"|"+c.principal] = true
		}
	}
	return reachSorted(set)
}

// reachOwnerRoutes are the routes whose reach the owner decides: the manifest's
// Guards.Owner routes, read from the evaluation the cells came from, and core's
// own owner routes.
func reachOwnerRoutes(in reachObligationInput) []string {
	set := map[string]bool{}
	if in.res.r2 != nil {
		for k, owner := range in.res.r2.ownerGuard {
			if owner {
				set[k] = true
			}
		}
	}
	for _, k := range reachCoreOwnerRoutes {
		set[k] = true
	}
	return reachSorted(set)
}

// reachBarOwnerSet: on every owner route, a non-owner admin is refused on the
// registered path. R0's side of it is recorded (owner_set); the registered path's
// is not.
func reachBarOwnerSet(in reachObligationInput) []string {
	var out []string
	for _, k := range reachOwnerRoutes(in) {
		out = append(out, k+"|non-owner admin refused")
	}
	return out
}

// reachBarOwnerAllow: on every owner route, an owner lacking the requirement is
// refused (no recording can hold it: the table has no owner), and the verified
// owner who holds it is admitted unless the recording already shows an allowed
// cell for a principal the owner model treats as the owner.
func reachBarOwnerAllow(in reachObligationInput) []string {
	recorded := map[string]bool{}
	if in.vd.OwnerSet != nil {
		for _, r := range in.vd.OwnerSet.Routes {
			for i, p := range in.vd.Principals {
				if i < len(r.Verdict) && r.Verdict[i] == 'A' && in.assured(p) {
					recorded[r.key()] = true
				}
			}
		}
	}
	var out []string
	for _, k := range reachOwnerRoutes(in) {
		out = append(out, k+"|owner lacking the requirement refused")
		if !recorded[k] {
			out = append(out, k+"|owner admitted")
		}
	}
	return out
}

// reachBarRandomRoles: every legacy atom the manifest's routes require. A random
// native role is admitted by stage 13 on a route only if the route's successor
// admits it, so a discharge names each legacy atom it maps.
func reachBarRandomRoles(in reachObligationInput) []string {
	set := map[string]bool{}
	for _, lr := range in.manifest {
		if lr.Perm.Kind() == rbac.KindLegacyAtom {
			set[lr.Perm.Atom()] = true
		}
	}
	return reachSorted(set)
}

// ---- finding the discharge test ----

// reachFoundTest is a test function located with go/ast.
type reachFoundTest struct {
	file     string
	fn       *ast.FuncDecl
	literals map[string]bool // every string literal in the file
}

// reachParseTest finds the test function name in one source file, or nil.
func reachParseTest(file string, src []byte, name string) (*reachFoundTest, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, src, 0)
	if err != nil {
		return nil, err
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name.Name != name || fd.Body == nil {
			continue
		}
		if reachTestParam(fd) == "" {
			continue
		}
		found := &reachFoundTest{file: file, fn: fd, literals: map[string]bool{}}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil {
					found.literals[v] = true
				}
			}
			return true
		})
		return found, nil
	}
	return nil, nil
}

// reachTestParam is the name of a function's `*testing.T` parameter, or "" when
// it has no such single parameter.
func reachTestParam(fd *ast.FuncDecl) string {
	ps := fd.Type.Params
	if ps == nil || len(ps.List) != 1 || len(ps.List[0].Names) != 1 {
		return ""
	}
	star, ok := ps.List[0].Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return ""
	}
	if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "testing" {
		return ""
	}
	return ps.List[0].Names[0].Name
}

// reachFindTest looks for the test function name in the _test.go files of dir
// (relative to this package), or returns nil.
func reachFindTest(dir, name string) (*reachFoundTest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		found, err := reachParseTest(path, src, name)
		if err != nil {
			return nil, err
		}
		if found != nil {
			return found, nil
		}
	}
	return nil, nil
}

// reachTestAsserts says why a test function does not count as a test, or "". It
// must never skip (a t.Skip call, or a testing.Short gate that skips it in the
// short run) and must assert something: call t.Fatal/Error/Fail/Run, or hand t to
// another call (require.X(t, ...), a helper).
func reachTestAsserts(fn *ast.FuncDecl) string {
	tname := reachTestParam(fn)
	skips, asserts := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				switch {
				case x.Name == tname && strings.HasPrefix(sel.Sel.Name, "Skip"):
					skips = true
				case x.Name == "testing" && sel.Sel.Name == "Short":
					skips = true
				case x.Name == tname && (strings.HasPrefix(sel.Sel.Name, "Fatal") || strings.HasPrefix(sel.Sel.Name, "Error") || sel.Sel.Name == "Fail" || sel.Sel.Name == "FailNow" || sel.Sel.Name == "Run"):
					asserts = true
				}
			}
		}
		for _, a := range call.Args {
			if id, ok := a.(*ast.Ident); ok && id.Name == tname {
				asserts = true
			}
		}
		return true
	})
	switch {
	case skips:
		return "it can skip (t.Skip or a testing.Short gate)"
	case !asserts:
		return "it never asserts (no t.Fatal, t.Error, t.Run, or call that is handed t)"
	}
	return ""
}

// ---- the open set ----

// reachObligationState is one row's state against an input.
type reachObligationState struct {
	row  reachObligation
	bar  []string
	open bool
	why  string
}

// reachDeclared is the coverage a row's test declares, and why it is none.
func reachDeclared(in reachObligationInput, o reachObligation, found *reachFoundTest, bar []string) (declared []string, why string) {
	if o.dir == "." {
		for _, d := range in.discharges {
			if d.Obligation == o.name && d.Test == o.test && d.Covers != nil {
				return d.Covers(), ""
			}
		}
		return nil, "the test is not registered in reachDischarges, so it declares no coverage"
	}
	for _, item := range bar {
		if found.literals[item] {
			declared = append(declared, item)
		}
	}
	return declared, ""
}

// reachObligationStates evaluates every row against the input.
func reachObligationStates(in reachObligationInput, table []reachObligation) []reachObligationState {
	var out []reachObligationState
	for _, o := range table {
		st := reachObligationState{row: o, bar: o.bar(in)}
		switch {
		case len(st.bar) == 0:
			st.why = "its bar is empty: nothing is left to discharge"
		default:
			st.open = true
			found, err := in.find(o.dir, o.test)
			switch {
			case err != nil:
				st.why = fmt.Sprintf("%d %s to cover; reading %s: %v", len(st.bar), o.unit, o.dir, err)
			case found == nil:
				st.why = fmt.Sprintf("%d %s to cover; no test %s in %s", len(st.bar), o.unit, o.test, o.dir)
			default:
				if why := reachTestAsserts(found.fn); why != "" {
					st.why = fmt.Sprintf("%d %s to cover; %s is not a discharge: %s", len(st.bar), o.unit, o.test, why)
					break
				}
				declared, why := reachDeclared(in, o, found, st.bar)
				if why != "" {
					st.why = fmt.Sprintf("%d %s to cover; %s", len(st.bar), o.unit, why)
					break
				}
				missing := reachDiffSets(st.bar, declared)
				extra := reachDiffSets(declared, st.bar)
				switch {
				case len(missing) > 0:
					st.why = fmt.Sprintf("%d %s to cover; %s declares %d and leaves %d uncovered (first: %s)", len(st.bar), o.unit, o.test, len(declared)-len(extra), len(missing), missing[0])
				case o.exact && len(extra) > 0:
					st.why = fmt.Sprintf("%d %s to cover exactly; %s declares %d it should not (first: %s)", len(st.bar), o.unit, o.test, len(extra), extra[0])
				default:
					st.open = false
					st.why = ""
				}
			}
		}
		out = append(out, st)
	}
	return out
}

// reachDiffSets is a without b, sorted.
func reachDiffSets(a, b []string) []string {
	in := map[string]bool{}
	for _, x := range b {
		in[x] = true
	}
	var out []string
	for _, x := range a {
		if !in[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// reachOpenObligations are the rows with no valid discharge, as the flip names them.
func reachOpenObligations(in reachObligationInput, table []reachObligation) []reachFlipObligation {
	var out []reachFlipObligation
	for _, st := range reachObligationStates(in, table) {
		if st.open {
			out = append(out, reachFlipObligation{name: st.row.name, discharge: st.row.discharge + " [" + st.why + "]"})
		}
	}
	return out
}

// ---- the table's own checks ----

const reachSeamsSource = "../../../docs/agent-tasks/platform-core/seams.d/F4-07.md"

// reachSeamsObligations is the "Obligations on F4-08" section of the seams file.
func reachSeamsObligations(seams []byte) string {
	s := string(seams)
	_, rest, ok := strings.Cut(s, "\n## Obligations on F4-08\n")
	if !ok {
		return ""
	}
	section, _, _ := strings.Cut(rest, "\n## ")
	return section
}

// reachObligationTokens are the F4-07-<NAME> tokens a text holds.
func reachObligationTokens(text string) []string {
	set := map[string]bool{}
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '-' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) {
		if strings.HasPrefix(w, "F4-07-") && len(w) > len("F4-07-") {
			set[strings.TrimRight(w, "-")] = true
		}
	}
	return reachSorted(set)
}

// reachNamesWord reports whether text names word as a whole identifier: a longer
// name that merely begins with it (TestX against TestXY) is not the name.
func reachNamesWord(text, word string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(word) + `($|[^A-Za-z0-9_-])`).MatchString(text)
}

// reachObligationProblems is what the table fails on, as text: well-formed rows,
// the seams' Obligations section naming exactly the rows (each with its test), and
// every registered discharge naming a known row and a test that is a test.
func reachObligationProblems(in reachObligationInput, table []reachObligation, seams []byte) []string {
	var out []string
	section := reachSeamsObligations(seams)
	if section == "" {
		return []string{"seams.d/F4-07.md has no \"Obligations on F4-08\" section: the rows must be stated where F4-08 reads them"}
	}
	names := map[string]bool{}
	for _, o := range table {
		if names[o.name] {
			out = append(out, o.name+" is a row twice")
		}
		names[o.name] = true
		if strings.TrimSpace(o.discharge) == "" || o.dir == "" || o.test == "" || o.bar == nil || o.unit == "" {
			out = append(out, o.name+" is not a complete row (discharge, dir, test, unit and bar are all required)")
		}
		if !reachNamesWord(section, o.name) {
			out = append(out, o.name+" is a row and the seams' Obligations section does not state it")
		}
		if !reachNamesWord(section, o.test) {
			out = append(out, o.name+": the seams' Obligations section does not name its discharge test "+o.test)
		}
	}
	for _, tok := range reachObligationTokens(section) {
		if !names[tok] {
			out = append(out, tok+" is stated in the seams' Obligations section and has no row in reachObligations: a row cannot be dropped while the seams state it")
		}
	}
	rows := map[string]reachObligation{}
	for _, o := range table {
		rows[o.name] = o
	}
	for _, d := range in.discharges {
		o, ok := rows[d.Obligation]
		switch {
		case !ok:
			out = append(out, d.Obligation+" is registered in reachDischarges and is no row of reachObligations")
			continue
		case o.dir != ".":
			out = append(out, d.Obligation+" is registered in reachDischarges, but its test lives in "+o.dir+" and declares coverage by the literals of its file")
			continue
		case d.Test != o.test:
			out = append(out, fmt.Sprintf("%s: registered test %s is not the row's %s", d.Obligation, d.Test, o.test))
			continue
		case d.Covers == nil:
			out = append(out, d.Obligation+": the registration declares no coverage")
			continue
		}
		found, err := in.find(".", d.Test)
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("%s: reading the package for %s: %v", d.Obligation, d.Test, err))
		case found == nil:
			out = append(out, fmt.Sprintf("%s: registered test %s does not exist in this package", d.Obligation, d.Test))
		default:
			if why := reachTestAsserts(found.fn); why != "" {
				out = append(out, fmt.Sprintf("%s: registered test %s is not a test: %s", d.Obligation, d.Test, why))
			}
		}
	}
	slices.Sort(out)
	return out
}

// reachObligationInputFrom builds the input the oracle judges: the one owner
// predicate, this package's registrations, the real finder.
func reachObligationInputFrom(vd *reachR0Doc, res reachDiffResult) reachObligationInput {
	return reachObligationInput{
		vd: vd, res: res, manifest: reachManifest(),
		assured: reachOwnerAssured, discharges: reachDischarges, find: reachFindTest,
	}
}

func reachObligationInputOf(t testing.TB) reachObligationInput {
	t.Helper()
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _, _ := reachRun(t, pd, vd, atoms, atoms)
	return reachObligationInputFrom(vd, res)
}

func reachObligationProblemsNow(t testing.TB) []string {
	t.Helper()
	seams, err := os.ReadFile(reachSeamsSource)
	if err != nil {
		t.Fatal(err)
	}
	return reachObligationProblems(reachObligationInputOf(t), reachObligations, seams)
}

// reachOpenObligationsNow are the rows the flip waits on, today.
func reachOpenObligationsNow(t testing.TB) []reachFlipObligation {
	t.Helper()
	return reachOpenObligations(reachObligationInputOf(t), reachObligations)
}
