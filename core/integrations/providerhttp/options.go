// file: core/integrations/providerhttp/options.go
// version: 1.2.0
// guid: 4b3c95ad-7828-4971-b0b8-f48428f4b0e4
// last-edited: 2026-11-11

package providerhttp

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/metrics"
)

// Settings is the live core.providerhttp section the Registry reads on each
// call (05-metadata M2.7; name coined by F7-02, after integrations.Settings).
// The kernel adapts its config section to it; zero fields take M2.7's
// defaults.
type Settings struct {
	// Fallback is the limits every field of an instance's effective limits
	// falls back to (core.providerhttp.fallback.*): 2 rps, burst 2, 3 retries,
	// 30s per attempt.
	Fallback integrations.Limits
	// RetryAfterMax clamps a server's Retry-After (core.providerhttp.retry_after_max, 5m).
	RetryAfterMax time.Duration
	// HoldMax clamps every hold, a persisted one on load included
	// (core.providerhttp.hold.max, 24h).
	HoldMax time.Duration
}

// The defaults of M2.7.
const (
	defaultRPS           = 2.0
	defaultBurst         = 2
	defaultMaxRetries    = 3
	defaultTimeout       = 30 * time.Second
	defaultRetryAfterMax = 5 * time.Minute
	defaultHoldMax       = 24 * time.Hour
	defaultMaxBody       = 32 << 20
	defaultCounter       = "requests"
)

// withDefaults fills M2.7's defaults and repairs nonsense: a zero or negative
// rate never disables pacing (HEAD's normalize rule).
func (s Settings) withDefaults() Settings {
	f := s.Fallback
	if !(f.RPS > 0) {
		f.RPS = defaultRPS
	}
	if f.Burst < 1 {
		f.Burst = defaultBurst
	}
	if f.MaxRetries <= 0 {
		f.MaxRetries = defaultMaxRetries
	}
	if f.Timeout <= 0 {
		f.Timeout = defaultTimeout
	}
	s.Fallback = f
	if s.RetryAfterMax <= 0 {
		s.RetryAfterMax = defaultRetryAfterMax
	}
	if s.HoldMax <= 0 {
		s.HoldMax = defaultHoldMax
	}
	return s
}

// Options configures New (05-metadata M2.3). Every field is optional.
type Options struct {
	// Store persists holds and daily quota counters. nil keeps them in memory
	// (tests): nothing then survives the Registry.
	Store GateStore
	// Base is the template of the two guarded transports the Registry builds
	// (connection limits, TLS settings, timeouts). It is CLONED, and its Proxy,
	// DialContext and DialTLSContext are replaced: the egress guard lives in
	// the dial, and a proxy would resolve and connect on the guard's behalf.
	// Its TLS config's InsecureSkipVerify is forced false on the clone: a base
	// that skips verification must not make every provider call unverified.
	// Narrower than the spec's http.RoundTripper on purpose: a RoundTripper
	// that dials for itself would leave the SSRF boundary to the caller.
	// Default: a clone of http.DefaultTransport, never the global itself.
	Base *http.Transport
	// UserAgent is sent on requests that carry none ("<app>/<version> (+mediacore/<v>)").
	UserAgent string
	// Metrics is the core "providerhttp" scope created with the labels kind
	// and instance (and reason, class where the series carry them); nil
	// records nothing.
	Metrics *metrics.Scope
	// Log receives the Registry's warnings (hold persistence, one per store, and
	// the recovery of a boot read) and the dial guard's Debug lines, which name
	// the host and the address of a refused dial (the refusal an error carries
	// names neither). It never receives a URL, a header or a body. nil discards.
	Log *slog.Logger
	// Now is the clock (default time.Now).
	Now func() time.Time
	// Settings reads the live core.providerhttp section; nil = the defaults.
	Settings func() Settings
	// Disabled reports core.integrations.disabled: while true every attempt and
	// every Acquire is refused (ReasonDisabled) before anything is dialled or
	// written (R6-7). nil = never disabled.
	Disabled func() bool
	// LANAllow reports core.integrations.egress.lan_allow: the hosts,
	// host:port pairs and literal-IP CIDRs every instance without Local may
	// reach on a private network (SEC-15). Read on every request.
	LANAllow func() []string

	// Resolver resolves a host at dial time; nil uses the process resolver.
	// A test hook: the dial guard checks what it returns.
	Resolver func(ctx context.Context, host string) ([]net.IP, error)
	// DialAddr dials a literal, already-checked "ip:port"; nil uses a
	// net.Dialer. A test hook.
	DialAddr func(ctx context.Context, network, addr string) (net.Conn, error)
	// Sleep waits d or until ctx ends (retry back-off, bucket waits). nil uses
	// a timer. A test hook: the Registry's clock for waiting.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter returns the random addition to a back-off delay of d, in [0, d/2];
	// nil draws it from math/rand/v2. A test hook.
	Jitter func(d time.Duration) time.Duration
}

// Policy is one instance's outbound policy (05-metadata M2.3).
type Policy struct {
	// Limits are the instance's effective limits; a zero field falls back to
	// Settings.Fallback. Every Client of one Scope shares one set: the last
	// Client call (or SetLimits) wins, in place.
	Limits integrations.Limits
	Egress integrations.EgressPolicy
	// Scope groups instances that share an upstream budget. Default
	// ref.String(). Two instances using the same API key against one provider
	// set the same Scope (e.g. "google-books:" + hex(sha256(key))[:12]). A
	// Scope never appears in a log line, an error or a metric label.
	Scope string
	// Redact names extra query, header and JSON-field names to redact on top of
	// the built-in rules (redact.go: names ending in token, key, secret or
	// password, session and sig prefixes, auth, authorization, cookie ...).
	Redact []string
	// QueryCredential, when non-nil, is how an API that takes its credential in
	// the URL query gets it WITHOUT the caller ever holding a URL that carries
	// it: the transport adds the parameter to its own clone of the request URL,
	// per attempt, so the *url.Error net/http builds, the request a caller logs
	// and Response.Request all hold the credential-free URL. It is added only to
	// a request whose origin is one of QueryCredential.Origins, and never to a
	// hop of a redirect chain that has left its first endpoint.
	QueryCredential *integrations.QueryCredential
	// Trust lists the origins this instance may reach on a private network
	// (integrations.Trust). It is the ONLY way to arm the private-capable
	// transport: the Registry derives the per-request test from it (the
	// instance's bound origins, or an entry of lan_allow) so a caller cannot
	// supply a predicate of its own, and the test reads lan_allow on every
	// request, so a trust change applies without a rebuild. It is never
	// consulted for EgressPublicOnly, for a redirect to another endpoint, nor
	// for link-local addresses. The zero Trust still admits lan_allow.
	Trust integrations.Trust
	// MaxBody caps a response body; 0 takes the default (32 MiB). A read past
	// the cap fails with ErrResponseTooLarge.
	MaxBody int64
}
