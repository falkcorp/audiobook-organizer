<!-- file: changelog.d/oauth-oidc-cloudflare-access.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1f8c3a92-6b40-4d57-9e08-2a5c7b0e9d64 -->
<!-- last-edited: 2026-07-26 -->

### Added

#### OAuth2 / OIDC single sign-on (GitHub + Google) and Cloudflare Access passthrough

Users can now sign in with **GitHub** or **Google** in addition to username/password,
and the app can trust an existing **Cloudflare Access** login so users behind Zero
Trust aren't asked to log in twice.

- **Shared core** (`internal/oauth/`): CSRF `state` + PKCE (S256) on every code
  exchange, provider code-exchange (GitHub via `golang.org/x/oauth2`, Google via OIDC
  id-token verification), and Cloudflare Access JWT verification against the team JWKS
  with an `aud` check. Pure package, unit-tested.
- **Allowlist gate (verified ≠ authorized):** every path — GitHub, Google, and the
  Cloudflare Access JWT — resolves to a VERIFIED email and is then checked against
  `OAUTH_ALLOWED_EMAILS` before any user or session is created. A valid IdP login by a
  non-allowlisted account is rejected with nothing written. Account linking is by
  verified email only; a new allowlisted email auto-creates a user with a configurable
  default role (`OAUTH_DEFAULT_ROLE`, default `viewer`).
- **Identity model:** a new `OAuthIdentity` record links `(provider, subject)` → local
  `User`, so a user can attach multiple providers; matching is by the provider's stable
  account id, never email.
- **Endpoints:** public `GET /api/v1/auth/oauth/:provider/start` and `.../callback`,
  plus `GET /api/v1/auth/oauth-providers` (the enabled set, so the login page shows
  only working buttons). Sessions reuse the existing HttpOnly cookie flow.
- **Cloudflare Access:** an early, fail-open middleware verifies `Cf-Access-Jwt-Assertion`
  (not the spoofable email header) and binds the resolved user so the normal auth
  check is skipped; absent/invalid/non-allowlisted → falls through to normal auth.
- **Frontend:** "Sign in with Google/GitHub" buttons on the login page, plus a friendly
  "not authorized" message when a non-allowlisted account tries to sign in.

Everything is **off unless configured** (`OAUTH_ENABLED` / provider client IDs /
`CF_ACCESS_*`). See `docs/oauth-setup.md` for the config and the required Cloudflare
Access bypass policy on the callback path.
