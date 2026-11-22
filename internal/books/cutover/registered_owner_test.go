// file: internal/books/cutover/registered_owner_test.go
// version: 0.1.0
// guid: 9d4e7b32-5a18-4c6f-b2e0-71f3a8c95d46
// last-edited: 2026-11-22

package cutover_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/httpapi"
)

// The owner cells through the registered path (F4-08 B10e1r).
//
// HEAD gates nine routes on the owner: a caller passes only as the Cloudflare
// Access-verified owner_email, and with owner_email set and no Access verifier
// nobody does (R0's owner_set recording: 37 of 37 principals refused on all nine).
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
// "METHOD pattern": the eight books routes the manifest guards with Guards.Owner
// (seven of R0's nine recorded ones and the reissue route, which is newer than R0)
// and core's own factory reset, which replaces R0's two reset routes.
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

// ownerRecordedTwin names, for each owner route, the row of verdicts.json's
// owner_set recording that stands for it: the same route in HEAD's spelling, or its
// declared twin (reachOwnerTwins: reissue is revoke's, and core's factory reset
// replaces R0's, which is R0-only).
var ownerRecordedTwin = map[string]string{
	"GET /api/v1/admin/guest-service-tokens/plan":              "GET /api/v1/admin/guest-service-tokens/plan",
	"POST /api/core/v1/system/factory-reset":                   "POST /api/v1/system/factory-reset",
	"POST /api/v1/admin/guest-service-tokens/{userId}/reissue": "POST /api/v1/admin/guest-service-tokens/:userId/revoke",
	"POST /api/v1/admin/guest-service-tokens/{userId}/revoke":  "POST /api/v1/admin/guest-service-tokens/:userId/revoke",
	"POST /api/v1/backup/restore":                              "POST /api/v1/backup/restore",
	"POST /api/v1/users/merges/{jid}/resume":                   "POST /api/v1/users/merges/:jid/resume",
	"POST /api/v1/users/merges/{jid}/undo":                     "POST /api/v1/users/merges/:jid/undo",
	"POST /api/v1/users/{id}/merge-into/{target}/apply":        "POST /api/v1/users/:id/merge-into/:target/apply",
	"POST /api/v1/users/{id}/merge-into/{target}/preview":      "POST /api/v1/users/:id/merge-into/:target/preview",
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

// headOwnerVerdict is HEAD's recorded letter for principal on the owner route key.
func (o *ownerRecording) headOwnerVerdict(t testing.TB, key, principal string) byte {
	t.Helper()
	twin, ok := ownerRecordedTwin[key]
	if !ok {
		t.Fatalf("%s has no recorded twin", key)
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
	if o.OwnerSet.OwnerEmail == "" || len(o.OwnerSet.Routes) != 9 {
		t.Fatalf("verdicts.json owner_set: owner %q with %d routes, want an owner and 9 routes", o.OwnerSet.OwnerEmail, len(o.OwnerSet.Routes))
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
// principal's kind must get. It also holds the answer to R0's recorded letter for
// the route: an admit where HEAD refused is a widening and is named as one. It
// returns the number of cells run.
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
			if head := rec.headOwnerVerdict(t, key, p.Name); head == 'D' && res.Verdict() != 'D' {
				t.Errorf("WIDENING: %s as %s: HEAD refused, the flipped build answered %d (%s)", key, p.Name, res.Status, res.excerpt())
				continue
			}
			if res.Stage13 != Stage13Refused || res.Code != ownerRefusalCode(p) {
				t.Errorf("%s as %s: %d %q (%s), want stage 13's refusal %q", key, p.Name, res.Status, res.Code, res.Stage13, ownerRefusalCode(p))
			}
		}
	}
	return cells
}

// ownerEmailEnv is the environment key of owner_email under the flip.
const ownerEmailEnv = "ABO_BOOKS_IDENTITY_OWNER_EMAIL"

// TestReachRegisteredPath_OwnerSet: on every owner route, a non-owner is refused by
// stage 13, in each owner configuration. The nine routes are the plan's; every
// principal of the table is swept, the admin session among them (a non-owner admin
// is the case the row names), and each is held to R0's own recorded refusal.
func TestReachRegisteredPath_OwnerSet(t *testing.T) {
	rec := loadOwnerRecording(t)

	t.Run("owner set with the Access verifier", func(t *testing.T) {
		h := bootRegistered(t, registeredConfig{Owner: true})
		n := requireOwnerRefusal(t, h, rec)
		if want := 9 * (len(h.Principals) - 1); n != want || len(h.Principals) != 37 {
			t.Fatalf("%d cells over %d principals, want %d over 37 (36 of the table and the owner)", n, len(h.Principals), want)
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
		t.Setenv(ownerEmailEnv, ownerEmail)
		h := bootRegistered(t, registeredConfig{})
		if h.byName[Owner] != nil {
			t.Fatal("this boot has an owner principal: it is not the no-verifier configuration")
		}
		n := requireOwnerRefusal(t, h, rec)
		if want := 9 * len(h.Principals); n != want || len(h.Principals) != 36 {
			t.Fatalf("%d cells over %d principals, want %d over 36", n, len(h.Principals), want)
		}
	})

	// The 37th principal of R0's table: the auth-off one, which R0 also refused on
	// all nine routes with the owner set and no verifier.
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
			if head := rec.headOwnerVerdict(t, key, Authoff); head == 'D' && res.Verdict() != 'D' {
				t.Errorf("WIDENING: %s as authoff: HEAD refused, the flipped build answered %d (%s)", key, res.Status, res.excerpt())
				continue
			}
			// With an owner set core binds no auth-disabled principal at all, so the
			// caller is anonymous to stage 13: 401, a refusal, as R0's recording is
			// (401 on most routes, 403 on the rest; the verdict letter is D on all).
			if res.Stage13 != Stage13Refused || res.Code != "authentication_required" {
				t.Errorf("%s as authoff: %d %q (%s), want stage 13's authentication_required", key, res.Status, res.Code, res.Stage13)
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
