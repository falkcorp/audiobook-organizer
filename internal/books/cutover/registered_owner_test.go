// file: internal/books/cutover/registered_owner_test.go
// version: 0.1.0
// guid: 9d4e7b32-5a18-4c6f-b2e0-71f3a8c95d46
// last-edited: 2026-11-22

package cutover_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// The owner cells through the registered path (F4-08 B10e1r).
//
// The flipped build gates nine routes on the owner: a caller passes only as the
// Cloudflare Access-verified owner_email, and with owner_email set and no Access
// verifier nobody does. Eight are books routes HEAD gates the same way (R0's
// owner_set recording: 37 of 37 principals refused on each); the ninth is core's
// factory reset, which main does not serve at all (ownerR2Only).
// The flipped build must refuse everything HEAD refuses, so these cells boot the
// flipped build and ask stage 13 itself, through the harness, in the two owner
// configurations the flip meets:
//
//   - owner_email set WITH the Access verifier (registeredConfig{Owner: true}):
//     the owner is the one principal that passes the owner gate;
//   - owner_email set with NO verifier: nobody does. This is the guard for finding 4
//     of the B10 plan, core.auth.owner.required true whenever owner_email is set.
//
// TestReachRegisteredPath_OwnerSet and TestReachRegisteredPath_OwnerAllow carry the
// names of the F4-07-OWNER-SET and F4-07-OWNER-ALLOW rows of reachObligations.
// This file registers no discharge: reachDischarges is unexported and lives in
// package cutover, so the registration is the lead's (the Covers lists are
// ownerRouteKeys with "|non-owner admin refused" for OwnerSet and, for OwnerAllow,
// the same keys with "|owner lacking the requirement refused" and "|owner admitted").

// ownerRouteKeys are the nine owner routes of the registered path, as
// "METHOD pattern": the eight books routes the manifest guards with Guards.Owner,
// each recorded in R0's owner_set (R0 is main at f6698f9bf since F4-08), and core's
// own factory reset, which has no recording (ownerR2Only).
var ownerRouteKeys = []string{
	"GET /api/v1/admin/guest-service-tokens/plan",
	"POST /api/core/v1/system/factory-reset",
	"POST /api/v1/admin/guest-service-tokens/{userId}/reissue",
	"POST /api/v1/admin/guest-service-tokens/{userId}/revoke",
	"POST /api/v1/backup/restore",
	"POST /api/v1/users/merges/{jid}/resume",
	"POST /api/v1/users/merges/{jid}/undo",
	"POST /api/v1/users/{id}/merge-into/{target}/apply",
	"POST /api/v1/users/{id}/merge-into/{target}/preview",
}

// ownerBarKey is an owner route's key in the F4-07 obligation rows' spelling: the
// manifest's gin path, with ":name" for a parameter where the registered pattern has
// "{name}".
func ownerBarKey(key string) string { return ownerParam.ReplaceAllString(key, ":$1") }

var ownerParam = regexp.MustCompile(`\{([^}/]+)\}`)

// The discharge of F4-07-OWNER-SET: TestReachRegisteredPath_OwnerSet refuses a
// non-owner admin session on every owner route (and every other principal with it).
// The registration is read by the obligation check in package cutover.
func init() {
	cutover.RegisterReachDischarge("F4-07-OWNER-SET", "TestReachRegisteredPath_OwnerSet", func() []string {
		var out []string
		for _, k := range ownerRouteKeys {
			out = append(out, ownerBarKey(k)+"|non-owner admin refused")
		}
		return out
	})
}

// ownerRecordedRow names, for each books owner route, its row of verdicts.json's
// owner_set recording: the same route in HEAD's spelling. Every one has its own row
// since F4-08 re-recorded R0 at main; the reissue route was judged through revoke's
// row, and core's factory reset through the v0.223.0 release's
// POST /api/v1/system/factory-reset row, until then.
var ownerRecordedRow = map[string]string{
	"GET /api/v1/admin/guest-service-tokens/plan":              "GET /api/v1/admin/guest-service-tokens/plan",
	"POST /api/v1/admin/guest-service-tokens/{userId}/reissue": "POST /api/v1/admin/guest-service-tokens/:userId/reissue",
	"POST /api/v1/admin/guest-service-tokens/{userId}/revoke":  "POST /api/v1/admin/guest-service-tokens/:userId/revoke",
	"POST /api/v1/backup/restore":                              "POST /api/v1/backup/restore",
	"POST /api/v1/users/merges/{jid}/resume":                   "POST /api/v1/users/merges/:jid/resume",
	"POST /api/v1/users/merges/{jid}/undo":                     "POST /api/v1/users/merges/:jid/undo",
	"POST /api/v1/users/{id}/merge-into/{target}/apply":        "POST /api/v1/users/:id/merge-into/:target/apply",
	"POST /api/v1/users/{id}/merge-into/{target}/preview":      "POST /api/v1/users/:id/merge-into/:target/preview",
}

