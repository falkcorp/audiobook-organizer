// file: internal/books/cutover/registered_reach_eval_test.go
// version: 0.1.0
// guid: 9d4b7e21-5a63-4c18-b0f7-2e8c1a6d3f95
// last-edited: 2026-11-22

package cutover_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/httpapi"
)

// The reach oracle on the registered path (F4-08 B10f).
//
// It replaces the F4-07 oracle's hand evaluator (reach_eval_test.go, a test-local
// copy of core's Compile, Has and Satisfies). Nothing here evaluates a
// requirement. Every cell is the status the flipped build answers: stage 12 binds
// the real principal, httpapi.Authorize (stage 13) decides, and the verdict is the
// harness's Verdict() letter for that answer, compared with the letter R0 recorded
// for the same route and principal (internal/books/rbacreach/verdicts.json).
//
// A cell where the flip admits and R0 refused is a widening and fails the test
// with no way to list it. A cell where the flip refuses and R0 admitted is a
// narrowing: it must be on a named row of registeredNarrowings with the exact
// number of cells the row claims, so a change in either direction fails.
//
// What it sends is the recorder's request (record_r0_reach.py): probe_path with
// the dummy id, an empty JSON object for POST, PUT and PATCH, the config PUTs'
// refused bodies, one invite token per principal, and the session-killing route
// last with a fresh sign-in after it. A canary before and after each principal's
// pass proves the credential stood throughout, so a dead session cannot read D on
// every route and hide a widening.
//
// It does not use Registered.Do: Do reads the answer through a plain recorder, which
// blocks on an admitted event-stream route until the request ends. probeOnce takes
// the status at the first write instead and cancels the request.

// ---- R0's recording ----

type r0Route struct {
	Method    string `json:"method"`
	Path      string `json:"path"`
	ProbePath string `json:"probe_path"`
	ABSOnly   bool   `json:"abs_only"`
	Verdict   string `json:"verdict"`
}

func (r r0Route) key() string { return r.Method + " " + r.Path }

type r0Recording struct {
	Principals []string       `json:"principals"`
	Routes     []r0Route      `json:"routes"`
	Canary     map[string]int `json:"canary"`
}

func loadR0Recording(t testing.TB) r0Recording {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "internal/books/rbacreach", "verdicts.json"))
	if err != nil {
		t.Fatalf("the recorded R0 answers: %v", err)
	}
	var r r0Recording
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("verdicts.json: %v", err)
	}
	for _, rt := range r.Routes {
		if len(rt.Verdict) != len(r.Principals) {
			t.Fatalf("%s: %d letters for %d principals", rt.key(), len(rt.Verdict), len(r.Principals))
		}
	}
	return r
}

// ---- the recorder's requests ----

const (
	invitePath   = "/api/v1/auth/invite/:token"
	acceptPath   = "/api/v1/auth/accept-invite"
	logoutPath   = "/api/v1/auth/logout"
	canaryPath   = "/api/v1/auth/me"
	canaryAuthOf = "/api/v1/auth/status"
)

// probeBody is record_r0_reach.py probe_body: an empty object for a writing method,
// and for the two routes that would write the stored config on an empty body, one
// their handlers refuse with a 400 before any write.
func probeBody(method, path string) any {
	switch method + " " + path {
	case "PUT /api/v1/config":
		return map[string]any{"r2reach_probe": true}
	case "PUT /api/v1/maintenance-window/config":
		return map[string]any{"window_start": -1}
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		return map[string]any{}
	}
	return nil
}

var tokenSlug = regexp.MustCompile(`[^A-Za-z0-9]+`)

// probeRequest is probe_request: the path with the dummy id, the body, and for the
// two public invite routes a token of the principal's own (core bounds them per
// token).
func probeRequest(method string, r r0Route, principal string) (string, any) {
	tok := "r2reach-missing-" + tokenSlug.ReplaceAllString(principal, "-")
	switch r.key() {
	case "GET " + invitePath:
		return strings.Replace(probePath(r.Path), "r2reach-missing", tok, 1), nil
	case "POST " + acceptPath:
		return probePath(r.Path), map[string]any{"token": tok}
	}
	return probePath(r.Path), probeBody(method, r.Path)
}

// ---- sending one request ----

