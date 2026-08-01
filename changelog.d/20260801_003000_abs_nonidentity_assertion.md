<!-- file: changelog.d/20260801_003000_abs_nonidentity_assertion.md -->
<!-- version: 1.0.0 -->
<!-- guid: a3055725-e818-471e-b4e7-d5525fa4515a -->
<!-- last-edited: 2026-08-01 -->

### Fixed

- Audiobookshelf clients reaching the server through a Cloudflare Access **service
  token** are no longer rejected. Cloudflare mints a token with no email claim for a
  service token — it identifies a machine, not a person — and the server treated that
  as a forged credential and returned a terminal 401, even when the request also
  carried a perfectly valid bearer token. That made the whole service-token topology
  unusable, which is the one a native player app needs.

  A valid-but-anonymous assertion now falls through to the bearer token, exactly as if
  no assertion had been sent. Every other verification failure — forged signature,
  wrong issuer, wrong audience, expired — remains a hard 401, and a service token with
  no bearer alongside it is still rejected: it proves a device may reach the server,
  never who the user is.

### Added

- An opt-in diagnostic that logs which credentials each Audiobookshelf client actually
  puts on the wire — set `ABS_AUTH_PROBE=1`. It records presence and length only, never
  a credential value, and is off by default because these routes are polled every
  15–20 seconds.

  It exists to settle by observation a question no amount of reading a client's source
  can answer: after a player app signs in through its embedded web view, does its
  ordinary API client actually carry the resulting Cloudflare cookie, or does it use a
  separate cookie store and silently drop it?
