// file: internal/telemetry/endpoint.go
// version: 1.2.0
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
	// DroppedQuery is true when a "?query" or "#fragment" was removed.
	DroppedQuery bool
	// DroppedPath is true when the path of an http(s) URL was removed.
	DroppedPath bool
}

// Display is the only form of the endpoint that may be logged: scheme://host:port
// for a URL, dns:///host:port for a gRPC target, or the bare host:port. The
// target is already normalised, so this is just the stored value; the field a
// caller reads is deliberately not the configured string.
func (t otlpTarget) Display() string {
	if t.URL != "" {
		return t.URL
	}
	return t.Target
}

// Dropped names what parseOTLPEndpoint removed ("userinfo", "query",
// "path"), never the removed values.
func (t otlpTarget) Dropped() []string {
	var out []string
	if t.DroppedUserinfo {
		out = append(out, "userinfo")
	}
	if t.DroppedQuery {
		out = append(out, "query/fragment")
	}
	if t.DroppedPath {
		out = append(out, "path")
	}
	return out
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
//
// Anything after host:port that could carry a secret is removed before the
// endpoint reaches an exporter, an error message or a log line: userinfo
// ("user:pass@"), "?query", "#fragment" and, for http(s) URLs, the path.
// OTLP/gRPC authenticates with headers and credentials only; the SDK ignores
// the path for gRPC (otlptracegrpc and otlpmetricgrpc both record it as
// "URLPath is ignored by gRPC exporters", and WithEndpointURL takes only
// u.Host), so dropping it changes nothing. A dns:/// target keeps its path,
// which is the target name.
//
// Order: when a '?' or '#' comes after the last '@', the query is cut first
// (a query may itself contain '@'); otherwise the '?'/'#' is inside the
// userinfo password and goes with it.
func parseOTLPEndpoint(endpoint string) (otlpTarget, error) {
	ep := strings.TrimSpace(endpoint)
	var droppedQuery bool
	if qi := strings.IndexAny(ep, "?#"); qi >= 0 && qi > strings.LastIndex(ep, "@") {
		ep, droppedQuery = ep[:qi], true
	}
	ep, droppedUser := stripUserinfo(ep)
	if qi := strings.IndexAny(ep, "?#"); qi >= 0 { // a '?'/'#' that was inside the password is gone; any left is a query
		ep, droppedQuery = ep[:qi], true
	}
	t, err := parseStrippedOTLPEndpoint(ep)
	t.DroppedUserinfo, t.DroppedQuery = droppedUser, droppedQuery
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
			clean := strings.ToLower(u.Scheme) + "://" + u.Host
			return otlpTarget{URL: clean, DroppedPath: ep != clean}, nil
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
