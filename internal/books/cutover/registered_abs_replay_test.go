// file: internal/books/cutover/registered_abs_replay_test.go
// version: 0.1.0
// guid: 7c4e1b92-3d58-4a60-9f27-b81e6a05d4c3
// last-edited: 2026-11-22

package cutover_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
	"github.com/falkcorp/mediacore/storage"
)

// The ABS replay (F4-08 B10e3, discharge of F4-07-ABS-REPLAY).
//
// R0 recorded the 64 routes the ABS handler authenticates itself (the routes the
// oracle delegates: stage 12 and 13 never run on them, they are OwnAuth) for each of
// the 37 principals: 2368 cells that nothing compared. This file sends the same
// request, with the same credential, to the flipped build the registered-path
// harness boots, and compares the letter of every cell with R0's.
//
// The comparison, cell by cell:
//
//   - the flip admits where R0 refused: a WIDENING. It fails the test whatever the
//     known-differences table says, and the table cannot hold one.
//   - the flip refuses where R0 admitted: a narrowing. It must be a row of
//     absKnownDifferences, with a comment; a narrowing that is not listed fails,
//     and so does a listed row that no longer differs.
//   - a refusal keeps R0's status (401 or 403), so a client's cue to re-authenticate
//     is the same.
//   - no cell answers 5xx.
//
// The credentials are the harness's: a session is a cookie (the ABS surface does
// not read one, so every session cell is a refusal, as at R0), a key is an abk_
// bearer (HEAD's key format, imported by M3), anonymous sends none, and authoff is
// the principal of a boot with core.auth.mode disabled (a second boot, because the
// harness boots one registered build at a time).
//
// F-1's outcome on this surface (seams.d/F4-07.md, F-1). The three fp-worker keys
// scoped outside the worker scope (view, settings, users) are admitted by the ABS
// handler at R0 and are admitted by the flipped build on exactly the same 57 routes:
// compat.CheckCredentialFor refuses a Confined key only outside its scopes, and
// these keys are not Confined, so core's checker does not close the gap F-1 closes on
// /api/v1. They are refused on the 7 collection write routes, as at R0 (the
// in-handler collections.manage check). absFP1Admitted pins the 171 cells. This is
// parity, not a widening; it is the statement F-1 needed for this surface.

const (
	// absReplayCells is the bar reachBarABSReplay computes: the 64 delegated routes
	// times the 37 principals of verdicts.json.
	absReplayCells  = 2368
	absReplayRoutes = 64
	absReplayPrinc  = 37
	// absFP1Admitted: key:fp-worker:{view,settings,users} on the 57 delegated routes
	// that carry no in-handler permission check.
	absFP1Admitted = 171
	// absJWTSecret is the ABS JWT secret bootRegistered configures; the own-credential
	// tests mint tokens with it. TestRegisteredAbs_OwnCredentialRefusals proves it is
	// the booted one (a login token must parse under it).
	absJWTSecret = "synthetic-registered-harness-secret-0123456789abcdef"
)

// absGated are the R0 abs_only routes R2 gates by a Public requirement, so the oracle
// compares them and the replay leaves them out (reach_baseline_test.go
// reachABSOnlyButGated, the same 11).
var absGated = []string{
	"GET /api/items/:id/cover",
	"GET /auth/openid",
	"GET /auth/openid/browser",
	"GET /auth/openid/callback",
	"GET /ping",
	"GET /public/session/:id/track/:index",
	"GET /status",
	"HEAD /api/items/:id/cover",
	"POST /auth/openid/browser",
	"POST /auth/refresh",
	"POST /login",
}

// absNewerThanR0 are the OwnAuth routes R2 registers that R0's router at the pinned
// tag does not have, so there is no recorded cell to compare (reach_baseline_test.go
// reachDelegatedNewerThanR0, the same 4).
var absNewerThanR0 = []string{
	"GET /api/authors/:id/image",
	"GET /api/libraries/:libraryId/stats",
	"HEAD /api/authors/:id/image",
	"POST /api/items/batch/get",
}

