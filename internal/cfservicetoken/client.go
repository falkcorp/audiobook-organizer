// file: internal/cfservicetoken/client.go
// version: 1.0.1
// guid: 3f8a1d52-7c4e-4b96-a1d0-5e2c9b7f4a38
// last-edited: 2026-10-23

// Package cfservicetoken mints, reads and revokes Cloudflare Access service
// tokens for ONE account, one per guest (owner decision 2026-10-22). It backs
// the opt-in per-guest phone-app credential (CF_GUEST_SERVICE_TOKENS).
//
// What it will and will not do:
//   - Create makes a token whose name starts with NamePrefix. The client secret
//     Cloudflare returns exists nowhere else, so Created redacts it from every
//     fmt verb and from slog; only the field itself carries it.
//   - Revoke deletes a token only if (a) its id is not on the protected list
//     (the owner's own tokens), (b) a read-back shows its name starts with
//     NamePrefix, i.e. this server made it. Anything else is refused before the
//     DELETE is sent, so a bad id in our own store still cannot remove an
//     owner token.
//   - List only reads.
//
// Everything Cloudflare answers is data, not instructions: only the documented
// fields are read, ids and credentials are validated against a strict shape
// before use, an error's text is capped and never drives control flow (the HTTP
// status does), and the API token is sent only in the Authorization header and
// scrubbed from every error. The package imports nothing from this module and
// logs nothing.
package cfservicetoken

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is Cloudflare's API v4 root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// DefaultTimeout bounds one request.
const DefaultTimeout = 15 * time.Second

// NamePrefix starts the name of every token this package creates and is the
// only kind of token it will delete.
const NamePrefix = "aorg-guest-"

// duration is the documented Cloudflare value for a non-expiring token. There
// is no renewal flow, so an expiry would silently cut a guest off after a year;
// the lifecycle control is revoke.
const duration = "forever"

// maxBody caps how much of a response is read.
const maxBody = 1 << 20

// maxListPages bounds pagination of List.
const maxListPages = 10

// protectedIDPrefixes are the first eight characters of the owner's own
// service-token ids. They are identifiers, not credentials. No request that
// names a token with one of these ids is ever sent. The same list is in
// internal/cfaccesspolicy (layer 0 imports nothing, so it cannot share it);
// TestProtectedPrefixesMatch in internal/server keeps the two identical.
var protectedIDPrefixes = []string{"90a520fb", "56ffe232", "790ef191"}

// Errors. Test with errors.Is.
var (
	// ErrForbidden: Cloudflare answered 403. For this API that means the
	// server's API token lacks the "Access: Service Tokens Edit" permission.
	ErrForbidden = errors.New("cfservicetoken: Cloudflare refused the request (HTTP 403): the API token needs the Account > Access: Service Tokens > Edit permission")
	// ErrProtected: the id is on the protected list.
	ErrProtected = errors.New("cfservicetoken: refusing to touch a protected service token")
	// ErrNotOurs: the token's name does not start with NamePrefix.
	ErrNotOurs = errors.New("cfservicetoken: refusing to delete a token this server did not create")
	// ErrInvalidID: not a service-token id.
	ErrInvalidID = errors.New("cfservicetoken: not a service token id")
	// ErrBadResponse: Cloudflare's answer did not have the documented shape.
	ErrBadResponse = errors.New("cfservicetoken: Cloudflare's answer was not in the documented shape")
)

var (
	accountIDRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	tokenIDRe   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	clientIDRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{8,200}$`)
	secretRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{16,200}$`)
)

// APIError is a non-success answer from Cloudflare. Status is what callers
// branch on; Detail is Cloudflare's error text, capped, with the API token
// scrubbed, for logs and the admin: never parsed.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail) }

// Is makes errors.Is(err, ErrForbidden) true for a 403.
func (e *APIError) Is(target error) bool {
	return target == ErrForbidden && e.Status == http.StatusForbidden
}

