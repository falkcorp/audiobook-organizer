// file: internal/server/auth_oidc_discovery.go
// version: 1.0.0
// guid: c4a71e38-95b2-4d06-8f13-6b0e29c7d541
// last-edited: 2026-08-01

package server

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// OIDCDiscoveryEnvVar gates the discovery probe. Unset = nothing is registered
// and /auth/openid keeps its honest 404.
const OIDCDiscoveryEnvVar = "OIDC_DISCOVERY"

// oidcDiscoveryDummyCode is returned to the client so it proceeds to its NEXT
// call, which is the one we actually need to see. It authenticates nothing —
// the exchange handler only logs and never mints a session.
const oidcDiscoveryDummyCode = "discovery-probe-not-a-real-code"

// registerOIDCDiscoveryProbe wires a TEMPORARY, opt-in instrument that records
// exactly what an Audiobookshelf client asks of an OIDC-capable server.
//
// # Why this exists
//
// AudioBooth initiates single sign-on with a PKCE authorization request:
//
//	HEAD/GET /auth/openid?client_id=AudioBooth&response_type=code&scope=openid
//	         &redirect_uri=audiobooth://oauth&callback=audiobooth://oauth
//	         &code_challenge=...&code_challenge_method=S256
//
// That much is observed fact from the production journal. What is NOT known is
// the second half of the contract: which endpoint the client posts the code to,
// what parameters it sends, and what response shape it expects back. Implementing
// against a guess there fails silently — the app would simply never finish
// logging in, with nothing in the log to say why.
//
// So this handler accepts the authorization request, records it, and redirects
// to the client's own redirect_uri with a dummy code. The client then makes its
// exchange call, which the catch-all below records in full. One tap on Login
// yields the whole contract.
//
// # It authenticates nothing
//
// The dummy code is never accepted as a credential: the exchange handler logs
// and returns a fixed JSON body without consulting the store or minting a
// session. There is no path through this file that can produce a token. That is
// deliberate — a half-built auth endpoint is worse than none, and this ships
// before the real implementation exists.
//
// # Secret hygiene
//
// Request bodies and headers are logged as KEY NAMES plus value LENGTHS, not
// values. The contract is carried by which keys are present, so the shape is
// fully recoverable without writing a code_verifier or an assertion into the
// journal. Query parameters ARE logged in full because they are already in the
// request line that gin logs anyway, and the PKCE challenge is a public value
// by construction.
func (s *Server) registerOIDCDiscoveryProbe() {
	slog.Warn("oidc: DISCOVERY PROBE ENABLED — /auth/openid answers a dummy authorization code and " +
		"logs every request under /auth/openid. It authenticates NOTHING and mints no session. " +
		"Unset " + OIDCDiscoveryEnvVar + " as soon as the client contract has been captured")

	s.router.Any("/auth/openid", func(c *gin.Context) {
		logOIDCRequest(c, "authorize")

		// Prefer redirect_uri; AudioBooth also sends `callback` with the same
		// value, and which one is authoritative is part of what we are learning.
		target := strings.TrimSpace(c.Query("redirect_uri"))
		if target == "" {
			target = strings.TrimSpace(c.Query("callback"))
		}
		if target == "" {
			slog.Warn("oidc: authorize request carried no redirect_uri or callback — cannot continue the flow")
			c.JSON(http.StatusBadRequest, gin.H{"error": "redirect_uri required"})
			return
		}

		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		redirect := fmt.Sprintf("%s%scode=%s", target, sep, url.QueryEscape(oidcDiscoveryDummyCode))
		if st := c.Query("state"); st != "" {
			redirect += "&state=" + url.QueryEscape(st)
		}

		slog.Info("oidc: authorize → redirecting client to its callback", "redirect_to", redirect)
		// 302 to a custom scheme: the webview hands it to the OS, which reopens
		// the app. That hand-off is itself worth confirming works.
		c.Redirect(http.StatusFound, redirect)
	})

	// Whatever the client calls next lands here — the exchange. This is the
	// unknown half of the contract.
	s.router.Any("/auth/openid/*rest", func(c *gin.Context) {
		logOIDCRequest(c, "exchange")
		// A deliberately recognisable non-answer. If the client reports a
		// specific parse error against this, that error names the fields it
		// wanted — which is more signal, not less.
		c.JSON(http.StatusOK, gin.H{
			"discovery_probe": true,
			"note":            "contract capture only; no session was created",
		})
	})
}

// logOIDCRequest records one request in enough detail to reconstruct the client
// contract, without writing any credential value to the journal.
func logOIDCRequest(c *gin.Context, phase string) {
	query := map[string]string{}
	for k, v := range c.Request.URL.Query() {
		if len(v) > 0 {
			query[k] = v[0]
		}
	}

	// Header NAMES always; values only for headers that cannot carry a secret.
	var headerNames []string
	for name := range c.Request.Header {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)

	// Body: keys and lengths. For a form post the keys ARE the contract; for
	// JSON we record the raw length and let the key list come from the form
	// parse, which gin performs for both encodings on ParseForm.
	bodyLen := 0
	var bodyKeys []string
	if c.Request.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
		if err == nil {
			bodyLen = len(raw)
			// Try form decoding to recover key names without logging values.
			if vals, perr := url.ParseQuery(string(raw)); perr == nil {
				for k, v := range vals {
					l := 0
					if len(v) > 0 {
						l = len(v[0])
					}
					bodyKeys = append(bodyKeys, fmt.Sprintf("%s(len=%d)", k, l))
				}
				sort.Strings(bodyKeys)
			}
		}
	}

	slog.Info("oidc: discovery",
		"phase", phase,
		"method", c.Request.Method,
		"path", c.Request.URL.Path,
		"user_agent", c.Request.UserAgent(),
		"content_type", c.GetHeader("Content-Type"),
		"query", fmt.Sprintf("%v", query),
		"header_names", strings.Join(headerNames, ","),
		"body_len", bodyLen,
		"body_keys", strings.Join(bodyKeys, ","),
		// The whole point of the eventual implementation: is Cloudflare Access
		// already telling us who this is by the time the request lands?
		"cf_assertion_present", c.GetHeader(oauth.CFAccessHeader) != "",
		"cf_assertion_len", len(c.GetHeader(oauth.CFAccessHeader)),
	)
}
