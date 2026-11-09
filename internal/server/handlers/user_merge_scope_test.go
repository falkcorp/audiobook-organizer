// file: internal/server/handlers/user_merge_scope_test.go
// version: 1.0.1
// guid: e06f8e85-8d38-4fe0-9456-9e011d8a9412
// last-edited: 2026-11-09

package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/gin-gonic/gin"
)

// TestUserMerge_ScopedAdminKeyRefused is the HEAD-code half of 05-authn §11.1
// item 4 at the in-handler admin checks: mergeActor (user_merge.go) and the two
// handlers/user.go checks, SetUserEmail and inviteTokenVisible. They live here
// rather than in the middleware test because package middleware cannot import
// handlers (handlers imports middleware).
//
// An admin's API key scoped to users.manage passes every route guard these
// handlers sit behind, so before early fix 4 it reached the merge reads that are
// not credential routes (GET /users/merges, GET /users/:id/footprint) and set
// another user's email as verified. A key with every atom, a key with empty
// scopes and the admin's own session still pass.
func TestUserMerge_ScopedAdminKeyRefused(t *testing.T) {
	r := newMergeRig(t)
	member, err := r.store.CreateUser("selina-kyle", "selina@example.com", "bcrypt", "h4", []string{"viewer"}, "active")
	if err != nil {
		t.Fatal(err)
	}
	emailed := strings.Repeat("e", 64)
	if _, err := r.store.CreateInvite(&database.Invite{
		Token: emailed, Username: "barbara-gordon", Email: "barbara@example.com", RoleID: "viewer",
		CreatedByUserID: r.admin.ID, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	every := make([]string, 0, len(auth.All()))
	for _, p := range auth.All() {
		every = append(every, string(p))
	}
	type credential struct {
		method auth.Method
		key    *database.APIKey
	}
	keyWith := func(scopes []string) credential {
		return credential{auth.MethodAPIKey, &database.APIKey{ID: "k-batcave", UserID: r.admin.ID, Status: "active", Scopes: scopes}}
	}

	// serve answers one request as r.admin authenticated by cred, bound the
	// way handleAPIKeyAuth (or the session verifier) binds it.
	serve := func(cred credential, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set("auth_user", r.admin)
			if cred.key != nil {
				c.Set("auth_api_key", cred.key)
			}
			ctx := auth.WithUser(c.Request.Context(), r.admin)
			c.Request = c.Request.WithContext(auth.WithMethod(ctx, cred.method))
		})
		router.GET("/users/merges", r.h.ListUserMerges)
		router.GET("/users/:id/footprint", r.h.UserFootprint)
		router.PUT("/users/:id/email", r.h.SetUserEmail)
		router.GET("/users/invites", r.h.ListInvites)
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, path, rd)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	emailedToken := func(cred credential) string {
		t.Helper()
		w := serve(cred, http.MethodGet, "/users/invites", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("list invites: %d %s", w.Code, w.Body.String())
		}
		var env struct {
			Data struct {
				Invites []map[string]any `json:"invites"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		for _, inv := range env.Data.Invites {
			if inv["username"] == "barbara-gordon" {
				tok, _ := inv["token"].(string)
				return tok
			}
		}
		t.Fatalf("the emailed invite is not listed: %s", w.Body.String())
		return ""
	}
	footprint := "/users/" + r.absorbed.ID + "/footprint"
	email := "/users/" + member.ID + "/email"

	for name, cred := range map[string]credential{
		"users.manage key": keyWith([]string{string(auth.PermUsersManage)}),
		"library.view key": keyWith([]string{string(auth.PermLibraryView)}),
	} {
		t.Run(name, func(t *testing.T) {
			for _, c := range []struct {
				method, path string
				body         any
				msg          string
			}{
				{http.MethodGet, "/users/merges", nil, "merging accounts needs the admin role"},
				{http.MethodGet, footprint, nil, "merging accounts needs the admin role"},
				{http.MethodPut, email, map[string]any{"email": "catwoman@example.com"}, "setting another user's email needs the admin role"},
			} {
				w := serve(cred, c.method, c.path, c.body)
				if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), c.msg) {
					t.Errorf("%s %s: %d %s, want 403 %q", c.method, c.path, w.Code, w.Body.String(), c.msg)
				}
			}
			if got, _ := r.store.GetUserByID(member.ID); got.Email != "selina@example.com" {
				t.Errorf("a refused email set wrote %q", got.Email)
			}
			// Passes on the base too: CredentialChangeAllowed already hides
			// every token from every API key (see the brief).
			if tok := emailedToken(cred); tok != "" {
				t.Errorf("the emailed invite's token is shown to the key: %q", tok)
			}
		})
	}

	for name, cred := range map[string]credential{
		"every-atom key":  keyWith(every),
		"empty-scope key": keyWith(nil),
		"admin session":   {auth.MethodSession, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if w := serve(cred, http.MethodGet, "/users/merges", nil); w.Code != http.StatusOK {
				t.Errorf("merges: %d %s, want 200", w.Code, w.Body.String())
			}
			if w := serve(cred, http.MethodGet, footprint, nil); w.Code != http.StatusOK {
				t.Errorf("footprint: %d %s, want 200", w.Code, w.Body.String())
			}
			// Clearing is idempotent, so every credential can repeat it.
			if w := serve(cred, http.MethodPut, email, map[string]any{"email": ""}); w.Code != http.StatusOK {
				t.Errorf("email: %d %s, want 200", w.Code, w.Body.String())
			}
		})
	}

	// The admin's own sign-in still sees the emailed invite's token.
	if tok := emailedToken(credential{auth.MethodSession, nil}); tok != emailed {
		t.Errorf("admin session: emailed token = %q, want it shown", tok)
	}
}
