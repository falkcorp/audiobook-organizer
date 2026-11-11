// file: core/integrations/providerhttp/client.go
// version: 1.0.0
// guid: 435ee04d-8b14-43d6-9462-be6a491eb852
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/integrations"
)

// Client implements integrations.HTTP: an *http.Client whose transport runs
// the gate, plus helpers. It also is the instance's Gate (Acquire, State), so
// an adapter that mixes HTTP and non-HTTP work is held once.
type Client struct {
	*gate
	rt *transport
}

var _ integrations.HTTP = (*Client)(nil)

// Client returns a FRESH *http.Client on every call, never nil, with no
// Timeout (the per-attempt timeout is the transport's) and a CheckRedirect
// that caps the chain and re-applies the scheme allowlist. Its Transport is the
// gate: a copy that replaces CheckRedirect (as the Deluge adapter does) or
// follows redirects keeps every control, because they are not in the client.
func (c *Client) Client() *http.Client {
	return &http.Client{Transport: c.rt, CheckRedirect: CheckRedirect}
}

// Do sends req through the gate and returns its response; errors carry no
// credentials from the URL.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.Client().Do(req)
	return resp, redactError(err, c.pol.Redact)
}

// Gate is the instance's gate.
func (c *Client) Gate() integrations.Gate { return c.gate }

// SetLimits changes the scope's limits in place (rate.Limiter-style): no
// rebuild and no second bucket (fixes HEAD defect 1). Every Client of the
// Scope sees the change.
func (c *Client) SetLimits(l integrations.Limits) {
	c.sc.setLimits(normalizeLimits(l, c.r.settings()))
}

// maxJSON caps a GetJSON body; a provider answer beyond it is unusable.
const maxJSON = 16 << 20

// GetJSON GETs url and decodes its JSON answer into out. A non-2xx answer is
// classified into an *integrations.Error (Status, RetryAfter, a bounded and
// scrubbed Detail); an answer that is not valid JSON, or is too large, is
// ClassBadOutput.
func (c *Client) GetJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &integrations.Error{Ref: c.ref, Op: "get", Class: integrations.ClassInvalid,
			Err: errors.New("the request URL is not valid")}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil && !errors.Is(err, ErrResponseTooLarge) {
		return redactError(err, c.pol.Redact)
	}
	if err != nil || len(body) > maxJSON {
		return &integrations.Error{Ref: c.ref, Op: "get", Class: integrations.ClassBadOutput, Status: resp.StatusCode, Err: ErrResponseTooLarge}
	}
	if resp.StatusCode >= 400 {
		return &integrations.Error{Ref: c.ref, Op: "get", Class: classifyStatus(resp.StatusCode, body[:min(len(body), classifyBodyMax)]),
			Status: resp.StatusCode, RetryAfter: retryAfterOf(resp.Header, c.r.now()), Detail: c.excerpt(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &integrations.Error{Ref: c.ref, Op: "get", Class: integrations.ClassBadOutput, Status: resp.StatusCode,
			Err: errors.New("the answer is not valid JSON for the expected shape")}
	}
	return nil
}

// excerpt is body's first MaxDetail bytes, scrubbed of credentials.
func (c *Client) excerpt(body []byte) string {
	s := strings.ToValidUTF8(string(body), "")
	s = scrubText(s, c.pol.Redact)
	if len(s) > integrations.MaxDetail {
		s = s[:integrations.MaxDetail]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
