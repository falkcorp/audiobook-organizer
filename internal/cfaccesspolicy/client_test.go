// file: internal/cfaccesspolicy/client_test.go
// version: 1.1.0
// guid: 0b7e2d5a-91c4-4f38-8a6d-e3f15c2b9a70
// last-edited: 2026-10-19

package cfaccesspolicy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testAccount = "0123456789abcdef0123456789abcdef"
	testPolicy  = "11111111-2222-3333-4444-555555555555"
	testToken   = "cf-token-SENTINEL-9z8y"
)

// fakeCF serves one policy and records every request.
type fakeCF struct {
	mu       sync.Mutex
	policy   map[string]any
	methods  []string
	paths    []string
	putBody  map[string]any
	failPut  bool
	authSeen []string
}

func (f *fakeCF) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.methods = append(f.methods, r.Method)
		f.paths = append(f.paths, r.URL.Path)
		f.authSeen = append(f.authSeen, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": f.policy})
		case http.MethodPut:
			if f.failPut {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": false,
					"errors": []any{map[string]any{"code": 10000, "message": "Authentication error"}}})
				return
			}
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			dec := json.NewDecoder(strings.NewReader(string(b)))
			dec.UseNumber()
			if err := dec.Decode(&body); err != nil {
				t.Errorf("PUT body: %v", err)
			}
			f.putBody = body
			f.policy = body
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": body})
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func newPolicy() map[string]any {
	return map[string]any{
		"id":               testPolicy,
		"name":             "audiobook-guests",
		"decision":         "allow",
		"session_duration": "24h",
		"include": []any{
			map[string]any{"email": map[string]any{"email": "existing@example.com"}},
			map[string]any{"email_domain": map[string]any{"domain": "example.org"}},
		},
		"exclude":           []any{map[string]any{"email": map[string]any{"email": "blocked@example.com"}}},
		"require":           []any{map[string]any{"geo": map[string]any{"country_code": "US"}}},
		"approval_groups":   []any{map[string]any{"approvals_needed": 1}},
		"some_future_field": map[string]any{"nested": true},
	}
}

func newClient(t *testing.T, f *fakeCF) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	c, err := New(Config{AccountID: testAccount, PolicyID: testPolicy, APIToken: testToken, BaseURL: srv.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestEnsureEmail_AppendsAndPreservesEverything(t *testing.T) {
	f := &fakeCF{policy: newPolicy()}
	c := newClient(t, f)
	res, err := c.EnsureEmail(context.Background(), "  New.Guest@Example.COM ")
	if err != nil {
		t.Fatalf("EnsureEmail: %v", err)
	}
	if res != Added {
		t.Fatalf("result = %q, want added", res)
	}
	wantPath := "/accounts/" + testAccount + "/access/policies/" + testPolicy
	for i, p := range f.paths {
		if p != wantPath {
			t.Fatalf("request %d went to %q, want only %q", i, p, wantPath)
		}
		if f.authSeen[i] != "Bearer "+testToken {
			t.Fatalf("request %d Authorization = %q", i, f.authSeen[i])
		}
	}
	if strings.Join(f.methods, ",") != "GET,PUT" {
		t.Fatalf("methods = %v", f.methods)
	}
	// Every original field round-trips unchanged; include only grows.
	orig := newPolicy()
	for k, v := range orig {
		if k == "include" {
			continue
		}
		gotJSON, _ := json.Marshal(f.putBody[k])
		wantJSON, _ := json.Marshal(v)
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("field %s changed: got %s want %s", k, gotJSON, wantJSON)
		}
	}
	inc := f.putBody["include"].([]any)
	if len(inc) != 3 {
		t.Fatalf("include has %d rules, want 3", len(inc))
	}
	last, _ := json.Marshal(inc[2])
	if string(last) != `{"email":{"email":"new.guest@example.com"}}` {
		t.Fatalf("appended rule = %s", last)
	}
	first, _ := json.Marshal(inc[0])
	if string(first) != `{"email":{"email":"existing@example.com"}}` {
		t.Fatalf("existing rule rewritten: %s", first)
	}
}

func TestEnsureEmail_IdempotentCaseInsensitive(t *testing.T) {
	f := &fakeCF{policy: newPolicy()}
	c := newClient(t, f)
	res, err := c.EnsureEmail(context.Background(), "EXISTING@example.com")
	if err != nil || res != AlreadyPresent {
		t.Fatalf("res=%q err=%v, want already_present", res, err)
	}
	if strings.Join(f.methods, ",") != "GET" {
		t.Fatalf("an already-present email must not PUT: %v", f.methods)
	}
	// A second add of a new email is idempotent too.
	if _, err := c.EnsureEmail(context.Background(), "a@example.com"); err != nil {
		t.Fatal(err)
	}
	res, err = c.EnsureEmail(context.Background(), "A@example.com")
	if err != nil || res != AlreadyPresent {
		t.Fatalf("second add: res=%q err=%v", res, err)
	}
}

func TestEnsureEmail_ErrorNeverContainsToken(t *testing.T) {
	f := &fakeCF{policy: newPolicy(), failPut: true}
	c := newClient(t, f)
	_, err := c.EnsureEmail(context.Background(), "x@example.com")
	if err == nil {
		t.Fatal("a refused PUT must be an error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatal("error contains the API token")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "Authentication error") {
		t.Fatalf("error should carry Cloudflare's status and message: %v", err)
	}
}

func TestEnsureEmail_TimeoutIsBounded(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(block); srv.Close() })
	c, err := New(Config{AccountID: testAccount, PolicyID: testPolicy, APIToken: testToken, BaseURL: srv.URL, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := c.EnsureEmail(context.Background(), "x@example.com"); err == nil {
		t.Fatal("a stalled API must fail")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("EnsureEmail ignored its timeout")
	}
}

func TestEnsureEmail_ConcurrentAddsLoseNothing(t *testing.T) {
	f := &fakeCF{policy: newPolicy()}
	c := newClient(t, f)
	var wg sync.WaitGroup
	emails := []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}
	for _, e := range emails {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.EnsureEmail(context.Background(), e); err != nil {
				t.Errorf("%s: %v", e, err)
			}
		}()
	}
	wg.Wait()
	inc, _ := includeList(f.policy)
	for _, e := range emails {
		if !hasEmail(inc, e) {
			t.Fatalf("%s lost by a concurrent PUT", e)
		}
	}
}

