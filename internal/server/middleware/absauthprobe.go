// file: internal/server/middleware/absauthprobe.go
// version: 1.0.0
// guid: 5c9f21a7-3e64-48db-b0d2-9a8e7c4f6103
// last-edited: 2026-08-01

package middleware

import (
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// ABSAuthProbeEnvVar gates the probe. Unset/empty = the middleware is never
// registered and costs nothing.
const ABSAuthProbeEnvVar = "ABS_AUTH_PROBE"

// ABSAuthProbe logs WHICH credentials an ABS client actually put on the wire, so
// client behaviour can be settled by observation instead of inference.
//
// It exists to answer one question that no amount of reading the client's source
// can: after a native player completes a Cloudflare Access login in its embedded
// webview, does its ordinary API client carry the resulting CF_Authorization
// cookie? iOS apps frequently use a URLSession whose cookie jar is separate from
// the webview's, in which case the cookie is earned and then never sent, and every
// API call is bounced at the edge. Reading the app's source does not answer this
// either — it depends on which HTTP stack the app built and how iOS partitioned
// the jar at runtime.
//
// # It never logs a credential value
//
// Every field below is a bool, an int length, or a fixed enum. A length is safe and
// is worth having: it distinguishes a real JWT from a stray empty header, and shows
// at a glance whether an assertion is the identity shape or the much shorter
// service-token shape. Nothing here can be replayed.
//
// # Why env-gated rather than always-on at DEBUG
//
// The ABS surface is polled hard — clients hit /ping every 20s and sync every 15s
// (§1.9.4) — so an always-on per-request log line is real journal volume for a
// signal that is only wanted during a deliberate diagnostic window. Turning it on
// is an explicit act, and leaving it on is visible in the unit file.
func ABSAuthProbe() gin.HandlerFunc {
	return func(c *gin.Context) {
		assertion := strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader))
		authz := strings.TrimSpace(c.GetHeader("Authorization"))

		// The edge cookie. Its PRESENCE is the whole question: it means the app's
		// API client shares a cookie jar with the webview that logged in, and the
		// request would satisfy Cloudflare Access on its own.
		cfCookie := ""
		if ck, err := c.Request.Cookie("CF_Authorization"); err == nil && ck != nil {
			cfCookie = ck.Value
		}

		bearerKind := "none"
		switch {
		case authz == "":
		case len(authz) > 7 && strings.EqualFold(authz[:7], "bearer "):
			if strings.HasPrefix(strings.TrimSpace(authz[7:]), "abk_") {
				bearerKind = "app-api-key" // abk_ — our own API key, NOT an ABS token
			} else {
				bearerKind = "abs-access-token"
			}
		default:
			bearerKind = "non-bearer-scheme"
		}

		slog.Info("abs: auth probe",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			// Identifies WHICH client is calling — ShelfPlayer, Plappa, a browser, curl.
			"user_agent", c.Request.UserAgent(),
			// Mode C / browser-SSO signal: the edge verified a person and injected this.
			"cf_assertion", assertion != "",
			"cf_assertion_len", len(assertion),
			// THE question this probe was built for.
			"cf_cookie", cfCookie != "",
			"cf_cookie_len", len(cfCookie),
			// Mode B signal: the two-header service-token form. Plappa cannot use the
			// single-header Authorization variant (it collides with ABS auth), so this
			// pair is what a working Mode B request looks like.
			"cf_client_id", c.GetHeader("CF-Access-Client-Id") != "",
			"cf_client_secret", c.GetHeader("CF-Access-Client-Secret") != "",
			// Our own credential.
			"bearer", bearerKind,
			"query_token", c.Query("token") != "",
		)

		c.Next()
	}
}