// firstWrite is a response writer that signals the first status written, so a
// caller can read the status line of a response whose body never ends.
type firstWrite struct {
	*httptest.ResponseRecorder
	got  chan struct{}
	once sync.Once
}

func (w *firstWrite) WriteHeader(code int) {
	w.ResponseRecorder.WriteHeader(code)
	w.once.Do(func() { close(w.got) })
}

func (w *firstWrite) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	w.once.Do(func() { close(w.got) })
	return n, err
}

// Flush marks the response started even when the handler has written nothing yet
// (an event stream's first flush).
func (w *firstWrite) Flush() {
	w.ResponseRecorder.Flush()
	w.once.Do(func() { close(w.got) })
}

// probeOnce sends one request as p through the root handler and answers with its
// status line. The handler runs in its own goroutine: once it has written a status
// the request is cancelled if the handler is still running, and the call returns
// when the handler has.
func (h *Registered) probeOnce(p *Principal, method, path string, body any, ri *httpapi.RouteInfo) (Result, error) {
	h.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return Result{}, err
		}
		rd = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var req *http.Request
	if rd != nil {
		req = httptest.NewRequest(method, path, rd).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil).WithContext(ctx)
	}
	req.RemoteAddr = h.peerFor(p)
	switch {
	case p.cookie != nil:
		req.AddCookie(p.cookie)
	case p.bearer != "":
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	case p.access != "":
		req.Header.Set("Cf-Access-Jwt-Assertion", p.access)
	}
	w := &firstWrite{ResponseRecorder: httptest.NewRecorder(), got: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Handler.ServeHTTP(w, req)
	}()
	select {
	case <-done:
	case <-w.got:
		select {
		case <-done:
		case <-time.After(250 * time.Millisecond):
			cancel()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				return Result{}, fmt.Errorf("%s %s as %s: the handler did not return after its request was cancelled", method, path, p.Name)
			}
		}
	case <-time.After(30 * time.Second):
		cancel()
		return Result{}, fmt.Errorf("%s %s as %s: no status within 30s", method, path, p.Name)
	}
	return resultOf(w.ResponseRecorder, ri), nil
}

// ---- one principal's pass ----

// oracleCell is one answered probe.
type oracleCell struct {
	letter byte
	status int
	code   string
}

// probeCell sends the recorder's request for route r as p.
func (h *Registered) probeCell(p *Principal, r r0Route, ri *httpapi.RouteInfo) (Result, error) {
	h.t.Helper()
	path, body := probeRequest(r.Method, r, p.Name)
	return h.probeOnce(p, r.Method, path, body, ri)
}

func (h *Registered) canaryFor(p *Principal) int {
	h.t.Helper()
	path := canaryPath
	if p.Name == Authoff {
		path = canaryAuthOf
	}
	res, err := h.probeOnce(p, http.MethodGet, path, nil, nil)
	if err != nil {
		h.t.Fatalf("canary of %s: %v", p.Name, err)
	}
	return res.Status
}

// oraclePass is everything one boot's principals answered for R0's routes.
type oraclePass struct {
	// cells[route key][principal name]
	cells map[string]map[string]oracleCell
	// probed are R0's routes a registered pattern judges by stage 13.
	probed []string
	// delegated are R0's routes the flip registers as OwnAuth: stage 13 never runs on
	// them and ABS's own check answers (B10e3's replay holds them).
	delegated []string
	// unregistered are R0's routes no registered pattern matches.
	unregistered []string
	// unregisteredAnswers[route key][principal name] is the status the flip answers.
	unregisteredAnswers map[string]map[string]int
	// notJudged are routes stage 13 must judge where an anonymous request was not
	// refused by stage 13: an admit there would be the handler's, a hole.
	notJudged []string
	// noVerdict are probes that answered a 429 or failed to answer.
	noVerdict []string
	// serverErrors counts the 5xx answers per route (a handler's, behind an admit).
	serverErrors map[string]int
	// canary is each principal's canary status before its pass.
	canary map[string]int
	// canaryDrift lists principals whose canary changed over their pass.
	canaryDrift []string
}