// ownerR2Only are the owner routes the flipped build serves and R0 (main at
// f6698f9bf) does not, with the decision that sets their letters in place of a
// recording. Core's factory reset is a deliberate addition versus main, NOT parity
// with R0: main runs the reset only offline (623da76d3 deleted the gin reset routes),
// and the flip serves it as core's staged reset under 05-rbac §6.2 (core:admin,
// Guards.Interactive, Guards.Owner). Its expected letters (coordinator ruling,
// 2026-11-22): every principal that is not the verified owner is refused, and the
// owner is admitted only through an interactive sign-in. The non-owner side is
// requireOwnerRefusal's sweep; the owner side is
// TestReachRegisteredPath_OwnerFactoryReset.
var ownerR2Only = map[string]string{
	"POST /api/core/v1/system/factory-reset": "05-rbac §6.2: core:admin, Interactive and Owner; a deliberate addition versus main, which serves no reset route",
}

// ownerRecording is the part of verdicts.json this file reads: the principals in
// order and R0's owner_set answers.
type ownerRecording struct {
	Principals []string `json:"principals"`
	OwnerSet   struct {
		OwnerEmail string `json:"owner_email"`
		Routes     []struct {
			Method  string `json:"method"`
			Path    string `json:"path"`
			Verdict string `json:"verdict"`
		} `json:"routes"`
	} `json:"owner_set"`
}

// expectedOwnerVerdict is the letter a non-owner principal must get on the owner
// route key with an owner set: R0's recorded letter for a books route, and D for an
// R2-only route, whose letters are the decision ownerR2Only names (never parity).
func (o *ownerRecording) expectedOwnerVerdict(t testing.TB, key, principal string) byte {
	t.Helper()
	if _, ok := ownerR2Only[key]; ok {
		return 'D'
	}
	twin, ok := ownerRecordedRow[key]
	if !ok {
		t.Fatalf("%s has neither a recorded row nor an R2-only decision", key)
	}
	idx := slices.Index(o.Principals, principal)
	if idx < 0 {
		t.Fatalf("verdicts.json lists no principal %q", principal)
	}
	for _, r := range o.OwnerSet.Routes {
		if r.Method+" "+r.Path == twin {
			if idx >= len(r.Verdict) {
				t.Fatalf("%s: the recorded verdict has %d letters", twin, len(r.Verdict))
			}
			return r.Verdict[idx]
		}
	}
	t.Fatalf("verdicts.json owner_set has no row %q", twin)
	return 0
}

func loadOwnerRecording(t testing.TB) *ownerRecording {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), reachRel, "verdicts.json"))
	if err != nil {
		t.Fatalf("the recorded R0 answers: %v", err)
	}
	var o ownerRecording
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("verdicts.json: %v", err)
	}
	if o.OwnerSet.OwnerEmail == "" || len(o.OwnerSet.Routes) != len(ownerRecordedRow) {
		t.Fatalf("verdicts.json owner_set: owner %q with %d routes, want an owner and %d routes", o.OwnerSet.OwnerEmail, len(o.OwnerSet.Routes), len(ownerRecordedRow))
	}
	return &o
}

// ownerRoutes are the registered routes that carry Guards.Owner, sorted. The count
// and the names are held to ownerRouteKeys, so a route that gains or loses the guard
// fails here before it changes a table.
func ownerRoutes(t testing.TB, h *Registered) []httpapi.RouteInfo {
	t.Helper()
	var out []httpapi.RouteInfo
	for _, ri := range h.Routes {
		if ri.Guards.Owner && !ri.OwnAuth {
			out = append(out, ri)
		}
	}
	slices.SortFunc(out, func(a, b httpapi.RouteInfo) int {
		return strings.Compare(a.Method+" "+a.Pattern, b.Method+" "+b.Pattern)
	})
	var keys []string
	for _, ri := range out {
		keys = append(keys, ri.Method+" "+ri.Pattern)
	}
	if !slices.Equal(keys, ownerRouteKeys) {
		t.Fatalf("the registered owner routes are\n  %s\nwant the %d of the plan\n  %s", strings.Join(keys, "\n  "), len(ownerRouteKeys), strings.Join(ownerRouteKeys, "\n  "))
	}
	return out
}

// ownerRefusalCode is the stage 13 code an owner route must answer a non-owner with,
// by what the principal is: the Interactive guard comes first (a key or the
// auth-off principal is not an interactive sign-in), then the owner assurance. An
// anonymous caller has no principal at all.
func ownerRefusalCode(p *Principal) string {
	switch p.Kind {
	case "session":
		return "owner_required"
	case "anonymous":
		return "authentication_required"
	default: // key, authoff
		return "interactive_required"
	}
}

