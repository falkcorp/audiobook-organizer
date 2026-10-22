### Security

#### Trailing-slash and fixed-path redirects are off

Gin answered a request that missed a route by a trailing slash or by case (`/auth/openid/callback/`, `/PROBE`) with a 301 or 307 whose `Location` it built from the request path prefixed with the caller-supplied `X-Forwarded-Prefix` header, so a caller could choose part of the redirect target. The service reads that header nowhere else and sits behind a tunnel that does not set it, and the affected path is one that bypasses Cloudflare Access. `RedirectTrailingSlash` and `RedirectFixedPath` are now false on the router (`newBaseRouter`), so such a request gets the ordinary not-found answer. No registered route, web client call or ABS-compatible path relied on the redirect; the SPA is served from the not-found handler, which is unchanged.
