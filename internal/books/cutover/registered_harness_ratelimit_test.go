// file: internal/books/cutover/registered_harness_ratelimit_test.go
// version: 0.1.0
// guid: 6f203fdb-689e-4e61-9b1f-16cc9f988b62
// last-edited: 2026-11-22

package cutover_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubT is a testing.TB that records a Fatalf instead of ending the test, so the
// harness's own failure messages can be asserted. Fatalf stops the code under test
// the way the real one does, by unwinding.
type stubT struct {
	testing.TB
	fatal string
}

type stubStop struct{}

func (*stubT) Helper() {}
func (s *stubT) Fatalf(format string, args ...any) {
	s.fatal = strings.TrimSpace(fmt.Sprintf(format, args...))
	panic(stubStop{})
}
func (s *stubT) Errorf(format string, args ...any) { s.fatal = fmt.Sprintf(format, args...) }

// run calls fn and returns what the harness failed with ("" when it did not).
func (s *stubT) run(fn func()) (failed string) {
	s.fatal = ""
	defer func() {
		failed = s.fatal
		if r := recover(); r != nil {
			if _, ok := r.(stubStop); !ok {
				panic(r)
			}
		}
	}()
	fn()
	return s.fatal
}

func answers(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
}

// TestRegisteredHarness_ARateLimitedAnswerFailsTheCell: a 429 from any request path of
// the harness (a principal's, a probe's, a raw request) fails the test with the
// message that says why, naming the request and who sent it; ExpectRateLimit is the
// one way to let it through, and it does not leak past its call.
func TestRegisteredHarness_ARateLimitedAnswerFailsTheCell(t *testing.T) {
	newH := func(status int) (*Registered, *stubT) {
		st := &stubT{TB: t}
		return &Registered{t: st, Handler: answers(status)}, st
	}
	who := &Principal{Name: "session:admin", peer: "192.0.2.10:41000"}

	t.Run("a principal's request", func(t *testing.T) {
		h, st := newH(http.StatusTooManyRequests)
		got := st.run(func() { _, _ = h.send(h.Handler, who, http.MethodGet, "/api/v1/audiobooks", nil, nil) })
		if want := "rate-limited, cell proves nothing: GET /api/v1/audiobooks as session:admin"; got != want {
			t.Errorf("failed with %q, want %q", got, want)
		}
	})
	t.Run("a raw request", func(t *testing.T) {
		h, st := newH(http.StatusTooManyRequests)
		got := st.run(func() { h.Raw(httptest.NewRequest(http.MethodPost, "/api/me", nil)) })
		if !strings.HasPrefix(got, "rate-limited, cell proves nothing: POST /api/me as ") {
			t.Errorf("failed with %q", got)
		}
	})
	t.Run("another refusal is not one", func(t *testing.T) {
		h, st := newH(http.StatusForbidden)
		if got := st.run(func() { _, _ = h.send(h.Handler, who, http.MethodGet, "/x", nil, nil) }); got != "" {
			t.Errorf("a 403 failed the cell: %q", got)
		}
	})
	t.Run("ExpectRateLimit lets it through, only inside", func(t *testing.T) {
		h, st := newH(http.StatusTooManyRequests)
		var res Result
		if got := st.run(func() {
			h.ExpectRateLimit(func() { res, _ = h.send(h.Handler, who, http.MethodGet, "/x", nil, nil) })
		}); got != "" || res.Status != http.StatusTooManyRequests {
			t.Errorf("an expected 429: failed with %q, status %d", got, res.Status)
		}
		if got := st.run(func() { _, _ = h.send(h.Handler, who, http.MethodGet, "/x", nil, nil) }); got == "" {
			t.Errorf("the opt-in outlived its call")
		}
	})
}

