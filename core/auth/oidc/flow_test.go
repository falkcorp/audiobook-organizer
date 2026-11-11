// file: core/auth/oidc/flow_test.go
// version: 1.0.0
// guid: 20c9883a-eb19-464e-a862-def8f847560f
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestOIDC_PKCEForAllProviders: every provider kind sends a PKCE S256
// challenge, keeps the verifier out of the browser's URL, and redeems the
// code with the matching verifier. The fake providers enforce PKCE the way a
// real one does, so a completed sign-in proves the verifier was sent.
func TestOIDC_PKCEForAllProviders(t *testing.T) {
	for _, provider := range []string{"gotham", "hub"} {
		t.Run(provider, func(t *testing.T) {
			h := newHX(t)
			h.gh.emails = []ghEmail{{Email: "dick@wayne.example.test", Primary: true, Verified: true}}
			st, code := h.start(provider, StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))

			if got := queryParam(t, st.Location, "code_challenge_method"); got != "S256" {
				t.Fatalf("code_challenge_method = %q, want S256", got)
			}
			challenge := queryParam(t, st.Location, "code_challenge")
			if len(challenge) != 43 {
				t.Fatalf("code_challenge = %q, want a 43 character S256 value", challenge)
			}
			pl, err := h.flow.codec.Decode(st.Binding)
			if err != nil {
				t.Fatal(err)
			}
			if CodeChallengeS256(pl.Verifier) != challenge {
				t.Fatal("the challenge in the URL is not the S256 of the verifier in the state")
			}
			mustNot(t, st.Location, pl.Verifier)
			if provider == "gotham" && queryParam(t, st.Location, "nonce") != pl.Nonce {
				t.Fatal("the OIDC nonce in the URL is not the one in the state")
			}

			done, err := h.callback(provider, st, code)
			if err != nil {
				t.Fatalf("Callback: %v", err)
			}
			if done.Session == nil || done.User == nil {
				t.Fatalf("no session or user: %+v", done)
			}
			if provider == "gotham" {
				if got := h.idp.lastForm().Get("code_verifier"); got != pl.Verifier {
					t.Fatalf("token request code_verifier = %q, want the state's verifier", got)
				}
			}
		})
	}
}

// A provider that sees the wrong verifier refuses, and no account appears.
func TestOIDC_PKCEVerifierMismatchRefusedByProvider(t *testing.T) {
	h := newHX(t)
	st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
	other, _ := h.start("gotham", StartRequest{}, wayne("sub-nightwing", "dick@wayne.example.test"))
	// Redeem the first code with the second sign-in's state: a different
	// verifier, so the provider refuses the exchange.
	_, err := h.flow.Callback(context.Background(), CallbackRequest{
		Provider: "gotham", Code: code, State: queryParam(t, other.Location, "state"), Binding: other.Binding,
	})
	mustIs(t, err, ErrExchange)
	_ = st
	if n := h.countUsers(); n != 1 {
		t.Fatalf("users = %d, want only the first account", n)
	}
}

// TestOIDC_StartRedirectAllowlist: a native-app redirect_uri is accepted only
// when the hand-off lists it exactly, and only in a plain absolute form.
func TestOIDC_StartRedirectAllowlist(t *testing.T) {
	good := "S256" // placeholder so the table reads naturally
	challenge := strings.Repeat("A", 43)
	for _, tc := range []struct {
		name string
		uri  string
	}{
		{"unlisted", "batsignal://elsewhere"},
		{"listed plus suffix", appURI + "/extra"},
		{"listed plus query", appURI + "?x=1"},
		{"listed as prefix of a longer host", "batsignal://callback.evil.example"},
		{"different case", "BatSignal://callback"},
		{"userinfo", "batsignal://evil@callback"},
		{"fragment", appURI + "#frag"},
		{"backslash", "batsignal:\\\\callback"},
		{"space", "batsignal://call back"},
		{"newline", "batsignal://callback\nx"},
		{"https web url", "https://evil.example.test/steal"},
		{"scheme-relative", "//evil.example.test"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHX(t)
			_, err := h.flow.Start(context.Background(), StartRequest{
				Provider: "gotham", RedirectURI: tc.uri, CodeChallenge: challenge, CodeChallengeMethod: good,
			})
			mustIs(t, err, ErrBadRedirect)
		})
	}
	t.Run("no hand-off registered", func(t *testing.T) {
		h := newHX(t, withoutHandoff())
		_, err := h.flow.Start(context.Background(), StartRequest{
			Provider: "gotham", RedirectURI: appURI, CodeChallenge: challenge, CodeChallengeMethod: "S256",
		})
		mustIs(t, err, ErrBadRedirect)
	})
	t.Run("listed exactly is accepted", func(t *testing.T) {
		h := newHX(t)
		if _, err := h.flow.Start(context.Background(), StartRequest{
			Provider: "gotham", RedirectURI: appURI, CodeChallenge: challenge, CodeChallengeMethod: "S256",
		}); err != nil {
			t.Fatalf("Start: %v", err)
		}
	})
}

