### Fixed

#### ABS single sign-on: `/auth/openid` no longer crashes AudioBooth; SSO is now a two-hop flow

AudioBooth makes the first `GET /auth/openid` from its own HTTP client, with
only its Cloudflare service token, and passes the 302 `Location` to
`ASWebAuthenticationSession`, which crashes on anything but an http(s) URL. We
answered that request with a redirect straight to `audiobooth://oauth` (an
`access_denied` error, because the service token names no person), so the app
crashed on every SSO attempt. Reproduced in production.

`/auth/openid` now requires a verified Cloudflare credential (the app's service
token), rate-limits and quotas per that credential (at most three parked
requests each; a fourth evicts the caller's own oldest), validates
`redirect_uri` and PKCE, parks the request under a random single-use id (five
minute expiry, in a store capped at 1024 entries that evicts its oldest entry
rather than refusing), and always 302s to
`https://<same host>/auth/openid/browser?req=<id>`. Errors on
that hop are a 400 with a plain-text body; an unregistered `redirect_uri` gets
the exact text `Invalid redirect_uri`, as real Audiobookshelf sends it. The new
`/auth/openid/browser` hop runs in the system browser, where Cloudflare Access
authenticates the person, and shows a consent page naming the app, its callback
scheme and the signed-in account. Loading that page mints nothing. Its Allow
button POSTs back with a CSRF token bound to the request id and the account;
only that POST consumes the parked request and mints a code bound to the stored
challenge and `redirect_uri`, redirecting to the app's callback with the code
and the original `state`. Deny, a non-allowlisted account, or no verified
person at all (logged and audited) redirects with `error=access_denied`
instead. The consent step exists because nothing ties the browser to the
device that started the flow, so without it anyone who started a sign-in could
send the link to someone else and have a code minted for them. When the browser's first load
carries the app's service token instead of a person (iOS 17.4+ sends the app's
custom headers on that load), the hop redirects to itself once without the
token, so that Cloudflare authenticates the person. `/auth/openid/callback` now
refuses a `redirect_uri` that differs from the one the code was minted for.
`MintAppAuthorizationCode` takes the `redirect_uri` and refuses any target
that is not registered.