// frontedBy is whether stage 13 stands in front of ri: an anonymous request to it is
// refused with stage 13's authentication_required (read through probeOnce, so an
// unfronted event stream cannot block the sweep).
func (h *Registered) frontedBy(cache map[string]bool, ri *httpapi.RouteInfo) bool {
	key := ri.Method + " " + ri.Pattern
	if v, ok := cache[key]; ok {
		return v
	}
	res, err := h.probeOnce(&Principal{Name: Anonymous}, ri.Method, probePath(ri.Pattern), nil, ri)
	ok := err == nil && res.Stage13 == Stage13Refused && res.Code == "authentication_required"
	cache[key] = ok
	return ok
}

// runOracle probes every R0 route as every principal of this boot.
func (h *Registered) runOracle(r0 r0Recording) *oraclePass {
	h.t.Helper()
	pass := &oraclePass{
		cells:               map[string]map[string]oracleCell{},
		unregisteredAnswers: map[string]map[string]int{},
		serverErrors:        map[string]int{},
		canary:              map[string]int{},
	}
	front := map[string]bool{}

	type target struct {
		r  r0Route
		ri *httpapi.RouteInfo
	}
	var targets, strays []target
	for _, r := range r0.Routes {
		ri := h.match(r.Method, probePath(r.Path))
		switch {
		case ri == nil:
			pass.unregistered = append(pass.unregistered, r.key())
			strays = append(strays, target{r: r})
		case ri.OwnAuth:
			pass.delegated = append(pass.delegated, r.key())
		default:
			targets = append(targets, target{r, ri})
			pass.probed = append(pass.probed, r.key())
		}
	}
	// The session killer goes last, as the recorder orders it.
	sort.SliceStable(targets, func(i, j int) bool {
		return targets[i].r.key() != "POST "+logoutPath && targets[j].r.key() == "POST "+logoutPath
	})
	if !h.cfg.AuthOff {
		for _, tg := range targets {
			if judged(tg.ri) && !h.frontedBy(front, tg.ri) {
				pass.notJudged = append(pass.notJudged, tg.r.key())
			}
		}
	}

	for _, p := range h.Principals {
		before := h.canaryFor(p)
		pass.canary[p.Name] = before
		for _, tg := range targets {
			res, err := h.probeCell(p, tg.r, tg.ri)
			key := tg.r.key()
			if pass.cells[key] == nil {
				pass.cells[key] = map[string]oracleCell{}
			}
			switch {
			case err != nil:
				pass.noVerdict = append(pass.noVerdict, fmt.Sprintf("%s as %s: %v", key, p.Name, err))
				continue
			case res.Status == http.StatusTooManyRequests:
				pass.noVerdict = append(pass.noVerdict, fmt.Sprintf("%s as %s: 429 (the limiter answered, not stage 13)", key, p.Name))
				continue
			}
			if res.Status >= 500 {
				pass.serverErrors[key]++
			}
			pass.cells[key][p.Name] = oracleCell{letter: res.Verdict(), status: res.Status, code: res.Code}
			if key == "POST "+logoutPath && p.cookie != nil {
				p.peer = h.nextPeer()
				p.cookie = h.login(p, passwordFor(p.User))
			}
		}
		for _, st := range strays {
			path, body := probeRequest(st.r.Method, st.r, p.Name)
			res, err := h.probeOnce(p, st.r.Method, path, body, nil)
			if err != nil {
				pass.noVerdict = append(pass.noVerdict, fmt.Sprintf("%s as %s: %v", st.r.key(), p.Name, err))
				continue
			}
			if pass.unregisteredAnswers[st.r.key()] == nil {
				pass.unregisteredAnswers[st.r.key()] = map[string]int{}
			}
			pass.unregisteredAnswers[st.r.key()][p.Name] = res.Status
		}
		if after := h.canaryFor(p); after != before {
			pass.canaryDrift = append(pass.canaryDrift, fmt.Sprintf("%s: canary %d before its pass, %d after", p.Name, before, after))
		}
	}
	return pass
}

// ---- comparing with R0 ----

// oracleDiff is one cell where the flip and R0 disagree.
type oracleDiff struct {
	route, principal string
	r0, flip         byte
	status           int
}

func (d oracleDiff) cell() string { return d.route + " / " + d.principal }

func (d oracleDiff) String() string {
	return fmt.Sprintf("%s: R0 %c, flip %c (%d)", d.cell(), d.r0, d.flip, d.status)
}

