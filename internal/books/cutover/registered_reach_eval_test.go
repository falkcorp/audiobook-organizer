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
	"io"
	"maps"
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

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/storage"
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
// with no way to list it, with one exception: a retired stub that answers 410 Gone
// to every caller (registeredRetiredRotate). Its letter A is the stub's 410, not
// reach: nothing behind it runs. checkDiffs refuses a table row that lists any
// other R0-D, flip-A cell. A cell where the flip refuses and R0 admitted is a
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
	Status    []int  `json:"status"`
}

func (r r0Route) key() string { return r.Method + " " + r.Path }

type r0Recording struct {
	Principals []string       `json:"principals"`
	Routes     []r0Route      `json:"routes"`
	Canary     map[string]int `json:"canary"`
	Tasks      []r0Task       `json:"tasks"`
	Approvals  []r0Approval   `json:"approvals"`
}

// r0Task is R0's recording of one task definition's start (POST
// /api/v3/ops/defs/:id/runs with an empty parameter list) as every principal but the
// auth-disabled one.
type r0Task struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Status  []int  `json:"status"`
}

// r0Approval is R0's recording of approving one run awaiting a decision. The
// letter N is a probe that never reached the approver check and carries no reach
// information.
type r0Approval struct {
	RunID   string         `json:"run_id"`
	Run     map[string]any `json:"run"`
	Verdict string         `json:"verdict"`
	Status  []int          `json:"status"`
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
	for _, tk := range r.Tasks {
		if len(tk.Verdict) != len(r.Principals)-1 {
			t.Fatalf("task %s: %d letters, want one per principal but the auth-disabled one (%d)", tk.ID, len(tk.Verdict), len(r.Principals)-1)
		}
	}
	if len(r.Principals) == 0 || r.Principals[len(r.Principals)-1] != Authoff {
		t.Fatalf("the recording's last principal is not %q: the task letters would pair with the wrong principals", Authoff)
	}
	return r
}

// ---- the recorder's requests ----

const (
	invitePath = "/api/v1/auth/invite/:token"
	acceptPath = "/api/v1/auth/accept-invite"
	logoutPath = "/api/v1/auth/logout"
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
	res := resultOf(w.ResponseRecorder, ri)
	h.notRateLimited(res, method, path, p.Name)
	return res, nil
}

// coreLogoutRoute is core's logout, which ends the caller's own session.
const coreLogoutRoute = "/api/core/v1/auth/logout"

// coreLogout is the session killer after HEAD's logout route was probed as p, a
// session principal. HEAD's route answers 410 under core identity and changes no
// state, so p's session must still answer its canary as before; core's logout
// then ends it, and the canary must answer 401 (a principal core's logout
// refuses is left as it is). Either way it fails is recorded in
// pass.logoutLeftSession. The caller signs p in again.
func (h *Registered) coreLogout(p *Principal, canaryBefore int, pass *oraclePass) {
	h.t.Helper()
	if got := h.canaryFor(p); got != canaryBefore {
		pass.logoutLeftSession = append(pass.logoutLeftSession,
			fmt.Sprintf("%s: HEAD's logout route changed the session: canary %d before, %d after", p.Name, canaryBefore, got))
	}
	res, err := h.probeOnce(p, http.MethodPost, coreLogoutRoute, nil, nil)
	if err == nil && (res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden) {
		// Core's logout refuses this principal (the fp-worker sessions hold
		// only the worker grants), so nothing ends its session here; the
		// caller signs it in again either way.
		return
	}
	if err != nil || res.Status >= 300 {
		pass.logoutLeftSession = append(pass.logoutLeftSession, fmt.Sprintf("%s: core's logout answered %d (%v)", p.Name, res.Status, err))
		return
	}
	if got := h.canaryFor(p); got != http.StatusUnauthorized {
		pass.logoutLeftSession = append(pass.logoutLeftSession,
			fmt.Sprintf("%s: core's logout left the session live: canary answers %d", p.Name, got))
	}
}

// ---- one principal's pass ----

// oracleCell is one answered probe.
type oracleCell struct {
	letter byte
	status int
	// res is the whole answer of a refusal (a D cell), so a hand-written table row
	// is checked with h.Refused on it; an admit keeps none.
	res Result
}

// cellOf is the cell a probe answered with letter.
func cellOf(letter byte, res Result) oracleCell {
	c := oracleCell{letter: letter, status: res.Status}
	if letter == 'D' {
		c.res = res
	}
	return c
}

// probeCell sends the recorder's request for route r as p.
func (h *Registered) probeCell(p *Principal, r r0Route, ri *httpapi.RouteInfo) (Result, error) {
	h.t.Helper()
	path, body := probeRequest(r.Method, r, p.Name)
	return h.probeOnce(p, r.Method, path, body, ri)
}