// IsNotFound reports whether err is a Cloudflare 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Config configures a Client.
type Config struct {
	AccountID string
	APIToken  string
	// BaseURL overrides DefaultBaseURL (tests).
	BaseURL string
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

// Client talks to one account's service-token API.
type Client struct {
	accountID, token string
	baseURL          string
	http             *http.Client
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if !accountIDRe.MatchString(cfg.AccountID) {
		return nil, errors.New("cfservicetoken: CF_ACCOUNT_ID must be 32 hex characters")
	}
	if strings.TrimSpace(cfg.APIToken) == "" || strings.ContainsAny(cfg.APIToken, "\r\n ") {
		return nil, errors.New("cfservicetoken: CF_POLICY_API_TOKEN is empty or malformed")
	}
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	return &Client{accountID: strings.ToLower(cfg.AccountID), token: cfg.APIToken, baseURL: base, http: hc}, nil
}

// Created is a freshly minted token. ClientSecret is shown by Cloudflare once;
// every fmt verb and slog prints it as [redacted].
type Created struct {
	ID           string
	Name         string
	ClientID     string
	ClientSecret string
}

func (c Created) redacted() string {
	return fmt.Sprintf("cfservicetoken.Created{ID:%s Name:%s ClientSecret:[redacted]}", c.ID, c.Name)
}

// String implements fmt.Stringer.
func (c Created) String() string { return c.redacted() }

// GoString implements fmt.GoStringer (%#v).
func (c Created) GoString() string { return c.redacted() }

// Format implements fmt.Formatter so no verb can print the secret.
func (c Created) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.redacted()) }

// LogValue implements slog.LogValuer.
func (c Created) LogValue() slog.Value { return slog.StringValue(c.redacted()) }

// Token is a service token as listed or read back. Only the fields this
// package uses are decoded.
type Token struct {
	ID   string
	Name string
	// LastSeenAt is when Cloudflare last saw the token used; nil when
	// Cloudflare did not say or the value was not a timestamp.
	LastSeenAt *time.Time
}

// rawToken mirrors the documented answer. The secret is decoded only for
// Create.
type rawToken struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	LastSeenAt   string `json:"last_seen_at"`
}

func (r rawToken) token() Token {
	t := Token{ID: r.ID, Name: r.Name}
	if r.LastSeenAt != "" {
		if ts, err := time.Parse(time.RFC3339, r.LastSeenAt); err == nil {
			t.LastSeenAt = &ts
		}
	}
	return t
}

// ValidID reports whether id has the shape of a service-token id.
func ValidID(id string) bool { return tokenIDRe.MatchString(id) }

