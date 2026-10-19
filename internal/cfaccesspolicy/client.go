// file: internal/cfaccesspolicy/client.go
// version: 1.0.0
// guid: c863e806-fb62-4ceb-9226-d971b7d4d048
// last-edited: 2026-10-19

// Package cfaccesspolicy adds an email address to ONE Cloudflare Access
// reusable policy's include list. It backs the optional "accepted invitees
// get browser access" sync (owner decision 2026-10-19).
//
// Scope is fixed at construction: the account id and policy id are fields of
// the Client, never parameters, so no caller can point it at another policy.
// The only requests it makes are GET and PUT on
// /accounts/{account}/access/policies/{policy}.
//
// The PUT replaces the whole policy, so the GET result is round-tripped
// losslessly as a generic JSON object (numbers kept as json.Number): every
// field, including ones this package does not know about, goes back exactly
// as read, with only the include list extended. Existing rules are never
// removed or rewritten.
//
// The API token is sent only in the Authorization header and never appears in
// an error. Package imports nothing from this module and logs nothing.
package cfaccesspolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is Cloudflare's API v4 root.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// DefaultTimeout bounds one EnsureEmail call (GET plus PUT).
const DefaultTimeout = 15 * time.Second

// maxBody caps how much of a response is read.
const maxBody = 1 << 20

// Result is what EnsureEmail did.
type Result string

const (
	// Added: the email was appended and the policy saved.
	Added Result = "added"
	// AlreadyPresent: the email was already an include rule; nothing written.
	AlreadyPresent Result = "already_present"
)

var (
	accountIDRe = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	policyIDRe  = regexp.MustCompile(`^[0-9a-fA-F-]{8,64}$`)
)

// Config configures a Client.
type Config struct {
	AccountID string
	PolicyID  string
	APIToken  string
	// BaseURL overrides DefaultBaseURL (tests).
	BaseURL string
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
	// HTTPClient overrides the default client (tests).
	HTTPClient *http.Client
}

// Client edits one Access policy.
type Client struct {
	accountID, policyID, token string
	baseURL                    string
	timeout                    time.Duration
	http                       *http.Client
	// mu serializes read-modify-write cycles so two accepts in this process
	// cannot both read the old list and lose one email on the second PUT.
	mu sync.Mutex
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if !accountIDRe.MatchString(cfg.AccountID) {
		return nil, errors.New("cfaccesspolicy: CF_ACCOUNT_ID must be 32 hex characters")
	}
	if !policyIDRe.MatchString(cfg.PolicyID) {
		return nil, errors.New("cfaccesspolicy: CF_GUESTS_POLICY_ID is not a policy id")
	}
	if strings.TrimSpace(cfg.APIToken) == "" || strings.ContainsAny(cfg.APIToken, "\r\n ") {
		return nil, errors.New("cfaccesspolicy: CF_POLICY_API_TOKEN is empty or malformed")
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
	return &Client{
		accountID: strings.ToLower(cfg.AccountID), policyID: strings.ToLower(cfg.PolicyID),
		token: cfg.APIToken, baseURL: base, timeout: timeout, http: hc,
	}, nil
}

// NormalizeEmail lowercases and trims an address and checks it is one bare
// address. It is the form compared against and written to the policy.
func NormalizeEmail(email string) (string, error) {
	if strings.ContainsAny(email, "\r\n") {
		return "", fmt.Errorf("cfaccesspolicy: invalid email")
	}
	e := strings.ToLower(strings.TrimSpace(email))
	if e == "" || strings.ContainsAny(e, "\r\n\t <>\",") {
		return "", fmt.Errorf("cfaccesspolicy: invalid email")
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e {
		return "", fmt.Errorf("cfaccesspolicy: invalid email")
	}
	return e, nil
}

// EnsureEmail makes email an include rule of the configured policy. It is
// idempotent: an email already present (compared case-insensitively) writes
// nothing and returns AlreadyPresent.
func (c *Client) EnsureEmail(ctx context.Context, email string) (Result, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	policy, err := c.do(ctx, http.MethodGet, nil)
	if err != nil {
		return "", fmt.Errorf("cfaccesspolicy: read policy: %w", err)
	}
	include, err := includeList(policy)
	if err != nil {
		return "", err
	}
	if hasEmail(include, e) {
		return AlreadyPresent, nil
	}
	policy["include"] = append(include, map[string]any{"email": map[string]any{"email": e}})
	saved, err := c.do(ctx, http.MethodPut, policy)
	if err != nil {
		return "", fmt.Errorf("cfaccesspolicy: save policy: %w", err)
	}
	// Confirm from what Cloudflare stored, not from what was sent.
	if inc, err := includeList(saved); err == nil && !hasEmail(inc, e) {
		return "", errors.New("cfaccesspolicy: policy saved but the email is not in its include list")
	}
	return Added, nil
}

func includeList(policy map[string]any) ([]any, error) {
	raw, ok := policy["include"]
	if !ok || raw == nil {
		return []any{}, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, errors.New("cfaccesspolicy: policy include is not a list")
	}
	return list, nil
}

func hasEmail(include []any, email string) bool {
	for _, rule := range include {
		r, ok := rule.(map[string]any)
		if !ok {
			continue
		}
		inner, ok := r["email"].(map[string]any)
		if !ok {
			continue
		}
		if s, ok := inner["email"].(string); ok && strings.EqualFold(strings.TrimSpace(s), email) {
			return true
		}
	}
	return false
}

type apiEnvelope struct {
	Success bool            `json:"success"`
	Errors  []apiMessage    `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type apiMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) do(ctx context.Context, method string, body map[string]any) (map[string]any, error) {
	url := c.baseURL + "/accounts/" + c.accountID + "/access/policies/" + c.policyID
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, scrub(err, c.token)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, scrub(err, c.token)
	}
	var env apiEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("HTTP %d: response is not Cloudflare API JSON", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 || !env.Success {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, describe(env.Errors, c.token))
	}
	dec := json.NewDecoder(bytes.NewReader(env.Result))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil || out == nil {
		return nil, errors.New("response has no policy object")
	}
	return out, nil
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
	return scrubString(strings.Join(parts, "; "), token)
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
