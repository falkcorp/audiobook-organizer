<!-- file: changelog.d/20260802_020000_oauth_login_deeplink.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0b74e1c9-3d52-4a86-97f0-5e2c81a4bd37 -->
<!-- last-edited: 2026-08-02 -->

### Fixed

- **The web OAuth callback silently discarded a native client's custom-scheme
  `return`, dropping it on the web SPA root instead.** `sanitizeReturn` correctly
  requires a same-site path, so `audiobooth://oauth` became `""` and the destination
  fell back to `/` — with no error and nothing logged, which surfaced as "it logged me
  into the website" rather than as a failure.

  `/auth/oauth/:provider/start` now accepts a registered native callback and, on
  success, hands the client a single-use PKCE-bound authorization code on its own URL
  scheme, redeemable at the existing `/auth/openid/callback`. `sanitizeReturn` is
  **unchanged** — it is the open-redirect guard, and the deep-link path goes through
  the same exact-match allowlist `/auth/openid` already enforces rather than loosening
  it.

  Three things this deliberately gets right:

  - **The native path engages only when `redirect_uri` AND `code_challenge` are both
    present.** `/auth/oauth/:provider/start` is the unauthenticated endpoint the SPA's
    login buttons hit, so if a bare `redirect_uri` could trigger a 400, anyone could
    break login for every user by getting one query parameter appended to a shared
    link. A request carrying only one of the two is treated as an ordinary web login
    and the stray parameter is ignored.
  - **The two PKCE exchanges stay separate.** Server↔IdP uses our own verifier;
    app↔server uses the client's challenge, which we only ever store. Conflating them
    would either break the upstream token exchange or issue codes with no client-side
    proof of possession.
  - **No browser session is minted on the native path.** The caller is an
    `ASWebAuthenticationSession` whose cookie jar is discarded when it closes, so a
    session created there would be an unusable row written on every app login.

  Latent when fixed — production logs showed zero `/auth/oauth/*` traffic over 7 days,
  and Audiobookshelf clients use `/auth/openid` instead.
