<!-- file: docs/research/2026-06-15-config-architecture-evaluation.md -->
<!-- version: 1.0.0 -->
<!-- guid: c7f3a821-94e0-4b2d-b8e1-3d5f8ac22019 -->
<!-- last-edited: 2026-06-15 -->

# Config Architecture Evaluation — AppConfig / ToolConfig / Nested Structs

**Date:** 2026-06-15
**Status:** Research complete — pending design decision
**Method:** 3-agent parallel read-only investigation (go-specialist × 2, expert × 1)
**Next step:** CFG-1 decision doc at `docs/specs/config-architecture-decision.md`

---

## Executive Summary

`AppConfig` (type `Config` in `internal/config/config.go`) has grown to **155+ flat fields** with no structural plan and is now showing classic God-Object symptoms:

- Contributors are **escaping the struct** (dedup scoring lives in a parallel Viper-only system)
- **40% of fields have no frontend representation** (Settings UI type is hand-curated and lagging)
- **`internal/config` is not a leaf package** — it imports `internal/database` and `internal/serviceregistry`, coupling config to the domain layer
- **`mapstructure` tags on 4 of 155+ fields** — `viper.Unmarshal` is silently broken for almost everything
- **18+ production env vars bypass `AppConfig` entirely** via `os.Getenv`
- **1,434 direct field access sites**, most without the read lock, despite the struct being mutable at runtime

The right action is a **phased restructuring** — not a big-bang rewrite. Three changes deliver most of the value with manageable blast radius.

---

## Current State

### The `Config` struct (`internal/config/config.go`)

| Group | Field count | Example fields |
|---|---|---|
| Core paths | 6 | `RootDir`, `DatabasePath`, `DatabaseType` |
| Library organization | 8 | `OrganizationStrategy`, `FolderNamingPattern`, `AutoOrganize` |
| Storage quotas | 4 | `EnableDiskQuota`, `DiskQuotaPercent` |
| Metadata sources | 10 | `AutoFetchMetadata`, `MetadataSources`, `Language` |
| AI / LLM | 8 | `OpenAIAPIKey`, `DedupReviewModel`, `MetadataReviewModel` |
| Performance | 9 | `ConcurrentScans`, `MinBookSizeBytes`, `LogRetentionDays` |
| Embedding / dedup | 13 | `EmbeddingEnabled`, `EmbeddingModel`, `DedupBookHighThreshold` |
| Metadata scoring | 7 | `MetadataEmbeddingScoringEnabled`, `MetadataLLMRerankTopK` |
| API / auth | 9 | `EnableAuth`, `APIRateLimitPerMinute`, `BasicAuthUsername` |
| Memory / cache | 6 | `CacheSize`, `MemoryLimitMB`, `MetadataFetchCacheTTLDays` |
| Lifecycle / retention | 6 | `PurgeSoftDeletedAfterDays`, `ActivityLogRetentionChangeDays` |
| Logging | 3 | `LogLevel`, `LogFormat`, `EnableJsonLogging` |
| iTunes sync | 10 | `ITunesSyncEnabled`, `ITunesLibraryWritePath`, `ITunesPathMappings` |
| Deluge integration | 6 | `DelugeWebURL`, `DelugeWebPassword`, `DelugeMoveEnabled` |
| Auto-update | 5 | `AutoUpdateEnabled`, `AutoUpdateChannel` |
| Maintenance window | 17 | `MaintenanceWindowEnabled`, 15 per-task bools |
| Scheduled tasks | ~18 | `ScheduledAIDedupEnabled`, `ScheduledReconcileInterval`, etc. |
| Path formatting / apply | 5 | `PathFormat`, `AutoRenameOnApply`, `VerifyAfterWrite` |
| Download client | nested | `DownloadClient DownloadClientConfig` (already nested) |
| Plugin system | 3 | `Plugins map[string]PluginConfig`, `SupportedExtensions` |
| **TOTAL (approx)** | **~155** | |

### Load Path (startup sequence)