// compareToR0 lists every differing cell and how many cells it compared.
func compareToR0(r0 r0Recording, pass *oraclePass) (diffs []oracleDiff, compared int) {
	idx := map[string]int{}
	for i, n := range r0.Principals {
		idx[n] = i
	}
	for _, r := range r0.Routes {
		for n, c := range pass.cells[r.key()] {
			compared++
			if want := r.Verdict[idx[n]]; want != c.letter {
				diffs = append(diffs, oracleDiff{route: r.key(), principal: n, r0: want, flip: c.letter, status: c.status})
			}
		}
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i].cell() < diffs[j].cell() })
	return diffs, compared
}

// ---- the known differences, as named tables ----

// diffRow is one family of expected differences: every route in routes, for every
// principal in who, answers HEAD letter r0 at R0 and letter flip with status on the
// flip.
type diffRow struct {
	routes []string
	who    []string
	r0     byte
	flip   byte
	status int
	why    string
}

// diffTable is a named set of rows with the exact number of cells it claims.
type diffTable struct {
	name string
	want int
	rows []diffRow
}

func (tb diffTable) expand() map[string]diffRow {
	out := map[string]diffRow{}
	for _, row := range tb.rows {
		for _, r := range row.routes {
			for _, w := range row.who {
				out[r+" / "+w] = row
			}
		}
	}
	return out
}

var (
	fpWorkerKeys = []string{"key:fp-worker:view", "key:fp-worker:settings", "key:fp-worker:users"}
	adminKeys    = []string{"key:admin:every", "key:admin:empty"}
)

// registeredFPWorkerNarrowing: a key whose owner holds only the fp-worker role
// reaches no authenticated-only route. 32 routes x 3 keys. It is the F4-07 golden's
// 96-cell row (05-rbac 6.3), now measured through stage 13. Ruled 2026-11-22: the
// fingerprint worker never calls these routes; the PR body names the difference.
var registeredFPWorkerNarrowing = diffTable{
	name: "fp-worker keys reach no authenticated-only route",
	want: 96,
	rows: []diffRow{
		{
			routes: []string{
				"GET /api/v1/auth/me", "GET /api/v1/auth/sessions", "DELETE /api/v1/auth/sessions/:id",
				"GET /api/v1/auth/api-keys", "GET /api/v1/auth/api-keys/:id", "POST /api/v1/auth/api-keys",
				"DELETE /api/v1/auth/api-keys/:id", "POST /api/v1/auth/logout",
			},
			who: fpWorkerKeys, r0: 'A', flip: 'D', status: 403,
			why: "credential-metadata routes: core's native successors need a principal that holds an ambient scope, and the fp-worker role holds none; GET /auth/me is the ruled known difference",
		},
		{
			routes: []string{
				"GET /api/v1/audiobooks/:id/state", "GET /api/v1/audiobooks/:id/position", "POST /api/v1/audiobooks/:id/position",
				"DELETE /api/v1/audiobooks/:id/status", "PATCH /api/v1/audiobooks/:id/status", "POST /api/v1/audiobooks/:id/status/repair",
				"GET /api/v1/books/:id/state", "GET /api/v1/books/:id/position", "POST /api/v1/books/:id/position",
				"DELETE /api/v1/books/:id/status", "PATCH /api/v1/books/:id/status", "POST /api/v1/books/:id/status/repair",
				"GET /api/v1/me/:status",
			},
			who: fpWorkerKeys, r0: 'A', flip: 'D', status: 403,
			why: "per-user reading-state routes: Authenticated-only at HEAD, so a worker's key passed; the key's checker is empty after the cutover",
		},
		{
			routes: []string{
				"POST /api/v1/playlists", "GET /api/v1/playlists/:id", "PUT /api/v1/playlists/:id", "DELETE /api/v1/playlists/:id",
				"POST /api/v1/playlists/:id/books", "DELETE /api/v1/playlists/:id/books/:bookID",
				"POST /api/v1/playlists/:id/reorder", "POST /api/v1/playlists/:id/materialize", "GET /api/v1/playlists/:id/export.m3u",
			},
			who: fpWorkerKeys, r0: 'A', flip: 'D', status: 403,
			why: "playlist routes: Authenticated-only at HEAD",
		},
		{
			routes: []string{"GET /api/events", "GET /metrics"},
			who:    fpWorkerKeys, r0: 'A', flip: 'D', status: 403,
			why: "the event stream and the metrics scrape: Authenticated-only at HEAD",
		},
	},
}

