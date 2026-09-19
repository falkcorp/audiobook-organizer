// file: internal/fingerprint/workerclient/api.go
// version: 1.0.0
// guid: 8b107cfb-5f0a-4ac4-b6c3-24914ad0d741
// last-edited: 2026-09-19

package workerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/fingerprint/workerapi"
)

// apiPrefix is where the worker routes are mounted.
const apiPrefix = "/api/v1/fingerprint/worker"

// apiClient speaks the worker API. The key only ever goes into the
// Authorization header; no error or log line built here contains it.
type apiClient struct {
	base *url.URL
	key  string
	hc   *http.Client
}

// apiError is a non-2xx answer: the status and the server's message.
type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// call sends in (JSON, or no body when nil) and decodes a 200 answer into
// out. It returns the status code; a transport failure is status 0. A 204 is
// (204, nil) with out untouched; any other non-200 is an *apiError.
func (c *apiClient) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encode %s: %w", path, err)
		}
		body = bytes.NewReader(b)
	}
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + apiPrefix + path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, fmt.Errorf("build %s request: %w", path, err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if out == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			return resp.StatusCode, nil
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode %s answer: %w", path, err)
		}
		return resp.StatusCode, nil
	case http.StatusNoContent:
		return resp.StatusCode, nil
	}
	return resp.StatusCode, &apiError{Status: resp.StatusCode, Message: errorMessage(resp.Body)}
}

// errorMessage extracts the server's error text ({"error": "..."} or a
// plain body), bounded.
func errorMessage(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4096))
	var e struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(b, &e) == nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
}

func (c *apiClient) hello(ctx context.Context) (*workerapi.HelloResponse, int, error) {
	var out workerapi.HelloResponse
	st, err := c.call(ctx, http.MethodGet, "/hello", nil, &out)
	if err != nil || st != http.StatusOK {
		return nil, st, err
	}
	return &out, st, nil
}

// lease returns (nil, 204, nil) when nothing is leaseable.
func (c *apiClient) lease(ctx context.Context, req workerapi.LeaseRequest) (*workerapi.LeaseResponse, int, error) {
	var out workerapi.LeaseResponse
	st, err := c.call(ctx, http.MethodPost, "/lease", req, &out)
	if err != nil || st != http.StatusOK {
		return nil, st, err
	}
	return &out, st, nil
}

func (c *apiClient) renew(ctx context.Context, leaseID string, req workerapi.RenewRequest) (int, error) {
	var out workerapi.RenewResponse
	return c.call(ctx, http.MethodPost, "/lease/"+url.PathEscape(leaseID)+"/renew", req, &out)
}

func (c *apiClient) release(ctx context.Context, leaseID string, req workerapi.ReleaseRequest) (*workerapi.ReleaseResponse, int, error) {
	var out workerapi.ReleaseResponse
	st, err := c.call(ctx, http.MethodPost, "/lease/"+url.PathEscape(leaseID)+"/release", req, &out)
	if err != nil {
		return nil, st, err
	}
	return &out, st, nil
}

func (c *apiClient) results(ctx context.Context, req workerapi.ResultsRequest) (*workerapi.ResultsResponse, int, error) {
	var out workerapi.ResultsResponse
	st, err := c.call(ctx, http.MethodPost, "/results", req, &out)
	if err != nil {
		return nil, st, err
	}
	return &out, st, nil
}
