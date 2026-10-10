// file: internal/telemetry/endpoint.go
// version: 1.3.0
// guid: 6e0f4b1a-52c7-4d83-9a1e-3b7c8d2f5a40
// last-edited: 2026-10-10

package telemetry

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
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

// stripUserinfo removes URL userinfo from the part of an endpoint that can
// carry it: everything between the scheme separator (and any slashes after it)
// and the LAST '@'. It is used for dns:/// targets and scheme-less host:port,
// where there is no separate authority to look inside; a password containing
// '/' or '@' goes too. http(s) URLs use authorityOf instead.
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

// Error messages below NEVER echo the endpoint, in whole or in part, and
// never wrap a url.Parse error (whose text quotes its input): a path, query or
// password can hold a secret and these errors are logged. The caller knows
// which config key it was parsing.
var (
	errNoHostPort  = errors.New("endpoint must name a host and a port")
	errBadPort     = errors.New("endpoint port must be a number from 1 to 65535")
	errNotHostPort = errors.New("endpoint is neither a URL nor host:port")
	errBarePath    = errors.New("endpoint is neither a URL nor host:port: a bare host:port takes no path")
	errNoDNSTarget = errors.New("endpoint dns:/// target names no host")
	errUnparsable  = errors.New("endpoint is not a parsable URL")
	schemeNameRE   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]{0,15}$`)
)

// splitHostPort validates "host:port": a non-empty host and a numeric port in
// 1-65535 (net.SplitHostPort checks neither the port's content nor its range).
func splitHostPort(hp string) error {
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return errNotHostPort
	}
	if host == "" || port == "" {
		return errNoHostPort
	}
	if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
		return errBadPort
	}
	return nil
}

// cutQuery drops a "?query" or "#fragment". When the first '?' or '#' lies
// before the last '@' it is part of a userinfo password and is left for the
// userinfo strip to remove.
func cutQuery(ep string) (string, bool) {
	if qi := strings.IndexAny(ep, "?#"); qi >= 0 && qi > strings.LastIndex(ep, "@") {
		return ep[:qi], true
	}
	return ep, false
}

// parseOTLPEndpoint validates an OTLP endpoint. Accepted forms:
//
//   - "http://host:port" (plaintext) and "https://host:port" (TLS)
//   - a bare "host:port"
//   - a "dns:///host:port" gRPC target
//
// The port is required and must be a number from 1 to 65535.
//
// Anything after host:port that could carry a secret is removed before the
// endpoint reaches an exporter, an error message or a log line: userinfo
// ("user:pass@"), "?query", "#fragment" and, for http(s) URLs, the path.
// OTLP/gRPC authenticates with headers and credentials only; the SDK ignores
// the path for gRPC (otlptracegrpc and otlpmetricgrpc both record it as
// "URLPath is ignored by gRPC exporters", and WithEndpointURL takes only
// u.Host), so dropping it changes nothing. A dns:/// target keeps its path,
// which is the target name. A bare host:port with a '/' is rejected.
//
// For http(s) the authority is the text after "//" up to the first '/', '?' or
// '#', and userinfo is looked for only inside it (so "?tok=a@b" and
// "/path@x" never become a host); a password therefore needs RFC 3986
// percent-encoding of '/', '?' and '#'. dns:/// and bare forms have no
// separate authority and use the last '@'.
//
// Errors are fixed strings that never quote the endpoint.
func parseOTLPEndpoint(endpoint string) (otlpTarget, error) {
	ep := strings.TrimSpace(endpoint)
	if scheme, rest, ok := strings.Cut(ep, "://"); ok {
		if !schemeNameRE.MatchString(scheme) {
			return otlpTarget{}, errNotHostPort
		}
		switch strings.ToLower(scheme) {
		case "http", "https":
			return parseHTTPEndpoint(strings.ToLower(scheme), rest)
		case "dns":
			t := otlpTarget{}
			ep, t.DroppedQuery = cutQuery(ep)
			ep, t.DroppedUserinfo = stripUserinfo(ep)
			if qi := strings.IndexAny(ep, "?#"); qi >= 0 {
				ep, t.DroppedQuery = ep[:qi], true
			}
			_, rest, _ := strings.Cut(ep, "://")
			if strings.TrimLeft(rest, "/") == "" {
				return t, errNoDNSTarget
			}
			t.Target = ep
			return t, nil
		}
		return otlpTarget{}, fmt.Errorf("endpoint scheme %q is not http, https or dns", scheme)
	}
	t := otlpTarget{Bare: true}
	ep, t.DroppedQuery = cutQuery(ep)
	ep, t.DroppedUserinfo = stripUserinfo(ep)
	if qi := strings.IndexAny(ep, "?#"); qi >= 0 {
		ep, t.DroppedQuery = ep[:qi], true
	}
	if strings.Contains(ep, "/") {
		return t, errBarePath
	}
	if err := splitHostPort(ep); err != nil {
		return t, err
	}
	t.Target = ep
	return t, nil
}

// parseHTTPEndpoint handles "http(s)://" + rest; see parseOTLPEndpoint.
func parseHTTPEndpoint(scheme, rest string) (otlpTarget, error) {
	t := otlpTarget{}
	end := strings.IndexAny(rest, "/?#")
	authority, tail := rest, ""
	if end >= 0 {
		authority, tail = rest[:end], rest[end:]
	}
	if i := strings.IndexAny(tail, "?#"); i >= 0 {
		t.DroppedQuery = true
		tail = tail[:i]
	}
	t.DroppedPath = tail != "" && tail != "/"
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		authority, t.DroppedUserinfo = authority[at+1:], true
	}
	if err := splitHostPort(authority); err != nil {
		if err == errNotHostPort {
			err = errNoHostPort // a URL authority without ":port"
		}
		return t, err
	}
	t.URL = scheme + "://" + authority
	return t, nil
}
