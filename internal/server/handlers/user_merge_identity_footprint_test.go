// file: internal/server/handlers/user_merge_identity_footprint_test.go
// version: 0.1.0
// guid: 7d2e94b1-6a05-4c38-8e1f-b3c0a95d7e46
// last-edited: 2026-11-22

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B10fp: under core identity the footprint route is live. The service is
// the real usermerge.Service over the stand-in for core's admin handles
// (user_merge_identity_test.go), so what these cells pin is the route and the
// service's reading of core's masked view, not core's own read (core/auth).

// AdminAccountFootprint is the stand-in for core's masked read: it needs a
// principal on ctx, as the real one does, and answers what the test set.
func (c *idCore) AdminAccountFootprint(ctx context.Context, id coreauth.UserID) (coreauth.AccountFootprint, error) {
	c.footprintCalls++
	if _, ok := coreauth.PrincipalFrom(ctx); !ok {
		return coreauth.AccountFootprint{}, fmt.Errorf("%w: no principal", coreauth.ErrDenied)
	}
	if c.footprintErr != nil {
		return coreauth.AccountFootprint{}, c.footprintErr
	}
	if _, ok := c.users[string(id)]; !ok {
		return coreauth.AccountFootprint{}, coreauth.ErrNotFound
	}
	return c.footprint, nil
}

// decodeData decodes the response envelope's data member into T.
func decodeData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var env struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return env.Data
}

type footprintBody struct {
	User struct {
		ID       string   `json:"id"`
		Username string   `json:"username"`
		Roles    []string `json:"roles"`
	} `json:"user"`
	Identities []struct {
		ID         string `json:"id"`
		Provider   string `json:"provider"`
		Subject    string `json:"subject"`
		Email      string `json:"email"`
		OwnerEmail bool   `json:"owner_email"`
	} `json:"identities"`
	Counts         map[string]int `json:"counts"`
	ActiveSessions *int           `json:"active_sessions"`
	Unreadable     *[]string      `json:"unreadable"`
}

// The wired footprint answers 200 in HEAD's shape: the account from core, the
// sign-ins masked, core's counts for the families core owns, books' counts for
// the ones books owns, and every member present.
func TestUserMergeRoutes_FootprintIsLiveUnderIdentity(t *testing.T) {
	r, core := idMergeRig(t)
	r.idEngine(t, true)
	core.footprint = coreauth.AccountFootprint{
		Identities: []coreauth.IdentityLink{
			{Provider: "gotham-sso", SubjectMask: "su…42", Email: "owner@example.com", LinkedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
			{Provider: "gotham-sso", SubjectMask: "ab…yz", Email: "other@example.test", LinkedAt: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)},
		},
		APIKeys: 3, Invites: 2, InviteGrants: 1, ActiveSessions: 4, Unreadable: []string{"invite (undecodable)"},
	}
	w, _ := r.do(t, http.MethodGet, "/users/"+r.absorbed.ID+"/footprint", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("footprint under identity: %d %s, want 200", w.Code, w.Body.String())
	}
	body := decodeData[footprintBody](t, w.Body.Bytes())
	if body.User.ID != r.absorbed.ID || body.User.Username != "gone-acct" {
		t.Errorf("user = %+v, want core's account", body.User)
	}
	if len(body.Identities) != 2 || body.Identities[0].Subject != "su…42" || body.Identities[1].Subject != "ab…yz" {
		t.Fatalf("identities = %+v, want the two masked sign-ins in core's order", body.Identities)
	}
	if !body.Identities[0].OwnerEmail || body.Identities[1].OwnerEmail {
		t.Errorf("owner_email flags = %v, %v; want true for the configured owner's address only", body.Identities[0].OwnerEmail, body.Identities[1].OwnerEmail)
	}
	if body.Identities[0].ID == body.Identities[1].ID || body.Identities[0].ID == "" {
		t.Errorf("identity ids %q, %q: want distinct, non-empty", body.Identities[0].ID, body.Identities[1].ID)
	}
	for fam, want := range map[string]int{"oauth_identities": 2, "api_keys": 3, "invites": 2, "invite_grants": 1, "preferences": 1} {
		if got, ok := body.Counts[fam]; !ok || got != want {
			t.Errorf("counts[%s] = %d (present %v), want %d: %v", fam, got, ok, want, body.Counts)
		}
	}
	// Every member HEAD names is there, including the families with no rows.
	for _, fam := range []string{"book_state", "positions", "bookmarks", "sync_alias_use", "preferences", "playback_progress",
		"playback_events", "listen_stats", "oauth_identities", "api_keys", "playlists", "collections", "invites", "invite_grants"} {
		if _, ok := body.Counts[fam]; !ok {
			t.Errorf("counts has no %q: %v", fam, body.Counts)
		}
	}
	if body.ActiveSessions == nil || *body.ActiveSessions != 4 {
		t.Errorf("active_sessions = %v, want 4", body.ActiveSessions)
	}
	if body.Unreadable == nil || len(*body.Unreadable) != 1 || (*body.Unreadable)[0] != "invite (undecodable)" {
		t.Errorf("unreadable = %v, want core's note", body.Unreadable)
	}
	if core.footprintCalls != 1 {
		t.Errorf("core's footprint was read %d times, want 1", core.footprintCalls)
	}
	for _, leak := range []string{"subject-of", idCredential, "h1", "h2", "bcrypt"} {
		if strings.Contains(w.Body.String(), `"`+leak+`"`) || strings.Contains(w.Body.String(), idCredential) {
			t.Errorf("the footprint carries %q: %s", leak, w.Body.String())
		}
	}
}

