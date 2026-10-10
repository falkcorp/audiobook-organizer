// file: internal/telemetry/endpoint.go
// version: 2.0.0
// guid: 6e0f4b1a-52c7-4d83-9a1e-3b7c8d2f5a40
// last-edited: 2026-10-10

package telemetry

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Config keys the endpoint parser names in its (fixed) error text.
const (
	keyTraceEndpoint   = "otel_exporter_otlp_endpoint"
	keyMetricsEndpoint = "otel_metrics_otlp_endpoint"
)

// otlpTarget is a validated OTLP/gRPC endpoint, independent of which OTLP
// exporter package (trace or metric) it is handed to. It is built ONLY from
// validated parts (scheme, host, port); no byte of the configured string
// survives except through those.
type otlpTarget struct {
	// URL is the canonical "http://host:port" or "https://host:port"; empty
	// for the other forms.
	URL string
	// Target is the canonical bare "host:port" or "dns:///host:port"; empty
	// for URLs.
	Target string
	// Bare is true for a bare host:port.
	Bare bool
}

// Display is the canonical endpoint: exactly the string handed to the
// exporter (WithEndpointURL for a URL, WithEndpoint otherwise), so what is
// logged is what is dialled.
func (t otlpTarget) Display() string {
	if t.URL != "" {
		return t.URL
	}
	return t.Target
}

// hostRE is a DNS-style host: letters, digits, '.', '-', not starting or
// ending with '.' or '-'.
var hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)

// parseOTLPEndpoint validates endpoint strictly and rebuilds it. key is the
// config key it came from and is the only context in any error.
//
// Accepted (after trimming surrounding whitespace; the scheme is
// case-insensitive):
//
//   - host:port
//   - http://host:port and https://host:port, with at most one trailing '/'
//   - dns:///host:port
//
// host is a DNS name ([A-Za-z0-9.-], no leading or trailing '.' or '-', at
// most 253 bytes) or a bracketed IPv6 address that netip.ParseAddr accepts
// (no zone). port is a number from 1 to 65535.
//
// Everything else is REFUSED, never repaired: userinfo or any '@', '?', '#',
// '%', a path other than one trailing '/', whitespace or control characters
// inside the string, unknown schemes, a missing or bad port. The error is one
// fixed string per key and contains no part of the input, not even the scheme.
//
// Rebuilding from validated parts (rather than stripping what is not wanted)
// is what makes it impossible for a secret to ride along: OTLP/gRPC needs
// nothing but scheme, host and port, and the SDK ignores path, query and
// userinfo for gRPC anyway.
func parseOTLPEndpoint(key, endpoint string) (otlpTarget, error) {
	refuse := fmt.Errorf("%s is not a valid OTLP/gRPC endpoint (expected host:port, http(s)://host:port or dns:///host:port)", key)

	ep := strings.TrimSpace(endpoint)
	scheme, rest := "", ep
	if i := strings.Index(ep, "://"); i >= 0 {
		scheme, rest = strings.ToLower(ep[:i]), ep[i+3:]
	}
	switch scheme {
	case "http", "https":
		rest = strings.TrimSuffix(rest, "/")
	case "dns":
		if !strings.HasPrefix(rest, "/") {
			return otlpTarget{}, refuse
		}
		rest = rest[1:]
	case "":
	default:
		return otlpTarget{}, refuse
	}
	hp, ok := canonicalHostPort(rest)
	if !ok {
		return otlpTarget{}, refuse
	}
	switch scheme {
	case "http", "https":
		return otlpTarget{URL: scheme + "://" + hp}, nil
	case "dns":
		return otlpTarget{Target: "dns:///" + hp}, nil
	}
	return otlpTarget{Target: hp, Bare: true}, nil
}

// canonicalHostPort validates "host:port" or "[ipv6]:port" and returns it
// rebuilt from the parsed parts.
func canonicalHostPort(s string) (string, bool) {
	var host, port string
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 || !strings.HasPrefix(s[end+1:], ":") {
			return "", false
		}
		addr, err := netip.ParseAddr(s[1:end])
		if err != nil || !addr.Is6() || addr.Zone() != "" {
			return "", false
		}
		host, port = "["+addr.String()+"]", s[end+2:]
	} else {
		i := strings.LastIndex(s, ":")
		if i < 0 {
			return "", false
		}
		host, port = s[:i], s[i+1:]
		if len(host) > 253 || !hostRE.MatchString(host) {
			return "", false
		}
	}
	if port == "" || len(port) > 5 {
		return "", false
	}
	for _, c := range port {
		if c < '0' || c > '9' {
			return "", false
		}
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", false
	}
	return host + ":" + strconv.FormatUint(n, 10), true
}