// requireOwnerRefusal sweeps every principal of the boot except the owner over every
// owner route and fails on any that stage 13 does not refuse, with the code the
// principal's kind must get. It also holds the answer to the route's expected
// letter (expectedOwnerVerdict: R0's recording, or an R2-only route's decision): an
// admit where that letter refuses is a widening and is named as one. It returns the
// number of cells run.
func requireOwnerRefusal(t *testing.T, h *Registered, rec *ownerRecording) int {
	t.Helper()
	routes := ownerRoutes(t, h)
	cells := 0
	for _, ri := range routes {
		key := ri.Method + " " + ri.Pattern
		for _, p := range h.Principals {
			if p.Name == Owner {
				continue
			}
			cells++
			res, err := h.DoRoute(p, ri, nil)
			if err != nil {
				t.Errorf("%s as %s: %v (a judged route the harness could not see stage 13 on)", key, p.Name, err)
				continue
			}
			if want := rec.expectedOwnerVerdict(t, key, p.Recorded()); want == 'D' && res.Verdict() != 'D' {
				t.Errorf("WIDENING: %s as %s: the expected letter is D (%s), the flipped build answered %d (%s)", key, p.Name, ownerLetterSource(key), res.Status, res.excerpt())
				continue
			}
			if res.Stage13 != Stage13Refused || res.Code != ownerRefusalCode(p) {
				t.Errorf("%s as %s: %d %q (%s), want stage 13's refusal %q", key, p.Name, res.Status, res.Code, res.Stage13, ownerRefusalCode(p))
			}
		}
	}
	return cells
}

// ownerPrincipalCounts splits a boot's principals into the verdicts.json ones (the
// owner is not one) and the core-native keys beside them.
func ownerPrincipalCounts(h *Registered) (imported, core int) {
	for _, p := range h.Principals {
		switch {
		case p.Core:
			core++
		case p.Name != Owner:
			imported++
		}
	}
	return imported, core
}

// ownerEmailEnv is the environment key of owner_email under the flip.
const ownerEmailEnv = "ABO_BOOKS_IDENTITY_OWNER_EMAIL"

// ownerLetterSource says where an owner route's expected letter comes from, for a
// failure message.
func ownerLetterSource(key string) string {
	if why, ok := ownerR2Only[key]; ok {
		return "R2-only, " + why
	}
	return "R0's owner_set recording"
}

// TestReachRegisteredPath_OwnerSet: on every owner route, a non-owner is refused by
// stage 13, in each owner configuration. The nine routes are the plan's; every
// principal of the table is swept, the admin session among them (a non-owner admin
// is the case the row names), and each is held to its expected letter: R0's own
// recorded refusal on the eight books routes, the ruled refusal on core's factory
// reset.
func TestReachRegisteredPath_OwnerSet(t *testing.T) {
	rec := loadOwnerRecording(t)

	t.Run("owner set with the Access verifier", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{Owner: true})
		n := requireOwnerRefusal(t, h, rec)
		imported, core := ownerPrincipalCounts(h)
		if want := 9 * (len(h.Principals) - 1); n != want || imported != 36 || core != 18 || len(h.Principals) != imported+core+1 {
			t.Fatalf("%d cells over %d principals (%d of verdicts.json, %d core keys, and the owner), want %d over 36 + 18 + 1", n, len(h.Principals), imported, core, want)
		}
		// The admin session, named: the case the row exists for.
		admin := h.Principal("session:admin")
		for _, ri := range ownerRoutes(t, h) {
			res, err := h.DoRoute(admin, ri, nil)
			if err != nil || res.Code != "owner_required" || res.Status != http.StatusForbidden {
				t.Errorf("admin session on %s %s: %d %q %v, want 403 owner_required", ri.Method, ri.Pattern, res.Status, res.Code, err)
			}
		}
	})

	// The guard for finding 4. owner_email is set and no Access verifier exists, so
	// nobody can be the owner and HEAD refuses every principal (R0: 37 of 37). The
	// flipped build must not fall back to "an interactive admin": that is core's
	// fallback for a deployment with no owner at all.
	t.Run("owner set with no verifier", func(t *testing.T) {
		// registeredConfig has no switch for owner_email without the verifier: this is
		// the one place the owner's e-mail is set outside Owner.
		t.Setenv(ownerEmailEnv, ownerEmail)
		h := bootRegistered(t, registeredConfig{})
		if h.byName[Owner] != nil {
			t.Fatal("this boot has an owner principal: it is not the no-verifier configuration")
		}
		n := requireOwnerRefusal(t, h, rec)
		imported, core := ownerPrincipalCounts(h)
		if want := 9 * len(h.Principals); n != want || imported != 36 || core != 18 || len(h.Principals) != imported+core {
			t.Fatalf("%d cells over %d principals (%d of verdicts.json, %d core keys), want %d over 36 + 18", n, len(h.Principals), imported, core, want)
		}
	})

	// The 37th principal of R0's table: the auth-off one, which R0 also refused on
	// the eight books routes with the owner set and no verifier (core's factory
	// reset: the same refusal, by its ruling).
	t.Run("owner set with no verifier, auth off", func(t *testing.T) {
		t.Setenv(ownerEmailEnv, ownerEmail)
		h := bootRegistered(t, registeredConfig{AuthOff: true})
		off := h.Principal(Authoff)
		routes := ownerRoutes(t, h)
		for _, ri := range routes {
			key := ri.Method + " " + ri.Pattern
			res, err := h.DoRoute(off, ri, nil)
			if err != nil {
				// An auth-off boot has no anonymous refusal for the harness to find.
				t.Errorf("%s as authoff: %v", key, err)
				continue
			}
			if want := rec.expectedOwnerVerdict(t, key, Authoff); want == 'D' && res.Verdict() != 'D' {
				t.Errorf("WIDENING: %s as authoff: the expected letter is D (%s), the flipped build answered %d (%s)", key, ownerLetterSource(key), res.Status, res.excerpt())
				continue
			}
			// The auth-off boot binds core's auth-disabled principal, which is not an
			// interactive sign-in: the Interactive guard refuses it before the owner
			// check, 403, a refusal as R0's recording is (the verdict letter is D).
			if res.Stage13 != Stage13Refused || res.Code != "interactive_required" {
				t.Errorf("%s as authoff: %d %q (%s), want stage 13's interactive_required", key, res.Status, res.Code, res.Stage13)
			}
		}
		if len(routes) != 9 {
			t.Fatalf("%d owner routes, want 9", len(routes))
		}
	})
}