// The route still refuses whoever HEAD's mergeActor refused, and answers what
// core answers for the rest: a non-admin actor, an admin whose principal lacks
// core:admin, core's own denial, an absent account and a request with no
// principal. A read the handler refuses never reaches core's footprint.
func TestUserMergeRoutes_FootprintUnderIdentityKeepsItsGuards(t *testing.T) {
	absent := "u-nobody"
	for _, tc := range []struct {
		name       string
		actor      func(*mergeRig) // changes r.actor
		scopes     []rbac.Permission
		principal  bool
		coreErr    error
		id         func(*mergeRig) string
		want       int
		coreCalled bool
	}{
		{name: "an admin holding core:admin", scopes: []rbac.Permission{rbac.CoreAdmin}, principal: true, want: http.StatusOK, coreCalled: true},
		{name: "a non-admin actor", actor: func(r *mergeRig) { r.actor = r.viewer }, scopes: []rbac.Permission{rbac.CoreAdmin}, principal: true, want: http.StatusForbidden},
		{name: "the admin role without core:admin", scopes: nil, principal: true, want: http.StatusForbidden},
		{name: "core's own denial", scopes: []rbac.Permission{rbac.CoreAdmin}, principal: true,
			coreErr: fmt.Errorf("%w: users.manage", coreauth.ErrDenied), want: http.StatusForbidden, coreCalled: true},
		{name: "a request with no principal", scopes: []rbac.Permission{rbac.CoreAdmin}, principal: false, want: http.StatusForbidden},
		{name: "an absent account", scopes: []rbac.Permission{rbac.CoreAdmin}, principal: true, id: func(*mergeRig) string { return absent }, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, core := idMergeRig(t)
			core.footprintErr = tc.coreErr
			if tc.actor != nil {
				tc.actor(r)
			}
			e := gin.New()
			e.Use(func(c *gin.Context) {
				c.Set("auth_user", r.actor)
				ctx := rbac.WithChecker(legacyauth.WithIdentity(c.Request.Context(), bridgeIdentity(t)), bridgeChecker(tc.scopes))
				if tc.principal {
					ctx = coreauth.WithPrincipal(ctx, &coreauth.Principal{User: &coreauth.User{ID: coreauth.UserID(r.actor.ID)},
						CredentialID: idCredential, Method: coreauth.MethodSession, Perms: bridgeChecker(tc.scopes)})
				}
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			})
			e.GET("/users/:id/footprint", r.h.UserFootprint)
			r.router = e
			id := r.absorbed.ID
			if tc.id != nil {
				id = tc.id(r)
			}
			w, _ := r.do(t, http.MethodGet, "/users/"+id+"/footprint", nil)
			if w.Code != tc.want {
				t.Fatalf("%s: %d %s, want %d", tc.name, w.Code, w.Body.String(), tc.want)
			}
			if called := core.footprintCalls > 0; called != tc.coreCalled {
				t.Errorf("%s: core's footprint read called = %v, want %v", tc.name, called, tc.coreCalled)
			}
			if tc.want != http.StatusOK && strings.Contains(w.Body.String(), `"counts"`) {
				t.Errorf("%s: a refusal carries footprint data: %s", tc.name, w.Body.String())
			}
		})
	}
}
