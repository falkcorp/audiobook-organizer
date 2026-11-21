// file: internal/books/cutover/perimeter_rules.go
// version: 1.0.0
// guid: 4e8a1d57-3b2c-4f69-9a0e-7c5d2b8f1a34
// last-edited: 2026-11-20

package cutover

import (
	"net/netip"
	"strconv"
	"strings"
)

// Two rules HEAD applied when it read its perimeter settings, which core's
// keys do not carry. Core reads core.http.* itself,
// so M2 applies them once, to the values it moves.

// perimeterRules applies HEAD's two perimeter rules to the effective tree,
// before the rename table converts it:
//
//   - tunnel_connector_addrs: HEAD trusted a connector by its exact address
//     and ignored any other entry (client_trust.go, parseConnectorPeers).
//     core.http.trusted_proxies takes prefixes too, and stage 0 would then
//     trust every host in the range to name the client, so an entry that is not
//     an address (a CIDR range, a name) is dropped here, not moved.
//   - enable_rate_limit: HEAD limited only when the switch was on and
//     api_rate_limit_per_minute was positive (the CFG-AUDIT rule its
//     setupRoutes enforced). Core reads a per_minute of 0 as its default of
//     100, so a switch that was on over a threshold of 0 would start limiting;
//     it is moved as off.
//
// It reports each change by key and count, never by value: an address list is
// an operator's network layout.
func (pl *planner) perimeterRules() {
	if csv, ok := asString(pl.tree["tunnel_connector_addrs"]); ok {
		kept, dropped := connectorAddresses(csv)
		if dropped > 0 {
			if len(kept) == 0 {
				delete(pl.tree, "tunnel_connector_addrs")
			} else {
				pl.tree["tunnel_connector_addrs"] = strings.Join(kept, ",")
			}
			what := "1 entry that is not an exact address was"
			if dropped != 1 {
				what = strconv.Itoa(dropped) + " entries that are not exact addresses were"
			}
			pl.p.Reported = append(pl.p.Reported, "tunnel_connector_addrs: "+what+" not moved to core.http.trusted_proxies (HEAD ignored them too)")
			pl.p.count("perimeter-rules")
		}
	}
	if n, ok := headNumber(pl.tree["api_rate_limit_per_minute"]); ok && n <= 0 {
		if on, _ := pl.tree["enable_rate_limit"].(bool); on {
			pl.tree["enable_rate_limit"] = false
			pl.p.Reported = append(pl.p.Reported, "enable_rate_limit: was on with api_rate_limit_per_minute at 0, which HEAD read as no limit; moved as off")
			pl.p.count("perimeter-rules")
		}
	}
}

// connectorAddresses splits a comma-separated TUNNEL_CONNECTOR_ADDRS value into
// the entries that are exact addresses (an IPv6 zone allowed), in order, and
// the number of non-empty entries that are not.
func connectorAddresses(csv string) (kept []string, dropped int) {
	for e := range strings.SplitSeq(csv, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if _, err := netip.ParseAddr(e); err != nil {
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	return kept, dropped
}
