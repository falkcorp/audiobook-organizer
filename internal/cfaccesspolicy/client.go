// file: internal/cfaccesspolicy/client.go
// version: 1.4.1
// guid: c863e806-fb62-4ceb-9226-d971b7d4d048
// last-edited: 2026-10-23

// Package cfaccesspolicy adds one include rule to, and removes one from, ONE
// Cloudflare Access reusable policy's include list. Two rule kinds exist and no
// other: an email address ({"email":{"email":...}}, the optional "accepted
// invitees get browser access" sync, owner decision 2026-10-19) and ONE named
// service token ({"service_token":{"token_id":...}}, the per-guest phone-app
// credential, owner decision 2026-10-22). It never writes the
// any_valid_service_token rule, and never removes a rule it did not match
// exactly. It refuses the owner's own service-token ids (protectedTokenPrefixes).
//
// Scope is fixed at construction: the account id and policy id are fields of
// the Client, never parameters, so no caller can point it at another policy.
// The only requests it makes are GET and PUT on
// /accounts/{account}/access/policies/{policy}.
//
// The PUT replaces the whole policy, so the GET result is read as a generic
// JSON object (numbers kept as json.Number) and every documented writable
// field (see writableFields) goes back exactly as read, with only the include
// list changed. Server-managed fields (id, created_at, updated_at, app_count,
// reusable, ...) are never sent. After the PUT the policy is re-read and the
// email's presence verified; a mismatch is re-planned from a fresh read and
// retried a bounded number of times (the endpoint documents no ETag/If-Match),
// and ends in ErrWriteUnverified if a PUT landed but was never confirmed. The
// Ensure* calls only append; the Remove* calls drop only rules that are exactly
// {"email":{"email":<that address>}} or {"service_token":{"token_id":<that
// id>}} and leave every other rule as it was.
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

	"golang.org/x/text/unicode/norm"
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
	// Removed: the email's include rule was dropped and the policy saved.
	Removed Result = "removed"
	// NotPresent: the email was not an include rule; nothing written.
	NotPresent Result = "not_present"
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
	// retryDelay scales the pause between verify retries (attempt * retryDelay).
	retryDelay time.Duration
}

// New validates cfg and returns a Client.
func New(cfg Config) (*Client, error) {
	if !accountIDRe.MatchString(cfg.AccountID) {
		return nil, errors.New("cfaccesspolicy: CF_ACCOUNT_ID must be 32 hex characters")
	}
	if !policyIDRe.MatchString(cfg.PolicyID) {
		return nil, errors.New("cfaccesspolicy: the policy id (CF_GUESTS_POLICY_ID or CF_SERVICE_AUTH_POLICY_ID) is not a policy id")
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
		retryDelay: 250 * time.Millisecond,
	}, nil
}

// normalizeAddress is util.NormalizeEmail (trimmed, NFKC, lowercased, NFKC
// again), repeated here because this package imports no module package
// (layer 0 in internal/arch). TestCFAccessPolicyEmailMatchesUtil in
// internal/server keeps the two identical.
func normalizeAddress(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.TrimSpace(norm.NFKC.String(strings.ToLower(norm.NFKC.String(s))))
}