// registeredHeadDrift: cells where HEAD itself changed after R0 was recorded
// (v0.223.0), so the flip matches today's HEAD and not R0's recording. The D-to-A
// row is not a flip widening: HEAD now admits that principal too.
var registeredHeadDrift = diffTable{
	name: "R0 drift: HEAD changed after the recording",
	want: 4,
	rows: []diffRow{
		{
			routes: []string{"POST /api/v1/operations/reconcile"},
			who:    []string{"session:editor", "key:editor:every", "key:editor:empty"}, r0: 'A', flip: 'D', status: 403,
			why: "ed8d80f4b: POST /operations/reconcile enforces the settings.manage that reconcile.apply declares; an editor never held it",
		},
		{
			routes: []string{"POST /api/v1/operations/reconcile"},
			who:    []string{"key:admin:settings"}, r0: 'D', flip: 'A', status: 400,
			why: "ed8d80f4b: the settings-only admin key now passes the route's gate and the handler refuses its empty body (400)",
		},
	},
}

// registeredRuledNarrowings: the 7 cells F4-07 pinned (reachHandlerAdmin's
// narrowing rows and the restore row), reproduced on the flip.
var registeredRuledNarrowings = diffTable{
	name: "narrowings ruled in F4-07 (05-rbac 6.2, 05-authn 4.8)",
	want: 7,
	rows: []diffRow{
		{
			routes: []string{
				"PATCH /api/v1/admin/debug/books/:id", "PATCH /api/v1/admin/debug/book-files/:id", "POST /api/v1/admin/debug/edits/:edit_id/undo",
			},
			who: adminKeys, r0: 'A', flip: 'D', status: 403,
			why: "05-rbac 6.2 / 4.4 rule 1: books:admin.debug is Sensitive, so a non-interactive principal is refused even with core:admin",
		},
		{
			routes: []string{"POST /api/v1/backup/restore"},
			who:    []string{"session:custom-all"}, r0: 'A', flip: 'D', status: 403,
			why: "05-authn 4.8: restore's owner guard needs core:admin when no OwnerVerifier is set, which a custom role never holds",
		},
	},
}

// registeredB10yFamily: the admin-key and deluge/discover narrowings found by the
// registered-path sweep (B10d-h2). They are not ruled; B10y owns them, and
// TestRegisteredReach_B10yCellsMatchHEAD (held red until B10y lands) holds each
// cell to HEAD's verdict. The main oracle leaves them out of its exact diff.
var registeredB10yFamily = diffTable{
	name: "B10y family: admin keys and deluge/discover",
	want: 50,
	rows: []diffRow{
		{
			routes: []string{
				"GET /api/v1/admin/debug/book-files/:id", "GET /api/v1/admin/debug/books/:id", "GET /api/v1/admin/debug/edits",
				"GET /api/v1/admin/debug/lookup", "POST /api/v1/admin/recompact-digests",
			},
			who: adminKeys, r0: 'A', flip: 'D', status: 403,
			why: "admin-gated maintenance and debug reads: an admin's every-atom or empty-list key was admitted by HEAD's role check",
		},
		{
			routes: []string{"POST /api/v1/cache/invalidate", "GET /api/v1/cache/stats/keys", "POST /api/v1/maintenance/wipe"},
			who:    adminKeys, r0: 'A', flip: 'D', status: 403,
			why: "admin-gated cache and wipe routes: same shape",
		},
		{
			routes: []string{
				"GET /api/v1/master-lists", "POST /api/v1/master-lists", "DELETE /api/v1/master-lists/:id", "GET /api/v1/master-lists/elsewhere",
			},
			who: adminKeys, r0: 'A', flip: 'D', status: 403,
			why: "master lists: same shape",
		},
		{
			routes: []string{"GET /api/v1/users/:id/footprint", "GET /api/v1/users/merges"},
			who:    adminKeys, r0: 'A', flip: 'D', status: 403,
			why: "user footprint and merge list: same shape",
		},
		{
			routes: []string{"GET /api/v1/users/invites", "DELETE /api/v1/users/invites/:id"},
			who:    []string{"key:admin:users", "key:admin:every", "key:admin:empty", "key:custom-all:every", "key:custom-all:empty"}, r0: 'A', flip: 'D', status: 403,
			why: "invites: HEAD let a key act on them; the flip's credential guard refuses every key",
		},
		{
			routes: []string{"GET /api/v1/deluge/discover", "POST /api/v1/deluge/discover/import"},
			who: []string{
				"session:admin", "session:custom-all", "key:admin:every", "key:admin:empty", "key:custom-all:every", "key:custom-all:empty",
			},
			r0: 'A', flip: 'D', status: 403,
			why: "deluge discover: refused even for an admin session on the flip (integrations.manage)",
		},
	},
}

