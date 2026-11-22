// file: internal/books/identity_owner_routes_boot_test.go
// version: 0.1.0
// guid: 96e43590-a010-41c0-a434-6eabd7f2a695
// last-edited: 2026-11-22

package books

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/app"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Books' owner routes under core identity (F4-08 B10y, (ii) and item 6). HEAD
// registered backup restore, the four user-merge routes and the three
// guest-service-token routes through ownerCredRoute (origin/main
// owner_routes.go): credGuard (internal/auth Method.MayChangeCredentials:
// session, session_delegated, cf_access), the route's permission, and
// ownerGateWhenOwnerSet, which passed through while owner_email was unset. So
// with no owner a session or a delegated session holding the permission was
// admitted, a key and auth-off were refused; with an owner set only the owner
// passed. The manifest rows carry Guards.Owner with LegacyRoute.OwnerWhenSet;
// core's own owner routes keep 05-authn §4.8 (core:admin while no owner is
// required).

// ownerAddr hands every request its own address: the auth routes' per-source
// buckets answer 429 to a run of requests from one address.
var ownerAddr atomic.Int64

func ownerReq(t *testing.T, r *runningApp, method, path string, body any, ck *http.Cookie, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	n := ownerAddr.Add(1)
	req.RemoteAddr = fmt.Sprintf("192.0.2.%d:%d", 100+n%150, 42000+n)
	if ck != nil {
		req.AddCookie(ck)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	require.NotEqual(t, http.StatusTooManyRequests, rec.Code, "%s %s was rate limited; the cell proves nothing", method, path)
	return rec
}

const ownerPass = "correct-horse-gotham-battery-0451"

// ownerInvitee is invited with role, accepts through core's route (a delegated
// session) and signs in with the password it set (a session).
func ownerInvitee(t *testing.T, r *runningApp, admin *coreAdmin, username, role string) (delegated, session *http.Cookie) {
	t.Helper()
	rec := ownerReq(t, r, http.MethodPost, "/api/core/v1/users/invites", map[string]string{"username": username, "role": role}, admin.cookie, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Link string `json:"link"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created), rec.Body.String())
	_, token, ok := strings.Cut(created.Link, "#token=")
	require.True(t, ok, "invite link has no token: %q", created.Link)
	acc := ownerReq(t, r, http.MethodPost, "/api/core/v1/auth/invites/accept", map[string]string{"token": token, "password": ownerPass}, nil, "")
	require.Equal(t, http.StatusOK, acc.Code, acc.Body.String())
	in := ownerReq(t, r, http.MethodPost, "/api/core/v1/auth/login", map[string]string{"username": username, "password": ownerPass}, nil, "")
	require.Equal(t, http.StatusOK, in.Code, in.Body.String())
	return sessionCookie(t, acc), sessionCookie(t, in)
}

type ownerCell struct {
	name, method, path string
	ck                 *http.Cookie
	key                string
	// want is the exact status, or 0 for "past authorization": anything
	// but 401 and 403.
	want     int
	contains string
}

func runOwnerCells(t *testing.T, r *runningApp, state string, cells []ownerCell) {
	t.Helper()
	for _, c := range cells {
		var body any
		if c.method != http.MethodGet {
			body = map[string]any{}
		}
		rec := ownerReq(t, r, c.method, c.path, body, c.ck, c.key)
		t.Logf("cell %s %s (%s, %s): %d", c.method, c.path, c.name, state, rec.Code)
		switch {
		case c.want == 0 && (rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden):
			t.Errorf("%s, %s: %d %s; HEAD admitted it", state, c.name, rec.Code, credExcerpt(rec))
		case c.want != 0 && rec.Code != c.want:
			t.Errorf("%s, %s: %d %s; want %d", state, c.name, rec.Code, credExcerpt(rec), c.want)
		case c.contains != "" && !strings.Contains(rec.Body.String(), c.contains):
			t.Errorf("%s, %s: %d %s; want it to contain %q", state, c.name, rec.Code, credExcerpt(rec), c.contains)
		}
	}
}

const (
	ownerRestore  = "/api/v1/backup/restore"
	ownerMerge    = "/api/v1/users/u-joker/merge-into/u-harley/preview"
	ownerGuestKey = "/api/v1/admin/guest-service-tokens/plan"
	ownerCoreKill = "/api/core/v1/system/factory-reset"

	mergeNeedsAdmin = "merging accounts needs the admin role"
)

// TestOwnerRoutesBoot_OwnerUnsetKeepsTheOrdinaryGuardsOwnerSetDemandsTheOwner
// is the whole owner-route table in one boot: owner_email unset, then set.
func TestOwnerRoutesBoot_OwnerUnsetKeepsTheOrdinaryGuardsOwnerSetDemandsTheOwner(t *testing.T) {
	prev := config.Snapshot().OwnerEmail
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.OwnerEmail = prev }) })
	r := bootAccess(t, nil, func(c *config.Config) { c.OwnerEmail = "" })
	admin := newCoreAdmin(t, r)

	// custom.gcpd: every scope of HEAD's settings.manage and users.manage
	// atoms, no core:admin (the golden table's session:custom-all, narrowed to
	// what the routes ask).
	rec := ownerReq(t, r, http.MethodPost, "/api/core/v1/roles", map[string]any{
		"name": "custom.gcpd", "title": "GCPD", "description": "Gotham police: settings and users, never admin",
		"permissions": []string{"core:settings.manage", "books:settings.manage", "core:tasks.manage", "core:system.view",
			"core:system.manage", "core:library.manage", "core:library.browse", "core:integrations.manage",
			"core:users.manage", "core:roles.view"},
	}, admin.cookie, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	gordonDelegated, gordon := ownerInvitee(t, r, admin, "jim-gordon", "custom.gcpd")
	luciusDelegated, _ := ownerInvitee(t, r, admin, "lucius-fox", "admin")
	key, _ := admin.mintKey("batman-utility-belt", []string{"core:admin"})

	headOwnerRule := func() bool { return strings.TrimSpace(config.Snapshot().OwnerEmail) != "" }
	require.False(t, headOwnerRule())
	require.False(t, ownerRequiredNow(), "books' owner-required reader disagrees with HEAD's owner_email rule")

	runOwnerCells(t, r, "owner unset", []ownerCell{
		// HEAD's verdict on restore with an empty body is the handler's own 400.
		{"session:custom on restore", http.MethodPost, ownerRestore, gordon, "", http.StatusBadRequest, ""},
		// Merging takes the admin role in the handler at HEAD as here
		// (handlers/user_merge.go mergeActor): its own 403 means the route's
		// guards admitted the caller.
		{"session:custom on merge preview", http.MethodPost, ownerMerge, gordon, "", http.StatusForbidden, mergeNeedsAdmin},
		{"delegated:custom on restore", http.MethodPost, ownerRestore, gordonDelegated, "", http.StatusBadRequest, ""},
		{"delegated:custom on merge preview", http.MethodPost, ownerMerge, gordonDelegated, "", http.StatusForbidden, mergeNeedsAdmin},
		{"delegated:admin on merge preview", http.MethodPost, ownerMerge, luciusDelegated, "", 0, ""},
		{"delegated:admin on guest-key plan", http.MethodGet, ownerGuestKey, luciusDelegated, "", 0, ""},
		{"key:admin on restore", http.MethodPost, ownerRestore, nil, key, http.StatusForbidden, "interactive_required"},
		{"key:admin on merge preview", http.MethodPost, ownerMerge, nil, key, http.StatusForbidden, "interactive_required"},
		{"key:admin on guest-key plan", http.MethodGet, ownerGuestKey, nil, key, http.StatusForbidden, "interactive_required"},
		// Core's own owner route keeps §4.8's not-required branch: core:admin.
		{"session:custom on core factory reset", http.MethodPost, ownerCoreKill, gordon, "", http.StatusForbidden, "permission_denied"},
		// The equivalence cell: core.auth.owner.required is false, so the
		// admin without owner assurance reaches the handler's 422 (no confirm;
		// nothing is staged).
		{"session:admin on core factory reset", http.MethodPost, ownerCoreKill, admin.cookie, "", http.StatusUnprocessableEntity, ""},
	})

	config.Mutate(func(c *config.Config) { c.OwnerEmail = "bruce@example.test" })
	require.True(t, headOwnerRule())
	require.True(t, ownerRequiredNow(), "books' owner-required reader disagrees with HEAD's owner_email rule")
	runOwnerCells(t, r, "owner set", []ownerCell{
		{"session:custom on restore", http.MethodPost, ownerRestore, gordon, "", http.StatusForbidden, "owner_required"},
		{"delegated:custom on restore", http.MethodPost, ownerRestore, gordonDelegated, "", http.StatusForbidden, "owner_required"},
		{"delegated:admin on merge preview", http.MethodPost, ownerMerge, luciusDelegated, "", http.StatusForbidden, "owner_required"},
		{"session:admin on restore", http.MethodPost, ownerRestore, admin.cookie, "", http.StatusForbidden, "owner_required"},
		{"delegated:admin on guest-key plan", http.MethodGet, ownerGuestKey, luciusDelegated, "", http.StatusForbidden, "owner_required"},
		// The equivalence cell: core.auth.owner.required is now true.
		{"session:admin on core factory reset", http.MethodPost, ownerCoreKill, admin.cookie, "", http.StatusForbidden, "owner_required"},
	})
}

// TestOwnerRoutesBoot_AuthOffIsRefusedAsCoreRules: with auth off the owner
// routes still refuse the auth-disabled principal at their credential guard
// (core's SEC5-8 tightening of HEAD's no-op credGuard, kept for these rows).
func TestOwnerRoutesBoot_AuthOffIsRefusedAsCoreRules(t *testing.T) {
	t.Cleanup(SetIdentityFlipForTest(true))
	r := runComposedWith(t, nil, nil, func(o *app.Options) { o.ConfigFlags["core.auth.mode"] = "disabled" }, nil)
	for i, c := range []struct{ method, path string }{
		{http.MethodPost, ownerRestore}, {http.MethodPost, ownerMerge}, {http.MethodGet, ownerGuestKey},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = fmt.Sprintf("127.0.0.%d:41000", 40+i)
		rec := httptest.NewRecorder()
		r.app.Handler().ServeHTTP(rec, req)
		t.Logf("cell %s %s (auth off): %d", c.method, c.path, rec.Code)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "interactive_required") {
			t.Errorf("auth off, %s %s: %d %s; want 403 interactive_required", c.method, c.path, rec.Code, credExcerpt(rec))
		}
	}
}
