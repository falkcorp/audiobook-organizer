// file: internal/books/cutover/m6/trust.go
// version: 1.0.1
// guid: b642edd7-1075-46b6-9cf4-5e4ca7ea0ad8
// last-edited: 2026-11-19

package m6

import (
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/secrets"
)

// Trust carried over (05-metadata M1.10). core's origin and signature
// functions (core/integrations/fields.go originOf, signature) are unexported,
// and books cannot import them, so the two are copied here. Copying is safe
// because the real importer validates what is written with them
// (checkImport's wellFormedSignature refuses any signature core would not have
// rendered), and TestIntegrationsMigration_TrustCarried pins the copy
// against that check (every signature it asserts went through the real importer)
// instead of against a second copy of the rule, and TestTrust_OriginAndSignature
// pins the renderings.

// defaultDestinations is the signature of an instance with no destination set
// (core/integrations/fields.go defaultDestinations).
const defaultDestinations = "adapter-default"

// originOf is raw's origin, scheme://host:port with the scheme's default port
// filled in and scheme and host lower-cased, and ok false for a value that is
// not an absolute URL with a host (core renders that as a malformed origin that
// equals no real one; the step simply grants no trust for it).
func originOf(raw string) (origin string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Hostname() == "" || strings.IndexFunc(u.Hostname(), unicode.IsSpace) >= 0 {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	if port == "" {
		if strings.Contains(host, ":") {
			return scheme + "://[" + host + "]", true
		}
		return scheme + "://" + host, true
	}
	return scheme + "://" + net.JoinHostPort(host, port), true
}

// defaultPort is the port of a URL scheme that names none
// (core/integrations/fields.go defaultPort).
func defaultPort(scheme string) string {
	switch scheme {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	case "smtp":
		return "25"
	case "smtps":
		return "465"
	case "ftp":
		return "21"
	}
	return ""
}

// signature renders a destination origin map as core does: defaultDestinations
// when there is none, else key=origin pairs in key order, space-separated, also
// for a single destination.
func signature(origins map[string]string) string {
	if len(origins) == 0 {
		return defaultDestinations
	}
	keys := make([]string, 0, len(origins))
	for k := range origins {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + origins[k]
	}
	return strings.Join(parts, " ")
}

// secretName is the name core's importer requires a settings field's secret
// to have: integrations.<kind>.<instance>.<field> (05-config 2.3).
func secretName(ref integrations.Ref, field string) secrets.Name {
	return secrets.Name("integrations." + string(ref.Kind) + "." + string(ref.Instance) + "." + field)
}
