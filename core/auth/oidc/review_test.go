// file: core/auth/oidc/review_test.go
// version: 1.0.0
// guid: 602d69a3-19d6-41e4-a35b-9b55bffac1d9
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// NormalizeEmail folds compatibility characters onto ASCII (the Kelvin sign,
// the long s, fullwidth letters). A claim that is not already plain ASCII is a
// different account at the provider, so it must never link, displace or be
// admitted as the ASCII address it folds to.
func TestOIDC_NonPlainAddressNeverActsAsTheAsciiAddress(t *testing.T) {
	bad := []struct{ name, email string }{
		{"kelvin sign", "Kate@wayne.example.test"},
		{"long s", "kſate@wayne.example.test"},
		{"fullwidth", "ｋate@wayne.example.test"},
		{"leading space", " kate@wayne.example.test"},
		{"trailing space", "kate@wayne.example.test "},
		{"control", "kate\u0001@wayne.example.test"},
		{"embedded newline", "kate@wayne.example.test\nbcc@arkham.example.test"},
	}
	for _, tc := range bad {
		t.Run("link/"+tc.name, func(t *testing.T) {
			f := newFx(t)
			holder := f.put(&auth.User{ID: "01JBATCAVE0000000000000020", Username: "kate", Email: "kate@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
			_, err := resolve(t, f, trusting(), claims("sub-impostor", tc.email))
			mustIs(t, err, ErrEmailNotPlain)
			if _, err := f.identity("gotham", "sub-impostor"); !errors.Is(err, auth.ErrNotFound) {
				t.Fatalf("identity linked to the holder: %v", err)
			}
			if f.user(string(holder.ID)).Version != holder.Version || f.countUsers() != 1 {
				t.Fatal("the holder changed or an account appeared")
			}
		})
		t.Run("displace/"+tc.name, func(t *testing.T) {
			f := newFx(t)
			sq := f.put(&auth.User{ID: "01JBATCAVE0000000000000021", Username: "squatter", Email: "kate@wayne.example.test", Roles: []rbac.RoleName{rbac.RoleViewer}})
			_, err := resolve(t, f, trusting(), claims("sub-impostor", tc.email))
			mustIs(t, err, ErrEmailNotPlain)
			if got := f.user(string(sq.ID)); got.Email != "kate@wayne.example.test" {
				t.Fatalf("the squatter lost the address to a non-plain claim: %+v", got)
			}
			for _, a := range f.audit.actions() {
				if a == "core.identity.displace" {
					t.Fatal("displaced on a non-plain claim")
				}
			}
		})
		t.Run("admission/"+tc.name, func(t *testing.T) {
			f := newFx(t)
			r := f.resolver(func(_ context.Context, email string) (bool, error) { return email == "kate@wayne.example.test", nil })
			_, err := r.Resolve(context.Background(), Policy{Provider: "gotham"}, claims("sub-impostor", tc.email))
			mustIs(t, err, ErrEmailNotPlain)
			if f.countUsers() != 0 {
				t.Fatal("an account was created for a non-plain claim")
			}
		})
	}
	// The control: only case differs, which is the rule, and it still works.
	t.Run("upper case ASCII is the same address", func(t *testing.T) {
		f := newFx(t)
		holder := f.put(&auth.User{ID: "01JBATCAVE0000000000000022", Username: "kate", Email: "kate@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
		u, err := resolve(t, f, trusting(), claims("sub-kate", "Kate@Wayne.Example.TEST"))
		if err != nil {
			t.Fatal(err)
		}
		if u.ID != holder.ID {
			t.Fatalf("signed in %s, want %s", u.ID, holder.ID)
		}
	})
	t.Run("an unverified non-plain claim is unverified first", func(t *testing.T) {
		f := newFx(t)
		c := claims("sub-x", "Kate@wayne.example.test")
		c.EmailVerified = false
		_, err := resolve(t, f, trusting(), c)
		mustIs(t, err, ErrEmailNotVerified)
	})
}

// The signed binding carries the app's state in a cookie, so every field in it
// is bounded and Start refuses what would not fit.
func TestOIDC_BindingSizeIsBounded(t *testing.T) {
	challenge := strings.Repeat("A", 43)
	long := appURI + "/" + strings.Repeat("a", maxAppRedirect-len(appURI)-1)
	if len(long) != maxAppRedirect {
		t.Fatalf("test URI is %d bytes", len(long))
	}
	start := func(h *hx, uri, state string) (*Started, error) {
		return h.flow.Start(context.Background(), StartRequest{
			Provider: "gotham", RedirectURI: uri, CodeChallenge: challenge, CodeChallengeMethod: "S256", AppState: state,
		})
	}
	t.Run("the largest legal request fits with room to spare", func(t *testing.T) {
		h := newHX(t)
		h.handoff.allowed[long] = true
		st, err := start(h, long, strings.Repeat("x", MaxAppState))
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Binding) >= maxBinding {
			t.Fatalf("binding is %d bytes, bound %d", len(st.Binding), maxBinding)
		}
	})
	t.Run("one byte over the app state cap", func(t *testing.T) {
		h := newHX(t)
		_, err := start(h, appURI, strings.Repeat("x", MaxAppState+1))
		mustIs(t, err, ErrAppStateTooLong)
	})
	t.Run("a state that JSON-escapes past the binding bound is refused, never issued", func(t *testing.T) {
		h := newHX(t)
		h.handoff.allowed[long] = true
		st, err := start(h, long, strings.Repeat("\x01", MaxAppState))
		if err == nil {
			if len(st.Binding) >= maxBinding {
				t.Fatalf("issued a %d byte binding", len(st.Binding))
			}
			return
		}
		mustIs(t, err, ErrAppStateTooLong)
	})
	t.Run("a redirect URI over its cap is not a listed target", func(t *testing.T) {
		h := newHX(t)
		over := long + "a"
		h.handoff.allowed[over] = true
		_, err := start(h, over, "s")
		mustIs(t, err, ErrBadRedirect)
	})
}

// A discovery document that names a plain-http endpoint would send the code,
// the PKCE verifier and the client secret in the clear.
func TestOIDC_DiscoveryEndpointsMustBeHTTPS(t *testing.T) {
	for _, field := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		t.Run(field, func(t *testing.T) {
			p := newIDP(t)
			p.mu.Lock()
			p.discovery = func(d map[string]any) { d[field] = strings.Replace(d[field].(string), "https://", "http://", 1) }
			p.mu.Unlock()
			_, err := newOIDC(context.Background(), ProviderConfig{ID: "gotham", Kind: KindOIDC, Issuer: p.srv.URL, ClientID: testClientID, ClientSecret: testClientSecret}, p.srv.Client())
			if err == nil || !strings.Contains(err.Error(), "not https") {
				t.Fatalf("err = %v, want a refusal naming https", err)
			}
			mustNot(t, err.Error(), testClientSecret)
		})
	}
	t.Run("https endpoints are accepted", func(t *testing.T) {
		p := newIDP(t)
		if _, err := newOIDC(context.Background(), ProviderConfig{ID: "gotham", Kind: KindOIDC, Issuer: p.srv.URL, ClientID: testClientID, ClientSecret: testClientSecret}, p.srv.Client()); err != nil {
			t.Fatal(err)
		}
	})
	for _, raw := range []string{"http://x.example.test/a", "https://", "https://u:p@x.example.test/a", "//x.example.test/a", "", "javascript:1"} {
		if httpsURL(raw) {
			t.Errorf("httpsURL(%q) = true", raw)
		}
	}
}

// Every refusal after the exchange is audited as core.auth.login (05-rbac 7.1:
// success and failure share the action), carries the class of the refusal and
// the provider, and never an address, token, code or target.
func TestOIDC_FailedSignInIsAudited(t *testing.T) {
	logins := func(h *hx) []string {
		var out []string
		for _, e := range h.audit.emitted {
			if e.Action == "core.auth.login" {
				out = append(out, string(e.Outcome)+":"+e.Reason)
			}
		}
		return out
	}
	app := StartRequest{RedirectURI: appURI, CodeChallenge: strings.Repeat("B", 43), CodeChallengeMethod: "S256", AppState: "s"}
	for _, tc := range []struct {
		name string
		req  StartRequest
		tok  idpToken
		prep func(*hx)
		want string
	}{
		{"unverified address", StartRequest{}, idpToken{Sub: "sub-x", Email: "dick@wayne.example.test", EmailVerified: false}, nil, "failure:email_not_verified"},
		{"address outside the allowed domains", StartRequest{}, wayne("sub-x", "joker@arkham.example.test"), nil, "failure:email_not_allowed"},
		{"non-plain address", StartRequest{}, wayne("sub-x", "Kate@wayne.example.test"), nil, "failure:email_not_plain"},
		{"app hand-off failed", app, wayne("sub-x", "dick@wayne.example.test"), func(h *hx) { h.handoff.mintErr = fmt.Errorf("code store down") }, "failure:app_handoff_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHX(t)
			h.flow.d.Audit = h.audit
			if tc.prep != nil {
				tc.prep(h)
			}
			o := h.signIn("gotham", tc.req, tc.tok)
			if o.err == nil {
				t.Fatal("sign-in succeeded")
			}
			if got := logins(h); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("login events = %v, want [%s]", got, tc.want)
			}
			for _, e := range h.audit.emitted {
				if e.Action != "core.auth.login" {
					continue
				}
				mustNot(t, fmt.Sprint(e.Details)+e.Reason, "wayne.example", "arkham", o.code, o.started.Binding, appURI)
			}
		})
	}
	t.Run("exchange failure", func(t *testing.T) {
		h := newHX(t)
		h.flow.d.Audit = h.audit
		st, _ := h.start("gotham", StartRequest{}, wayne("sub-x", "dick@wayne.example.test"))
		_, err := h.callback("gotham", st, "not-a-real-code")
		mustIs(t, err, ErrExchange)
		if got := logins(h); len(got) != 1 || got[0] != "failure:exchange_failed" {
			t.Fatalf("login events = %v", got)
		}
	})
	t.Run("a held address", func(t *testing.T) {
		h := newHX(t)
		h.flow.d.Audit = h.audit
		h.put(&auth.User{ID: "01JBATCAVE0000000000000030", Username: "lucius", Email: "lucius@wayne.example.test", EmailVerified: true, Roles: []rbac.RoleName{rbac.RoleViewer}})
		o := h.signIn("gotham", StartRequest{}, wayne("sub-x", "lucius@wayne.example.test"))
		mustIs(t, o.err, auth.ErrEmailInUse)
		if got := logins(h); len(got) != 1 || got[0] != "failure:email_in_use" {
			t.Fatalf("login events = %v", got)
		}
	})
	// A refusal that is not about the identity is not a failed login.
	t.Run("state, code and provider refusals are not logins", func(t *testing.T) {
		h := newHX(t)
		h.flow.d.Audit = h.audit
		st, code := h.start("gotham", StartRequest{}, wayne("sub-x", "dick@wayne.example.test"))
		_, _ = h.flow.Callback(context.Background(), CallbackRequest{Provider: "gotham", Code: code, State: "wrong", Binding: st.Binding})
		_, _ = h.callback("gotham", st, "")
		_, _ = h.flow.Callback(context.Background(), CallbackRequest{Provider: "gotham", Code: code, State: queryParam(t, st.Location, "state"), Binding: st.Binding, IdPError: "access_denied"})
		if got := logins(h); len(got) != 0 {
			t.Fatalf("login events = %v, want none", got)
		}
	})
}
