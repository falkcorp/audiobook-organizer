// file: internal/server/middleware/abs_caller_key_test.go
// version: 1.0.0
// guid: 8b3e1d47-2c95-4f60-a7d8-5e9c0b1f6a23
// last-edited: 2026-10-19

package middleware

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
	"github.com/falkcorp/audiobook-organizer/internal/server/absauth"
)

// verifierFunc adapts a function to CFAssertionVerifier.
type verifierFunc func(raw string) (*oauth.IdentityClaims, error)

func (f verifierFunc) Verify(_ context.Context, raw string) (*oauth.IdentityClaims, error) {
	return f(raw)
}

func callerKeyContext(assertion string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/auth/openid", nil)
	if assertion != "" {
		c.Request.Header.Set(oauth.CFAccessHeader, assertion)
	}
	return c
}

// CFCallerKey names the VERIFIED credential and provisions nobody: it backs
// SSO hop 1's per-caller quota, which must not create accounts and must not be
// keyable by a forged header.
func TestCFCallerKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg, err := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: absTestSecret, AuthModes: "cf,jwt"})
	if err != nil {
		t.Fatal(err)
	}
	store := newFakeABSStore()
	v := &fakeCFVerifier{
		byToken: map[string]*oauth.IdentityClaims{
			"person": {Provider: oauth.ProviderCFAccess, Subject: "cf|p", Email: "Owner@Example.com", EmailVerified: true},
		},
	}
	svc := &oauth.NonIdentityAssertionError{CommonName: "family"}
	r := NewABSIdentityResolver(cfg, verifierFunc(func(raw string) (*oauth.IdentityClaims, error) {
		if raw == "svc" {
			return nil, svc
		}
		if raw == "svc-no-cn" {
			return nil, &oauth.NonIdentityAssertionError{Subject: "s"}
		}
		return v.Verify(context.Background(), raw)
	}), oauth.New(oauth.Config{AllowedEmails: []string{"owner@example.com"}}), store)

	cases := []struct {
		assertion, want string
		ok              bool
	}{
		{"person", "email:owner@example.com", true},
		{"svc", "svc:family", true},
		{"svc-no-cn", "", false},
		{"forged", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		c := callerKeyContext(tc.assertion)
		got, ok := r.CFCallerKey(c)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CFCallerKey(%q) = %q,%v; want %q,%v", tc.assertion, got, ok, tc.want, tc.ok)
		}
		if tc.assertion == "svc" && ABSServiceToken(c) != "family" {
			t.Error("a service-token key must also record the token for the audit line")
		}
	}
	if len(store.users) != 0 {
		t.Fatalf("CFCallerKey provisioned %d users; it must only verify", len(store.users))
	}

	jwtOnly, _ := absauth.Load(absauth.Settings{Enabled: true, JWTSecret: absTestSecret, AuthModes: "jwt"})
	if _, ok := NewABSIdentityResolver(jwtOnly, v, oauth.New(oauth.Config{}), store).CFCallerKey(callerKeyContext("person")); ok {
		t.Fatal("with cf mode off there is no verified caller key")
	}
}

// AllowKey shares the per-IP buckets but under an arbitrary key, so callers
// behind one proxy address can be limited independently.
func TestIPRateLimiter_AllowKeyIsPerKey(t *testing.T) {
	l := NewIPRateLimiter(1, 2)
	if !l.AllowKey("a") || !l.AllowKey("a") {
		t.Fatal("the burst must be allowed")
	}
	if l.AllowKey("a") {
		t.Fatal("over the burst must be refused")
	}
	if !l.AllowKey("b") {
		t.Fatal("another key must have its own bucket")
	}
}