```
Cobra OnInitialize
  └─ initConfig()                    [cmd/root.go]
       ├─ viper.AutomaticEnv()       [no prefix, no key replacer]
       ├─ viper.ReadInConfig()       [~/.audiobook-organizer.yaml]
       ├─ viper.BindPFlag(...)       [4 CLI flags only]
       └─ config.InitConfig()        [viper.SetDefault + Mutate onto AppConfig]

serveCmd.RunE
  ├─ initializeStore()               [open PebbleDB]
  ├─ initEncryption()                [AES key for secrets]
  ├─ config.LoadConfigFromDatabase() [blob → applySetting() legacy → YAML fallback]
  ├─ config.SyncConfigFromEnv()      [re-reads viper for 4 specific fields post-DB]
  └─ config.AppConfig.Validate()     [structural checks]
```

**Precedence (lowest → highest):**
`viper.SetDefault` → YAML file → env vars → CLI flags → DB blob → `SyncConfigFromEnv` re-read

### Other Config Structs (not part of AppConfig)

| Struct | Location | Load mechanism | Relationship to AppConfig |
|---|---|---|---|
| `itunesservice.Config` | `internal/itunes/service/config.go` | Snapshot copy at construction | Mirrors iTunes fields — the only real isolation |
| `unified.ScoreConfig` | `internal/dedup/unified/config.go` | `viper.Get("dedup.signals.*")` directly | Parallel Viper system — **completely outside AppConfig** |
| `telemetry.Config` | `internal/telemetry/config.go` | `os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")` | No connection to AppConfig |
| `server.ServerConfig` | `internal/server/server.go` | CLI flags → hardcoded defaults | No connection to AppConfig; not persisted |
| `mtls.Dir` | `internal/mtls/config.go` | Filesystem (cert dir) | Not a config struct; cert manager |

---

## Key Findings

### Finding 1: `internal/config` is not a leaf package

`internal/config` imports:
- `github.com/falkcorp/audiobook-organizer/internal/database` (for `SettingsStore`, `DecryptValue`, `MaskSecret`)
- `github.com/falkcorp/audiobook-organizer/internal/serviceregistry` (via `register.go` `init()`)

**Impact:** Any package importing `config` transitively pulls in `database` and `serviceregistry`. Tests that call `InitConfig()` trigger the registry `init()` side effect. This makes the config package unusable as a pure data layer.

**Fix:** Move `persistence.go`, `update_service.go`, and `register.go` into `internal/config/persistence` (or leave them in server wiring). The leaf `internal/config` becomes pure: struct definition + Viper defaults + `Mutate`/`Snapshot` — zero internal imports.

---

### Finding 2: `mapstructure` tags on 4 of 155+ fields — viper.Unmarshal is silently broken

Only `DedupReviewModel`, `MetadataReviewModel`, `FilenameParseModel`, and `CoverArtModel` have `mapstructure` tags. All other fields rely on the json round-trip path or explicit viper key reads. This means `viper.Unmarshal(&AppConfig)` (or `viper.UnmarshalKey`) would silently leave ~151 fields at their zero values. If any future code attempts a Viper unmarshal instead of the manual `Mutate` pattern, the result is a completely misconfigured server with no error.

**Fix:** Either add `mapstructure` tags to all fields (mechanical, ~155 additions) or explicitly document that Viper unmarshal is banned and only the `Mutate` pattern is used.

---

### Finding 3: Embedding/dedup defaults bypass viper entirely

Fields in the embedding and dedup groups (`EmbeddingEnabled`, `EmbeddingModel`, `EmbeddingDimensions`, `DedupBookHighThreshold`, all `MetadataEmbedding*`, all `MetadataLLM*`) are set via **hardcoded Go assignments inside the `Mutate` block**, not via `viper.SetDefault`. This means:

- `EMBEDDING_ENABLED=false` (env var) **does nothing** — viper sets it but `InitConfig` ignores the viper key
- These fields cannot be changed via YAML config file
- `applySetting()` does not handle them, so legacy installs cannot restore them from individual DB rows
- Only the `config_blob` path restores them correctly

This is the same "escape the struct" pattern as dedup signals — it happened organically when contributors found the viper path inconvenient.

**Fix:** Convert all hardcoded Mutate assignments to `viper.SetDefault` + `viper.GetX(key)`. This is mechanical.

---

### Finding 4: The dedup signal scoring system is a parallel config universe

