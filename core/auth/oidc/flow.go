// file: core/auth/oidc/flow.go
// version: 1.0.0
// guid: 58d48f93-f1a4-4c1d-872a-9b0960e6346c
// last-edited: 2026-11-11

package oidc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
)

// DefaultCallbackBase is the callback path prefix when an app sets none
// (auth.Options.OIDCCallbackBase empty): the callback is
// <DefaultCallbackBase>/{provider}/callback.
const DefaultCallbackBase = "/api/core/v1/auth/oidc"

// SessionIssuer opens the web session a finished sign-in ends in.
// *auth.Service is one.
type SessionIssuer interface {
	IssueSession(ctx context.Context, u *auth.User, origin auth.SessionOrigin, remember bool, client auth.ClientInfo) (*auth.IssuedSession, error)
}

// Deps are the flow's collaborators.
type Deps struct {
	// Settings is read on every request, so a provider-set change takes
	// effect at the next Reload and a redirect_base_url change at once.
	Settings func() Settings
	Keys     KeyDeriver
	Resolver *Resolver
	Sessions SessionIssuer
	// Handoff is the module's native-app hand-off (auth.MethodSpec.AppHandoff).
	// Nil: a start that asks for one is refused.
	Handoff auth.OIDCAppHandoff
	// HasUsers is Service.HasUsers; nil skips the first-run check. With no
	// user yet, a sign-in is refused: the first account comes from setup.
	HasUsers func() bool
	// CallbackBase is auth.Options.OIDCCallbackBase; empty means DefaultCallbackBase.
	CallbackBase string
	// Audit, when set, records each completed sign-in (asynchronously).
	Audit      audit.Logger
	HTTPClient *http.Client
	Log        *slog.Logger
	Clock      func() time.Time

	// newProvider builds one provider; tests point providers at fakes.
	newProvider func(ctx context.Context, cfg ProviderConfig, hc *http.Client) (Provider, error)
}

// Flow is the sign-in: Start sends a browser to a provider, Callback finishes
// what the provider sends back. It holds the enabled providers (Reload
// rebuilds them), the state codec and the set of spent states.
type Flow struct {
	d        Deps
	codec    *StateCodec
	spent    *consumed
	mu       sync.RWMutex
	provider map[string]Provider
	order    []string
}

// New builds a Flow and its first provider set. A provider that cannot be
// built (its discovery failed) is left out and logged: one dead provider
// must not disable the others.
func New(ctx context.Context, d Deps) (*Flow, error) {
	switch {
	case d.Settings == nil || d.Keys == nil || d.Resolver == nil || d.Sessions == nil:
		return nil, errors.New("oidc: a flow needs Settings, Keys, Resolver and Sessions")
	}
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	if d.CallbackBase == "" {
		d.CallbackBase = DefaultCallbackBase
	}
	d.CallbackBase = "/" + strings.Trim(d.CallbackBase, "/")
	if d.newProvider == nil {
		d.newProvider = buildProvider
	}
	codec, err := NewStateCodecFrom(d.Keys, WithClock(d.Clock))
	if err != nil {
		return nil, err
	}
	f := &Flow{d: d, codec: codec, spent: newConsumed(), provider: map[string]Provider{}}
	f.Reload(ctx)
	return f, nil
}

// buildProvider builds the provider of cfg's kind.
func buildProvider(ctx context.Context, cfg ProviderConfig, hc *http.Client) (Provider, error) {
	switch cfg.Kind {
	case KindGitHub:
		return newGitHub(cfg, hc, defaultGitHub), nil
	case KindOIDC:
		return newOIDC(ctx, cfg, hc)
	}
	return nil, fmt.Errorf("oidc: provider %s: unknown kind", cfg.ID)
}

// Reload rebuilds the provider set from Settings. An invalid or unbuildable
// entry is skipped and logged, never fatal, and never kept from the old set:
// a provider an operator removed or broke stops signing people in.
func (f *Flow) Reload(ctx context.Context) {
	set := f.d.Settings()
	next := map[string]Provider{}
	var order []string
	for _, cfg := range set.Providers {
		if err := cfg.Validate(); err != nil {
			f.d.Log.Warn("oidc: provider skipped", "provider", cfg, "err", err)
			continue
		}
		if _, dup := next[cfg.ID]; dup {
			f.d.Log.Warn("oidc: duplicate provider id skipped", "provider", cfg)
			continue
		}
		p, err := f.d.newProvider(ctx, cfg, f.d.HTTPClient)
		if err != nil {
			f.d.Log.Warn("oidc: provider unavailable", "provider", cfg, "err", err)
			continue
		}
		next[cfg.ID] = p
		order = append(order, cfg.ID)
	}
	f.mu.Lock()
	f.provider, f.order = next, order
	f.mu.Unlock()
}

// ProviderInfo is what the public provider list shows: ids and kinds, never
// a client id or a secret.
type ProviderInfo struct{ ID, Kind string }

