// file: internal/oauth/providers.go
// version: 1.0.0
// guid: 9e3b1d75-2c48-4a06-8f19-5b0a7c2e6d43
// last-edited: 2026-07-26

package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	googleendpoint "golang.org/x/oauth2/google"
)

// Provider is one identity provider's authorize-URL + code-exchange flow. Exchange
// returns normalized, VERIFIED IdentityClaims (or an error). Implementations must
// only report EmailVerified=true when the IdP actually vouched for the address.
type Provider interface {
	Name() string
	AuthCodeURL(state, challenge, redirectURI string) string
	Exchange(ctx context.Context, code, verifier, redirectURI string) (*IdentityClaims, error)
}

// ---- GitHub (plain OAuth2, no OIDC; no PKCE support in the web flow) ----

type githubProvider struct {
	clientID     string
	clientSecret string
}

// NewGitHubProvider builds the GitHub OAuth2 provider.
func NewGitHubProvider(clientID, clientSecret string) Provider {
	return &githubProvider{clientID: clientID, clientSecret: clientSecret}
}

func (p *githubProvider) Name() string { return ProviderGitHub }

func (p *githubProvider) oauthConfig(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.clientID,
		ClientSecret: p.clientSecret,
		Endpoint:     github.Endpoint,
		RedirectURL:  redirectURI,
		Scopes:       []string{"read:user", "user:email"},
	}
}

func (p *githubProvider) AuthCodeURL(state, _ string, redirectURI string) string {
	// GitHub's web flow does not support PKCE; CSRF is covered by `state`.
	return p.oauthConfig(redirectURI).AuthCodeURL(state)
}

func (p *githubProvider) Exchange(ctx context.Context, code, _ string, redirectURI string) (*IdentityClaims, error) {
	cfg := p.oauthConfig(redirectURI)
	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("github: code exchange: %w", err)
	}
	client := cfg.Client(ctx, tok)

	// Basic profile (for the stable numeric id + name).
	var profile struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user", &profile); err != nil {
		return nil, fmt.Errorf("github: fetch user: %w", err)
	}

	// The primary VERIFIED email — never trust an unverified address.
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, "https://api.github.com/user/emails", &emails); err != nil {
		return nil, fmt.Errorf("github: fetch emails: %w", err)
	}
	email, verified := "", false
	for _, e := range emails {
		if e.Primary && e.Verified {
			email, verified = e.Email, true
			break
		}
	}
	if !verified {
		for _, e := range emails { // fall back to any verified email
			if e.Verified {
				email, verified = e.Email, true
				break
			}
		}
	}
	return &IdentityClaims{
		Provider:      ProviderGitHub,
		Subject:       fmt.Sprintf("%d", profile.ID),
		Email:         email,
		EmailVerified: verified,
		Name:          profile.Name,
	}, nil
}

// ---- Google (OIDC; PKCE supported) ----

type googleProvider struct {
	clientID     string
	clientSecret string
	oidc         *oidc.Provider // lazily discovered
}

// NewGoogleProvider discovers Google's OIDC endpoints (a network call) and builds the
// provider. Returns an error if discovery fails so the caller can skip Google.
func NewGoogleProvider(ctx context.Context, clientID, clientSecret string) (Provider, error) {
	prov, err := oidc.NewProvider(ctx, "https://accounts.google.com")
	if err != nil {
		return nil, fmt.Errorf("google: oidc discovery: %w", err)
	}
	return &googleProvider{clientID: clientID, clientSecret: clientSecret, oidc: prov}, nil
}

func (p *googleProvider) Name() string { return ProviderGoogle }

func (p *googleProvider) oauthConfig(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.clientID,
		ClientSecret: p.clientSecret,
		Endpoint:     googleendpoint.Endpoint,
		RedirectURL:  redirectURI,
		Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

func (p *googleProvider) AuthCodeURL(state, challenge string, redirectURI string) string {
	return p.oauthConfig(redirectURI).AuthCodeURL(state,
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

func (p *googleProvider) Exchange(ctx context.Context, code, verifier, redirectURI string) (*IdentityClaims, error) {
	cfg := p.oauthConfig(redirectURI)
	tok, err := cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return nil, fmt.Errorf("google: code exchange: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, fmt.Errorf("google: no id_token in token response")
	}
	verifierCfg := p.oidc.Verifier(&oidc.Config{ClientID: p.clientID})
	idTok, err := verifierCfg.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("google: verify id_token: %w", err)
	}
	var claims struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idTok.Claims(&claims); err != nil {
		return nil, fmt.Errorf("google: parse claims: %w", err)
	}
	return &IdentityClaims{
		Provider:      ProviderGoogle,
		Subject:       claims.Sub,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
	}, nil
}

// getJSON does an authenticated GET and decodes the JSON body.
func getJSON(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
