// file: internal/server/bootstrap_rate_key_test.go
// version: 1.1.0
// guid: 5d0e8a47-6b3c-4f19-9a52-c7e1b4d30f68
// last-edited: 2026-10-20

package server

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// resetBootstrapRate installs a fresh limiter for the test and restores a fresh
// one when it ends.
func resetBootstrapRate(t *testing.T) {
	t.Helper()
	bootstrapLimits = newBootstrapLimiter()
	t.Cleanup(func() { bootstrapLimits = newBootstrapLimiter() })
}

// setTunnelConnectors configures TUNNEL_CONNECTOR_ADDRS for the test.
func setTunnelConnectors(t *testing.T, csv string) {
	t.Helper()
	prev := config.TunnelConnectorAddrs()
	config.Mutate(func(c *config.Config) { c.TunnelConnectorAddrs = csv })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { c.TunnelConnectorAddrs = prev }) })
}

const bootstrapTestConnector = "198.51.100.20"

// bootstrapAttemptStatus posts one bootstrap attempt to a server with no store
// (so an attempt that passes the limiter answers 500 "database not
// initialized" without touching any token) and returns the status. verified
// stands in for a request whose Access assertion CloudflareAccessAuth verified.
func bootstrapAttemptStatus(t *testing.T, remote, cfIP string, verified bool) int {
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
	if verified {
		servermiddleware.MarkCFAccessVerified(c)
	}
	s.handleBootstrap(c)
	return w.Code
}

// exhaustBootstrap makes bootstrapMaxAttempts attempts (header from hdr(i)) and
// checks the next, from hdr(next), is refused.
func exhaustBootstrap(t *testing.T, remote string, verified bool, hdr func(i int) string, next string) {
	t.Helper()
	for i := range bootstrapMaxAttempts {
		if code := bootstrapAttemptStatus(t, remote, hdr(i), verified); code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d from %q limited early", i, remote)
		}
	}
	if code := bootstrapAttemptStatus(t, remote, next, verified); code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d from %q: %d, want 429", bootstrapMaxAttempts, remote, code)
	}
}

func fixed(v string) func(int) string { return func(int) string { return v } }

// TestHandleBootstrap_ConfiguredConnectorClientsHaveIndependentBuckets: through
// the configured connector one visitor exhausting its attempts must not lock
// out the owner at another address.
func TestHandleBootstrap_ConfiguredConnectorClientsHaveIndependentBuckets(t *testing.T) {
	resetBootstrapRate(t)
	setTunnelConnectors(t, bootstrapTestConnector)
	remote := bootstrapTestConnector + ":4000"
	exhaustBootstrap(t, remote, false, fixed("203.0.113.50"), "203.0.113.50")
	if code := bootstrapAttemptStatus(t, remote, "198.51.100.60", false); code == http.StatusTooManyRequests {
		t.Fatal("a different CF-Connecting-IP through the connector shares the attacker's bucket")
	}
}

// TestHandleBootstrap_VerifiedAccessLocalPeerIsPerIP: a verified Access
// assertion through a loopback peer makes the header believable.
func TestHandleBootstrap_VerifiedAccessLocalPeerIsPerIP(t *testing.T) {
	resetBootstrapRate(t)
	setTunnelConnectors(t, "")
	exhaustBootstrap(t, "127.0.0.1:4000", true, fixed("203.0.113.50"), "203.0.113.50")
	if code := bootstrapAttemptStatus(t, "127.0.0.1:4001", "198.51.100.60", true); code == http.StatusTooManyRequests {
		t.Fatal("a different verified CF-Connecting-IP shares the first client's bucket")
	}
}