// Providers lists the enabled providers (GET /auth/providers).
func (f *Flow) Providers() []ProviderInfo {
	set := f.d.Settings()
	f.mu.RLock()
	defer f.mu.RUnlock()
	var out []ProviderInfo
	for _, id := range f.order {
		for _, cfg := range set.Providers {
			if cfg.ID == id {
				out = append(out, ProviderInfo{ID: id, Kind: cfg.Kind})
				break
			}
		}
	}
	return out
}

// lookup returns the enabled provider id and its current configuration.
func (f *Flow) lookup(id string) (Provider, ProviderConfig, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	f.mu.RLock()
	p, ok := f.provider[id]
	f.mu.RUnlock()
	if !ok {
		return nil, ProviderConfig{}, ErrUnknownProvider
	}
	for _, cfg := range f.d.Settings().Providers {
		if cfg.ID == id {
			return p, cfg, nil
		}
	}
	return nil, ProviderConfig{}, ErrUnknownProvider // removed since the last Reload
}

// redirectURI is the provider's callback URL, built from configuration only.
func (f *Flow) redirectURI(id string) (string, error) {
	base := f.d.Settings().RedirectBaseURL
	if err := validBase(base); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotConfigured, err)
	}
	return strings.TrimRight(strings.TrimSpace(base), "/") + f.d.CallbackBase + "/" + id + "/callback", nil
}

// StartRequest is a sign-in's start parameters. The route merges the
// redirect_uri alias callback into RedirectURI.
type StartRequest struct {
	Provider string
	// Return is the post-login path; anything but a same-site absolute path
	// is dropped.
	Return              string
	RedirectURI         string // the native app's redirect_uri
	CodeChallenge       string // the app's PKCE challenge
	CodeChallengeMethod string
	AppState            string
}

// Started is a sign-in under way: send the browser to Location and keep
// Binding in a short-lived cookie (HttpOnly, SameSite=Lax: the callback is a
// top-level navigation from the provider's site, which Strict would drop), for
// MaxAge seconds. The callback needs it back: it ties the sign-in to the
// browser that began it.
type Started struct {
	Location string
	Binding  string
	MaxAge   int
}

var challengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

// Start begins a sign-in. With both of redirect_uri and code_challenge it is a
// native-app sign-in: the redirect_uri must be one the hand-off allows
// exactly, the method must be S256, and both travel inside the signed state, so
// the callback cannot be steered elsewhere. With only one of them it is an
// ordinary web sign-in and the stray parameter is ignored: a bare query
// parameter appended to a shared link must not break web sign-in for everyone.
func (f *Flow) Start(ctx context.Context, req StartRequest) (*Started, error) {
	p, cfg, err := f.lookup(req.Provider)
	if err != nil {
		return nil, err
	}
	redirectURI, err := f.redirectURI(cfg.ID)
	if err != nil {
		return nil, err
	}
	pl := Payload{Provider: cfg.ID, Return: sanitizeReturn(req.Return)}
	if app := strings.TrimSpace(req.RedirectURI); app != "" && strings.TrimSpace(req.CodeChallenge) != "" {
		challenge := strings.TrimSpace(req.CodeChallenge)
		if !f.appRedirectAllowed(app) {
			return nil, ErrBadRedirect
		}
		if req.CodeChallengeMethod != "S256" || !challengePattern.MatchString(challenge) {
			return nil, ErrBadChallenge
		}
		pl.AppRedirectURI, pl.AppChallenge, pl.AppState = app, challenge, req.AppState
	}
	for _, g := range []struct {
		dst *string
		n   int
	}{{&pl.ID, 16}, {&pl.State, 24}, {&pl.Verifier, 48}, {&pl.Nonce, 24}} {
		if *g.dst, err = randToken(g.n); err != nil {
			return nil, err
		}
	}
	blob, err := f.codec.Encode(pl)
	if err != nil {
		return nil, err
	}
	return &Started{
		Location: p.AuthURL(AuthRequest{
			State: pl.State, Nonce: pl.Nonce, Challenge: CodeChallengeS256(pl.Verifier), RedirectURI: redirectURI,
		}),
		Binding: blob,
		MaxAge:  int(StateTTL / time.Second),
	}, nil
}

