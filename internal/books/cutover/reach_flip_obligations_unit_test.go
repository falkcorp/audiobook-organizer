// file: internal/books/cutover/reach_flip_obligations_unit_test.go
// version: 1.3.0
// guid: 3e8a5c17-92d4-4b60-a1f3-7c6d09e2b845
// last-edited: 2026-11-15

package cutover

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// The self-tests of the obligation table (reach_flip_obligations_test.go): each
// row is cleared by a discharge shaped like the test F4-08 would write, and by
// nothing cheaper.

// reachWantBarSizes are what each bar holds today, from the committed recording.
// They are derived (the ABS bar is the delegated abs_only routes times the 37
// principals) and only pinned here so a drift is read, not absorbed.
func reachRowByName(t *testing.T, name string) reachObligation {
	t.Helper()
	for _, o := range reachObligations {
		if o.name == name {
			return o
		}
	}
	t.Fatalf("no row %s", name)
	return reachObligation{}
}

func reachStateOf(t *testing.T, in reachObligationInput, name string) reachObligationState {
	t.Helper()
	for _, st := range reachObligationStates(in, reachObligations) {
		if st.row.name == name {
			return st
		}
	}
	t.Fatalf("no row %s", name)
	return reachObligationState{}
}

// reachDischargedRows are the rows a test outside this package discharges
// today (the guard finds it with go/ast in the row's directory, so no
// registration is involved): every other row is open. F4-07-RANDOM-ROLES is
// discharged by TestLegacyStage13_RandomRolesAdmitOnlyWhatTheSuccessorAdmits
// in core/httpapi (F4-08 B6a). A row joins this set when its discharge lands.
var reachDischargedRows = map[string]bool{"F4-07-RANDOM-ROLES": true}

func TestReachObligations_TheTableIsStatedInTheSeamsAndEachRowIsOpenUntilItsDischargeLands(t *testing.T) {
	for _, p := range reachObligationProblemsNow(t) {
		t.Error(p)
	}
	in := reachObligationInputOf(t)
	states := reachObligationStates(in, reachObligations)
	if len(states) != 6 {
		t.Fatalf("%d rows, want the six obligations the seams state", len(states))
	}
	for _, st := range states {
		t.Logf("%-28s %4d %s to cover; open=%v (%s)", st.row.name, len(st.bar), st.row.unit, st.open, st.why)
		switch {
		case reachDischargedRows[st.row.name] && st.open:
			t.Errorf("%s has its discharge test in %s and is still open: %s", st.row.name, st.row.dir, st.why)
		case !reachDischargedRows[st.row.name] && !st.open:
			t.Errorf("%s is closed and no discharge is registered: %s", st.row.name, st.why)
		}
	}
	// The bars are derived; the sizes are read here, with the arithmetic the
	// seams state, so a change in the recording or the manifest is noticed.
	abs := reachStateOf(t, in, "F4-07-ABS-REPLAY")
	routes := map[string]bool{}
	for _, c := range abs.bar {
		k, _, _ := strings.Cut(c, "|")
		routes[k] = true
	}
	if len(routes) != 64 || len(abs.bar) != 64*len(in.vd.Principals) || len(abs.bar) != 2368 {
		t.Errorf("the ABS bar is %d cells over %d routes and %d principals, want 2368 over 64 and 37 (derived: abs_only routes that are still delegated)", len(abs.bar), len(routes), len(in.vd.Principals))
	}
	if n := len(reachStateOf(t, in, "F4-07-HANDLER-REFUSALS").bar); n != 8 {
		t.Errorf("the HANDLER-REFUSALS bar is %d cells, want the 8 auth-off cells on non-Public routes: %v", n, reachStateOf(t, in, "F4-07-HANDLER-REFUSALS").bar)
	}
	// The bar is the union of the in-handler sites' routes (reachHiddenSites), not a
	// recorded status: four sites sit on routes that never answered 404.
	hidden := reachStateOf(t, in, "F4-07-HIDDEN-HANDLER-CHECKS").bar
	want := map[string]bool{}
	for _, s := range reachHiddenSites {
		for _, r := range s.routes {
			want[r] = true
		}
	}
	for _, r := range reachOwnerRoutes(in) {
		want[r] = true
	}
	if len(hidden) != len(want) || len(hidden) != 32 {
		t.Errorf("the HIDDEN-HANDLER-CHECKS bar is %d routes, want the %d routes of the %d sites and the owner routes", len(hidden), len(want), len(reachHiddenSites))
	}
	for _, r := range []string{"GET /api/v1/users/invites", "PUT /api/v1/auth/me/password", "POST /api/v1/audiobooks/batch", "GET /api/v3/ops/events"} {
		if !slices.Contains(hidden, r) {
			t.Errorf("the HIDDEN-HANDLER-CHECKS bar lacks %s, a site route that no recorded 404 finds", r)
		}
	}
	owner := reachOwnerRoutes(in)
	if len(owner) != 9 {
		t.Errorf("%d owner routes, want the 8 manifest Guards.Owner routes and core's factory-reset: %v", len(owner), owner)
	}
	if n := len(reachStateOf(t, in, "F4-07-OWNER-SET").bar); n != 9 {
		t.Errorf("the OWNER-SET bar is %d cases, want one per owner route", n)
	}
	if n := len(reachStateOf(t, in, "F4-07-OWNER-ALLOW").bar); n != 18 {
		t.Errorf("the OWNER-ALLOW bar is %d cases, want two per owner route (owner admitted, owner lacking the requirement refused)", n)
	}
	if n := len(reachStateOf(t, in, "F4-07-RANDOM-ROLES").bar); n == 0 {
		t.Error("the RANDOM-ROLES bar is empty: the manifest names no legacy atom")
	}
}