// S256 only: no plain, no default, and a challenge of PKCE's shape.
func TestOIDC_StartRequiresS256Challenge(t *testing.T) {
	good := strings.Repeat("A", 43)
	for _, tc := range []struct{ name, method, challenge string }{
		{"plain", "plain", good},
		{"missing method", "", good},
		{"lowercase s256", "s256", good},
		{"short challenge", "S256", strings.Repeat("A", 42)},
		{"overlong challenge", "S256", strings.Repeat("A", 129)},
		{"challenge with padding", "S256", strings.Repeat("A", 43) + "="},
		{"challenge with a slash", "S256", strings.Repeat("A", 42) + "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHX(t)
			_, err := h.flow.Start(context.Background(), StartRequest{
				Provider: "gotham", RedirectURI: appURI, CodeChallenge: tc.challenge, CodeChallengeMethod: tc.method,
			})
			mustIs(t, err, ErrBadChallenge)
		})
	}
}

// With only one of redirect_uri and code_challenge the sign-in is an ordinary
// web sign-in: a stray parameter on a shared link must not break it, and must
// not turn into an app hand-off.
func TestOIDC_StartHalfAppParametersIsWebSignIn(t *testing.T) {
	for _, req := range []StartRequest{
		{RedirectURI: "batsignal://not-listed"},
		{CodeChallenge: strings.Repeat("A", 43), CodeChallengeMethod: "S256"},
	} {
		h := newHX(t)
		o := h.signIn("gotham", req, wayne("sub-robin", "dick@wayne.example.test"))
		if o.err != nil {
			t.Fatalf("web sign-in with %+v: %v", req, o.err)
		}
		if o.done.Session == nil || o.done.Location != "" || h.handoff.mints() != 0 {
			t.Fatalf("a half app request must stay a web sign-in: %+v, mints %d", o.done, h.handoff.mints())
		}
	}
}

