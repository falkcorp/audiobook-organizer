// file: internal/server/middleware/cfbounce.go
// version: 1.1.0
// guid: 3b7e1d52-9c04-4a68-b1f3-5d2a8e60c9b7
// last-edited: 2026-10-20

package middleware

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/oauth"
)

// Service-token browser recovery.
//
// # The failure this exists for
//
// The Cloudflare organization runs with strict_service_token_auth=false, so a
// request admitted on a service token is handed a CF_Authorization app-session
// cookie. AudioBooth's SSO opens an ASWebAuthenticationSession, which SHARES
// Safari's cookie jar and carries the app's service-token headers on its first
// load. The result: Safari's CF_Authorization cookie for the host is overwritten
// by a service-token session, which names no person. From then on every Safari
// load is admitted by Access on that cookie, the web UI sees an assertion with no
// email (so /api/v1/auth/me is 401 and the user is shown the login page), and the
// SSO browser hop cannot ever see a person.
//
// # The recovery
//
// A top-level browser NAVIGATION that arrives with a service-token assertion is
// answered with a tiny static page that (1) expires the CF_Authorization cookie
// when that cookie is itself the service-token session, and (2) meta-refreshes to
// the same URL plus a one-shot marker. The refresh is a fresh navigation WITHOUT
// the custom service-token headers, so Cloudflare sends it through its identity
// login (whichever IdPs Access is configured with) and back with a person cookie.
//
// # What must never happen
//
// Only a navigation (GET, Sec-Fetch-Mode: navigate, Sec-Fetch-Dest: document) with
// no API key is ever bounced. AudioBooth's URLSession, AppleCoreMedia streaming,
// the Watch app and the SPA's own fetch/XHR carry service tokens legitimately and
// must be left strictly alone. A service token is never mapped to a user.

const (
	// CFAuthorizationCookie is the name Cloudflare Access gives its app-session
	// cookie. It is host-only, which is why the expiry below sets no Domain.
	CFAuthorizationCookie = "CF_Authorization"

	// CFResetParam is the one-shot loop-guard marker added to the bounced URL.
	CFResetParam = "cfreset"

	// cfRecoveryCSP matches the SSO browser hop (openid.go oidcBrowserHeaders):
	// no script, no network, inline style only. A meta refresh is not governed by
	// CSP, so the bounce works under it.
	cfRecoveryCSP = "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'"

	// cfExpireCookie deletes CF_Authorization. Deliberately no Domain attribute.
	cfExpireCookie = CFAuthorizationCookie + "=; Path=/; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure; HttpOnly"

	cfRecoveryPageStyle = "body{font:16px system-ui,sans-serif;margin:3rem 1.5rem;text-align:center}"
)

var cfBounceLog = logger.New("cfaccess")

// CFNavKind classifies a request for service-token browser recovery.
type CFNavKind int

const (
	// CFNavSkip: not a candidate -- not a browser navigation, carries an API key,
	// has no assertion header, or the assertion did not verify. Leave it alone.
	CFNavSkip CFNavKind = iota
	// CFNavServiceToken: a browser navigation whose assertion verifies but names no
	// person.
	CFNavServiceToken
	// CFNavPerson: a browser navigation whose assertion verifies and names a person.
	CFNavPerson
)

// CFNavInspection is the result of CFServiceTokenBouncer.Inspect.
type CFNavInspection struct {
	Kind CFNavKind
	// CommonName is the service token's common_name (a credential id, never an
	// identity). Only set for CFNavServiceToken. Log CFLogName(CommonName), not it.
	CommonName string
	// CookiePoisoned: the CF_Authorization COOKIE value itself verifies as a
	// non-identity (service-token) session. Only set for CFNavServiceToken.
	CookiePoisoned bool
	// SecFetchMode and SecFetchDest are the request's Sec-Fetch-Mode/-Dest values
	// ("" when absent), sanitized for logging. Set whenever Kind is not CFNavSkip,
	// and on a SkippedNoSecFetch skip.
	SecFetchMode, SecFetchDest string
	// SkippedNoSecFetch: a navigation-LOOKING request (GET, Accept text/html) that
	// carries a service-token assertion was passed through only because it has no
	// Sec-Fetch headers. Only ever set by Inspect, never InspectBrowserHop.
	SkippedNoSecFetch bool
}

// CFServiceTokenBouncer holds the verifier the recovery decisions run on. A nil
// bouncer never matches anything.
type CFServiceTokenBouncer struct {
	verifier CFAssertionVerifier
}

// NewCFServiceTokenBouncer returns a bouncer on v, or nil when v is nil.
func NewCFServiceTokenBouncer(v CFAssertionVerifier) *CFServiceTokenBouncer {
	if v == nil {
		return nil
	}
	return &CFServiceTokenBouncer{verifier: v}
}

