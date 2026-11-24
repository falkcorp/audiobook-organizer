// file: core/examples/hello/cmd/hello/identity_test.go
// version: 0.1.0
// guid: 5d1c7e2a-93b4-4f60-8a1e-2c7b9d0e4f13
// last-edited: 2026-11-21

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/coretest/logcapture"
	"github.com/falkcorp/mediacore/examples/hello"
)

// TestOptions_DeclaresIdentityBoot: hello serves core's identity (F4-08 B9c).
// Dropping Options.IdentityBoot boots hello as before the flip, with no core
// identity routes, so this fails.
func TestOptions_DeclaresIdentityBoot(t *testing.T) {
	if !options(nil).IdentityBoot {
		t.Fatal("hello's options do not declare IdentityBoot")
	}
}

// TestOpenAPI_CarriesCoreIdentityRoutes: the document `hello openapi` writes
// holds core's identity routes, including the review-admin route, because
// hello declares the identity boot. An empty "paths" (the document before
// B9c) fails.
func TestOpenAPI_CarriesCoreIdentityRoutes(t *testing.T) {
	var stdout bytes.Buffer
	out := t.TempDir() + "/core.json"
	if code, err := run(t.Context(), []string{"openapi", "--out", out}, &stdout); code != 0 || err != nil {
		t.Fatalf("hello openapi = %d, %v", code, err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for path, method := range map[string]string{
		"/api/core/v1/auth/login":              "post",
		"/api/core/v1/auth/setup":              "post",
		"/api/core/v1/users":                   "get",
		"/api/core/v1/users/{id}/review-admin": "post",
		"/api/core/v1/me":                      "get",
	} {
		if _, ok := doc.Paths[path][method]; !ok {
			t.Errorf("core.json has no %s %s", strings.ToUpper(method), path)
		}
	}
}

// TestIdentityBoot_PlainHTTPSession boots hello through cmd's own options and
// walks the first-run flow over a plain-HTTP listener: status, first
// administrator, sign-in, /me. A plain peer is served and its session cookie
// is not __Host- prefixed (B4fu).
func TestIdentityBoot_PlainHTTPSession(t *testing.T) {
	capture := logcapture.New(t)
	o := options(nil)
	o.Logger = capture.Logger()
	stateDir := t.TempDir()
	o.StateDir = stateDir
	o.Env = []string{}
	o.ConfigFlags = map[config.Path]string{"core.http.listen": "localhost:0"}
	a, err := app.New(t.Context(), o, hello.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	base := "http://" + waitListening(t, capture, done)

	do := func(method, path, body string, cookies ...*http.Cookie) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}

	if resp, b := do(http.MethodGet, "/api/core/v1/auth/status", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", resp.StatusCode, b)
	}
	// A first run mints a bootstrap token into the state directory, and a
	// request that is not proven local must present it.
	tok, err := os.ReadFile(filepath.Join(stateDir, auth.BootstrapTokenFile))
	if err != nil {
		t.Fatalf("the first boot wrote no bootstrap token: %v", err)
	}
	creds := `{"username":"bruce.wayne","password":"correct-horse-batman-staple-9"}`
	setup := fmt.Sprintf(`{"username":"bruce.wayne","password":"correct-horse-batman-staple-9","bootstrap_token":%q}`, strings.TrimSpace(string(tok)))
	if resp, b := do(http.MethodPost, "/api/core/v1/auth/setup", setup); resp.StatusCode != http.StatusOK {
		t.Fatalf("setup = %d %s, want 200", resp.StatusCode, b)
	}
	resp, b := do(http.MethodPost, "/api/core/v1/auth/login", creds)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %s, want 200", resp.StatusCode, b)
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		t.Fatal("login set no cookie over plain HTTP")
	}
	for _, c := range cookies {
		if strings.HasPrefix(c.Name, "__Host-") {
			t.Errorf("plain-HTTP peer got a __Host- cookie %q", c.Name)
		}
	}
	if resp, b := do(http.MethodGet, "/api/core/v1/me", "", cookies...); resp.StatusCode != http.StatusOK {
		t.Fatalf("me = %d %s, want 200", resp.StatusCode, b)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("hello did not stop")
	}
}

// waitListening returns the address the kernel logged it listens on.
func waitListening(t *testing.T, c *logcapture.Capture, done <-chan error) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range c.Records() {
			if r.Message != "kernel: listening" {
				continue
			}
			var addr string
			r.Attrs(func(a slog.Attr) bool {
				if a.Key == "addr" {
					addr = a.Value.String()
				}
				return true
			})
			return addr
		}
		select {
		case err := <-done:
			t.Fatalf("Run returned before listening: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("the app never logged that it was listening")
	return ""
}