func (h *Registered) canaryFor(p *Principal) int {
	h.t.Helper()
	res, err := h.probeOnce(p, http.MethodGet, canaryRoute(p), nil, nil)
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
	// recorded is the verdicts.json name each principal's cells are held to: its own,
	// or its imported twin's for a core-native key.
	recorded map[string]string
	// canary is each principal's canary status before its pass.
	canary map[string]int
	// canaryDrift lists principals whose canary changed over their pass.
	canaryDrift []string
	// logoutLeftSession lists session principals whose session HEAD's logout
	// route ended (it answers 410 under core identity and must change nothing),
	// or whose session core's logout, sent right after it, did not end.
	logoutLeftSession []string
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
		recorded:            map[string]string{},
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
		if p.Name == Owner {
			continue // the owner boot's own cells are B10e1r's; the owner has no R0 recording
		}
		pass.recorded[p.Name] = p.Recorded()
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
			pass.cells[key][p.Name] = cellOf(res.Verdict(), res)
			if key == "POST "+logoutPath && p.cookie != nil {
				h.coreLogout(p, before, pass)
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

// ---- task starts and approvals ----

const (
	taskStartRoute   = "/api/v3/ops/defs/:id/runs"
	taskApproveRoute = "/api/v3/ops/runs/:id/approve"
	taskStartKey     = "TASK-START "
	taskApproveKey   = "TASK-APPROVE "
)

// taskRoutes are R0's recorded task starts and approvals as pseudo-routes of the
// recording, in the key form the comparison uses: one letter per recorded
// principal (the auth-disabled one is never asked).
func taskRoutes(r0 r0Recording) []r0Route {
	var out []r0Route
	for _, t := range r0.Tasks {
		out = append(out, r0Route{Method: "TASK-START", Path: t.ID, Verdict: t.Verdict, Status: t.Status})
	}
	for _, a := range r0.Approvals {
		def, _ := a.Run["def_id"].(string)
		out = append(out, r0Route{Method: "TASK-APPROVE", Path: def, Verdict: a.Verdict, Status: a.Status})
	}
	return out
}

// runTasks sends the recorder's task starts and approvals as every principal of an
// auth-on boot and adds each answer to pass.cells under its pseudo-route key.
// A task start that an authorized principal gets past the checks must fail
// validation: a status that starts a run is a finding (pass.noVerdict), because
// the cell proves reach and the probe may have started work.
func (h *Registered) runTasks(r0 r0Recording, pass *oraclePass) {
	h.t.Helper()
	start := h.match(http.MethodPost, probePath(taskStartRoute))
	approve := h.match(http.MethodPost, probePath(taskApproveRoute))
	if start == nil || approve == nil {
		h.t.Fatalf("the flip registers no pattern for %s (%v) or %s (%v)", taskStartRoute, start != nil, taskApproveRoute, approve != nil)
	}
	for _, p := range h.Principals {
		if p.Name == Owner || p.Name == Authoff {
			continue // the recorder never started a task as the auth-disabled principal
		}
		before := h.canaryFor(p)
		for _, t := range r0.Tasks {
			key := taskStartKey + t.ID
			res, err := h.probeOnce(p, http.MethodPost, "/api/v3/ops/defs/"+t.ID+"/runs", map[string]any{"params": []any{}}, start)
			h.recordTaskCell(pass, key, p, res, err, map[int]bool{400: true, 404: true, 409: true, 422: true})
		}
		for _, a := range r0.Approvals {
			def, _ := a.Run["def_id"].(string)
			res, err := h.probeOnce(p, http.MethodPost, "/api/v3/ops/runs/"+a.RunID+"/approve", map[string]any{"decision": "maybe"}, approve)
			h.recordTaskCell(pass, taskApproveKey+def, p, res, err, map[int]bool{400: true})
		}
		if after := h.canaryFor(p); after != before {
			pass.canaryDrift = append(pass.canaryDrift, fmt.Sprintf("%s: canary %d before its task pass, %d after", p.Name, before, after))
		}
	}
}

// recordTaskCell adds one task cell. A refusal is D; a status in reached is A; any
// other status is not a verdict (an approval that never reached the approver
// check is R0's N) and a task start that was not refused and did not fail
// validation is listed as a finding.
func (h *Registered) recordTaskCell(pass *oraclePass, key string, p *Principal, res Result, err error, reached map[int]bool) {
	h.t.Helper()
	if err != nil {
		pass.noVerdict = append(pass.noVerdict, fmt.Sprintf("%s as %s: %v", key, p.Name, err))
		return
	}
	if pass.cells[key] == nil {
		pass.cells[key] = map[string]oracleCell{}
	}
	switch {
	case res.Status == http.StatusUnauthorized || res.Status == http.StatusForbidden:
		pass.cells[key][p.Name] = cellOf('D', res)
	case reached[res.Status]:
		pass.cells[key][p.Name] = cellOf('A', res)
	case strings.HasPrefix(key, taskApproveKey):
		pass.cells[key][p.Name] = cellOf('N', res)
	default:
		pass.noVerdict = append(pass.noVerdict, fmt.Sprintf("%s as %s: %d (neither a refusal nor a validation failure: the probe may have started a run)", key, p.Name, res.Status))
	}
}

// ---- comparing with R0 ----

// oracleDiff is one cell where the flip and R0 disagree: in the letter, or in the class
// of the status (StatusClass) with the letter the same. r0Status is the status R0
// recorded, 0 where the recording holds none; r0Class is its class.
type oracleDiff struct {
	route, principal string
	r0, flip         byte
	r0Status         int
	r0Class          string
	status           int
	res              Result // the flip's refusal (a D cell)
}

func (d oracleDiff) cell() string { return d.route + " / " + d.principal }

func (d oracleDiff) String() string {
	if d.r0 == d.flip {
		return fmt.Sprintf("%s: letter %c both, but R0 answered %d (%s) and the flip %d (%s)", d.cell(), d.r0, d.r0Status, d.r0Class, d.status, StatusClass(d.status))
	}
	return fmt.Sprintf("%s: R0 %c (%d, %s), flip %c (%d, %s)", d.cell(), d.r0, d.r0Status, d.r0Class, d.flip, d.status, StatusClass(d.status))
}

// compareToR0 lists every differing cell and how many cells it compared. A cell
// differs when the letters differ, and also when they agree but the class of the
// status does not (StatusClass): a route that answered 200 at R0 and 410 on the flip
// is the letter A twice, and only the class shows it. A cell whose R0 status the
// recording lacks is compared by letter alone.
func compareToR0(r0 r0Recording, pass *oraclePass) (diffs []oracleDiff, compared int) {
	idx := map[string]int{}
	for i, n := range r0.Principals {
		idx[n] = i
	}
	for _, r := range append(slices.Clone(r0.Routes), taskRoutes(r0)...) {
		for n, c := range pass.cells[r.key()] {
			rec := pass.recorded[n]
			if idx[rec] >= len(r.Verdict) {
				continue // the recorder never asked this principal (the auth-disabled one)
			}
			if r.Verdict[idx[rec]] == 'N' || c.letter == 'N' {
				continue // no reach information
			}
			compared++
			want := r.Verdict[idx[rec]]
			var r0St int
			if idx[rec] < len(r.Status) {
				r0St = r.Status[idx[rec]]
			}
			if want != c.letter || (r0St > 0 && StatusClass(r0St) != StatusClass(c.status)) {
				diffs = append(diffs, oracleDiff{route: r.key(), principal: n, r0: want, flip: c.letter, r0Status: r0St, r0Class: StatusClass(r0St), status: c.status, res: c.res})
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
	// handler: the flip's refusal is the handler's, behind an admit by stage 13; a
	// cell of the row that stage 13 refused fails.
	handler bool
	// code, when set, is the flip's problem code, exactly (stage 13's); a row that
	// leaves it empty does not assert one.
	code string
	// r0Class, when set, is the StatusClass of R0's recorded status for every cell of
	// the row. A row whose letters agree (r0 == flip) lists a change of class alone and
	// must set it: it is what the row accepts, and the cell fails if R0's class is
	// anything else.
	r0Class string
}

// diffTable is a named set of rows with the exact number of cells it claims. A
// principal with a core-native twin (Principal.Twin) differs exactly as its
// imported twin does, and those cells are counted apart: want is the cells of the
// recorded principals, wantTwin the cells of their core-key twins.
type diffTable struct {
	name     string
	want     int
	wantTwin int
	rows     []diffRow
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

// twins are the cells of the core-native keys that mirror the cells of cells: the same
// route for the principal whose Twin is the recorded one, with the same row.
func (h *Registered) twins(cells map[string]diffRow) map[string]diffRow {
	byRecorded := map[string]string{}
	for _, p := range h.Principals {
		if p.Core {
			byRecorded[p.Twin] = p.Name
		}
	}
	out := map[string]diffRow{}
	for k, row := range cells {
		i := strings.LastIndex(k, " / ")
		if tw, ok := byRecorded[k[i+3:]]; ok {
			out[k[:i]+" / "+tw] = row
		}
	}
	return out
}

// handlerCells are the cells HEAD's handler, not its router, refuses: the 11 cells
// the removed evaluator's handler-admin pin hid (reachPinnedCells) and the eight
// auth-off cells of reachHandlerRefusalCells, split by the boot that has the
// principal. Each is a route stage 13 may admit and the handler refuses, so the
// oracle's comparison already holds the flip's letter to R0's D: this lists them so
// a vanished cell, or one the flip answers with anything but a 401 or 403, fails by
// name, and counts the ones stage 13 itself refuses.
func handlerCells(authOff bool) []string {
	var out []string
	for _, c := range cutover.ReachPinnedCells() {
		i := strings.LastIndex(c, " / ")
		j := strings.Index(c[i:], ": R0")
		who := c[i+3 : i+j]
		if (who == Authoff) == authOff {
			out = append(out, c[:i]+" / "+who)
		}
	}
	if authOff {
		for _, route := range cutover.ReachHandlerRefusalCells() {
			out = append(out, route+" / "+Authoff)
		}
	}
	return out
}

// checkHandlerCells holds handlerCells (and their core-key twins) to R0's D.
func (h *Registered) checkHandlerCells(t *testing.T, pass *oraclePass, authOff bool) (cells, byStage13 int) {
	t.Helper()
	listed := map[string]diffRow{}
	for _, k := range handlerCells(authOff) {
		listed[k] = diffRow{}
	}
	maps.Copy(listed, h.twins(listed))
	for k := range listed {
		i := strings.LastIndex(k, " / ")
		route, who := k[:i], k[i+3:]
		c, ok := pass.cells[route][who]
		switch {
		case !ok:
			t.Errorf("handler cell %s was not sent", k)
		case c.letter != 'D':
			t.Errorf("WIDENING: the flip admits where HEAD's handler refused: %s answered %d", k, c.status)
		default:
			cells++
			if c.res.Stage13 == Stage13Refused {
				byStage13++
			}
		}
	}
	return cells, byStage13
}

// checkR2OnlyLetters holds the routes R0 never recorded to the letters
// reachWantR2OnlyVerdicts pins, for the principals this boot has. The letter is
// stage 13's own: admitted or not, whatever the handler then answers.
func (h *Registered) checkR2OnlyLetters(t *testing.T, r0 r0Recording, authOff bool) {
	t.Helper()
	for route, want := range cutover.ReachWantR2OnlyVerdicts() {
		if len(want) != len(r0.Principals) {
			t.Errorf("%s: %d letters pinned for %d recorded principals", route, len(want), len(r0.Principals))
			continue
		}
		method, path, _ := strings.Cut(route, " ")
		ri := h.match(method, probePath(path))
		if ri == nil {
			t.Errorf("%s is pinned as an R2-only route and the flip registers no pattern for it", route)
			continue
		}
		for i, name := range r0.Principals {
			if (name == Authoff) != authOff {
				continue
			}
			res, err := h.probeCell(h.Principal(name), r0Route{Method: method, Path: path}, ri)
			if err != nil {
				t.Errorf("%s as %s: %v", route, name, err)
				continue
			}
			got := byte('D')
			if res.Admitted() {
				got = 'A'
			}
			if got != want[i] {
				t.Errorf("R2-only route %s as %s: stage 13 gives %c, pinned %c (%d)", route, name, got, want[i], res.Status)
			}
		}
	}
}

// checkDiffs holds every differing cell to the tables: a cell no table lists is an
// unlisted narrowing (or a WIDENING when R0 refused it), a listed cell that no
// longer differs fails, and so does a table whose expansion is not the count it
// claims.
func (h *Registered) checkDiffs(t *testing.T, diffs []oracleDiff, tables []diffTable) {
	t.Helper()
	for _, problem := range h.diffProblems(diffs, tables, h.Refused) {
		t.Error(problem)
	}
}

// diffProblems is checkDiffs's judgement as data: one line per problem. refused is
// h.Refused for a real boot; a synthetic set (the class cells of
// reach_status_class_test.go) passes a stub, since it has no answer to read.
func (h *Registered) diffProblems(diffs []oracleDiff, tables []diffTable, refused func(Result, int, string) bool) []string {
	var out []string
	problemf := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	expect := map[string]diffRow{}
	for _, tb := range tables {
		cells := tb.expand()
		twin := h.twins(cells)
		if len(cells) != tb.want || len(twin) != tb.wantTwin {
			problemf("table %q expands to %d cells and %d core-key twin cells, claims %d and %d", tb.name, len(cells), len(twin), tb.want, tb.wantTwin)
		}
		for _, row := range tb.rows {
			// A widening cannot be listed. The one R0-D, flip-A row a table may
			// hold is a retired stub's 410 Gone, which reaches nothing.
			if row.r0 == 'D' && row.flip == 'A' && row.status != http.StatusGone {
				problemf("table %q lists a widening (R0 D, flip A with %d) for %v: only a retired stub's 410 may be listed", tb.name, row.status, row.routes)
			}
			// A row whose letters agree accepts a change of class and nothing else:
			// it names the class R0 answered, and the class the flip answers differs.
			if row.r0 == row.flip && (row.r0Class == "" || row.r0Class == StatusClass(row.status)) {
				problemf("table %q lists a same-letter row for %v that names no change of class (R0 class %q, flip status %d)", tb.name, row.routes, row.r0Class, row.status)
			}
		}
		for _, m := range []map[string]diffRow{cells, twin} {
			for k, row := range m {
				if _, dup := expect[k]; dup {
					problemf("cell %s is in two tables", k)
				}
				expect[k] = row
			}
		}
	}
	seen := map[string]bool{}
	for _, d := range diffs {
		row, ok := expect[d.cell()]
		switch {
		case !ok && d.r0 == 'D':
			problemf("WIDENING: the flip admits where HEAD refused: %s", d)
		case !ok && d.r0 == d.flip:
			problemf("unlisted change of status class: %s", d)
		case !ok:
			problemf("unlisted narrowing: %s", d)
		case row.r0 != d.r0 || row.flip != d.flip:
			problemf("%s: the table says R0 %c, flip %c (%d)", d, row.r0, row.flip, row.status)
		case row.r0Class != "" && row.r0Class != d.r0Class:
			problemf("%s: the table says R0 answered class %s", d, row.r0Class)
		case row.flip == 'A' && row.status != d.status:
			problemf("%s: the table says the flip admits with %d", d, row.status)
		case row.flip == 'D' && !refused(d.res, row.status, row.code):
			// Refused reported the status or code that differs.
		case row.handler && d.res.Stage13 == Stage13Refused:
			problemf("%s: the table says the handler refuses behind an admit, stage 13 refused", d)
		default:
			seen[d.cell()] = true
		}
	}
	for k := range expect {
		if !seen[k] {
			problemf("listed difference no longer differs: %s", k)
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
	want: 96, // the fp-worker keys have no core-native twin (h.NoCoreTwin)
	rows: []diffRow{
		{
			routes: []string{
				"GET /api/v1/auth/me", "GET /api/v1/auth/sessions", "DELETE /api/v1/auth/sessions/:id",
				"GET /api/v1/auth/api-keys", "GET /api/v1/auth/api-keys/:id", "POST /api/v1/auth/api-keys",
				"DELETE /api/v1/auth/api-keys/:id", "POST /api/v1/auth/logout",
			},
			who: fpWorkerKeys, r0: 'A', flip: 'D', status: 403, code: "permission_denied",
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
			who: fpWorkerKeys, r0: 'A', flip: 'D', status: 403, code: "permission_denied",
			why: "per-user reading-state routes: Authenticated-only at HEAD, so a worker's key passed; the key's checker is empty after the cutover",
		},
		{
			routes: []string{
				"POST /api/v1/playlists", "GET /api/v1/playlists/:id", "PUT /api/v1/playlists/:id", "DELETE /api/v1/playlists/:id",
				"POST /api/v1/playlists/:id/books", "DELETE /api/v1/playlists/:id/books/:bookID",
				"POST /api/v1/playlists/:id/reorder", "POST /api/v1/playlists/:id/materialize", "GET /api/v1/playlists/:id/export.m3u",
			},
			who: fpWorkerKeys, r0: 'A', flip: 'D', status: 403, code: "permission_denied",
			why: "playlist routes: Authenticated-only at HEAD",
		},
		{
			routes: []string{"GET /api/events", "GET /metrics"},
			who:    fpWorkerKeys, r0: 'A', flip: 'D', status: 403, code: "permission_denied",
			why: "the event stream and the metrics scrape: Authenticated-only at HEAD",
		},
	},
}

// registeredRuledNarrowings: the 6 cells F4-07 pinned (reachHandlerAdmin's
// narrowing rows), reproduced on the flip. F4-07's restore row is gone: B10y's
// ruling (ii) restores HEAD's owner gate, so a custom role holding settings.manage
// is admitted again while no owner is set.
var registeredRuledNarrowings = diffTable{
	name:     "narrowings ruled in F4-07 (05-rbac 6.2)",
	want:     6,
	wantTwin: 6,
	rows: []diffRow{
		{
			routes: []string{
				"PATCH /api/v1/admin/debug/books/:id", "PATCH /api/v1/admin/debug/book-files/:id", "POST /api/v1/admin/debug/edits/:edit_id/undo",
			},
			who: adminKeys, r0: 'A', flip: 'D', status: 403, code: "interactive_required",
			why: "05-rbac 6.2 / 4.4 rule 1: books:admin.debug is Sensitive, so a non-interactive principal is refused even with core:admin",
		},
	},
}

// registeredBootstrapRefused: the books bootstrap route under identity (B10bs). R0
// recorded 400 for every principal, the probe's empty body read first; the flip's
// handler refuses every caller with 401 invalid bootstrap token before it reads the
// body. Accepted by the coordinator's bootstrap ruling (2026-11-22 06:29, option 2):
// the token is redeemed at core's /api/core/v1/auth/bootstrap, and the cutover
// deletes HEAD's token rows. The auth-off boot's cell is its own table, because each
// boot's tables must name only cells that boot sends.
var (
	bootstrapRoute = []string{"POST /api/v1/auth/bootstrap"}
	bootstrapWhy   = "B10bs, coordinator ruling 11-22 06:29: the books route refuses before it reads a token; core's route redeems it"

	registeredBootstrapRefused = diffTable{
		name:     "the books bootstrap route refuses every caller under identity (B10bs)",
		want:     36,
		wantTwin: 18,
		rows: []diffRow{{
			routes: bootstrapRoute,
			who:    []string{"session:admin", "session:editor", "session:viewer", "session:fp-worker", "session:custom-all", "session:custom-subset", "session:custom-unknown", "key:admin:view", "key:admin:settings", "key:admin:users", "key:admin:worker", "key:admin:every", "key:admin:empty", "key:editor:view", "key:editor:settings", "key:editor:users", "key:editor:worker", "key:editor:every", "key:editor:empty", "key:viewer:view", "key:viewer:settings", "key:viewer:users", "key:viewer:worker", "key:viewer:every", "key:viewer:empty", "key:fp-worker:view", "key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:worker", "key:fp-worker:every", "key:fp-worker:empty", "key:custom-all:every", "key:custom-all:empty", "key:custom-subset:outside", "key:custom-subset:empty", "anonymous"},
			r0:     'A', flip: 'D', status: 401, code: "UNAUTHORIZED", handler: true,
			why: bootstrapWhy,
		}},
	}
	registeredBootstrapRefusedAuthOff = diffTable{
		name: "the books bootstrap route refuses the auth-disabled principal under identity (B10bs)",
		want: 1,
		rows: []diffRow{{
			routes: bootstrapRoute,
			who:    []string{Authoff}, r0: 'A', flip: 'D', status: 401, code: "UNAUTHORIZED", handler: true,
			why: bootstrapWhy,
		}},
	}
)

// registeredAuthOffTightening: the 10 cells of the auth-disabled principal that HEAD
// admitted and the flip refuses on purpose. Ruled by the coordinator (2026-11-22): these
// are RECORDED TIGHTENINGS, not defects. 05-rbac.md section 4.11, "Auth-off tightenings
// (SEC2-21, SEC5-8)" (a): every Guards.Interactive route refuses the auth-disabled
// principal, which is non-interactive, where HEAD's credential guard is a pass-through
// with auth off (internal/server/credential_routes.go:79-82); 05-authn.md section 4.11
// (SEC5-8, "a deliberate auth-off tightening"). R2's release note, 09-abo-on-core.md
// section 9.2.6 "Recorded changes (release notes)" and the row 26 of the changed-surfaces
// list, says: "With authentication off, credential, API-key, session, audit and backup
// routes now refuse requests; turn authentication on to use them." That sentence names
// credential, key, session, audit and backup routes only; the user-management, import-path,
// tool-install, update-apply and plugin-settings routes below are Interactive by the
// manifest and covered by (a)'s "every Guards.Interactive route", but not by the note's words.
//
// Two cells left the table at the flip (F4-08 r13): POST /api/v1/auth/temp-tokens is
// deleted (registeredDeletedAtFlip), so no boot judges it; and POST
// /api/v1/auth/api-keys/:id/rotate answers the auth-disabled principal 410 as HEAD
// did, now that the credential guard in front of the retired stub is gone.
var registeredAuthOffTightening = diffTable{
	name: "auth-off tightening: Guards.Interactive refuses the auth-disabled principal (05-rbac 4.11(a), 05-authn 4.11, SEC5-8)",
	want: 10,
	rows: []diffRow{
		{
			routes: []string{"POST /api/v1/backup/restore"},
			who:    []string{Authoff}, r0: 'A', flip: 'D', status: 403, code: "interactive_required",
			why: "05-rbac 4.11(a), 05-authn 4.11 (SEC5-8): backup restore is Interactive; the release note names it (backup routes)",
		},
		{
			routes: []string{
				"POST /api/v1/import-paths", "POST /api/v1/tools/:name/install", "POST /api/v1/update/apply", "PUT /api/v1/plugins/:id/settings",
				"POST /api/v1/users/invite", "POST /api/v1/users/invites/:id/resend",
				"POST /api/v1/users/:id/deactivate", "POST /api/v1/users/:id/reactivate", "POST /api/v1/users/:id/reset-password",
			},
			who: []string{Authoff}, r0: 'A', flip: 'D', status: 403, code: "interactive_required",
			why: "05-rbac 4.11(a), 05-authn 4.11 (SEC5-8): Interactive by the manifest; the release note's wording does not name this route family",
		},
	},
}

// registeredRetiredRotate: POST /api/v1/auth/api-keys/:id/rotate, the stub HEAD
// retired (01-P81k), answers 410 Gone to every caller once the gin credential guard
// in front of it is gone (F4-08 B10g; approved reach contract, owner and coordinator
// rulings 2026-11-22). R0 recorded D for these keys: HEAD's guard refused an API key
// with 403 before the stub. The flip's letter A is the stub's 410, and nothing is
// minted. The sessions and the auth-disabled principal got the 410 at R0 too; the
// fp-worker principals, key:admin:worker and the anonymous caller are refused on the
// flip as at R0, so they are not differences.
// 21 recorded keys and their 14 core-native twins: the 35 cells of the contract.
var registeredRetiredRotate = diffTable{
	name:     "retired stub: 410 to every caller once the credential guard is gone",
	want:     21,
	wantTwin: 14,
	rows: []diffRow{{
		routes: []string{"POST /api/v1/auth/api-keys/:id/rotate"},
		who: []string{
			"key:admin:view", "key:admin:settings", "key:admin:users", "key:admin:every", "key:admin:empty",
			"key:editor:view", "key:editor:settings", "key:editor:users", "key:editor:worker", "key:editor:every", "key:editor:empty",
			"key:viewer:view", "key:viewer:settings", "key:viewer:users", "key:viewer:worker", "key:viewer:every", "key:viewer:empty",
			"key:custom-all:every", "key:custom-all:empty", "key:custom-subset:outside", "key:custom-subset:empty",
		},
		r0: 'D', flip: 'A', status: http.StatusGone,
		why: "retired stub: 410 to every caller once the credential guard is gone",
	}},
}

// recordedPrincipals are the principals verdicts.json records on the auth-on boot, in
// its order: everyone but the auth-disabled one (TestRecordedPrincipalsAreTheRecording
// holds the list to the file). The tables that name "every principal" are built from it.
var recordedPrincipals = []string{
	"session:admin", "session:editor", "session:viewer", "session:fp-worker", "session:custom-all", "session:custom-subset", "session:custom-unknown",
	"key:admin:view", "key:admin:settings", "key:admin:users", "key:admin:worker", "key:admin:every", "key:admin:empty",
	"key:editor:view", "key:editor:settings", "key:editor:users", "key:editor:worker", "key:editor:every", "key:editor:empty",
	"key:viewer:view", "key:viewer:settings", "key:viewer:users", "key:viewer:worker", "key:viewer:every", "key:viewer:empty",
	"key:fp-worker:view", "key:fp-worker:settings", "key:fp-worker:users", "key:fp-worker:worker", "key:fp-worker:every", "key:fp-worker:empty",
	"key:custom-all:every", "key:custom-all:empty", "key:custom-subset:outside", "key:custom-subset:empty", Anonymous,
}

// except is names without the ones in drop.
func except(names []string, drop ...string) []string {
	return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return slices.Contains(drop, n) })
}

// goneUnderIdentityWhy is why each route below answers 410 on the flip.
const goneUnderIdentityWhy = "core owns the account under identity, so HEAD's route refuses with 410 Gone and names core's replacement (internal/server/wire_auth_routes.go: gone()); ruled by the coordinator 2026-11-28 as the designed refusal of a retired route, no access change"

// logoutAnswers200: the principals that R0 answered POST /auth/logout 200 and the flip
// answers 410. Left out: the five that stage 13 refuses at R0 and on the flip (the
// fp-worker session, the fp-worker keys that hold the worker atom alone, and
// key:admin:worker), the anonymous caller (401 on both), and the three fp-worker keys
// that R0 admitted and the flip refuses (registeredFPWorkerNarrowing: a letter change,
// not a class one).
var logoutAnswers200 = except(recordedPrincipals,
	"session:fp-worker", "key:admin:worker", "key:fp-worker:worker", "key:fp-worker:every", "key:fp-worker:empty",
	"key:fp-worker:view", "key:fp-worker:settings", "key:fp-worker:users", Anonymous)

// goneUnderIdentity builds the table of the four routes that answer 410 under core
// identity where R0 answered an ordinary status with the same letter A: every row is a
// change of class alone (R0 class, then 410), one per route and transition, so the
// letters stay A on both sides and the cells are named by class, not by letter. who is
// the principals the table claims on the boot it belongs to.
func goneUnderIdentity(name string, who func(route string) []string, want, wantTwin int) diffTable {
	row := func(route, r0Class string) diffRow {
		return diffRow{
			routes: []string{route}, who: who(route),
			r0: 'A', flip: 'A', status: http.StatusGone, r0Class: r0Class,
			why: goneUnderIdentityWhy,
		}
	}
	return diffTable{
		name: name, want: want, wantTwin: wantTwin,
		rows: []diffRow{
			row("GET /api/v1/auth/invite/:token", "404"),
			row("POST /api/v1/auth/accept-invite", "4xx"),
			row("POST /api/v1/auth/setup", "4xx"),
			row("POST /api/v1/auth/logout", "2xx"),
		},
	}
}

// registeredGoneUnderIdentity: auth-on. 36 principals on each of invite, accept-invite
// and setup, 27 on logout (logoutAnswers200); the core-native twins of 18 and 14 of
// them.
var registeredGoneUnderIdentity = goneUnderIdentity(
	"retired identity routes answer 410 under core identity (a change of class, letter A on both sides)",
	func(route string) []string {
		if route == "POST /api/v1/auth/logout" {
			return logoutAnswers200
		}
		return recordedPrincipals
	},
	3*36+27, 3*18+14,
)

// updateApplyStateWhy names the one cell the hermetic updater (hermeticOutbound) moved:
// R0 answered session:admin's POST /update/apply 500 where every other principal got
// 400, an answer that depends on the updater's state and on the live GitHub's reply,
// which scripts/r2corpus/record_r0_reach.py:209-210 records as "answered 400 and then
// 500 for one principal". Against the stubbed release lookup the cell answers 400, as
// the other principals' cells do. Neither is a success, so the set of principals the
// route lets through is the same.
const updateApplyStateWhy = "hermetic updater: R0's 500 for this one cell was the updater's state against the live GitHub (record_r0_reach.py:209-210); the stubbed release gives the 400 every other principal got; no principal gains a 2xx"

// registeredUpdateApplyState: auth-on, one cell, no twin.
var registeredUpdateApplyState = diffTable{
	name:     "update/apply: R0's one 5xx cell is the 400 of its siblings (a change of class, letter A on both sides)",
	want:     1,
	wantTwin: 0,
	rows: []diffRow{{
		routes: []string{"POST /api/v1/update/apply"}, who: []string{"session:admin"},
		r0: 'A', flip: 'A', status: http.StatusBadRequest, r0Class: "5xx",
		why: updateApplyStateWhy,
	}},
}

// registeredGoneUnderIdentityAuthOff: the same four routes for the auth-disabled
// principal, whose R0 answers were 404, 400, 400 and 200.
var registeredGoneUnderIdentityAuthOff = goneUnderIdentity(
	"retired identity routes answer 410 to the auth-disabled principal (a change of class, letter A on both sides)",
	func(string) []string { return []string{Authoff} },
	4, 0,
)

// registeredDeletedAtFlip are R0's routes the identity flip deletes (F4-08 spec step
// 3; internal/server routesDeletedAtTheFlip): no pattern matches them and they answer
// 404 to every principal. Core's POST /api/core/v1/users/{id}/temp-login and POST
// /api/core/v1/auth/temp-login/consume replace them. No boot judges them, so the
// verdict sweep does not send them (TestRegisteredHarness_DeletedRoutesAreNotRegistered
// holds them to 404 instead) and the oracle pins them in registeredUnregistered.
var registeredDeletedAtFlip = []unregisteredRoute{
	{"GET /auth/temp-login", http.StatusNotFound, deletedAtFlipWhy},
	{"POST /api/v1/auth/temp-tokens", http.StatusNotFound, deletedAtFlipWhy},
}

const deletedAtFlipWhy = "route deleted at the flip, replaced by core's native route (spec step 3)"

// unregisteredRoute is an R0 route no registered pattern matches, with the status the
// flip answers every principal and why.
type unregisteredRoute struct {
	route  string
	status int
	why    string
}

// registeredUnregistered are R0 routes no registered pattern matches, with what
// the flip answers every principal. Pinned so a change either way shows.
var registeredUnregistered = append([]unregisteredRoute{
	{"GET /", http.StatusOK, "the SPA index: served by the static handler, public, so no route carries a requirement"},
}, registeredDeletedAtFlip...)

// registeredCanaryNarrowing: the principals whose canary (GET /auth/me) answers
// differently on the flip. The known difference of the 11-22 ruling.
var registeredCanaryNarrowing = map[string][2]int{
	"key:fp-worker:view":     {200, 403},
	"key:fp-worker:settings": {200, 403},
	"key:fp-worker:users":    {200, 403},
}

// ---- counts ----

const (
	registeredR0Routes     = 574
	registeredProbed       = 503 // 505 until the flip deleted GET /auth/temp-login and POST /api/v1/auth/temp-tokens (registeredDeletedAtFlip)
	registeredDelegated    = 68
	registeredPrincipals   = 36 // the recorded abk_ keys, sessions and the anonymous caller
	registeredCoreKeys     = 18 // core-native mck_ twins, held to their imported twin's recording
	registeredNoCoreTwin   = 10
	registeredAuthOnCells  = registeredProbed * registeredPrincipals
	registeredAuthOffCells = registeredProbed // one principal over the probed routes
)

// The recorded task starts and approvals, as pseudo-routes.
const (
	registeredTasks     = 236
	registeredApprovals = 1
)

// noFingerprintBackend puts the boot where R0's recording was made: with no fpcalc and
// no ffmpeg on PATH. POST /dedup/fingerprint-rescan answers 503 "no fingerprint backend"
// after validating its body (internal/server/fingerprint_rescan.go) and before it
// enqueues anything, so R0's runner recorded 503; on a host that has either tool the
// same handler answers 202 and starts a real operation, so the cell would compare the
// host's tools, not reach, and the oracle would pass or fail by where it runs.
func noFingerprintBackend(t testing.TB) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	// The resolved path is a package global the boot itself also sets (server.go,
	// from the tool registry): put back what this test found, not "".
	prev := fingerprint.ResolvedFpcalcPath()
	t.Cleanup(func() { fingerprint.SetResolvedFpcalcPath(prev) })
	fingerprint.SetResolvedFpcalcPath("")
	if fingerprint.Available() {
		t.Fatal("a fingerprint backend is still found with an empty PATH")
	}
}

// stubGitHubHost and stubRelease are the one outbound endpoint the oracle's boots serve:
// the releases API the updater asks (POST /update/check and /update/apply). R0's
// recording asked the live GitHub, so its update/check cells answered 200 and its
// update/apply cell 500 ("no release asset to apply") or 400, by the run's luck; the
// flip asking it too made the oracle depend on the network and, with 500 or so
// admitted probes a run, on GitHub's unauthenticated rate limit (60 an hour).
const (
	stubGitHubHost    = "api.github.com"
	stubReleasePath   = "/repos/falkcorp/audiobook-organizer/releases/latest"
	stubReleaseAnswer = `{"tag_name":"v99.0.0","html_url":"https://example.test/releases/v99.0.0","body":"Gotham release notes","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z","assets":[]}`
)

// serveStubbedRelease answers the one release lookup the harness serves and reports
// whether r was it. Both of the harness's local servers (the outbound one below and
// the Access app, which owner boots install as the default transport instead) call
// it, so the updater sees the same release in every boot.
func serveStubbedRelease(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.Host != stubGitHubHost || r.URL.Path != stubReleasePath {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, stubReleaseAnswer)
	return true
}

// backgroundHosts are the hosts a boot reaches for from background work, never from a
// request's answer: operations an admitted probe enqueues (an AI scan's embeddings
// and file uploads, the Open Library dump fetch) and the jobs a boot starts. The
// harness refuses them with a 404 and nothing leaves the machine; no probe's status
// waits for one, which the oracle's own cell comparison would show (a probe that
// answered differently with the lookup refused would be a diff). Any other host is a
// failure of the test.
var backgroundHosts = map[string]string{
	"api.openai.com":  "AI scan and embedding operations",
	"openlibrary.org": "the Open Library dump fetch",
	"archive.org":     "the Open Library dump's mirror",
}

// outboundApp is the harness's stand-in for the network outside it: a local TLS server
// that serves the stubbed release lookup and refuses every other request, recording it.
// A host that is neither the release lookup nor a named background host fails the test
// that installed it, at its end: a real network call from a boot is never a silent pass
// or a flake.
type outboundApp struct {
	srv *httptest.Server
	mu  sync.Mutex
	// refused is every request the server refused, as "METHOD host/path".
	refused []string
	// strayHosts are the hosts of refused that backgroundHosts does not name.
	strayHosts []string
}

func (o *outboundApp) serve(w http.ResponseWriter, r *http.Request) {
	if serveStubbedRelease(w, r) {
		return
	}
	host := strings.ToLower(r.Host)
	o.mu.Lock()
	o.refused = append(o.refused, r.Method+" "+host+r.URL.Path)
	if _, ok := backgroundHosts[host]; !ok {
		o.strayHosts = append(o.strayHosts, host)
	}
	o.mu.Unlock()
	http.Error(w, "the harness serves no such host", http.StatusNotFound)
}

// hermeticOutbound installs an outboundApp as http.DefaultTransport for the test and
// restores it at its end. The updater's client has no transport of its own, so it goes
// through the default one, as core's own clients do; dialTo keeps it an
// *http.Transport, which those clients clone. The Access transport swap of the owner
// boots is the precedent, and nests inside this one.
func hermeticOutbound(t testing.TB) *outboundApp {
	t.Helper()
	o := &outboundApp{}
	o.srv = httptest.NewTLSServer(http.HandlerFunc(o.serve))
	orig := http.DefaultTransport
	http.DefaultTransport = dialTo(o.srv)
	t.Cleanup(func() {
		http.DefaultTransport = orig
		o.srv.Close()
		o.mu.Lock()
		defer o.mu.Unlock()
		if len(o.strayHosts) > 0 {
			t.Errorf("the boot reached for %d outbound requests to hosts the harness neither serves nor names as background work: %v", len(o.strayHosts), slices.Compact(slices.Sorted(slices.Values(o.strayHosts))))
		}
		t.Logf("outbound: %d requests to background hosts refused locally", len(o.refused)-len(o.strayHosts))
	})
	return o
}

// recordedEnvironment puts a boot where R0's recording was made, minus what made it
// flake: no fingerprint backend (noFingerprintBackend) and no network but the one
// stubbed release lookup (hermeticOutbound).
func recordedEnvironment(t testing.TB) {
	t.Helper()
	noFingerprintBackend(t)
	hermeticOutbound(t)
}

// seedRecordedFlags turns on, in the stored settings blob, the two settings R0 was
// recorded with (the row HEAD wrote: a database.Setting whose value is the blob's
// JSON; record_r0_reach.py's BOOT_FLAGS, both boots):
//
//   - deluge_discovery_enabled: GET /deluge/discover and its import answer 403
//     "deluge discovery is disabled" to every principal while it is off, so a corpus
//     with it off compares a setting, not reach.
//   - fingerprint_remote_workers_enabled: the fingerprint worker routes answer 404
//     ahead of the handler while it is off, so a corpus with it off answers every
//     principal stage 13 admits 404 where R0 answered 503 or 400 (its hub is not
//     running, or the empty body is refused), and compares a setting, not reach.
//     The boot's config.Config sets it, but the stored blob wins at boot.
func seedRecordedFlags(ctx context.Context, lr storage.LegacyRange) error {
	blob, err := lr.Get(ctx, []byte("setting:config_blob"))
	if err != nil {
		return fmt.Errorf("read the settings blob: %w", err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(blob, &row); err != nil {
		return fmt.Errorf("decode the settings row: %w", err)
	}
	var val string
	if err := json.Unmarshal(row["value"], &val); err != nil {
		return fmt.Errorf("decode the settings blob's value: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(val), &fields); err != nil {
		return fmt.Errorf("decode the settings blob: %w", err)
	}
	fields["deluge_discovery_enabled"] = json.RawMessage("true")
	fields["fingerprint_remote_workers_enabled"] = json.RawMessage("true")
	nv, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	if row["value"], err = json.Marshal(string(nv)); err != nil {
		return err
	}
	if blob, err = json.Marshal(row); err != nil {
		return err
	}
	return lr.Set(ctx, []byte("setting:config_blob"), blob)
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
		recordedEnvironment(t)
		h := bootRegistered(t, registeredConfig{Seed: seedRecordedFlags})
		var core int
		for _, p := range h.Principals {
			if p.Core {
				core++
			}
		}
		if len(h.Principals) != registeredPrincipals+registeredCoreKeys || core != registeredCoreKeys || len(h.NoCoreTwin) != registeredNoCoreTwin {
			t.Fatalf("the boot has %d principals (%d core keys, %d keys without a twin); the oracle pins %d recorded, %d core keys and %d without a twin",
				len(h.Principals), core, len(h.NoCoreTwin), registeredPrincipals, registeredCoreKeys, registeredNoCoreTwin)
		}
		if len(h.KnownDifferences) != len(registeredCanaryNarrowing) {
			t.Errorf("the boot applied %d canary known differences, the oracle pins %d", len(h.KnownDifferences), len(registeredCanaryNarrowing))
		}
		pass := h.runOracle(r0)
		h.runTasks(r0, pass)

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
			if len(got) != registeredPrincipals+registeredCoreKeys {
				t.Errorf("%s: answered for %d principals, want %d", u.route, len(got), registeredPrincipals+registeredCoreKeys)
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
		if len(pass.logoutLeftSession) > 0 {
			t.Errorf("logout: %v", pass.logoutLeftSession)
		}
		for name, got := range pass.canary {
			rec := pass.recorded[name]
			wantR0, wantFlip := r0.Canary[rec], r0.Canary[rec]
			if k, ok := registeredCanaryNarrowing[name]; ok {
				wantR0, wantFlip = k[0], k[1]
			}
			if wantR0 != r0.Canary[rec] {
				t.Errorf("%s: R0 recorded canary %d, the table says %d", name, r0.Canary[rec], wantR0)
			}
			if got != wantFlip {
				t.Errorf("%s: canary answers %d, want %d", name, got, wantFlip)
			}
		}

		diffs, compared := compareToR0(r0, pass)
		if len(r0.Tasks) != registeredTasks || len(r0.Approvals) != registeredApprovals {
			t.Errorf("R0 recorded %d task starts and %d approvals, the oracle pins %d and %d", len(r0.Tasks), len(r0.Approvals), registeredTasks, registeredApprovals)
		}
		t.Logf("compared %d cells (%d route cells pinned)", compared, registeredAuthOnCells)
		tables := []diffTable{registeredFPWorkerNarrowing, registeredRuledNarrowings, registeredBootstrapRefused, registeredRetiredRotate, registeredGoneUnderIdentity, registeredUpdateApplyState}
		h.checkDiffs(t, diffs, tables)
		h.checkR2OnlyLetters(t, r0, false)
		hc, hcStage13 := h.checkHandlerCells(t, pass, false)
		t.Logf("handler cells (auth-on, core-key twins in): %d, of which stage 13 refuses %d", hc, hcStage13)
		t.Logf("auth-on: %d routes probed, %d delegated, %d unregistered, %d cells compared, %d differing",
			len(pass.probed), len(pass.delegated), len(pass.unregistered), compared, len(diffs))
		var five []string
		for k, n := range pass.serverErrors {
			five = append(five, fmt.Sprintf("%s x%d", k, n))
		}
		sort.Strings(five)
		t.Logf("routes with a 5xx behind an admit (%d): %v", len(five), five)
	})

	t.Run("auth-off", func(t *testing.T) {
		recordedEnvironment(t)
		h := bootRegistered(t, registeredConfig{AuthOff: true, Seed: seedRecordedFlags}) // fails unless auth_disabled is true
		pass := h.runOracle(r0)
		if len(pass.noVerdict) > 0 || len(pass.canaryDrift) > 0 {
			t.Fatalf("no verdict: %v; canary drift: %v", pass.noVerdict, pass.canaryDrift)
		}
		diffs, compared := compareToR0(r0, pass)
		h.checkDiffs(t, diffs, []diffTable{registeredAuthOffTightening, registeredBootstrapRefusedAuthOff, registeredGoneUnderIdentityAuthOff})
		if compared != registeredAuthOffCells {
			t.Errorf("auth-off compared %d cells, pinned %d", compared, registeredAuthOffCells)
		}
		for _, d := range diffs {
			t.Logf("auth-off difference: %s: %q", d, d.res.excerpt())
		}
		h.checkR2OnlyLetters(t, r0, true)
		hc, hcStage13 := h.checkHandlerCells(t, pass, true)
		t.Logf("handler cells (auth-off): %d, of which stage 13 refuses %d", hc, hcStage13)
		t.Logf("auth-off: %d cells compared, %d differing", compared, len(diffs))
	})
}