// TestOIDC_StateCarriesAppHandoffFields: the app's redirect_uri, S256
// challenge and state travel inside the signed state, and a tampered or stale
// state is refused.
func TestOIDC_StateCarriesAppHandoffFields(t *testing.T) {
	now := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	cur := now
	codec, err := NewStateCodec(fixedKey(3), WithClock(func() time.Time { return cur }))
	if err != nil {
		t.Fatal(err)
	}
	challenge := CodeChallengeS256("the-apps-own-verifier-kept-on-the-device")
	blob, err := codec.Encode(Payload{
		ID: "id1", State: "st1", Verifier: "v1", Nonce: "n1", Provider: "gotham",
		AppRedirectURI: appURI, AppChallenge: challenge, AppState: "app-opaque-state",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(blob)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.AppRedirectURI != appURI || got.AppChallenge != challenge || got.AppState != "app-opaque-state" {
		t.Fatalf("hand-off fields did not round-trip: %+v", got)
	}

	t.Run("tampered body", func(t *testing.T) {
		body, mac, _ := strings.Cut(blob, ".")
		forged, _ := codec.Encode(Payload{ID: "id1", State: "st1", Provider: "gotham", AppRedirectURI: "batsignal://evil", AppChallenge: challenge})
		forgedBody, _, _ := strings.Cut(forged, ".")
		_ = body
		_, err := codec.Decode(forgedBody + "." + mac)
		mustIs(t, err, ErrStateInvalid)
	})
	t.Run("tampered signature", func(t *testing.T) {
		flip := blob[:len(blob)-2] + map[bool]string{true: "AA", false: "BB"}[!strings.HasSuffix(blob, "AA")]
		_, err := codec.Decode(flip)
		mustIs(t, err, ErrStateInvalid)
	})
	t.Run("another key", func(t *testing.T) {
		other, _ := NewStateCodec(fixedKey(4), WithClock(func() time.Time { return cur }))
		_, err := other.Decode(blob)
		mustIs(t, err, ErrStateInvalid)
	})
	t.Run("exactly ten minutes old is accepted, one second more is not", func(t *testing.T) {
		cur = now.Add(StateTTL)
		if _, err := codec.Decode(blob); err != nil {
			t.Fatalf("a state exactly ttl old must still be valid: %v", err)
		}
		cur = now.Add(StateTTL + time.Second)
		_, err := codec.Decode(blob)
		mustIs(t, err, ErrStateInvalid)
		cur = now
	})
	t.Run("issued in the future", func(t *testing.T) {
		cur = now.Add(-futureSkew - time.Second)
		_, err := codec.Decode(blob)
		mustIs(t, err, ErrStateInvalid)
		cur = now.Add(-futureSkew)
		if _, err := codec.Decode(blob); err != nil {
			t.Fatalf("skew within the allowance is accepted: %v", err)
		}
		cur = now
	})
	t.Run("malformed", func(t *testing.T) {
		for _, bad := range []string{"", ".", "abc", "abc.", ".abc", "a.b.c", "!!!.???"} {
			if _, err := codec.Decode(bad); err == nil {
				t.Fatalf("Decode(%q) accepted", bad)
			}
		}
	})
	t.Run("a signed payload with no id or state is refused", func(t *testing.T) {
		for _, p := range []Payload{{State: "s"}, {ID: "i"}} {
			b, _ := codec.Encode(p)
			_, err := codec.Decode(b)
			mustIs(t, err, ErrStateInvalid)
		}
	})
}

// End to end: the flow's state carries the hand-off fields and the callback
// honours them, through the hand-off's Mint and not through a session.
func TestOIDC_CallbackAppHandoff(t *testing.T) {
	h := newHX(t)
	challenge := CodeChallengeS256("verifier-kept-on-the-bat-phone")
	o := h.signIn("gotham", StartRequest{
		RedirectURI: appURI, CodeChallenge: challenge, CodeChallengeMethod: "S256", AppState: "opaque-1",
		Return: "/ignored-for-apps",
	}, wayne("sub-robin", "dick@wayne.example.test"))
	if o.err != nil {
		t.Fatalf("Callback: %v", o.err)
	}
	if o.done.Session != nil {
		t.Fatal("an app hand-off must not open a web session")
	}
	if !strings.HasPrefix(o.done.Location, appURI+"?code=") {
		t.Fatalf("Location = %q, want the app's redirect_uri with a code", o.done.Location)
	}
	if h.handoff.mints() != 1 {
		t.Fatalf("mints = %d, want 1", h.handoff.mints())
	}
	m := h.handoff.minted[0]
	if m.Challenge != challenge || m.RedirectURI != appURI || m.AppState != "opaque-1" || m.Email != "dick@wayne.example.test" {
		t.Fatalf("Mint saw %+v", m)
	}
	if m.User == nil || m.User.ID != o.done.User.ID {
		t.Fatal("Mint was not given the resolved user")
	}
	// The challenge is the app's, not the server<->provider one.
	pl, _ := h.flow.codec.Decode(o.started.Binding)
	if m.Challenge == CodeChallengeS256(pl.Verifier) {
		t.Fatal("the app hop reused the provider verifier")
	}
}

func TestOIDC_CallbackAppHandoffRefusals(t *testing.T) {
	challenge := strings.Repeat("B", 43)
	app := StartRequest{RedirectURI: appURI, CodeChallenge: challenge, CodeChallengeMethod: "S256", AppState: "s"}
	for _, tc := range []struct {
		name string
		prep func(*hx)
		mint int
	}{
		{"location off the redirect_uri host", func(h *hx) { h.handoff.location = "batsignal://evil?code=x" }, 1},
		{"location on another scheme", func(h *hx) { h.handoff.location = "https://callback?code=x" }, 1},
		{"location on another path", func(h *hx) { h.handoff.location = appURI + "/deeper?code=x" }, 1},
		{"location with userinfo", func(h *hx) { h.handoff.location = "batsignal://evil@callback?code=x" }, 1},
		{"location with a fragment", func(h *hx) { h.handoff.location = appURI + "?code=x#frag" }, 1},
		{"mint failed", func(h *hx) { h.handoff.mintErr = fmt.Errorf("code store down: secret-detail-9") }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHX(t)
			tc.prep(h)
			o := h.signIn("gotham", app, wayne("sub-robin", "dick@wayne.example.test"))
			mustIs(t, o.err, ErrMintFailed)
			if o.done != nil {
				t.Fatalf("a refused hand-off returned %+v", o.done)
			}
			if h.handoff.mints() != tc.mint {
				t.Fatalf("mints = %d, want %d", h.handoff.mints(), tc.mint)
			}
			mustNot(t, o.err.Error(), "secret-detail-9")
		})
	}
	t.Run("allowlist changed between start and callback", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", app, wayne("sub-robin", "dick@wayne.example.test"))
		h.handoff.revoke(appURI)
		_, err := h.callback("gotham", st, code)
		mustIs(t, err, ErrMintFailed)
		if h.handoff.mints() != 0 {
			t.Fatal("a target the module no longer lists received a code")
		}
	})
}