// TestRegisteredHarness_RefusedMatchesStatusAndCodeExactly: Refused accepts the one
// refusal it names and nothing else: not the same status with another code, not the
// same code under another status.
func TestRegisteredHarness_RefusedMatchesStatusAndCodeExactly(t *testing.T) {
	problem := Result{Status: http.StatusForbidden, Body: []byte(`{"type":"https://example.test/problems/permission_denied","code":"permission_denied","status":403}`)}
	typed := Result{Status: http.StatusUnauthorized, Body: []byte(`{"type":"https://example.test/problems/invalid_credentials","status":401}`)}
	plain := Result{Status: http.StatusForbidden, Body: []byte(`{"error":"identity is not authorized for this server"}`)}
	for name, c := range map[string]struct {
		res    Result
		status int
		code   string
		ok     bool
	}{
		"problem document":      {problem, 403, "permission_denied", true},
		"type segment":          {typed, 401, "invalid_credentials", true},
		"plain error message":   {plain, 403, "identity is not authorized for this server", true},
		"same status, no match": {problem, 403, "authentication_required", false},
		"same code, no match":   {problem, 401, "permission_denied", false},
		"a 429 for a 403":       {Result{Status: 429, Body: []byte(`{"code":"rate_limited"}`)}, 403, "permission_denied", false},
	} {
		st := &stubT{TB: t}
		h := &Registered{t: st}
		if got := h.Refused(c.res, c.status, c.code); got != c.ok {
			t.Errorf("%s: Refused = %v, want %v", name, got, c.ok)
		}
	}
}

// TestRegisteredHarness_RateLimiterIsKeyedByThePrincipalAddress: each principal has
// its own stable address; the limiter, on and keyed by it, gives each its own bucket.
// The boot's budget is 500 a minute (a burst of 100: the boot's own requests, the
// administrator minting the core keys most of them, fit in it). Three principals read
// 40 times each, 120 requests in all: inside every bucket, so no 429, whereas one
// address for all three would be over the burst and the first request past it fails
// the cell. Then one principal is driven past its own burst, which does throttle it,
// and another's bucket is untouched.
func TestRegisteredHarness_RateLimiterIsKeyedByThePrincipalAddress(t *testing.T) {
	h := bootRegistered(t, registeredConfig{RateLimitPerMinute: 500})
	const route = "/api/v1/audiobooks"
	who := []*Principal{h.Principal("key:admin:view"), h.Principal("session:editor"), h.Principal("session:viewer")}

	t.Run("own and stable address", func(t *testing.T) {
		seen := map[string]string{}
		for _, p := range h.Principals {
			if p.Kind == "authoff" {
				continue
			}
			a := h.peerFor(p)
			if a != h.peerFor(p) {
				t.Errorf("%s: the address changes between requests", p.Name)
			}
			host := a[:strings.LastIndex(a, ":")]
			if other, dup := seen[host]; dup {
				t.Errorf("%s and %s share the address %s", p.Name, other, host)
			}
			seen[host] = p.Name
		}
		if len(seen) < 36 {
			t.Errorf("only %d addresses for %d principals", len(seen), len(h.Principals))
		}
	})
	t.Run("a burst inside every bucket", func(t *testing.T) {
		for range 40 {
			for _, p := range who {
				if _, err := h.Do(p, http.MethodGet, route, nil); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	t.Run("past one principal's burst", func(t *testing.T) {
		limited := 0
		h.ExpectRateLimit(func() {
			for range 150 {
				res, err := h.Do(who[1], http.MethodGet, route, nil)
				if err != nil {
					t.Fatal(err)
				}
				if res.Status == http.StatusTooManyRequests {
					limited++
				}
			}
		})
		if limited == 0 {
			t.Errorf("150 requests from one address got no 429: the limiter is not keyed by it, or is off")
		}
		if res, err := h.Do(who[2], http.MethodGet, route, nil); err != nil || res.Status == http.StatusTooManyRequests {
			t.Errorf("another principal's bucket was spent: %d, %v", res.Status, err)
		}
	})
}