// The owner routes the bar names are the manifest's Guards.Owner routes, so the
// R2-only reissue route is among them, and core's own route is a real one.
func TestReachObligations_TheOwnerBarHoldsTheReissueRouteAndCoresFactoryReset(t *testing.T) {
	in := reachObligationInputOf(t)
	owner := reachOwnerRoutes(in)
	for _, want := range append(slices.Clone(reachOwnerR2Only), reachCoreOwnerRoutes...) {
		if !slices.Contains(owner, want) {
			t.Errorf("%s is not an owner route of the bar: %v", want, owner)
		}
	}
}

func TestReachObligations_TheCoreOwnerRouteIsStillCores(t *testing.T) {
	src, err := os.ReadFile(reachCoreOwnerSource)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range reachCoreOwnerRoutes {
		if !strings.Contains(string(src), `"`+k+`"`) {
			t.Errorf("%s no longer names %s: the successor of the reset pair moved, so the owner bar reads a route that is gone", reachCoreOwnerSource, k)
		}
	}
}

// ---- a discharge shaped like F4-08's clears the row, and nothing cheaper does ----

// reachFixtureSources are discharge tests as F4-08 would write them: data-driven
// replays that loop over the recording and hold no route literals (the ones in
// this package), and, for core, a test whose table names each legacy atom.
func reachFixtureSource(name string, atomLiterals []string) string {
	var lits string
	for _, a := range atomLiterals {
		lits += fmt.Sprintf("\t\t%q,\n", a)
	}
	return fmt.Sprintf(`package fixture

import "testing"

func %s(t *testing.T) {
	atoms := []string{
%s	}
	for _, r := range recording() {
		t.Run(r, func(t *testing.T) {
			if !replay(r) {
				t.Fatalf("%%s differs on the registered path", r)
			}
		})
	}
	_ = atoms
}
`, name, lits)
}

// reachFixtureInput returns the real input with the discharge for row installed:
// the registration (this package) or the file literals (core), as asked.
func reachFixtureInput(t *testing.T, row reachObligation, covers []string, source string) reachObligationInput {
	t.Helper()
	in := reachObligationInputOf(t)
	files := map[string]string{row.dir + "|" + row.test: source}
	in.find = func(dir, name string) (*reachFoundTest, error) {
		src, ok := files[dir+"|"+name]
		if !ok {
			return nil, nil
		}
		return reachParseTest("fixture_test.go", []byte(src), name)
	}
	// The fixture judges its own discharge alone: the real registrations
	// (RegisterReachDischarge, from the B10 cell files' init) name tests the
	// fake finder above cannot see, and would read as missing.
	in.discharges = nil
	if row.dir == "." {
		in.discharges = []reachDischarge{{Obligation: row.name, Test: row.test, Covers: func() []string { return covers }}}
	}
	return in
}

func reachWithout(items []string, drop string) []string {
	return slices.DeleteFunc(slices.Clone(items), func(s string) bool { return s == drop })
}

func TestReachObligations_ADischargeShapedLikeF48ClearsEachRow(t *testing.T) {
	base := reachObligationInputOf(t)
	for _, row := range reachObligations {
		t.Run(row.name, func(t *testing.T) {
			bar := row.bar(base)
			in := reachFixtureInput(t, row, bar, reachFixtureSource(row.test, bar))
			for _, st := range reachObligationStates(in, reachObligations) {
				if st.row.name == row.name && st.open {
					t.Errorf("%s stays open with a test that declares the whole bar: %s", row.name, st.why)
				}
				if st.row.name != row.name && !st.open {
					t.Errorf("%s closed by a discharge for %s", st.row.name, row.name)
				}
			}
			if p := reachObligationProblemsWith(t, in); len(p) != 0 {
				t.Errorf("a valid discharge produced problems: %v", p)
			}
		})
	}
}