// TestHandleBootstrap_UntrustedLocalSharesOneBoundedBucket: a loopback peer's
// header with no verified Access and no configured connector is not believed:
// rotating it buys no fresh buckets, and every such request shares one bounded
// bucket that does not touch attributed clients.
func TestHandleBootstrap_UntrustedLocalSharesOneBoundedBucket(t *testing.T) {
	resetBootstrapRate(t)
	setTunnelConnectors(t, "")
	exhaustBootstrap(t, "127.0.0.1:4000", false, func(i int) string { return fmt.Sprintf("203.0.113.%d", i+1) }, "203.0.113.200")
	for name, hdr := range map[string]string{"none": "", "garbage": "not-an-ip"} {
		if code := bootstrapAttemptStatus(t, "127.0.0.1:4001", hdr, false); code != http.StatusTooManyRequests {
			t.Fatalf("%s header while the shared bucket is spent: %d, want 429", name, code)
		}
	}
	// An attributed client (verified Access) is not limited by it.
	if code := bootstrapAttemptStatus(t, "127.0.0.1:4002", "198.51.100.61", true); code == http.StatusTooManyRequests {
		t.Fatal("an attributed client was limited by the untrusted-local bucket")
	}
}

// TestHandleBootstrap_ConnectorWithoutHeaderSharesTheUntrustedBucket: the
// connector itself, with no usable header, identifies no client.
func TestHandleBootstrap_ConnectorWithoutHeaderSharesTheUntrustedBucket(t *testing.T) {
	resetBootstrapRate(t)
	setTunnelConnectors(t, bootstrapTestConnector)
	remote := bootstrapTestConnector + ":4000"
	exhaustBootstrap(t, remote, false, fixed(""), "garbage")
	if code := bootstrapAttemptStatus(t, remote, "198.51.100.62", false); code == http.StatusTooManyRequests {
		t.Fatal("an attributed visitor was limited by the shared bucket")
	}
}

// TestHandleBootstrap_NonLocalPeerKeyedOnOwnAddress: a direct caller cannot
// dodge the limiter by cycling a spoofed CF-Connecting-IP.
func TestHandleBootstrap_NonLocalPeerKeyedOnOwnAddress(t *testing.T) {
	resetBootstrapRate(t)
	setTunnelConnectors(t, bootstrapTestConnector)
	exhaustBootstrap(t, "198.51.100.9:4000", true, func(i int) string { return fmt.Sprintf("203.0.113.%d", i+1) }, "203.0.113.99")
}

// TestHandleBootstrap_IPv6ClientsShareTheirSlash64: a visitor rotating addresses
// inside one /64 is one client.
func TestHandleBootstrap_IPv6ClientsShareTheirSlash64(t *testing.T) {
	resetBootstrapRate(t)
	setTunnelConnectors(t, bootstrapTestConnector)
	remote := bootstrapTestConnector + ":4000"
	exhaustBootstrap(t, remote, false, func(i int) string { return fmt.Sprintf("2001:db8:1:2::%x", i+1) }, "2001:db8:1:2:ffff::9")
	if code := bootstrapAttemptStatus(t, remote, "2001:db8:1:3::1", false); code == http.StatusTooManyRequests {
		t.Fatal("a different /64 shares the first one's bucket")
	}
}

func TestBootstrapRateKey(t *testing.T) {
	tests := []struct {
		name       string
		ip         string
		attributed bool
		want       string
	}{
		{"ipv4", "203.0.113.5", true, "203.0.113.5"},
		{"ipv4-mapped ipv6", "::ffff:203.0.113.5", true, "203.0.113.5"},
		{"ipv6 host bits dropped", "2001:db8:1:2:aaaa:bbbb:cccc:dddd", true, "2001:db8:1:2::/64"},
		{"ipv6 same /64, other host bits", "2001:db8:1:2::1", true, "2001:db8:1:2::/64"},
		{"ipv6 other /64", "2001:db8:1:3::1", true, "2001:db8:1:3::/64"},
		{"ipv6 zone dropped", "fe80::1%eth0", true, "fe80::/64"},
		{"not attributed", "203.0.113.5", false, untrustedLocalClientKey},
		{"unparseable", "not-an-ip", true, untrustedLocalClientKey},
		{"empty", "", true, untrustedLocalClientKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bootstrapRateKey(tt.ip, tt.attributed); got != tt.want {
				t.Fatalf("bootstrapRateKey(%q, %v) = %q, want %q", tt.ip, tt.attributed, got, tt.want)
			}
		})
	}
}

// testBootstrapLimiter builds a limiter on a controllable clock.
func testBootstrapLimiter(maxKeys, globalMax int) (*bootstrapLimiter, *time.Time) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	l := newBootstrapLimiter()
	l.maxKeys, l.globalMax = maxKeys, globalMax
	l.now = func() time.Time { return now }
	return l, &now
}

