// file: internal/telemetry/endpoint.go
// version: 1.1.0
// guid: 6e0f4b1a-52c7-4d83-9a1e-3b7c8d2f5a40
// last-edited: 2026-10-10

package telemetry

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// otlpTarget is a validated OTLP/gRPC endpoint, independent of which OTLP
// exporter package (trace or metric) it is handed to.
type otlpTarget struct {
	// URL is the trimmed http:// or https:// URL; empty for the other forms.
	URL string
	// Target is the trimmed bare host:port or dns:/// target; empty for URLs.
	Target string
	// Bare is true for a bare host:port (the form that needs an explicit
	// insecure switch for plaintext gRPC). A dns:/// target is not Bare.
	Bare bool
	// DroppedUserinfo is true when the configured endpoint carried
	// "user:pass@" userinfo, which parseOTLPEndpoint removed. gRPC never
	// authenticates with OTLP endpoint userinfo, so dropping it changes
	// nothing functionally and keeps it out of exporter errors and logs.
	DroppedUserinfo bool
}

// stripUserinfo removes URL userinfo from an endpoint string: everything
// between the scheme separator (and any slashes after it) and the LAST '@',
// so a password containing '/' or '@' goes too. It handles "scheme://u:p@h",
// "dns:///u:p@h" and a scheme-less "u:p@h:4317". A path containing '@' is not
// a valid OTLP endpoint and would be cut too, which is the safe direction.
func stripUserinfo(ep string) (string, bool) {
	at := strings.LastIndex(ep, "@")
	if at < 0 {
		return ep, false
	}
	start := 0
	if i := strings.Index(ep, "://"); i >= 0 && i < at {
		start = i + len("://")
		for start < at && ep[start] == '/' {
			start++
		}
	}
	return ep[:start] + ep[at+1:], true
}

// parseOTLPEndpoint validates an OTLP endpoint. Accepted forms:
//
//   - "http://host:port" (plaintext) and "https://host:port" (TLS)
//   - a bare "host:port"
//   - a "dns:///host:port" gRPC target
//
// Everything else is an error. Error messages are stable: the trace path's
// callers and tests depend on them.
func parseOTLPEndpoint(endpoint string) (otlpTarget, error) {
	ep, dropped := stripUserinfo(strings.TrimSpace(endpoint))
	// From here on the userinfo is gone: the messages in
	// parseStrippedOTLPEndpoint quote the stripped endpoint only.
	t, err := parseStrippedOTLPEndpoint(ep)
	t.DroppedUserinfo = dropped
	return t, err
}

func parseStrippedOTLPEndpoint(ep string) (otlpTarget, error) {
	endpoint := ep
	if scheme, rest, ok := strings.Cut(ep, "://"); ok {
		switch strings.ToLower(scheme) {
		case "http", "https":
			u, err := url.Parse(ep)
			if err != nil {
				return otlpTarget{}, fmt.Errorf("endpoint %q is not a URL: %w", endpoint, err)
			}
			if u.Hostname() == "" || u.Port() == "" {
				return otlpTarget{}, fmt.Errorf("endpoint %q must name a host and a port", endpoint)
			}
			return otlpTarget{URL: ep}, nil
		case "dns":
			if strings.TrimLeft(rest, "/") == "" {
				return otlpTarget{}, fmt.Errorf("endpoint %q names no target", endpoint)
			}
			return otlpTarget{Target: ep}, nil
		}
		return otlpTarget{}, fmt.Errorf("endpoint %q: scheme %q is not http, https or dns", endpoint, scheme)
	}
	host, port, err := net.SplitHostPort(ep)
	if err != nil || host == "" || port == "" {
		return otlpTarget{}, fmt.Errorf("endpoint %q is neither a URL nor host:port", endpoint)
	}
	return otlpTarget{Target: ep, Bare: true}, nil
}