// registeredUnregistered are R0 routes no registered pattern matches, with what
// the flip answers every principal. Pinned so a change either way shows.
var registeredUnregistered = []struct {
	route  string
	status int
	why    string
}{
	{"GET /", http.StatusOK, "the SPA index: served by the static handler, public, so no route carries a requirement"},
	{"POST /api/v1/system/reset", http.StatusNotFound, "623da76d3 deleted the gin reset routes: the reset runs only offline (wire_system_routes.go)"},
	{"POST /api/v1/system/factory-reset", http.StatusNotFound, "623da76d3, as above; core's POST /api/core/v1/system/factory-reset is the successor"},
}

// registeredCanaryNarrowing: the principals whose canary (GET /auth/me) answers
// differently on the flip. The known difference of the 11-22 ruling.
var registeredCanaryNarrowing = map[string][2]int{
	"key:fp-worker:view":     {200, 403},
	"key:fp-worker:settings": {200, 403},
	"key:fp-worker:users":    {200, 403},
}

// ---- counts ----

const (
	registeredR0Routes    = 572
	registeredProbed      = 505
	registeredDelegated   = 64
	registeredPrincipals  = 36
	registeredAuthOnCells = registeredProbed * registeredPrincipals
)

// authDisabled reads the booted service's posture the way the SPA does.
func (h *Registered) authDisabled() bool {
	h.t.Helper()
	res, err := h.probeOnce(&Principal{Name: Anonymous}, http.MethodGet, "/api/core/v1/auth/status", nil, nil)
	if err != nil || res.Status != http.StatusOK {
		return false
	}
	var doc struct {
		AuthDisabled bool `json:"auth_disabled"`
	}
	return json.Unmarshal(res.Body, &doc) == nil && doc.AuthDisabled
}

