// file: internal/books/cutover/registered_harness_cells_test.go
// version: 0.1.0
// guid: 2a9e5734-630d-4f1a-a35f-dbfa7206c1af
// last-edited: 2026-11-22

package cutover_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// ---- the wired resolver is the atom table ----

// resolverDiffs lists where r answers differently from books.AtomTable(): for every
// row, its Route on the route side and, on the read side, its ReadSide when it has
// one and its Route otherwise (the in-handler read-side entry exists for
// users.manage alone: the table's own contract), and the refusals (an atom the
// table does not know; an atom with no entry of the asked side, playlists.create's
// zero Route among them). Empty means equal.
func resolverDiffs(r httpapi.AtomResolver) []string {
	var out []string
	for _, a := range books.AtomTable() {
		readWant := a.Route
		if !a.ReadSide.IsZero() {
			readWant = a.ReadSide
		}
		for _, side := range []struct {
			name     string
			readSide bool
			want     rbac.Requirement
		}{{"route", false, a.Route}, {"read side", true, readWant}} {
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

// ---- the frozen API ----

// TestRegisteredHarness_APISurface exercises each helper B10e and B10f call, so a
// change to one fails here before it breaks a table.
func TestRegisteredHarness_APISurface(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	// The 36 principals of verdicts.json, then a core-native twin of 18 of the 28
	// keys among them (corekey:..., Twin naming the imported one).
	var recorded, core int
	for _, p := range h.Principals {
		if p.Core {
			core++
			if p.Twin == "" || h.Principal(p.Twin).Core || p.Name != coreKeyName(p.Twin) || !strings.HasPrefix(p.bearer, "mck_") {
				t.Fatalf("core key %s: twin %q, bearer shape %q", p.Name, p.Twin, p.bearer[:min(4, len(p.bearer))])
			}
			continue
		}
		recorded++
		if p.Kind == "key" && !strings.HasPrefix(p.bearer, "abk_") {
			t.Fatalf("%s holds a %q key, want abk_ (the shape prod's rows hold)", p.Name, p.bearer[:min(4, len(p.bearer))])
		}
	}
	// Ten of the 28 keys have no core twin: M3 gave each the ambient scopes only (its
	// atoms lie outside its owner's roles), and core's mint refuses a key without a
	// role scope. The three fp-worker keys are among them.
	wantNoTwin := []string{"key:editor:settings", "key:editor:users", "key:editor:worker", "key:viewer:settings", "key:viewer:users", "key:viewer:worker",
		"key:fp-worker:view", "key:fp-worker:settings", "key:fp-worker:users", "key:custom-subset:outside"}
	if !slices.Equal(h.NoCoreTwin, wantNoTwin) {
		t.Fatalf("keys without a core twin: %v, want %v", h.NoCoreTwin, wantNoTwin)
	}
	if recorded != 36 || core != 18 || len(h.Routes) < 500 {
		t.Fatalf("%d recorded and %d core-key principals and %d routes, want 36, 18 and at least 500", recorded, core, len(h.Routes))
	}
	admin := h.Principal("session:admin")
	res := h.MustDo(t, admin, http.MethodGet, "/api/v1/auth/me", nil)
	if res.Status != http.StatusOK || res.Verdict() != 'A' || !res.Admitted() || res.Route == nil || res.Route.Pattern != "/api/v1/auth/me" {
		t.Fatalf("admin GET /api/v1/auth/me: %d verdict %c route %v", res.Status, res.Verdict(), res.Route)
	}
	var ri httpapi.RouteInfo
	for _, r := range h.Routes {
		if r.Method == http.MethodGet && r.Pattern == "/api/v1/users" {
			ri = r
		}
	}
	res, err := h.DoRoute(h.Principal("session:viewer"), ri, nil)
	if err != nil || res.Verdict() != 'D' || res.Admitted() || res.Code != "permission_denied" {
		t.Fatalf("viewer DoRoute GET /api/v1/users: %d %q %v", res.Status, res.Code, err)
	}
	if _, err := h.Do(admin, http.MethodGet, "/api/me", nil); !errors.Is(err, ErrOwnAuth) {
		t.Fatalf("an ABS route answered without ErrOwnAuth: %v", err)
	}
	// Raw serves a request as no principal, here a path no route claims.
	raw := h.Raw(httptest.NewRequest(http.MethodGet, "/ping", nil))
	if raw.Route != nil || raw.Status == 0 {
		t.Fatalf("Raw: %+v", raw)
	}
	if h.Wired == nil || h.Logs() == "" {
		t.Fatal("Wired or Logs is empty")
	}
}

// TestRegisteredHarness_OwnerAndAuthOffBoots: the two other boots of the API. The
// owner is admitted through a signed Access assertion the real verifier checks
// against the harness's key set; the auth-off principal is the one core binds when
// core.auth.mode is disabled.
func TestRegisteredHarness_OwnerAndAuthOffBoots(t *testing.T) {
	t.Run("owner", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{Owner: true, ABSModes: "cf,jwt"})
		own := h.Principal(Owner)
		if h.Principals[len(h.Principals)-1] != own {
			t.Fatal("the owner is not the last principal")
		}
		res := h.MustDo(t, own, http.MethodGet, "/api/core/v1/me", nil)
		var me struct {
			Method        string `json:"method"`
			OwnerVerified bool   `json:"owner_verified"`
		}
		if err := json.Unmarshal(res.Body, &me); err != nil || res.Status != http.StatusOK || me.Method != "ext:cfaccess" || !me.OwnerVerified {
			t.Fatalf("owner GET /api/core/v1/me: %d %s", res.Status, res.excerpt())
		}
		if got := h.AccessRefused(); len(got) != 0 {
			t.Fatalf("the boot reached for hosts the harness does not serve: %v", got)
		}
	})
	t.Run("authoff", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		off := h.Principal(Authoff)
		res := h.MustDo(t, off, http.MethodGet, "/api/v1/auth/status", nil)
		if res.Status != http.StatusOK {
			t.Fatalf("auth-off GET /api/v1/auth/status: %d %s", res.Status, res.excerpt())
		}
		for range 3 { // fresh loopback peers: never throttled
			if r := h.MustDo(t, off, http.MethodGet, "/api/v1/auth/status", nil); r.Status != http.StatusOK {
				t.Fatalf("auth-off repeat: %d", r.Status)
			}
		}
	})
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
func (h *Registered) stage13Sweep(via http.Handler, real bool, anon *Principal, methods ...string) (problems []string, judgedRoutes int) {
	for i := range h.Routes {
		ri := h.Routes[i]
		if !judged(&ri) || !slices.Contains(methods, ri.Method) {
			continue
		}
		// Core rate limits an address; each probe comes from its own (the 198.18/15
		// benchmarking range, never a principal's), so the sweep measures stage 13 and
		// not the limiter.
		fresh := *anon
		fresh.peer = h.nextPeer()
		res, err := h.do(via, real, &fresh, ri.Method, probePath(ri.Pattern), nil, &ri)
		judgedRoutes++
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case res.Stage13 != Stage13Refused || res.Code != "authentication_required":
			problems = append(problems, fmt.Sprintf("%s %s as anonymous answered %d (%s) %s, not stage 13's 401 authentication_required", ri.Method, ri.Pattern, res.Status, res.Stage13, res.excerpt()))
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
	reads, n := h.stage13Sweep(h.Handler, true, anon, http.MethodGet, http.MethodHead)
	if len(reads) != 0 {
		t.Fatalf("stage 13 is not in front of %d of %d read routes:\n  %s", len(reads), n, strings.Join(reads, "\n  "))
	}
	if n < 100 {
		t.Fatalf("only %d read routes were swept: the registered routes are not what the harness expects", n)
	}
	writes, m := h.stage13Sweep(h.Handler, true, anon, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete)
	if len(writes) != 0 {
		t.Fatalf("stage 13 is not in front of %d of %d write routes:\n  %s", len(writes), m, strings.Join(writes, "\n  "))
	}
	t.Logf("stage 13 refused the anonymous caller on %d read and %d write routes", n, m)

	// Stage 13 decides by principal, not just by "signed in": on one route, one chain,
	// a viewer session is refused with stage 13's permission_denied and an admin session
	// is admitted. A chain that held no stage 13 could not tell them apart.
	const route = "/api/v1/users"
	viewer, err := h.Do(h.Principal("session:viewer"), http.MethodGet, route, nil)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := h.Do(h.Principal("session:admin"), http.MethodGet, route, nil)
	if err != nil {
		t.Fatal(err)
	}
	if viewer.Stage13 != Stage13Refused || viewer.Code != "permission_denied" || viewer.Status != http.StatusForbidden {
		t.Fatalf("viewer GET %s: %d %s %s, want stage 13's 403 permission_denied", route, viewer.Status, viewer.Stage13, viewer.excerpt())
	}
	if admin.Stage13 != Stage13Unobserved || admin.Status >= 300 {
		t.Fatalf("admin GET %s: %d %s %s, want an admit", route, admin.Status, admin.Stage13, admin.excerpt())
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
		if judged(ri) && ri.Perm.Kind() == rbac.KindAllOf && atomRoute == nil && ri.Method == http.MethodGet {
			atomRoute = ri
		}
		if ri.OwnAuth && ownAuthRoute == nil && ri.Method == http.MethodGet {
			ownAuthRoute = ri
		}
	}
	if atomRoute == nil || ownAuthRoute == nil {
		t.Fatalf("the registered routes hold no AllOf route (%v) or no OwnAuth route (%v) to mutate", atomRoute, ownAuthRoute)
	}

	t.Run("a handler answers in front of stage 13", func(t *testing.T) {
		// The mutant: the request is served by something that mounts the route
		// without stage 13 and admits it, as a router that dropped stage 13 would.
		bypass := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		res, err := h.do(bypass, false, anon, atomRoute.Method, probePath(atomRoute.Pattern), nil, atomRoute)
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
		_, err := h.do(own, false, anon, atomRoute.Method, probePath(atomRoute.Pattern), nil, atomRoute)
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
				if readSide && !a.ReadSide.IsZero() {
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
		if !row.ReadSide.IsZero() {
			mutated = books.AtomTable()
			mutated[i].ReadSide = rbac.Need("core:users.manage")
			if len(resolverDiffs(resolver(mutated))) == 0 {
				t.Errorf("changing the %s row's read side went unreported", row.Name)
			}
		}
	}
}

// TestRegisteredHarness_SeedHEADKeys: the Seed hook and SeedAPIKey, the pattern
// B10e3 uses for a revoked abk_ key and a Confined (fp-worker-style) one. The seeded
// rows are imported by M3 like HEAD's own: the revoked key is refused wherever it is
// shown, the confined key reaches the worker API and nothing else.
func TestRegisteredHarness_SeedHEADKeys(t *testing.T) {
	var revoked, worker string
	h := bootRegistered(t, registeredConfig{Seed: func(ctx context.Context, lr storage.LegacyRange) (err error) {
		if revoked, err = SeedAPIKey(ctx, lr, KeySeed{User: "lucius.fox", Status: "revoked", Scopes: []string{"library.view"}}); err != nil {
			return err
		}
		worker, err = SeedAPIKey(ctx, lr, KeySeed{User: "fp.worker", Scopes: []string{"fingerprint.worker"}})
		return err
	}})
	as := func(raw, method, path string) Result {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+raw)
		req.RemoteAddr = h.nextPeer()
		return h.Raw(req)
	}
	h.Refused(as(revoked, http.MethodGet, "/api/v1/auth/me"), http.StatusUnauthorized, "revoked")
	if res := as(worker, http.MethodPost, "/api/v1/fingerprint/worker/lease"); res.Verdict() != 'A' {
		t.Errorf("the confined worker key on the worker API: %d %s", res.Status, res.excerpt())
	}
	h.Refused(as(worker, http.MethodGet, "/api/v1/audiobooks"), http.StatusForbidden, "permission_denied")
}