// reachObligationProblemsWith is the table check on an input, against the real seams.
func reachObligationProblemsWith(t *testing.T, in reachObligationInput) []string {
	t.Helper()
	seams, err := os.ReadFile(reachSeamsSource)
	if err != nil {
		t.Fatal(err)
	}
	return reachObligationProblems(in, reachObligations, seams)
}

func TestReachObligations_NothingCheaperThanTheBarClearsARow(t *testing.T) {
	base := reachObligationInputOf(t)
	for _, row := range reachObligations {
		bar := row.bar(base)
		full := reachFixtureSource(row.test, bar)
		type variant struct {
			name   string
			covers []string
			source string
			dir    string // "" keeps the row's
			rename string // the test is stored under another name
			// registered false installs no registration (this package only)
			unregistered bool
		}
		vs := []variant{
			{name: "one case short", covers: reachWithout(bar, bar[0]), source: reachFixtureSource(row.test, reachWithout(bar, bar[0]))},
			{name: "no cases", covers: nil, source: reachFixtureSource(row.test, nil)},
			{name: "a skipping test", covers: bar, source: strings.Replace(full, "\tatoms := []string{", "\tt.Skip(\"later\")\n\tatoms := []string{", 1)},
			{name: "a short-gated test", covers: bar, source: strings.Replace(full, "\tatoms := []string{", "\tif testing.Short() {\n\t\treturn\n\t}\n\tatoms := []string{", 1)},
			{name: "a test that asserts nothing", covers: bar, source: "package fixture\n\nimport \"testing\"\n\nfunc " + row.test + "(t *testing.T) {\n\tfor range recording() {\n\t}\n}\n"},
			{name: "a test stored under another name", covers: bar, source: full, rename: row.test + "V2"},
			{name: "a test in another directory", covers: bar, source: full, dir: "../../elsewhere"},
		}
		if row.dir == "." {
			vs = append(vs, variant{name: "no registration", covers: bar, source: full, unregistered: true})
		}
		if row.exact {
			extra := append(slices.Clone(bar), "GET /api/batcave/gadgets|session:admin")
			vs = append(vs, variant{name: "an extra case on an exact row", covers: extra, source: reachFixtureSource(row.test, extra)})
		}
		for _, v := range vs {
			t.Run(row.name+"/"+v.name, func(t *testing.T) {
				in := reachFixtureInput(t, row, v.covers, v.source)
				if v.rename != "" || v.dir != "" {
					dir, name := row.dir, row.test
					if v.dir != "" {
						dir = v.dir
					}
					if v.rename != "" {
						name = v.rename
					}
					in.find = func(d, n string) (*reachFoundTest, error) {
						if d != dir || n != name {
							return nil, nil
						}
						return reachParseTest("fixture_test.go", []byte(v.source), name)
					}
				}
				if v.unregistered {
					in.discharges = nil
				}
				if st := reachStateOf(t, in, row.name); !st.open {
					t.Errorf("%s: row %s cleared", v.name, row.name)
				}
			})
		}
	}
}

// The registration the guard trusts must itself be a test: a registration that
// names a missing or skipping test, an unknown row, another row's test or no
// coverage fails the table check.
func TestReachObligations_ABadRegistrationFailsTheTable(t *testing.T) {
	base := reachObligationInputOf(t)
	abs := reachRowByName(t, "F4-07-ABS-REPLAY")
	cover := func() []string { return nil }
	for _, tc := range []struct {
		name string
		d    reachDischarge
		find func(string, string) (*reachFoundTest, error)
		want string
	}{
		{"an unknown row", reachDischarge{"F4-07-BATCAVE", "TestX", cover}, nil, "no row of reachObligations"},
		{"a row whose test lives elsewhere", reachDischarge{"F4-07-RANDOM-ROLES", "TestX", cover}, nil, "lives in"},
		{"another test than the row's", reachDischarge{abs.name, "TestSomethingElse", cover}, nil, "is not the row's"},
		{"no coverage", reachDischarge{abs.name, abs.test, nil}, nil, "declares no coverage"},
		{"a test that does not exist", reachDischarge{abs.name, abs.test, cover}, func(string, string) (*reachFoundTest, error) { return nil, nil }, "does not exist"},
		{"a test that skips", reachDischarge{abs.name, abs.test, cover}, func(_, n string) (*reachFoundTest, error) {
			return reachParseTest("f_test.go", []byte("package f\nimport \"testing\"\nfunc "+n+"(t *testing.T) { t.Skip(\"x\") }\n"), n)
		}, "is not a test"},
	} {
		in := base
		in.discharges = []reachDischarge{tc.d}
		if tc.find != nil {
			in.find = tc.find
		}
		got := strings.Join(reachObligationProblemsWith(t, in), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: %q does not contain %q", tc.name, got, tc.want)
		}
	}
}

