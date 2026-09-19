// file: internal/server/https_redirect_redact_test.go
// version: 1.0.0
// guid: 493e9f52-1cdc-41c5-be6b-6a0d5ce98bc8
// last-edited: 2026-09-19

package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Review MEDIUM #2: the http->https redirect logged r.URL.String() verbatim,
// query string (and so ?token=) included.
func TestHTTPSRedirectHandler_LogsNoQueryCredential(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

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