func TestNew_ValidatesScope(t *testing.T) {
	bad := []Config{
		{AccountID: "short", PolicyID: testPolicy, APIToken: testToken},
		{AccountID: testAccount, PolicyID: "../../zones", APIToken: testToken},
		{AccountID: testAccount, PolicyID: testPolicy, APIToken: ""},
		{AccountID: testAccount, PolicyID: testPolicy, APIToken: "a\nb"},
	}
	for i, cfg := range bad {
		if _, err := New(cfg); err == nil {
			t.Fatalf("case %d: invalid config accepted", i)
		}
	}
}

func TestNormalizeEmail(t *testing.T) {
	if e, err := NormalizeEmail(" Guest@Example.com "); err != nil || e != "guest@example.com" {
		t.Fatalf("got %q %v", e, err)
	}
	for _, bad := range []string{"", "Name <a@example.com>", "a@example.com,b@example.com", "a@example.com\r\n", "nope"} {
		if _, err := NormalizeEmail(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestRemoveEmail_DropsOnlyThatRuleAndPreservesTheRest(t *testing.T) {
	f := &fakeCF{policy: newPolicy()}
	f.policy["include"] = append(f.policy["include"].([]any), map[string]any{"email": map[string]any{"email": "guest@example.com"}})
	c := newClient(t, f)
	res, err := c.RemoveEmail(context.Background(), "Guest@Example.com")
	if err != nil || res != Removed {
		t.Fatalf("res=%q err=%v", res, err)
	}
	inc := f.putBody["include"].([]any)
	if len(inc) != 2 || hasEmail(inc, "guest@example.com") || !hasEmail(inc, "existing@example.com") {
		t.Fatalf("include after remove = %v", inc)
	}
	for k, v := range newPolicy() {
		if k == "include" {
			continue
		}
		got, _ := json.Marshal(f.putBody[k])
		want, _ := json.Marshal(v)
		if string(got) != string(want) {
			t.Fatalf("field %s changed: %s vs %s", k, got, want)
		}
	}
	// Idempotent.
	f.methods = nil
	if res, err := c.RemoveEmail(context.Background(), "guest@example.com"); err != nil || res != NotPresent {
		t.Fatalf("second remove: %q %v", res, err)
	}
	if strings.Join(f.methods, ",") != "GET" {
		t.Fatalf("a no-op remove must not PUT: %v", f.methods)
	}
}

func TestRemoveEmail_RefusesToEmptyThePolicy(t *testing.T) {
	p := newPolicy()
	p["include"] = []any{map[string]any{"email": map[string]any{"email": "only@example.com"}}}
	f := &fakeCF{policy: p}
	c := newClient(t, f)
	if _, err := c.RemoveEmail(context.Background(), "only@example.com"); err == nil {
		t.Fatal("removing the only include rule must be refused")
	}
	if strings.Contains(strings.Join(f.methods, ","), "PUT") {
		t.Fatal("refused remove wrote the policy")
	}
}
