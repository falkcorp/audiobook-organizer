### Changed

#### Consolidated ad-hoc `os.Getenv` reads into the existing cobra/viper config pipeline

Following up on the 2026-08-20 configuration-option audit, replaced 25 direct
`os.Getenv`/`os.Setenv` call sites scattered across `internal/ai`,
`internal/database`, `internal/dedup`, `internal/itunes/service`,
`internal/maintenance/jobs`, `internal/metadata`, `internal/plugins/acoustid`,
`internal/plugins/maintenance`, `internal/search`, `internal/server`,
`internal/telemetry`, `internal/transcribe`, and the `dedup-bench` CLI tools
with reads from `config.AppConfig`, sourced through the same
`viper.SetDefault`/`viper.BindEnv` mechanism every other setting in this repo
already uses. Every one of these variables was previously read live at each
call site instead of once at startup, so a running process could disagree with
itself about a setting depending on which code path happened to read the env
var first.

New unified/renamed settings: `ACOUSTID_API_KEY`, `DEDUP_CHROMEM_LAZY`,
`ITUNES_WRITEBACK_DRYRUN`, `FP_PARALLEL_WORKERS`, `WHISPER_CLIP_CACHE_DIR`,
`WHISPER_BATCH_SLEEP_MS`, `OPENAI_BASE_URL` (widened from a dedup-bench-only
override — it also governs `internal/ai`'s production OpenAI client),
`ABS_AUTH_PROBE`, `ABS_ITUNES_POSITION_BACKFILL_USER_ID`,
`OTEL_EXPORTER_OTLP_ENDPOINT`, `LIST_WARMER_TRICKLE_INTERVAL_MS`,
`LIBRARY_COUNTS_CACHE_MIN_INTERVAL_SECONDS`, `BLEVE_DESCRIPTION_MAX_CHARS`, and
per-provider metadata base-URL overrides `AUDIBLE_BASE_URL`,
`OPENLIBRARY_BASE_URL`, `AUDNEXUS_BASE_URL`, `GOOGLE_BOOKS_BASE_URL` (now
resolved through `config.AppConfig.MetadataSources[].BaseURL`). Consolidated
`LIST_WARMER_HEAP_DELTA_MB` and its legacy alias `LIST_WARMER_MAX_HEAP_MB` into
one viper key bound to both names, so either still works. Behavior is
unchanged; only the read path moved from live process-env lookups to the
config snapshot populated at `InitConfig()`.

Two packages that could not import `internal/config` directly (an import-cycle
constraint in `internal/database`, and a deliberate package-movability design
in `internal/itunes/service/writeback_batcher.go`) got a package-level setter
and a config-injected struct field respectively, matching existing idioms
already used elsewhere in each package rather than introducing a new pattern.

`.env.example` dropped `HOST`, `PORT`, `AO_DB`, and `AO_DIR` — all four were
already dead per the audit (documented but never read by any Go code) — in
favor of documenting the real keys (`--host`/`--port` flags, `ROOT_DIR`,
`DATABASE_PATH`) and every newly consolidated variable above.