// ownerAccount is the core account the Access sign-in of the owner lands on.
func ownerAccount(t *testing.T, h *Registered) (id string, roles []string) {
	t.Helper()
	res := h.MustDo(t, h.Principal(Owner), http.MethodGet, "/api/core/v1/me", nil)
	var me struct {
		User struct {
			ID    string   `json:"id"`
			Email string   `json:"email"`
			Roles []string `json:"roles"`
		} `json:"user"`
		Method        string `json:"method"`
		OwnerVerified bool   `json:"owner_verified"`
	}
	if err := json.Unmarshal(res.Body, &me); err != nil || res.Status != http.StatusOK || me.User.ID == "" {
		t.Fatalf("owner GET /api/core/v1/me: %d %s", res.Status, res.excerpt())
	}
	if me.Method != "ext:cfaccess" || !me.OwnerVerified || !strings.EqualFold(me.User.Email, ownerEmail) {
		t.Fatalf("the owner's sign-in is %q verified=%v as %q, want ext:cfaccess, owner-verified, %s", me.Method, me.OwnerVerified, me.User.Email, ownerEmail)
	}
	return me.User.ID, me.User.Roles
}

// accessUsers is the admin's list of core accounts: the user rows as
// "username <email> [roles]" lines and the count.
func accessUsers(t *testing.T, h *Registered) (lines []string) {
	t.Helper()
	res := h.MustDo(t, h.Principal("session:admin"), http.MethodGet, "/api/core/v1/users?limit=500", nil)
	var page struct {
		Items []struct {
			Username string   `json:"username"`
			Email    string   `json:"email"`
			Roles    []string `json:"roles"`
			Status   string   `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Body, &page); err != nil || res.Status != http.StatusOK || len(page.Items) == 0 {
		t.Fatalf("GET /api/core/v1/users: %d %s", res.Status, res.excerpt())
	}
	for _, u := range page.Items {
		lines = append(lines, fmt.Sprintf("%s <%s> %v %s", u.Username, u.Email, u.Roles, u.Status))
	}
	slices.Sort(lines)
	return lines
}

// accessGet is a GET of path carrying the given Access assertion and no other
// credential.
func accessGet(h *Registered, path, assertion string) Result {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Cf-Access-Jwt-Assertion", assertion)
	return h.Raw(req)
}

// TestReachRegisteredPath_OwnerAccessPolicy: the Access admission policy admits the
// owner and nobody else. A signed assertion for an address that is not on the
// allow-list is no credential at all (the request is anonymous and the route
// refuses it) and creates no account, as at HEAD, where IsEmailAllowed matched an
// exact address.
func TestReachRegisteredPath_OwnerAccessPolicy(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true})

	t.Run("the owner is admitted", func(t *testing.T) {
		id, _ := ownerAccount(t, h)
		if id == "" {
			t.Fatal("no owner account")
		}
	})

	t.Run("an address off the allow-list is no credential and creates nothing", func(t *testing.T) {
		before := accessUsers(t, h)
		const stranger = "harvey.dent@example.invalid"
		jwt := h.access.assertion(t, "r2reach-stranger-sub", stranger)
		res := accessGet(h, "/api/core/v1/me", jwt)
		if res.Status != http.StatusUnauthorized || res.Code != "authentication_required" {
			t.Fatalf("a verified Access identity off the allow-list: %d %q %s, want 401 authentication_required", res.Status, res.Code, res.excerpt())
		}
		for _, ri := range ownerRoutes(t, h) {
			if ri.Method != http.MethodGet {
				continue
			}
			r := accessGet(h, probePath(ri.Pattern), jwt)
			if r.Verdict() != 'D' {
				t.Errorf("GET %s with an off-list assertion: %d, want a refusal", ri.Pattern, r.Status)
			}
		}
		after := accessUsers(t, h)
		if !slices.Equal(before, after) {
			t.Fatalf("an off-list Access sign-in changed the accounts:\nbefore %v\nafter  %v", before, after)
		}
		if slices.ContainsFunc(after, func(l string) bool { return strings.Contains(l, stranger) }) {
			t.Fatalf("an account holds %s", stranger)
		}
	})

	t.Run("a header that does not verify never falls through to a weaker method", func(t *testing.T) {
		// A forged assertion beside a valid session cookie: the presented invalid
		// credential is refused, it does not become the session's request.
		sess := h.Principal("session:admin")
		req := httptest.NewRequest(http.MethodGet, "/api/core/v1/me", nil)
		req.AddCookie(sess.cookie)
		req.Header.Set("Cf-Access-Jwt-Assertion", "not-a-jwt")
		res := h.Raw(req)
		if res.Status != http.StatusUnauthorized {
			t.Fatalf("an unverifiable Access header beside a session: %d %s, want 401", res.Status, res.excerpt())
		}
	})
}

// setRoles replaces a user's roles through core's admin API, signed in as the
// admin session.
func setRoles(t *testing.T, h *Registered, userID string, roles ...string) {
	t.Helper()
	res := h.MustDo(t, h.Principal("session:admin"), http.MethodPut, "/api/core/v1/users/"+userID+"/roles", map[string]any{"roles": roles})
	if res.Status != http.StatusOK {
		t.Fatalf("PUT roles %v: %d %s", roles, res.Status, res.excerpt())
	}
}

// coreUser is core's account as the admin list shows it.
type coreUser struct {
	ID         string   `json:"id"`
	Username   string   `json:"username"`
	Email      string   `json:"email"`
	Roles      []string `json:"roles"`
	Status     string   `json:"status"`
	MergedInto string   `json:"merged_into_user_id"`
}

// coreAccounts is every core account by ID, as the admin session lists them.
func coreAccounts(t *testing.T, h *Registered) map[string]coreUser {
	t.Helper()
	res := h.MustDo(t, h.Principal("session:admin"), http.MethodGet, "/api/core/v1/users?limit=500", nil)
	var page struct {
		Items []coreUser `json:"items"`
	}
	if err := json.Unmarshal(res.Body, &page); err != nil || res.Status != http.StatusOK || len(page.Items) == 0 {
		t.Fatalf("GET /api/core/v1/users: %d %s", res.Status, res.excerpt())
	}
	out := map[string]coreUser{}
	for _, u := range page.Items {
		out[u.ID] = u
	}
	return out
}

// accessMe is GET /api/core/v1/me under an Access assertion and no other credential:
// the status, the account it landed on (empty when refused), the method, and whether
// the owner proof is set.
func accessMe(t *testing.T, h *Registered, assertion string) (status int, user coreUser, method string, ownerVerified bool, res Result) {
	t.Helper()
	res = accessGet(h, "/api/core/v1/me", assertion)
	var me struct {
		User          coreUser `json:"user"`
		Method        string   `json:"method"`
		OwnerVerified bool     `json:"owner_verified"`
	}
	if res.Status == http.StatusOK {
		if err := json.Unmarshal(res.Body, &me); err != nil {
			t.Fatalf("GET /api/core/v1/me: %v: %s", err, res.excerpt())
		}
	}
	return res.Status, me.User, me.Method, me.OwnerVerified, res
}

// The same-address install (F4-08 B10e1r, after B10e1c's composed-harness version):
// two HEAD administrators hold the owner's address, both proven, and only one is
// linked to Cloudflare Access, by the opaque sub the Access JWT carries.
const (
	sameEmailLinked = "lucius.fox" // the corpus admin; gets the address and the Access link
	sameEmailTwin   = "batman"     // a second admin with the same address and no link
	// sameEmailTwinID and sameEmailLinkID are ULIDs of the seeded rows.
	sameEmailTwinID = "01R2SAMEMAIL00000000000T01"
	sameEmailLinkID = "01R2SAMEMAIL00000000000K01"
)

// seedSameEmail writes the shared-address rows into the HEAD corpus, so M2 and M3
// import them like any rows a HEAD build wrote. HEAD's own CreateUser refuses a
// second proven holder of an address, which is the state this fixture needs, so the
// rows are written raw: the corpus admin gets the owner's address (proof "setup") and
// the Access identity (provider cfaccess, subject ownerSub), and a clone of its row
// becomes the second administrator (proof "admin", no password, no link).
func seedSameEmail(ctx context.Context, lr storage.LegacyRange) error {
	uid, err := lr.Get(ctx, []byte("idx:user:username:"+sameEmailLinked))
	if err != nil || len(uid) == 0 {
		return fmt.Errorf("the corpus has no user %q: %v", sameEmailLinked, err)
	}
	raw, err := lr.Get(ctx, []byte("u:"+string(uid)))
	if err != nil || len(raw) == 0 {
		return fmt.Errorf("no row u:%s: %v", uid, err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return err
	}
	set := func(k string, v []byte) error {
		if err := lr.Set(ctx, []byte(k), v); err != nil {
			return fmt.Errorf("seed %s: %w", k, err)
		}
		return nil
	}
	row["email"], row["email_source"] = ownerEmail, "setup"
	linked, _ := json.Marshal(row)
	row["id"], row["username"], row["email_source"] = sameEmailTwinID, sameEmailTwin, "admin"
	row["password_hash"], row["password_hash_algo"] = "", "oauth"
	twin, _ := json.Marshal(row)
	ident, _ := json.Marshal(map[string]any{
		"id": sameEmailLinkID, "provider": "cfaccess", "subject": ownerSub, "email": ownerEmail,
		"user_id": string(uid), "created_at": time.Now().UTC().Format(time.RFC3339),
	})
	for _, kv := range []struct {
		k string
		v []byte
	}{
		{"u:" + string(uid), linked},
		{"u:" + sameEmailTwinID, twin},
		{"idx:user:username:" + sameEmailTwin, []byte(sameEmailTwinID)},
		{"oauthid:" + sameEmailLinkID, ident},
		{"idx:oauth:sub:cfaccess:" + ownerSub, []byte(sameEmailLinkID)},
		{"idx:oauth:user:" + string(uid) + ":" + sameEmailLinkID, []byte("1")},
	} {
		if err := set(kv.k, kv.v); err != nil {
			return err
		}
	}
	return nil
}

// TestReachRegisteredPath_OwnerSameEmail: two administrators hold the owner's
// address and one is linked to Access. The Access sign-in lands on the linked one;
// neither account is merged, displaced, re-linked or renamed; and the two other ways
// an identity with that address could arrive, the address itself as the subject and
// a subject the install has never seen, are refused and create nothing. The owner
// cell of the same boot proves the fixture links at all.
func TestReachRegisteredPath_OwnerSameEmail(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true, Seed: seedSameEmail})
	_, adminMe := h.principalMe(t, "session:admin")
	linkedID := adminMe.ID
	before := coreAccounts(t, h)

	holders := func(users map[string]coreUser) (ids []string) {
		for id, u := range users {
			if strings.EqualFold(u.Email, ownerEmail) {
				ids = append(ids, id)
			}
		}
		slices.Sort(ids)
		return ids
	}
	if got, want := holders(before), []string{linkedID, sameEmailTwinID}; !slices.Equal(got, want) {
		t.Fatalf("the accounts holding %s are %v, want the two seeded administrators %v", ownerEmail, got, want)
	}
	for _, id := range []string{linkedID, sameEmailTwinID} {
		if u := before[id]; !slices.Equal(u.Roles, []string{"admin"}) || u.Status != "active" || u.MergedInto != "" {
			t.Fatalf("account %s (%s) before any sign-in: roles %v, status %q, merged into %q, want an active unmerged admin", id, u.Username, u.Roles, u.Status, u.MergedInto)
		}
	}

	t.Run("the Access sign-in lands on the linked administrator", func(t *testing.T) {
		res := h.MustDo(t, h.Principal(Owner), http.MethodGet, "/api/core/v1/me", nil)
		var me struct {
			User          coreUser `json:"user"`
			Method        string   `json:"method"`
			OwnerVerified bool     `json:"owner_verified"`
		}
		if err := json.Unmarshal(res.Body, &me); err != nil || res.Status != http.StatusOK {
			t.Fatalf("owner GET /api/core/v1/me: %d %s", res.Status, res.excerpt())
		}
		if me.User.ID != linkedID || me.Method != "ext:cfaccess" || !me.OwnerVerified {
			t.Fatalf("the Access sign-in is %s via %q owner-verified=%v, want the linked admin %s via ext:cfaccess owner-verified", me.User.ID, me.Method, me.OwnerVerified, linkedID)
		}
	})

	t.Run("the address as the subject and a fresh subject are refused", func(t *testing.T) {
		for name, jwt := range map[string]string{
			"the address as the subject (the empty-sub fallback's form)": h.access.assertion(t, ownerEmail, ownerEmail),
			"a subject the install has never seen":                       h.access.assertion(t, "r2reach-fresh-sub", ownerEmail),
		} {
			status, user, _, _, res := accessMe(t, h, jwt)
			if status != http.StatusUnauthorized || res.Code != "authentication_required" || user.ID != "" {
				t.Errorf("%s: %d %q (user %q) %s, want 401 authentication_required and no account", name, status, res.Code, user.ID, res.excerpt())
			}
		}
	})

	t.Run("neither administrator is merged, displaced or created beside", func(t *testing.T) {
		after := coreAccounts(t, h)
		if len(after) != len(before) {
			t.Fatalf("%d accounts after the sign-ins, %d before", len(after), len(before))
		}
		for id, b := range before {
			a, ok := after[id]
			if !ok || a.Username != b.Username || a.Email != b.Email || !slices.Equal(a.Roles, b.Roles) || a.Status != b.Status || a.MergedInto != b.MergedInto {
				t.Errorf("account %s changed across the sign-ins: before %+v, after %+v", id, b, a)
			}
		}
	})
}

// principalMe is the account a principal's own session or key is bound to.
func (h *Registered) principalMe(t *testing.T, name string) (Result, coreUser) {
	t.Helper()
	res := h.MustDo(t, h.Principal(name), http.MethodGet, "/api/core/v1/me", nil)
	var me struct {
		User coreUser `json:"user"`
	}
	if err := json.Unmarshal(res.Body, &me); err != nil || res.Status != http.StatusOK || me.User.ID == "" {
		t.Fatalf("%s GET /api/core/v1/me: %d %s", name, res.Status, res.excerpt())
	}
	return res, me.User
}

// A second address on the Access allow-list: a person Access admits who is not the
// owner. Core's resolver lets the allow-list in, so it signs in to a new viewer.
const accessGuest = "alfred.pennyworth@example.invalid"

// TestReachRegisteredPath_OwnerAccessNonOwner: an Access identity that is on the
// allow-list but is not the owner e-mail is admitted as a person and never as the
// owner, even with the admin role. This is the cell mutant (b) of the owner policy
// (the extension minting the owner assurance for any verified identity) is killed by.
func TestReachRegisteredPath_OwnerAccessNonOwner(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true, AccessAllowed: []string{accessGuest}})
	jwt := h.access.assertion(t, "r2reach-guest-sub", accessGuest)

	status, user, method, ownerVerified, res := accessMe(t, h, jwt)
	if status != http.StatusOK || method != "ext:cfaccess" || user.ID == "" || !slices.Equal(user.Roles, []string{"viewer"}) {
		t.Fatalf("an allow-listed non-owner address: %d via %q as %+v %s, want 200 via ext:cfaccess as a new viewer", status, method, user, res.excerpt())
	}
	if ownerVerified {
		t.Fatal("an allow-listed address that is not the owner is owner-verified: the owner assurance is not the owner e-mail's alone")
	}

	// Given the admin role it is still not the owner: the e-mail decides, not the role.
	setRoles(t, h, user.ID, "admin")
	status, user, _, ownerVerified, res = accessMe(t, h, jwt)
	if status != http.StatusOK || !slices.Equal(user.Roles, []string{"admin"}) {
		t.Fatalf("the promoted guest: %d as %+v %s, want 200 as an admin", status, user, res.excerpt())
	}
	if ownerVerified {
		t.Fatal("the promoted non-owner admin is owner-verified")
	}
	// Every owner route refuses it. Today the Interactive guard answers first (B10y:
	// stage 13 does not yet read the extension's MethodSpec), after B10y it is
	// owner_required; either way the refusal is stage 13's and it is a 403.
	refused := 0
	for _, ri := range ownerRoutes(t, h) {
		path := probePath(ri.Pattern)
		req := httptest.NewRequest(ri.Method, path, nil)
		req.Header.Set("Cf-Access-Jwt-Assertion", jwt)
		r := h.Raw(req)
		if r.Stage13 != Stage13Refused || r.Status != http.StatusForbidden || (r.Code != "owner_required" && r.Code != "interactive_required") {
			t.Errorf("%s %s as an allow-listed admin who is not the owner: %d %q (%s) %s, want stage 13's 403", ri.Method, ri.Pattern, r.Status, r.Code, r.Stage13, r.excerpt())
			continue
		}
		refused++
	}
	if refused != 9 {
		t.Fatalf("%d of 9 owner routes refused the non-owner admin", refused)
	}
}

// ownerFactoryReset is core's factory reset, the R2-only owner route (ownerR2Only).
const ownerFactoryReset = "POST /api/core/v1/system/factory-reset"

// TestReachRegisteredPath_OwnerFactoryReset pins the owner side of core's factory
// reset, an R2-only owner route whose letters are a ruling (ownerR2Only: 05-rbac
// §6.2, a deliberate addition versus main, not parity with R0). In the owner boot:
//
//   - the route is registered with core:admin, Guards.Interactive and
//     Guards.Owner (the route table is what stage 13 judges);
//   - the owner, signed in through Access (an interactive method) and holding
//     core:admin, is admitted; a body without the "RESET" confirmation is then
//     refused by the handler (422 confirm_required), so nothing is staged;
//   - the same owner through an API key of its own account (not an interactive
//     sign-in) is refused 403 interactive_required;
//   - the owner without core:admin is refused 403 permission_denied.
//
// The two refusals cannot say which layer wrote them: the handler's guard repeats
// stage 13's judgement with the same codes, and Result.Stage13 is read from the
// code. That is why the registration is pinned on its own.
//
// The non-owner side is TestReachRegisteredPath_OwnerSet's sweep.
func TestReachRegisteredPath_OwnerFactoryReset(t *testing.T) {
	h := bootRegistered(t, registeredConfig{Owner: true})
	var reset httpapi.RouteInfo
	for _, ri := range ownerRoutes(t, h) {
		if ri.Method+" "+ri.Pattern == ownerFactoryReset {
			reset = ri
		}
	}
	if reset.Pattern == "" {
		t.Fatalf("%s is not an owner route of the registered build", ownerFactoryReset)
	}
	// The route table is what stage 13 judges. The handler repeats every check and
	// writes the same problem codes, so over HTTP a route that dropped core:admin
	// would look the same as one that kept it: the declaration is pinned here.
	if want := rbac.Need(rbac.CoreAdmin).String(); reset.Perm.String() != want || !reset.Guards.Interactive || !reset.Guards.Owner {
		t.Fatalf("%s is registered with Perm %s, Guards %+v; 05-rbac §6.2 wants %s with Interactive and Owner", ownerFactoryReset, reset.Perm, reset.Guards, want)
	}
	id, roles := ownerAccount(t, h)
	if !slices.Contains(roles, "admin") {
		setRoles(t, h, id, "admin")
	}

	// No "confirm": the handler refuses before anything is staged.
	unconfirmed := map[string]any{"kind": "factory"}

	t.Run("the owner through Access is admitted", func(t *testing.T) {
		res, err := h.DoRoute(h.Principal(Owner), reset, unconfirmed)
		if err != nil {
			t.Fatalf("owner on %s: %v", ownerFactoryReset, err)
		}
		if res.Stage13 == Stage13Refused || res.Verdict() != 'A' {
			t.Fatalf("WIDENING the wrong way: the verified owner with core:admin on %s: %d %q (%s) %s, want it admitted", ownerFactoryReset, res.Status, res.Code, res.Stage13, res.excerpt())
		}
		// res.Code holds stage 13's codes only; the handler's is the problem type's.
		if code := problemCode(res); res.Status != http.StatusUnprocessableEntity || code != "confirm_required" {
			t.Fatalf("owner on %s without the confirmation: %d %q %s, want the handler's 422 confirm_required (nothing staged)", ownerFactoryReset, res.Status, code, res.excerpt())
		}
	})

	t.Run("the owner through a key is refused, interactive_required", func(t *testing.T) {
		var made struct {
			Key string `json:"key"`
		}
		h.coreJSON(h.Principal("session:admin"), http.MethodPost, "/api/core/v1/users/"+id+"/api-keys",
			map[string]any{"name": "owner-reset-key", "scopes": []string{"core:admin"}, "expires_in": "1h"}, &made)
		if made.Key == "" {
			t.Fatal("core minted no key for the owner's account")
		}
		key := &Principal{Name: "owner-key", Kind: "key", User: id, bearer: made.Key}
		res, err := h.DoRoute(key, reset, unconfirmed)
		if err != nil {
			t.Fatalf("owner key on %s: %v", ownerFactoryReset, err)
		}
		if res.Stage13 != Stage13Refused || res.Status != http.StatusForbidden || res.Code != "interactive_required" {
			t.Fatalf("WIDENING: the owner's own key on %s: %d %q (%s) %s, want 403 interactive_required", ownerFactoryReset, res.Status, res.Code, res.Stage13, res.excerpt())
		}
	})

	t.Run("the owner without core:admin is refused, permission_denied", func(t *testing.T) {
		setRoles(t, h, id, "viewer")
		res, err := h.DoRoute(h.Principal(Owner), reset, unconfirmed)
		if err != nil {
			t.Fatalf("owner on %s: %v", ownerFactoryReset, err)
		}
		if res.Stage13 != Stage13Refused || res.Status != http.StatusForbidden || res.Code != "permission_denied" {
			t.Fatalf("WIDENING: the owner as a viewer on %s: %d %q (%s) %s, want 403 permission_denied", ownerFactoryReset, res.Status, res.Code, res.Stage13, res.excerpt())
		}
	})
}
