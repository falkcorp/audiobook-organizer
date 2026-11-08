// file: core/coretest/client.go
// version: 1.0.0
// guid: ddea14bb-6f93-4fb8-9a3f-12504f5d67f7
// last-edited: 2026-11-07

package coretest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Client sends requests straight to a harness's App.Handler(), in process: no
// listener and no network.
type Client struct {
	handler http.Handler
}

// Do runs req through the App's handler and returns the recorded response.
// The body is in memory, so closing it is optional. A request built with
// http.NewRequest lacks what a server fills in; Do serves a copy with the
// fields httptest.NewRequest sets when they are empty: RemoteAddr
// (192.0.2.1:1234, the RFC 5737 documentation address), Host (example.com)
// and RequestURI. req itself is not modified.
func (c *Client) Do(req *http.Request) *http.Response {
	req = req.Clone(req.Context())
	if req.RemoteAddr == "" {
		req.RemoteAddr = "192.0.2.1:1234"
	}
	if req.Host == "" {
		req.Host = "example.com"
	}
	if req.RequestURI == "" {
		req.RequestURI = req.URL.RequestURI()
	}
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	return rec.Result()
}

// GetJSON sends GET path and returns the status. When out is not nil and the
// response has a body, the body is decoded into out whatever the status, so a
// test can read a problem document as well as a success; a body that does
// not decode fails the test.
func (c *Client) GetJSON(t testing.TB, path string, out any) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("coretest: GetJSON %s: %v", path, err)
		return 0
	}
	resp := c.Do(req)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("coretest: GetJSON %s: read body: %v", path, err)
		return 0
	}
	if out != nil && len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("coretest: GetJSON %s: status %d: decode %q: %v", path, resp.StatusCode, body, err)
		}
	}
	return resp.StatusCode
}