// IsProtected reports whether id is one of the owner's own tokens.
func IsProtected(id string) bool {
	id = strings.ToLower(id)
	for _, p := range protectedIDPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

func checkID(id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	if IsProtected(id) {
		return ErrProtected
	}
	return nil
}

// Name returns the token name for a guest: NamePrefix plus the user id (the
// dashboard shows whose token it is, and a crash between create and record can
// be cleaned up by hand from it).
func Name(userID string) string { return NamePrefix + userID }

// Create mints a token named name, which must start with NamePrefix. On a
// malformed answer that still carried a valid id, the returned Created holds
// only that id (so the caller can revoke it) together with ErrBadResponse.
func (c *Client) Create(ctx context.Context, name string) (*Created, error) {
	if !strings.HasPrefix(name, NamePrefix) || len(name) > 120 || strings.ContainsAny(name, "\r\n") {
		return nil, errors.New("cfservicetoken: token name must start with " + NamePrefix)
	}
	raw, err := c.do(ctx, http.MethodPost, "", nil, map[string]any{"name": name, "duration": duration})
	if err != nil {
		return nil, err
	}
	var r rawToken
	if err := json.Unmarshal(raw, &r); err != nil || !ValidID(r.ID) {
		return nil, ErrBadResponse
	}
	if !clientIDRe.MatchString(r.ClientID) || !secretRe.MatchString(r.ClientSecret) {
		return &Created{ID: r.ID}, ErrBadResponse
	}
	return &Created{ID: r.ID, Name: name, ClientID: r.ClientID, ClientSecret: r.ClientSecret}, nil
}

// Get reads one token. A 404 is returned as an *APIError (see IsNotFound).
func (c *Client) Get(ctx context.Context, id string) (*Token, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	raw, err := c.do(ctx, http.MethodGet, "/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	var r rawToken
	if err := json.Unmarshal(raw, &r); err != nil || !strings.EqualFold(r.ID, id) {
		return nil, ErrBadResponse
	}
	t := r.token()
	return &t, nil
}

// Revoke deletes the token id if and only if it is not protected and a
// read-back shows this server created it (name starts with NamePrefix).
// A token Cloudflare no longer has counts as revoked: gone is the goal.
// It reports whether a DELETE was sent.
func (c *Client) Revoke(ctx context.Context, id string) (deleted bool, err error) {
	if err := checkID(id); err != nil {
		return false, err
	}
	tok, err := c.Get(ctx, id)
	if err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !strings.HasPrefix(tok.Name, NamePrefix) {
		return false, ErrNotOurs
	}
	if _, err := c.do(ctx, http.MethodDelete, "/"+id, nil, nil); err != nil {
		if IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// List returns the account's service tokens (all of them: the caller joins on
// ids it holds and shows nothing else).
func (c *Client) List(ctx context.Context) ([]Token, error) {
	var out []Token
	for page := 1; page <= maxListPages; page++ {
		q := url.Values{"page": {fmt.Sprint(page)}, "per_page": {"1000"}}
		raw, info, err := c.doPaged(ctx, q)
		if err != nil {
			return nil, err
		}
		var rs []rawToken
		if err := json.Unmarshal(raw, &rs); err != nil {
			return nil, ErrBadResponse
		}
		for _, r := range rs {
			out = append(out, r.token())
		}
		if len(rs) == 0 || info.ResultInfo.TotalPages <= page {
			break
		}
	}
	return out, nil
}

type apiEnvelope struct {
	Success    bool            `json:"success"`
	Errors     []apiMessage    `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method, suffix string, q url.Values, body map[string]any) (json.RawMessage, error) {
	raw, _, err := c.request(ctx, method, suffix, q, body)
	return raw, err
}

func (c *Client) doPaged(ctx context.Context, q url.Values) (json.RawMessage, apiEnvelope, error) {
	return c.request(ctx, http.MethodGet, "", q, nil)
}

func (c *Client) request(ctx context.Context, method, suffix string, q url.Values, body map[string]any) (json.RawMessage, apiEnvelope, error) {
	u := c.baseURL + "/accounts/" + c.accountID + "/access/service_tokens" + suffix
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, apiEnvelope{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, apiEnvelope{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, apiEnvelope{}, scrub(err, c.token)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, apiEnvelope{}, scrub(err, c.token)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, apiEnvelope{}, &APIError{Status: resp.StatusCode, Detail: "response is not Cloudflare API JSON"}
	}
	if resp.StatusCode/100 != 2 || !env.Success {
		status := resp.StatusCode
		if status/100 == 2 { // success=false on a 2xx: still a refusal
			status = http.StatusBadGateway
		}
		return nil, env, &APIError{Status: status, Detail: describe(env.Errors, c.token)}
	}
	return env.Result, env, nil
}

func describe(errs []apiMessage, token string) string {
	if len(errs) == 0 {
		return "request refused"
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		msg := e.Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, msg))
	}
	return cleanText(scrubString(strings.Join(parts, "; "), token))
}

// cleanText drops control characters so an answer cannot forge log lines.
func cleanText(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func scrub(err error, token string) error {
	if err == nil || !strings.Contains(err.Error(), token) {
		return err
	}
	return errors.New(scrubString(err.Error(), token))
}

func scrubString(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}