// absKnownDifferences are the cells where the flip refuses and R0 admitted, one row
// each ("principal|METHOD path"), with why. A row is a decision for the F4-08 lead,
// never something to widen back. There are none: the replay found the flip equal to
// R0 on all 2368 cells.
var absKnownDifferences = map[string]string{}

type absR0Route struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	ABSOnly bool   `json:"abs_only"`
	Status  []int  `json:"status"`
	Verdict string `json:"verdict"`
}

func (r absR0Route) key() string { return r.Method + " " + r.Path }

type absR0Doc struct {
	Principals []string     `json:"principals"`
	Routes     []absR0Route `json:"routes"`
}

func loadAbsR0(t testing.TB) absR0Doc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), reachRel, "verdicts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var d absR0Doc
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// absCell is one (route, principal) cell: R0's letter and status and the flip's.
type absCell struct {
	route, principal string
	head, flip       byte
	headSt, flipSt   int
}

func (c absCell) key() string { return c.principal + "|" + c.route }

var absBraces = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// absTopology classifies the 75 R0 abs_only routes against the booted routes: the
// delegated ones (OwnAuth: replayed), the gated ones (not OwnAuth), and the OwnAuth
// routes of the build that R0 has no cell for.
type absTopology struct {
	delegated []absR0Route
	gated     []string
	newer     []string
	missing   []string // an abs_only route the build does not register
	dup       []string // two R0 routes that resolve to one registered route
}

func (h *Registered) absTopology(d absR0Doc) absTopology {
	var top absTopology
	seen := map[string]string{}
	for _, r := range d.Routes {
		if !r.ABSOnly {
			continue
		}
		ri := h.match(r.Method, probePath(r.Path))
		if ri == nil {
			top.missing = append(top.missing, r.key())
			continue
		}
		reg := ri.Method + " " + ri.Pattern
		if prev, ok := seen[reg]; ok {
			top.dup = append(top.dup, prev+" and "+r.key()+" -> "+reg)
		}
		seen[reg] = r.key()
		if !ri.OwnAuth {
			top.gated = append(top.gated, r.key())
			continue
		}
		top.delegated = append(top.delegated, r)
	}
	for _, ri := range h.Routes {
		if ri.OwnAuth && seen[ri.Method+" "+ri.Pattern] == "" {
			top.newer = append(top.newer, ri.Method+" "+absBraces.ReplaceAllString(ri.Pattern, ":$1"))
		}
	}
	sort.Strings(top.gated)
	sort.Strings(top.newer)
	return top
}

// replay sends every delegated route as every principal of the boot that names an
// R0 column and returns the cells. Each request is the recorder's: the route's probe
// path, an empty JSON object for a POST, PUT or PATCH, and the principal's own
// credential.
func (h *Registered) absReplay(t *testing.T, d absR0Doc, routes []absR0Route, use func(name string) bool) []absCell {
	t.Helper()
	var cells []absCell
	for _, r := range routes {
		var body any
		if slices.Contains([]string{http.MethodPost, http.MethodPut, http.MethodPatch}, r.Method) {
			body = map[string]any{}
		}
		for i, name := range d.Principals {
			if !use(name) {
				continue
			}
			res, err := h.Do(h.Principal(name), r.Method, probePath(r.Path), body)
			if !errors.Is(err, ErrOwnAuth) {
				// A delegated route must answer as an OwnAuth one: anything else means
				// the route is judged by stage 13 (or not by anything) and is not what
				// this replay is about.
				t.Fatalf("%s as %s: want ErrOwnAuth, got %v (status %d)", r.key(), name, err, res.Status)
			}
			cells = append(cells, absCell{route: r.key(), principal: name, head: r.Verdict[i], flip: res.Verdict(), headSt: r.Status[i], flipSt: res.Status})
		}
	}
	return cells
}

