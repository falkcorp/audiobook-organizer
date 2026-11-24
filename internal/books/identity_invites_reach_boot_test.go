// file: internal/books/identity_invites_reach_boot_test.go
// version: 0.1.0
// guid: cb3a41e9-c4f9-4d97-a04a-91aab610c571
// last-edited: 2026-11-22

package books

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/app"
)

// The invite list and delete at HEAD's reach under core identity (F4-08 B10y,
// item 4 cause B and auth-off cause 2). HEAD's GET and DELETE
// /api/v1/users/invites were s.perm(users.manage) with no credential guard
// (origin/main wire_library_routes.go:156-157), so a key holding the atom was
// admitted, and with auth off every request was. Creating and resending were
// credRoutes (:161 and the invite POST) and stay refused to a key. Core's own
// /api/core/v1/users/invites routes keep their rule.

// invitesReq is one request through the root handler from its own address.
func invitesReq(t *testing.T, r *runningApp, method, path, body, bearer string, ck *http.Cookie, addr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = addr
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if ck != nil {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// TestInvitesReachBoot_KeyListsAndDeletesButNeverCreates: the users.manage key
// lists and deletes through books' routes as at HEAD; it is still refused on
// create and resend, a key without the atom is refused the list, and core's
// native delete still refuses the key.
func TestInvitesReachBoot_KeyListsAndDeletesButNeverCreates(t *testing.T) {
	r := bootAccess(t, nil, nil)
	admin := newCoreAdmin(t, r)
	create := func(username string) string {
		rec := invitesReq(t, r, http.MethodPost, "/api/v1/users/invite", `{"username":"`+username+`","role_id":"viewer"}`, "", admin.cookie, "192.0.2.11:41000")
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		var out struct {
			Data struct {
				Invite struct {
					ID string `json:"id"`
				} `json:"invite"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		require.NotEmpty(t, out.Data.Invite.ID, rec.Body.String())
		return out.Data.Invite.ID
	}
	manager, _ := admin.mintKey("batman-oracle-link", []string{"core:users.manage", "core:roles.view"})
	viewer, _ := admin.mintKey("batman-gotham-gazette", []string{"books:view"})
	first, second := create("harvey-bullock"), create("renee-montoya")

	cells := []struct {
		name, method, path, body, key string
		want                          int
		contains                      string
	}{
		{"key:users.manage lists", http.MethodGet, "/api/v1/users/invites", "", manager, http.StatusOK, first},
		{"key:users.manage deletes", http.MethodDelete, "/api/v1/users/invites/" + first, "", manager, http.StatusOK, first},
		{"key:users.manage deletes again", http.MethodDelete, "/api/v1/users/invites/" + first, "", manager, http.StatusNotFound, ""},
		{"key:users.manage creates", http.MethodPost, "/api/v1/users/invite", `{"username":"selina-kyle","role_id":"viewer"}`, manager, http.StatusForbidden, "interactive_required"},
		{"key:users.manage resends", http.MethodPost, "/api/v1/users/invites/" + second + "/resend", "", manager, http.StatusForbidden, "interactive_required"},
		{"key without users.manage lists", http.MethodGet, "/api/v1/users/invites", "", viewer, http.StatusForbidden, ""},
		{"key:users.manage on core's native delete", http.MethodDelete, "/api/core/v1/users/invites/" + second, "", manager, http.StatusForbidden, "interactive_required"},
	}
	for i, c := range cells {
		rec := invitesReq(t, r, c.method, c.path, c.body, c.key, nil, "192.0.2."+strconv.Itoa(40+i)+":41000")
		t.Logf("cell %s %s (%s): %d", c.method, c.path, c.name, rec.Code)
		if rec.Code != c.want || (c.contains != "" && !strings.Contains(rec.Body.String(), c.contains)) {
			t.Errorf("%s: %d %s; want %d containing %q", c.name, rec.Code, credExcerpt(rec), c.want, c.contains)
		}
	}
	// The resend refused above left the invite alone.
	rec := invitesReq(t, r, http.MethodGet, "/api/v1/users/invites", "", "", admin.cookie, "192.0.2.12:41000")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), second)
	require.NotContains(t, rec.Body.String(), first)
}

// TestInvitesReachBoot_AuthOffListsAndDeletesAsHEADDid: with auth off, books'
// list answers 200 and its delete answers the invite's own 404, never 401
// (which ends the SPA's session) or 403; core's native routes still refuse the
// auth-disabled principal: the list 403 interactive_required (needUser; it was
// 401 until F4-08 ruling G5(a), which would end the SPA's session), the delete
// 403 at its interactive guard.
func TestInvitesReachBoot_AuthOffListsAndDeletesAsHEADDid(t *testing.T) {
	r := runComposedWith(t, nil, nil, func(o *app.Options) { o.ConfigFlags["core.auth.mode"] = "disabled" }, nil)
	cells := []struct {
		name, method, path string
		want               int
	}{
		{"books list", http.MethodGet, "/api/v1/users/invites", http.StatusOK},
		{"books delete of an absent invite", http.MethodDelete, "/api/v1/users/invites/01BATMANNOSUCHINVITE00000", http.StatusNotFound},
		{"core's native list", http.MethodGet, "/api/core/v1/users/invites", http.StatusForbidden},
		// Its interactive guard answers first (05-rbac §4.11(a), SEC5-8).
		{"core's native delete", http.MethodDelete, "/api/core/v1/users/invites/01BATMANNOSUCHINVITE00000", http.StatusForbidden},
	}
	for i, c := range cells {
		rec := invitesReq(t, r, c.method, c.path, "", "", nil, "127.0.0."+strconv.Itoa(20+i)+":41000")
		t.Logf("cell %s %s (auth off, %s): %d", c.method, c.path, c.name, rec.Code)
		if rec.Code != c.want {
			t.Errorf("auth off, %s: %d %s; want %d", c.name, rec.Code, credExcerpt(rec), c.want)
		}
	}
}