// TestOIDC_CallbackStateChecks: the state is bound to the browser, the
// provider and the callback, and is spent by its first use.
func TestOIDC_CallbackStateChecks(t *testing.T) {
	t.Run("single use", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		if _, err := h.callback("gotham", st, code); err != nil {
			t.Fatal(err)
		}
		before := h.idp.calls()
		_, err := h.callback("gotham", st, code)
		mustIs(t, err, ErrStateInvalid)
		if h.idp.calls() != before {
			t.Fatal("a replayed state reached the token endpoint: the state must be spent before the exchange")
		}
	})
	t.Run("spent even when the exchange fails", func(t *testing.T) {
		h := newHX(t)
		st, _ := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.callback("gotham", st, "not-a-real-code")
		mustIs(t, err, ErrExchange)
		_, err = h.callback("gotham", st, "not-a-real-code")
		mustIs(t, err, ErrStateInvalid)
	})
	t.Run("state parameter must equal the cookie's", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.flow.Callback(context.Background(), CallbackRequest{Provider: "gotham", Code: code, State: "attacker-state", Binding: st.Binding})
		mustIs(t, err, ErrStateInvalid)
		_, err = h.flow.Callback(context.Background(), CallbackRequest{Provider: "gotham", Code: code, State: "", Binding: st.Binding})
		mustIs(t, err, ErrStateInvalid)
		if h.idp.calls() != 0 {
			t.Fatal("a CSRF'd callback reached the token endpoint")
		}
	})
	t.Run("missing binding", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.flow.Callback(context.Background(), CallbackRequest{Provider: "gotham", Code: code, State: queryParam(t, st.Location, "state")})
		mustIs(t, err, ErrStateInvalid)
	})
	t.Run("state minted for another provider", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.flow.Callback(context.Background(), CallbackRequest{
			Provider: "hub", Code: code, State: queryParam(t, st.Location, "state"), Binding: st.Binding,
		})
		mustIs(t, err, ErrStateInvalid)
	})
	t.Run("expired after ten minutes", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		h.advance(StateTTL + time.Second)
		_, err := h.callback("gotham", st, code)
		mustIs(t, err, ErrStateInvalid)
		if h.idp.calls() != 0 {
			t.Fatal("an expired state reached the token endpoint")
		}
	})
	t.Run("no code", func(t *testing.T) {
		h := newHX(t)
		st, _ := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.callback("gotham", st, "")
		mustIs(t, err, ErrNoCode)
	})
	t.Run("provider declined", func(t *testing.T) {
		h := newHX(t)
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.flow.Callback(context.Background(), CallbackRequest{
			Provider: "gotham", Code: code, State: queryParam(t, st.Location, "state"), Binding: st.Binding, IdPError: "access_denied",
		})
		mustIs(t, err, ErrDeniedByProvider)
	})
	t.Run("unknown provider", func(t *testing.T) {
		h := newHX(t)
		_, err := h.flow.Start(context.Background(), StartRequest{Provider: "joker"})
		mustIs(t, err, ErrUnknownProvider)
		_, err = h.flow.Callback(context.Background(), CallbackRequest{Provider: "joker"})
		mustIs(t, err, ErrUnknownProvider)
	})
	t.Run("first run refuses sign-in", func(t *testing.T) {
		h := newHX(t, withHasUsers(func() bool { return false }))
		st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		_, err := h.callback("gotham", st, code)
		mustIs(t, err, ErrSetupRequired)
		if h.idp.calls() != 0 || h.countUsers() != 1 {
			t.Fatal("setup-required must stop before the exchange and create nothing")
		}
	})
}

