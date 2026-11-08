// file: internal/books/perimeter_test.go
// version: 1.0.2
// guid: 7c2f9a14-3e8b-4d06-b5a1-9f4e2c8d6b37
// last-edited: 2026-11-08

package books

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/server"
)

// TestR1Values_RateLimitNeedsSwitchAndThreshold is the CFG-AUDIT rule HEAD's
// setupRoutes enforced on its /api limiter (ratelimit_gate_test.go until
// R1-17), now on the value core's stage 10 reads: enable_rate_limit is the
// switch and a positive api_rate_limit_per_minute the threshold, and both
// must hold.
func TestR1Values_RateLimitNeedsSwitchAndThreshold(t *testing.T) {
	for _, c := range []struct {
		enabled bool
		rpm     int
		want    bool
	}{
		{false, 100, false}, // the regression: a rate left configured, enforcement off
		{true, 100, true},
		{true, 0, false},
		{false, 0, false},
	} {
		v := r1Values(server.ServerConfig{Host: "127.0.0.1", Port: "8484"}, config.Config{EnableRateLimit: c.enabled, APIRateLimitPerMinute: c.rpm}, 0)
		if v.RateLimitEnabled != c.want || v.RateLimitPerMinute != c.rpm {
			t.Errorf("enable_rate_limit=%v per_minute=%d: RateLimitEnabled=%v PerMinute=%d, want %v %d",
				c.enabled, c.rpm, v.RateLimitEnabled, v.RateLimitPerMinute, c.want, c.rpm)
		}
	}
}

// TestR1Values_PerimeterKeys: the connectors' header and the comma lists.
func TestR1Values_PerimeterKeys(t *testing.T) {
	v := r1Values(server.ServerConfig{Host: "127.0.0.1", Port: "8484"}, config.Config{
		AllowedPeerCIDRs:     " 192.0.2.0/24, ,198.51.100.7 ",
		TunnelConnectorAddrs: "fe80::5%eth0",
		JSONBodyLimitMB:      2,
		UploadBodyLimitMB:    50,
	}, 0)
	if v.ClientIPHeader != "CF-Connecting-IP" || len(v.PeerAllowlist) != 2 || v.PeerAllowlist[1] != "198.51.100.7" ||
		len(v.TrustedProxies) != 1 || v.JSONMaxBytes != 2<<20 || v.UploadMaxBytes != 50<<20 {
		t.Fatalf("r1Values = %+v", v)
	}
}

// TestR1Values_TunnelConnectorsAddressOnly: HEAD trusted a connector by its
// exact address and ignored anything else (client_trust.go warns about it).
// A CIDR is dropped too, because core's stage 0 would trust the whole range.
func TestR1Values_TunnelConnectorsAddressOnly(t *testing.T) {
	v := r1Values(server.ServerConfig{Host: "127.0.0.1", Port: "8484"}, config.Config{
		TunnelConnectorAddrs: "192.0.2.10, 198.51.100.0/24, fe80::5%eth0, arkham-asylum, 2001:db8::/32, 203.0.113.53",
	}, 0)
	if want := []string{"192.0.2.10", "fe80::5%eth0", "203.0.113.53"}; !slices.Equal(v.TrustedProxies, want) {
		t.Fatalf("TrustedProxies = %v, want %v", v.TrustedProxies, want)
	}
}

// TestActivityTap_AccessLinesMakeNoEntries: core's stage 4 access records
// are printed, as gin's request log was, and never make an Activity entry.
// Stage 4 logs through core's http scope, not the process default logger, so
// the Activity tap skips it (R1-18, Record.Process); this pins that for the
// record the composed App really writes. The marker, logged after the
// request, is the first entry that arrives: the tap's queue is FIFO.
func TestActivityTap_AccessLinesMakeNoEntries(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "192.0.2.9:41000"
	c.app.Handler().ServeHTTP(httptest.NewRecorder(), req)
	slog.Info("alfred marker")

	if e := nextActivity(t, c); !strings.Contains(e.Summary, "alfred marker") {
		t.Errorf("an access record became an Activity entry: %+v", e)
	}
	var access []string
	for _, l := range strings.Split(c.newLog.String(), "\n") {
		if strings.Contains(l, `msg="http access"`) {
			access = append(access, l)
		}
	}
	if len(access) != 1 {
		t.Fatalf("the log stream got %d access records, want 1:\n%s", len(access), c.newLog.String())
	}
	if !strings.Contains(access[0], "module=core") {
		t.Errorf("the access record is not core's: %s", access[0])
	}
}