// IsBrowserNavigation reports a top-level document navigation: GET with
// Sec-Fetch-Mode: navigate and Sec-Fetch-Dest: document. A request without
// Sec-Fetch headers (curl, URLSession, AVPlayer, old clients) is NOT one, and
// neither is anything identifying itself as AppleCoreMedia.
func IsBrowserNavigation(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet {
		return false
	}
	if strings.Contains(r.UserAgent(), "AppleCoreMedia") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode")), "navigate") &&
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")), "document")
}

// Inspect classifies the request for the WEB UI, which shares paths with fetch
// and XHR and therefore requires browser-navigation Sec-Fetch headers. It never
// mutates the request or response.
func (b *CFServiceTokenBouncer) Inspect(c *gin.Context) CFNavInspection {
	return b.inspect(c, true)
}

// InspectBrowserHop classifies a request to a route only ever opened by a browser
// (SSO hop 2, the ASWebAuthenticationSession sheet). Sec-Fetch headers are NOT
// required there: demanding them adds no safety on a browser-only route and would
// deny the first load if the sheet did not send them. The method (GET), API-key
// and AppleCoreMedia exclusions still apply, and a service-token assertion is
// still required.
func (b *CFServiceTokenBouncer) InspectBrowserHop(c *gin.Context) CFNavInspection {
	return b.inspect(c, false)
}

func (b *CFServiceTokenBouncer) inspect(c *gin.Context, requireNav bool) CFNavInspection {
	if b == nil || b.verifier == nil || c == nil || c.Request == nil {
		return CFNavInspection{}
	}
	r := c.Request
	mode := logger.SanitizeLogValue(strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode")))
	dest := logger.SanitizeLogValue(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")))
	// Same test as CloudflareAccessAuth: a key authenticates the request, an
	// assertion riding along with it is not considered.
	if r.Method != http.MethodGet || strings.Contains(r.UserAgent(), "AppleCoreMedia") || APIKeyFromRequest(r) != "" {
		return CFNavInspection{}
	}
	nav := IsBrowserNavigation(r)
	if requireNav && !nav {
		// Not bounced. If it merely LOOKS like a page load (Accept: text/html, no
		// Sec-Fetch headers at all) say so, so a device test can tell whether a
		// client that needed recovery was passed over for want of the headers.
		if mode == "" && dest == "" && strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html") {
			if raw := strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader)); raw != "" {
				if _, err := b.verifier.Verify(r.Context(), raw); errors.Is(err, oauth.ErrNonIdentityAssertion) {
					return CFNavInspection{SkippedNoSecFetch: true, CommonName: strings.TrimSpace(oauth.CFAssertionCommonName(err))}
				}
			}
		}
		return CFNavInspection{}
	}
	raw := strings.TrimSpace(c.GetHeader(oauth.CFAccessHeader))
	if raw == "" {
		return CFNavInspection{}
	}
	ctx := r.Context()
	_, err := b.verifier.Verify(ctx, raw)
	switch {
	case err == nil:
		return CFNavInspection{Kind: CFNavPerson, SecFetchMode: mode, SecFetchDest: dest}
	case !errors.Is(err, oauth.ErrNonIdentityAssertion):
		return CFNavInspection{} // forged / expired / wrong aud: not ours to recover
	}
	out := CFNavInspection{
		Kind: CFNavServiceToken, CommonName: strings.TrimSpace(oauth.CFAssertionCommonName(err)),
		SecFetchMode: mode, SecFetchDest: dest,
	}
	if ck, cerr := c.Cookie(CFAuthorizationCookie); cerr == nil && strings.TrimSpace(ck) != "" {
		_, verr := b.verifier.Verify(ctx, strings.TrimSpace(ck))
		out.CookiePoisoned = errors.Is(verr, oauth.ErrNonIdentityAssertion)
	}
	return out
}

// CFLogHeader renders a possibly-empty header value for a log line.
func CFLogHeader(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// CFLogName is the form of a service token's common_name that is safe to log:
// a short prefix, never the whole credential id.
func CFLogName(commonName string) string {
	const n = 8
	commonName = logger.SanitizeLogValue(commonName)
	if len(commonName) > n {
		return commonName[:n] + "..."
	}
	return commonName
}

// CFRecoveryURL builds the same-origin relative URL for path/query of u with the
// loop-guard marker added (marker true) or removed (false). ok is false when u's
// path cannot be safely emitted as a relative URL (a leading "//" would be read as
// a scheme-relative URL to another host).
func CFRecoveryURL(u *url.URL, marker bool) (string, bool) {
	if u == nil {
		return "", false
	}
	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "", false
	}
	q := u.Query()
	if marker {
		q.Set(CFResetParam, "1")
	} else {
		q.Del(CFResetParam)
	}
	if enc := q.Encode(); enc != "" {
		p += "?" + enc
	}
	return p, true
}

func setCFRecoveryHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Referrer-Policy", "same-origin")
	c.Header("X-Frame-Options", "DENY")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Security-Policy", cfRecoveryCSP)
}

// WriteCFBouncePage answers a service-token browser navigation with the static
// bounce page: 200, no-store, a zero-delay meta refresh to target and a visible
// Continue link. No script. When expireCookie is set it also deletes the
// CF_Authorization cookie. target is escaped into the page; callers pass only
// same-origin URLs they built themselves.
func WriteCFBouncePage(c *gin.Context, target string, expireCookie bool) {
	setCFRecoveryHeaders(c)
	if expireCookie {
		c.Writer.Header().Add("Set-Cookie", cfExpireCookie)
	}
	esc := html.EscapeString(target)
	body := `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<meta http-equiv="refresh" content="0;url=` + esc + `"><title>Signing in</title><style>` + cfRecoveryPageStyle + `</style></head>
<body><p>Signing you in&hellip;</p><p><a href="` + esc + `">Continue</a></p></body></html>
`
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
	c.Abort()
}

// cfLogoutURL is the Access logout link for the request host, or the host-relative
// path when the Host header is not a plausible hostname.
func cfLogoutURL(host string) string {
	const path = "/cdn-cgi/access/logout"
	host = strings.TrimSpace(host)
	if host == "" || len(host) > 255 {
		return path
	}
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == ':':
		default:
			return path
		}
	}
	return "https://" + host + path
}

// WriteCFLoopErrorPage is the terminal answer when a navigation that already went
// through the one-shot bounce STILL arrives as a service token: no redirect, just
// instructions. 403 and no-store.
func WriteCFLoopErrorPage(c *gin.Context) {
	setCFRecoveryHeaders(c)
	link := cfLogoutURL(c.Request.Host)
	esc := html.EscapeString(link)
	body := `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>Sign-in needs a reset</title><style>` + cfRecoveryPageStyle + `</style></head>
<body><p>Cloudflare signed this browser in with a device token instead of your account.</p>` +
		`<p>Open <a href="` + esc + `">` + esc + `</a>, then reload.</p></body></html>
`
	c.Data(http.StatusForbidden, "text/html; charset=utf-8", []byte(body))
	c.Abort()
}

// CFServiceTokenRecovery is the web-UI middleware: place it where page
// navigations are served (the SPA fallback), next to CloudflareAccessAuth's
// position in the chain. eligible decides which URL paths are SPA page routes;
// anything it rejects -- the API, the ABS surface, static assets, the SSO hop --
// passes through untouched. A nil bouncer is a pass-through.
func CFServiceTokenRecovery(b *CFServiceTokenBouncer, eligible func(path string) bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if b == nil || c.Request == nil || c.Request.URL == nil || eligible == nil || !eligible(c.Request.URL.Path) {
			c.Next()
			return
		}
		ins := b.Inspect(c)
		if ins.Kind == CFNavSkip {
			if ins.SkippedNoSecFetch {
				cfBounceLog.Info("cfaccess: page-like request with a service-token assertion left alone: no Sec-Fetch headers (sec_fetch_mode=- sec_fetch_dest=-) service_token=%s path=%s",
					CFLogName(ins.CommonName), logger.SanitizeLogValue(c.Request.URL.Path))
			}
			c.Next()
			return
		}
		marked := c.Request.URL.Query().Has(CFResetParam)
		switch ins.Kind {
		case CFNavPerson:
			if !marked {
				c.Next()
				return
			}
			// Recovered: drop the one-shot marker so it never lingers in the URL
			// bar or a bookmark.
			clean, ok := CFRecoveryURL(c.Request.URL, false)
			if !ok {
				c.Next()
				return
			}
			c.Redirect(http.StatusFound, clean)
			c.Abort()
		case CFNavServiceToken:
			if marked {
				// The bounce already happened and Cloudflare STILL admitted this
				// navigation on a service token. Another bounce would loop.
				cfBounceLog.Warn("cfaccess: browser still presents a service-token session after recovery bounce: service_token=%s cookie_poisoned=%t path=%s",
					CFLogName(ins.CommonName), ins.CookiePoisoned, logger.SanitizeLogValue(c.Request.URL.Path))
				WriteCFLoopErrorPage(c)
				return
			}
			target, ok := CFRecoveryURL(c.Request.URL, true)
			if !ok {
				c.Next()
				return
			}
			cfBounceLog.Info("cfaccess: bouncing browser navigation that carries a service-token session: service_token=%s cookie_poisoned=%t sec_fetch_mode=%s sec_fetch_dest=%s path=%s",
				CFLogName(ins.CommonName), ins.CookiePoisoned, CFLogHeader(ins.SecFetchMode), CFLogHeader(ins.SecFetchDest),
				logger.SanitizeLogValue(c.Request.URL.Path))
			WriteCFBouncePage(c, target, ins.CookiePoisoned)
		}
	}
}
