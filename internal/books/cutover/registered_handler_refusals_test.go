// file: internal/books/cutover/registered_handler_refusals_test.go
// version: 0.1.0
// guid: 367f1bc9-e2c5-4b4b-9d43-74449ae1bd55
// last-edited: 2026-11-22

package cutover_test

// F4-08 B10e2: the handler-level refusals, proved on the registered path.
//
// Three of the F4-07 obligations that internal/books/cutover could only state
// against the oracle's model are proved here against the flipped build itself,
// through the B10d harness (stage 12 binds the principal, stage 13 decides, and
// books' gin engine answers behind them):
//
//	TestReachRegisteredPath_HandlerRefusals      F4-07-HANDLER-REFUSALS (8 cells)
//	TestReachRegisteredPath_HiddenHandlerChecks  F4-07-HIDDEN-HANDLER-CHECKS (32 routes)
//	TestReachRegisteredPath_PinnedCells          the 11 cells reachHandlerAdmin hides
//	TestReachRegisteredPath_CustomCoreAdmin      a role holding core:admin that is not
//	                                             named admin, on the admin-gated routes:
//	                                             refused where a handler check still names
//	                                             the admin role, admitted on the three
//	                                             ROLE-NAME-GATE rows it was sent to (owner
//	                                             ruling 11-22, B10rn STOP 1)
//
// HEAD's verdict is the recorded one: verdicts.json (every route, and every task
// definition and the approval, with the real ids R0 used) and the R0 tables of
// reach_gate_test.go, reach_hidden_sites_test.go and reach_pin_test.go. The flip's
// is res.Verdict(). The tables of those files belong to package cutover, which this
// package cannot read, so the rows are spelled out here and their counts asserted
// exactly; the discharge registration (reachDischarges) names the tests above.
//
// Three outcomes, all explicit:
//
//	flip admits, HEAD refused    a WIDENING: the test fails, with no table to absorb it
//	flip refuses, HEAD admitted  a narrowing: listed in rhrKnownNarrowing with its reason,
//	                             or the test fails
//	flip 404, HEAD refused       the record the handler would have judged does not exist
//	                             under the flip (rhrAbsentUnderFlip): the status is pinned
//	                             to 404, so a 200 or a 5xx still fails
//
// HEAD's side of a hidden check cannot be recorded with a dummy id (the handler
// answers 404 before it reaches its check), so it is derived from the code HEAD ran:
// the route's recorded letter is the floor, and the in-handler test adds a refusal
// that is stated per row in terms of recorded letters of routes that carry the same
// permission atom alone (rhrExemplar), never in terms of the flipped handler.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
)

// ---- HEAD's recorded answers ----

type rhrTask struct {
	ID          string   `json:"id"`
	Permissions []string `json:"permissions"`
	Status      []int    `json:"status"`
}

type rhrRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Probe  string `json:"probe_path"`
	Status []int  `json:"status"`
}

type rhrApproval struct {
	RunID  string `json:"run_id"`
	Status []int  `json:"status"`
}

type rhrOwnerSet struct {
	Routes []struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Status []int  `json:"status"`
	} `json:"routes"`
}

type rhrRecorded struct {
	principals []string
	idx        map[string]int
	routes     map[string]rhrRoute
	tasks      []rhrTask
	approval   rhrApproval
	owner      rhrOwnerSet
}