// NormalizeEmail normalizes an address (normalizeAddress) and checks it is one
// bare address. It is the form compared against and written to the policy.
func NormalizeEmail(email string) (string, error) {
	if strings.ContainsAny(email, "\r\n") {
		return "", fmt.Errorf("cfaccesspolicy: invalid email")
	}
	e := normalizeAddress(email)
	if e == "" || strings.ContainsAny(e, "\r\n\t <>\",") {
		return "", fmt.Errorf("cfaccesspolicy: invalid email")
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e {
		return "", fmt.Errorf("cfaccesspolicy: invalid email")
	}
	return e, nil
}

// writableFields is the explicit allowlist of reusable Access policy fields
// accepted by the update (PUT) call, copied verbatim from the GET. Anything
// else in the GET result is server-managed or unknown and is dropped: id,
// account_id, created_at, updated_at, app_count, reusable, version and any
// field Cloudflare adds later. Choice: an unknown field is dropped rather than
// echoed, because echoing a read-only field can make the PUT fail or be
// silently ignored; a newly documented writable field must be added here.
// Source: https://developers.cloudflare.com/api/resources/zero_trust/subresources/access/subresources/policies/methods/update/
var writableFields = []string{
	"name", "decision", "include", "exclude", "require",
	"session_duration", "approval_groups", "approval_required",
	"isolation_required", "purpose_justification_prompt",
	"purpose_justification_required", "connection_rules", "mfa_config",
}

// maxAttempts bounds the read-modify-write-verify cycle. The endpoint documents
// no ETag/If-Match, so a concurrent writer (another process or the dashboard)
// is detected by re-reading after the PUT, and the cycle is retried.
const maxAttempts = 3

// writableBody returns the PUT body for policy: only writableFields present in
// the GET result, values untouched.
func writableBody(policy map[string]any) map[string]any {
	out := make(map[string]any, len(writableFields))
	for _, k := range writableFields {
		if v, ok := policy[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ruleSpec is the one include rule a plan appends or removes: what to append,
// how to recognise exactly that rule, and what to call it in messages.
type ruleSpec struct {
	rule  map[string]any
	match func(rule any) bool
	label string
}

func emailSpec(e string) ruleSpec {
	return ruleSpec{
		rule:  map[string]any{"email": map[string]any{"email": e}},
		match: func(r any) bool { return isEmailRule(r, e) },
		label: "email",
	}
}

func serviceTokenSpec(id string) ruleSpec {
	return ruleSpec{
		rule:  map[string]any{"service_token": map[string]any{"token_id": id}},
		match: func(r any) bool { return isServiceTokenRule(r, id) },
		label: "service token",
	}
}

// plan reads the policy and computes the change for spec. When the policy needs
// no change it returns a nil body and AlreadyPresent / NotPresent.
func (c *Client) plan(ctx context.Context, spec ruleSpec, add bool) (body map[string]any, res Result, err error) {
	policy, err := c.do(ctx, http.MethodGet, nil)
	if err != nil {
		return nil, "", fmt.Errorf("cfaccesspolicy: read policy: %w", err)
	}
	include, err := includeList(policy)
	if err != nil {
		return nil, "", err
	}
	var want []any
	if add {
		if hasRule(include, spec.match) {
			return nil, AlreadyPresent, nil
		}
		want = append(append(make([]any, 0, len(include)+1), include...), spec.rule)
		res = Added
	} else {
		want = make([]any, 0, len(include))
		for _, rule := range include {
			if !spec.match(rule) {
				want = append(want, rule)
			}
		}
		if len(want) == len(include) {
			return nil, NotPresent, nil
		}
		if len(want) == 0 {
			// Cloudflare refuses a policy with an empty include list, and an
			// allow policy with no include would admit nobody anyway. Leave the
			// last rule for the owner to remove by hand.
			return nil, "", errors.New("cfaccesspolicy: refusing to remove the policy's only include rule; remove it by hand")
		}
		res = Removed
	}
	body = writableBody(policy)
	body["include"] = want
	return body, res, nil
}

// PlanAdd performs the GET only and returns the JSON PUT body EnsureEmail
// would send for email, without sending it. The body is nil (and the Result
// AlreadyPresent) when nothing would be written. Used to inspect the request
// against a scratch policy before enabling the sync.
func (c *Client) PlanAdd(ctx context.Context, email string) ([]byte, Result, error) {
	return c.planJSON(ctx, email, true)
}

// PlanRemove is PlanAdd for RemoveEmail (Result NotPresent when a no-op).
func (c *Client) PlanRemove(ctx context.Context, email string) ([]byte, Result, error) {
	return c.planJSON(ctx, email, false)
}

func (c *Client) planJSON(ctx context.Context, email string, add bool) ([]byte, Result, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return nil, "", err
	}
	spec := emailSpec(e)
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	body, res, err := c.plan(ctx, spec, add)
	if err != nil || body == nil {
		return nil, res, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	return b, res, nil
}

// EnsureEmail makes email an include rule of the configured policy. It is
// idempotent: an email already present (compared case-insensitively) writes
// nothing and returns AlreadyPresent.
func (c *Client) EnsureEmail(ctx context.Context, email string) (Result, error) {
	return c.apply(ctx, email, true)
}

// RemoveEmail drops email's include rule from the configured policy. It is
// idempotent: an email that is not there writes nothing and returns
// NotPresent. Only plain email rules for exactly that address are removed.
func (c *Client) RemoveEmail(ctx context.Context, email string) (Result, error) {
	return c.apply(ctx, email, false)
}

// ErrWriteUnverified means at least one PUT of this call succeeded (2xx) but
// the include list was not confirmed afterwards (the verifying read failed or
// kept disagreeing, or a later step failed): the write may have happened. The
// Result returned with it is the one the write would have produced (Added or
// Removed), so a caller that must be able to undo an add records it as added.
var ErrWriteUnverified = errors.New("cfaccesspolicy: write may have happened but could not be verified")

// apply runs GET, PUT, then a verifying GET, up to maxAttempts times.
//
// Verification checks only this email's presence (add) or absence (remove): our
// PUT changes nothing else, so another writer editing unrelated include rules
// must neither fail verification nor cause a retry that would put such a rule
// back. Every retry re-plans from a fresh GET, never from a stale snapshot.
//
// Once one of our PUTs has landed, a later attempt that finds the email already
// in the wanted state is OUR write showing through, so the result stays
// Added/Removed (never AlreadyPresent/NotPresent: the caller records "we added
// it" and a later revoke depends on that). If the write cannot be confirmed
// the error wraps ErrWriteUnverified and is returned with that same result.
func (c *Client) apply(ctx context.Context, email string, add bool) (Result, error) {
	e, err := NormalizeEmail(email)
	if err != nil {
		return "", err
	}
	return c.applyRule(ctx, emailSpec(e), add)
}

// EnsureServiceToken makes the service token tokenID an include rule of the
// configured policy ({"service_token":{"token_id":tokenID}}): that ONE token,
// never any_valid_service_token. Idempotent. tokenID must be a service-token id
// and must not be one of the owner's own tokens.
func (c *Client) EnsureServiceToken(ctx context.Context, tokenID string) (Result, error) {
	spec, err := checkedServiceTokenSpec(tokenID)
	if err != nil {
		return "", err
	}
	return c.applyRule(ctx, spec, true)
}

// RemoveServiceToken drops the include rule for tokenID, and only a rule that
// is exactly {"service_token":{"token_id":tokenID}}. Idempotent. Refuses the
// owner's own token ids before any request is sent.
func (c *Client) RemoveServiceToken(ctx context.Context, tokenID string) (Result, error) {
	spec, err := checkedServiceTokenSpec(tokenID)
	if err != nil {
		return "", err
	}
	return c.applyRule(ctx, spec, false)
}

// ErrProtectedToken: the id is one of the owner's own service tokens.
var ErrProtectedToken = errors.New("cfaccesspolicy: refusing to touch a protected service token")

// protectedTokenPrefixes: see cfservicetoken.protectedIDPrefixes, which this
// copies (layer 0 imports nothing); TestProtectedPrefixesMatch keeps them equal.
var protectedTokenPrefixes = []string{"90a520fb", "56ffe232", "790ef191"}

var tokenIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ProtectedTokenPrefixes returns a copy of the protected id prefixes (tests).
func ProtectedTokenPrefixes() []string { return append([]string(nil), protectedTokenPrefixes...) }

func checkedServiceTokenSpec(tokenID string) (ruleSpec, error) {
	id := strings.ToLower(tokenID)
	if !tokenIDRe.MatchString(id) {
		return ruleSpec{}, errors.New("cfaccesspolicy: not a service token id")
	}
	for _, p := range protectedTokenPrefixes {
		if strings.HasPrefix(id, p) {
			return ruleSpec{}, ErrProtectedToken
		}
	}
	return serviceTokenSpec(id), nil
}

func (c *Client) applyRule(ctx context.Context, spec ruleSpec, add bool) (Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	wrote := false
	var wroteRes Result
	// fail wraps err as ErrWriteUnverified once a PUT has landed.
	fail := func(err error) (Result, error) {
		if wrote {
			return wroteRes, fmt.Errorf("%w: %w", ErrWriteUnverified, err)
		}
		return "", err
	}
	var lastProblem string
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		body, res, err := c.plan(ctx, spec, add)
		if err != nil {
			return fail(err)
		}
		if body == nil {
			if wrote {
				return wroteRes, nil
			}
			return res, nil
		}
		if _, err := c.do(ctx, http.MethodPut, body); err != nil {
			return fail(fmt.Errorf("cfaccesspolicy: save policy: %w", err))
		}
		wrote, wroteRes = true, res
		after, err := c.do(ctx, http.MethodGet, nil)
		if err != nil {
			return fail(fmt.Errorf("cfaccesspolicy: verify policy: %w", err))
		}
		inc, err := includeList(after)
		if err != nil {
			return fail(err)
		}
		if add == hasRule(inc, spec.match) {
			return wroteRes, nil
		}
		if add {
			lastProblem = "the " + spec.label + " is not in the include list"
		} else {
			lastProblem = "the " + spec.label + " is still in the include list"
		}
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return fail(fmt.Errorf("cfaccesspolicy: verify policy: %w", ctx.Err()))
			case <-time.After(time.Duration(attempt) * c.retryDelay):
			}
		}
	}
	return fail(fmt.Errorf("cfaccesspolicy: policy not as intended after %d attempts: %s", maxAttempts, lastProblem))
}

// isEmailRule reports whether rule is exactly {"email":{"email":email}}.
func isEmailRule(rule any, email string) bool {
	r, ok := rule.(map[string]any)
	if !ok || len(r) != 1 {
		return false
	}
	inner, ok := r["email"].(map[string]any)
	if !ok || len(inner) != 1 {
		return false
	}
	s, ok := inner["email"].(string)
	return ok && normalizeAddress(s) == email
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

func hasRule(include []any, match func(any) bool) bool {
	for _, rule := range include {
		if match(rule) {
			return true
		}
	}
	return false
}

// isServiceTokenRule reports whether rule is exactly
// {"service_token":{"token_id":id}}. any_valid_service_token never matches.
func isServiceTokenRule(rule any, id string) bool {
	r, ok := rule.(map[string]any)
	if !ok || len(r) != 1 {
		return false
	}
	inner, ok := r["service_token"].(map[string]any)
	if !ok || len(inner) != 1 {
		return false
	}
	s, ok := inner["token_id"].(string)
	return ok && strings.EqualFold(s, id)
}

// APIError is a non-success answer from Cloudflare. Callers branch on Status
// (IsForbidden), never on Detail, which is Cloudflare's text, capped and with
// the API token scrubbed, for logs and the admin.
type APIError struct {
	Status int
	Detail string
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail) }

// IsForbidden reports whether err is, or wraps, a Cloudflare 403: the API
// token lacks the permission the call needs.
func IsForbidden(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusForbidden
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
		return nil, &APIError{Status: resp.StatusCode, Detail: "response is not Cloudflare API JSON"}
	}
	if resp.StatusCode/100 != 2 || !env.Success {
		return nil, &APIError{Status: resp.StatusCode, Detail: describe(env.Errors, c.token)}
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
