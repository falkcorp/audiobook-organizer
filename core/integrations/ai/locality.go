// file: core/integrations/ai/locality.go
// version: 1.0.0
// guid: d18f3a52-6e07-4b94-a2c5-8b9e1f4d7c60
// last-edited: 2026-11-12

package ai

import (
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// cgnat is 100.64.0.0/10 (RFC 6598), where Tailscale-style overlays live.
func cgnat() netip.Prefix { return netip.MustParsePrefix("100.64.0.0/10") }

// localHostSuffixes are names that never resolve through public DNS.
func localHostSuffixes() []string {
	return []string{".localhost", ".local", ".lan", ".internal", ".home.arpa"}
}

// hostIsLocal classifies one URL host, fail-closed toward "cloud": a loopback,
// RFC 1918/ULA private, link-local or CGNAT IP literal, "localhost", a name
// under a never-public suffix, or a single-label name is local; every other
// host, including every dotted public name and public IP, is not.
func hostIsLocal(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		ip := net.IP(addr.AsSlice())
		return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || cgnat().Contains(addr.Unmap()) || ip.IsLoopback()
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, s := range localHostSuffixes() {
		if strings.HasSuffix(host, s) {
			return true
		}
	}
	return false
}

// settingsURLs yields the http(s) URLs among an instance's settings values, at
// any depth. The adapter's schema says which field is a URL; the router has no
// schema walk, so it reads every string value that parses as an absolute http
// or https URL, which can only ever add a host to the check, never hide one.
func settingsURLs(v any, out *[]*url.URL) {
	switch x := v.(type) {
	case string:
		if u, err := url.Parse(strings.TrimSpace(x)); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			*out = append(*out, u)
		}
	case map[string]any:
		for _, e := range x {
			settingsURLs(e, out)
		}
	case []any:
		for _, e := range x {
			settingsURLs(e, out)
		}
	}
}

// heuristicLocal reads the instance's configured URLs: local only when there
// is at least one and every one is local. No URL at all is not local.
func heuristicLocal(settings map[string]any) bool {
	var urls []*url.URL
	settingsURLs(settings, &urls)
	if len(urls) == 0 {
		return false
	}
	for _, u := range urls {
		if !hostIsLocal(u.Hostname()) {
			return false
		}
	}
	return true
}

// effectiveLocality decides whether an endpoint keeps data on the operator's
// own network: the operator's explicit setting, else what the adapter can
// prove, else the URL heuristic. The result is LocalityLocal or LocalityCloud,
// never Auto or Unknown, so a privacy floor needs one comparison and an
// endpoint that cannot be shown local is cloud.
func effectiveLocality(set EndpointSettings, measured Locality, settings map[string]any) Locality {
	switch set.Locality {
	case LocalityLocal, LocalityCloud:
		return set.Locality
	}
	switch measured {
	case LocalityLocal, LocalityCloud:
		return measured
	}
	if heuristicLocal(settings) {
		return LocalityLocal
	}
	return LocalityCloud
}