func rhrLoad(t testing.TB) *rhrRecorded {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), reachRel, "verdicts.json"))
	if err != nil {
		t.Fatalf("the recorded R0 answers: %v", err)
	}
	var doc struct {
		Principals []string      `json:"principals"`
		Routes     []rhrRoute    `json:"routes"`
		Tasks      []rhrTask     `json:"tasks"`
		Approvals  []rhrApproval `json:"approvals"`
		OwnerSet   rhrOwnerSet   `json:"owner_set"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("verdicts.json: %v", err)
	}
	r := &rhrRecorded{principals: doc.Principals, idx: map[string]int{}, routes: map[string]rhrRoute{}, tasks: doc.Tasks, owner: doc.OwnerSet}
	for i, n := range doc.Principals {
		r.idx[n] = i
	}
	for _, rt := range doc.Routes {
		r.routes[rt.Method+" "+rt.Path] = rt
	}
	if len(doc.Approvals) != 1 {
		t.Fatalf("verdicts.json records %d approvals, want 1", len(doc.Approvals))
	}
	r.approval = doc.Approvals[0]
	return r
}

func rhrLetter(status int) byte {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return 'D'
	}
	return 'A'
}

// status is HEAD's recorded status for route (a "METHOD /gin/path" key) and principal.
func (r *rhrRecorded) status(t testing.TB, route, who string) int {
	t.Helper()
	rt, ok := r.routes[route]
	if !ok {
		t.Fatalf("verdicts.json has no route %s", route)
	}
	i, ok := r.idx[who]
	if !ok {
		t.Fatalf("verdicts.json has no principal %s", who)
	}
	return rt.Status[i]
}

// headStatus is HEAD's recorded status for the cell: the approval's own recording
// for the approve (a real run id), the route's otherwise.
func (r *rhrRecorded) headStatus(t testing.TB, route, who string) int {
	t.Helper()
	if route == "POST /api/v3/ops/runs/:id/approve" {
		return r.approval.Status[r.idx[who]]
	}
	return r.status(t, route, who)
}

func (r *rhrRecorded) letter(t testing.TB, route, who string) byte {
	t.Helper()
	return rhrLetter(r.status(t, route, who))
}

// rhrExemplar is, per legacy permission atom, a route whose only requirement is
// that atom and whose handler tests nothing further: HEAD's recorded letter on it
// is whether the principal holds the atom (a key's scopes included).
var rhrExemplar = map[string]string{
	"users.manage":          "GET /api/v1/users",
	"settings.manage":       "DELETE /api/v1/operations/v2/:id",
	"library.delete":        "DELETE /api/v1/audiobooks/:id",
	"library.organize":      "POST /api/v1/audiobooks/:id/restore",
	"library.edit_metadata": "POST /api/v1/ai/parse-filename",
	"library.view":          "GET /api/v1/activity",
	"integrations.manage":   "GET /api/v1/deluge/labels",
	"collections.manage":    "POST /api/v1/collections",
	"scan.trigger":          "POST /api/v1/operations/reconcile/scan",
}

func (r *rhrRecorded) holds(t testing.TB, atom, who string) bool {
	t.Helper()
	ex, ok := rhrExemplar[atom]
	if !ok {
		t.Fatalf("no exemplar route for atom %s", atom)
	}
	return r.letter(t, ex, who) == 'A'
}

func (r *rhrRecorded) task(t testing.TB, id string) rhrTask {
	t.Helper()
	for _, tk := range r.tasks {
		if tk.ID == id {
			return tk
		}
	}
	t.Fatalf("verdicts.json has no task %s", id)
	return rhrTask{}
}

// defAdmits is HEAD's start rule for a definition: scan.trigger and every
// permission the definition declares (settings.manage when it declares none).
func (r *rhrRecorded) defAdmits(t testing.TB, def, who string) bool {
	t.Helper()
	return rhrLetter(r.task(t, def).Status[r.idx[who]]) == 'A'
}

// ---- what the flipped boot holds ----

func rhrBody(t testing.TB, res Result) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(res.Body, &m); err != nil {
		t.Fatalf("not JSON (%d): %s", res.Status, res.excerpt())
	}
	if d, ok := m["data"].(map[string]any); ok {
		return d
	}
	return m
}

func rhrItems(m map[string]any, key string) []map[string]any {
	var out []map[string]any
	list, _ := m[key].([]any)
	for _, e := range list {
		if o, ok := e.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

func rhrStr(m map[string]any, k string) string { s, _ := m[k].(string); return s }

// rhrIDs are real ids of the corpus the harness booted, found through the admin
// session rather than typed.
type rhrIDs struct {
	book      string // a library book
	target    string // a user no principal signs in as (the corpus "admin" user)
	keyID     string // an API key of keyOwner
	keyOwner  string // lucius.fox: the owner of the admin principals
	fixer     string // a Repairs fixer that is not plan-only
	run       string // a finished v3 run
	runDef    string // its definition
	v2Row     string // a retryable v2 operation row
	v2RowDef  string // its definition
	v2Finish  string // a row for finish: interrupted_ask when the corpus has one, else v2Row
	userNames map[string]string
}

func rhrDiscover(t testing.TB, h *Registered, rec *rhrRecorded) rhrIDs {
	t.Helper()
	adm := h.Principal("session:admin")
	ids := rhrIDs{userNames: map[string]string{}}

	books := rhrItems(rhrBody(t, h.MustDo(t, adm, "GET", "/api/v1/audiobooks", nil)), "items")
	if len(books) == 0 {
		t.Fatal("the corpus has no books")
	}
	ids.book = rhrStr(books[0], "id")

	users := rhrItems(rhrBody(t, h.MustDo(t, adm, "GET", "/api/core/v1/users", nil)), "items")
	for _, u := range users {
		ids.userNames[rhrStr(u, "username")] = rhrStr(u, "id")
	}
	ids.target, ids.keyOwner = ids.userNames["admin"], ids.userNames["lucius.fox"]
	if ids.target == "" || ids.keyOwner == "" {
		t.Fatalf("the corpus lacks the admin or lucius.fox user: %v", ids.userNames)
	}
	for _, name := range []string{"lucius.fox", "dick.grayson", "barbara.gordon", "fp.worker", "james.gordon", "harleen.quinzel", "lucius.fox.jr"} {
		_ = name // principal users, none of them the target
	}
	keys := rhrItems(rhrBody(t, h.MustDo(t, adm, "GET", "/api/core/v1/users/"+ids.keyOwner+"/api-keys", nil)), "items")
	if len(keys) == 0 {
		t.Fatal("lucius.fox has no API key in the corpus")
	}
	ids.keyID = rhrStr(keys[0], "id")

	for _, f := range rhrItems(rhrBody(t, h.MustDo(t, adm, "GET", "/api/v1/repairs", nil)), "fixers") {
		if planOnly, _ := f["plan_only"].(bool); !planOnly {
			ids.fixer = rhrStr(f, "id")
			break
		}
	}
	if ids.fixer == "" {
		t.Fatal("the corpus has no Repairs fixer that is not plan-only")
	}

	for _, r := range rhrItems(rhrBody(t, h.MustDo(t, adm, "GET", "/api/v3/ops/runs", nil)), "items") {
		def := rhrStr(r, "def_id")
		if _, known := rec.taskIdx(def); known && ids.run == "" && rhrStr(r, "state") != "running" {
			ids.run, ids.runDef = rhrStr(r, "id"), def
		}
	}
	if ids.run == "" {
		t.Fatal("the corpus has no finished v3 run of a recorded definition")
	}

	for _, o := range rhrItems(rhrBody(t, h.MustDo(t, adm, "GET", "/api/v1/operations/timeline", nil)), "operations") {
		def, status := rhrStr(o, "def_id"), rhrStr(o, "status")
		if _, known := rec.taskIdx(def); !known {
			continue
		}
		switch status {
		case "completed", "failed", "canceled":
			if ids.v2Row == "" {
				ids.v2Row, ids.v2RowDef = rhrStr(o, "id"), def
			}
		case "interrupted_ask":
			if ids.v2Finish == "" {
				ids.v2Finish = rhrStr(o, "id")
			}
		}
	}
	if ids.v2Row == "" {
		t.Fatal("the corpus has no finished v2 operation of a recorded definition")
	}
	if ids.v2Finish == "" {
		ids.v2Finish = ids.v2Row
	}
	return ids
}

func (r *rhrRecorded) taskIdx(id string) (rhrTask, bool) {
	for _, tk := range r.tasks {
		if tk.ID == id {
			return tk, true
		}
	}
	return rhrTask{}, false
}

// rhrWarden signs in a user whose role is custom.wardens: it holds core:admin and
// nothing else, and is not named admin. HEAD has no such role (it has no role
// CRUD); the flip's role-name requirement exists to refuse it where HEAD's
// owner-role check refuses an account that is not the admin role.
func rhrWarden(t testing.TB, h *Registered) *Principal {
	t.Helper()
	adm := h.Principal("session:admin")
	if res := h.MustDo(t, adm, "POST", "/api/core/v1/roles", map[string]any{
		"name": "custom.wardens", "title": "Wardens", "description": "core:admin, not named admin",
		"permissions": []string{"core:admin"},
	}); res.Status != http.StatusCreated {
		t.Fatalf("create the custom core:admin role: %d %s", res.Status, res.excerpt())
	}
	res := h.MustDo(t, adm, "POST", "/api/core/v1/users/invites", map[string]any{"username": "alfred.pennyworth.warden", "role": "custom.wardens"})
	if res.Status != http.StatusCreated {
		t.Fatalf("invite the warden: %d %s", res.Status, res.excerpt())
	}
	var inv struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(res.Body, &inv); err != nil || inv.Link == "" {
		t.Fatalf("the invite answered no link: %s", res.excerpt())
	}
	token := inv.Link[strings.LastIndexAny(inv.Link, "=/#")+1:]
	const password = "fixture-password-wardens-1939"
	req := httptest.NewRequest(http.MethodPost, "/api/core/v1/auth/invites/accept",
		strings.NewReader(fmt.Sprintf(`{"token":%q,"password":%q}`, token, password)))
	req.Header.Set("Content-Type", "application/json")
	if acc := h.Raw(req); acc.Status != http.StatusOK {
		t.Fatalf("accept the warden's invite: %d %s", acc.Status, acc.excerpt())
	}
	p := &Principal{Name: "session:custom-core-admin", Kind: "session", User: "alfred.pennyworth.warden", Role: "custom.wardens", peer: h.nextPeer()}
	if p.cookie = h.login(p, password); p.cookie == nil {
		t.Fatal("the warden has no session")
	}
	if me := h.MustDo(t, p, "GET", "/api/core/v1/me", nil); !strings.Contains(string(me.Body), `"core:admin"`) {
		t.Fatalf("the warden does not hold core:admin: %s", me.excerpt())
	}
	return p
}

// rhrStream sends a request to an event stream: the handler holds the connection
// open after its permission check, so the request carries a deadline and the
// answer is read when it ends. A refusal is written before the stream starts.
func rhrStream(h *Registered, p *Principal, path string) Result {
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	req.RemoteAddr = h.peerFor(p)
	switch {
	case p.cookie != nil:
		req.AddCookie(p.cookie)
	case p.bearer != "":
		req.Header.Set("Authorization", "Bearer "+p.bearer)
	}
	rec := httptest.NewRecorder()
	h.Handler.ServeHTTP(rec, req)
	return resultOf(rec, h.match(http.MethodGet, path))
}

// ---- the cells ----

// rhrCell is one request of the hidden-check table.
type rhrCell struct {
	route  string // the route's key, "METHOD /gin/path"
	method string
	path   string
	body   any
	// refuses is the in-handler refusal HEAD adds for this principal beyond the
	// route's recorded letter, from the code HEAD ran. nil: none.
	refuses func(who string) bool
	stream  bool
	// head overrides the whole of HEAD's answer (a recorded letter of its own).
	head func(who string) byte
	// victim, when set, gives each request of the cell its own path: a cell
	// that changes the record it names (a key disabled or revoked) must not
	// change one a later request, or a principal's own credential, depends on.
	victim func(t testing.TB) string
	note   string
}

// rhrVictims are the keys the cells that change a key act on, one per request:
// HEAD key rows of lucius.fox with the scopes of the key rhrDiscover names
// (library.view), seeded before the boot so M3 imports them as it imports
// prod's keys, and held by no principal. The owner and the scopes are the old
// target's, so a principal's verdict on a victim is its verdict on that key.
type rhrVictims struct {
	pools map[string][]string // tag -> key ids, in hand-out order
	next  map[string]int
}

// rhrVictimScopes are the HEAD atoms of every victim: those of the corpus key of
// lucius.fox that rhrDiscover names (keys[0], key:admin:view's row).
var rhrVictimScopes = []string{"library.view"}

// newRHRVictims reserves n victims for each tag. The ids sort after every
// principal's key (01M7R2...), so rhrDiscover's keys[0] is unchanged.
func newRHRVictims(n int, tags ...string) *rhrVictims {
	v := &rhrVictims{pools: map[string][]string{}, next: map[string]int{}}
	for ti, tag := range tags {
		for i := range n {
			v.pools[tag] = append(v.pools[tag], fmt.Sprintf("01M7VD%02d%018d", ti, i))
		}
	}
	return v
}

// seed is the registeredConfig.Seed that writes every victim.
func (v *rhrVictims) seed(ctx context.Context, lr storage.LegacyRange) error {
	for tag, ids := range v.pools {
		for i, id := range ids {
			if _, err := SeedAPIKey(ctx, lr, KeySeed{ID: id, User: "lucius.fox", Name: fmt.Sprintf("victim-%s-%02d", tag, i), Scopes: rhrVictimScopes}); err != nil {
				return err
			}
		}
	}
	return nil
}

// path hands out the next victim of tag under prefix, failing when the pool is
// spent: a reused victim would be a key an earlier request already changed.
func (v *rhrVictims) path(tag, prefix string) func(t testing.TB) string {
	return func(t testing.TB) string {
		t.Helper()
		i := v.next[tag]
		if i >= len(v.pools[tag]) {
			t.Fatalf("the %s victims are spent (%d): reserve more", tag, len(v.pools[tag]))
		}
		v.next[tag] = i + 1
		return prefix + v.pools[tag][i]
	}
}

// rhrAlive returns the principals whose credential no longer authenticates: a
// cell that disabled or revoked a key a principal holds, or ended a session,
// turns every later cell of that principal into a 401, which the table reads as
// a refusal and so hides.
func rhrAlive(t testing.TB, h *Registered) []string {
	t.Helper()
	var dead []string
	for _, p := range h.Principals {
		if p.Name == Anonymous {
			continue
		}
		res, err := h.Do(p, http.MethodGet, "/api/core/v1/me", nil)
		switch {
		case err != nil:
			dead = append(dead, fmt.Sprintf("%s: %v", p.Name, err))
		case res.Status == http.StatusUnauthorized:
			dead = append(dead, fmt.Sprintf("%s: 401 %s", p.Name, res.excerpt()))
		}
	}
	return dead
}

// The obligations these tests discharge (reach_flip_obligations_test.go): each
// declares the cases it covers from the tables the test itself runs, so a table
// edited without the bar, or the bar edited without the table, shows up there.
func init() {
	cutover.RegisterReachDischarge("F4-07-HANDLER-REFUSALS", "TestReachRegisteredPath_HandlerRefusals", func() []string {
		out := make([]string, 0, len(rhrHandlerRefusals))
		for _, r := range rhrHandlerRefusals {
			out = append(out, r.route+"|"+Authoff)
		}
		sort.Strings(out)
		return out
	})
	cutover.RegisterReachDischarge("F4-07-HIDDEN-HANDLER-CHECKS", "TestReachRegisteredPath_HiddenHandlerChecks", func() []string {
		out := slices.Clone(rhrHiddenRoutes)
		sort.Strings(out)
		return out
	})
}

// rhrAbsentUnderFlip are the routes whose handler judges a record of books' own
// store (a user) that M3 moved into core and left no copy of: under the flip the
// lookup finds nothing, so a principal HEAD refused on a real record gets a 404
// and nothing of the record. The status is pinned; an answer that carried the
// record would be a widening and fails. The api-key routes are not here: under
// identity they read core's keys (internal/server/handlers/apikeys_identity.go),
// so a principal HEAD refused is refused again, 403.
var rhrAbsentUnderFlip = map[string]string{
	"PUT /api/v1/auth/me/password": "books' user table is empty under the flip: the target user is not found after the users.manage check",
}

// The two core rules HEAD's /api/v1/auth/api-keys/:id meets under identity
// (ruled option 1, coordinator 11-22; 09-abo-on-core §9.2.6).
const (
	rhrWhyKeyMayCreate = "a key manages only keys it could create (core keyMayManage, 05-authn §4.4; core/auth/apikeys.go:234,281): " +
		"every scope of the target must be held by the managing key, else 403 cannot grant scope you don't have. " +
		"HEAD let any key of the owner revoke any of the owner's keys (internal/server/handlers/apikeys.go:475 at origin/main)"
	rhrWhyDominance = "users.manage plus dominance over the owner's roles, and an interactive session for another user's key " +
		"(core keyReader.may, core/auth/admin_keys.go:116-129; mayManage, core/auth/service.go:546-564; SEC-4): custom-all holds " +
		"every atom but not core:admin, so it does not outrank lucius.fox's admin role, and core answers 403 access denied. " +
		"HEAD checked users.manage only (isAdminUser, internal/server/handlers/apikeys.go:220-222, used at :384, :414 and :475 at origin/main)"
	rhrWhyKeyInteractive = "; a key is also refused another user's key for want of an interactive sign-in (core/auth/service.go:558)"
)

// rhrKnownNarrowing are the (route, principal) cells where the flip refuses and HEAD
// admitted, each pinned with HEAD's and the flip's status. Every row is reported in
// the hand-back; none loosens the general expectation.
type rhrNarrow struct {
	route, who string
	head, flip int
	why        string
}

var rhrKnownNarrowing = []rhrNarrow{
	// Ruled option 1 (coordinator, 11-22): HEAD's /api/v1/auth/api-keys/:id under core's key rules.
	// HEAD's status is derived (head 0): R0 recorded these routes with a missing id, 404 for everyone.
	// A core twin is judged under its imported twin's name, so a key:... row also covers corekey:...
	{route: "DELETE /api/v1/auth/api-keys/:id", who: "key:admin:settings", head: 0, flip: 403, why: rhrWhyKeyMayCreate},
	{route: "DELETE /api/v1/auth/api-keys/:id", who: "key:admin:users", head: 0, flip: 403, why: rhrWhyKeyMayCreate},
	{route: "DELETE /api/v1/auth/api-keys/:id", who: "key:admin:view", head: 0, flip: 403,
		why: rhrWhyKeyMayCreate + ". Only the core twin (corekey:admin:view) is refused: the victim, an imported-shape library.view key, carries the ambient scopes (core:self, core:metrics.read, books:reading) that a core-minted key never holds; key:admin:view itself revokes it"},
	{route: "GET /api/v1/auth/api-keys/:id", who: "key:custom-all:empty", head: 0, flip: 403, why: rhrWhyDominance},
	{route: "GET /api/v1/auth/api-keys/:id", who: "key:custom-all:every", head: 0, flip: 403, why: rhrWhyDominance},
	{route: "GET /api/v1/auth/api-keys/:id", who: "session:custom-all", head: 0, flip: 403, why: rhrWhyDominance},
	{route: "PATCH /api/v1/auth/api-keys/:id", who: "session:custom-all", head: 0, flip: 403, why: rhrWhyDominance},
	{route: "DELETE /api/v1/auth/api-keys/:id", who: "key:custom-all:empty", head: 0, flip: 403, why: rhrWhyDominance + rhrWhyKeyInteractive},
	{route: "DELETE /api/v1/auth/api-keys/:id", who: "key:custom-all:every", head: 0, flip: 403, why: rhrWhyDominance + rhrWhyKeyInteractive},
	{route: "DELETE /api/v1/auth/api-keys/:id", who: "session:custom-all", head: 0, flip: 403, why: rhrWhyDominance},
	{route: "GET /api/v1/auth/api-keys", who: "key:fp-worker:settings", head: 200, flip: 403,
		why: "core refuses the fingerprint-worker scope on the key listing (permission_denied); HEAD's route is AnyAuthenticated. The same cell as GET /api/v1/auth/me, already ruled"},
	{route: "GET /api/v1/auth/api-keys", who: "key:fp-worker:users", head: 200, flip: 403,
		why: "core refuses the fingerprint-worker scope on the key listing (permission_denied); HEAD's route is AnyAuthenticated. The same cell as GET /api/v1/auth/me, already ruled"},
	{route: "GET /api/v1/auth/api-keys", who: "key:fp-worker:view", head: 200, flip: 403,
		why: "core refuses the fingerprint-worker scope on the key listing (permission_denied); HEAD's route is AnyAuthenticated. The same cell as GET /api/v1/auth/me, already ruled"},
	{route: "PUT /api/v1/config", who: "key:admin:empty", head: 0, flip: 403,
		why: "recorded (coordinator, 11-22, option 1): core refuses an API key on a protected key even when the write leaves it unchanged; HEAD refused a non-interactive caller only for a protected field that changed (internal/config/update_service.go:784 at origin/main), and the probe writes the stored value, so HEAD admitted it (derived, not recorded, hence head 0). A changing write is refused by both"},
	{route: "PUT /api/v1/config", who: "key:admin:every", head: 0, flip: 403,
		why: "recorded (coordinator, 11-22, option 1): core refuses an API key on a protected key even when the write leaves it unchanged; HEAD refused a non-interactive caller only for a protected field that changed (internal/config/update_service.go:784 at origin/main), and the probe writes the stored value, so HEAD admitted it (derived, not recorded, hence head 0). A changing write is refused by both"},
}

func (r rhrNarrow) key() string { return r.route + "|" + r.who }

// rhrRun sends cell as p.
func rhrRun(t testing.TB, h *Registered, p *Principal, c rhrCell) (Result, error) {
	t.Helper()
	if c.stream {
		return rhrStream(h, p, c.path), nil
	}
	return h.Do(p, c.method, c.path, c.body)
}

// rhrJudge runs every principal of the boot (the 36 recorded ones present) over
// cells and returns the problems. headOf is HEAD's letter for p on c.
func rhrJudge(t *testing.T, h *Registered, rec *rhrRecorded, cells []rhrCell, used map[string]bool) (problems []string, ran int) {
	t.Helper()
	known := map[string]rhrNarrow{}
	for _, k := range rhrKnownNarrowing {
		known[k.key()] = k
	}
	for _, c := range cells {
		for _, p := range h.Principals {
			// A core-native key has no HEAD recording: it is held to its imported twin's.
			rn := p.Recorded()
			if _, recorded := rec.idx[rn]; !recorded {
				continue
			}
			head := byte('A')
			switch {
			case c.head != nil:
				head = c.head(rn)
			case rec.letter(t, c.route, rn) == 'D' || (c.refuses != nil && c.refuses(rn)):
				head = 'D'
			}
			cc := c
			if c.victim != nil {
				cc.path = c.victim(t)
			}
			res, err := rhrRun(t, h, p, cc)
			ran++
			label := fmt.Sprintf("%s as %s", c.route, p.Name)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v (a finding, not a skip)", label, err))
				continue
			}
			flip := res.Verdict()
			switch {
			case flip == head:
			case flip == 'A' && head == 'D':
				if why, ok := rhrAbsentUnderFlip[c.route]; ok && res.Status == http.StatusNotFound {
					_ = why
					continue
				}
				problems = append(problems, fmt.Sprintf("WIDENING %s: HEAD refuses, the flip answers %d %s", label, res.Status, res.excerpt()))
			default:
				if k, ok := known[c.route+"|"+rn]; ok && k.flip == res.Status {
					if k.head != 0 && k.head != rec.headStatus(t, c.route, rn) {
						problems = append(problems, fmt.Sprintf("known narrowing %s as %s lists HEAD status %d, the recording has %d", c.route, rn, k.head, rec.headStatus(t, c.route, rn)))
					}
					used[k.key()] = true
					continue
				}
				problems = append(problems, fmt.Sprintf("NARROWING (unlisted) %s: HEAD admits, the flip answers %d %s", label, res.Status, res.excerpt()))
			}
		}
	}
	return problems, ran
}

func rhrReport(t *testing.T, problems []string) {
	t.Helper()
	sort.Strings(problems)
	for i, p := range problems {
		if i == 40 {
			t.Errorf("... and %d more", len(problems)-40)
			break
		}
		t.Error(p)
	}
}

// ---- 1. HandlerRefusals ----

// rhrHandlerRefusals are the eight cells of reachHandlerRefusals: seven credential
// routes and the approve, each as the auth-disabled principal, whom HEAD's handler
// refuses for want of a user. HEAD's statuses are the ones
// internal/server TestReachHeadHandlers_RefuseTheAuthDisabledPrincipal holds.
var rhrHandlerRefusals = []struct {
	route string
	head  int // HEAD's status
}{
	{"GET /api/v1/auth/me", http.StatusUnauthorized},
	{"GET /api/v1/auth/sessions", http.StatusUnauthorized},
	{"DELETE /api/v1/auth/sessions/:id", http.StatusUnauthorized},
	{"GET /api/v1/auth/api-keys", http.StatusUnauthorized},
	{"GET /api/v1/auth/api-keys/:id", http.StatusUnauthorized},
	{"POST /api/v1/auth/api-keys", http.StatusUnauthorized},
	{"DELETE /api/v1/auth/api-keys/:id", http.StatusUnauthorized},
	{"POST /api/v3/ops/runs/:id/approve", http.StatusForbidden},
}

func TestReachRegisteredPath_HandlerRefusals(t *testing.T) {
	if got := len(rhrHandlerRefusals); got != 8 {
		t.Fatalf("the table has %d cells, the obligation lists 8", got)
	}
	rec := rhrLoad(t)
	h := bootRegistered(t, registeredConfig{AuthOff: true})
	p := h.Principal(Authoff)

	for _, c := range rhrHandlerRefusals {
		t.Run(c.route, func(t *testing.T) {
			if got := rec.letter(t, c.route, Authoff); got != 'D' {
				t.Fatalf("HEAD's recorded letter for the auth-disabled principal is %c, the obligation says D", got)
			}
			method, pattern, _ := strings.Cut(c.route, " ")
			path := strings.NewReplacer(":id", "r2reach-missing").Replace(pattern)
			var body any
			if method == http.MethodPost {
				body = map[string]any{"decision": "maybe"}
				if strings.HasSuffix(pattern, "/api-keys") {
					body = map[string]any{"name": "batarang", "scopes": []string{}}
				}
			}
			res, err := h.Do(p, method, path, body)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if res.Verdict() != 'D' {
				t.Fatalf("WIDENING: HEAD refuses the auth-disabled principal (%d), the flip answers %d %s", c.head, res.Status, res.excerpt())
			}
			if res.Status != c.head {
				// The refusal is the same; the status is the registered path's own.
				t.Logf("status differs: HEAD %d, flip %d (stage 13 evidence %v): %s", c.head, res.Status, res.Stage13, res.excerpt())
			}
		})
	}
}

// ---- 2. HiddenHandlerChecks ----

// rhrHiddenRoutes are the 32 routes the obligation bar names: the 23 routes of the
// in-handler sites of reachHiddenSites and the 9 owner routes (reachOwnerRoutes).
var rhrHiddenRoutes = []string{
	"POST /api/v1/repairs/:fixer/owner-apply",
	"POST /api/v1/auth/api-keys", "GET /api/v1/auth/api-keys",
	"GET /api/v1/auth/api-keys/:id", "PATCH /api/v1/auth/api-keys/:id", "DELETE /api/v1/auth/api-keys/:id",
	"PUT /api/v1/auth/me/password",
	"GET /api/v1/audiobooks/soft-deleted", "POST /api/v1/audiobooks/:id/discard-progress-and-purge",
	"POST /api/v1/audiobooks/batch", "POST /api/v1/audiobooks/batch-operations",
	"POST /api/v1/operations/v2", "POST /api/v1/operations/v2/:id/retry", "POST /api/v1/operations/v2/:id/finish",
	"GET /api/v1/operations/events", "GET /api/v3/ops/events",
	"POST /api/v3/ops/defs/:id/runs", "POST /api/v3/ops/runs", "POST /api/v3/ops/runs/:id/cancel", "POST /api/v3/ops/runs/:id/approve",
	"POST /api/v1/maintenance/jobs/:job_id",
	"GET /api/v1/users/invites",
	"PUT /api/v1/config",
	// the owner routes
	"POST /api/core/v1/system/factory-reset",
	"POST /api/v1/admin/guest-service-tokens/:userId/revoke", "POST /api/v1/admin/guest-service-tokens/:userId/reissue",
	"GET /api/v1/admin/guest-service-tokens/plan",
	"POST /api/v1/backup/restore",
	"POST /api/v1/users/:id/merge-into/:target/apply", "POST /api/v1/users/:id/merge-into/:target/preview",
	"POST /api/v1/users/merges/:jid/resume", "POST /api/v1/users/merges/:jid/undo",
}

func TestReachRegisteredPath_HiddenHandlerChecks(t *testing.T) {
	if got := len(rhrHiddenRoutes); got != 32 {
		t.Fatalf("the table names %d routes, the obligation bar has 32", got)
	}
	if dup := rhrDuplicates(rhrHiddenRoutes); len(dup) > 0 {
		t.Fatalf("routes listed twice: %v", dup)
	}
	rec := rhrLoad(t)
	// Under identity PATCH and DELETE /api/v1/auth/api-keys/:id change core's key.
	// On one shared key the first admitted principal (session:admin, lucius.fox)
	// disables it and then revokes it, and that key is keys[0], key:admin:view's
	// own credential: every later cell of key:admin:view answered 401. Each request
	// of those two cells gets a victim of its own instead. At most every recorded
	// principal plus a core twin per key row runs a cell.
	rows, _ := loadPrincipalTable(t)
	victims := newRHRVictims(2*len(rows), "patch", "delete")
	h := bootRegistered(t, registeredConfig{Seed: victims.seed})
	ids := rhrDiscover(t, h, rec)
	// The 23 routes of the in-handler sites are all in R0's recording; the owner
	// routes (the last 9) are judged against their recorded rows, except core's own
	// factory-reset, which main does not serve and whose letters are ruled
	// (factoryResetNoOwnerLetter).
	for _, r := range rhrHiddenRoutes[:23] {
		if _, ok := rec.routes[r]; !ok {
			t.Fatalf("verdicts.json has no route %s", r)
		}
	}

	noOwnerMint := func(who string) bool {
		// Another user's key is minted for them only by an interactive caller with
		// users.manage: a key credential is refused at the route (credRouteWhen).
		return strings.HasPrefix(who, "key:") || !rec.holds(t, "users.manage", who)
	}
	notUsersManage := func(who string) bool { return !rec.holds(t, "users.manage", who) }
	lacks := func(atom string) func(string) bool {
		return func(who string) bool { return !rec.holds(t, atom, who) }
	}
	otherUsersKey := func(who string) bool {
		// A key of lucius.fox is another user's to everyone but lucius.fox's own
		// principals, and an admin of users reads it either way.
		owner := false
		for _, p := range h.Principals {
			if p.Name == who && p.User == "lucius.fox" {
				owner = true
			}
		}
		return !owner && !rec.holds(t, "users.manage", who)
	}
	startDef := "maintenance.purge-old-logs"
	if _, ok := rec.taskIdx(startDef); !ok {
		t.Fatalf("verdicts.json has no task %s", startDef)
	}
	defStart := func(def string) func(string) byte {
		return func(who string) byte {
			if rec.defAdmits(t, def, who) {
				return 'A'
			}
			return 'D'
		}
	}
	// retry and finish: the route's own atom (settings.manage) is the floor; the
	// row's definition adds its permissions.
	rowDefRefuses := func(def string) func(string) bool {
		tk := rec.task(t, def)
		return func(who string) bool {
			for _, perm := range tk.Permissions {
				if !rec.holds(t, perm, who) {
					return true
				}
			}
			return false
		}
	}
	cancelHead := func(who string) byte {
		// the caller did not start the run: scan.trigger and the definition's permissions
		if rec.letter(t, "POST /api/v3/ops/runs/:id/cancel", who) == 'D' || !rec.defAdmits(t, ids.runDef, who) {
			return 'D'
		}
		return 'A'
	}
	approveHead := func(who string) byte { return rhrLetter(rec.approval.Status[rec.idx[who]]) }

	owner := func(who string) bool { return true } // no owner is configured: HEAD refuses everyone

	cells := []rhrCell{
		{route: "POST /api/v1/repairs/:fixer/owner-apply", method: "POST", path: "/api/v1/repairs/" + ids.fixer + "/owner-apply",
			body: map[string]any{"plan_op_id": "r2reach-missing", "row_id": "r2reach-missing"}, refuses: owner,
			note: "no owner proof: ownerWhyNot refuses before the role name is read"},

		{route: "POST /api/v1/auth/api-keys", method: "POST", path: "/api/v1/auth/api-keys",
			body: map[string]any{"name": "batarang", "user_id": ids.target, "scopes": []string{}}, refuses: noOwnerMint},
		{route: "GET /api/v1/auth/api-keys", method: "GET", path: "/api/v1/auth/api-keys"},
		{route: "GET /api/v1/auth/api-keys/:id", method: "GET", path: "/api/v1/auth/api-keys/" + ids.keyID, refuses: otherUsersKey},
		{route: "PATCH /api/v1/auth/api-keys/:id", method: "PATCH", victim: victims.path("patch", "/api/v1/auth/api-keys/"),
			body: map[string]any{"status": "inactive"}, refuses: otherUsersKey},
		{route: "DELETE /api/v1/auth/api-keys/:id", method: "DELETE", victim: victims.path("delete", "/api/v1/auth/api-keys/"), refuses: otherUsersKey},

		{route: "PUT /api/v1/auth/me/password", method: "PUT", path: "/api/v1/auth/me/password",
			body: map[string]any{"user_id": ids.target, "new_password": "batmobile-1939-gotham"}, refuses: notUsersManage},

		{route: "GET /api/v1/audiobooks/soft-deleted", method: "GET", path: "/api/v1/audiobooks/soft-deleted",
			note: "the check narrows what is shown (rows of other users); the corpus has none, so only admission is judged"},
		{route: "POST /api/v1/audiobooks/:id/discard-progress-and-purge", method: "POST", path: "/api/v1/audiobooks/" + ids.book + "/discard-progress-and-purge",
			note: "same: the viewer test narrows, it does not refuse"},

		{route: "POST /api/v1/audiobooks/batch", method: "POST", path: "/api/v1/audiobooks/batch",
			body: map[string]any{"ids": []string{ids.book}, "updates": map[string]any{"marked_for_deletion": true}}, refuses: lacks("library.delete")},
		{route: "POST /api/v1/audiobooks/batch", method: "POST", path: "/api/v1/audiobooks/batch",
			body: map[string]any{"ids": []string{ids.book}, "updates": map[string]any{"marked_for_deletion": false}}, refuses: lacks("library.organize")},
		{route: "POST /api/v1/audiobooks/batch-operations", method: "POST", path: "/api/v1/audiobooks/batch-operations",
			body: map[string]any{"operations": []map[string]any{{"id": ids.book, "action": "delete"}}}, refuses: lacks("library.delete")},
		{route: "POST /api/v1/audiobooks/batch-operations", method: "POST", path: "/api/v1/audiobooks/batch-operations",
			body: map[string]any{"operations": []map[string]any{{"id": ids.book, "action": "restore"}}}, refuses: lacks("library.organize")},

		{route: "POST /api/v1/operations/v2", method: "POST", path: "/api/v1/operations/v2",
			body: map[string]any{"def_id": startDef}, head: defStart(startDef)},
		{route: "POST /api/v1/operations/v2/:id/retry", method: "POST", path: "/api/v1/operations/v2/" + ids.v2Row + "/retry", refuses: rowDefRefuses(ids.v2RowDef)},
		{route: "POST /api/v1/operations/v2/:id/finish", method: "POST", path: "/api/v1/operations/v2/" + ids.v2Finish + "/finish",
			body: map[string]any{}, refuses: nil, note: "a row not waiting on a decision answers 409 behind the check when the corpus has no interrupted_ask row"},

		{route: "GET /api/v1/operations/events", method: "GET", path: "/api/v1/operations/events", stream: true},
		{route: "GET /api/v3/ops/events", method: "GET", path: "/api/v3/ops/events", stream: true},

		{route: "POST /api/v3/ops/runs", method: "POST", path: "/api/v3/ops/runs",
			body: map[string]any{"def": startDef, "params": []any{}}, head: defStart(startDef)},
		{route: "POST /api/v3/ops/runs/:id/cancel", method: "POST", path: "/api/v3/ops/runs/" + ids.run + "/cancel", head: cancelHead},
		{route: "POST /api/v3/ops/runs/:id/approve", method: "POST", path: "/api/v3/ops/runs/" + rec.approval.RunID + "/approve",
			body: map[string]any{"decision": "maybe"}, head: approveHead},

		{route: "POST /api/v1/maintenance/jobs/:job_id", method: "POST", path: "/api/v1/maintenance/jobs/backfill-book-files",
			body: map[string]any{"dry_run": true}, note: "the job declares settings.manage, the route's own atom"},
		{route: "GET /api/v1/users/invites", method: "GET", path: "/api/v1/users/invites",
			note: "the check hides invite tokens from a non-admin; the corpus has no invite with an e-mail, so only admission is judged"},
		{route: "PUT /api/v1/config", method: "PUT", path: "/api/v1/config",
			body: map[string]any{"set": map[string]any{"activity_backend": "pebble"}},
			head: func(who string) byte {
				// A key above settings.manage (admin: true) needs the admin role by name.
				if rec.letter(t, "PUT /api/v1/config", who) == 'D' || (who != "session:admin" && who != "key:admin:every" && who != "key:admin:empty") {
					return 'D'
				}
				return 'A'
			}},
	}

	used := map[string]bool{} // the known narrowings seen, over every subtest

	// every v3 start, with the real definition id R0 used: HEAD's recorded letter
	// per definition and principal is the ground truth.
	t.Run("routes", func(t *testing.T) {
		problems, ran := rhrJudge(t, h, rec, cells, used)
		t.Logf("%d cells over %d routes", ran, len(cells))
		rhrReport(t, problems)
	})

	// No cell may cost a principal its credential: a 401 reads as a refusal, so
	// a dead credential would pass every cell HEAD refuses and hide the rest.
	t.Run("every credential still authenticates after the route cells", func(t *testing.T) {
		for _, d := range rhrAlive(t, h) {
			t.Errorf("a cell ended this principal's credential: %s", d)
		}
	})

	t.Run("v3 starts with every recorded definition", func(t *testing.T) {
		var problems []string
		ran := 0
		for _, tk := range rec.tasks {
			for _, p := range h.Principals {
				i, ok := rec.idx[p.Recorded()]
				if !ok {
					continue
				}
				head := rhrLetter(tk.Status[i])
				res, err := h.Do(p, "POST", "/api/v3/ops/defs/"+tk.ID+"/runs", map[string]any{"params": []any{}})
				ran++
				switch {
				case err != nil:
					problems = append(problems, fmt.Sprintf("%s as %s: %v", tk.ID, p.Name, err))
				case res.Status == http.StatusAccepted:
					problems = append(problems, fmt.Sprintf("%s as %s: the start was accepted (202): a run began", tk.ID, p.Name))
				case res.Verdict() == 'A' && head == 'D':
					problems = append(problems, fmt.Sprintf("WIDENING start %s as %s: HEAD %d, flip %d %s", tk.ID, p.Name, tk.Status[i], res.Status, res.excerpt()))
				case res.Verdict() == 'D' && head == 'A':
					problems = append(problems, fmt.Sprintf("NARROWING (unlisted) start %s as %s: HEAD %d, flip %d %s", tk.ID, p.Name, tk.Status[i], res.Status, res.excerpt()))
				}
			}
		}
		t.Logf("%d definitions, %d cells", len(rec.tasks), ran)
		rhrReport(t, problems)
	})

	// The owner routes, on a boot with no owner_email. The gate is skipped until an
	// owner is set (ownerGateWhenOwnerSet: the first owner_email may be claimed by
	// whoever signs in through Access), so HEAD's answer here is the route's ordinary
	// guard, which is what R0's route recording holds: session:admin is admitted, the
	// rest refused. With an owner set (R0's owner_set recording: 37 of 37 refused) is
	// B10e1r's. Every books owner route has its own recorded row (R0 is main at
	// f6698f9bf since F4-08). Core's factory reset has none: main serves no reset
	// route (623da76d3), so its letters are a ruling, not parity (registered_owner_test.go
	// ownerR2Only, 05-rbac §6.2, a deliberate addition versus main). With no owner set
	// it is 05-authn §4.8's fallback, Guards.Interactive plus core:admin: the
	// interactive admin session passes, every other principal is refused
	// (factoryResetNoOwnerLetter).
	t.Run("owner routes, no owner set", func(t *testing.T) {
		owners := rhrHiddenRoutes[23:]
		paths := map[string]string{
			"POST /api/core/v1/system/factory-reset":                  "/api/core/v1/system/factory-reset",
			"POST /api/v1/admin/guest-service-tokens/:userId/revoke":  "/api/v1/admin/guest-service-tokens/" + ids.target + "/revoke",
			"POST /api/v1/admin/guest-service-tokens/:userId/reissue": "/api/v1/admin/guest-service-tokens/" + ids.target + "/reissue",
			"GET /api/v1/admin/guest-service-tokens/plan":             "/api/v1/admin/guest-service-tokens/plan",
			"POST /api/v1/backup/restore":                             "/api/v1/backup/restore",
			"POST /api/v1/users/:id/merge-into/:target/apply":         "/api/v1/users/" + ids.keyOwner + "/merge-into/" + ids.target + "/apply",
			"POST /api/v1/users/:id/merge-into/:target/preview":       "/api/v1/users/" + ids.keyOwner + "/merge-into/" + ids.target + "/preview",
			"POST /api/v1/users/merges/:jid/resume":                   "/api/v1/users/merges/r2reach-missing/resume",
			"POST /api/v1/users/merges/:jid/undo":                     "/api/v1/users/merges/r2reach-missing/undo",
		}
		if len(owners) != 9 || len(paths) != 9 {
			t.Fatalf("%d owner routes listed, %d paths, want 9", len(owners), len(paths))
		}
		var cells []rhrCell
		for _, route := range owners {
			method, _, _ := strings.Cut(route, " ")
			c := rhrCell{route: route, method: method, path: paths[route], note: route}
			if route == "POST /api/core/v1/system/factory-reset" {
				c.head = factoryResetNoOwnerLetter
			}
			cells = append(cells, c)
		}
		problems, ran := rhrJudge(t, h, rec, cells, used)
		t.Logf("%d cells over %d owner routes", ran, len(cells))
		rhrReport(t, problems)
	})

	t.Run("every credential still authenticates after every cell", func(t *testing.T) {
		for _, d := range rhrAlive(t, h) {
			t.Errorf("a cell ended this principal's credential: %s", d)
		}
	})

	for _, k := range rhrKnownNarrowing {
		if !used[k.key()] {
			t.Errorf("known narrowing %s as %s no longer occurs (a stale row, or the flip admits again)", k.route, k.who)
			continue
		}
		t.Logf("known narrowing: %s as %s, HEAD %d, flip %d: %s", k.route, k.who, k.head, k.flip, k.why)
	}
}

func rhrDuplicates(in []string) []string {
	seen := map[string]bool{}
	var dup []string
	for _, s := range in {
		if seen[s] {
			dup = append(dup, s)
		}
		seen[s] = true
	}
	return dup
}

// ---- 3. the 11 pinned cells ----

// rhrPinnedCells are the cells reachHandlerAdmin hides (reachPinnedCells): the
// principals the route gate lets in and HEAD's handler refuses, for the role-name
// test and the key's admin-acting test. The flip must refuse each in the handler.
var rhrPinnedCells = []struct{ route, who string }{
	{"GET /api/v1/users/:id/footprint", "authoff"},
	{"GET /api/v1/users/:id/footprint", "key:admin:users"},
	{"GET /api/v1/users/:id/footprint", "key:custom-all:empty"},
	{"GET /api/v1/users/:id/footprint", "key:custom-all:every"},
	{"GET /api/v1/users/:id/footprint", "session:custom-all"},
	{"GET /api/v1/users/merges", "authoff"},
	{"GET /api/v1/users/merges", "key:admin:users"},
	{"GET /api/v1/users/merges", "key:custom-all:empty"},
	{"GET /api/v1/users/merges", "key:custom-all:every"},
	{"GET /api/v1/users/merges", "session:custom-all"},
	{"PUT /api/v1/users/:id/email", "session:custom-all"},
}

func rhrPinnedRequest(ids rhrIDs, route string) (method, path string, body any) {
	switch route {
	case "GET /api/v1/users/:id/footprint":
		return "GET", "/api/v1/users/" + ids.target + "/footprint", nil
	case "GET /api/v1/users/merges":
		return "GET", "/api/v1/users/merges", nil
	default:
		return "PUT", "/api/v1/users/" + ids.target + "/email", map[string]any{"email": "selina.kyle@example.test"}
	}
}

func TestReachRegisteredPath_PinnedCells(t *testing.T) {
	if got := len(rhrPinnedCells); got != 11 {
		t.Fatalf("the table has %d cells, the obligation lists 11", got)
	}
	// This test discharges the reachHandlerAdmin pin at the flip gate
	// (reachPinDischarge), so its cells must be the pin's cells exactly, as sets
	// in both directions: a pinned cell no registered cell runs, or a registered
	// cell the pin does not name, fails here (F4-08 B lead 2026-11-22, B10f row
	// amended).
	pinned := map[string]bool{}
	for _, c := range cutover.ReachPinnedCells() {
		i := strings.LastIndex(c, " / ")
		if i < 0 {
			t.Fatalf("pinned cell %q is not ROUTE / WHO: R0 ...", c)
		}
		j := strings.Index(c[i:], ": R0")
		if j < 0 {
			t.Fatalf("pinned cell %q is not ROUTE / WHO: R0 ...", c)
		}
		pinned[c[:i]+"|"+c[i+3:i+j]] = true
	}
	ours := map[string]bool{}
	for _, c := range rhrPinnedCells {
		ours[c.route+"|"+c.who] = true
		if !pinned[c.route+"|"+c.who] {
			t.Errorf("%s as %s: a registered cell the pin (reachPinnedCells) does not name", c.route, c.who)
		}
	}
	for k := range pinned {
		if !ours[k] {
			t.Errorf("%s: pinned (reachPinnedCells) but no registered cell runs it", strings.Replace(k, "|", " as ", 1))
		}
	}
	rec := rhrLoad(t)
	for _, c := range rhrPinnedCells {
		if got := rec.letter(t, c.route, c.who); got != 'D' {
			t.Fatalf("%s as %s: HEAD's recorded letter is %c, the pin says R0 D", c.route, c.who, got)
		}
	}

	// the 9 cells of principals the default boot holds
	t.Run("default boot", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{})
		ids := rhrDiscover(t, h, rec)
		n := 0
		for _, c := range rhrPinnedCells {
			if c.who == Authoff {
				continue
			}
			n++
			method, path, body := rhrPinnedRequest(ids, c.route)
			res := h.MustDo(t, h.Principal(c.who), method, path, body)
			t.Logf("%s as %s: %d, stage 13 %v", c.route, c.who, res.Status, res.Stage13)
			if res.Verdict() != 'D' {
				t.Errorf("WIDENING %s as %s: R0's handler refuses, the flip answers %d %s", c.route, c.who, res.Status, res.excerpt())
			}
		}
		if n != 9 {
			t.Fatalf("%d cells ran in the default boot, want 9", n)
		}
		// the pinned routes still admit the admin the handler lets in: the refusal
		// above is the handler's, not a closed route.
		for _, route := range []string{"GET /api/v1/users/:id/footprint", "GET /api/v1/users/merges", "PUT /api/v1/users/:id/email"} {
			method, path, body := rhrPinnedRequest(ids, route)
			if res := h.MustDo(t, h.Principal("session:admin"), method, path, body); res.Verdict() != 'A' {
				t.Errorf("control: session:admin is refused on %s (%d %s)", route, res.Status, res.excerpt())
			}
		}
	})

	// the 2 cells of the auth-disabled principal, which only the auth-off boot has
	t.Run("auth-off boot", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		ids := rhrIDs{target: "r2reach-missing"}
		n := 0
		for _, c := range rhrPinnedCells {
			if c.who != Authoff {
				continue
			}
			n++
			method, path, body := rhrPinnedRequest(ids, c.route)
			res := h.MustDo(t, h.Principal(Authoff), method, path, body)
			if res.Verdict() != 'D' {
				t.Errorf("WIDENING %s as %s: R0's handler refuses, the flip answers %d %s", c.route, c.who, res.Status, res.excerpt())
			}
		}
		if n != 2 {
			t.Fatalf("%d cells ran in the auth-off boot, want 2", n)
		}
	})
}

// ---- 4. a role holding core:admin that is not named admin ----

// rhrAdminGated are the requests whose HEAD gate is the admin role by name and whose
// flipped gate still is: a handler check that names the admin role. A custom role
// that holds core:admin has no HEAD counterpart; the flip keeps the role-name
// requirement beside core:admin so the role is refused where HEAD's owner-role check
// refuses every account that is not the admin role. Each row has a positive control:
// the admin session is admitted, so the refusal is the check's.
func rhrAdminGated(ids rhrIDs) []rhrCell {
	return []rhrCell{
		{route: "GET /api/v1/users/merges", method: "GET", path: "/api/v1/users/merges"},
		{route: "GET /api/v1/users/:id/footprint", method: "GET", path: "/api/v1/users/" + ids.target + "/footprint"},
		{route: "PUT /api/v1/users/:id/email", method: "PUT", path: "/api/v1/users/" + ids.target + "/email",
			body: map[string]any{"email": "selina.kyle@example.test"}},
		{route: "PUT /api/v1/config", method: "PUT", path: "/api/v1/config",
			body: map[string]any{"set": map[string]any{"activity_backend": "pebble"}},
			note: "an admin: true key: HEAD's configCan(core:admin) is the admin role by name (B10y item 5)"},
		{route: "POST /api/v1/repairs/:fixer/owner-apply", method: "POST", path: "/api/v1/repairs/" + ids.fixer + "/owner-apply",
			body: map[string]any{"plan_op_id": "r2reach-missing", "row_id": "r2reach-missing"}},
	}
}

// rhrScopeGated are the three requests of this test that are ROLE-NAME-GATE rows
// (rnRows; the 19 entries of internal/server's B10mg allow-list, whose entries for
// these three cited this test). HEAD's gate was gin's RequireAdmin, the admin role by
// name. Since B10g bridgedAdminGate enforces only the manifest requirement
// (internal/server/wire_admin_role.go), so a custom role holding core:admin, which
// implies every scope (05-rbac §4.3), is admitted: the owner-approved widening of
// 2026-11-22 (B10rn STOP 1, option 1: permissions only), the same ruling
// TestReachRegisteredPath_RoleNameRowsScopeGateAdmits asserts per scope class. Each
// is asserted admitted end to end, and gin's role-name refusal behind stage 13 fails
// it by name.
func rhrScopeGated() []rhrCell {
	return []rhrCell{
		{route: "GET /api/v1/admin/debug/edits", method: "GET", path: "/api/v1/admin/debug/edits"},
		{route: "GET /api/v1/admin/guest-service-tokens", method: "GET", path: "/api/v1/admin/guest-service-tokens"},
		{route: "GET /api/v1/cache/stats/keys", method: "GET", path: "/api/v1/cache/stats/keys"},
	}
}

func TestReachRegisteredPath_CustomCoreAdmin(t *testing.T) {
	rec := rhrLoad(t)
	h := bootRegistered(t, registeredConfig{})
	ids := rhrDiscover(t, h, rec)
	warden := rhrWarden(t, h)
	adm := h.Principal("session:admin")

	for _, c := range rhrScopeGated() {
		t.Run(c.route, func(t *testing.T) {
			if !slices.ContainsFunc(rnRows, func(r rnRow) bool { return r.route == c.route }) {
				t.Fatalf("%s is admitted by permission alone here but is not one of the 19 ROLE-NAME-GATE rows (rnRows): no ruling covers it", c.route)
			}
			res, err := rhrRun(t, h, warden, c)
			if err != nil {
				t.Fatalf("%v (a finding, not a skip)", err)
			}
			rnAdmittedEndToEnd(t, warden, res, rnNameRefusal)
		})
	}
	for _, c := range rhrAdminGated(ids) {
		t.Run(c.route, func(t *testing.T) {
			if slices.ContainsFunc(rnRows, func(r rnRow) bool { return r.route == c.route }) {
				t.Fatalf("%s is a ROLE-NAME-GATE row: the 11-22 ruling gates it by permission alone, so it belongs in rhrScopeGated", c.route)
			}
			res, err := rhrRun(t, h, warden, c)
			if err != nil {
				t.Fatalf("%v (a finding, not a skip)", err)
			}
			if res.Verdict() != 'D' {
				t.Errorf("WIDENING: HEAD's owner-role check refuses a role that is not the admin role; the flip answers %d %s", res.Status, res.excerpt())
			}
			if c.route == "POST /api/v1/repairs/:fixer/owner-apply" {
				return // refused for want of an owner proof whoever asks: no admitting control exists
			}
			ctl, err := rhrRun(t, h, adm, c)
			if err != nil {
				t.Fatalf("control: %v", err)
			}
			if ctl.Verdict() != 'A' {
				t.Errorf("control: the admin session is refused too (%d %s): the cell proves nothing", ctl.Status, ctl.excerpt())
			}
		})
	}
}

// factoryResetNoOwnerLetter is the expected letter of core's factory reset with no
// owner set, by principal: 05-authn §4.8's fallback for an owner route (Interactive
// plus core:admin) under 05-rbac §6.2's requirement, so the interactive admin
// session is admitted and every other principal is refused. It is a ruling for an
// R2-only route (registered_owner_test.go ownerR2Only), not a recorded letter: main
// serves no reset route.
func factoryResetNoOwnerLetter(principal string) byte {
	if principal == "session:admin" {
		return 'A'
	}
	return 'D'
}
