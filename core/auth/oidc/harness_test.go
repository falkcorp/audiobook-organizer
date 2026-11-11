// file: core/auth/oidc/harness_test.go
// version: 1.0.0
// guid: dd9ce6ac-7afd-4bff-9c9d-232b9e683153
// last-edited: 2026-11-11

package oidc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

const (
	callbackBase = "https://signal.example.test"
	appURI       = "batsignal://callback"
	appURI2      = "batsignal://other"
)

// keyFake is a KeyDeriver that records the info it was asked for.
type keyFake struct {
	mu    sync.Mutex
	infos []string
	key   []byte
	err   error
}

func (k *keyFake) DeriveKey(info string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.infos = append(k.infos, info)
	return k.key, k.err
}

func fixedKey(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

// handoffFake is the module's native-app hand-off.
type handoffFake struct {
	mu      sync.Mutex
	allowed map[string]bool
	mintErr error
	// location overrides what Mint returns; empty builds redirectURI?code=...
	location string
	minted   []mintCall
}

type mintCall struct {
	User                                    *auth.User
	Email, Challenge, RedirectURI, AppState string
}

func (h *handoffFake) Allowed(uri string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.allowed[uri]
}

func (h *handoffFake) Mint(_ context.Context, u *auth.User, email, challenge, redirectURI, appState string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.minted = append(h.minted, mintCall{u, email, challenge, redirectURI, appState})
	if h.mintErr != nil {
		return "", h.mintErr
	}
	if h.location != "" {
		return h.location, nil
	}
	return redirectURI + "?code=one-time-code&state=" + appState, nil
}

func (h *handoffFake) mints() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.minted)
}

func (h *handoffFake) revoke(uri string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.allowed, uri)
}

// lockedBuf is a log sink safe for the concurrent flow.
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// hx is a Flow over the fixture with a fake OIDC provider ("gotham", kind
// oidc) and a fake GitHub ("hub", kind github).
type hx struct {
	*fx
	t       testing.TB
	flow    *Flow
	idp     *idp
	gh      *gh
	handoff *handoffFake
	keys    *keyFake
	logs    *lockedBuf

	mu       sync.Mutex
	settings Settings
	hasUsers func() bool
}

type hxOption func(*hx)

func withHasUsers(f func() bool) hxOption { return func(h *hx) { h.hasUsers = f } }

func withoutHandoff() hxOption { return func(h *hx) { h.handoff = nil } }

func newHX(t testing.TB, opts ...hxOption) *hx {
	t.Helper()
	f := newFx(t)
	h := &hx{
		fx: f, t: t, idp: newIDP(t), gh: newGH(t), keys: &keyFake{key: fixedKey(7)}, logs: &lockedBuf{},
		handoff: &handoffFake{allowed: map[string]bool{appURI: true, appURI2: true}},
	}
	h.hasUsers = f.svc.HasUsers
	for _, o := range opts {
		o(h)
	}
	h.settings = Settings{
		RedirectBaseURL: callbackBase,
		Providers: []ProviderConfig{
			{ID: "gotham", Kind: KindOIDC, Issuer: h.idp.srv.URL, ClientID: testClientID, ClientSecret: testClientSecret,
				AllowedDomains: []string{"wayne.example.test"}},
			{ID: "hub", Kind: KindGitHub, ClientID: testClientID, ClientSecret: testClientSecret,
				AllowedDomains: []string{"wayne.example.test"}},
		},
	}
	// A first account so sign-in is not first-run.
	f.put(&auth.User{ID: "01JBATCAVE0000000000000001", Username: "alfred", Email: "alfred@wayne.example.test", Roles: []rbac.RoleName{rbac.RoleAdmin}})
	var deps Deps
	deps = Deps{
		Settings: func() Settings { h.mu.Lock(); defer h.mu.Unlock(); return h.settings },
		Keys:     h.keys, Resolver: f.resolver(nil), Sessions: f.svc,
		HasUsers: func() bool { return h.hasUsers() }, HTTPClient: h.idp.srv.Client(),
		Log: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Clock: f.clock,
		newProvider: func(ctx context.Context, cfg ProviderConfig, hc *http.Client) (Provider, error) {
			if cfg.Kind == KindGitHub {
				return newGitHub(cfg, hc, h.gh.urls()), nil
			}
			return newOIDC(ctx, cfg, hc)
		},
	}
	if h.handoff != nil {
		deps.Handoff = h.handoff
	}
	var err error
	if h.flow, err = New(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *hx) setSettings(fn func(*Settings)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(&h.settings)
}

// setProvider edits one provider's config.
func (h *hx) setProvider(id string, fn func(*ProviderConfig)) {
	h.setSettings(func(s *Settings) {
		for i := range s.Providers {
			if s.Providers[i].ID == id {
				fn(&s.Providers[i])
			}
		}
	})
}

// run does Start then, with the IdP told what to issue, Callback.
type outcome struct {
	started *Started
	done    *Completed
	err     error
	code    string
}

// start begins a sign-in on provider and registers the code the IdP will hand
// back. tok is what the provider's token endpoint yields for it (an oidc token
// is given the sign-in's nonce unless tok.Nonce is already set).
func (h *hx) start(provider string, req StartRequest, tok idpToken) (*Started, string) {
	h.t.Helper()
	req.Provider = provider
	st, err := h.flow.Start(context.Background(), req)
	if err != nil {
		h.t.Fatalf("Start: %v", err)
	}
	code := "code-" + provider + "-" + tok.Sub + "-" + st.Binding[:8]
	if provider == "hub" {
		h.gh.issue(code, st.Location)
		return st, code
	}
	if tok.Nonce == "" {
		tok.Nonce = queryParam(h.t, st.Location, "nonce")
	}
	h.idp.issue(code, st.Location, tok)
	return st, code
}

// callback finishes a sign-in with the state a real provider would echo.
func (h *hx) callback(provider string, st *Started, code string) (*Completed, error) {
	return h.flow.Callback(context.Background(), CallbackRequest{
		Provider: provider, Code: code, State: queryParam(h.t, st.Location, "state"), Binding: st.Binding,
	})
}

func (h *hx) signIn(provider string, req StartRequest, tok idpToken) outcome {
	h.t.Helper()
	st, code := h.start(provider, req, tok)
	done, err := h.callback(provider, st, code)
	return outcome{started: st, done: done, err: err, code: code}
}

// wayne is an allowed Batman-family OIDC identity.
func wayne(sub, email string) idpToken {
	return idpToken{Sub: sub, Email: email, EmailVerified: true, Name: "Bruce Wayne"}
}

func mustIs(t testing.TB, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func mustNot(t testing.TB, haystack string, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			t.Fatalf("output leaked %q:\n%s", n, haystack)
		}
	}
}

func newTextLogger(w *lockedBuf) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func newTestKey(t testing.TB) (*rsa.PrivateKey, error) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, nil
}