// TestOIDC_IDTokenVerification: signature, issuer, audience, expiry, nonce and
// the verified flag are all enforced, and a refused token creates no account.
func TestOIDC_IDTokenVerification(t *testing.T) {
	stranger, _ := newTestKey(t)
	for _, tc := range []struct {
		name string
		mod  func(*idpToken)
	}{
		{"wrong audience", func(k *idpToken) { k.Audience = "joker-client" }},
		{"wrong issuer", func(k *idpToken) { k.Issuer = "https://arkham.example.test" }},
		{"expired", func(k *idpToken) { k.Expiry = time.Now().Add(-time.Hour) }},
		{"signed by another key", func(k *idpToken) { k.SignWith = stranger }},
		{"nonce of another sign-in", func(k *idpToken) { k.Nonce = "replayed-from-elsewhere" }},
		{"no id_token at all", func(k *idpToken) { k.OmitIDToken = true }},
		{"no subject", func(k *idpToken) { k.Sub = "" }},
		{"email_verified as a string", func(k *idpToken) { k.EmailVerified = "true" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHX(t)
			tok := wayne("sub-robin", "dick@wayne.example.test")
			tc.mod(&tok)
			o := h.signIn("gotham", StartRequest{}, tok)
			mustIs(t, o.err, ErrExchange)
			if h.countUsers() != 1 {
				t.Fatal("a refused id_token created an account")
			}
			mustNot(t, o.err.Error(), testClientSecret, o.code)
		})
	}
	t.Run("email_verified false is refused at the resolver", func(t *testing.T) {
		h := newHX(t)
		tok := wayne("sub-robin", "dick@wayne.example.test")
		tok.EmailVerified = false
		o := h.signIn("gotham", StartRequest{}, tok)
		mustIs(t, o.err, ErrEmailNotVerified)
		if h.countUsers() != 1 {
			t.Fatal("an unverified address created an account")
		}
	})
	t.Run("the control: a valid token signs in", func(t *testing.T) {
		h := newHX(t)
		o := h.signIn("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
		if o.err != nil {
			t.Fatal(o.err)
		}
	})
}

// TestOIDC_NoSecretsInErrorsOrLogs: provider bodies, codes, tokens and the
// client secret never reach an error text or a log line.
func TestOIDC_NoSecretsInErrorsOrLogs(t *testing.T) {
	h := newHX(t)
	h.idp.failBody = "leaky body: client_secret=" + testClientSecret + " token=hunter-token-77"
	st, code := h.start("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
	_, err := h.callback("gotham", st, code)
	mustIs(t, err, ErrExchange)
	out := err.Error() + "\n" + h.logs.String()
	mustNot(t, out, testClientSecret, "hunter-token-77", code, "leaky body", st.Binding)

	// A good sign-in's logs carry no token either.
	h2 := newHX(t)
	o := h2.signIn("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	mustNot(t, h2.logs.String(), testClientSecret, "at-oidc-token", o.code, o.started.Binding, o.done.Session.Token)
}

func TestOIDC_ProviderConfigRendersWithoutSecret(t *testing.T) {
	cfg := ProviderConfig{ID: "gotham", Kind: KindOIDC, ClientID: testClientID, ClientSecret: testClientSecret}
	for _, s := range []string{fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), fmt.Sprint(cfg)} {
		mustNot(t, s, testClientSecret)
	}
	var b lockedBuf
	slog := newTextLogger(&b)
	slog.Info("provider", "cfg", cfg)
	mustNot(t, b.String(), testClientSecret)
	if !strings.Contains(b.String(), "gotham") {
		t.Fatalf("the log lost the provider id: %s", b.String())
	}
}

func TestProviderConfigValidate(t *testing.T) {
	ok := ProviderConfig{ID: "gotham", Kind: KindOIDC, Issuer: "https://idp.example.test", ClientID: "c", ClientSecret: "s"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*ProviderConfig){
		"uppercase id":       func(c *ProviderConfig) { c.ID = "Gotham" },
		"empty id":           func(c *ProviderConfig) { c.ID = "" },
		"reserved providers": func(c *ProviderConfig) { c.ID = "providers" },
		"reserved base":      func(c *ProviderConfig) { c.ID = "redirect_base_url" },
		"unknown kind":       func(c *ProviderConfig) { c.Kind = "saml" },
		"no client id":       func(c *ProviderConfig) { c.ClientID = "" },
		"no secret":          func(c *ProviderConfig) { c.ClientSecret = "" },
		"http issuer":        func(c *ProviderConfig) { c.Issuer = "http://idp.example.test" },
		"issuer userinfo":    func(c *ProviderConfig) { c.Issuer = "https://u:p@idp.example.test" },
		"issuer query":       func(c *ProviderConfig) { c.Issuer = "https://idp.example.test/?a=b" },
		"issuer fragment":    func(c *ProviderConfig) { c.Issuer = "https://idp.example.test/#a" },
		"no issuer":          func(c *ProviderConfig) { c.Issuer = "" },
	} {
		t.Run(name, func(t *testing.T) {
			c := ok
			mod(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("Validate accepted it")
			}
			mustNot(t, err.Error(), "s\u0000")
		})
	}
	gh := ProviderConfig{ID: "hub", Kind: KindGitHub, ClientID: "c", ClientSecret: "s"}
	if err := gh.Validate(); err != nil {
		t.Fatalf("a github provider needs no issuer: %v", err)
	}
}

// TestOIDC_RedirectURIFromConfigOnly: the callback URL the provider is given
// is built from core.auth.oidc.redirect_base_url, never from the request.
func TestOIDC_RedirectURIFromConfigOnly(t *testing.T) {
	h := newHX(t)
	st, _ := h.start("gotham", StartRequest{Return: "/x"}, wayne("sub-robin", "dick@wayne.example.test"))
	if got, want := queryParam(t, st.Location, "redirect_uri"), callbackBase+DefaultCallbackBase+"/gotham/callback"; got != want {
		t.Fatalf("redirect_uri = %q, want %q", got, want)
	}
	h.setSettings(func(s *Settings) { s.RedirectBaseURL = "http://signal.example.test" })
	_, err := h.flow.Start(context.Background(), StartRequest{Provider: "gotham"})
	mustIs(t, err, ErrNotConfigured)
	h.setSettings(func(s *Settings) { s.RedirectBaseURL = "http://localhost:8484/" })
	st, err = h.flow.Start(context.Background(), StartRequest{Provider: "gotham"})
	if err != nil {
		t.Fatalf("loopback http is allowed: %v", err)
	}
	if got := queryParam(t, st.Location, "redirect_uri"); !strings.HasPrefix(got, "http://localhost:8484"+DefaultCallbackBase+"/gotham/callback") {
		t.Fatalf("redirect_uri = %q", got)
	}
	h.setSettings(func(s *Settings) { s.RedirectBaseURL = "" })
	_, err = h.flow.Start(context.Background(), StartRequest{Provider: "gotham"})
	mustIs(t, err, ErrNotConfigured)
}

func TestOIDC_ReloadDropsRemovedAndBrokenProviders(t *testing.T) {
	h := newHX(t)
	if got := h.flow.Providers(); len(got) != 2 || got[0].ID != "gotham" || got[1].ID != "hub" || got[1].Kind != KindGitHub {
		t.Fatalf("Providers = %+v", got)
	}
	h.setSettings(func(s *Settings) {
		s.Providers = []ProviderConfig{
			s.Providers[1],
			{ID: "Bad Id", Kind: KindOIDC}, // invalid: skipped
			{ID: "dead", Kind: KindOIDC, Issuer: "https://127.0.0.1:1", ClientID: "c", ClientSecret: "s"}, // discovery fails: skipped
			s.Providers[1], // duplicate id: skipped
		}
	})
	h.flow.Reload(context.Background())
	if got := h.flow.Providers(); len(got) != 1 || got[0].ID != "hub" {
		t.Fatalf("Providers after Reload = %+v", got)
	}
	_, err := h.flow.Start(context.Background(), StartRequest{Provider: "gotham"})
	mustIs(t, err, ErrUnknownProvider)
	if !strings.Contains(h.logs.String(), "skipped") {
		t.Fatalf("a skipped provider must be logged: %s", h.logs.String())
	}
	mustNot(t, h.logs.String(), testClientSecret)
}

// TestOIDC_WebSignInReturnPath: an open redirect through the return parameter
// is dropped; a same-site path survives.
func TestOIDC_WebSignInReturnPath(t *testing.T) {
	for ret, want := range map[string]string{
		"/library?tab=new":             "/library?tab=new",
		"/":                            "/",
		"//evil.example.test":          "",
		"/\\evil.example.test":         "",
		"https://evil.example.test":    "",
		"javascript:alert(1)":          "",
		"relative/path":                "",
		"/ok\\path":                    "",
		"/line\nbreak":                 "",
		"":                             "",
		"/" + strings.Repeat("a", 600): "",
	} {
		if got := sanitizeReturn(ret); got != want {
			t.Errorf("sanitizeReturn(%q) = %q, want %q", ret, got, want)
		}
	}
	h := newHX(t)
	o := h.signIn("gotham", StartRequest{Return: "//evil.example.test"}, wayne("sub-robin", "dick@wayne.example.test"))
	if o.err != nil || o.done.Return != "" {
		t.Fatalf("return = %q, err %v", o.done.Return, o.err)
	}
	h = newHX(t)
	o = h.signIn("gotham", StartRequest{Return: "/library"}, wayne("sub-robin", "dick@wayne.example.test"))
	if o.err != nil || o.done.Return != "/library" {
		t.Fatalf("return = %q, err %v", o.done.Return, o.err)
	}
}

func TestSameTarget(t *testing.T) {
	for _, tc := range []struct {
		loc, target string
		want        bool
	}{
		{appURI + "?code=a&state=b", appURI, true},
		{"BATSIGNAL://CALLBACK?code=a", appURI, true},
		{"batsignal://callback/?code=a", appURI, false},
		{"batsignal://evil?code=a", appURI, false},
		{"https://callback?code=a", appURI, false},
		{"batsignal://u@callback?code=a", appURI, false},
		{appURI + "?code=a#f", appURI, false},
		{"", appURI, false},
		{"::bad::", appURI, false},
	} {
		if got := sameTarget(tc.loc, tc.target); got != tc.want {
			t.Errorf("sameTarget(%q, %q) = %v, want %v", tc.loc, tc.target, got, tc.want)
		}
	}
}

// GitHub: only a VERIFIED address ever signs anyone in; the primary one wins.
func TestOIDC_GitHubEmailSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		emails []ghEmail
		want   string
		err    error
	}{
		{"primary verified wins", []ghEmail{
			{Email: "a@wayne.example.test", Verified: true}, {Email: "dick@wayne.example.test", Primary: true, Verified: true},
		}, "dick@wayne.example.test", nil},
		{"falls back to any verified", []ghEmail{
			{Email: "x@wayne.example.test", Primary: true, Verified: false}, {Email: "tim@wayne.example.test", Verified: true},
		}, "tim@wayne.example.test", nil},
		{"only unverified", []ghEmail{{Email: "dick@wayne.example.test", Primary: true, Verified: false}}, "", ErrEmailNotVerified},
		{"none", nil, "", ErrEmailNotVerified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHX(t)
			h.gh.emails = tc.emails
			o := h.signIn("hub", StartRequest{}, idpToken{Sub: "gh"})
			if tc.err != nil {
				mustIs(t, o.err, tc.err)
				return
			}
			if o.err != nil {
				t.Fatal(o.err)
			}
			if o.done.User.Email != tc.want {
				t.Fatalf("email = %q, want %q", o.done.User.Email, tc.want)
			}
			if id, err := h.identity("hub", "1939"); err != nil || id.UserID != o.done.User.ID {
				t.Fatalf("identity (hub, 1939) = %+v, %v", id, err)
			}
		})
	}
	t.Run("token endpoint refusal", func(t *testing.T) {
		h := newHX(t)
		st, _ := h.start("hub", StartRequest{}, idpToken{})
		_, err := h.callback("hub", st, "never-issued")
		mustIs(t, err, ErrExchange)
	})
}

// A completed sign-in is audited (async Emit), a refused one is not.
func TestOIDC_SignInAudited(t *testing.T) {
	h := newHX(t)
	// The flow was built without an audit sink in the harness; build one.
	h.flow.d.Audit = h.audit
	o := h.signIn("gotham", StartRequest{}, wayne("sub-robin", "dick@wayne.example.test"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	if n := len(h.audit.emitted); n != 1 || h.audit.emitted[0].Action != "core.auth.login" {
		t.Fatalf("emitted = %+v", h.audit.emitted)
	}
	mustNot(t, fmt.Sprint(h.audit.emitted[0].Details), "dick@wayne", o.done.Session.Token)
}
