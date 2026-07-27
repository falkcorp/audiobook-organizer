<!-- file: changelog.d/fix-oauth-config-blob-bypass.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c1e0d94-6a37-4b52-9f08-2a5c7b0e9d63 -->
<!-- last-edited: 2026-07-26 -->

### Fixed

#### OAuth/Cloudflare-Access config read from env at point of use (config-blob was zeroing it)

`LoadConfigFromDatabase` overwrites `config.AppConfig` from a stored config-blob right
after `InitConfig`, preserving only a hardcoded list of env-immutable fields; the
OAuth/CF-Access fields were not on that list, so a systemd-set `Environment=` value was
zeroed and the Cloudflare Access passthrough never initialized. `buildOAuthWiring` now
reads `CF_ACCESS_*` / `OAUTH_*` from `os.Getenv` at the point of use (config.AppConfig
fallback), which is authoritative regardless of the blob — the same reason
`WHISPER_REMOTE_URL` reads env directly.