`internal/dedup/unified/config.go` defines `ScoreConfig` with ~15 fields, loaded directly from `viper.Get("dedup.signals.*")` keys that are set via `viper.SetDefault` in `InitConfig`. These are:
- **Not in `AppConfig`** — no struct fields
- **Not in the Settings UI** — no TypeScript type
- **Not in `config_blob`** — not persisted to DB
- **Not in `applySetting()`** — not loadable from legacy individual keys
- **Not changeable at runtime via `PUT /api/v1/config`**

This is fully functional but creates a two-tier config system where critical dedup calibration values are only configurable via YAML or environment variables — not through the same API path as everything else.

**Fix:** Absorb `ScoreConfig` into a `DedupConfig` sub-struct inside `AppConfig`. The `dedup.signals.*` Viper defaults become `viper.SetDefault("dedup.signal_band_count", ...)` etc. with matching fields.

---

### Finding 5: 18+ production `os.Getenv` bypasses

The following env vars are read with `os.Getenv` directly, bypassing viper, `AppConfig`, and the settings API entirely:

| Env var | Location | Notes |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `internal/telemetry/config.go` | Intentional — telemetry is always env-only |
| `OPENAI_BASE_URL` | `internal/ai/` (3 files) | Overlaps with `AppConfig.EmbeddingBaseURL`; fallback |
| `LIBRARY_COUNTS_CACHE_MIN_INTERVAL_SECONDS` | `internal/database/pebble_store.go` | Tuning knob; no field in AppConfig |
| `DEDUP_CHROMEM_LAZY` | `internal/dedup/lifecycle.go` | Lazy vs eager vector index init |
| `ITUNES_WRITEBACK_DRYRUN` | `internal/itunes/service/writeback_batcher.go` | Dry-run mode |
| `AUDIBLE_BASE_URL` / `AUDNEXUS_BASE_URL` / `GOOGLE_BOOKS_BASE_URL` / `OPENLIBRARY_BASE_URL` | `internal/metadata/*.go` | Test override URLs |
| `FP_PARALLEL_WORKERS` | `internal/plugins/acoustid/fingerprint_rescan.go` | |
| `ACOUSTID_API_KEY` | `internal/plugins/acoustid/online_lookup.go` | Fallback; primary is AppConfig field |
| `BLEVE_DESCRIPTION_MAX_CHARS` | `internal/search/index_builder.go` | |
| `LIST_WARMER_HEAP_DELTA_MB` / `LIST_WARMER_MAX_HEAP_MB` / `LIST_WARMER_TRICKLE_INTERVAL_MS` | `internal/server/library_list_warmer.go` | Memory tuning |
| `OPENAI_API_KEY` | `internal/server/bench.go` | Uses raw env, not DB-loaded key |
| `PPROF_ADDR` | `pprof_debug.go` | Debug only |

The `OPENAI_BASE_URL` / `ACOUSTID_API_KEY` cases are the most dangerous: there are now two code paths for the same semantic setting, and the env var path bypasses the DB-backed update API.

**Fix categorized:**
- **Should move to AppConfig:** `LIBRARY_COUNTS_CACHE_MIN_INTERVAL_SECONDS`, `FP_PARALLEL_WORKERS`, `BLEVE_DESCRIPTION_MAX_CHARS`, `DEDUP_CHROMEM_LAZY`, List Warmer knobs
- **Should deduplicate (env → AppConfig fallback):** `OPENAI_BASE_URL` (should route through `EmbeddingBaseURL`), `ACOUSTID_API_KEY`
- **Fine as env-only (intentional/test):** `OTEL_EXPORTER_OTLP_ENDPOINT`, metadata base URLs, `ITUNES_WRITEBACK_DRYRUN`, `PPROF_ADDR`

---

### Finding 6: Race hazard — 1,434 direct reads without read lock

`AppConfig` is a live-mutating global protected by a `sync.RWMutex` inside `Mutate`/`Snapshot`. However, 1,434 direct `config.AppConfig.Field` accesses in production code operate **without the read lock**. The documented stance is "direct reads of fields set once at startup are tolerated." But many fields are mutable at runtime via `PUT /api/v1/config`:

- `RootDir` (215 reads) — mutable at runtime
- `OpenAIAPIKey` (67 reads) — mutable at runtime
- `ConcurrentScans` (36 reads, including in scanner goroutines) — mutable at runtime
- `DedupEmbeddingsEnabled` (read in dedup hot path) — mutable at runtime

