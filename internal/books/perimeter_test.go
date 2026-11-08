// file: internal/books/perimeter_test.go
// version: 1.0.2
// guid: 7c2f9a14-3e8b-4d06-b5a1-9f4e2c8d6b37
// last-edited: 2026-11-08

package books

import (
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
		v := r1Values(server.ServerConfig{Host: "127.0.0.1", Port: "8484"}, config.Config{EnableRateLimit: c.enabled, APIRateLimitPerMinute: c.rpm})
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
	})
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
	})
	if want := []string{"192.0.2.10", "fe80::5%eth0", "203.0.113.53"}; !slices.Equal(v.TrustedProxies, want) {
		t.Fatalf("TrustedProxies = %v, want %v", v.TrustedProxies, want)
	}
}

// TestLogTee_AccessLinesStayOffTheActivityFeed: core's stage 4 access
// records go to stdout only, as gin's request log did, and never make an
// Activity entry. Stage 4 logs through core's http scope, so the record
// carries module=core right after its message and the tee's scopedLine
// (R1-15 review M2) sends it to stdout; this pins that for the record the
// composed App really writes.
func TestLogTee_AccessLinesStayOffTheActivityFeed(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	feed := &syncBuffer{}
	c.mod.logOut.setActivity(feed)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	req.RemoteAddr = "192.0.2.9:41000"
	c.app.Handler().ServeHTTP(httptest.NewRecorder(), req)

	var access []string
	for _, l := range nonEmptyLines(c.newLog.String()) {
		if strings.Contains(l, `msg="http access"`) {
			access = append(access, l)
		}
	}
	if len(access) != 1 {
		t.Fatalf("stdout got %d access records, want 1:\n%s", len(access), c.newLog.String())
	}
	if !strings.Contains(access[0], `msg="http access" module=core `) {
		t.Errorf("the access record does not lead with core's module, so scopedLine would send it to the feed: %s", access[0])
	}
	if strings.Contains(feed.String(), "http access") {
		t.Errorf("an access record reached the activity feed:\n%s", feed.String())
	}
}