// ---- the table is held to the seams in both directions ----

func TestReachObligations_DroppingARowOrItsSeamsStatementFails(t *testing.T) {
	in := reachObligationInputOf(t)
	seams, err := os.ReadFile(reachSeamsSource)
	if err != nil {
		t.Fatal(err)
	}
	for i, o := range reachObligations {
		without := slices.Delete(slices.Clone(reachObligations), i, i+1)
		got := reachObligationProblems(in, without, seams)
		if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), o.name+" is stated in the seams") {
			t.Errorf("dropping the row %s: %q", o.name, got)
		}
		// the other way: the seams stop naming it
		stripped := []byte(strings.ReplaceAll(string(seams), o.name, "F4-07-REMOVED"))
		got = reachObligationProblems(in, reachObligations, stripped)
		if !strings.Contains(strings.Join(got, "\n"), o.name+" is a row and the seams' Obligations section does not state it") {
			t.Errorf("seams without %s: %q", o.name, got)
		}
	}
	if got := reachObligationProblems(in, reachObligations, []byte("# nothing\n")); len(got) != 1 || !strings.Contains(got[0], "no \"Obligations on F4-08\" section") {
		t.Errorf("seams with no Obligations section: %q", got)
	}
	// a discharge test the seams do not name
	for _, o := range reachObligations {
		stripped := []byte(strings.ReplaceAll(string(seams), o.test, "TestRemoved"))
		got := reachObligationProblems(in, reachObligations, stripped)
		if !strings.Contains(strings.Join(got, "\n"), "does not name its discharge test "+o.test) {
			t.Errorf("seams without the test name %s: %q", o.test, got)
		}
	}
	dup := append(slices.Clone(reachObligations), reachObligations[0])
	if got := reachObligationProblems(in, dup, seams); !strings.Contains(strings.Join(got, "\n"), "a row twice") {
		t.Errorf("a duplicated row: %q", got)
	}
	bare := slices.Clone(reachObligations)
	bare[0].test = ""
	if got := reachObligationProblems(in, bare, seams); !strings.Contains(strings.Join(got, "\n"), "not a complete row") {
		t.Errorf("a row with no test: %q", got)
	}
}

// ---- the bars read the recording and the diff, never a row or a function name ----

func TestReachObligations_HandlerRefusalsBarIsReadFromTheDiffNotTheRows(t *testing.T) {
	in := reachObligationInputOf(t)
	if len(reachBarHandlerRefusals(in)) != 8 {
		t.Fatalf("premise: the bar holds %d cells", len(reachBarHandlerRefusals(in)))
	}
	// The round-4 C1: the rows are renamed or moved. The bar reads no row, so it
	// is the same set and the row stays open.
	if got := reachBarHandlerRefusals(in); len(got) != 8 {
		t.Errorf("the bar moved with the rows: %d", len(got))
	}
	// A cell the router admits and R0's handler refused, for a principal other
	// than the auth-off one, is not a handler refusal of the auth-off principal.
	other := in
	other.res.cells = append(slices.Clone(in.res.cells), reachCell{route: "GET /api/v1/batcave/gadgets", principal: "session:viewer", r0: 'D', r2: 'A'})
	if len(reachBarHandlerRefusals(other)) != 8 {
		t.Error("a cell of another principal entered the auth-off bar")
	}
	// A Public route's cell (login, refresh) is a recorder limit and is not in it.
	for _, c := range in.res.cells {
		if c.principal == reachAuthOffName && (c.route == "POST /login" || c.route == "POST /auth/refresh") {
			for _, b := range reachBarHandlerRefusals(in) {
				if strings.HasPrefix(b, c.route+"|") {
					t.Errorf("the Public route %s is in the bar", c.route)
				}
			}
		}
	}
	// The cells go when R2 stops differing (the routes are deleted or tightened):
	// the bar is then empty and the row closes without a test, because nothing
	// is left to prove.
	none := in
	none.res.cells = slices.DeleteFunc(slices.Clone(in.res.cells), func(c reachCell) bool { return c.principal == reachAuthOffName })
	if got := reachBarHandlerRefusals(none); len(got) != 0 {
		t.Errorf("no auth-off cell differs and the bar holds %v", got)
	}
	if st := reachStateOf(t, none, "F4-07-HANDLER-REFUSALS"); st.open {
		t.Errorf("an empty bar left the row open: %s", st.why)
	}
}