// TestRegisteredReach_Oracle sends R0's recording to the flipped build, route by
// route and principal by principal, and compares each answer with the letter R0
// recorded. One boot per mode.
func TestRegisteredReach_Oracle(t *testing.T) {
	r0 := loadR0Recording(t)
	if len(r0.Routes) != registeredR0Routes {
		t.Fatalf("R0 recorded %d routes, the oracle pins %d", len(r0.Routes), registeredR0Routes)
	}

	t.Run("auth-on", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		if len(h.Principals) != registeredPrincipals {
			t.Fatalf("the boot has %d principals, the oracle pins %d", len(h.Principals), registeredPrincipals)
		}
		pass := h.runOracle(r0)

		// Coverage: every R0 route is probed, delegated to ABS, or pinned unregistered.
		if len(pass.probed) != registeredProbed || len(pass.delegated) != registeredDelegated || len(pass.unregistered) != len(registeredUnregistered) {
			t.Errorf("R0's %d routes split into %d probed, %d delegated, %d unregistered; pinned %d, %d, %d",
				len(r0.Routes), len(pass.probed), len(pass.delegated), len(pass.unregistered), registeredProbed, registeredDelegated, len(registeredUnregistered))
		}
		for _, u := range registeredUnregistered {
			got, ok := pass.unregisteredAnswers[u.route]
			if !ok {
				t.Errorf("%s: pinned unregistered, but no principal was sent it (is it registered now?)", u.route)
				continue
			}
			for name, st := range got {
				if st != u.status {
					t.Errorf("%s as %s: the flip answers %d, pinned %d (%s)", u.route, name, st, u.status, u.why)
				}
			}
			if len(got) != registeredPrincipals {
				t.Errorf("%s: answered for %d principals, want %d", u.route, len(got), registeredPrincipals)
			}
		}
		sort.Strings(pass.unregistered)
		want := make([]string, 0, len(registeredUnregistered))
		for _, u := range registeredUnregistered {
			want = append(want, u.route)
		}
		sort.Strings(want)
		if !slices.Equal(pass.unregistered, want) {
			t.Errorf("unregistered R0 routes = %v, pinned %v", pass.unregistered, want)
		}

		// A judged route stage 13 does not front is a hole.
		if len(pass.notJudged) > 0 {
			t.Errorf("ErrNotJudged: %d routes need a stage 13 verdict and an anonymous request was not refused by it: %v", len(pass.notJudged), pass.notJudged)
		}
		if len(pass.noVerdict) > 0 {
			t.Errorf("%d probes got no verdict: %v", len(pass.noVerdict), pass.noVerdict)
		}
		if len(pass.canaryDrift) > 0 {
			t.Errorf("a credential changed over its pass, so its D cells prove nothing: %v", pass.canaryDrift)
		}
		for name, got := range pass.canary {
			wantR0, wantFlip := r0.Canary[name], r0.Canary[name]
			if k, ok := registeredCanaryNarrowing[name]; ok {
				wantR0, wantFlip = k[0], k[1]
			}
			if wantR0 != r0.Canary[name] {
				t.Errorf("%s: R0 recorded canary %d, the table says %d", name, r0.Canary[name], wantR0)
			}
			if got != wantFlip {
				t.Errorf("%s: canary answers %d, want %d", name, got, wantFlip)
			}
		}

		diffs, compared := compareToR0(r0, pass)
		if compared != registeredAuthOnCells {
			t.Errorf("compared %d cells, pinned %d (%d routes x %d principals)", compared, registeredAuthOnCells, registeredProbed, registeredPrincipals)
		}
		tables := []diffTable{registeredFPWorkerNarrowing, registeredHeadDrift, registeredRuledNarrowings}
		heldRed := registeredB10yFamily.expand()
		expect := map[string]diffRow{}
		for _, tb := range tables {
			cells := tb.expand()
			if len(cells) != tb.want {
				t.Errorf("table %q expands to %d cells, claims %d", tb.name, len(cells), tb.want)
			}
			for k, row := range cells {
				if _, dup := expect[k]; dup {
					t.Errorf("cell %s is in two tables", k)
				}
				expect[k] = row
			}
		}
		if len(heldRed) != registeredB10yFamily.want {
			t.Errorf("table %q expands to %d cells, claims %d", registeredB10yFamily.name, len(heldRed), registeredB10yFamily.want)
		}
		seen := map[string]bool{}
		for _, d := range diffs {
			if _, held := heldRed[d.cell()]; held {
				continue
			}
			row, ok := expect[d.cell()]
			switch {
			case !ok && d.r0 == 'D':
				t.Errorf("WIDENING: the flip admits where HEAD refused: %s", d)
			case !ok:
				t.Errorf("unlisted narrowing: %s", d)
			case row.r0 != d.r0 || row.flip != d.flip || row.status != d.status:
				t.Errorf("%s: the table says R0 %c, flip %c (%d)", d, row.r0, row.flip, row.status)
			default:
				seen[d.cell()] = true
			}
		}
		for k := range expect {
			if !seen[k] {
				t.Errorf("listed difference no longer differs: %s", k)
			}
		}
		t.Logf("auth-on: %d routes probed, %d delegated, %d unregistered, %d cells compared, %d differing (%d listed, %d held for B10y)",
			len(pass.probed), len(pass.delegated), len(pass.unregistered), compared, len(diffs), len(expect), len(heldRed))
		var five []string
		for k, n := range pass.serverErrors {
			five = append(five, fmt.Sprintf("%s x%d", k, n))
		}
		sort.Strings(five)
		t.Logf("routes with a 5xx behind an admit (%d): %v", len(five), five)
	})

	t.Run("auth-off", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		if !h.authDisabled() {
			t.Skip("the harness's AuthOff boot is not auth-disabled yet (GET /api/core/v1/auth/status says auth_disabled=false): needs the B10d-h2 re-freeze")
		}
		pass := h.runOracle(r0)
		if len(pass.noVerdict) > 0 || len(pass.canaryDrift) > 0 {
			t.Fatalf("no verdict: %v; canary drift: %v", pass.noVerdict, pass.canaryDrift)
		}
		diffs, compared := compareToR0(r0, pass)
		if compared != registeredProbed {
			t.Errorf("compared %d auth-off cells, pinned %d", compared, registeredProbed)
		}
		for _, d := range diffs {
			t.Logf("auth-off difference: %s", d)
		}
	})
}
