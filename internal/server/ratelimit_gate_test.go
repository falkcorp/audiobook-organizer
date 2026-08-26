// file: internal/server/ratelimit_gate_test.go
// version: 1.0.1
// guid: acf71ad4-2da2-41b4-9d39-0b1a3752cfa1
// last-edited: 2026-08-22

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/gin-gonic/gin"
)

// hitOperationsActive fires n requests against /api/v1/operations/active on
// the given server, all from the same client IP (httptest.NewRequest always
// sets RemoteAddr to 192.0.2.1:1234), and returns the count that came back
// http.StatusTooManyRequests. The route itself answers 410 Gone and touches
// no store, so any 429 seen can only be the apiRateLimiter middleware.
func hitOperationsActive(srv *Server, n int) (tooMany int) {
	for range n {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/operations/active", nil)
		w := httptest.NewRecorder()
		srv.router.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			tooMany++
		}
	}
	return tooMany
}

// TestRateLimit_DisabledBypassesLimiter is the CFG-AUDIT regression case:
// EnableRateLimit=false must disable the limiter regardless of
// APIRateLimitPerMinute's value. Before the fix, setupRoutes built the real
// IPRateLimiter purely from APIRateLimitPerMinute > 0 and never consulted
// EnableRateLimit, so this configuration (a positive rate left configured
// but enforcement explicitly turned off) still got rate-limited.
func TestRateLimit_DisabledBypassesLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origCfg })
	config.AppConfig.EnableRateLimit = false
	config.AppConfig.APIRateLimitPerMinute = 100 // burst would be 20; well above it below

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	// burst for rpm=100 would be 100/5=20 if the limiter were installed, so
	// firing well beyond that proves nothing is throttling requests.
	if got := hitOperationsActive(srv, 40); got != 0 {
		t.Fatalf("EnableRateLimit=false: got %d requests rate-limited (429), want 0 — the passthrough handler should be installed", got)
	}
}

// TestRateLimit_EnabledStillLimits is the anti-over-suppression twin: with
// EnableRateLimit=true and the same positive APIRateLimitPerMinute, the real
// limiter must still be installed and still enforce its burst threshold.
// This proves the EnableRateLimit guard added by the fix didn't also
// suppress the enabled case.
func TestRateLimit_EnabledStillLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origCfg })
	config.AppConfig.EnableRateLimit = true
	config.AppConfig.APIRateLimitPerMinute = 100 // burst = max(100/5, 10) = 20

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	if got := hitOperationsActive(srv, 40); got == 0 {
		t.Fatalf("EnableRateLimit=true, APIRateLimitPerMinute=100: got 0 requests rate-limited (429) out of 40, want at least 1 once the burst of 20 is exceeded")
	}
}

// TestRateLimit_EnabledZeroRateStaysNoop pins the edge case the fix
// deliberately left unchanged: EnableRateLimit=true with
// APIRateLimitPerMinute=0 must still be a no-op, since a limiter needs a
// positive rate to exist at all. Unlike the two tests above, this one
// passes on both the old and the fixed code — it is not a regression
// discriminator for this bug, just a guardrail so a future change to the
// EnableRateLimit guard doesn't accidentally start requiring rpm==0 to mean
// "limit at rate 0" instead of "no limiter".
func TestRateLimit_EnabledZeroRateStaysNoop(t *testing.T) {
	gin.SetMode(gin.TestMode)

	origCfg := config.AppConfig
	t.Cleanup(func() { config.AppConfig = origCfg })
	config.AppConfig.EnableRateLimit = true
	config.AppConfig.APIRateLimitPerMinute = 0

	srv := &Server{router: gin.New()}
	srv.setupRoutes()

	if got := hitOperationsActive(srv, 40); got != 0 {
		t.Fatalf("EnableRateLimit=true, APIRateLimitPerMinute=0: got %d requests rate-limited (429), want 0 — a zero rate must stay a no-op", got)
	}
}
