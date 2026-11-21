// file: internal/server/handlers/bridge_admin_sites_test.go
// version: 0.1.0
// guid: b4de5d32-5034-4f14-92e2-46f276acd6c5
// last-edited: 2026-11-21

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/server/legacyauth"
)

// F4-08 B7: the role-name admin tests (mergeActor, SetUserEmail,
// inviteTokenVisible) keep HEAD's role test and add core:admin as a further
// refusal under core identity. The actor here is always HEAD's admin (role
// "admin", active, session), so only the principal differs between worlds:
//
//   - no Identity, a core principal without core:admin: unchanged (HEAD allows);
//   - Identity, the principal holds core:admin: allowed;
//   - Identity, the principal lacks core:admin: refused although the role holds.

// bridgedEngine re-serves the three routes with HEAD's admin as the user and a
// core principal of method m holding scopes (and the Identity when id is non-nil).
func (r *mergeRig) bridgedEngine(id *legacyauth.Identity, m coreauth.Method, scopes ...rbac.Permission) {
	e := gin.New()
	e.Use(func(c *gin.Context) {
		c.Set("auth_user", r.actor)
		c.Request = c.Request.WithContext(auth.WithMethod(c.Request.Context(), auth.MethodSession))
		bindCore(c, id, m, scopes...)
		c.Next()
	})
	e.GET("/users/:id/footprint", r.h.UserFootprint)
	e.PUT("/users/:id/email", r.h.SetUserEmail)
	e.GET("/users/invites", r.h.ListInvites)
	r.router = e
}

type adminWorld struct {
	name    string
	id      bool
	scopes  []rbac.Permission
	allowed bool
}

func adminWorlds() []adminWorld {
	return []adminWorld{
		{"legacy: a core principal without core:admin changes nothing", false, nil, true},
		{"legacy: a core principal with core:admin changes nothing", false, []rbac.Permission{rbac.CoreAdmin}, true},
		{"core identity: the principal holds core:admin", true, []rbac.Permission{rbac.CoreAdmin}, true},
		{"core identity: the role holds but the principal lacks core:admin", true, nil, false},
	}
}

func (w adminWorld) identity(t *testing.T) *legacyauth.Identity {
	if !w.id {
		return nil
	}
	return bridgeIdentity(t)
}

func TestBridgeSites_MergeActor(t *testing.T) {
	for _, w := range adminWorlds() {
		t.Run(w.name, func(t *testing.T) {
			r := newMergeRig(t)
			r.bridgedEngine(w.identity(t), coreauth.MethodSession, w.scopes...)
			rec, _ := r.do(t, http.MethodGet, "/users/"+r.viewer.ID+"/footprint", nil)
			want := http.StatusForbidden
			if w.allowed {
				want = http.StatusOK
			}
			if rec.Code != want {
				t.Fatalf("footprint = %d, want %d: %s", rec.Code, want, rec.Body.String())
			}
		})
	}
}

func TestBridgeSites_SetUserEmail(t *testing.T) {
	for _, w := range adminWorlds() {
		t.Run(w.name, func(t *testing.T) {
			r := newMergeRig(t)
			r.bridgedEngine(w.identity(t), coreauth.MethodSession, w.scopes...)
			rec, _ := r.do(t, http.MethodPut, "/users/"+r.viewer.ID+"/email", map[string]any{"email": "nightwing@example.com"})
			got, _ := r.store.GetUserByID(r.viewer.ID)
			if w.allowed {
				if rec.Code != http.StatusOK || got.Email != "nightwing@example.com" {
					t.Fatalf("set email = %d (email %q), want 200 and the new address: %s", rec.Code, got.Email, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusForbidden || got.Email == "nightwing@example.com" {
				t.Fatalf("set email = %d (email %q), want 403 and no write", rec.Code, got.Email)
			}
		})
	}
}

func TestBridgeSites_InviteTokenVisible(t *testing.T) {
	const emailed = "e5d2c1b0a9f8e7d6c5b4a3f2e1d0c9b8a7f6e5d4c3b2a1f0e9d8c7b6a5f4e3d2"
	tokenOf := func(t *testing.T, r *mergeRig) string {
		t.Helper()
		rec, _ := r.do(t, http.MethodGet, "/users/invites", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list invites = %d: %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Data struct {
				Invites []map[string]any `json:"invites"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		for _, inv := range env.Data.Invites {
			if inv["username"] == "barbara-gordon" {
				tok, _ := inv["token"].(string)
				return tok
			}
		}
		t.Fatalf("the emailed invite is not listed: %s", rec.Body.String())
		return ""
	}
	rig := func(t *testing.T, id *legacyauth.Identity, m coreauth.Method, scopes ...rbac.Permission) *mergeRig {
		t.Helper()
		r := newMergeRig(t)
		if _, err := r.store.CreateInvite(&database.Invite{
			Token: emailed, Username: "barbara-gordon", Email: "barbara@example.com", RoleID: "viewer",
			CreatedByUserID: r.admin.ID, ExpiresAt: time.Now().Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		r.bridgedEngine(id, m, scopes...)
		return r
	}

	for _, w := range adminWorlds() {
		t.Run(w.name, func(t *testing.T) {
			tok := tokenOf(t, rig(t, w.identity(t), coreauth.MethodSession, w.scopes...))
			if w.allowed && tok != emailed {
				t.Fatalf("token = %q, want it shown", tok)
			}
			if !w.allowed && tok != "" {
				t.Fatalf("token = %q, want it hidden", tok)
			}
		})
	}
	t.Run("core identity: the auth-disabled principal sees every token", func(t *testing.T) {
		if tok := tokenOf(t, rig(t, bridgeIdentity(t), coreauth.MethodAuthDisabled)); tok != emailed {
			t.Fatalf("token = %q, want it shown", tok)
		}
	})
	t.Run("core identity: the wiring flag is not read", func(t *testing.T) {
		r := rig(t, bridgeIdentity(t), coreauth.MethodSession)
		r.h.SetAuthEnabled(false)
		if tok := tokenOf(t, r); tok != "" {
			t.Fatalf("token = %q: books' enable_auth flag opened what the principal's checker closes", tok)
		}
	})
	t.Run("legacy: the wiring flag is HEAD's", func(t *testing.T) {
		r := rig(t, nil, coreauth.MethodAuthDisabled)
		r.h.SetAuthEnabled(false)
		if tok := tokenOf(t, r); tok != emailed {
			t.Fatalf("token = %q, want it shown with auth off", tok)
		}
		r2 := rig(t, nil, coreauth.MethodAuthDisabled)
		r2.actor = r2.viewer
		if tok := tokenOf(t, r2); tok != "" {
			t.Fatalf("token = %q: an auth-disabled core principal must not open HEAD's check", tok)
		}
	})
}