// A doctored recording cannot empty a bar quietly: the oracle's own checks fail on
// each doctoring (the delegated set, the uniform-404 pin).
func TestReachObligations_ADoctoredRecordingIsStillCaughtByTheOracle(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _, _ := reachRun(t, pd, vd, atoms, atoms)
	rows := reachOracleRows()
	if p := reachChecks(vd, res, rows); len(p) != 0 {
		t.Fatalf("the unchanged recording already fails: %v", p)
	}
	noABS := *vd
	noABS.Routes = slices.Clone(vd.Routes)
	for i := range noABS.Routes {
		noABS.Routes[i].ABSOnly = false
	}
	if bar := reachBarABSReplay(reachObligationInput{vd: &noABS, res: res}); len(bar) != 0 {
		t.Fatalf("premise: clearing abs_only empties the bar, got %d", len(bar))
	}
	noABSRes, _, _ := reachRun(t, pd, &noABS, atoms, atoms)
	if p := reachChecks(&noABS, noABSRes, rows); len(p) == 0 {
		t.Error("abs_only cleared on every route and the oracle passed")
	}
	noHidden := *vd
	noHidden.UniformNotFound = nil
	if p := reachChecks(&noHidden, res, rows); len(p) == 0 {
		t.Error("uniform_not_found emptied and the oracle passed")
	}
}

// The owner predicate is one function. The obligations and the oracle's owner-set
// comparison both read it, so a predicate that treats every principal as the owner
// fails the comparison (the recording refuses them all) and, read by the OWNER-ALLOW
// bar, changes nothing there: the recording holds no allowed cell for it.
func TestReachObligations_OneOwnerPredicateFeedsTheModelAndTheBar(t *testing.T) {
	pd, vd := reachLoad(t)
	atoms := idAtoms()
	res, _, r2 := reachRun(t, pd, vd, atoms, atoms)
	in := reachObligationInputFrom(vd, res)
	if fmt.Sprintf("%p", in.assured) != fmt.Sprintf("%p", reachOwnerAssured) {
		t.Error("the obligation input does not read reachOwnerAssured")
	}
	everyone := func(string) bool { return true }
	if p := reachOwnerSetProblems(vd, r2, everyone); len(p) == 0 {
		t.Error("a predicate that makes every principal the owner passed the owner-set comparison")
	}
	allow := in
	allow.assured = everyone
	if got, want := len(reachBarOwnerAllow(allow)), len(reachBarOwnerAllow(in)); got != want {
		t.Errorf("the OWNER-ALLOW bar moved with the predicate (%d, was %d) though the recording holds no allowed cell", got, want)
	}
	// With a recorded allowed cell for an owner-assured principal, that route's
	// admitted case needs no registered-path test.
	rec := *vd
	o := *vd.OwnerSet
	o.Routes = slices.Clone(o.Routes)
	owner := vd.Principals[0]
	v := []byte(o.Routes[0].Verdict)
	v[0] = 'A'
	o.Routes[0].Verdict = string(v)
	rec.OwnerSet = &o
	withRec := reachObligationInput{vd: &rec, res: res, manifest: in.manifest, assured: func(p string) bool { return p == owner }}
	if got, want := len(reachBarOwnerAllow(withRec)), len(reachBarOwnerAllow(in))-1; got != want {
		t.Errorf("a recorded owner allow left the OWNER-ALLOW bar at %d, want %d", got, want)
	}
}

// A test name the seams state only as the start of a longer name is not stated.
func TestReachObligations_ALongerNameIsNotTheNameTheSeamsState(t *testing.T) {
	const text = "cleared by `TestReachRegisteredPath_OwnerSetX` (F4-07-OWNER-SETX)"
	for _, w := range []string{"TestReachRegisteredPath_OwnerSet", "F4-07-OWNER-SET"} {
		if reachNamesWord(text, w) {
			t.Errorf("%s was found inside a longer name", w)
		}
	}
	for _, w := range []string{"TestReachRegisteredPath_OwnerSetX", "F4-07-OWNER-SETX"} {
		if !reachNamesWord(text, w) {
			t.Errorf("%s is stated and was not found", w)
		}
	}
}
