// file: internal/aidispatch/locality.go
// version: 1.0.0
// guid: a662f5a8-9d5f-4cfb-9f74-51e8e591662f
// last-edited: 2026-09-19

package aidispatch

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// Locality says whether an endpoint keeps data on the operator's own network.
type Locality string

const (
	// LocalityLocal: the endpoint runs on this host or the private LAN.
	LocalityLocal Locality = "local"
	// LocalityCloud: anything else, including anything we cannot prove local.
	LocalityCloud Locality = "cloud"
)

// cgnat is 100.64.0.0/10 (RFC 6598), where Tailscale-style overlays live.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// localHostSuffixes are names that never resolve through public DNS.
var localHostSuffixes = []string{".localhost", ".local", ".lan", ".internal", ".home.arpa"}

// EndpointLocality classifies ep. The rule, fail-closed toward "cloud":
//
//  1. An auth_ref makes it CLOUD. A row that needs a stored provider secret
//     is a hosted API, whatever its URL says (a test double or a proxy on
//     127.0.0.1 for api.openai.com is still OpenAI traffic by intent).
//  2. local_process is LOCAL (it has no network address).
//  3. Otherwise the URL host decides: a loopback, RFC 1918/ULA private,
//     link-local or CGNAT (100.64/10) IP literal, "localhost", a name under
//     .localhost/.local/.lan/.internal/.home.arpa, or a single-label name
//     (no dot, so no public DNS) is LOCAL.
//  4. Everything else -- any dotted public name, a public IP, an unparsable
//     URL or a missing host -- is CLOUD.
func EndpointLocality(ep Endpoint) Locality {
	if ep.AuthRef != "" {
		return LocalityCloud
	}
	if ep.Protocol == ProtocolLocalProcess {
		return LocalityLocal
	}
	u, err := url.Parse(ep.URL)
	if err != nil || u.Hostname() == "" {
		return LocalityCloud
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if addr, err := netip.ParseAddr(host); err == nil {
		ip := net.IP(addr.AsSlice())
		if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr.Unmap()) || ip.IsLoopback() {
			return LocalityLocal
		}
		return LocalityCloud
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return LocalityLocal
	}
	for _, s := range localHostSuffixes {
		if strings.HasSuffix(host, s) {
			return LocalityLocal
		}
	}
	return LocalityCloud
}

// WithLocalOnly refuses every endpoint EndpointLocality classifies as cloud.
// Routed work in a mode the operator configured as local (llm_mode local,
// embedding_mode local) must never reach a hosted API: legacy local mode
// never contacts one, and spillover or failover must not change that.
func WithLocalOnly() Option { return func(d *Dispatcher) { d.localOnly = true } }
