### Added

#### Full configuration-option audit (`docs/audits/2026-08-20-config-option-audit.md`)

Added a full inventory and grep-verified audit of every configuration option in
the tree — 565 distinct options across `internal/config/config.go`, its
persistence/DB-settings layer, every CLI flag, ad-hoc `os.Getenv` call sites
outside `internal/config`, the frontend Settings UI, and the deploy surface
(`config.yaml`, `.env.example`, `docker-compose.yml`, Prometheus configs, the
systemd unit).

Produced by a 25-agent fan-out: 13 inventory agents extracted every option (866
raw entries, deduplicated to 565), then 12 domain-scoped agents each verified
real usage via grep, checked naming consistency across the Go struct / env var
/ YAML key / frontend layers, and evaluated whether each default value still
makes sense — every finding is grep-verified, not inferred from a field's name.

No production code changed. Notable findings surfaced for follow-up: 55 options
with zero behavior-gating call sites (including two entire Settings-UI
subsystems — Storage Quotas and Memory Limits — that are fully unenforced),
`EnableRateLimit=false` not actually disabling rate limiting, an
`ai_backend.local_base_url` default that points at a hardcoded developer LAN
IP, a `ResetToDefaults()` bug that silently disables chapter consolidation, a
3-way default mismatch on `MetadataFetchCacheTTLDays`, a fully inert
`--enable-sqlite3-i-know-the-risks` flag, and `AO_DB`/`AO_DIR` env vars that are
documented everywhere but never read by any Go code.
