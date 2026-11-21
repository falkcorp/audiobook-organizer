// file: core/kernel/perimeter.go
// version: 1.0.1
// guid: a5d309f5-6a3d-4bcc-b110-4a74d851c77f
// last-edited: 2026-11-08

package kernel

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/httpapi"
)

// setPerimeter hands the perimeter values of core.http to the router (06
// §6.5.2 start step 7: stages 0, 10 and 11, built at P8, read them through
// one pointer the kernel sets before the first listener opens; 05-http-api
// §10). An allow-list entry that is not a prefix or an address is refused, as
// HEAD's ALLOWED_PEER_CIDRS check refused it: serving with an entry dropped
// would turn away peers the operator listed. At start the refusal fails boot;
// after a change (followPerimeter) the previous values stay in force. A
// trusted-proxy entry that cannot be used is logged and left out (HEAD's
// TUNNEL_CONNECTOR_ADDRS rule).
func (k *Kernel) setPerimeter(s *httpSettings) error {
	allow, aerrs := parsePeers(s.PeerAllowlist)
	if err := errors.Join(aerrs...); err != nil {
		return fmt.Errorf("core.http.peer_allowlist: %w", err)
	}
	trusted, terrs := parsePeers(s.TrustedProxies)
	for _, e := range terrs {
		k.coreLog("http").Warn("kernel: core.http.trusted_proxies entry ignored", "error", e)
	}
	o := httpapi.Options{
		PeerAllowlist:  allow,
		TrustedProxies: trusted,
		ClientIPHeader: s.ClientIPHeader,
		Limits: httpapi.LimitOptions{
			RateLimitEnabled:   s.RateLimit.Enabled,
			RateLimitPerMinute: s.RateLimit.PerMinute,
			JSONMaxBytes:       int64(s.Body.JSONMaxBytes),
			UploadMaxBytes:     int64(s.Body.UploadMaxBytes),
		},
	}
	// An entry the router cannot use (a link-local address without a zone,
	// a zone naming no interface, which is retried as requests arrive, or a
	// bad CORS origin) is left out; the rest apply.
	if err := k.router.SetPerimeter(o); err != nil {
		k.coreLog("http").Warn("kernel: core.http perimeter entry ignored", "error", err)
	}
	return nil
}

// followPerimeter keeps the router's perimeter in step with core.http: these
// keys are live (05-http-api §10), so a change committed after start reaches
// stages 0, 10 and 11 without a restart. The subscription ends with the
// kernel (closeCore).
func (k *Kernel) followPerimeter(h *config.Handle[httpSettings]) {
	k.perimeterStop = h.OnChange(func(ctx context.Context, _, next *httpSettings) {
		if err := k.setPerimeter(next); err != nil {
			k.coreLog("http").WarnContext(ctx, "kernel: core.http change refused; the previous perimeter stays in force", "error", err)
		}
	})
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
