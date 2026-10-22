// file: internal/server/https_redirect_redact_test.go
// version: 1.3.0
// guid: 493e9f52-1cdc-41c5-be6b-6a0d5ce98bc8
// last-edited: 2026-10-22

package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/logger/logtest"
)

// Review MEDIUM #2: the http->https redirect logged r.URL.String() verbatim,
// query string (and so ?token=) included.
func TestHTTPSRedirectHandler_LogsNoQueryCredential(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelDebug)

	secret := "abk_" + "redirect-secret"
	w := httptest.NewRecorder()
	newHTTPSRedirectHandler("192.0.2.10", "8484").ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "http://192.0.2.10/api/items/a/cover?token="+secret, nil))

	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d", w.Code)
	}
	if loc := w.Header().Get("Location"); !strings.Contains(loc, "token="+secret) {
		t.Fatalf("the redirect itself must keep the query (the client needs it): %q", loc)
	}
	if out := buf.String(); strings.Contains(out, "redirect-secret") || !strings.Contains(out, "REDACTED") {
		t.Fatalf("redirect log leaked or lost the credential marker: %s", out)
	}
}

// The invite token lives in the path, so the query-only redaction missed it: the
// debug line must mask it in both the url and the target, while the redirect
// itself keeps the real path.
func TestHTTPSRedirectHandler_LogsNoInviteToken(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelDebug)

	const tok = "redirect-invite-token-example"
	w := httptest.NewRecorder()
	newHTTPSRedirectHandler("192.0.2.10", "8484").ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "http://192.0.2.10/invite/"+tok, nil))

	if loc := w.Header().Get("Location"); loc != "https://192.0.2.10:8484/invite/"+tok {
		t.Fatalf("the redirect must keep the real path: %q", loc)
	}
	out := buf.String()
	if strings.Contains(out, tok) {
		t.Fatalf("redirect log leaked the invite token: %s", out)
	}
	if !strings.Contains(out, "https://192.0.2.10:8484/invite/[REDACTED]") {
		t.Fatalf("redirect log lost the readable target: %s", out)
	}
}

// SF2: the debug line logs RequestURI, which keeps an encoded slash escaped, so
// /api/v1/auth/invite%2F<token> must be masked in both the url and the target,
// while the redirect itself keeps the real path.
func TestHTTPSRedirectHandler_LogsNoEncodedSlashInviteToken(t *testing.T) {
	buf := logtest.Capture(t, slog.LevelDebug)

	const tok = "redirect-encoded-slash-token-example"
	w := httptest.NewRecorder()
	newHTTPSRedirectHandler("192.0.2.10", "8484").ServeHTTP(w,
		httptest.NewRequest(http.MethodGet, "http://192.0.2.10/api/v1/auth/invite%2F"+tok, nil))

	if loc := w.Header().Get("Location"); loc != "https://192.0.2.10:8484/api/v1/auth/invite%2F"+tok {
		t.Fatalf("the redirect must keep the real path: %q", loc)
	}
	out := buf.String()
	if strings.Contains(out, tok) {
		t.Fatalf("redirect log leaked the invite token: %s", out)
	}
	if !strings.Contains(out, "https://192.0.2.10:8484/api/v1/auth/invite/[REDACTED]") {
		t.Fatalf("redirect log lost the readable target: %s", out)
	}
}
