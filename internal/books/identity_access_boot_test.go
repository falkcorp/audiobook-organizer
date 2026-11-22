// file: internal/books/identity_access_boot_test.go
// version: 0.1.0
// guid: 4c7e1a9d-3b52-4f68-8d04-e1a6b9c32f75
// last-edited: 2026-11-21

package books

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/legacyoauth"
)

// The Access-only install of F4-08 B10o: Cloudflare Access verifies the person,
// the allow-list is modules.books.cfaccess.allowed_emails (env
// ABO_BOOKS_CFACCESS_ALLOWED_EMAILS), and no OIDC provider entry exists. Every
// address is Batman's, on the documentation range.
const (
	accessOwner  = "bruce.wayne@wayne-enterprises.example"
	accessAlly   = "lucius.fox@wayne-enterprises.example"
	accessOutlaw = "edward.nygma@arkham.example"
	accessEnvKey = "ABO_BOOKS_CFACCESS_ALLOWED_EMAILS"
)

// accessAssertions is the test verifier the late handle takes: a token names the
// verified claims it stands for, and an unknown token is a forgery.
type accessAssertions map[string]*legacyoauth.IdentityClaims

func (a accessAssertions) VerifyIssued(_ context.Context, raw string) (*cfaccess.VerifiedAssertion, error) {
	c, ok := a[raw]
	if !ok {
		return nil, errors.New("not an assertion this test minted")
	}
	return &cfaccess.VerifiedAssertion{Claims: c, IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (a accessAssertions) Verify(ctx context.Context, raw string) (*legacyoauth.IdentityClaims, error) {
	va, err := a.VerifyIssued(ctx, raw)
	if err != nil {
		return nil, err
	}
	return va.Claims, nil
}

func newAccessAssertions() accessAssertions {
	mint := func(sub, email string) *legacyoauth.IdentityClaims {
		return &legacyoauth.IdentityClaims{Provider: cfaccessProvider, Subject: sub, Email: email, EmailVerified: true}
	}
	return accessAssertions{
		"owner":   mint("sub-bruce", accessOwner),
		"ally":    mint("sub-lucius", accessAlly),
		"outlaw":  mint("sub-edward", accessOutlaw),
		"outlaw2": mint("sub-edward-again", accessOutlaw),
	}
}

// bootAccessOnly is a real composed boot, Run and listening, with the identity
// flip on, sign-in required, the owner configured, Access the only way in (no
// provider entry anywhere) and the allow-list set the way an operator sets it:
// the books key's environment layer. The Access verifier is the late handle
// books builds, bound here to the test verifier because the real one fetches
// Cloudflare's keys over the network (the team domain and AUD stay unset, so
// Register binds nothing of its own).
func bootAccessOnly(t *testing.T, allowList string) *runningApp {
	t.Helper()
	t.Cleanup(SetIdentityFlipForTest(true))
	t.Setenv(accessEnvKey, allowList)
	r := runComposedWith(t, func(c *config.Config) {
		c.EnableAuth = true
		c.OwnerEmail = accessOwner
	}, nil, nil, nil)
	require.NoError(t, r.mod.idw.verifier.Bind(newAccessAssertions()))
	return r
}

// accessGet is an authenticated-route request carrying the Access assertion
// header the edge injects, answered by the root handler.
func accessGet(t *testing.T, r *runningApp, assertion string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/me", nil)
	req.RemoteAddr = "192.0.2.10:41000"
	req.Header.Set(cfaccess.CFAccessHeader, assertion)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	return rec
}

// userEmails is the name of every user core holds (an Access account is named by its e-mail), read as the bootstrap admin
// through GET /api/core/v1/users: the one place a refused sign-in's lack of side
// effects shows at full boot (the books store no longer holds users).
func userEmails(t *testing.T, r *runningApp, admin *http.Cookie) []string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/core/v1/users", nil)
	req.RemoteAddr = "192.0.2.10:41000"
	req.AddCookie(admin)
	rec := httptest.NewRecorder()
	r.app.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		Items []struct {
			Username string `json:"username"`
		} `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotEmpty(t, body.Items, "the user list is not in the shape this helper reads: %s", rec.Body.String())
	out := make([]string, 0, len(body.Items))
	for _, u := range body.Items {
		out = append(out, u.Username)
	}
	return out
}

// bootstrapAdmin redeems the bootstrap token (which also waits for the app to be
// ready: a request before that is answered before stage 12 runs) and logs the
// generated admin in. It returns the admin's session cookie.
func bootstrapAdmin(t *testing.T, r *runningApp) *http.Cookie {
	t.Helper()
	user, pass := redeemBootstrap(t, r.composed)
	rec := post(t, r.app.Handler(), "/api/core/v1/auth/login", map[string]string{"username": user, "password": pass}, false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, ck := range rec.Result().Cookies() {
		if strings.HasSuffix(ck.Name, "session_id") {
			return ck
		}
	}
	t.Fatalf("no session cookie: %v", rec.Result().Header["Set-Cookie"])
	return nil
}

func refused(code int) bool { return code == http.StatusUnauthorized || code == http.StatusForbidden }

// TestIdentityAccessBoot_ListedOwnerSignsIn: on an Access-only install (no OIDC
// provider entry) whose allow-list is the books key, the listed owner signs in
// through an Access assertion and reaches an authenticated route as themself.
func TestIdentityAccessBoot_ListedOwnerSignsIn(t *testing.T) {
	r := bootAccessOnly(t, " "+strings.ToUpper(accessOwner)+" , "+accessAlly)
	admin := bootstrapAdmin(t, r)
	rec := accessGet(t, r, "owner")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var me struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me), rec.Body.String())
	require.Equal(t, accessOwner, me.User.Email)
	require.Contains(t, userEmails(t, r, admin), accessOwner, "the owner's account was not created")
}

// TestIdentityAccessBoot_UnlistedPersonIsRefusedAndCreatesNobody: a verified
// Access identity that is not on the list is refused at the route and no user is
// created for it. The same person is refused again under a second subject (the
// refusal is the list's, not a cached answer).
func TestIdentityAccessBoot_UnlistedPersonIsRefusedAndCreatesNobody(t *testing.T) {
	r := bootAccessOnly(t, accessOwner+","+accessAlly)
	admin := bootstrapAdmin(t, r)
	before := userEmails(t, r, admin)
	for _, tok := range []string{"outlaw", "outlaw2"} {
		rec := accessGet(t, r, tok)
		require.True(t, refused(rec.Code), "%s: status %d: %s", tok, rec.Code, rec.Body.String())
	}
	require.ElementsMatch(t, before, userEmails(t, r, admin), "a refused sign-in created a user")
	require.NotContains(t, userEmails(t, r, admin), accessOutlaw)
}

// TestIdentityAccessBoot_EmptyListRefusesTheOwnerToo: an empty allow-list admits
// nobody, the configured owner included (fail closed, as HEAD's gate did). The
// owner then signs in by other means; this test asserts the refusal and does not
// soften it.
func TestIdentityAccessBoot_EmptyListRefusesTheOwnerToo(t *testing.T) {
	r := bootAccessOnly(t, "")
	admin := bootstrapAdmin(t, r)
	before := userEmails(t, r, admin)
	for _, tok := range []string{"owner", "ally"} {
		rec := accessGet(t, r, tok)
		require.True(t, refused(rec.Code), "%s: status %d: %s", tok, rec.Code, rec.Body.String())
	}
	require.ElementsMatch(t, before, userEmails(t, r, admin), "a refused sign-in created a user")
}

// TestIdentityAccessBoot_LinkedSubjectIsStillJudgedByTheList: a person who has
// signed in (their subject is linked to an account) and is then taken off the
// list is refused on the next request: admission is checked before the linked
// lookup, as at HEAD, so an account is no standing right to enter. The list is
// narrowed by rebinding the policy the boot built, from the same reader.
func TestIdentityAccessBoot_LinkedSubjectIsStillJudgedByTheList(t *testing.T) {
	r := bootAccessOnly(t, accessOwner+","+accessAlly)
	admin := bootstrapAdmin(t, r)
	require.Equal(t, http.StatusOK, accessGet(t, r, "ally").Code)
	require.Contains(t, userEmails(t, r, admin), accessAlly, "the ally's account was not created")

	pol, err := identityPolicyFrom(allowListKey(`"`+accessOwner+`"`), config.Snapshot())
	require.NoError(t, err)
	r.mod.idw.policy.bind(pol)

	rec := accessGet(t, r, "ally")
	require.True(t, refused(rec.Code), "a linked subject taken off the list still got in: %d %s", rec.Code, rec.Body.String())
	require.Equal(t, http.StatusOK, accessGet(t, r, "owner").Code, "the narrowed list refused a person still on it")
}
