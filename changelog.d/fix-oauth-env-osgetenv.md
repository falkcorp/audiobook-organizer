<!-- file: changelog.d/fix-oauth-env-osgetenv.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e9a1c73-8b04-4d62-9f17-3a6c2b0e9d51 -->
<!-- last-edited: 2026-07-26 -->

### Fixed

#### OAuth / Cloudflare Access config now read from env vars (os.Getenv, not viper)

The OAuth2/OIDC + Cloudflare Access settings (`CF_ACCESS_TEAM_DOMAIN`, `CF_ACCESS_AUD`,
`OAUTH_ALLOWED_EMAILS`, `OAUTH_*`) were wired through `viper.BindEnv`, which does not
reliably surface a systemd-set env var into config at runtime in this app (config is
loaded from config.yaml + the DB settings store). A drop-in `Environment=` value was
silently dropped, so the Cloudflare Access passthrough middleware never initialized and
users still hit the app's own login. These are now read via `os.Getenv` (env-first,
viper fallback), matching the established `WHISPER_REMOTE_URL` pattern. Also: the CF
Access middleware now falls back to the `CF_Authorization` cookie when the header is
absent, and logs (at Warn) when a present JWT fails verification or a verified identity
is not admitted, so misconfig is no longer silent.