The Go race detector would flag these. They haven't caused production issues yet, likely because config updates are infrequent and writes are atomic on most architectures for the field types in use. But this is undefined behavior in the Go memory model.

**Fix:** Add `Snapshot()` usage at goroutine dispatch boundaries. A goroutine should call `cfg := config.Snapshot()` once at entry and use `cfg.Field` throughout — not `config.AppConfig.Field`. The `Snapshot()` API already exists; adoption is the gap.

---

### Finding 7: `EmbeddingBaseURL` env var doesn't survive startup

Steps:
1. User sets `EMBEDDING_BASE_URL=http://localhost:11434/v1`
2. Phase 1: `viper.AutomaticEnv()` maps it to viper key `embedding_base_url`
3. `InitConfig()` reads it (unclear — depends on whether there's a Mutate assignment for it; there is: `AppConfig.EmbeddingBaseURL = viper.GetString("embedding_base_url")`)
4. Phase 2: `LoadConfigFromDatabase()` loads config blob → unmarshals full struct including `EmbeddingBaseURL` from blob → **overwrites the env var value with whatever was last saved to DB**
5. `SyncConfigFromEnv()` re-reads only: `root_dir`, `openai_api_key`, `google_books_api_key`, `enable_ai_parsing` — **does not re-read `embedding_base_url`**

Result: `EMBEDDING_BASE_URL` is silently ignored after a config blob exists. To change `EmbeddingBaseURL`, users must use `PUT /api/v1/config` or modify the blob directly.

**Fix:** Add `embedding_base_url` to `SyncConfigFromEnv()`.

---

### Finding 8: `applySetting()` legacy path missing ~70 fields

The `applySetting(key, value string)` switch statement handles 106 recognized keys for the pre-blob (legacy individual DB rows) load path. Approximately 70 fields added since the blob path was introduced have no case in `applySetting`. This means:

- Legacy installs (pre-blob DB) cannot restore these fields from DB rows
- If a migration tool writes individual DB rows for these fields, they are silently ignored
- New installs start with blob immediately, so this is only a risk for upgrades from old versions

**Fix:** Long-term, `applySetting` can be retired once all production installs have migrated to the blob path. Short-term, add a warning log for unrecognized keys rather than the current silent discard.

---

### Finding 9: Frontend Config interface 40% behind Go struct

`web/src/services/api.ts` defines a `Config` TypeScript interface that is a **manually-curated subset** of the Go `Config` struct. Approximately 60 Go fields have no TypeScript representation, including:

- All 5 `Embedding*` fields (no Settings UI for embedding config)
- All 9 dedup threshold fields (`DedupBookHighThreshold`, etc.)
- All `MetadataLLM*` and `MetadataEmbedding*` scoring fields
- All 18 `Scheduled*` task fields
- Most `MaintenanceWindow*` per-task booleans
- `ITunesPathMappings`, `ITunesWindowsRootPath`, `ITunesMediaRoot`
- `Plugins map[string]PluginConfig`
- All 4 LLM model fields (`DedupReviewModel`, etc.)

Adding a new configurable field requires updates at 5 separate sites: Go struct → `InitConfig` defaults → `applySetting` → `ResetToDefaults` → TypeScript type. Missing any step produces silent zero-value behavior.

**Fix:** Generate the TypeScript interface from the Go struct using `go generate` or a build-time script rather than maintaining it manually. This is a significant but high-leverage change.

---

### Finding 10: No `ToolConfig` struct exists

External binary tool configuration is handled in three incompatible ways:

1. **Hardcoded `exec.LookPath`** — `fpcalc`, `ffmpeg`, `ffprobe` are looked up in PATH at each invocation across 5+ packages with zero override mechanism
2. **Flat `AppConfig` field** — `EmbeddingBaseURL` is the only tool-adjacent field in Config
3. **`os.Getenv` bypass** — `OPENAI_BASE_URL` used as a fallback in 3 AI files

The managed-tool lifecycle spec (TOOL-1..6, captured in TODO.md) requires a `ToolConfig` sub-struct as its foundation. No `ToolConfig` type or variable exists anywhere in the codebase today.

---

## Growth Trajectory

**Current:** ~155 flat fields, `applySetting` switch ~300 lines

**Projected at current rate (10–15 new fields per major feature wave, 4–6 waves in 6 months):**
- +40–90 fields in 6 months → **195–245 total fields**
- `applySetting` → 400–500+ lines
- TypeScript gap grows from ~60 missing fields to 90+ unless generation is adopted

The dedup signal escape-hatch (putting signals in Viper-only) shows contributors are already working around the flat struct. Without a restructure, expect more satellite config systems to emerge.

---

## Proposed Restructuring — Three Changes

### Change A: Extract leaf `internal/config` (no internal imports)

**Move** `persistence.go`, `update_service.go`, `register.go` into `internal/config/persistence` or into `internal/server/config_service/`. The leaf `internal/config` becomes:
- `config.go` — `Config` struct, `AppConfig`, `Mutate`, `Snapshot`, `InitConfig`, `Validate`, `ResetToDefaults`
- Zero internal imports (only `viper`, `sync`, `os`, `regexp`, `strings`, `fmt`, `runtime`)

**Impact:** Eliminates `config → database` coupling. Tests that need `AppConfig` don't pull in the database layer. This is a pure package boundary change — no API surface changes.

**Effort:** Medium. ~5 files move; all import paths that import `internal/config/persistence` or `register.go` need updating. The `config.LoadConfigFromDatabase` and `config.SaveConfigToDatabase` callsites in `cmd/root.go` would import from the new location.

---

### Change B: Introduce nested sub-structs for cohesive groups

**Priority order** (by impact/feasibility):

1. **`ToolConfig`** — highest priority; required by TOOL-1..6
   ```go
   type ToolConfig struct {
       FfmpegPath  string `json:"ffmpeg_path"  mapstructure:"ffmpeg_path"`
       FfprobePath string `json:"ffprobe_path" mapstructure:"ffprobe_path"`
       FpcalcPath  string `json:"fpcalc_path"  mapstructure:"fpcalc_path"`
       OllamaURL   string `json:"ollama_url"   mapstructure:"ollama_url"`
   }
   // In Config:
   Tools ToolConfig `json:"tools" mapstructure:"tools"`
   ```
   Binary path lookup pattern: `path := c.Tools.FfmpegPath; if path == "" { path, _ = exec.LookPath("ffmpeg") }`

2. **`EmbeddingConfig`** — absorbs 5 flat fields + fixes the `EMBEDDING_BASE_URL` SyncConfigFromEnv gap
   ```go
   type EmbeddingConfig struct {
       Enabled       bool   `json:"enabled"`
       Model         string `json:"model"`
       Dimensions    int    `json:"dimensions"`
       BaseURL       string `json:"base_url"`
       VectorBackend string `json:"vector_backend"`
   }
   // In Config:
   Embedding EmbeddingConfig `json:"embedding"`
   ```

3. **`DedupConfig`** — absorbs 9 flat fields + absorbs `unified.ScoreConfig` (ends the parallel Viper system)
   ```go
   type DedupConfig struct {
       BookHighThreshold           float64           `json:"book_high_threshold"`
       // ... 8 more threshold/flag fields
       Signals                     DedupSignalConfig `json:"signals"`
   }
   // In Config:
   Dedup DedupConfig `json:"dedup"`
   ```

4. **`MaintenanceConfig`** — absorbs `MaintenanceWindowEnabled` + 15 per-task booleans + window hours
5. **`ITunesConfig`** — absorbs 10 iTunes fields; aligns with the existing `itunesservice.Config` slice

**Migration concern:** Nesting changes the JSON key paths in `config_blob`. `"embedding_enabled": true` becomes `"embedding": {"enabled": true}`. A one-time migration in `LoadConfigFromDatabase` must read both old flat keys and new nested keys during the transition window.

**Effort:** High per sub-struct. Each requires: struct definition, `InitConfig` update, `applySetting` update (or retirement), `ResetToDefaults` update, `config_blob` migration, TypeScript interface update, and callsite updates for `config.AppConfig.EmbeddingEnabled` → `config.AppConfig.Embedding.Enabled`.

**Recommendation:** Do one sub-struct at a time as part of feature work. `ToolConfig` first (prerequisite for TOOL-1..6). `EmbeddingConfig` second (small, low-blast-radius). `DedupConfig` third (ends the parallel Viper system). Others later.

---

### Change C: `Snapshot()` adoption at goroutine boundaries

This is a discipline/convention change, not a structural change.

**Rule:** Any goroutine that reads multiple `AppConfig` fields should call `cfg := config.Snapshot()` at entry and use `cfg.*` throughout. Direct `config.AppConfig.Field` reads are permitted only in code that is demonstrably single-threaded or reads only one field.

**Priority callsites:**
- Scanner goroutines (reads `ConcurrentScans`, `ExcludePatterns`, `SupportedExtensions`, `MinBookSizeBytes`, `RootDir`)
- Dedup engine hot path (reads `DedupEmbeddingsEnabled`, thresholds)
- Maintenance window dispatcher (reads all `MaintenanceWindow*` fields)
- Basic auth middleware (reads `BasicAuthEnabled`, `BasicAuthUsername`, `BasicAuthPassword`)

**Effort:** Low per callsite. A grep for `config.AppConfig.` in goroutine-dispatching files gives the target list.

---

## Naming Consistency Issues (Appendix)

| Issue | Detail |
|---|---|
| `EnableSQLite` JSON/viper mismatch | JSON blob: `enable_sqlite`; viper/flag/env: `enable_sqlite3_i_know_the_risks` |
| `EmbeddingBaseURL` → `OPENAI_BASE_URL` overlap | Same semantic, two env var paths |
| `AcoustIDAPIKey` dual fallback | `AppConfig` field + `os.Getenv("ACOUSTID_API_KEY")` in consumer |
| Embedding defaults not in viper | `EmbeddingEnabled` etc. hardcoded in Mutate; `EMBEDDING_ENABLED` env var does nothing |
| `MetadataFetchCacheTTLDays` comment says "default 7" | Actual viper default is 180 |
| Old key aliases undocumented | `itunes_library_itl_path` / `itunes_library_xml_path` still work but deprecated |
| `mapstructure` tags on 4/155 fields | `viper.Unmarshal` silently broken for 151 fields |
| `OperationLogRetentionDays` | No viper default; only set in `ResetToDefaults`; zero value = no retention limit |
| `ActivityLogRetention*` fields (3) | No viper defaults; only set in `ResetToDefaults`; zero = disables retention (dangerous) |

---

## Recommended Next Steps

**Immediate (no code — just discipline fixes):**
1. Add `embedding_base_url` to `SyncConfigFromEnv()` — one line, fixes env var override gap
2. Add `mapstructure` tags to all remaining `Config` fields — mechanical, can be done in one PR
3. Document the ban on `viper.Unmarshal` in a code comment

**Short-term (CFG-1 decision, then one sub-struct per feature wave):**
4. Write decision doc to `docs/specs/config-architecture-decision.md` (CFG-1)
5. Add `ToolConfig` sub-struct as part of TOOL-1 implementation (prerequisite)
6. Add `EmbeddingConfig` sub-struct in next embedding work wave

**Medium-term:**
7. Extract leaf `internal/config` (Change A) — do this before adding many new sub-structs
8. Absorb `unified.ScoreConfig` into `DedupConfig` (ends parallel Viper system)
9. Adopt `Snapshot()` at goroutine boundaries (Change C) — low effort, high safety

**Long-term:**
10. Generate TypeScript `Config` interface from Go struct to eliminate the 40% gap
11. Retire `applySetting()` once all installs are confirmed on blob path

---

## Files Investigated

- `internal/config/config.go` — main struct, `InitConfig`, `Validate`, `ResetToDefaults`
- `internal/config/persistence.go` — `LoadConfigFromDatabase`, `SaveConfigToDatabase`, `applySetting`, `SyncConfigFromEnv`
- `internal/config/update_service.go` — runtime update path, JSON round-trip
- `internal/config/register.go` — service registry wiring via `init()`
- `cmd/root.go` — Cobra/Viper init, load sequence
- `internal/dedup/unified/config.go` — parallel Viper-only `ScoreConfig`
- `internal/itunes/service/config.go` — isolated iTunes config slice
- `internal/telemetry/config.go` — env-only telemetry config
- `internal/server/server.go` — `ServerConfig` (HTTP only, not persisted)
- `web/src/services/api.ts` — TypeScript `Config` interface (hand-curated)
- ~100 production files importing `internal/config`
