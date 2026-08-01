<!-- file: changelog.d/20260731_235800_sse_cf_access_identity.md -->
<!-- version: 1.0.0 -->
<!-- guid: b755d63b-08fa-4bac-a0fc-a6b3159672c2 -->
<!-- last-edited: 2026-07-31 -->

### Fixed

- The live-updates stream (`/api/events`) now works for browsers signed in through
  Cloudflare Access SSO. It was returning 401 on a permanent reconnect loop, so the UI
  showed a "Connection lost" banner even though the page had loaded and every other
  request succeeded.

  `/api/events` is registered as a top-level route rather than inside the `/api/v1`
  group, so it inherited none of that group's middleware — including the Cloudflare
  Access identity stage. Its auth guard assumed a browser would always present a
  session cookie, which is true for password login but false under Access SSO, where
  identity arrives only as a verified `Cf-Access-Jwt-Assertion` header. The route now
  runs the same fail-open identity middleware as `/api/v1` before its auth guard.
  Anonymous clients are still rejected (pen-test finding MED-2 is unaffected).