// appRedirectAllowed is the hand-off's exact-match allowlist, plus what core
// will not leave to it: a plain absolute URI with a scheme, no userinfo, no
// fragment and no whitespace, control character or backslash (the shapes
// browsers and URL parsers disagree about).
func (f *Flow) appRedirectAllowed(raw string) bool {
	if f.d.Handoff == nil || len(raw) > 2048 || strings.ContainsAny(raw, " \t\r\n\\") {
		return false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	return f.d.Handoff.Allowed(raw)
}

// CallbackRequest is what the provider sent back and what the browser held.
type CallbackRequest struct {
	Provider string
	Code     string
	State    string // the callback's state query parameter
	Binding  string // the cookie Start's caller set
	// IdPError is the callback's error parameter, if any (a declined sign-in).
	IdPError string
	Client   auth.ClientInfo
}

// Completed is a finished sign-in. For a native-app hand-off Session is nil
// and the browser goes to Location (the app scheme, with its code and state):
// no web session exists. Otherwise Session is the new session and Return the
// same-site path to land on ("" means the root).
type Completed struct {
	User     *auth.User
	Session  *auth.IssuedSession
	Location string
	Return   string
}

// Callback finishes a sign-in. The caller clears the binding cookie whatever
// the outcome. Checks, in order: the provider's own error, the state (signed,
// fresh, for this provider, equal to the callback's state parameter, never
// used before), the code, the exchange (with the PKCE verifier and the
// nonce), then Resolver (admission, linking, status). Then either the app
// hand-off mints its code, before any session exists, or a session opens.
func (f *Flow) Callback(ctx context.Context, req CallbackRequest) (*Completed, error) {
	p, cfg, err := f.lookup(req.Provider)
	if err != nil {
		return nil, err
	}
	if req.IdPError != "" {
		return nil, ErrDeniedByProvider
	}
	pl, err := f.codec.Decode(req.Binding)
	if err != nil {
		return nil, err
	}
	if pl.Provider != cfg.ID || !sameSecret(pl.State, req.State) {
		return nil, fmt.Errorf("%w: not this sign-in", ErrStateInvalid)
	}
	now := f.d.Clock()
	if !f.spent.take(pl.ID, now, time.Unix(pl.IssuedAt, 0).Add(StateTTL+futureSkew)) {
		return nil, fmt.Errorf("%w: already used", ErrStateInvalid)
	}
	if req.Code == "" {
		return nil, ErrNoCode
	}
	if f.d.HasUsers != nil && !f.d.HasUsers() {
		return nil, ErrSetupRequired
	}
	redirectURI, err := f.redirectURI(cfg.ID)
	if err != nil {
		return nil, err
	}
	claims, err := p.Exchange(ctx, ExchangeRequest{Code: req.Code, Verifier: pl.Verifier, Nonce: pl.Nonce, RedirectURI: redirectURI})
	if err != nil {
		f.d.Log.Warn("oidc: code exchange failed", "provider", cfg.ID, "err", err)
		return nil, ErrExchange
	}
	user, err := f.d.Resolver.Resolve(ctx, cfg.Policy(), *claims)
	if err != nil {
		return nil, err
	}
	if pl.AppRedirectURI != "" && pl.AppChallenge != "" {
		return f.handoff(ctx, pl, user, claims.Email)
	}
	sess, err := f.d.Sessions.IssueSession(ctx, user, auth.OriginOIDC(cfg.ID), false, req.Client)
	if err != nil {
		return nil, err
	}
	f.signedIn(ctx, user, cfg.ID, "session")
	return &Completed{User: user, Session: sess, Return: pl.Return}, nil
}

// handoff is the callback's native-app branch.
func (f *Flow) handoff(ctx context.Context, pl *Payload, u *auth.User, email string) (*Completed, error) {
	// The state is signed, so the target cannot have been swapped, but the
	// allowlist is checked again: it is the module's to change, and a target
	// it no longer lists must not receive a code.
	if !f.appRedirectAllowed(pl.AppRedirectURI) {
		return nil, ErrMintFailed
	}
	loc, err := f.d.Handoff.Mint(ctx, u, email, pl.AppChallenge, pl.AppRedirectURI, pl.AppState)
	if err != nil {
		f.d.Log.Warn("oidc: app authorization code not issued", "provider", pl.Provider, "err", err)
		return nil, ErrMintFailed
	}
	if !sameTarget(loc, pl.AppRedirectURI) {
		f.d.Log.Warn("oidc: the hand-off returned a location off the app's redirect_uri", "provider", pl.Provider)
		return nil, ErrMintFailed
	}
	f.signedIn(ctx, u, pl.Provider, "app_handoff")
	return &Completed{User: u, Location: loc}, nil
}

// signedIn records a completed sign-in; async, so an audit failure never
// undoes it.
func (f *Flow) signedIn(ctx context.Context, u *auth.User, provider, via string) {
	if f.d.Audit == nil {
		return
	}
	f.d.Audit.Emit(ctx, audit.Event{
		Action: "core.auth.login", Outcome: audit.Success, Target: userRef(u.ID),
		Details: map[string]any{"origin": string(auth.OriginOIDC(provider)), "via": via},
	})
}

// sameTarget reports whether location is on the same scheme, host and path as
// target: a hand-off may add its code and state to the query, nothing else.
func sameTarget(location, target string) bool {
	l, err1 := url.Parse(location)
	t, err2 := url.Parse(target)
	if err1 != nil || err2 != nil || l.Scheme == "" || l.User != nil || l.Fragment != "" {
		return false
	}
	return strings.EqualFold(l.Scheme, t.Scheme) && strings.EqualFold(l.Host, t.Host) && l.Path == t.Path
}

// sanitizeReturn allows only a same-site absolute path (no open redirect): a
// single leading slash, so "//host" and "/\host" (browsers read a backslash as
// a slash) are refused, and no backslash anywhere.
func sanitizeReturn(ret string) string {
	if ret == "" || len(ret) > 512 || !strings.HasPrefix(ret, "/") || strings.ContainsRune(ret, '\\') {
		return ""
	}
	if len(ret) > 1 && (ret[1] == '/' || ret[1] == '\\') {
		return ""
	}
	if slices.ContainsFunc([]rune(ret), func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return ret
}
