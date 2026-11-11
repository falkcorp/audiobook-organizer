// file: core/auth/oidc/state_test.go
// version: 1.0.0
// guid: 9ec7485c-f998-42bd-8483-97a224040976
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
	"golang.org/x/oauth2"
)

func TestNewStateCodecKeys(t *testing.T) {
	if _, err := NewStateCodec(make([]byte, 31)); err == nil {
		t.Fatal("a 31 byte key was accepted")
	}
	if _, err := NewStateCodec(make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	k := &keyFake{key: fixedKey(9)}
	if _, err := NewStateCodecFrom(k); err != nil {
		t.Fatal(err)
	}
	if len(k.infos) != 1 || k.infos[0] != StateKeyInfo || StateKeyInfo != "mediacore/auth/oidc-state/v1" {
		t.Fatalf("derived under %v, want exactly the pinned info string", k.infos)
	}
	if _, err := NewStateCodecFrom(&keyFake{err: errors.New("sealed")}); err == nil {
		t.Fatal("a failed derivation was accepted")
	}
	if _, err := NewStateCodecFrom(&keyFake{key: []byte("short")}); err == nil {
		t.Fatal("a short derived key was accepted")
	}
}

// RFC 7636 appendix B.
func TestCodeChallengeS256Vector(t *testing.T) {
	const verifier, want = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := CodeChallengeS256(verifier); got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestRandTokenIsRandom(t *testing.T) {
	a, err := randToken(24)
	b, err2 := randToken(24)
	if err != nil || err2 != nil || a == b || len(a) != 32 {
		t.Fatalf("tokens %q %q (%v, %v)", a, b, err, err2)
	}
}

func TestConsumedSetIsSingleUseAndBounded(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	c := newConsumed()
	if !c.take("a", now, now.Add(time.Minute)) {
		t.Fatal("first use refused")
	}
	if c.take("a", now.Add(time.Second), now.Add(time.Minute)) {
		t.Fatal("second use accepted")
	}
	if !c.take("a", now.Add(2*time.Minute), now.Add(3*time.Minute)) {
		t.Fatal("an expired entry must not block a reused id forever")
	}
	// Full of live ids: the set stays at its bound, the newest is spent, and
	// the one expiring first is the one dropped.
	c = newConsumed()
	c.max = 3
	for i, id := range []string{"x1", "x2", "x3"} {
		if !c.take(id, now, now.Add(time.Duration(i+1)*time.Minute)) {
			t.Fatal(id)
		}
	}
	if !c.take("x4", now, now.Add(4*time.Minute)) {
		t.Fatal("x4 refused")
	}
	if len(c.ids) != 3 {
		t.Fatalf("set grew to %d, want 3", len(c.ids))
	}
	if _, ok := c.ids["x1"]; ok {
		t.Fatal("the entry expiring first was not the one evicted")
	}
	if c.take("x4", now, now.Add(4*time.Minute)) {
		t.Fatal("x4 reusable")
	}
}

func TestErrClassStripsProviderText(t *testing.T) {
	re := &oauth2.RetrieveError{
		Response:  &http.Response{StatusCode: 400},
		Body:      []byte("client_secret=" + testClientSecret),
		ErrorCode: "invalid_grant",
	}
	got := errClass(fmt.Errorf("wrap: %w", re)).Error()
	mustNot(t, got, testClientSecret, "wrap")
	if want := "the token endpoint refused the request (status 400, error invalid_grant)"; got != want {
		t.Fatalf("errClass = %q, want %q", got, want)
	}
	re.ErrorCode = "bad code with spaces and \"quotes\""
	if got := errClass(re).Error(); got != "the token endpoint refused the request (status 400, error unspecified)" {
		t.Fatalf("errClass = %q", got)
	}
	re.ErrorCode = ""
	if got := errClass(re).Error(); got != "the token endpoint refused the request (status 400, error unspecified)" {
		t.Fatalf("errClass = %q", got)
	}
	if got := errClass(context.DeadlineExceeded).Error(); got != "timed out" {
		t.Fatalf("errClass = %q", got)
	}
	if got := errClass(context.Canceled).Error(); got != "canceled" {
		t.Fatalf("errClass = %q", got)
	}
	if got := errClass(errors.New("dial https://idp.example.test/token?code=abc")).Error(); got != "the provider could not be reached or answered unexpectedly" {
		t.Fatalf("errClass = %q", got)
	}
	if errClass(nil) != nil {
		t.Fatal("errClass(nil) != nil")
	}
}

func TestNewFlowValidatesDeps(t *testing.T) {
	h := newHX(t)
	base := Deps{Settings: func() Settings { return Settings{} }, Keys: h.keys, Resolver: h.fx.resolver(nil), Sessions: h.fx.svc}
	if _, err := New(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Deps){
		"settings": func(d *Deps) { d.Settings = nil },
		"keys":     func(d *Deps) { d.Keys = nil },
		"resolver": func(d *Deps) { d.Resolver = nil },
		"sessions": func(d *Deps) { d.Sessions = nil },
		"key err":  func(d *Deps) { d.Keys = &keyFake{err: errors.New("sealed")} },
	} {
		d := base
		mod(&d)
		if _, err := New(context.Background(), d); err == nil {
			t.Errorf("New without %s succeeded", name)
		}
	}
}

func TestBuildProviderKinds(t *testing.T) {
	p, err := buildProvider(context.Background(), ProviderConfig{ID: "hub", Kind: KindGitHub, ClientID: "c", ClientSecret: "s"}, http.DefaultClient)
	if err != nil || p.ID() != "hub" {
		t.Fatalf("github: %v, %v", p, err)
	}
	if _, err := buildProvider(context.Background(), ProviderConfig{ID: "x", Kind: "saml"}, http.DefaultClient); err == nil {
		t.Fatal("an unknown kind built")
	}
	h := newHX(t)
	op, err := newOIDC(context.Background(), ProviderConfig{ID: "gotham", Kind: KindOIDC, Issuer: h.idp.srv.URL, ClientID: testClientID, ClientSecret: testClientSecret}, h.idp.srv.Client())
	if err != nil || op.ID() != "gotham" {
		t.Fatalf("oidc: %v, %v", op, err)
	}
	// Scopes: openid is always present for an OIDC provider; a configured list
	// is kept and openid prepended; the defaults carry email.
	for _, tc := range []struct {
		scopes []string
		want   string
	}{
		{nil, "openid email profile"},
		{[]string{"email"}, "openid email"},
		{[]string{"openid", "groups"}, "openid groups"},
	} {
		cfg := op.(*oidcProvider)
		cfg.cfg.Scopes = tc.scopes
		if got := fmt.Sprint(cfg.oauthConfig("https://x.example.test/cb").Scopes); got != "["+tc.want+"]" {
			t.Errorf("scopes %v -> %s, want [%s]", tc.scopes, got, tc.want)
		}
	}
	gp := newGitHub(ProviderConfig{ID: "hub", Kind: KindGitHub, Scopes: []string{"read:user"}}, http.DefaultClient, defaultGitHub).(*githubProvider)
	if got := fmt.Sprint(gp.oauthConfig("https://x.example.test/cb").Scopes); got != "[read:user]" {
		t.Fatalf("github scopes = %s", got)
	}
}

// A linked account whose address another account now holds verified is not
// marked verified (two verified holders would break the e-mail rule).
func TestOIDC_ResolveDoesNotMarkVerifiedWhenAnotherHoldsIt(t *testing.T) {
	f := newFx(t)
	first, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	u := f.user(string(first.ID))
	u.EmailVerified, u.Version = false, u.Version+1
	f.put(u)
	f.put(&auth.User{ID: "01JBATCAVE0000000000000011", Username: "holder", Email: "dick@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
	got, err := resolve(t, f, untrusting(), claims("sub-robin", "dick@wayne.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID || f.user(string(first.ID)).EmailVerified {
		t.Fatalf("a second verified holder was created: %+v", got)
	}
}
