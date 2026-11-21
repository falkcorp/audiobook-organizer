// file: internal/cfaccess/owner_guard_core_test.go
// version: 0.1.0
// guid: ce6352d7-b303-4aac-8245-60670685f352
// last-edited: 2026-11-21

package cfaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	coreauth "github.com/falkcorp/mediacore/auth"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
)

// corePrincipal is a principal of method m holding assurance a, as stage 12
// binds it.
func corePrincipal(m coreauth.Method, a coreauth.Assurance) *coreauth.Principal {
	return &coreauth.Principal{
		Method:    m,
		Assurance: a,
		User:      &coreauth.User{ID: "01JALFRED", Username: "alfred", Status: coreauth.StatusActive},
	}
}

// The owner proof under core identity is the principal of this package's own
// extension carrying AssuranceOwner. Every refusal here must read exactly as
// HEAD's proof words it, so the page says the same thing under either chain.
func TestOwnerProofWhyNotCore_AgreesWithHeadsProof(t *testing.T) {
	const host = "books.example.test"
	for _, tc := range []struct {
		name  string
		owner string
		p     *coreauth.Principal
		// head is the legacy context HEAD's proof reads for the same caller.
		headMethod auth.Method
		headEmail  string
	}{
		{"the owner through Access", ownerMail, corePrincipal(CoreMethod, coreauth.AssuranceOwner), auth.MethodCFAccess, ownerMail},
		{"another Access user", ownerMail, corePrincipal(CoreMethod, 0), auth.MethodCFAccess, "dick@wayne.example.test"},
		{"a password session", ownerMail, corePrincipal(coreauth.MethodSession, 0), auth.MethodSession, ""},
		{"an API key", ownerMail, corePrincipal(coreauth.MethodAPIKey, 0), auth.MethodAPIKey, ""},
		{"a delegated session", ownerMail, corePrincipal(coreauth.MethodSessionDelegated, 0), auth.MethodSessionDelegated, ""},
		{"no owner configured", "", corePrincipal(CoreMethod, coreauth.AssuranceOwner), auth.MethodCFAccess, ownerMail},
		{"a blank owner", "   ", corePrincipal(CoreMethod, coreauth.AssuranceOwner), auth.MethodCFAccess, ownerMail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := auth.WithMethod(context.Background(), tc.headMethod)
			ctx = auth.WithAccessEmail(ctx, tc.headEmail)
			head := auth.OwnerProofWhyNot(ctx, tc.owner, host)
			core := OwnerProofWhyNotCore(tc.p, tc.owner, host)
			if (head == "") != (core == "") {
				t.Fatalf("HEAD says %q, core says %q", head, core)
			}
			if tc.name == "another Access user" {
				// HEAD names the e-mail the JWT stated; the principal carries
				// none, so core says "an unknown identity" and nothing else.
				if want := "Owner actions need the owner's Cloudflare Access sign-in; this request is signed in as an unknown identity"; core != want {
					t.Fatalf("core = %q", core)
				}
				return
			}
			if head != core {
				t.Fatalf("HEAD says %q, core says %q", head, core)
			}
		})
	}
}

// Assurance is the Access extension's own: the bit on a principal of another
// method is not an owner proof, and no principal is not one either.
func TestOwnerProofWhyNotCore_OnlyTheAccessExtensionProves(t *testing.T) {
	for name, p := range map[string]*coreauth.Principal{
		"assurance on a session":          corePrincipal(coreauth.MethodSession, coreauth.AssuranceOwner),
		"assurance on an API key":         corePrincipal(coreauth.MethodAPIKey, coreauth.AssuranceOwner),
		"assurance on another extension":  corePrincipal("ext:other", coreauth.AssuranceOwner),
		"the auth-disabled principal":     {Method: coreauth.MethodAuthDisabled},
		"no principal":                    nil,
		"the extension without assurance": corePrincipal(CoreMethod, 0),
	} {
		if why := OwnerProofWhyNotCore(p, ownerMail, ""); why == "" {
			t.Errorf("%s passed the owner proof", name)
		}
	}
	if why := OwnerProofWhyNotCore(corePrincipal(CoreMethod, coreauth.AssuranceOwner), ownerMail, ""); why != "" {
		t.Fatalf("the owner was refused: %s", why)
	}
}

// The gate: the proof, then the permission leg (HEAD's auth.Can at
// owner_guard.go:74, here the injected can), never skipped.
func TestRequireOwnerCore_Gate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const perm auth.Permission = "library.edit"
	yes := func(context.Context, auth.Permission) bool { return true }
	no := func(context.Context, auth.Permission) bool { return false }
	owner := corePrincipal(CoreMethod, coreauth.AssuranceOwner)
	for _, tc := range []struct {
		name  string
		p     *coreauth.Principal
		owner string
		perm  auth.Permission
		can   func(context.Context, auth.Permission) bool
		want  int
	}{
		{"owner through Access, no permission leg", owner, ownerMail, "", nil, http.StatusOK},
		{"owner through Access holding the permission", owner, ownerMail, perm, yes, http.StatusOK},
		{"owner through Access without the permission", owner, ownerMail, perm, no, http.StatusForbidden},
		{"owner, permission asked, no check wired fails closed", owner, ownerMail, perm, nil, http.StatusForbidden},
		{"another Access user", corePrincipal(CoreMethod, 0), ownerMail, perm, yes, http.StatusForbidden},
		{"a session admin", corePrincipal(coreauth.MethodSession, 0), ownerMail, perm, yes, http.StatusForbidden},
		{"an API key", corePrincipal(coreauth.MethodAPIKey, 0), ownerMail, perm, yes, http.StatusForbidden},
		{"no owner configured", owner, "", perm, yes, http.StatusForbidden},
		{"no principal", nil, ownerMail, perm, yes, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(func(c *gin.Context) {
				if tc.p != nil {
					c.Request = c.Request.WithContext(coreauth.WithPrincipal(c.Request.Context(), tc.p))
				}
				c.Next()
			})
			r.POST("/owner", RequireOwnerCore(func() string { return tc.owner }, tc.perm, tc.can, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/owner", nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	t.Run("applies skips the gate for a preview", func(t *testing.T) {
		r := gin.New()
		r.POST("/owner", RequireOwnerCore(func() string { return ownerMail }, perm, no, func(*gin.Context) bool { return false }),
			func(c *gin.Context) { c.Status(http.StatusOK) })
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/owner", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	})
	t.Run("an owner e-mail change takes effect at once", func(t *testing.T) {
		email := ownerMail
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(coreauth.WithPrincipal(c.Request.Context(), owner))
		})
		r.POST("/owner", RequireOwnerCore(func() string { return email }, "", nil, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
		for _, want := range []struct {
			email string
			code  int
		}{{ownerMail, http.StatusOK}, {"", http.StatusForbidden}, {ownerMail, http.StatusOK}} {
			email = want.email
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/owner", nil))
			if w.Code != want.code {
				t.Fatalf("owner %q: status = %d, want %d", want.email, w.Code, want.code)
			}
		}
	})
}

// CoreMethod is the method the extension registers, not a copy that could
// drift: the principals stage 12 binds for it carry exactly Spec.Method.
func TestCoreMethodIsTheExtensionsMethod(t *testing.T) {
	g := newRig(t)
	if CoreMethod != g.ext.Spec.Method {
		t.Fatalf("CoreMethod = %q, the extension registers %q", CoreMethod, g.ext.Spec.Method)
	}
}
