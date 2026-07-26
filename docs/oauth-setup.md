<!-- file: docs/oauth-setup.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b7e0c94-8a21-4d56-9f07-1a6c5b2e9d43 -->
<!-- last-edited: 2026-07-26 -->

# OAuth2 / OIDC SSO + Cloudflare Access — setup

All of this is **off unless configured**. Nothing here exposes credentials; fill the
values from your own IdP + Cloudflare dashboards. Infra-specific values (real team
domain, tunnel IDs) live in the private ops repo, not here.

## Configuration (env vars or config.yaml keys)

| Env var | config.yaml key | Purpose |
|---|---|---|
| `OAUTH_ENABLED` | `oauth_enabled` | Master switch for GitHub/Google login. |
| `OAUTH_GITHUB_CLIENT_ID` / `_SECRET` | `oauth_github_client_id` / `_secret` | GitHub OAuth app. |
| `OAUTH_GOOGLE_CLIENT_ID` / `_SECRET` | `oauth_google_client_id` / `_secret` | Google OAuth 2.0 Web client. |
| `OAUTH_REDIRECT_BASE_URL` | `oauth_redirect_base_url` | Public origin, e.g. `https://books.example.com`. Falls back to `--external-url`. |
| `OAUTH_ALLOWED_EMAILS` | `oauth_allowed_emails` | **Required.** Comma-separated allowlist. Empty = every OAuth login is rejected. |
| `OAUTH_DEFAULT_ROLE` | `oauth_default_role` | Role for a newly auto-created OAuth user (default `viewer`; `editor`/`admin` available). |
| `CF_ACCESS_TEAM_DOMAIN` | `cf_access_team_domain` | e.g. `myteam.cloudflareaccess.com`. |
| `CF_ACCESS_AUD` | `cf_access_aud` | The Access application's AUD tag. |

**Verified ≠ authorized:** a valid Google/GitHub login only proves the person owns that
email. Only emails on `OAUTH_ALLOWED_EMAILS` are admitted; everyone else is rejected
with nothing written to the database. Set the allowlist before enabling.

## Provider redirect / callback URIs

Register these callback URLs in each provider:

- GitHub OAuth app → `https://<your-host>/api/v1/auth/oauth/github/callback`
- Google OAuth 2.0 Web client → `https://<your-host>/api/v1/auth/oauth/google/callback`

Google needs only the minimal scopes (`openid`, `email`, `profile`) — no sensitive
scopes, so no Google verification review is required. Use a **dedicated** OAuth client
for this app (do not reuse the client another integration, e.g. Cloudflare Access,
already depends on).

## Cloudflare Access (optional passthrough)

When `CF_ACCESS_*` are set, the app verifies the signed `Cf-Access-Jwt-Assertion`
header (against the team JWKS + AUD) and logs the user in automatically — no second
login. The verification is the trust anchor; the `Cf-Access-Authenticated-User-Email`
header is never trusted on its own. The same allowlist applies (defense in depth).

**Required dashboard change — bypass policy on the OAuth callback path.** If you *also*
enable app-level Google/GitHub login while the whole app sits behind one Access
application, Access will shadow the app's own OAuth callback and cause a redirect loop.
In Cloudflare Zero Trust → Access → Applications, add a second, more-specific
self-hosted application scoped to `.../api/v1/auth/oauth/*/callback` with a **Bypass**
policy (Access evaluates the most-specific match first). This lets the app complete its
own OAuth handshake while the rest of the app stays gated.

**Origin bypass protection** (rejecting direct-to-origin traffic that skips Access) is
handled at the network layer — a Cloudflare Tunnel (no public origin listener) or
Authenticated Origin Pulls — not in app code.
