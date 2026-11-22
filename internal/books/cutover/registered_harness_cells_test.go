// file: internal/books/cutover/registered_harness_cells_test.go
// version: 0.1.0
// guid: 2a9e5734-630d-4f1a-a35f-dbfa7206c1af
// last-edited: 2026-11-22

package cutover_test

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// ---- the wired resolver is the atom table ----

// resolverDiffs lists where r answers differently from books.AtomTable(): for every
// row, its Route on the route side and its ReadSide on the read side, and the
// refusals (an atom the table does not know; an atom with no entry of the asked
// side, playlists.create's zero Route among them). Empty means equal.
func resolverDiffs(r httpapi.AtomResolver) []string {
	var out []string
	for _, a := range books.AtomTable() {
		for _, side := range []struct {
			name     string
			readSide bool
			want     rbac.Requirement
		}{{"route", false, a.Route}, {"read side", true, a.ReadSide}} {
			got, ok := r(a.Name, side.readSide)
			wantOK := !side.want.IsZero()
			switch {
			case ok != wantOK:
				out = append(out, fmt.Sprintf("%s %s: resolves=%v, the table says %v", a.Name, side.name, ok, wantOK))
			case ok && !reflect.DeepEqual(got, side.want):
				out = append(out, fmt.Sprintf("%s %s: %v, the table says %v", a.Name, side.name, got, side.want))
			}
		}
	}
	if req, ok := r("batcave.access", false); ok {
		out = append(out, fmt.Sprintf("an atom the table does not know resolves to %v", req))
	}
	if req, ok := r("", true); ok {
		out = append(out, fmt.Sprintf("the empty atom resolves to %v", req))
	}
	return out
}

// TestRegisteredHarness_WiredResolverIsTheAtomTable: the resolver books.Options
// hands core (what stage 13 resolves LegacyAtom and LegacyCheck requirements with)
// answers exactly what AtomTable() says, on both sides, for all 13 rows.
func TestRegisteredHarness_WiredResolverIsTheAtomTable(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	if diffs := resolverDiffs(h.Wired); len(diffs) != 0 {
		t.Fatalf("the wired resolver differs from books.AtomTable():\n  %s", strings.Join(diffs, "\n  "))
	}
	if n := len(books.AtomTable()); n != 13 {
		t.Fatalf("the atom table has %d rows, want 13 (05-rbac 6.1)", n)
	}
}

// ---- stage 13 ran ----

// stage13Sweep sends one request per registered route as the anonymous principal,
// through via, and returns what the harness could not show stage 13 judged: for a
// route it must judge, an anonymous caller is refused by stage 13 itself (401
// authentication_required, a problem document), so a route that answers anything
// else is one stage 13 did not stand in front of. OwnAuth routes are skipped (they
// authenticate themselves by design: Do names them ErrOwnAuth) and so are public
// ones. The sweep covers the methods in methods only; a refusal is the harness's
// proof, but a bypassed route would run its handler, so the caller keeps the mutating
// methods behind a GET sweep that passed.
func (h *Registered) stage13Sweep(via http.Handler, anon *Principal, methods ...string) (problems []string, judgedRoutes int) {
	for i := range h.Routes {
		ri := h.Routes[i]
		must, _ := judged(&ri)
		if !must || !slices.Contains(methods, ri.Method) {
			continue
		}
		res, err := h.do(via, anon, ri.Method, probePath(ri.Pattern), nil, &ri)
		judgedRoutes++
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case res.Stage13 != Stage13Refused || res.Code != "authentication_required":
			problems = append(problems, fmt.Sprintf("%s %s as anonymous answered %d (%s), not stage 13's 401 authentication_required", ri.Method, ri.Pattern, res.Status, res.Stage13))
		}
	}
	return problems, judgedRoutes
}

