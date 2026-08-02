// file: internal/server/middleware/absauth_servicetoken_test.go
// version: 1.0.0
// guid: 9f2b60d4-38a1-4c75-b9e0-71a5c34e8206
// last-edited: 2026-08-02

package middleware

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// A Cloudflare service token's JWT names a CREDENTIAL, not a person:
//
//	{"type":"app","sub":"","common_name":"4f27b863….access","iss":"…","aud":"…"}
//
// These tests pin the two properties that make recording it useful without making it
// dangerous: it must never become identity, and it must appear on the SAME audit line
// as the user it accompanied.

// captureLogs swaps the default slog handler for the duration of fn.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

// resetPairings clears process-wide state so tests do not leak into each other.
func resetPairings(t *testing.T) {
	t.Helper()
	seenServiceTokenPairings = sync.Map{}
	t.Cleanup(func() { seenServiceTokenPairings = sync.Map{} })
}

func ctxWithServiceToken(token string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/me", nil)
	if token != "" {
		c.Set(contextABSServiceTokenKey, token)
	}
	return c
}

// ── the error carrying the common_name ──────────────────────────────────────

// 🔴 TestNonIdentityAssertionError_StillMatchesErrorsIs is a regression guard for the
// fall-through in ResolveCFAssertion. That branch is what lets a Mode B request
// (service token at the edge + our own bearer) authenticate at all; if the typed
// error stopped matching errors.Is, every such request would 401.
func TestNonIdentityAssertionError_StillMatchesErrorsIs(t *testing.T) {
	err := error(&oauth.NonIdentityAssertionError{Subject: "", CommonName: "family.access"})
	if !errors.Is(err, oauth.ErrNonIdentityAssertion) {
		t.Fatal("errors.Is no longer matches — ResolveCFAssertion would 401 every Mode B request")
	}
}

func TestCFAssertionCommonName(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"typed non-identity": {&oauth.NonIdentityAssertionError{CommonName: "testing.access"}, "testing.access"},
		"wrapped":            {errors.Join(&oauth.NonIdentityAssertionError{CommonName: "friends.access"}), "friends.access"},
		"bare sentinel":      {oauth.ErrNonIdentityAssertion, ""},
		"unrelated":          {errors.New("boom"), ""},
		"nil":                {nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := oauth.CFAssertionCommonName(tc.err); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ── the accessor ────────────────────────────────────────────────────────────

func TestABSServiceToken(t *testing.T) {
	if got := ABSServiceToken(ctxWithServiceToken("family.access")); got != "family.access" {
		t.Fatalf("got %q, want %q", got, "family.access")
	}
	if got := ABSServiceToken(ctxWithServiceToken("")); got != "" {
		t.Fatalf("got %q, want empty for a request with no service token", got)
	}
	if got := ABSServiceToken(nil); got != "" {
		t.Fatalf("got %q, want empty for a nil context", got)
	}
}

// ── failure attribution ─────────────────────────────────────────────────────

// TestAbortABSAuth_RecordsTheServiceToken: on a FAILED attempt there is no user_id
// and never will be, so the token is the only attribution the record can carry.
func TestAbortABSAuth_RecordsTheServiceToken(t *testing.T) {
	c := ctxWithServiceToken("testing.access")
	out := captureLogs(t, func() {
		AbortABSAuth(c, "resolve", absErr(401, "no-credential", "authentication required", ABSModeJWT))
	})
	if !strings.Contains(out, "service_token=testing.access") {
		t.Fatalf("failed-auth record carries no service_token:\n%s", out)
	}
}

// ── the pairing tripwire ────────────────────────────────────────────────────

func identity(userID, username string) *ABSIdentity {
	return &ABSIdentity{
		User: &database.User{ID: userID, Username: username},
		Mode: ABSModeJWT,
	}
}

// 🔴 TestNoteServiceTokenPairing_LogsTokenAndUserOnOneLine. The PAIRING is the whole
// signal: token↔person is normally stable, so `family` token + a friend's identity
// means either a compromised account or a leaked token. Split across two log lines
// that anomaly is invisible.
func TestNoteServiceTokenPairing_LogsTokenAndUserOnOneLine(t *testing.T) {
	resetPairings(t)
	out := captureLogs(t, func() {
		noteServiceTokenPairing("family.access", identity("u1", "owner"))
	})
	for _, want := range []string{"service_token=family.access", "user_id=u1", "username=owner", "first-seen"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if lines := strings.Count(strings.TrimSpace(out), "\n") + 1; lines != 1 {
		t.Fatalf("pairing spans %d lines; the token and the user must appear TOGETHER:\n%s", lines, out)
	}
}

// TestNoteServiceTokenPairing_QuietOnRepeat: the ABS surface is polled every 15-20 s
// per device, so an unconditional line would be pure journal noise.
func TestNoteServiceTokenPairing_QuietOnRepeat(t *testing.T) {
	resetPairings(t)
	noteServiceTokenPairing("family.access", identity("u1", "owner"))
	out := captureLogs(t, func() {
		for range 20 {
			noteServiceTokenPairing("family.access", identity("u1", "owner"))
		}
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("repeat pairings logged again:\n%s", out)
	}
}

// 🔴 TestNoteServiceTokenPairing_FiresWhenTheTokenChangesHands is the tripwire itself.
func TestNoteServiceTokenPairing_FiresWhenTheTokenChangesHands(t *testing.T) {
	resetPairings(t)
	noteServiceTokenPairing("family.access", identity("u1", "owner"))
	out := captureLogs(t, func() {
		noteServiceTokenPairing("family.access", identity("u2", "a-friend"))
	})
	if !strings.Contains(out, "service-token-now-used-by-a-different-user") {
		t.Fatalf("a token changing hands did not raise the tripwire:\n%s", out)
	}
	for _, want := range []string{"service_token=family.access", "user_id=u2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

// TestNoteServiceTokenPairing_IgnoresIncompleteInput: no token, or no resolved user,
// means there is no pairing to report — and never a panic.
func TestNoteServiceTokenPairing_IgnoresIncompleteInput(t *testing.T) {
	resetPairings(t)
	out := captureLogs(t, func() {
		noteServiceTokenPairing("", identity("u1", "owner"))
		noteServiceTokenPairing("family.access", nil)
		noteServiceTokenPairing("family.access", &ABSIdentity{})
	})
	if strings.TrimSpace(out) != "" {
		t.Fatalf("logged something for an incomplete pairing:\n%s", out)
	}
}

// TestNoteServiceTokenPairing_ConcurrentIsRaceFree — every ABS request calls this, so
// it runs concurrently by construction. Run with -race.
func TestNoteServiceTokenPairing_ConcurrentIsRaceFree(t *testing.T) {
	resetPairings(t)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			noteServiceTokenPairing("family.access", identity("u1", "owner"))
			_ = ABSServiceToken(ctxWithServiceToken("family.access"))
			_ = i
		}(i)
	}
	wg.Wait()
}
