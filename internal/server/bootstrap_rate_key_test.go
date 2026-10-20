// file: internal/server/bootstrap_rate_key_test.go
// version: 1.0.0
// guid: 5d0e8a47-6b3c-4f19-9a52-c7e1b4d30f68
// last-edited: 2026-10-20

package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// resetBootstrapRate clears the package-level attempt map for the test and
// again when it ends.
func resetBootstrapRate(t *testing.T) {
	t.Helper()
	reset := func() {
		bootstrapRateMu.Lock()
		bootstrapRate = map[string]*bootstrapAttempt{}
		bootstrapRateMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// bootstrapAttemptStatus posts one bootstrap attempt to a server with no store
// (so an attempt that passes the limiter answers 500 "database not
// initialized" without touching any token) and returns the status.
func bootstrapAttemptStatus(t *testing.T, remote, cfIP string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/bootstrap", bytes.NewReader([]byte(`{"token":"abbs_wrong"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	if cfIP != "" {
		req.Header.Set("CF-Connecting-IP", cfIP)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	s.handleBootstrap(c)
	return w.Code
}

// exhaust makes bootstrapMaxAttempts attempts and checks the next is refused.
func exhaustBootstrap(t *testing.T, remote, cfIP string) {
	t.Helper()
	for i := range bootstrapMaxAttempts {
		if code := bootstrapAttemptStatus(t, remote, cfIP); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d from %q/%q limited early", i, remote, cfIP)
		}
	}
	if code := bootstrapAttemptStatus(t, remote, cfIP); code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d from %q/%q: %d, want 429", bootstrapMaxAttempts, remote, cfIP, code)
	}
}

// TestHandleBootstrap_TunnelClientsHaveIndependentBuckets: behind the tunnel
// (loopback peer) one visitor exhausting its attempts must not lock out the
// owner at another address.
func TestHandleBootstrap_TunnelClientsHaveIndependentBuckets(t *testing.T) {
	resetBootstrapRate(t)
	exhaustBootstrap(t, "127.0.0.1:4000", "203.0.113.50")
	if code := bootstrapAttemptStatus(t, "127.0.0.1:4001", "198.51.100.60"); code == http.StatusTooManyRequests {
		t.Fatal("a different CF-Connecting-IP through the tunnel shares the attacker's bucket")
	}
}

// TestHandleBootstrap_UnattributedLocalSharesOneBoundedBucket: a local peer
// with no usable header is still limited (the endpoint is high-value), in one
// fixed shared bucket that does not touch attributed clients.
func TestHandleBootstrap_UnattributedLocalSharesOneBoundedBucket(t *testing.T) {
	resetBootstrapRate(t)
	exhaustBootstrap(t, "127.0.0.1:4000", "")
	// Garbage header lands in the same unattributed bucket.
	if code := bootstrapAttemptStatus(t, "127.0.0.1:4001", "not-an-ip"); code != http.StatusTooManyRequests {
		t.Fatalf("unattributed request with garbage header: %d, want 429 (shared bucket)", code)
	}
	if code := bootstrapAttemptStatus(t, "127.0.0.1:4002", "198.51.100.61"); code == http.StatusTooManyRequests {
		t.Fatal("an attributed tunnel client was limited by the unattributed bucket")
	}
}

// TestHandleBootstrap_NonLocalPeerKeyedOnOwnAddress: a direct caller cannot
// dodge the limiter by cycling a spoofed CF-Connecting-IP.
func TestHandleBootstrap_NonLocalPeerKeyedOnOwnAddress(t *testing.T) {
	resetBootstrapRate(t)
	for i := range bootstrapMaxAttempts {
		hdr := "203.0.113." + string(rune('1'+i))
		if code := bootstrapAttemptStatus(t, "198.51.100.9:4000", hdr); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d limited early", i)
		}
	}
	if code := bootstrapAttemptStatus(t, "198.51.100.9:4000", "203.0.113.99"); code != http.StatusTooManyRequests {
		t.Fatalf("spoofed header bought a fresh bucket: %d, want 429", code)
	}
}
