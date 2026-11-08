// file: core/kernel/perimeter.go
// version: 1.0.0
// guid: a5d309f5-6a3d-4bcc-b110-4a74d851c77f
// last-edited: 2026-11-08

package kernel

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/falkcorp/mediacore/httpapi"
)

// setPerimeter hands the perimeter values of v to the router before the
// first listener opens (06 §6.5.2 start step 7; 05-config §1.12.1: "Stages
// 0, 10 and 11, built at P8, read the returned values through one pointer
// the kernel sets before step (7)"). configured is false when the app has
// no R1Values, and every key is then at core's default (05-http-api §10).
// An allow-list entry that is not a prefix or an address fails boot, as
// HEAD's ALLOWED_PEER_CIDRS check did: serving with an entry dropped would
// refuse peers the operator listed. A trusted-proxy entry that cannot be
// used is logged and left out (HEAD's TUNNEL_CONNECTOR_ADDRS rule).
func (k *Kernel) setPerimeter(v R1Values, configured bool) error {
	allow, aerrs := parsePeers(v.PeerAllowlist)
	if err := errors.Join(aerrs...); err != nil {
		return fmt.Errorf("core.http.peer_allowlist: %w", err)
	}
	trusted, terrs := parsePeers(v.TrustedProxies)
	for _, e := range terrs {
		k.coreLog("http").Warn("kernel: core.http.trusted_proxies entry ignored", "error", e)
	}
	o := httpapi.Options{
		PeerAllowlist:  allow,
		TrustedProxies: trusted,
		ClientIPHeader: v.ClientIPHeader,
		Limits: httpapi.LimitOptions{
			RateLimitEnabled:   true,
			RateLimitPerMinute: v.RateLimitPerMinute,
			JSONMaxBytes:       v.JSONMaxBytes,
			UploadMaxBytes:     v.UploadMaxBytes,
		},
	}
	if configured {
		o.Limits.RateLimitEnabled = v.RateLimitEnabled
	}
	// Entries the router cannot use (a zone naming no interface) are logged
	// by the router; the rest apply.
	_ = k.router.SetPerimeter(o)
	return nil
}

// parsePeers parses prefixes ("192.0.2.0/24") and single addresses, which
// may carry an IPv6 zone ("fe80::5%eth0"), the forms of
// core.http.peer_allowlist and core.http.trusted_proxies.
func parsePeers(in []string) ([]httpapi.TrustedPeer, []error) {
	var out []httpapi.TrustedPeer
	var errs []error
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				errs = append(errs, fmt.Errorf("%q is not a CIDR network", s))
				continue
			}
			out = append(out, httpapi.TrustedPeer{Prefix: p})
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			errs = append(errs, fmt.Errorf("%q is not an IP address", s))
			continue
		}
		out = append(out, httpapi.TrustedPeer{Addr: a})
	}
	return out, errs
}
