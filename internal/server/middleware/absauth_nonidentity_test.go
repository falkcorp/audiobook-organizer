// file: internal/server/middleware/absauth_nonidentity_test.go
// version: 1.0.0
// guid: 8d3f07c5-1b96-4e2a-a740-6f5c93b1e082
// last-edited: 2026-08-01

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/gin-gonic/gin"
)

// These tests cover the Mode B topology: Cloudflare Access admits the request on a
// SERVICE TOKEN (a machine credential, no person attached), and our own ABS bearer
// token says who the user is. The assertion Cloudflare mints in that case is
// cryptographically valid but has no email claim.
//
// The original bug collapsed "valid but anonymous" into "invalid", so every Mode B
// request was a terminal 401 even when it carried a perfectly good bearer alongside.
// The fix distinguishes the two with oauth.ErrNonIdentityAssertion. The danger in a
// fix like this is over-correcting into a fail-open, so the tests below pin BOTH
// edges: the non-identity case falls through, and every other verification failure
// stays a hard 401.

const svcTokenAssertion = "valid-but-no-identity"

func nonIdentityHarness(t *testing.T) *absHarness {
	t.Helper()
	h := newABSHarness(t, "cf,jwt", []string{"owner@example.com"})
	h.verifier.nonIdentity = map[string]bool{svcTokenAssertion: true}
	return h
}

// (a) A forged assertion must STILL be a terminal 401, and must still refuse to be
// rescued by a valid bearer. This is the over-correction guard: if the sentinel were
// matched too loosely (say, by string matching, or by treating any Verify error as
// non-identity), this test goes red.
func TestABSAuth_NonIdentity_ForgedAssertionStillHard401(t *testing.T) {
	h := nonIdentityHarness(t)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	bearer := h.mintAccess(t, "u1", "s1")

	for _, forged := range []string{"not-a-jwt", "a.b.c", "forged", svcTokenAssertion + "-tampered"} {
		w := h.do(http.MethodGet, "/api/me", map[string]string{
			oauth.CFAccessHeader: forged,
			"Authorization":      "Bearer " + bearer,
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("forged assertion %q: got %d, want 401 — a bad credential must never be rescued by a second one (body: %s)",
				forged, w.Code, w.Body.String())
		}
	}
}

// (b) THE FIX. A valid service-token assertion carries no identity, so the request
// must fall through to the bearer token and be admitted as that user.
func TestABSAuth_NonIdentity_WithBearerIsAdmitted(t *testing.T) {
	h := nonIdentityHarness(t)
	h.store.addUser(activeUser("u1", "owner"))
	h.store.addSession(liveABSSession("s1", "u1"))
	bearer := h.mintAccess(t, "u1", "s1")

	w := h.do(http.MethodGet, "/api/me", map[string]string{
		oauth.CFAccessHeader: svcTokenAssertion,
		"Authorization":      "Bearer " + bearer,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — a service-token assertion carries no identity and must fall through to the bearer (body: %s)",
			w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"u1"`) {
		t.Fatalf("resolved the wrong user: %s", w.Body.String())
	}
	// The user was proven by the BEARER, not the assertion. If this reported the CF
	// mode, the assertion would have been credited with an identity it never carried.
	if strings.Contains(w.Body.String(), `"mode":"cf"`) {
		t.Fatalf("mode must not be cf — identity came from the bearer, not the assertion: %s", w.Body.String())
	}
}

// (c) Falling through is NOT the same as being admitted. A service token alone
// proves a device may reach the origin; it never says who the user is, so with no
// bearer the request must still be rejected.
func TestABSAuth_NonIdentity_WithoutBearerIs401(t *testing.T) {
	h := nonIdentityHarness(t)
	h.store.addUser(activeUser("u1", "owner"))

	w := h.do(http.MethodGet, "/api/me", map[string]string{
		oauth.CFAccessHeader: svcTokenAssertion,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 — a service token is not an identity; falling through must not mean admitting (body: %s)",
			w.Code, w.Body.String())
	}
}

// (d) POST /login and POST /auth/refresh call ResolveCFAssertion directly, and read
// its (nil, nil) return as "no CF identity here, do the normal password check". A
// service-token assertion must produce exactly that, so a Mode B client can still log
// in with a username and password. Before the fix it produced a terminal 401 instead,
// making login impossible for the very topology Mode B exists to serve.
func TestABSAuth_NonIdentity_LoginReachesPasswordPath(t *testing.T) {
	h := nonIdentityHarness(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)
	c.Request.Header.Set(oauth.CFAccessHeader, svcTokenAssertion)

	identity, authErr := h.resolver.ResolveCFAssertion(c)
	if authErr != nil {
		t.Fatalf("got error %+v, want nil — a service-token assertion must not block /login; "+
			"the client has to be able to fall through to the password check", authErr)
	}
	if identity != nil {
		t.Fatalf("got identity %+v, want nil — the assertion named no person, so nothing may be resolved from it", identity)
	}
}

// A forged assertion on /login must still be terminal. Pairs with (d): the fall-
// through is granted to the non-identity case ONLY.
func TestABSAuth_NonIdentity_LoginStillRejectsForgedAssertion(t *testing.T) {
	h := nonIdentityHarness(t)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)
	c.Request.Header.Set(oauth.CFAccessHeader, "forged")

	identity, authErr := h.resolver.ResolveCFAssertion(c)
	if authErr == nil {
		t.Fatalf("want a terminal error for a forged assertion on /login, got nil (identity=%+v)", identity)
	}
	if authErr.Status != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401 for a forged assertion", authErr.Status)
	}
}