// bootstrapLimiterSize is the number of keys l tracks.
func bootstrapLimiterSize(l *bootstrapLimiter) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

func TestBootstrapLimiter_PerKeyWindow(t *testing.T) {
	l, now := testBootstrapLimiter(100, 1000)
	for i := range bootstrapMaxAttempts {
		if !l.allow("k") {
			t.Fatalf("attempt %d refused early", i)
		}
	}
	if l.allow("k") {
		t.Fatal("attempt past the per-key cap admitted")
	}
	if !l.allow("other") {
		t.Fatal("another key was refused")
	}
	*now = now.Add(bootstrapWindowDuration + time.Second)
	if !l.allow("k") {
		t.Fatal("the key's window ended but it is still refused")
	}
}

// TestBootstrapLimiter_SweepsExpiredEntries: ended windows are dropped on the
// next call, so the map does not hold them.
func TestBootstrapLimiter_SweepsExpiredEntries(t *testing.T) {
	l, now := testBootstrapLimiter(1000, 1000)
	for i := range 50 {
		l.allow(fmt.Sprintf("k%d", i))
	}
	if bootstrapLimiterSize(l) != 50 {
		t.Fatalf("size = %d, want 50", bootstrapLimiterSize(l))
	}
	*now = now.Add(bootstrapWindowDuration/2 + time.Second)
	l.allow("late") // a younger window that must survive the sweep below
	*now = now.Add(bootstrapWindowDuration/2 + time.Second)
	l.allow("trigger")
	if bootstrapLimiterSize(l) != 2 {
		t.Fatalf("size after the first 50 windows ended = %d, want 2 (late, trigger)", bootstrapLimiterSize(l))
	}
}

// TestBootstrapLimiter_HardCapEvictsOldest: rotating keys cannot grow the map
// past the cap; the oldest windows go first.
func TestBootstrapLimiter_HardCapEvictsOldest(t *testing.T) {
	const maxKeys = 8
	l, now := testBootstrapLimiter(maxKeys, 1_000_000)
	for i := range 10 * maxKeys {
		*now = now.Add(time.Second)
		l.allow(fmt.Sprintf("k%d", i))
		if bootstrapLimiterSize(l) > maxKeys {
			t.Fatalf("size = %d after %d keys, cap is %d", bootstrapLimiterSize(l), i+1, maxKeys)
		}
	}
	if bootstrapLimiterSize(l) != maxKeys {
		t.Fatalf("size = %d, want the cap %d", bootstrapLimiterSize(l), maxKeys)
	}
	l.mu.Lock()
	_, oldest := l.entries["k0"]
	_, newest := l.entries[fmt.Sprintf("k%d", 10*maxKeys-1)]
	l.mu.Unlock()
	if oldest || !newest {
		t.Fatalf("eviction order wrong: oldest present=%v, newest present=%v", oldest, newest)
	}
}

// TestBootstrapLimiter_GlobalCeiling: however many distinct keys appear, the
// total admitted per window is bounded; refused attempts spend nothing.
func TestBootstrapLimiter_GlobalCeiling(t *testing.T) {
	const globalMax = 12
	l, now := testBootstrapLimiter(1000, globalMax)
	for i := range globalMax {
		if !l.allow(fmt.Sprintf("k%d", i)) {
			t.Fatalf("distinct key %d refused under the ceiling", i)
		}
	}
	if l.allow("fresh") {
		t.Fatal("a fresh key was admitted past the global ceiling")
	}
	if bootstrapLimiterSize(l) != globalMax {
		t.Fatalf("a refused attempt created an entry: size = %d, want %d", bootstrapLimiterSize(l), globalMax)
	}
	*now = now.Add(bootstrapWindowDuration + time.Second)
	if !l.allow("fresh") {
		t.Fatal("the window ended but the global ceiling still refuses")
	}

	// A key refused by its OWN cap does not spend the global budget.
	l, _ = testBootstrapLimiter(1000, bootstrapMaxAttempts+1)
	for range bootstrapMaxAttempts {
		l.allow("k")
	}
	for range 50 {
		l.allow("k") // refused per key
	}
	if !l.allow("other") {
		t.Fatal("per-key refusals spent the global budget")
	}
}