// absProblems judges the cells against R0, as text.
func absProblems(cells []absCell, known map[string]string) []string {
	var out []string
	differs := map[string]bool{}
	for _, c := range cells {
		switch {
		case c.head == 'D' && c.flip == 'A':
			out = append(out, fmt.Sprintf("WIDENING %s: R0 %d, flip %d (the flip admits what R0 refused)", c.key(), c.headSt, c.flipSt))
		case c.head == 'A' && c.flip == 'D':
			differs[c.key()] = true
			if _, ok := known[c.key()]; !ok {
				out = append(out, fmt.Sprintf("narrowing %s: R0 %d, flip %d, not in absKnownDifferences", c.key(), c.headSt, c.flipSt))
			}
		case c.head == 'D' && c.headSt != c.flipSt:
			out = append(out, fmt.Sprintf("refusal status %s: R0 %d, flip %d", c.key(), c.headSt, c.flipSt))
		}
		if c.flipSt >= 500 {
			out = append(out, fmt.Sprintf("5xx %s: %d", c.key(), c.flipSt))
		}
	}
	for k := range known {
		if !differs[k] {
			out = append(out, "absKnownDifferences lists "+k+", which no longer differs from R0")
		}
	}
	sort.Strings(out)
	return out
}

// TestReachRegisteredPath_AbsReplay is the discharge of F4-07-ABS-REPLAY: the 2368
// recorded cells of the 64 delegated ABS routes, replayed against the registered path.
func TestReachRegisteredPath_AbsReplay(t *testing.T) {
	d := loadAbsR0(t)
	if len(d.Principals) != absReplayPrinc {
		t.Fatalf("verdicts.json lists %d principals, want %d", len(d.Principals), absReplayPrinc)
	}
	var cells []absCell
	var top absTopology

	t.Run("sessions, keys and anonymous", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		for _, p := range h.Principals {
			if p.Kind == "key" && !p.Core && !strings.HasPrefix(p.bearer, "abk_") {
				t.Fatalf("%s holds a key that is not HEAD's abk_ format: the ABS surface would not even read it", p.Name)
			}
		}
		top = h.absTopology(d)
		cells = h.absReplay(t, d, top.delegated, func(n string) bool { return n != Authoff })
	})
	if len(top.delegated) == 0 {
		return // the first boot failed; its error is above
	}
	t.Run("auth-off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		cells = append(cells, h.absReplay(t, d, top.delegated, func(n string) bool { return n == Authoff })...)
	})

	// The topology: the replay's 64 are R0's abs_only routes less the 11 Public ones,
	// and the build has the 4 OwnAuth routes R0 does not.
	if len(top.missing) != 0 || len(top.dup) != 0 {
		t.Errorf("abs_only routes the build does not register: %v; two R0 routes on one registered route: %v", top.missing, top.dup)
	}
	if len(top.delegated) != absReplayRoutes {
		t.Errorf("%d delegated routes, want %d", len(top.delegated), absReplayRoutes)
	}
	if !slices.Equal(top.gated, absGated) {
		t.Errorf("the gated abs_only routes are %v, want %v", top.gated, absGated)
	}
	if !slices.Equal(top.newer, absNewerThanR0) {
		t.Errorf("the OwnAuth routes with no R0 cell are %v, want %v", top.newer, absNewerThanR0)
	}

	// The count is exact: no more and no fewer than the bar (reachBarABSReplay).
	if len(cells) != absReplayCells {
		t.Fatalf("replayed %d cells, want exactly %d (%d routes x %d principals)", len(cells), absReplayCells, absReplayRoutes, absReplayPrinc)
	}
	for _, p := range absProblems(cells, absKnownDifferences) {
		t.Error(p)
	}

	// F-1 on the ABS surface: parity with R0 on the three fp-worker keys.
	fp1 := map[string]bool{"key:fp-worker:view": true, "key:fp-worker:settings": true, "key:fp-worker:users": true}
	admitted, refused := 0, 0
	for _, c := range cells {
		if !fp1[c.principal] {
			continue
		}
		if c.flip == 'A' {
			admitted++
		} else {
			refused++
		}
	}
	if admitted != absFP1Admitted || refused != 3*absReplayRoutes-absFP1Admitted {
		t.Errorf("F-1 on the ABS surface: key:fp-worker:{view,settings,users} are admitted on %d cells and refused on %d, want %d and %d (R0's)",
			admitted, refused, absFP1Admitted, 3*absReplayRoutes-absFP1Admitted)
	}
	a, dn := 0, 0
	for _, c := range cells {
		if c.flip == 'A' {
			a++
		} else {
			dn++
		}
	}
	t.Logf("replayed %d cells over %d routes: the flip admits %d and refuses %d, R0's own split", len(cells), len(top.delegated), a, dn)
}

