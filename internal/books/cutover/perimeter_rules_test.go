// file: internal/books/cutover/perimeter_rules_test.go
// version: 1.0.0
// guid: b3d6f1a8-2c47-4e95-8a10-5f9e7d2c4b63
// last-edited: 2026-11-20

package cutover

import (
	"slices"
	"strings"
	"testing"
)

// planWith is a planner over tree with an empty plan, the state perimeterRules
// sees.
func planWith(tree map[string]any) *planner {
	return &planner{tree: tree, p: &plan{Counts: map[string]int{}}}
}

// HEAD's setupRoutes limited /api only when enable_rate_limit was on and
// api_rate_limit_per_minute was positive (the CFG-AUDIT rule). Core reads a
// per_minute of 0 as its default of 100, so a switch that was on over a
// threshold of 0 would start limiting after the flip: M2 moves it as off, and
// leaves every other combination alone. (These cells were
// TestR1Values_RateLimitNeedsSwitchAndThreshold, over books' r1Values, until
// core read core.http.* itself.)
func TestPerimeterRules_RateLimitNeedsSwitchAndThreshold(t *testing.T) {
	for _, c := range []struct {
		name        string
		tree        map[string]any
		wantEnabled any
		wantMoved   bool
	}{
		{"on over a threshold of 0 moves as off", map[string]any{"enable_rate_limit": true, "api_rate_limit_per_minute": float64(0)}, false, true},
		{"on over a negative threshold moves as off", map[string]any{"enable_rate_limit": true, "api_rate_limit_per_minute": float64(-5)}, false, true},
		{"on over a positive threshold is kept", map[string]any{"enable_rate_limit": true, "api_rate_limit_per_minute": float64(100)}, true, false},
		{"off over a threshold stays off", map[string]any{"enable_rate_limit": false, "api_rate_limit_per_minute": float64(100)}, false, false},
		{"off over 0 stays off, unreported", map[string]any{"enable_rate_limit": false, "api_rate_limit_per_minute": float64(0)}, false, false},
		{"on with no threshold in the blob is left to core's default", map[string]any{"enable_rate_limit": true}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			pl := planWith(c.tree)
			pl.perimeterRules()
			if got := pl.tree["enable_rate_limit"]; got != c.wantEnabled {
				t.Errorf("enable_rate_limit = %v, want %v", got, c.wantEnabled)
			}
			if moved := pl.p.Counts["perimeter-rules"] == 1; moved != c.wantMoved {
				t.Errorf("reported a change = %v (reported %v), want %v", moved, pl.p.Reported, c.wantMoved)
			}
		})
	}
}

// HEAD trusted a tunnel connector by its exact address and ignored anything
// else (client_trust.go warns about it). core.http.trusted_proxies takes
// prefixes, so a CIDR that moved would make stage 0 trust the whole range: it
// is dropped, and the report names the key and a count, never an entry. (Was
// TestR1Values_TunnelConnectorsAddressOnly.)
func TestPerimeterRules_TunnelConnectorsAddressOnly(t *testing.T) {
	pl := planWith(map[string]any{
		"tunnel_connector_addrs": "192.0.2.10, 198.51.100.0/24, fe80::5%eth0, arkham-asylum, 2001:db8::/32, 203.0.113.53",
	})
	pl.perimeterRules()
	got, _ := pl.tree["tunnel_connector_addrs"].(string)
	if want := []string{"192.0.2.10", "fe80::5%eth0", "203.0.113.53"}; !slices.Equal(strings.Split(got, ","), want) {
		t.Fatalf("tunnel_connector_addrs = %q, want %v", got, want)
	}
	if len(pl.p.Reported) != 1 || !strings.Contains(pl.p.Reported[0], "tunnel_connector_addrs: 3 entries that are not exact addresses were not moved") {
		t.Fatalf("reported %v, want one line naming the key and 3 entries", pl.p.Reported)
	}
	for _, line := range pl.p.Reported {
		for _, leaked := range []string{"198.51.100", "arkham", "2001:db8", "192.0.2.10"} {
			if strings.Contains(line, leaked) {
				t.Errorf("the report names an entry: %q", line)
			}
		}
	}

	t.Run("a list of addresses only is untouched and unreported", func(t *testing.T) {
		pl := planWith(map[string]any{"tunnel_connector_addrs": "192.0.2.10,203.0.113.53"})
		pl.perimeterRules()
		if pl.tree["tunnel_connector_addrs"] != "192.0.2.10,203.0.113.53" || len(pl.p.Reported) != 0 {
			t.Fatalf("tree %v reported %v", pl.tree, pl.p.Reported)
		}
	})
	t.Run("a list with no address left moves nothing", func(t *testing.T) {
		pl := planWith(map[string]any{"tunnel_connector_addrs": "198.51.100.0/24"})
		pl.perimeterRules()
		if _, ok := pl.tree["tunnel_connector_addrs"]; ok {
			t.Fatalf("the key stayed with every entry dropped: %v", pl.tree)
		}
		if len(pl.p.Reported) != 1 || !strings.Contains(pl.p.Reported[0], "1 entry that is not an exact address was not moved") {
			t.Fatalf("reported %v", pl.p.Reported)
		}
	})
}