// TestRegisteredHarness_Stage13StandsInFrontOfEveryRoute is the harness checking
// itself: on the registered path, every route stage 13 must judge refuses the
// anonymous caller with stage 13's own problem. The reads go first; the writes are
// probed only once every read held, because a write that stage 13 did not guard
// would reach its handler.
func TestRegisteredHarness_Stage13StandsInFrontOfEveryRoute(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	anon := h.Principal(Anonymous)
	reads, n := h.stage13Sweep(h.Handler, anon, http.MethodGet, http.MethodHead)
	if len(reads) != 0 {
		t.Fatalf("stage 13 is not in front of %d of %d read routes:\n  %s", len(reads), n, strings.Join(reads, "\n  "))
	}
	if n < 100 {
		t.Fatalf("only %d read routes were swept: the registered routes are not what the harness expects", n)
	}
	writes, m := h.stage13Sweep(h.Handler, anon, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete)
	if len(writes) != 0 {
		t.Fatalf("stage 13 is not in front of %d of %d write routes:\n  %s", len(writes), m, strings.Join(writes, "\n  "))
	}
	t.Logf("stage 13 refused the anonymous caller on %d read and %d write routes", n, m)

	// A request that reaches the Perm step asks the resolver: a viewer session on a
	// route its role does not hold is refused by stage 13 with a resolver call, and
	// one it holds is admitted with one.
	viewer := h.Principal("session:viewer")
	before := h.resolved.Load()
	res, err := h.Do(viewer, http.MethodGet, "/api/v1/users", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stage13 == Stage13Unobserved || h.resolved.Load() == before {
		t.Fatalf("a signed-in request to an atom route left no stage 13 mark (status %d, evidence %s)", res.Status, res.Stage13)
	}
}

// ---- the mutants ----

// TestRegisteredHarness_BypassingStage13FailsTheHarness: three ways a request ends
// up unjudged, each of which the harness itself must refuse to answer.
func TestRegisteredHarness_BypassingStage13FailsTheHarness(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	anon := h.Principal(Anonymous)

	var atomRoute, ownAuthRoute *httpapi.RouteInfo
	for i := range h.Routes {
		ri := &h.Routes[i]
		if _, atom := judged(ri); atom && atomRoute == nil && ri.Method == http.MethodGet {
			atomRoute = ri
		}
		if ri.OwnAuth && ownAuthRoute == nil && ri.Method == http.MethodGet {
			ownAuthRoute = ri
		}
	}
	if atomRoute == nil || ownAuthRoute == nil {
		t.Fatalf("the registered routes hold no atom route (%v) or no OwnAuth route (%v) to mutate", atomRoute, ownAuthRoute)
	}

	t.Run("a handler answers in front of stage 13", func(t *testing.T) {
		// The mutant: the request is served by something that mounts the route
		// without stage 13 and admits it, as a router that dropped stage 13 would.
		bypass := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		res, err := h.do(bypass, anon, atomRoute.Method, probePath(atomRoute.Pattern), nil, atomRoute)
		if !errors.Is(err, ErrNotJudged) {
			t.Fatalf("a 200 from outside stage 13 on %s %s was answered (%d, %s, err %v): the harness would report the handler's answer as a verdict", atomRoute.Method, atomRoute.Pattern, res.Status, res.Stage13, err)
		}
	})

	t.Run("a refusal that is not stage 13's", func(t *testing.T) {
		// The mutant: a handler's own 401 (gin's envelope, not a problem document
		// with stage 13's code) is not evidence of stage 13.
		own := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"authentication required"}`))
		})
		_, err := h.do(own, anon, atomRoute.Method, probePath(atomRoute.Pattern), nil, atomRoute)
		if !errors.Is(err, ErrNotJudged) {
			t.Fatalf("a handler's own 401 passed as stage 13's refusal (err %v)", err)
		}
	})

	t.Run("an OwnAuth route on the real registered path", func(t *testing.T) {
		// Not simulated: the real kernel runs no stage 12 or 13 for an OwnAuth route,
		// so a request to one is a bypass of stage 13 by construction.
		_, err := h.DoRoute(anon, *ownAuthRoute, nil)
		if !errors.Is(err, ErrOwnAuth) {
			t.Fatalf("a request to the OwnAuth route %s %s was answered as a stage 13 verdict (err %v)", ownAuthRoute.Method, ownAuthRoute.Pattern, err)
		}
	})
}

// TestRegisteredHarness_ChangedAtomRowFailsTheResolverCheck: each of the 13 rows,
// changed in a copy of the table the resolver answers from, is reported by the
// equality check.
func TestRegisteredHarness_ChangedAtomRowFailsTheResolverCheck(t *testing.T) {
	table := books.AtomTable()
	resolver := func(rows []books.Atom) httpapi.AtomResolver {
		return func(atom string, readSide bool) (rbac.Requirement, bool) {
			for _, a := range rows {
				if a.Name != atom {
					continue
				}
				req := a.Route
				if readSide {
					req = a.ReadSide
				}
				return req, !req.IsZero()
			}
			return rbac.Requirement{}, false
		}
	}
	if diffs := resolverDiffs(resolver(table)); len(diffs) != 0 {
		t.Fatalf("the unchanged table reads as different: %v", diffs)
	}
	for i, row := range table {
		mutated := books.AtomTable()
		mutated[i].Route = rbac.AllOf("books:fingerprint.worker")
		if row.Name == "fingerprint.worker" {
			mutated[i].Route = rbac.AllOf("books:view")
		}
		if row.Route.IsZero() {
			// playlists.create: a route where the table has none.
			mutated[i].Route = rbac.AllOf("books:view")
		}
		if len(resolverDiffs(resolver(mutated))) == 0 {
			t.Errorf("changing the %s row's route went unreported", row.Name)
		}
	}
}