// ---- ABS credentials that are not in the principal table ----

func absLogin(t *testing.T, h *Registered, user, password string) (token, sub, sid string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": user, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := h.Raw(req)
	var lr struct {
		User struct {
			AccessToken string `json:"accessToken"`
		} `json:"user"`
	}
	if res.Status != http.StatusOK || json.Unmarshal(res.Body, &lr) != nil || lr.User.AccessToken == "" {
		t.Fatalf("ABS login of %s: %d", user, res.Status)
	}
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(lr.User.AccessToken, claims); err != nil {
		t.Fatalf("the login token does not parse: %v", err)
	}
	sub, _ = claims["sub"].(string)
	sid, _ = claims["sid"].(string)
	if sub == "" || sid == "" {
		t.Fatal("the login token names no user or session")
	}
	return lr.User.AccessToken, sub, sid
}

// absGET sends GET /api/libraries (a delegated route every principal of the recorded
// ABS columns is judged on) with extra headers and returns the status and the error
// the ABS handler wrote.
func absGET(h *Registered, header http.Header) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	for k, v := range header {
		req.Header[k] = v
	}
	res := h.Raw(req)
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(res.Body, &e)
	return res.Status, e.Error
}

func bearer(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }

// TestRegisteredAbs_OwnCredentialRefusals: the credentials the ABS surface verifies
// itself and the table cannot hold (an ABS access token is minted at sign-in): after
// M3 moved accounts into core, a token the surface refused at HEAD is still refused,
// and one it admitted still is.
func TestRegisteredAbs_OwnCredentialRefusals(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	token, sub, sid := absLogin(t, h, "lucius.fox", passwordGotham)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, AuthModes: "jwt", JWTSecret: absJWTSecret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.ParseAccessToken(token, time.Now()); err != nil {
		t.Fatalf("the harness' ABS secret is not the one this test mints with: %v", err)
	}

	t.Run("a live token is admitted", func(t *testing.T) {
		if st, msg := absGET(h, bearer(token)); st != http.StatusOK {
			t.Fatalf("the sign-in's own token: %d %q, want 200", st, msg)
		}
	})
	t.Run("an expired token of a live session is refused", func(t *testing.T) {
		// The session is live (the sign-in above), the signature is the real one;
		// only exp is past. A surface that did not judge exp would admit it.
		old, _, err := cfg.MintAccessToken(sub, sid, time.Now().Add(-cfg.AccessTTL-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if st, msg := absGET(h, bearer(old)); st != http.StatusUnauthorized || msg != "access token expired" {
			t.Fatalf("expired token: %d %q, want 401 \"access token expired\"", st, msg)
		}
	})
	t.Run("a token of a session that does not exist is refused", func(t *testing.T) {
		tok, _, err := cfg.MintAccessToken(sub, "r2reach-missing-session", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if st, msg := absGET(h, bearer(tok)); st != http.StatusUnauthorized || msg != "session not found" {
			t.Fatalf("token of no session: %d %q, want 401 \"session not found\"", st, msg)
		}
	})
	t.Run("a token signed with another secret is refused", func(t *testing.T) {
		other, err := absauth.Load(absauth.Settings{Enabled: true, AuthModes: "jwt", JWTSecret: "another-synthetic-secret-0123456789abcdef-gotham"})
		if err != nil {
			t.Fatal(err)
		}
		tok, _, err := other.MintAccessToken(sub, sid, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if st, msg := absGET(h, bearer(tok)); st != http.StatusUnauthorized || msg != "invalid access token" {
			t.Fatalf("forged token: %d %q, want 401 \"invalid access token\"", st, msg)
		}
	})
	t.Run("no credential is refused", func(t *testing.T) {
		if st, msg := absGET(h, nil); st != http.StatusUnauthorized || msg != "authentication required" {
			t.Fatalf("no credential: %d %q, want 401 \"authentication required\"", st, msg)
		}
	})
}

// TestRegisteredAbs_CFModeRefusals: the ABS surface's Cloudflare Access sign-in
// (ABS auth modes "cf,jwt"), where R0 JIT-provisioned an account for an allow-listed
// address. R0's recording has no Access verifier, so no table holds it. Under the
// flip the person goes to core's resolver under the Access admission policy: an
// address that is not on the allow-list is refused 403 as at HEAD, a forged
// assertion is refused 401 and never rescued by a second credential, and the
// allow-listed owner is admitted (the control: the refusals are not a surface that
// admits nobody).
func TestRegisteredAbs_CFModeRefusals(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true, ABSModes: "cf,jwt"})
	assert := func(sub, email string) http.Header {
		return http.Header{"Cf-Access-Jwt-Assertion": {h.access.assertion(t, sub, email)}}
	}
	t.Run("the allow-listed owner is admitted", func(t *testing.T) {
		if st, msg := absGET(h, assert(ownerSub, ownerEmail)); st != http.StatusOK {
			t.Fatalf("the owner's assertion: %d %q, want 200", st, msg)
		}
	})
	t.Run("a verified person who is not on the allow-list is refused and gets no account", func(t *testing.T) {
		users := func() []string {
			res := h.MustDo(t, h.Principal("session:admin"), http.MethodGet, "/api/core/v1/users?limit=500", nil)
			var page struct {
				Items []struct {
					ID       string `json:"id"`
					Username string `json:"username"`
				} `json:"items"`
			}
			if res.Status != http.StatusOK || json.Unmarshal(res.Body, &page) != nil {
				t.Fatalf("core's user list as the admin: %d %s", res.Status, res.excerpt())
			}
			var ids []string
			for _, u := range page.Items {
				ids = append(ids, u.ID+" "+u.Username)
			}
			slices.Sort(ids)
			return ids
		}
		before := users()
		if len(before) == 0 || !slices.ContainsFunc(before, func(u string) bool { return strings.HasSuffix(u, " lucius.fox") }) {
			t.Fatalf("core's user list is not the corpus's (%d users): the no-account check would prove nothing", len(before))
		}
		st, msg := absGET(h, assert("r2reach-sub-selina", "selina.kyle@example.invalid"))
		if st != http.StatusForbidden || msg != "identity is not authorized for this server" {
			t.Fatalf("an address off the allow-list: %d %q, want 403 \"identity is not authorized for this server\"", st, msg)
		}
		if after := users(); !slices.Equal(before, after) {
			t.Errorf("a refused Access sign-in changed core's accounts: %d users before, %d after", len(before), len(after))
		}
	})
	t.Run("a tampered assertion is refused and not rescued by a bearer", func(t *testing.T) {
		token, _, _ := absLogin(t, h, "lucius.fox", passwordGotham)
		good := h.access.assertion(t, ownerSub, ownerEmail)
		i := len(good) - 6
		repl := "A"
		if good[i:i+1] == "A" {
			repl = "B"
		}
		hd := bearer(token)
		hd.Set("Cf-Access-Jwt-Assertion", good[:i]+repl+good[i+1:])
		if st, msg := absGET(h, hd); st != http.StatusUnauthorized || msg != "invalid Cloudflare Access assertion" {
			t.Fatalf("a tampered assertion beside a live bearer: %d %q, want 401 \"invalid Cloudflare Access assertion\"", st, msg)
		}
	})
}

// absBearer sends one request with a raw ABS bearer key.
func absBearer(h *Registered, raw, method, path string) Result {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	req.RemoteAddr = h.nextPeer()
	return h.Raw(req)
}

// TestRegisteredAbs_SeededKeys: the two key states the 37-principal table cannot show
// the ABS handler, seeded as HEAD rows (SeedAPIKey) so M3 imports them as it does
// prod's. The revoked key is refused 401 as at HEAD. The Confined one (scope
// fingerprint.worker, the fp-worker key) is refused 403 with HEAD's own message,
// because a worker-only credential never reaches the ABS surface (HEAD's
// abortWorkerOnlyOutsideWorkerAPI): after the flip that refusal is
// compat.CheckCredentialFor's (core's Confined rule), the one line of the ABS
// handler no table cell reaches. The live key is the control.
func TestRegisteredAbs_SeededKeys(t *testing.T) {
	var live, revoked, worker string
	h := bootRegistered(t, registeredConfig{Seed: func(ctx context.Context, lr storage.LegacyRange) (err error) {
		if live, err = SeedAPIKey(ctx, lr, KeySeed{User: "lucius.fox", Scopes: []string{"library.view"}}); err != nil {
			return err
		}
		if revoked, err = SeedAPIKey(ctx, lr, KeySeed{User: "lucius.fox", Status: "revoked", Scopes: []string{"library.view"}}); err != nil {
			return err
		}
		worker, err = SeedAPIKey(ctx, lr, KeySeed{User: "fp.worker", Scopes: []string{"fingerprint.worker"}})
		return err
	}})
	for name, raw := range map[string]string{"live": live, "revoked": revoked, "worker": worker} {
		if !strings.HasPrefix(raw, "abk_") {
			t.Fatalf("the %s seeded key is %q..., want HEAD's abk_ shape", name, raw[:min(4, len(raw))])
		}
	}
	const route = "/api/libraries"
	if res := absBearer(h, live, http.MethodGet, route); res.Status != http.StatusOK {
		t.Fatalf("the live seeded key: %d %s, want 200 (the control)", res.Status, res.excerpt())
	}
	t.Run("a revoked key is refused", func(t *testing.T) {
		if res := absBearer(h, revoked, http.MethodGet, route); res.Status != http.StatusUnauthorized {
			t.Fatalf("revoked key: %d %s, want 401", res.Status, res.excerpt())
		}
	})
	t.Run("a worker-confined key is refused outside the worker API", func(t *testing.T) {
		res := absBearer(h, worker, http.MethodGet, route)
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &e)
		if res.Status != http.StatusForbidden || e.Error != "this credential is limited to the fingerprint worker API" {
			t.Fatalf("worker-confined key: %d %s, want 403 with HEAD's message", res.Status, res.excerpt())
		}
		// A write route is refused the same way, not merely a read.
		if res := absBearer(h, worker, http.MethodPost, "/api/collections"); res.Status != http.StatusForbidden {
			t.Fatalf("worker-confined key on a write route: %d %s, want 403", res.Status, res.excerpt())
		}
	})
}

// TestRegisteredAbs_RemoveFromContinueListeningServed: HEAD registers
// GET /api/me/item/:id/remove-from-continue-listening as a tolerated alias
// (handlers/abs/handler.go, beside the POST and the /api/me/progress pair). The flip
// folds the GET into an OwnAuth pattern of its own, so a route-table check cannot say
// the handler still answers it. This sends the request with a real library item id
// and expects the handler's answer, not a not-found; a path beside it that the fold
// must not swallow stays a 404, and the anonymous caller is refused (401).
func TestRegisteredAbs_RemoveFromContinueListeningServed(t *testing.T) {
	h := bootRegistered(t, registeredConfig{})
	key := h.Principal("key:admin:every").bearer
	var libs struct {
		Libraries []struct {
			ID string `json:"id"`
		} `json:"libraries"`
	}
	if res := absBearer(h, key, http.MethodGet, "/api/libraries"); res.Status != http.StatusOK || json.Unmarshal(res.Body, &libs) != nil || len(libs.Libraries) == 0 {
		t.Fatalf("libraries: %d %s", res.Status, res.excerpt())
	}
	var items struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if res := absBearer(h, key, http.MethodGet, "/api/libraries/"+libs.Libraries[0].ID+"/items?limit=1"); res.Status != http.StatusOK || json.Unmarshal(res.Body, &items) != nil || len(items.Results) == 0 {
		t.Fatalf("library items: %d %s", res.Status, res.excerpt())
	}
	id := items.Results[0].ID
	if ri := h.match(http.MethodGet, "/api/me/item/"+id+"/remove-from-continue-listening"); ri == nil || !ri.OwnAuth {
		t.Fatalf("GET /api/me/item/:id/remove-from-continue-listening is not an OwnAuth route of the flipped build: %+v", ri)
	}
	// The GET alias alone does the work: the item is hidden from Continue Listening
	// afterwards (read back through /api/me), not merely answered 200.
	hidden := func() (hide, present bool) {
		res := absBearer(h, key, http.MethodGet, "/api/me")
		var me struct {
			MediaProgress []struct {
				LibraryItemID string `json:"libraryItemId"`
				Hide          bool   `json:"hideFromContinueListening"`
			} `json:"mediaProgress"`
		}
		_ = json.Unmarshal(res.Body, &me)
		for _, m := range me.MediaProgress {
			if m.LibraryItemID == id {
				return m.Hide, true
			}
		}
		return false, false
	}
	// Give the item a started progress row first: /api/me lists only items the
	// listener has started, and the control read must show the row unhidden.
	patch := httptest.NewRequest(http.MethodPatch, "/api/me/progress/"+id, strings.NewReader(`{"currentTime":30,"duration":3600}`))
	patch.Header.Set("Authorization", "Bearer "+key)
	patch.Header.Set("Content-Type", "application/json")
	patch.RemoteAddr = h.nextPeer()
	if res := h.Raw(patch); res.Status != http.StatusOK {
		t.Fatalf("starting the item: %d %s", res.Status, res.excerpt())
	}
	if hide, present := hidden(); hide || !present {
		t.Fatalf("before the alias: hidden %v, listed in /api/me %v, want listed and not hidden: the effect check would prove nothing", hide, present)
	}
	if res := absBearer(h, key, http.MethodGet, "/api/me/item/"+id+"/remove-from-continue-listening"); res.Status != http.StatusOK {
		t.Fatalf("GET alias: %d %s", res.Status, res.excerpt())
	}
	if hide, present := hidden(); !hide || !present {
		t.Errorf("GET /api/me/item/:id/remove-from-continue-listening answered 200 but did not hide the item (hidden %v, listed %v)", hide, present)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/me/item/" + id + "/remove-from-continue-listening"},
		{http.MethodPost, "/api/me/item/" + id + "/remove-from-continue-listening"},
		{http.MethodGet, "/api/me/progress/" + id + "/remove-from-continue-listening"},
		{http.MethodPost, "/api/me/progress/" + id + "/remove-from-continue-listening"},
	} {
		res := absBearer(h, key, c.method, c.path)
		var obj map[string]any
		if res.Status != http.StatusOK || json.Unmarshal(res.Body, &obj) != nil {
			t.Errorf("%s %s: %d %s, want the handler's 200 and a JSON object", c.method, c.path, res.Status, res.excerpt())
		}
	}
	if res := absBearer(h, key, http.MethodGet, "/api/me/item/"+id+"/nonsense"); res.Status != http.StatusNotFound {
		t.Errorf("a path beside the folded route: %d %s, want 404", res.Status, res.excerpt())
	}
	anon := httptest.NewRequest(http.MethodGet, "/api/me/item/"+id+"/remove-from-continue-listening", nil)
	anon.RemoteAddr = h.nextPeer()
	if res := h.Raw(anon); res.Status != http.StatusUnauthorized {
		t.Errorf("the anonymous caller: %d %s, want 401", res.Status, res.excerpt())
	}
}
