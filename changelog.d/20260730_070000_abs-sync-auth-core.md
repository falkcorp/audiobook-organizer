<!-- file: changelog.d/20260730_070000_abs-sync-auth-core.md -->
<!-- version: 1.0.0 -->
<!-- guid: 16c7566d-2857-4d6a-8cc9-fcea9aabaa32 -->
<!-- last-edited: 2026-07-30 -->

### Added

- **Audiobookshelf-compatible auth core (Phase 1, TASK-11).** New top-level ABS
  router group -- `GET /ping`, `GET /status`, `POST /login`, `POST /auth/refresh`,
  `POST /logout` (`?allDevices=1`), `GET /api/me`, `GET /api/me/sessions`,
  `DELETE /api/me/sessions/:id` -- behind a single fail-closed identity middleware
  that implements the spec's unified resolution order: a *verified*
  `Cf-Access-Jwt-Assertion` first (Mode C/A, with allowlist-gated JIT user
  provisioning), then our own bearer JWT (Mode B), else 401. Feature-flagged off by
  default via `ABS_API_ENABLED`; `ABS_AUTH_MODES` (default `cf,jwt`) selects which
  resolvers are active. New `abs_sess:` PebbleDB keyspace holds one session per
  device. Access tokens are real HS256 JWTs with a **30-day** default TTL (not 1h --
  clients that implement no refresh would otherwise be logged out hourly); refresh
  tokens are opaque `abr_`-prefixed strings whose SHA-256 hashes alone are persisted,
  with single-flight rotation plus a 10-minute grace window so a concurrent or
  replayed refresh returns the already-minted pair instead of orphaning the session.
  argon2id for new passwords, with bcrypt verify plus transparent rehash-on-login for
  existing users. Every auth attempt, success and failure, is audit-logged with its
  source IP.

### Security

- **The ABS surface does not inherit the `/api/v1` fail-open Cloudflare-Access
  behaviour.** On `/api/v1` an unverifiable `Cf-Access-Jwt-Assertion` falls through to
  session/API-key auth; on the ABS group there is no second gate, so a malformed,
  forged, expired or wrong-AUD assertion is a hard 401 and a verified-but-not-
  allowlisted identity is a hard 403 that provisions nothing. Explicit regression
  tests cover both, including a request that pairs a forged assertion with a valid
  bearer token -- it is still rejected, so the Cloudflare path cannot be probed for
  free. `ABS_JWT_SECRET` is required whenever the ABS API is enabled and the server
  refuses to boot without it; it is never auto-generated and never read from the DB
  config blob.

### Fixed

- **`GET /api/me` is excluded from the `/api/*` -> `/api/v1/*` compatibility
  redirect.** The ABS protocol is unversioned, so without the exclusion the endpoint
  would 301 into the app API and answer a different shape -- it would look implemented
  and behave broken.
