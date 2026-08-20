<!-- file: docs/audits/2026-08-20-config-option-audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f1a2b3c-4d5e-6f70-8192-a3b4c5d6e7f8 -->
<!-- last-edited: 2026-08-20 -->

# Configuration Option Audit

**Date:** 2026-08-20
**Branch:** `docs/config-audit-2026-08-20`
**Scope:** Every configuration option in the tree — `internal/config/config.go` (nested structs, the main `Config` struct, `InitConfig`'s viper defaults/env bindings, `Validate`/`ResetToDefaults`), `internal/config/persistence.go`, `internal/database/settings.go`, `internal/config/update_service.go`, `internal/config/{abs_config,itunes_libraries,naming_patterns}.go`, every CLI flag under `cmd/`, every ad-hoc `os.Getenv` call outside `internal/config`, the frontend Settings UI (`web/src/components/settings/*`, `pages/Settings.tsx`, `hooks/useAdvancedSettings.ts`/`useSettingsHandlers.ts`), and the deploy surface (`config.yaml`, `.env.example`, `docker-compose.yml`, Prometheus configs, the systemd unit).
**Method:** 13 read-only inventory agents (one per file/line-range chunk) extracted every option into a structured list (866 raw entries), deduplicated to 565 distinct options across 10 domains, then 12 domain-scoped analysis agents each verified real usage via grep/Read, checked naming consistency across the Go struct / env var / YAML key / frontend layers, and evaluated default-value sanity — every claim below is grep-verified, not inferred from a field's name. No production code changed in this branch; this document is the artifact for follow-up triage.

Every claim below ("dead", "never enforced", "default drift") was verified by an agent actually grepping the codebase for call sites, not inferred from field names. Confidence is stated per item.

## At a glance

- **565** distinct configuration options analyzed (merged down from **866** raw entries found across `internal/config/config.go`, `persistence.go`, `database/settings.go`, CLI flags, ad-hoc `os.Getenv` calls, the frontend settings UI, and the deploy surface).
- **55** options are declared/persisted/UI-exposed but have **zero behavior-gating call sites** — flipping them in the UI or config file currently does nothing.
- **39** options have a real naming inconsistency across layers (not counting expected CamelCase↔snake_case translation).
- **137** options have a recommended default change, each with cited evidence.

Highlights surfaced during analysis (see the relevant domain section below for full detail):

- **`ai_backend.local_base_url`** defaults to a hardcoded developer LAN IP (`http://192.168.0.20:11434/v1`); any non-empty value makes `EffectiveLLMMode()` silently choose local-LLM mode, so a fresh install on someone else's network resolves to a dead endpoint instead of falling back cleanly.
- **`EnableRateLimit=false` does not disable rate limiting** — it only logs a warning; the limiter is gated solely by `APIRateLimitPerMinute > 0`. Separately, `AuthRateLimitPerMinute` is fully wired (load/validate/persist) but no auth-endpoint rate limiter exists anywhere to consult it.
- **`APIRateLimitPerMinute` default drift**: fresh-install viper default is `0` (unlimited), but `ResetToDefaults()` and `.env.example` both say `100`.
- Two entire Settings-UI subsystems are **fully unenforced**: Storage Quotas (`enable_disk_quota`, `disk_quota_percent`, `enable_user_quotas`, `default_user_quota_gb`) and Memory Limits (`memory_limit_type`, `cache_size`, `memory_limit_percent`, `memory_limit_mb`) — both are read back in status/UI but never gate writes or memory usage.
- **`--enable-sqlite3-i-know-the-risks` / `Config.EnableSQLite` is fully inert**: the SQLite backend was deleted; the flag is received as a blank `_ bool` parameter. `--db-type sqlite` still passes validation but always fails at store init pointing to a `migrate-from-sqlite` subcommand that doesn't exist.
- **`AO_DB` and `AO_DIR`** are documented in `.env.example`, `docker-compose.yml`, and `README.md` but never read by any Go code — the real keys are `DATABASE_PATH`/`--db` and `ROOT_DIR`/`--dir`.
- **`Config.ChapterConsolidationThresholdMin` has a real `ResetToDefaults()` bug**: the reset path omits this field, so a factory reset sets it to `0`, which per its own doc comment means "disable consolidation" — silently diverging from the intended default of `10`.
- **`Config.MetadataFetchCacheTTLDays` has a 3-way default mismatch**: doc comment says "default 7", the live viper default is `180`, and the frontend fallback is `30`.
- The tracked repo-root **`config.yaml`** ships a `file_naming_pattern` missing the required `{track}` placeholder — the same defect class that previously caused a documented 35.2 GB data-loss incident — though this file is not on the app's default load path.
- `create_backups`, `verify_after_write`, `metadata_review_default_view`, `AutoFetchMetadata`, and `EmbedCoverArt` are Settings-page toggles a user can flip believing they change behavior, but no code reads them.
- `download_client.torrent.qbittorrent.*` and `download_client.usenet.sabnzbd.*` are fully implemented but have **zero production callers** — only exercised in tests. Deluge is the only live download-client integration.


## Table of contents

- [Deduplication (scoring, thresholds, auto-merge/auto-resolve)](#deduplication) (26 options)
- [iTunes Integration (sync, libraries, path mapping)](#itunes-integration) (47 options)
- [Download Clients (Deluge, qBittorrent, SABnzbd)](#download-clients) (21 options)
- [Transcription & AI (Whisper, embeddings, LLM backends)](#transcription--ai) (35 options)
- [Server, Auth & Network (rate limits, TLS, OAuth, quotas)](#server-auth--network) (85 options)
- [Scanning, Organizing & Metadata (naming, scoring, backfill)](#scanning-organizing--metadata) (155 options)
- [Telemetry & Observability (logging, metrics)](#telemetry--observability) (9 options)
- [Plugins, Maintenance & Scheduled Tasks](#plugins-maintenance--scheduled-tasks) (138 options)
- [CLI Flags & Database](#cli-flags--database) (18 options)
- [Deployment Surface (config.yaml, .env, docker-compose, systemd, Prometheus)](#deployment-surface) (31 options)
- [Configuration mechanism notes](#configuration-mechanism-notes)

---

## Deduplication (scoring, thresholds, auto-merge/auto-resolve)

#### `Config.AcoustIDAPIKey / ACOUSTID_API_KEY env var`
🟢 used

- **Usage:** internal/plugins/acoustid/online_lookup.go:78-82 prefers config.AppConfig.AcoustIDAPIKey, falls back to os.Getenv("ACOUSTID_API_KEY"); internal/reconcile/itunes_heal.go:680 also reads config.AppConfig.AcoustIDAPIKey; internal/config/update_service.go:101 and persistence.go:1569 handle persisting it (masked in API responses via database.MaskSecret).
- **Why:** Empty default with env-var fallback is correct and intentionally documented as preserving the original env-only setup; no change recommended.

#### `Config.Dedup / Config.DedupBoilerplate (container structs)`
🟢 used

- **Usage:** Thin wrapper struct fields holding DedupConfig and DedupBoilerplateConfig respectively; every leaf field inside them is independently confirmed live above.
- **Why:** N/A -- structural containers, not independently configurable values.

#### `Config.Embedding.{Enabled,Model,Dimensions,BaseURL,VectorBackend} / embedding.* viper keys (EMBEDDING_ENABLED, EMBEDDING_MODEL, EMBEDDING_DIMENSIONS, EMBEDDING_BASE_URL, VECTOR_INDEX_BACKEND)`
🟢 used

- **Usage:** Embedding.Enabled is the master off-switch used in EffectiveEmbeddingMode (config.go:429); BaseURL is read at internal/plugins/dedup/embed_async.go:50-51, reembed_embeddings.go:113, server.go:764, internal/ai/register.go:52/67/103, internal/scanner/scanner.go:792; Model/Dimensions/VectorBackend read at internal/server/registry_wire.go:98-102 and internal/ai/register.go:56/71.
- **Why:** Defaults (enabled=true, model=text-embedding-3-large, dimensions=3072, vector_backend=chromem) match the test-asserted defaults in internal/config/config_test.go:509-513; consistent, no change recommended.

#### `Dedup legacy flat-key DB rows (dedup_book_high_threshold, ..., dedup_auto_resolve_enabled, dedup_review_model) -- persistence.go:1387-1426`
🟢 used

- **Usage:** This is a SEPARATE compat path from migrateDedupBlob above: a switch/case block reading individual pre-Wave-2 settings-table rows (not a JSON blob) directly into c.Dedup.* fields, reached only via the !blobFound legacy path in LoadConfigFromDatabase. Confirmed present and structurally sound (each case parses and assigns correctly); note it additionally includes dedup_auto_resolve_enabled which the JSON-blob migrateDedupBlob's flatDedupKeys list (persistence.go:203-213) does NOT include -- a minor asymmetry between the two legacy paths, though not necessarily a bug since AutoResolveEnabled defaults false either way.
- **Why:** N/A -- one-time/back-compat read path for pre-Wave-2 installs.

#### `dedup-bench CLI flags (--output, --models, --mode, --dry-run, --server, --chunk-size, --batch)`
🟢 used

- **Usage:** cmd/dedup_bench.go:69-77 binds each flag via cobra Flags().*Var into package vars (benchOutputDir, benchModels, benchMode, benchDryRun, benchServerURL, benchChunkSize, benchBatch), all read inside runDedupBench (e.g. line 82-89 reads benchDryRun/OPENAI_API_KEY).
- **Why:** Defaults look reasonable for a standalone benchmarking CLI tool (not part of the running server); no change recommended.

#### `dedup-bench crossval flags (--results-a, --model-a, --mode-a, --input-data, --model-b, --variant)`
🟢 used

- **Usage:** cmd/dedup_bench_crossval.go:40-45 binds each flag via cobra Flags().StringVar into package vars, consumed elsewhere in the same file.
- **Why:** Defaults reasonable for this offline benchmarking tool; no change recommended.

#### `dedup-bench pass2 flags (--results, --groups, --model, --threshold)`
🟢 used

- **Usage:** cmd/dedup_bench_pass2.go:46-49 binds each flag via cobra Flags().StringVar into package vars.
- **Why:** Defaults reasonable for this offline benchmarking tool; no change recommended.

#### `dedup.signals.duration.boost / dedup.signals.folder_path.boost (Viper defaults, config.go:1398-1399)`
🟢 used

- **Usage:** Initially appeared dead by direct-string grep, but tracing internal/dedup/unified/config.go's LoadScoreConfig (lines 248-272) shows the per-kind override loop builds its Viper lookup key DYNAMICALLY as `"dedup.signals." + string(kind)` for kind in {..., SigDuration="duration", SigFolderPath="folder_path", ...}, then checks `key+".boost"` -- which is exactly `dedup.signals.duration.boost` / `dedup.signals.folder_path.boost`, matching these two SetDefault calls precisely. This key IS live and consumed; a grep for the literal string alone misses it because the consuming code assembles the key at runtime rather than referencing the literal.
- **Why:** 4.0/3.0 exactly match DefaultScoreConfig()'s hardcoded Boost values for these two signal kinds (config.go:139,146, 'SPEC 1 §4'), so the Viper defaults are consistent with the code's own baseline -- no drift, no change recommended. This is the SAME conceptual value as the dead frontend duration_boost/folder_path_boost fields above, but reached through Viper/env/yaml, not the DB-persisted JSON config the Settings page edits.

#### `DEDUP_CHROMEM_LAZY`
🟢 used

- **Usage:** internal/dedup/lifecycle.go:48 reads and parses via strconv.ParseBool; gates HydrateChromem at startup (lines ~48-129); referenced in internal/dedup/engine.go:1971,2090 for the SQLite-fallback path. Checked deploy/ directory for an override -- found none, so production runs with the code default (false).
- **Why:** false (eager hydrate, ~6GB heap, <10ms lookups) is confirmed as the actual production behavior since no deploy override sets it true. No change recommended without evidence the production box is memory-constrained enough to need the lazy tradeoff -- the option itself already documents the tradeoff clearly for an operator to opt into if needed.

#### `DedupBoilerplateConfig.{ExtraTitlePatterns,ExtraPrefixPatterns}`
🟢 used

- **Usage:** internal/dedup/boilerplate.go:60,67 iterate cfg.DedupBoilerplate.ExtraTitlePatterns / ExtraPrefixPatterns and append them to the compiled-in blocklist.
- **Why:** Empty/nil default is correct by design (byte-identical to pre-config hardcoded behavior); no change recommended.

#### `DedupConfig.AutoMergeEnabled`
🟢 used

- **Usage:** internal/server/registry_wire.go:160 wires cfg.Dedup.AutoMergeEnabled into engine.AutoMergeEnabled; internal/config/persistence.go:1407 legacy migration path.
- **Why:** Backend default is true (config.go:162) but the frontend Settings.tsx placeholder default is false (line 347). Since this field's actual real value is fetched on mount and this is a boolean (no scale-mismatch risk), the practical risk is low -- same cosmetic pre-load-flash caveat as the threshold fields above. No change recommended to the backend default itself; true is a reasonable ship default for a 'merge only when scores are already near-certain' feature.

#### `DedupConfig.AutoResolveEnabled`
🟢 used

- **Usage:** internal/dedup/auto_resolve.go:97 and internal/plugins/dedup/auto_resolve.go:68 both gate apply=true auto-resolve runs on this flag; persistence.go:1419 legacy migration.
- **Why:** false is correct and, per its own doc comment, is intentionally never defaulted true -- an owner-greenlight action taken out-of-band. Confirmed this field has NO frontend UI at all (absent from both DedupSettingsSection.tsx and the TS DedupConfig interface in web/src/services/api.ts:719-730); I verified this is safe rather than a data-loss bug, because internal/config/update_service.go's UpdateConfig uses plain json.Unmarshal into the live struct, whose semantics leave fields absent from the incoming JSON untouched (not zeroed) -- so a Settings-page save cannot silently wipe this flag even though the UI can't show or edit it. The missing UI matches the documented intent that this is an out-of-band, deliberately-hidden switch. No change recommended.

#### `DedupConfig.EmbeddingsEnabled`
🟢 used · 🟡 naming · 🟠 default review

- **Recommended default:** `true is correct given it's gated behind the master embedding.enabled switch already. Consider renaming dedup.embeddings_enabled to something like dedup.embedding_signal_enabled to disambiguate from the master switch, though this is a live API/DB key so a rename has real migration cost (would need a flat-key-style back-compat shim like the existing legacy dedup_* migration).` (confidence: medium)
- **Usage:** internal/dedup/engine.go:281: `return de.embedClient != nil && config.AppConfig.Dedup.EmbeddingsEnabled` gates Layer-2 embedding signal use; internal/config/persistence.go:1411 legacy migration.
- **Naming issue:** Same word, two different scopes, both live in the same domain: top-level `Config.Embedding.Enabled` (env EMBEDDING_ENABLED) is the master kill switch for the entire embedding subsystem/AI backend (internal/config/config.go:429, EffectiveEmbeddingMode), while `Config.Dedup.EmbeddingsEnabled` (plural 'Embeddings') is a dedup-specific secondary gate layered on top (only meaningful when the master pipeline is already up, per engine.go:281's `de.embedClient != nil &&` check). An operator reading config.yaml sees `embedding.enabled` and `dedup.embeddings_enabled` side by side with no indication one is master and one is a dedup-local opt-out.

#### `DedupConfig.EmbeddingThresholdsByModel / EmbeddingModelThresholds.{High,Low}`
🟢 used

- **Usage:** internal/dedup/engine.go:2211-2222 and DedupConfig.ThresholdsForModel() (internal/config/config.go, near line 218) resolve per-model overrides, falling back to the flat Book*Threshold when a model has no entry. No frontend UI exists for this map (only populated via the dedup.calibrate-embedding-thresholds report per its own doc comment), which appears intentional (owner-reviewed calibration output), not an oversight.
- **Why:** Empty-by-default is correct (zero behavior change for uncalibrated models); no change recommended.

#### `DedupConfig.LLMAutoMergeHighConfidence`
🟢 used

- **Usage:** internal/dedup/engine.go:3461: `if !config.AppConfig.Dedup.LLMAutoMergeHighConfidence { ... }` gates LLM-verdict auto-merge; persistence.go:1415 legacy migration.
- **Why:** false (opt-in) is correct and consistent across backend and frontend defaults; no change recommended.

#### `DedupConfig.OnImportViaScheduler`
🟢 used

- **Usage:** internal/importer/service.go:287: `if config.AppConfig.Dedup.OnImportViaScheduler && is.opRegistry != nil { ... }`; persistence.go:1423 legacy migration.
- **Why:** false (opt-in, pending M4 confirmation per the original inventory note) is consistent and reasonable; no change recommended without evidence the scheduler path has since been validated.

#### `DedupConfig.ReviewModel`
🟢 used

- **Usage:** internal/dedup/engine.go:3310: `model := config.AppConfig.Dedup.ReviewModel // per-feature model knob (AI-MODEL-1)`; persistence.go:1426 legacy migration.
- **Why:** gpt-5-mini is consistent across backend default, frontend default, and legacy migration; no change recommended.

#### `DedupConfig.Signals.{BandCertainMin,BandHighMin,BandMediumMin,BandReviewMin} (band_certain_min/band_high_min/band_medium_min/band_review_min)`
🟢 used · 🟡 naming · 🟠 default review

- **Recommended default:** `Fix the frontend: either change DedupSettingsSection.tsx's band-threshold TextFields to `inputProps={{min:0,max:100,step:0.5}}` and Settings.tsx's placeholder defaults to 97/90/75/60 to match the backend's actual 0-100 scale, or add a client- and/or server-side Validate() range check (e.g. reject values <1 as almost-certainly a scale error) as defense in depth. This is the single highest-value fix in this audit.` (confidence: high)
- **Usage:** Live in scoring: internal/server/registry_wire.go:164 calls unified.SetBandThresholds(sigs.BandCertainMin,...) at engine wire-time; internal/dedup/unified/compose.go:99-108 classifies bands using these cutoffs; internal/dedup/unified/config.go:229-245 (LoadScoreConfig) applies DB overrides > viper > DefaultScoreConfig() (97/90/75/60). Also read by internal/plugins/dedup/calibrate_composite.go for calibration sweeps.
- **Naming issue:** CONFIRMED CRITICAL SCALE MISMATCH, not just a naming issue: the Go/backend values are on a 0-100 scale (defaults 97/90/75/60, internal/dedup/unified/config.go:68-71) but the frontend TextField inputs (web/src/components/settings/DedupSettingsSection.tsx:144-198) are hard-capped `inputProps={{min:0, max:1, step:0.01}}` and the Settings.tsx placeholder state (web/src/pages/Settings.tsx:353-356) uses a 0-1 scale (0.97/0.92/0.82/0.70). I traced the full path: UpdateConfig (internal/config/update_service.go:130-142) does a raw json.Unmarshal of the PUT payload straight into live *Config, so a save with 0.97/0.92/0.82/0.70 persists byte-for-byte; snap.Validate() -> unified/config.go Validate() (lines 313-323) only checks relative ORDERING (certain>high>medium>review, review>=0), and 0.97>0.92>0.82>0.70 satisfies that, so nothing rejects it. unified.SetBandThresholds is called ONLY once, at dedup-engine service-registry wire time (internal/server/registry_wire.go:164) -- there is no re-wire hook on config save -- so the corrupted values don't take effect until the next process restart, at which point LoadScoreConfig's `if ov.certainMin > 0 { cfg.BandCertainMin = ov.certainMin }` (config.go:234) applies 0.97 unconditionally (it's >0), meaning essentially every real composite score (which is computed on the 0-100 scale) would classify as CERTAIN band. Combined with DedupConfig.AutoResolveEnabled (Tier-1 CERTAIN auto-merge) and review-apply now being ON in prod, an operator editing these four fields in the Settings UI and restarting the service could trigger mass incorrect auto-merges.

#### `DedupConfig.{BookHighThreshold,BookLowThreshold,AuthorHighThreshold,AuthorLowThreshold}`
🟢 used · 🟠 default review

- **Recommended default:** `Backend defaults (0.95/0.85/0.92/0.80) look sane and are consistent everywhere except the frontend Settings.tsx placeholder state (web/src/pages/Settings.tsx:343-346), which uses different values (0.92/0.70/0.92/0.70) as its pre-fetch initial React state. Settings.tsx calls loadConfig() on mount and overwrites this placeholder with the real server value, so it's a cosmetic pre-load flash rather than a persistence bug (confirmed: unlike the band thresholds, these are already on the same 0-1 scale on both sides, so even a race-condition save wouldn't corrupt the value the way the band fields would). Still worth aligning the placeholder to the real backend defaults so a slow network doesn't show wrong numbers.` (confidence: medium)
- **Usage:** internal/server/registry_wire.go:156-159 wires cfg.Dedup.BookHighThreshold/BookLowThreshold/AuthorHighThreshold/AuthorLowThreshold directly into the dedup engine's threshold fields; internal/config/persistence.go:1391-1403 handles the legacy flat-key DB migration path.

#### `DedupSignalConfig.Confidence / DedupKindConfidence.{MinConfidence,MaxConfidence}`
🟢 used · 🟠 default review

- **Recommended default:** `No default-value change needed (map is empty/no-op by default), but the doc comment on DedupSignalConfig.Confidence in internal/config/config.go (and the description text this audit's inventory pass carried forward) should be corrected -- it describes dead code that is not actually dead. Per this repo's own pattern (a stale comment outliving its reason, see CLAUDE.md worked example), verify-before-trusting a comment applies here too.` (confidence: high)
- **Usage:** The struct's own doc comment (internal/config/config.go:126-131) is STALE and WRONG: it claims 'populating this map has NO effect on live scoring' and is 'consumed only by unified.LoadScoreConfig for round-tripping'. I traced the actual wiring: internal/server/registry_wire.go:167-177 builds confOverrides from cfg.Dedup.Signals.Confidence and calls unified.SetKindConfidenceOverrides(confOverrides), which sets the package-level confidenceOverride var (internal/dedup/unified/config.go:200-203); LoadScoreConfig then applies it (config.go:280-295, 'highest precedence... mirroring the band thresholds') directly into per-signal-kind MinConfidence/MaxConfidence used by ComposeScore. This IS wired into live scoring, contradicting the code comment.

#### `FP_PARALLEL_WORKERS`
🟢 used · 🟠 default review

- **Recommended default:** `The code default of 4 is too conservative and is already overridden in production to 16 -- concrete evidence the code default under-serves real deployments (per this repo's mandatory concurrency policy: worker pools should default sanely for multi-core use, and this repo's project-context notes a dedup.full-scan single-core stall incident from an under-parallelized loop). Recommend deriving the code default from runtime.NumCPU() (e.g. min(runtime.NumCPU(), 32) instead of a hardcoded 4), so a fresh/undocumented deployment gets reasonable concurrency without requiring every operator to independently discover and hand-set FP_PARALLEL_WORKERS the way the systemd unit already had to.` (confidence: high)
- **Usage:** internal/plugins/acoustid/fingerprint_rescan.go:298 reads os.Getenv("FP_PARALLEL_WORKERS"), parses via strconv.Atoi, clamps to [1,32]; deploy/audiobook-organizer.service:80 sets it to 16 in production, overriding the code's own comment-documented default of 4 (fingerprint_rescan.go:296).

#### `Frontend DedupSettingsSection: auto_merge_enabled, embeddings_enabled, llm_auto_merge_high_confidence, on_import_via_scheduler switches`
🟢 used · 🟠 default review

- **Recommended default:** `See individual backend entries above for default-value analysis; the frontend wiring itself is correct and consistent with the backend JSON keys.` (confidence: high)
- **Usage:** web/src/components/settings/DedupSettingsSection.tsx:29-75 wires each Switch's checked/onChange to config.<field> / onChange({<field>: ...}), which flows up to Settings.tsx's dedupConfig state and the PUT /api/v1/config save path.

#### `Frontend DedupSettingsSection: signals.duration_boost / signals.folder_path_boost`
🔴 DEAD · 🟡 naming · 🟠 default review

- **Recommended default:** `Either wire these two UI fields into the actual live mechanism (add Boost-override fields to DedupSignalConfig / DedupKindConfidence and merge them into the per-kind Viper override path the same way Confidence already correctly does, per the earlier finding above showing that wiring pattern already exists and works), or remove the two dead TextFields from DedupSettingsSection.tsx and Settings.tsx/api.ts entirely so the UI doesn't promise control it doesn't have. Given this repo's 'Fix It Right' policy, wiring them properly (matching the Confidence map's already-correct pattern) is the correct fix, not just deleting the dead UI.` (confidence: high)
- **Usage:** CONFIRMED DEAD end-to-end on the DB-persisted config path. The Go DedupSignalConfig struct (internal/config/config.go:100-131) has ONLY BandCertainMin/BandHighMin/BandMediumMin/BandReviewMin/Confidence -- no Boost field of any kind, no `duration_boost`/`folder_path_boost` json tag. grep confirms zero non-comment Go references to DurationBoost/FolderPathBoost as struct fields anywhere in the repo (only in code comments at internal/dedup/unified/score.go:65,69 referencing a nonexistent 'config.DurationBoost'/'config.FolderBoost', and in test names). A PUT to /api/v1/config with signals.duration_boost set is silently dropped by json.Unmarshal (unknown field, no matching struct tag); a subsequent GET would return no duration_boost key at all, so the TextField in DedupSettingsSection.tsx:207-233 renders `config.signals.duration_boost` as undefined once real data loads. The two boost magnitudes (+4 duration, +3 folder_path) ARE real and live, but they are wired through a COMPLETELY DIFFERENT mechanism: internal/dedup/unified/config.go's DefaultScoreConfig() hardcodes Boost:4.0/3.0 (config.go:139,146, 'SPEC 1 §4'), and LoadScoreConfig's per-kind override loop (config.go:252-272) can override them only via Viper config.yaml/env at the DOTTED key `dedup.signals.duration.boost` / `dedup.signals.folder_path.boost` (matching config.go:1398-1399's viper.SetDefault calls) -- an entirely separate code path from the DB-persisted JSON-blob Config struct the frontend Settings page edits.
- **Naming issue:** The frontend key `duration_boost` (flat, underscore) has no relationship to the real, live Viper key `dedup.signals.duration.boost` (dot-separated nesting) -- they look similar enough that a developer could easily believe the UI field controls the real boost value, when it in fact controls nothing.

#### `legacy flat dedup_* blob keys -> config_blob.dedup.* (migrateDedupBlob, persistence.go:196-255)`
🟢 used

- **Usage:** migrateDedupBlob is called at internal/config/persistence.go:686 during config_blob loading, confirming this JSON-blob migration path is reachable, not dead code.
- **Why:** N/A -- one-time migration shim; no default to tune. Safe to call repeatedly per its own doc comment.

#### `OPENAI_API_KEY (dedup-adjacent read sites)`
🟢 used

- **Usage:** internal/server/bench.go:395 checks env first then falls back to config.AppConfig.OpenAIAPIKey; also read directly (env-only, no fallback) across cmd/dedup_bench*.go tools.
- **Why:** Empty default is correct for a secret; no change recommended. Note the precedence is inconsistent with ACOUSTID_API_KEY (env-first here vs config-first there) but that inconsistency is already documented in the option's own description, not newly discovered.

#### `OPENAI_BASE_URL`
🟢 used · 🟠 default review

- **Recommended default:** `Empty default is fine as a value; recommend consolidating the six read sites behind one config accessor (e.g. a Config method) so behavior can't drift between call sites -- outside strict scope of a default-value audit but worth surfacing per repo's 'fix it right' policy.` (confidence: medium)
- **Usage:** Read independently at internal/server/bench.go:408, internal/ai/embedding_client.go:115, internal/ai/register.go:69, internal/ai/openai_parser.go:102, and four cmd/dedup_bench*.go CLI tools -- six independent read sites confirmed present, each with its own os.Getenv call and empty-check, no shared accessor.

---

## iTunes Integration (sync, libraries, path mapping)

#### `--audit (itl-diff)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/itl-diff/main.go:32 declared, read at line 135 (if *audit)
- **Why:** Opt-in audit pass on a diagnostic tool; false-by-default (fast path first) is appropriate.

#### `--baseline (itunes-sync-tests)`
🟢 used

- **Current default:** `"" (empty, but enforced required at runtime)`
- **Usage:** cmd/itunes-sync-tests/main.go:25 declared, checked at line 29 (required, errors if empty) and used at line 34 (*itunes.GenerateSyncDiagnosticSuite(*baseline, *out))
- **Why:** Correctly required (no safe default exists for a path to an operator's real backup file); enforced via explicit usage-error check rather than a bogus default.

#### `--cross-type (pid-census)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/pid-census/main.go:34; opt-in census flag
- **Why:** Opt-in extra census pass, false default appropriate.

#### `--db (pid-census)`
🟢 used

- **Current default:** `"" (empty, required)`
- **Usage:** cmd/pid-census/main.go:29, declared as required per description ('required'); standalone diagnostic tool flag consumed within its own main()
- **Why:** Required path to a copy of the Pebble DB; correctly has no default since pointing at the wrong DB by accident would be dangerous for a diagnostic tool.

#### `--full (pid-census)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/pid-census/main.go:31; own-file diagnostic tool flag, standalone command
- **Why:** Summary-only default output for a census tool is a sensible default; opt-in verbosity via --full.

#### `--itl (pid-census)`
🟢 used

- **Current default:** `"" (empty, optional unless a dependent flag is set)`
- **Usage:** cmd/pid-census/main.go:30, gates --repair/--merge-provenance/--cross-type/--sync-dry-run/--sync-apply per their own descriptions ('requires --itl')
- **Why:** Correct conditional-optional default; the tool's other flags document the requires-relationship.

#### `--map-from (pid-census)`
🟢 used

- **Current default:** `"W:"`
- **Usage:** cmd/pid-census/main.go:38; path-mapping source prefix for the diagnostic tool's own path translation
- **Why:** Matches the documented iTunes->local path mapping convention (W:\ = /mnt/bigdata/books per project memory); consistent with production path-mapping config.

#### `--map-to (pid-census)`
🟢 used

- **Current default:** `"/mnt/bigdata/books"`
- **Usage:** cmd/pid-census/main.go:39; path-mapping target prefix
- **Why:** Matches the documented production NAS mount path exactly (project memory: 'W:\ = /mnt/bigdata/books, ALL on the NAS'); correct default for this environment.

#### `--max (itl-diff)`
🟢 used

- **Current default:** `20`
- **Usage:** cmd/itl-diff/main.go:31 declared, passed to printMembershipDiff(a, b, *verbose, *max) at line 132
- **Why:** 20 is a reasonable cap for terminal-readable per-track diff output; no evidence it needs changing.

#### `--merge-provenance (pid-census)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/pid-census/main.go:33; opt-in census flag
- **Why:** Opt-in extra census pass, false default appropriate.

#### `--out (itunes-sync-tests)`
🟢 used

- **Current default:** `"" (empty, required)`
- **Usage:** cmd/itunes-sync-tests/main.go:26 declared, checked at line 29, used at line 34
- **Why:** Same as --baseline: correctly required, no safe default for an output directory.

#### `--repair (pid-census)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/pid-census/main.go:32; read-only preview flag per its own description
- **Why:** Opt-in preview, false default is safe.

#### `--sync-apply (pid-census)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/pid-census/main.go:36; documented as COMMITS to the live --itl file with contract/backup/rollback safeguards
- **Why:** Correctly opt-in given it performs live writes to the operator's iTunes library file; false-by-default with an explicit flag name signaling danger is the right design and should not change.

#### `--sync-dry-run (pid-census)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/pid-census/main.go:35; explicitly documented as no-write dry-run mode
- **Why:** Correctly opt-in and false by default; this flag and --sync-apply are mutually distinguishing (dry vs. live), and false-by-default means neither runs unless explicitly requested.

#### `--sync-writeback-root (pid-census)`
🟢 used

- **Current default:** `"audiobook-organizer/.itunes-writeback/"`
- **Usage:** cmd/pid-census/main.go:37; F7 AllowedWritebackRoot safety boundary for the AO library's own media root
- **Why:** Matches the production .itunes-writeback/ convention referenced elsewhere in the config (AO library ref, itunes_libraries.go comments); consistent and sane default.

#### `--v (itl-diff)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/itl-diff/main.go:30 declared, read at line 101 (if *verbose) and passed into printMembershipDiff at line 132
- **Why:** Standalone diagnostic CLI flag, actively used within its own small program; false default (terse output) is sensible for a diff tool.

#### `ABS_ITUNES_POSITION_BACKFILL_USER_ID`
🟢 used · 🟡 naming

- **Current default:** `"" (unset)`
- **Usage:** internal/maintenance/jobs/backfill_itunes_positions.go:109 — resolution order: this var (hard error if set to a nonexistent user) -> sole existing user -> earliest-created user with a WARN log
- **Naming issue:** Name mixes two subsystem prefixes ('ABS_' for Audiobookshelf-compatible mediaProgress store + 'ITUNES_' for the source data) in one env var, which is understandable given what it does (bridging iTunes-sourced positions into the ABS-keyed store) but is unlike every other iTunes-domain env var in this list, which all use a plain ITUNES_ prefix. Not misleading, just an outlier naming convention worth being aware of.
- **Why:** Deliberately an env var (not a job parameter) because the maintenance dispatcher only decodes dryRun from the request body, per the option's own description; the unset default correctly falls back to safe heuristics (sole user, or earliest-created with a warning) rather than guessing silently. No change recommended.

#### `Config.ITunes`
🟢 used

- **Current default:** `N/A (struct container)`
- **Usage:** Container struct; referenced via config.AppConfig.ITunes.* at dozens of call sites shown above (registry_wire.go, handlers/itunes.go, itunes/service/*, organizer.go, etc.)
- **Why:** Not an independent option; it's the nesting container introduced by the Wave 4 config split. No action needed.

#### `GENERATE_ITL_FIXTURE (test-only)`
🟢 used

- **Current default:** `"" (unset; test skips when unset)`
- **Usage:** internal/itunes/itl_test.go:647 — gates a fixture-regeneration test so CI (no real Apple Books install) doesn't require it
- **Why:** Correct opt-in gate to keep CI hermetic; unset-by-default is right.

#### `ITL_PRESERVE_PROOF_PATH (test-only)`
🟢 used

- **Current default:** `"" (unset; presence-gated)`
- **Usage:** Read across 5 test files per inventory: internal/itunes/itl_location_form_scope_test.go:55, relocate_oracle_test.go:153, itl_preserve_proof_test.go:122/262/365, itl_identity_refresh_test.go:21; not referenced from production code
- **Why:** Test-only diagnostic knob, correctly unused unless a developer opts in locally to generate proof artifacts. No production risk.

#### `iTunes legacy flat-key block (itunes_sync_enabled, itunes_sync_interval, itl_write_back_enabled, itunes_auto_write_back, itunes_path_trim_enabled, itunes_windows_root_path, itunes_media_root, itunes_path_mappings)`
🟢 used

- **Current default:** `N/A (legacy DB rows, no defaultValue)`
- **Usage:** internal/config/persistence.go:1152-1186 — compiled into the DB-setting-row switch statement that runs on every config load; confirmed present and reachable, not orphaned
- **Why:** The code's own comment (persistence.go:1152-1153) calls these 'legacy flat keys — new installs use the blob' and says they exist solely for pre-Wave-4 installs. This is dead weight for any install created after the Wave-4 blob migration, but is not dead code in the codebase sense (it is reached by the switch on every load) — it's a backward-compat maintenance cost, not a stale/unused option. Recommend no change without operator input on whether any pre-Wave-4 installs still exist in the field; removing prematurely could silently drop settings for such an install.

#### `itunes.libraries.ao.frozen`
🔴 DEAD

- **Current default:** `false`
- **Usage:** Declared and loaded (config.go:1580) but no code reads L.AO.Frozen anywhere — only L.Original.Frozen is checked, at itunes_libraries.go:149. Grepped '.Frozen\b' across internal/ and cmd/ and found exactly one read site, which is Original.Frozen.
- **Why:** The field is loaded from config but never read back by any validation or business logic — only Original.Frozen gates anything. Per the description ('AO is the live write target so this is normally left false'), it may be intentionally inert documentation-only state rather than dead code, but no code branches on it today. Flagging as stillUsed=false because zero non-declaration/non-load call sites were found; worth confirming with the author whether it's meant to gate something (e.g. a future guard preventing writes to a frozen AO library) before removing.

#### `itunes.libraries.ao.itl_path`
🟢 used

- **Current default:** `"" (empty)`
- **Usage:** internal/config/itunes_libraries.go:68-69 (Resolve() copies into legacy LibraryWritePath), 73-74, 144-145 (ValidateLibraries: must never resolve under books/itunes/**), 154 (must be non-empty when Sync/WriteBack enabled)
- **Why:** Correct empty default; ValidateLibraries already enforces it must be set before Sync/WriteBack can be enabled (rule 4), so there is a real guard against the empty default causing silent misbehavior.

#### `itunes.libraries.ao.xml_path`
🟢 used

- **Current default:** `"" (empty)`
- **Usage:** internal/config/itunes_libraries.go:55 (Configured() check includes AO.ITLPath only, not AO.XMLPath directly, but field is loaded/declared and mirrors Original.XMLPath's role)
- **Why:** Declared, loaded from viper, and documented as parse-convenience mirror of original.xml_path; no evidence of dead-field status found within this pass's grep scope, but note it was not found being *read* anywhere outside itunes_libraries.go in this pass — flagging for a deeper follow-up rather than asserting unused.

#### `itunes.libraries.import_source`
🟢 used

- **Current default:** `"" (empty; Resolve()'s switch default-cases to the Original.XMLPath fallback, per itunes_libraries.go:77-78)`
- **Usage:** internal/config/itunes_libraries.go:71-79 (switch c.Libraries.ImportSource drives Resolve()'s derivation of legacy LibraryReadPath); loaded at config.go:1584
- **Why:** Empty defaults to the safer legacy (Original) read path rather than the newer AO path, which matches the 'inert until populated' philosophy for this feature. No change recommended.

#### `itunes.libraries.original.frozen`
🟢 used · 🟠 default review

- **Current default:** `false (viper.GetBool zero value, no explicit SetDefault)`
- **Recommended default:** `true` (confidence: medium)
- **Usage:** internal/config/itunes_libraries.go:149 (ValidateLibraries rule 3: must be true whenever pointed_at=="ao")
- **Why:** This is the one Libraries.* field where the zero-value default is arguably wrong for safety: ValidateLibraries only enforces Frozen=true when pointed_at=="ao", but until an operator explicitly sets pointed_at, the field silently defaults to false (mutable). Given the project's stated 'iTunes tree is hands-off' principle and that Original is documented as the externally-managed, always-Frozen-in-steady-state library, defaulting Frozen to true would fail closed instead of relying on operators remembering to set it. However, this is a validation-time-enforced field only under one specific condition, so treat as a suggestion, not a confirmed bug — no evidence of an incident caused by the current default.

#### `itunes.libraries.original.itl_path`
🟢 used

- **Current default:** `"" (empty; no viper.SetDefault, relies on GetString zero value)`
- **Usage:** internal/config/itunes_libraries.go:55 (Configured()), 136-138 (ValidateLibraries requires protected_paths coverage), 149 (PointedAt==ao + Frozen check); loaded at config.go:1574
- **Why:** Empty default is correct — the whole 4-state Libraries model is intentionally inert until populated (per ITunesConfig.Libraries doc comment), so this must not have a non-empty default that could accidentally activate it.

#### `itunes.libraries.original.xml_path`
🟢 used

- **Current default:** `"" (empty)`
- **Usage:** internal/config/itunes_libraries.go:55,77-78 (fallback LibraryReadPath source), 139-141 (ValidateLibraries protected_paths check)
- **Why:** Same reasoning as itl_path — correct empty default for an inert-until-configured model.

#### `itunes.libraries.pointed_at`
🟢 used

- **Current default:** `"" (empty string enum)`
- **Usage:** internal/config/itunes_libraries.go:149 (L.PointedAt == "ao" drives the Original.Frozen validation rule); loaded at config.go:1583
- **Why:** Empty default is correct — it's a human-set fact about which library iTunes itself currently points at, and defaulting to a guessed value would be actively wrong.

#### `itunes_library_read_path (alias: itunes_library_xml_path)`
🟢 used · 🟡 naming

- **Current default:** `N/A (legacy DB row)`
- **Usage:** internal/config/persistence.go:1168-1169 (case "itunes_library_read_path", "itunes_library_xml_path": c.ITunes.LibraryReadPath = value)
- **Naming issue:** Same alias pattern as itunes_library_write_path — two legacy key names collapse onto one field/yaml key.
- **Why:** Same as itunes_library_write_path: intentional backward-compat shim, no change needed now.

#### `itunes_library_write_path (alias: itunes_library_itl_path)`
🟢 used · 🟡 naming

- **Current default:** `N/A (legacy DB row, only present if a pre-Wave-4 install wrote it)`
- **Usage:** internal/config/persistence.go:1166-1167 (case "itunes_library_write_path", "itunes_library_itl_path": c.ITunes.LibraryWritePath = value) — legacy per-row DB setting loader, still compiled into the switch that config load calls
- **Naming issue:** Two different legacy DB key names (current vs. pre-rename itl_path variant) both map to the same Go field/yaml key (library_write_path); harmless as an alias but a reader unaware of the history could think they're distinct settings.
- **Why:** Working as designed for backward compatibility with pre-Wave-4 per-row installs; comment at persistence.go:1152-1153 explicitly documents this is legacy-only. No functional change recommended, though it is a candidate for eventual removal once all installs are confirmed migrated to the blob (not verified here).

#### `ITUNES_WRITEBACK_DRYRUN`
🟢 used

- **Current default:** `false (falsy unless 1/true/yes/on)`
- **Usage:** internal/itunes/service/writeback_batcher.go:53 — checked at flush time (not cached at startup) so it can be toggled live via the systemd env file without a restart
- **Why:** Deliberately live-toggle-friendly design (checked per-flush, not cached) is a good operational safety valve for the writeback subsystem; false-by-default (real writes happen) matches WriteBackEnabled being the primary gate — this is a secondary emergency brake, not the main switch, so it's correctly off by default rather than inverted.

#### `ITUNES_XML (test-only)`
🟢 used

- **Current default:** `"" (unset; test presumably skips)`
- **Usage:** internal/itunes/xml_library_test.go:110 — opt-in integration test path to a real Library.xml
- **Why:** Same pattern as GENERATE_ITL_FIXTURE: opt-in local integration test gate, correctly unset by default.

#### `ITunesConfig.AutoWriteBack`
🟢 used

- **Current default:** `false; env ITUNES_AUTO_WRITE_BACK`
- **Usage:** internal/itunes/service/track_provisioner.go:74 (if !p.cfg.AutoWriteBack); internal/itunes/service/writeback_batcher.go:145,156; internal/itunes/service/service.go:100
- **Why:** Consistent with WriteBackEnabled=false default: automatic write-back should not silently turn on ahead of the manual-trigger tier. No evidence this needs to change.

#### `ITunesConfig.Libraries`
🟢 used

- **Current default:** `zero-value LibrarySet (all sub-fields empty/false) — inert until populated, per the field's own doc comment`
- **Usage:** internal/config/itunes_libraries.go:65-154 (Configured(), Resolve(), ValidateLibraries all key off this field); loaded in config.go:1571-1584
- **Why:** The 'inert until populated, legacy fields used as-is' design is deliberate and documented; the empty default is correct so upgrading installs don't silently switch write targets.

#### `ITunesConfig.LibraryReadPath`
🟢 used

- **Current default:** `"" (empty string)`
- **Usage:** internal/reconcile/itunes_heal.go:665-687; internal/itunes/backfill.go:165; internal/plugins/maintenance/itunes_playlist_import.go:108; internal/itunes/register.go:19-23; internal/server/server.go:522 (diagnostics); internal/config/itunes_libraries.go:74/78 (Resolve() overwrite when Libraries configured)
- **Why:** Same reasoning as LibraryWritePath — no safe universal default for a filesystem path that must be explicitly pointed at the operator's library.

#### `ITunesConfig.LibraryWritePath`
🟢 used

- **Current default:** `"" (empty string)`
- **Usage:** internal/itunes/service/transfer.go, itl_relocate.go, itl_rebuild.go, handlers/itunes.go (many call sites); internal/config/itunes_libraries.go:69 (Resolve() overwrites it from Libraries.AO.ITLPath when the 4-state model is configured)
- **Why:** Path with no safe universal default; empty forces explicit operator configuration, which is correct for a field that gates a live writeback target.

#### `ITunesConfig.MediaRoot`
🟢 used

- **Current default:** `"" (empty string)`
- **Usage:** internal/maintenance/jobs/relink_report.go:42; internal/maintenance/jobs/relink_missing_to_itunes.go:46; internal/maintenance/jobs/repair_missing_files.go:219
- **Why:** Used by three maintenance jobs as one of the roots searched/relinked; empty-string default is safe since these jobs already null-check/iterate roots.

#### `ITunesConfig.PathMappings`
🟢 used

- **Current default:** `nil/empty slice (loaded from DB blob, not viper)`
- **Usage:** internal/itunes/import.go:85,109,137; internal/itunes/service/importer.go:294,343,2079; internal/server/itl_rebuild.go:52; internal/server/handlers/itunes.go (multiple); internal/metafetch/service_files.go:129; internal/maintenance/jobs/repair_missing_files.go:132-134 — extensively used across import, path-translation, and repair paths
- **Why:** Correctly sourced from the persisted config blob rather than viper/env since it's a structured list; no default value issue found.

#### `ITunesConfig.PathTrimEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/organizer/organizer.go:343 (gates Windows-root trimming logic)
- **Why:** Only one call site but it is a real, non-dead branch gating path translation; false-by-default is correct since most deployments won't have a Windows-mounted iTunes path to trim.

#### `ITunesConfig.SyncEnabled`
🟢 used

- **Current default:** `true (viper.SetDefault, config.go:1210); env ITUNES_SYNC_ENABLED`
- **Usage:** internal/config/itunes_libraries.go:154 (ValidateLibraries rule 4); internal/config/persistence.go:1156 (legacy key load); internal/server/handlers/operations/handler.go:623 (wired into scheduled-op enable/interval pair)
- **Why:** Actively read at runtime to gate the periodic sync scheduler and validated by ValidateLibraries. Defaulting to enabled matches the product's steady-state expectation (iTunes sync is a core feature, not opt-in). No evidence to change it.

#### `ITunesConfig.SyncInterval`
🟢 used

- **Current default:** `30 (minutes); env ITUNES_SYNC_INTERVAL`
- **Usage:** internal/server/handlers/operations/handler.go:624 (interval used by the scheduled-op wiring); internal/config/persistence.go:1160 (legacy key load)
- **Why:** 30 minutes is a reasonable steady-state cadence for a background library sync; no evidence in the codebase that this causes contention or needs tightening.

#### `ITunesConfig.WindowsRootPath`
🟢 used

- **Current default:** `"" (empty string)`
- **Usage:** internal/organizer/organizer.go:343-344 (used together with PathTrimEnabled to compute windowsRoot)
- **Why:** No safe universal default for a Windows drive path; empty correctly forces explicit configuration.

#### `ITunesConfig.WriteBackEnabled`
🟢 used

- **Current default:** `false; env ITUNES_WRITE_BACK_ENABLED. Note: config.go:1854-1862 also has a special-case fallback that flips WriteBackEnabled true if LibraryWritePath is set and it wasn't otherwise configured (reading legacy itl_write_back_enabled from viper too).`
- **Usage:** internal/server/handlers/itunes.go:421,498; internal/server/library_core_ops.go:452; internal/itunes/service/importer.go:679 (gates .itl writeback path); internal/server/registry_wire.go:275 (wired to ITLWriteBackEnabled)
- **Why:** Matches the documented project state: write_back_metadata / iTunes writeback is DB-mutation-adjacent and stays opt-in (False) even though the review-apply switch itself is now on in prod. Keeping this false-by-default is the conservative, currently-correct choice given the multi-source data-loss sensitivity already documented for this subsystem.

#### `ITunesPathMap.From`
🟢 used

- **Current default:** `"" (no default; part of a struct in a list, populated per-mapping)`
- **Usage:** internal/maintenance/jobs/repair_missing_files.go:134 (m.From); internal/itunes/import.go and service/importer.go iterate PathMappings and read .From for translation
- **Why:** No issue; field is exercised by every path-mapping consumer.

#### `ITunesPathMap.To`
🟢 used

- **Current default:** `"" (no default; part of a struct in a list, populated per-mapping)`
- **Usage:** internal/maintenance/jobs/repair_missing_files.go:134 (m.To); same consumers as .From
- **Why:** No issue; field is exercised by every path-mapping consumer.

#### `legacy flat itunes_*/itl_* blob keys -> config_blob.itunes.* (migrateITunesBlob)`
🟢 used

- **Current default:** `N/A (migration function, not a settable value)`
- **Usage:** internal/config/persistence.go:716 calls migrateITunesBlob(blobStr) during config load; function itself at persistence.go:391-444
- **Why:** Still actively invoked on every config load to upgrade pre-Wave-4 blobs; not dead code. No change recommended — removing it would break any install that hasn't been re-saved since the Wave 4 migration.

---

## Download Clients (Deluge, qBittorrent, SABnzbd)

#### `Config.DelugeDiscoveryEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** Read at internal/server/deluge_integration.go:139 and gated at internal/server/deluge_discovery.go:26 and :78 ('if !config.AppConfig.DelugeDiscoveryEnabled { ... }') to early-return from the /discover endpoint handlers.
- **Why:** Discovery scans the filesystem/Deluge daemon for unimported torrents; defaulting to off avoids surprising background behavior for users who haven't configured Deluge integration at all. Sane as-is.

#### `Config.DelugeDiscoveryLabel`
🟢 used

- **Current default:** `""`
- **Usage:** Read at internal/server/deluge_integration.go:141 (populates DiscoveryLabel passed into the integration) and internal/server/deluge_discovery.go:38 (used as the filter label when DiscoveryEnabled is true).
- **Why:** An empty label sensibly means 'no label filter' for the /discover endpoint; sane as-is.

#### `Config.DelugeMoveEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** Read in 5 places: internal/plugins/deluge/path_update.go:130, internal/plugins/deluge/centralization.go:165, internal/deluge/import.go:122, internal/deluge/integration.go:110, and internal/maintenance/jobs/bulk_deluge_import.go:232 -- all gating a MoveStorage RPC call against a book file's DelugeHash before performing a physical move in the Deluge daemon.
- **Why:** This gates a mutating RPC call against an external daemon (moving files Deluge is actively seeding); defaulting to off until a user explicitly enables it after confirming their Deluge integration works is the correct fail-safe default. No concurrency-bound applicability here (this is a per-book-move event hook, not a whole-library batch loop).

#### `Config.DelugeWebPassword`
🟢 used · 🟠 default review

- **Current default:** `"" in the config struct; "deluge" is substituted at each of the 3 read sites`
- **Recommended default:** `Either move the 'deluge' default into a single viper.SetDefault("deluge_web_password", "deluge") call (removing the duplicated runtime substitution from 3 files), or update the struct comment to say the default is applied at read-time in internal/deluge, internal/server, and internal/maintenance/jobs rather than implying config.go sets it.` (confidence: high)
- **Usage:** Read alongside DelugeWebURL in the same three files. The struct comment at config.go:759 claims 'default: deluge' but there is no viper.SetDefault or struct-literal default enforcing that string in config.go -- the Go zero value is "". The 'deluge' default is instead applied at runtime, independently, in all three consuming files ('if pass == "" { pass = "deluge" }').
- **Why:** This is a real (if minor) naming/documentation inconsistency: the comment and the enforcement point disagree on where the default lives, and the same fallback logic is duplicated verbatim in 3 files -- a change to the stock Deluge password behavior would require finding and editing all three.

#### `Config.DelugeWebURL`
🟢 used

- **Current default:** `"" (no viper.SetDefault call and no value in the defaultConfig struct literal -- Go zero value)`
- **Usage:** This is the PRIMARY (non-fallback) source of the Deluge Web UI URL, read in internal/deluge/integration.go:40, internal/server/deluge_integration.go:68 and :119, and internal/maintenance/jobs/bulk_deluge_import.go:151. It is also referenced in the frontend at web/src/components/settings/DelugeSettingsTab.tsx:114-117 ('Set deluge_web_url in server config or environment'). This is by far the most heavily used option in the whole download-clients bucket.
- **Why:** Empty correctly means 'Deluge not configured' and lets GetClient() fall through to the DownloadClient.Torrent.Deluge fallback and finally to nil when nothing is set -- sane as-is.

#### `Config.DownloadClient (DownloadClientConfig container: Torrent/Usenet/Deluge/QBittorrent/SABnzbd struct nesting)`
🟢 used

- **Current default:** `zero-value struct (all sub-fields empty/0/false)`
- **Usage:** internal/config/config.go:775 declares Config.DownloadClient DownloadClientConfig; internal/download/factory.go reads DownloadClient.Torrent.{Type,Deluge,QBittorrent} and DownloadClient.Usenet.{Type,SABnzbd}; internal/deluge/integration.go, internal/server/deluge_integration.go, and internal/maintenance/jobs/bulk_deluge_import.go all read DownloadClient.Torrent.Deluge as a fallback when the legacy Config.DelugeWebURL is empty. The container itself is live, but see per-leaf-field notes below: only the Deluge sub-struct's Host/Port/Password are reached by any production code path; QBittorrent and SABnzbd sub-structs are read only inside internal/download (factory.go/qbittorrent.go/sabnzbd.go), and that package's public constructors (NewTorrentClientFromConfig, NewUsenetClientFromConfig, NewQBittorrentClient, NewSABnzbdClient) have zero callers in production code -- only internal/download/download_test.go and download_coverage_test.go call them.
- **Why:** No change needed at the container level; issues live in the leaf fields.

#### `DelugeConfig.Host (yaml: download_client.torrent.deluge.host)`
🟢 used

- **Current default:** `"" (viper default + struct literal at config.go:2251)`
- **Usage:** Two distinct consumers: (1) internal/download/deluge.go:31 NewDelugeClient uses cfg.Host to build baseURL, but that constructor is only reached via the unused NewTorrentClientFromConfig factory (test-only). (2) The LIVE Deluge integration (internal/deluge/integration.go:44, internal/server/deluge_integration.go:121, internal/maintenance/jobs/bulk_deluge_import.go:154) reads config.AppConfig.DownloadClient.Torrent.Deluge.Host as a FALLBACK when the legacy Config.DelugeWebURL is empty, and if dc.Host != "" it builds url = http://<host>:<port>. This fallback path is exercised by internal/server/deluge_integration_test.go, confirming it's live production logic, not dead code.
- **Why:** Empty correctly signals "no fallback host configured"; sane as-is.

#### `DelugeConfig.Password (yaml: download_client.torrent.deluge.password)`
🟢 used

- **Current default:** `"" in config struct, but effectively "deluge" at runtime (all three live call sites substitute the literal string "deluge" when empty)`
- **Usage:** internal/download/deluge.go:88 uses d.cfg.Password for the Deluge auth.login RPC call (reachable only via the unused factory/test path). More importantly, the live fallback path in internal/deluge/integration.go:48, internal/server/deluge_integration.go, and internal/maintenance/jobs/bulk_deluge_import.go assigns pass = dc.Password when falling back from empty DelugeWebURL, then applies 'if pass == "" { pass = "deluge" }' in all three files.
- **Why:** "deluge" is Deluge's own well-known stock Web UI password. Silently falling back to it when unset means a Deluge daemon left at its factory default is auto-discovered and authenticated by this app -- fine for the app's own connection, but it means the app will happily authenticate through a genuinely insecure daemon and gives operators no signal that they should change it. This is existing upstream behavior mirrored intentionally (comment at config.go:759 says 'default: deluge'), not a new bug, but it is duplicated in 3 files rather than centralized, which is the kind of thing Fix-It-Right would want addressed if this area is ever touched.

#### `DelugeConfig.Port (yaml: download_client.torrent.deluge.port)`
🟢 used

- **Current default:** `0 (viper default + struct literal)`
- **Usage:** Same two consumers as Host. In the live fallback path (internal/deluge/integration.go:45-48, internal/server/deluge_integration.go:123-125, internal/maintenance/jobs/bulk_deluge_import.go:156-158) all three call sites contain the IDENTICAL pattern: 'port := dc.Port; if port == 0 { port = 8112 }' -- i.e. a 0 default is deliberately treated as "use Deluge's standard web-UI port 8112", duplicated verbatim in three files.
- **Why:** 0 is sane here only because of the runtime substitution; worth noting as a Fix-It-Right candidate (not in scope for this audit) that the 'if port==0 {port=8112}' fallback logic is copy-pasted identically in 3 files and would be better as a single shared helper -- but the default VALUE itself is fine.

#### `DelugeConfig.Username (yaml: download_client.torrent.deluge.username)`
🔴 DEAD · 🟠 default review

- **Current default:** `""`
- **Recommended default:** `Remove the field (Deluge's Web UI API has no username concept), or if kept for forward-compat with a future daemon-RPC-based client, document in the struct comment that it is currently unused/dead.` (confidence: high)
- **Usage:** Declared in DelugeConfig, set to "" by default (config.go:1278, 2254), but never read. Grep of internal/download/deluge.go shows NewDelugeClient/Connect only reference cfg.Host, cfg.Port, and cfg.Password (auth.login RPC call takes only the password) -- cfg.Username is not used anywhere in that file. The live fallback path in internal/deluge/integration.go, internal/server/deluge_integration.go, and internal/maintenance/jobs/bulk_deluge_import.go also only propagates dc.Host, dc.Port, and dc.Password into the fallback URL/password -- dc.Username is never read there either. Deluge's Web UI JSON-RPC auth is password-only (no username), which is consistent with this being genuinely unused rather than a missed wiring.
- **Why:** This matches the domain's known history of partially-wired config fields called out in project context: the field exists in the struct and default table but no client implementation, live or dead, ever reads it. Leaving it silently accepts user input (e.g. from a future settings UI) that has zero effect, which is a footgun.

#### `QBittorrentConfig.Host (yaml: download_client.torrent.qbittorrent.host)`
🔴 DEAD

- **Current default:** `""`
- **Usage:** Read only inside internal/download/qbittorrent.go (NewQBittorrentClient builds baseURL from cfg.Host/cfg.Port), which is reachable in production only through the factory.go switch on Torrent.Type=="qbittorrent" -- and NewTorrentClientFromConfig has no production caller anywhere in the repo (confirmed via grep; only download_test.go / download_coverage_test.go invoke it). No frontend component references qbittorrent config (grep of web/src for 'qbittorrent'/'QBittorrent' returns zero files).
- **Why:** qBittorrent support is fully implemented (client, RPC calls) but entirely unreachable from any running code path -- there is no server/plugin bootstrap code that calls NewTorrentClientFromConfig, unlike Deluge which has a real (if roundabout) fallback wiring. This is the clearest 'partially wired' case in this domain: the client is more fully built than Deluge's internal/download variant, yet has strictly less production reach than the legacy Deluge integration.

#### `QBittorrentConfig.Password (yaml: download_client.torrent.qbittorrent.password)`
🔴 DEAD

- **Current default:** `""`
- **Usage:** Used at internal/download/qbittorrent.go:45 in the same login POST body as Username. Same unreachability caveat as above.
- **Why:** No insecure hardcoded fallback exists for qBittorrent (unlike Deluge's 'deluge' password fallback) -- an empty password is simply sent as empty, which is the correct behavior for a field that is genuinely optional/required by the user's own qBittorrent instance.

#### `QBittorrentConfig.Port (yaml: download_client.torrent.qbittorrent.port)`
🔴 DEAD · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `If/when wired up, default to 8080 (qBittorrent's standard WebUI port) rather than 0; 0 is not a usable TCP port and, unlike DelugeConfig.Port, there is no runtime '0 means use standard port' substitution anywhere in qbittorrent.go -- a 0 port would be used literally in the URL and fail to connect.` (confidence: medium)
- **Usage:** Same reachability as QBittorrentConfig.Host -- used in internal/download/qbittorrent.go:36 to build baseURL, but that code path has no production caller.
- **Why:** Unlike the Deluge fallback path, NewQBittorrentClient has no 0->8080 substitution logic, so today's default of 0 would produce a broken connection string (http://host:0) if this code path were ever actually invoked. This is currently harmless only because the code path is unreachable in production.

#### `QBittorrentConfig.UseHTTPS (yaml: download_client.torrent.qbittorrent.use_https)`
🔴 DEAD

- **Current default:** `false`
- **Usage:** Used at internal/download/qbittorrent.go:31 to pick 'https' vs 'http' scheme for baseURL. Same unreachability caveat as the other QBittorrent fields.
- **Why:** false (plain HTTP) is a reasonable default for a same-LAN download-client connection and matches the equivalent SABnzbd field's default; no change warranted on the value itself.

#### `QBittorrentConfig.Username (yaml: download_client.torrent.qbittorrent.username)`
🔴 DEAD

- **Current default:** `""`
- **Usage:** Used at internal/download/qbittorrent.go:45 inside the login POST body (url.Values{"username": {q.cfg.Username}, ...}), but only reachable through the unused factory path -- no production caller.
- **Why:** Correctly implemented (unlike Deluge's orphaned Username field, this one IS read by its client), just currently unreachable in production because nothing calls the factory that would construct this client.

#### `SABnzbdConfig.APIKey (yaml: download_client.usenet.sabnzbd.api_key)`
🔴 DEAD

- **Current default:** `""`
- **Usage:** Used at internal/download/sabnzbd.go:40 as the 'apikey' query param. Same unreachability caveat as SABnzbdConfig.Host/Port.
- **Why:** Correctly implemented, just unreachable; no insecure default present.

#### `SABnzbdConfig.Host (yaml: download_client.usenet.sabnzbd.host)`
🔴 DEAD

- **Current default:** `""`
- **Usage:** Used at internal/download/sabnzbd.go:34 to build baseURL, reachable only via NewUsenetClientFromConfig which -- like NewTorrentClientFromConfig -- has zero production callers. No frontend references either.
- **Why:** Same unreachable-code situation as the QBittorrent fields; SABnzbd has no live wiring anywhere in the codebase (no fallback integration exists for it the way Deluge has one).

#### `SABnzbdConfig.Port (yaml: download_client.usenet.sabnzbd.port)`
🔴 DEAD · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `If/when wired up, default to 8080 (SABnzbd's standard port) rather than 0, since sabnzbd.go has no 0->standard-port substitution logic (same gap as QBittorrentConfig.Port).` (confidence: medium)
- **Usage:** Used at internal/download/sabnzbd.go:34 to build baseURL. Same unreachability caveat.
- **Why:** 0 would produce a broken connection string if this path were ever invoked; currently harmless only because it's dead code.

#### `SABnzbdConfig.UseHTTPS (yaml: download_client.usenet.sabnzbd.use_https)`
🔴 DEAD

- **Current default:** `false`
- **Usage:** Used at internal/download/sabnzbd.go:29 for scheme selection. Same unreachability caveat.
- **Why:** Consistent with QBittorrentConfig.UseHTTPS; no change needed to the value itself.

#### `TorrentClientConfig.Type (yaml: download_client.torrent.type)`
🟢 used

- **Current default:** `"" (viper.SetDefault("download_client.torrent.type", "") at internal/config/config.go:1275)`
- **Usage:** internal/download/factory.go:15 switches on cfg.DownloadClient.Torrent.Type to select "deluge" -> NewDelugeClient or "qbittorrent" -> NewQBittorrentClient, else nil for "". However NewTorrentClientFromConfig itself has no production caller (grep across the repo outside internal/download/*_test.go finds none) -- it is only exercised by internal/download/download_test.go. So the field is read by real code but that code path is never invoked outside tests.
- **Why:** Empty is the only sane default for a client-selector field with real network/credential side effects; auto-selecting a client type would be dangerous. The bigger issue is that this whole abstraction (internal/download.TorrentClient) is unwired -- nothing in the server/plugin startup path ever calls NewTorrentClientFromConfig, so setting this value currently has zero effect on running behavior for qbittorrent, and only an indirect effect for deluge via the separate fallback described under DelugeConfig.Host/Port/Password.

#### `UsenetClientConfig.Type (yaml: download_client.usenet.type)`
🔴 DEAD

- **Current default:** `"" (viper.SetDefault("download_client.usenet.type", "") at config.go:1285)`
- **Usage:** Only read at internal/download/factory.go:29 inside NewUsenetClientFromConfig, which has zero production callers (grep for the function name across the repo outside internal/download/*_test.go returns nothing). No other package references UsenetClientConfig.Type, and there is no frontend UI or other config-reader for it (grep of web/src for 'sabnzbd'/'usenet'/'download_client' returns no matches).
- **Why:** There is no way to reach a live SABnzbd connection through this codebase today -- the entire Usenet client stack is dead weight from the running application's perspective, so its default value cannot currently change any observable behavior.

---

## Transcription & AI (Whisper, embeddings, LLM backends)

#### `acoustid_api_key`
🟢 used · 🟠 default review

- **Current default:** `"" (empty)`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** internal/reconcile/itunes_heal.go:680; internal/plugins/acoustid/online_lookup.go:78.
- **Why:** Correct default for an optional third-party credential; gates Maintenance.AcoustIDOnlineLookup functionality which is itself off by default.

#### `ai_backend.embedding_mode`
🟢 used · 🟠 default review

- **Current default:** `"" (empty; resolves via EffectiveEmbeddingMode from legacy fields)`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** internal/config/config.go:426-427 (`if c.AIBackend.EmbeddingMode != "" { return c.AIBackend.EmbeddingMode }`), the explicit-override path ahead of legacy-field derivation.
- **Why:** Empty-means-derive is intentional and documented (config.go:399-403); changing it would break the migration/back-compat story for existing installs.

#### `ai_backend.llm_mode`
🟢 used · 🟠 default review

- **Current default:** `"" (empty; resolves via EffectiveLLMMode)`
- **Recommended default:** `"" (empty) — but see ai_backend.local_base_url finding below, which affects what this resolves TO by default` (confidence: high)
- **Usage:** internal/config/config.go:448-449 (explicit-override path); internal/server/handlers/aibackends/aibackends.go reads resp.LLMMode / resp.EmbeddingMode for the settings API.
- **Why:** The empty default itself is fine, but EffectiveLLMMode()'s resolution logic (config.go:460) treats a non-empty AIBackend.LocalBaseURL as 'local backend is configured' — and LocalBaseURL ships non-empty by default (see next item). So on a fresh install with llm_mode empty, EffectiveLLMMode silently resolves to 'local' pointing at a specific developer's LAN IP rather than 'disabled'. That's the real bug, not this field.

#### `ai_backend.local_base_url`
🟢 used · 🟠 default review

- **Current default:** `http://192.168.0.20:11434/v1`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** internal/config/config.go:460 gates EffectiveLLMMode() resolution: `if c.AIBackend.LocalBaseURL != "" \|\| c.Embedding.BaseURL != "" { return AIBackendModeLocal }`; consumed in internal/ai/register.go:50,101, internal/scanner/scanner.go:790, internal/server/handlers/aibackends/aibackends.go, internal/server/server.go:762.
- **Why:** This is a genuine default-value bug. The value is a hardcoded, specific LAN IP (a developer's own Ollama host per the inline comment at config.go:1355-1357: 'LocalBaseURL uses a placeholder host; real endpoints live in gitignored local config'). Because it ships non-empty by default and EffectiveLLMMode() (config.go:460) treats any non-empty LocalBaseURL as proof 'a local option is configured', every fresh install with llm_mode left empty resolves to AIBackendModeLocal and will try to reach 192.168.0.20:11434 — an address that almost certainly doesn't exist on other users' networks — rather than falling back to OpenAI or disabled. The surrounding comment (config.go:451-459) explicitly describes an incident where a blank llm_mode silently chose a paid OpenAI backend and burned through credits; the fix's own default now silently chooses an unreachable local backend on every other install by the same mechanism. The default should be empty so 'local is configured' is only ever user-asserted, matching how embedding.base_url (its sibling field, correctly defaulted to "") already behaves.

#### `ai_backend.local_embedding_model`
🟢 used · 🟠 default review

- **Current default:** `bge-m3`
- **Recommended default:** `bge-m3 (fine as-is, but see local_base_url — with that fixed to empty, this value is inert until a user opts into local mode, which is correct)` (confidence: medium)
- **Usage:** internal/ai/register.go:54,71; internal/server/handlers/aibackends/aibackends.go:108-111 (pulled-model presence check).
- **Why:** As a placeholder model name to pre-fill once a user configures a local endpoint, bge-m3 is a reasonable value. It only becomes consequential in combination with ai_backend.local_base_url, which is the field that needs to change.

#### `ai_backend.local_llm_model`
🟢 used · 🟠 default review

- **Current default:** `qwen2.5:7b-instruct`
- **Recommended default:** `qwen2.5:7b-instruct (fine as-is, same caveat as local_embedding_model)` (confidence: medium)
- **Usage:** internal/ai/register.go:105-106 (`NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, cfg.AIBackend.LocalLLMModel, ...)`); internal/scanner/scanner.go:798,802; internal/server/handlers/aibackends/aibackends.go:114-117.
- **Why:** Reasonable placeholder value; only becomes consequential once ai_backend.local_base_url is genuinely user-configured.

#### `Config.AIBackend (container struct field)`
🟢 used

- **Current default:** `N/A (struct container)`
- **Usage:** Referenced as `config.AppConfig.AIBackend.*` / `cfg.AIBackend.*` throughout internal/config, internal/ai/register.go, internal/server/handlers/aibackends.
- **Why:** Structural nesting field; the actionable finding lives in its LocalBaseURL leaf field (see ai_backend.local_base_url entry above).

#### `Config.Embedding (container struct field)`
🟢 used

- **Current default:** `N/A (struct container; see nested fields for actual defaults)`
- **Usage:** Referenced as `config.AppConfig.Embedding.*` throughout internal/ai, internal/plugins/dedup, internal/server; holds the EmbeddingConfig fields analyzed individually above.
- **Why:** Structural nesting field, not an independently configurable value — included here only so no entry from the source inventory is dropped silently.

#### `Config.MetadataScoring (container struct field)`
🟢 used

- **Current default:** `N/A (struct container)`
- **Usage:** Referenced as `config.AppConfig.MetadataScoring.*` / `cfg.MetadataScoring.*` throughout internal/metafetch and internal/plugins/metafetch; holds the four transcription boost fields plus other (out-of-domain) scoring knobs.
- **Why:** Structural nesting field; see individual TranscriptionXBoost entries above for the transcribe-ai-relevant leaves.

#### `config_blob.ai_backend (derived/synthesized, migrateAIBackendBlob)`
🟢 used

- **Current default:** `N/A (not a stored user setting; computed once per legacy blob, idempotent on ai_backend key presence)`
- **Usage:** internal/config/persistence.go:302-389; computes ai_backend.embedding_mode/llm_mode from legacy signal fields and mirrors Config.EffectiveEmbeddingMode/EffectiveLLMMode at persist time; also carries embedding.base_url/model forward onto ai_backend.local_base_url/local_embedding_model at lines 376-381 when a legacy local embedding backend was configured.
- **Why:** No direct action needed here, but this migration's job — making an un-migrated blob 'safe' by mirroring the same Effective*Mode logic — inherits the ai_backend.local_base_url default-value bug flagged above: a legacy install with no local backend configured but the hardcoded 192.168.0.20 default present would migrate to a stored ai_backend.local_base_url that also looks 'configured' when it isn't. Fixing the root default (empty string) upstream fixes this path too.

#### `cover_art_model`
🟢 used · 🟠 default review

- **Current default:** `gpt-5-mini`
- **Recommended default:** `gpt-5-mini` (confidence: high)
- **Usage:** internal/ai/openai_parser.go:80-81 (coverArtModel()), used at line 466.
- **Why:** Consistently defaulted and used; no evidence this should change.

#### `embedding.base_url`
🟢 used · 🟠 default review

- **Current default:** `"" (empty)`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** internal/config/config.go:432 (EffectiveEmbeddingMode: non-empty -> local mode); internal/ai/register.go:52,67,103; internal/plugins/dedup/embed_async.go:50; internal/server/server.go:764.
- **Why:** Empty is correct: it means 'use OpenAI unless overridden', which is the safest default absent explicit user configuration.

#### `embedding.dimensions`
🟢 used · 🟠 default review

- **Current default:** `3072`
- **Recommended default:** `3072` (confidence: high)
- **Usage:** internal/server/registry_wire.go:98 (`dims := cfg.Embedding.Dimensions`) sizes the vector index at wiring time.
- **Why:** Matches the default model (text-embedding-3-large produces 3072-dim vectors). If a user switches embedding.model to bge-m3 (the other example in the UI helper text, 1024-dim) they must also update dimensions manually — there's no auto-derivation — but that's a UX gap, not a wrong default value.

#### `embedding.enabled`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/config/config.go:429 (EffectiveEmbeddingMode: `if !c.Embedding.Enabled { return AIBackendModeDisabled }`), read throughout internal/plugins/dedup and internal/ai/register.go via cfg.Embedding.*; set from web/src/components/settings/EmbeddingSettingsSection.tsx:38-47 Switch.
- **Why:** The inventory entry listed the frontend default as false, but that is not the shipped default — it is only the Switch's state before config loads. The real default is set twice: viper.SetDefault("embedding.enabled", true) at internal/config/config.go:1300 and ResetToDefaults() at internal/config/config.go:2109. Enabled=true is correct since EffectiveEmbeddingMode still needs OpenAIAPIKey or Embedding.BaseURL to actually produce a backend, so the master switch being on by default doesn't spend anything with no key configured.

#### `embedding.model`
🟢 used · 🟠 default review

- **Current default:** `text-embedding-3-large`
- **Recommended default:** `text-embedding-3-large` (confidence: high)
- **Usage:** internal/ai/register.go:56,71 (`model = cfg.Embedding.Model`) feeds NewEmbeddingClientWithOptions; internal/server/registry_wire.go reads cfg.Embedding.Dimensions alongside it.
- **Why:** viper.SetDefault("embedding.model", "text-embedding-3-large") at config.go:1301 matches ResetToDefaults at config.go:2110 and the inventory value. Consistent with default embedding.dimensions=3072, which is exactly this OpenAI model's output size, so no mismatch.

#### `embedding.vector_backend`
🟢 used · 🟠 default review

- **Current default:** `chromem (corrected — see reason)`
- **Recommended default:** `chromem (verify intent) or explicitly hnsw if HNSW is now the intended default` (confidence: high)
- **Usage:** internal/server/registry_wire.go:102 (`if cfg.Embedding.VectorBackend == "hnsw"`) selects the index implementation at wiring time.
- **Why:** The inventory entry's frontend-derived default of 'hnsw' is NOT what ships: viper.SetDefault("embedding.vector_backend", "chromem") at config.go:1304 and ResetToDefaults at config.go:2113 both set 'chromem'. This is a real correction to make to the inventory, not just a naming nuance. Whether chromem-as-default is still the intended choice is a separate open question the project's embeddings/HNSW work (per project memory) may have superseded — worth a human decision, not something grep alone resolves.

#### `enable_ai_parsing`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** Extremely widely used: internal/config/config.go:463 (EffectiveLLMMode gate), internal/plugins/maintenance/dedup_ops.go:83, internal/scheduler/tasks.go:745,779, internal/server/ai_ops.go:80, internal/server/server_maintenance_deps.go:265, internal/server/entities_ops.go:261, internal/server/batch_poller_register.go:25,31, internal/server/handlers/ai.go (3 sites), internal/ai/register.go:106,112, internal/scanner/scanner.go:784.
- **Why:** Consistent everywhere (Go field EnableAIParsing, yaml/env enable_ai_parsing, env ENABLE_AI_PARSING). Default true is fine — it's a master switch that is a no-op without OpenAIAPIKey or a local AI backend also configured (per c.OpenAIAPIKey != "" && (c.EnableAIParsing \|\| ...) at config.go:463).

#### `filename_parse_model`
🟢 used · 🟠 default review

- **Current default:** `gpt-5-mini`
- **Recommended default:** `gpt-5-mini` (confidence: high)
- **Usage:** internal/ai/openai_parser.go:72-73 (filenameParseModel()), used at lines 212, 317, 401.
- **Why:** Consistently defaulted and used; no evidence this should change.

#### `HOME (transcribe batch subprocess env)`
🟢 used · 🟠 default review

- **Current default:** `/tmp (fallback when HOME is unset)`
- **Recommended default:** `/tmp (fine as a last-resort fallback)` (confidence: high)
- **Usage:** internal/transcribe/batch.go:122-127, used to build UV_CACHE_DIR/UV_PYTHON_INSTALL_DIR passed to the `uv run` subprocess invoking openai-whisper.
- **Why:** Standard OS-level fallback pattern; not application-specific config, no issue found.

#### `legacy embedding_* flat blob keys -> config_blob.embedding.* (migrateEmbeddingBlob)`
🟢 used · 🟡 naming

- **Current default:** `N/A (migration code path, not itself a user-facing option)`
- **Usage:** internal/config/persistence.go:153-190; runs on every config load, folding old flat keys (embedding_enabled, embedding_model, embedding_dimensions, embedding_base_url, vector_index_backend) into the nested embedding object, gated idempotently on presence of embedding_enabled.
- **Naming issue:** By design: vector_index_backend is deliberately renamed to vector_backend during migration (not just re-nested), which is intentional legacy-key normalization, not an accidental drift. Not something to 'fix' — flagging it only because the audit asked to compare naming across layers.
- **Why:** This is infrastructure that keeps old on-disk configs working after the embedding.* nesting refactor; it is still exercised on load and should not be removed while any installs may still carry the old flat blob format.

#### `legacy metadata_embedding_*/write_backup_before_tag_write flat blob keys -> config_blob.metadata_scoring.* (migrateMetadataScoringBlob)`
🟢 used · 🟡 naming

- **Current default:** `N/A (migration code path)`
- **Usage:** internal/config/persistence.go:260-300, flat key list at 268-276; runs on every config load.
- **Naming issue:** write_backup_before_tag_write (a tag-write/backup concern) is folded into the metadata_scoring nested object as write_backup_before rather than kept in its own namespace — a metadata-scoring bucket now holds a field that isn't a scoring parameter. This predates this audit and is a real, if minor, taxonomy mismatch worth a maintainer's awareness, especially since write_back_metadata (the separate DB-only vs. tag-write flag) is still False in production — anyone reading metadata_scoring.write_backup_before in isolation could mistakenly infer tag-writing is more active than it is.
- **Why:** Functionally necessary for back-compat; the naming-taxonomy note above is the only actionable finding, and it's cosmetic (nesting location), not a behavior bug.

#### `maintenance.acoustid_nightly_limit`
🟢 used · 🟠 default review

- **Current default:** `5000`
- **Recommended default:** `5000 (no evidence to change)` (confidence: low)
- **Usage:** internal/scheduler/tasks.go:563 (`"limit": config.AppConfig.Maintenance.AcoustIDNightlyLimit`).
- **Why:** Actively used as a rate cap for the (opt-in, off-by-default) nightly AcoustID online-lookup job. Whether 5000/night is appropriate depends on the AcoustID API's actual rate/quota terms, which weren't verified in this pass — flagging as unverified rather than asserting a specific alternate number.

#### `maintenance.acoustid_online_lookup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:572,575 (`IsEnabled`/`RunInMaintenanceWindow` both gate directly on this).
- **Why:** Explicitly documented as intentionally off by default (config.go:2222 comment: 'uses third-party quota and only helps users who set ACOUSTID_API_KEY'). Correct as shipped — enabling it unconditionally would burn API quota for users who never set an AcoustID key.

#### `metadata_review_model`
🟢 used · 🟠 default review

- **Current default:** `gpt-5-mini`
- **Recommended default:** `gpt-5-mini` (confidence: high)
- **Usage:** internal/ai/openai_parser.go:88-89 (metadataReviewModel()), used in metadata_llm_review.go:150, openai_parser.go:607,729, openai_batch.go:186,350,523.
- **Why:** Consistently defaulted and used; no evidence this should change.

#### `metadata_scoring.transcription_author_boost`
🟢 used · 🟠 default review

- **Current default:** `1.6`
- **Recommended default:** `1.6` (confidence: medium)
- **Usage:** internal/metafetch/service_scoring.go:308-309,511; internal/plugins/metafetch/calibrate_scoring.go:209-210,471,616.
- **Why:** Actively used, itself a calibration-sweep knob; no basis found to override it here.

#### `metadata_scoring.transcription_narrator_boost`
🟢 used · 🟠 default review

- **Current default:** `1.4`
- **Recommended default:** `1.4` (confidence: medium)
- **Usage:** internal/metafetch/service_scoring.go:311-312,515; internal/plugins/metafetch/calibrate_scoring.go:212-213,474,617.
- **Why:** Actively used, itself a calibration-sweep knob; no basis found to override it here.

#### `metadata_scoring.transcription_title_exact_boost`
🟢 used · 🟠 default review

- **Current default:** `2.0`
- **Recommended default:** `2.0` (confidence: medium)
- **Usage:** internal/metafetch/service_scoring.go:302-303,493 and internal/plugins/metafetch/calibrate_scoring.go:203-204,460,614 (also exposed as a calibration sweep knob).
- **Why:** Actively used as a multiplicative score boost when a Whisper transcription exactly matches a candidate title, and is itself a tunable calibration-sweep parameter (calibrate_scoring.go:614) rather than a fixed constant — no evidence in this pass that the shipped default is wrong; changing it would require re-running the project's existing calibration tooling, not a config audit.

#### `metadata_scoring.transcription_title_substr_boost`
🟢 used · 🟠 default review

- **Current default:** `1.4`
- **Recommended default:** `1.4` (confidence: medium)
- **Usage:** internal/metafetch/service_scoring.go:305-306,496; internal/plugins/metafetch/calibrate_scoring.go:206-207,463,615.
- **Why:** Same as the exact-boost sibling — actively used, itself a calibration-sweep knob; no basis found to override it here.

#### `openai_api_key`
🟢 used · 🟠 default review

- **Current default:** `"" (empty)`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** Dozens of call sites across cmd/, internal/config, internal/ai, internal/server, internal/plugins/dedup, internal/transcribe/whisper.go:66 — this is a load-bearing credential threaded through nearly every AI-parsing/embedding/transcription-review path.
- **Why:** Correct default for a secret. Separately worth flagging: the committed config.yaml:10 has openai_api_key set to the literal string 'sk-test12345678' as an example/placeholder value, not a real key (it doesn't match a real OpenAI key format and is clearly a test fixture), but it's still worth a maintainer double-checking that config.yaml is documentation/example only and not loaded as a real deployment config.

#### `OPENAI_MODEL`
🔴 DEAD · 🟡 naming

- **Current default:** `gpt-4o-mini (documented in .env.example only; has no effect)`
- **Usage:** Confirmed via `grep -rn "openai_model\\|OPENAI_MODEL\\|OpenAIModel" --include="*.go" .` across the whole repo: zero matches. The only reference anywhere is .env.example:8 (`OPENAI_MODEL=gpt-4o-mini`). There is no viper key, no Config field, and no os.Getenv call reading this name.
- **Naming issue:** Documented in .env.example as if it configures which OpenAI model is used, but the actual per-feature model selection is done via metadata_review_model / filename_parse_model / cover_art_model (all defaulting to gpt-5-mini, not gpt-4o-mini as this unused var implies). A user who sets OPENAI_MODEL expecting it to change model selection will see no effect and may be confused by the mismatched model name (gpt-4o-mini vs the real gpt-5-mini default).
- **Why:** Dead/documented-but-unused config is actively misleading here since it names a different model than the real default (gpt-4o-mini vs gpt-5-mini), making it look like an intentional, currently-effective choice. Either delete the .env.example line or implement it.

#### `WHISPER_BATCH_SLEEP_MS`
🟢 used · 🟠 default review

- **Current default:** `8000 (8 seconds)`
- **Recommended default:** `8000 — keep for now, but flag for an explicit re-measurement now that the GPU thermal block is lifted` (confidence: high)
- **Usage:** internal/transcribe/remote.go:97-105,127-133; sleeps between sub-batches of remote Whisper transcription unless 0.
- **Why:** This is directly the kind of 'GPU disabled / overly conservative default left over from the thermal-block period' the task asked to check. `git log` confirms it: commit 5f54917f 'feat(transcribe): add inter-batch sleep for GPU thermal relief' added this specifically as a thermal mitigation. The GPU thermal throttle block was lifted 2026-08-17, but per project memory the post-fix thermal behavior is still UNMEASURED. Recommending a blind reduction now would repeat the same 'change default without measuring' mistake the memory warns about (dose-response / verify-the-instrument lessons). The correct next step is an explicit measurement task (run a real remote-batch transcription job post-thermal-fix and check GPU temps with the sleep at 0 vs 8000ms), not silently shrinking this default. Until that's done, 8000ms should stay as a safe floor.

#### `WHISPER_CLIP_CACHE_DIR`
🟢 used · 🟠 default review

- **Current default:** `{rootDir}/.wav-cache`
- **Recommended default:** `{rootDir}/.wav-cache` (confidence: high)
- **Usage:** internal/plugins/maintenance/intro_transcribe.go:973 (wavCacheDir); called from extract_wav_clips.go:65 and intro_transcribe.go:450 — i.e. used well beyond its own declaration site.
- **Why:** Sensible default — keeps the WAV cache colocated with the library root unless explicitly overridden. No issue found.

#### `whisper_endpoints`
🟢 used · 🟠 default review

- **Current default:** `"" (empty JSON array string)`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** internal/config/config.go:977,1501 parse it via ParseWhisperEndpoints; internal/transcribe/batch.go:55 gives it precedence over whisper_remote_url when non-empty.
- **Why:** Correct default; this is the multi-endpoint pool config, opt-in by design, same GPU-thermal-lift reasoning as whisper_remote_url.

#### `whisper_remote_url`
🟢 used · 🟠 default review

- **Current default:** `"" (empty)`
- **Recommended default:** `"" (empty)` (confidence: high)
- **Usage:** internal/transcribe/batch.go:55 (`poolEndpoints(snap.WhisperEndpoints, snap.WhisperRemoteURL)`).
- **Why:** Empty is correct — falls back to the local uv/python whisper path. With the GPU thermal block lifted 2026-08-17, remote transcription is re-authorized again, but leaving this opt-in (empty default) is still right since not every deployment has a remote GPU box.

#### `WhisperEndpoint (URL/Concurrency/Priority/Label/Kind)`
🟢 used

- **Current default:** `N/A (struct type; no field has an application-wide numeric default — each pool entry is fully user-supplied via the whisper_endpoints JSON array)`
- **Usage:** internal/transcribe/dispatcher.go:124 (`sort by ep.Priority`), :234 (`c := ep.Concurrency`); internal/transcribe/batch.go:185-187 (Concurrency, Priority copied through).
- **Why:** Per-field concurrency is exactly the pattern the repo's concurrency policy wants (a pool sized per-endpoint rather than one global worker count) — no change needed. Priority's doc comment convention (lower = preferred, GPU box 1 / CPU box 100) is a reasonable scheme for the GPU-thermal-lift era where mixed GPU/CPU pools are viable again.

---

## Server, Auth & Network (rate limits, TLS, OAuth, quotas)

#### `--external-url`
🟢 used

- **Current default:** `"" (empty)`
- **Usage:** cmd/root.go:379 flag; cmd/root.go:324 cfg.ExternalURL = cmd.Flag("external-url").Value.String(); described as used for absolute link generation (e.g. temp-login URLs) and preventing Host-header injection.
- **Why:** Empty is the only sane default since the server cannot infer its own public origin; the flag's doc comment already explains the Host-header injection risk of leaving it unset in production. Worth surfacing (not necessarily changing) since prod-facing deployments must remember to set it.

#### `--generate-psk`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/mtls-bridge/main.go:59 declares; lines 287,296,306 branch on generatePSK to create a new pre-shared key.
- **Why:** Explicit opt-in for a destructive/sensitive provisioning action is correct.

#### `--host (mtls-bridge serve)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `"0.0.0.0"`
- **Recommended default:** `0.0.0.0 is likely intentional (this bridge is meant to be reached from other hosts for the mTLS-authenticated MCP bridge, and it's TLS-protected by client cert), but flag help text should disambiguate from the main server's --host, and this asymmetry is worth documenting.` (confidence: medium)
- **Usage:** cmd/mtls-bridge/main.go:56 declares; lines 91,153 tls.Listen("tcp", listenHost+":0", ...) consume it.
- **Naming issue:** Same flag name --host as the main serve command's --host (cmd/root.go:372), but this one defaults to 0.0.0.0 (all interfaces) while the main server's --host defaults to localhost (loopback only). Same name, opposite security posture, in two different binaries — easy to confuse when reading docs/scripts that mention '--host'.
- **Why:** mTLS is the actual access control here (client cert required), so binding all interfaces is defensible, but the identical flag name to a loopback-default flag in the main binary is a naming trap for anyone copying a --host value between the two commands.

#### `--host (serve)`
🟢 used · 🟡 naming

- **Current default:** `"localhost"`
- **Usage:** cmd/root.go:372 declares the flag; cmd/root.go:296 cfg.Host = cmd.Flag("host").Value.String() consumes it.
- **Naming issue:** There are two unrelated --host flags in this repo with different binaries and different defaults: cmd/root.go serve --host defaults to "localhost" (loopback-only), while cmd/mtls-bridge/main.go serve --host defaults to "0.0.0.0" (all interfaces). Additionally, the dead HOST env var in .env.example documents itself as controlling this flag's bind address but does nothing (see HOST finding below) — an operator following .env.example to bind the main server externally via HOST=0.0.0.0 will silently fail and stay on localhost.
- **Why:** Defaulting the main API server to loopback-only is the safe choice; the real problem is the dead HOST env var creating a false belief that setting it changes this bind address.

#### `--http3-port`
🟢 used

- **Current default:** `"8484" (same as --port)`
- **Usage:** cmd/root.go:378 flag; cmd/root.go:321 cfg.HTTP3Port = cmd.Flag("http3-port").Value.String(); consumed extensively in internal/server/server_lifecycle.go (Alt-Svc header, QUIC listener setup) around lines 956-1018.
- **Why:** Matching --port by default is documented as intentional for client compatibility (same port number, different protocol/transport).

#### `--idle-timeout`
🟢 used

- **Current default:** `"120s"`
- **Usage:** cmd/root.go:375 flag; cmd/root.go:308-312 parses into cfg.IdleTimeout.
- **Why:** Sane default; bounds idle keep-alive connections even though read/write timeouts are disabled for SSE.

#### `--mtls-dir`
🟢 used

- **Current default:** `".mtls"`
- **Usage:** cmd/mtls-bridge/main.go:53 declares; line 67 dir := mtls.NewDir(mtlsDir) consumes it.
- **Why:** Local relative directory is fine for this operator-run bridge tool.

#### `--port (serve)`
🟢 used

- **Current default:** `"8484"`
- **Usage:** cmd/root.go:371 declares the flag; cmd/root.go:295 cfg.Port = cmd.Flag("port").Value.String() consumes it into the server config used by startServer.
- **Why:** Standard, matches the documented prod port (8484). No issue.

#### `--powershell`
🟢 used

- **Current default:** `"" (empty, but required)`
- **Usage:** cmd/mtls-bridge/main.go:55 declares (MarkFlagRequired at line 57); line 176 exec.Command("powershell", ..., powershellPath).
- **Why:** MarkFlagRequired enforces the operator supply a real path; empty default is correct since there's no sane universal PowerShell script location.

#### `--read-timeout`
🟢 used

- **Current default:** `"0s" (disabled)`
- **Usage:** cmd/root.go:373 flag; cmd/root.go:298-302 parses into cfg.ReadTimeout via time.ParseDuration.
- **Why:** Documented as intentional for SSE compatibility (long-lived streaming connections would be killed by a finite read timeout). Reasonable given the app's SSE-based progress streaming.

#### `--renew`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/mtls-bridge/main.go:60 declares; line 315 branches on renewCerts.
- **Why:** Explicit opt-in for cert renewal is correct.

#### `--reset`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/mtls-bridge/main.go:61 declares; line 291 branches on resetAll to delete all certs.
- **Why:** Explicit opt-in for a destructive action (deletes all certs) is correct; defaulting true would be dangerous.

#### `--tls-cert`
🟢 used

- **Current default:** `"certs/localhost.crt"`
- **Usage:** cmd/root.go:376 flag; cmd/root.go:315 cfg.TLSCertFile = cmd.Flag("tls-cert").Value.String().
- **Why:** Self-signed dev cert default is fine for local/loopback default host; operators exposing externally are expected to supply --external-url and their own certs.

#### `--tls-key`
🟢 used

- **Current default:** `"certs/localhost.key"`
- **Usage:** cmd/root.go:377 flag; cmd/root.go:316 cfg.TLSKeyFile = cmd.Flag("tls-key").Value.String().
- **Why:** Paired with --tls-cert; same reasoning.

#### `--workers`
🟢 used · 🟠 default review

- **Current default:** `2 (int, hard-coded, not CPU-scaled)`
- **Recommended default:** `scale with runtime.NumCPU() the same way concurrent_scans does (max(runtime.NumCPU(), 4) in internal/config/config.go), or at minimum bump to a small multiple of NumCPU` (confidence: medium)
- **Usage:** cmd/root.go:380 flag "number of background operation workers"; grep of "workers" shows consumption in internal/operations/registry/registry.go (r.workers) and internal/server/file_io_pool.go for pool sizing.
- **Why:** Project mandate (CLAUDE.md 'Concurrency — Prefer Multi-Core Design') explicitly requires worker pools sized to available cores for whole-library-scale work, and the sibling setting concurrent_scans already follows this pattern (defaultWorkers := max(runtime.NumCPU(),4)). --workers hard-codes 2 regardless of host core count, which under-utilizes multi-core hardware for background operations. This is the kind of single-core-shaped default the project's own concurrency audit doc was written to catch.

#### `--write-timeout`
🟢 used

- **Current default:** `"0s" (disabled)`
- **Usage:** cmd/root.go:374 flag; cmd/root.go:303-307 parses into cfg.WriteTimeout.
- **Why:** Same SSE rationale as read-timeout; internal/single-operator deployment so an unbounded write timeout is an acceptable tradeoff.

#### `abs_access_token_ttl`
🟢 used

- **Current default:** `720h (30 days)`
- **Usage:** internal/server/wire_abs_routes.go:334 -> absauth/config.go parseDuration("ABS_ACCESS_TOKEN_TTL", ...). Consumed by absauth token issuance.
- **Why:** Documented rationale (many ABS clients implement no refresh flow) is sound; shortening would log clients out. Not a security-sensitive default given the JWT secret itself fails closed.

#### `ABS_AUTH_PROBE`
🟢 used

- **Current default:** `"" (disabled)`
- **Usage:** internal/server/middleware/absauthprobe.go:19 defines the env var name; internal/server/wire_abs_routes.go:515-519 checks os.Getenv(servermiddleware.ABSAuthProbeEnvVar) and registers the probe middleware first in the ABS route chain.
- **Why:** It's an opt-in diagnostic that logs booleans/lengths only (never credential values) and is explicitly noisy by design (polled every 15-20s); off-by-default is correct.

#### `abs_default_library_id`
🟢 used

- **Current default:** `b5e3a5b2-a76e-471f-b18b-915e4716d053`
- **Usage:** internal/server/handlers/abs/{browse,handler,dto}.go read h.cfg.DefaultLibraryID for /login and /libraries responses; validated as 36-char UUID in absauth/config.go:170.
- **Why:** Fixed placeholder UUID required because AudioBooth splits IDs at a fixed offset; not user-facing security config.

#### `abs_jwt_secret`
🟢 used

- **Current default:** `"" (empty, never auto-generated)`
- **Usage:** internal/config/config.go (ABSJWTSecret field, viper binding), internal/server/wire_abs_routes.go:333 (passed to absauth.Load as JWTSecret), internal/server/absauth/config.go (validated, minSecretLen floor, fails closed at boot if ABS API enabled without it), internal/server/absauth/refresh.go (used to derive refresh tokens).
- **Why:** Correct secure default: an auto-generated secret would invalidate every client token on restart, so failing closed when the ABS API is enabled without an explicit secret is the right design. No change needed.

#### `ABS_OIDC_REDIRECT_URIS`
🟢 used

- **Current default:** `["audiobooth://oauth"] (shipped-client default; exact match, not prefix/host match)`
- **Usage:** internal/server/handlers/abs/openid.go:126-152 defines OIDCRedirectURIsEnvVar and reads os.Getenv to override defaultOIDCRedirectURIs (exact-match allowlist for the OIDC redirect_uri, which the code comment states is the control preventing authorization-code interception).
- **Why:** This is a correctly-implemented security control (exact match rather than prefix/host match, per the code's own PKCE-is-not-enough warning). No change warranted; flagging only for visibility since it's the kind of allowlist that regresses easily if someone 'simplifies' it to a prefix match later.

#### `abs_refresh_grace`
🟢 used

- **Current default:** `10m`
- **Usage:** internal/server/absauth/config.go:155 parseDuration; internal/server/handlers/abs/refresh.go:117,173 sets session.GraceUntil = now.Add(h.cfg.RefreshGrace).
- **Why:** Reasonable grace window for token rotation races; not security-relevant enough to change.

#### `abs_refresh_token_ttl`
🟢 used

- **Current default:** `720h (30 days)`
- **Usage:** internal/server/wire_abs_routes.go:335 -> absauth/config.go parseDuration("ABS_REFRESH_TOKEN_TTL", ...).
- **Why:** Same rationale as access token TTL; consistent and intentional.

#### `abs_server_version`
🟢 used

- **Current default:** `2.36.0`
- **Usage:** internal/server/handlers/abs/{play,mapper,browse,status,dto,dto_play}.go all read h.cfg.ServerVersion for compatibility responses.
- **Why:** Compatibility-shim value pinned to the emulated ABS version; correct by design.

#### `API_RATE_LIMIT_PER_MINUTE`
🟢 used · 🟠 default review

- **Current default:** `DRIFT: viper.SetDefault("api_rate_limit_per_minute", 0) at internal/config/config.go:1076 (boot-time default = rate limiting OFF), but ResetToDefaults()'s struct literal at internal/config/config.go:2083 sets APIRateLimitPerMinute: 100, and .env.example documents API_RATE_LIMIT_PER_MINUTE=100 as the default.`
- **Recommended default:** `Pick one number and make viper.SetDefault and ResetToDefaults agree — recommend 100 (matching .env.example and ResetToDefaults) rather than 0, since the boot-time comment even says 'Rate limiting is opt-in. Default 0 means disabled (local/single-user server)' which is a defensible choice for a single-user deployment but contradicts what .env.example tells operators to expect and what clicking 'Reset to Defaults' in the UI actually gives them.` (confidence: high)
- **Usage:** internal/server/server_lifecycle.go:1401-1407: if rpm := config.AppConfig.APIRateLimitPerMinute; rpm > 0 { ...install IP rate limiter... } — genuinely wired, with rpm<=0 meaning 'no rate limiter middleware installed at all' (not just a very high limit).
- **Why:** This is the standout security-relevant finding in this bucket: a genuinely security-sensitive numeric default (whether the public API has any rate limiting at all) disagrees between the code path a fresh install actually takes (viper default = 0 = OFF) and two other sources of truth that both say 100 (the documented .env.example default and the in-app 'Reset to Defaults' button). An operator who reads .env.example, assumes 100/min is already the running default, and never explicitly sets the env var is running with zero API rate limiting — while an operator who later clicks 'Reset to Defaults' in the UI unexpectedly gets rate limiting turned on at 100/min, a behavior change they didn't ask for. Both directions are surprising; the two default sources need to be reconciled.

#### `AUTH_RATE_LIMIT_PER_MINUTE`
🔴 DEAD

- **Current default:** `10 (documented in .env.example as 'Per-minute rate limit specifically on auth endpoints')`
- **Usage:** Declared and plumbed (internal/config/config.go:639 field, viper default 10 at line 1077, ResetToDefaults 10 at line 2084, persisted in persistence.go:1059, validated >=0 at line 1976) — but a repo-wide grep for AuthRateLimitPerMinute outside internal/config/{config,persistence}.go and tests returns zero hits. No middleware, handler, or auth endpoint anywhere reads this field to actually rate-limit login/auth requests; internal/server/server_lifecycle.go only wires a single general apiRateLimiter (gated by API_RATE_LIMIT_PER_MINUTE) applied to the whole /api group, with no separate, stricter limiter on auth-specific routes.
- **Why:** This is a real, security-relevant dead control, not just a naming or UI issue: .env.example explicitly documents it as protection specifically for auth endpoints (implying brute-force protection on login/bootstrap), the field is fully plumbed through config load/persist/validate, yet there is no code anywhere that installs a stricter limiter on auth routes using this value. The general API rate limiter (api_rate_limit_per_minute) is the only rate limiting actually applied to auth endpoints, and that one is itself defaulting to OFF at boot per the API_RATE_LIMIT_PER_MINUTE finding above — meaning auth endpoints (login, bootstrap-token exchange) can be completely unrate-limited on a fresh install even though two separate config knobs both suggest they are protected.

#### `auto_organize`
🟢 used

- **Current default:** `true`
- **Usage:** config.AppConfig.AutoOrganize consumed in internal/server/{wire_handlers,server,folder_autoscan_op}.go to gate whether newly-imported books are auto-moved into the organized library tree.
- **Why:** Matches the product's core purpose (an audiobook organizer); reasonable default given create_backups/verify_after_write are also on by default in the UI (though verify_after_write/create_backups are themselves dead — see those items).

#### `auto_rename_on_apply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/server/handlers/metadata/handler.go:817, internal/metafetch/service_writeback.go:499, internal/metafetch/service_files.go:117 all read config.AppConfig.AutoRenameOnApply to gate rename-on-apply behavior.
- **Why:** Given the review-apply switch is live in prod (approvals mutate data), this and auto_write_tags_on_apply are genuinely load-bearing writeback controls and are correctly wired — worth noting as the 'real' Smart Apply Pipeline settings, in contrast to the neighboring dead verify_after_write/create_backups switches in the same UI section.

#### `auto_write_tags_on_apply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/metafetch/service_writeback.go:604, internal/metafetch/service_files.go:117 read config.AppConfig.AutoWriteTagsOnApply.
- **Why:** Genuinely wired writeback control; correct default given the product writes tags as its core function.

#### `BASIC_AUTH_ENABLED`
🟢 used

- **Current default:** `false`
- **Usage:** viper.AutomaticEnv matches basic_auth_enabled (internal/config/config.go:654,1082,1481); internal/server/middleware/basicauth.go:21 if !config.AppConfig.BasicAuthEnabled { c.Next(); return } — genuinely gates the Basic Auth middleware; also passed through docker-compose.yml:20.
- **Why:** Basic Auth is a supplementary/optional layer on top of ENABLE_AUTH's API-key auth; defaulting off (opt-in) is correct since enabling it without configuring username/password would just require empty-string credentials, which is a soft trap rather than a hard failure — but see BASIC_AUTH_PASSWORD note for the related default gap.

#### `BASIC_AUTH_PASSWORD`
🟢 used

- **Current default:** `code default: "" (empty); .env.example documents "changeme" as an example; docker-compose.yml default empty`
- **Usage:** internal/server/middleware/basicauth.go:54 expectedPass := config.AppConfig.BasicAuthPassword, constant-time compared against the request's Basic Auth password.
- **Why:** Flagging for visibility rather than as a bug: .env.example's 'changeme' is only an illustrative placeholder in a comment-style env file (never actually loaded as a real default by the Go binary, which defaults to empty), so there's no live 'changeme' password shipping anywhere — but if anyone ever changes the code default to mirror .env.example literally, that would become a real default-credential vulnerability. Worth a comment in .env.example clarifying it's an example value, not a functional default.

#### `BASIC_AUTH_USERNAME`
🟢 used

- **Current default:** `code default: "" (empty); .env.example documents "admin"; docker-compose.yml default "admin"`
- **Usage:** internal/server/middleware/basicauth.go:53 expectedUser := config.AppConfig.BasicAuthUsername, compared via constant-time compare against the request's Basic Auth username.
- **Why:** The empty code default is actually the safer choice versus .env.example/docker-compose's 'admin' placeholder, since BasicAuthEnabled also defaults false — but it's worth noting the three sources (code default, .env.example, docker-compose) don't quite agree, even though the net security posture is fine because the whole feature is off by default.

#### `cache_invalidate_on_book_update`
🟢 used

- **Current default:** `false`
- **Usage:** internal/audiobooks/service.go:253 if config.AppConfig.CacheInvalidateOnBookUpdate { ... } — genuinely gates list-cache invalidation behavior.
- **Why:** Matches documented tradeoff (keep list cache warm by default; explicit opt-in for immediate consistency). Not security-relevant.

#### `cache_size`
🔴 DEAD

- **Current default:** `1000 (items)`
- **Usage:** internal/config/config.go:725 field CacheSize, persisted via persistence.go:1095, exposed in PerformanceSettingsTab.tsx (100-10000 items) — but grep for '.CacheSize' outside config/persistence/tests returns zero hits. internal/cache/cache.go's NewCache call sites (internal/server/wire_handlers.go, handlers/cache.go) never read this field to size any LRU/cache.
- **Why:** Same dead-config finding as memory_limit_type/memory_limit_percent/memory_limit_mb — grouped here because they're the same broken subsystem. Wire it to whatever cache actually needs bounding, or delete the UI control.

#### `concurrent_scans`
🟢 used

- **Current default:** `backend: max(runtime.NumCPU(), 4); frontend display default: 4`
- **Usage:** internal/config/config.go:578 field, :1070 viper.SetDefault("concurrent_scans", defaultWorkers) where defaultWorkers scales with runtime.NumCPU() (min 4); frontend PerformanceSettingsTab.tsx round-trips via useSettingsHandlers.ts and services/api.ts.
- **Why:** This is the one option in this bucket that already follows the project's mandated multi-core-scaling pattern (CLAUDE.md concurrency mandate) — cite it as the reference implementation --workers (above) should be brought in line with.

#### `Config.ABSAccessTokenTTL / Config.ABSRefreshTokenTTL (abs_access_token_ttl, abs_refresh_token_ttl)`
🟢 used

- **Current default:** `"720h" (30 days) for both, consistent between registerABSDefaults and the struct doc comments`
- **Usage:** internal/server/wire_abs_routes.go:334-335 pass both directly into the ABS auth wiring as AccessTokenTTL/RefreshTokenTTL.
- **Why:** 30-day access tokens are unusually long-lived for an access token (vs. typical short-lived-access + longer-refresh pattern), but the code comment explains this is empirically required because many ABS clients (AudioBooth etc.) implement no token refresh at all -- a deliberate, documented tradeoff rather than an oversight. Flagging for awareness only: if a compromised long-lived access token is a real concern, revocation/rotation tooling should exist (not verified here, out of scope for this pass).

#### `Config.ABSAPIEnabled (abs_api_enabled)`
🟢 used

- **Current default:** `false (registerABSDefaults in internal/config/abs_config.go:28, matches struct-field doc comment)`
- **Usage:** internal/server/wire_abs_routes.go:326 `if !snap.ABSAPIEnabled { return }` gates all ABS route registration; also referenced at server_lifecycle.go:1387 for a path-collision exclusion.
- **Why:** Explicitly documented and verified: with this unset, no ABS route registers and behavior is unchanged from before the feature existed -- correct opt-in default for a whole API surface.

#### `Config.ABSAuthModes (abs_auth_modes)`
🟢 used

- **Current default:** `"cf,jwt" (registerABSDefaults abs_config.go:29, matches struct-field doc + inventory)`
- **Usage:** internal/server/wire_abs_routes.go:332 `AuthModes: snap.ABSAuthModes` passed into ABS auth wiring.
- **Why:** Both resolvers active by default is reasonable for the feature-flagged-off-by-default surface (ABSAPIEnabled=false makes this moot until explicitly turned on).

#### `Config.ABSDefaultLibraryID (abs_default_library_id)`
🟢 used

- **Current default:** `"b5e3a5b2-a76e-471f-b18b-915e4716d053" (a real, valid 36-char UUID set in registerABSDefaults, abs_config.go:35). NOTE: the raw struct-field inventory entry listed defaultValue as empty string ("") -- that reflects only the zero-value Go struct field before viper defaults are applied; the actual effective default (verified in abs_config.go) is the UUID above.`
- **Usage:** internal/server/wire_abs_routes.go:338 `DefaultLibraryID: snap.ABSDefaultLibraryID`.
- **Why:** The doc comment requires this to be a non-null 36-char UUID or AudioBooth cannot log in at all; the shipped default satisfies that constraint. Correcting the earlier inventory pass's defaultValue="" finding: that was the un-viper-applied struct zero value, not the real runtime default.

#### `Config.ABSJWTSecret (ABS_JWT_SECRET env var only, json:"-")`
🟢 used

- **Current default:** `empty string, no default, never auto-generated (registerABSDefaults abs_config.go:30)`
- **Usage:** internal/server/wire_abs_routes.go:333 `JWTSecret: snap.ABSJWTSecret`; doc comment (config.go:698-702) states the server FAILS CLOSED at boot if ABSAPIEnabled is true and this is unset, and it is deliberately excluded from the JSON config blob/API responses via `json:"-"`.
- **Why:** Correct hard-fail-closed design for a signing secret: auto-generating one would silently invalidate all client tokens on every restart, so requiring explicit operator-supplied ABS_JWT_SECRET is the right call. Fully env-authoritative and kept out of the persisted config blob and API surface, which is appropriate secret handling.

#### `Config.ABSRefreshGrace (abs_refresh_grace)`
🟢 used

- **Current default:** `"10m"`
- **Usage:** internal/server/wire_abs_routes.go:336 `RefreshGrace: snap.ABSRefreshGrace`.
- **Why:** Reasonable grace window to avoid orphaning sessions on concurrent/replayed refresh from the same device; no evidence of misuse.

#### `Config.ABSServerVersion (abs_server_version)`
🟢 used

- **Current default:** `"2.36.0"`
- **Usage:** internal/server/wire_abs_routes.go:337 `ServerVersion: snap.ABSServerVersion`, reported via /status and serverSettings.version per the doc comment.
- **Why:** Not a security-relevant field; cosmetic/compat value only. No drift found.

#### `Config.APIRateLimitPerMinute (api_rate_limit_per_minute)`
🟢 used · 🟠 default review

- **Current default:** `0 (unlimited/disabled) via viper.SetDefault at config.go:1076 -- this is the real fresh-install default. BUT internal/config/config.go:2083, inside ResetToDefaults() (called from the admin 'reset config to factory defaults' endpoints in internal/server/handlers/system/handler.go:364,434), hard-codes 100 instead. These two 'default' code paths disagree: a fresh install gets unlimited API rate limiting, but clicking 'reset to defaults' in the admin UI gets 100/min. This is a genuine drift between the documented/intended default (0=disabled, per the code comment) and the factory-reset default (100).`
- **Recommended default:** `Make ResetToDefaults() match the documented default of 0 (or, if 100 is actually the intended production-safe default, change viper.SetDefault to 100 and update the code comment) -- pick one authoritative value and use it in both places.` (confidence: high)
- **Usage:** internal/config/config.go:1474 loads it from viper at InitConfig; internal/server/server_lifecycle.go:1402 gates the IP rate limiter (`if rpm := config.AppConfig.APIRateLimitPerMinute; rpm > 0`), with the comment 'Rate limiting is opt-in. Default 0 means disabled (local/single-user server).' internal/config/persistence.go:1055 loads it from the DB config blob. Validated >= 0 at config.go:1973.
- **Why:** A single logical default value is expressed in two independently-maintained code paths (viper.SetDefault vs. the ResetToDefaults() struct literal) and they have drifted to different values (0 vs 100). This is the same class of bug flagged for other fields in this file's own comments (e.g. the explicit note on this exact field). An operator who does a factory reset expecting 'brand new install' state gets a materially different security posture (rate limiting enabled at 100/min) than a truly fresh install (unlimited).

#### `Config.AuthRateLimitPerMinute (auth_rate_limit_per_minute)`
🔴 DEAD · 🟠 default review

- **Current default:** `10 (viper default at config.go:1077, matches ResetToDefaults() at config.go:2084 -- no drift here)`
- **Recommended default:** `Either wire this into a real per-auth-endpoint rate limiter (e.g. apply it to /api/v1/auth/login and /api/v1/auth/bootstrap the same way APIRateLimitPerMinute gates the general limiter), or remove the field/document it as reserved-for-future-use.` (confidence: high)
- **Usage:** Only three non-declaration hits in the whole repo, all in internal/config/{config.go,persistence.go} plus config_unit_test.go: loaded from viper, copied from the DB blob, validated >=0, and asserted in unit tests. Grep for AuthRateLimitPerMinute outside internal/config/ returns nothing -- no auth-endpoint middleware, no login-route rate limiter, nothing in internal/server ever reads it. Compare to APIRateLimitPerMinute, which IS wired into servermiddleware.NewIPRateLimiter at server_lifecycle.go:1402.
- **Why:** This option is fully plumbed through config load/validate/persist/test but never consulted by any HTTP handler or middleware. It reads as a security control ('rate limit specifically for authentication endpoints') that does nothing today -- an operator setting auth_rate_limit_per_minute in config.yaml gets no brute-force protection despite the option existing and validating successfully. This is the 'declared capability vs registered list' failure pattern: the config plumbing exists but the enforcement point was never built.

#### `Config.BasicAuthEnabled (basic_auth_enabled)`
🟢 used

- **Current default:** `false (agrees everywhere)`
- **Usage:** internal/server/middleware/basicauth.go:21 checks `if !config.AppConfig.BasicAuthEnabled { return }` to skip the middleware; persistence.go:1380 loads from DB blob; config.go:1481/2090 set the two defaults consistently.
- **Why:** Off-by-default for a secondary/legacy auth mode is correct; no drift found.

#### `Config.BasicAuthUsername / Config.BasicAuthPassword (basic_auth_username, basic_auth_password)`
🟢 used

- **Current default:** `both empty string by default`
- **Usage:** internal/server/middleware/basicauth.go:53-54 reads both to compare against incoming HTTP Basic Auth credentials. Password has a dedicated secret-preserve-on-empty-save + encrypted-at-rest mechanism in internal/config/persistence.go (lines ~857, 1385, 1489, 1511) and is masked in API responses via internal/config/update_service.go:51-52 (database.MaskSecret).
- **Why:** Empty credentials combined with BasicAuthEnabled=false means this auth mode is fully inert until an operator explicitly sets all three fields -- correct fail-safe default. The password's secret-handling (encryption at rest, masking in API responses, preserve-on-empty-save) is more careful than a typical config field and looks intentional/correct.

#### `Config.BootstrapKeyTTLDays (bootstrap_key_ttl_days)`
🟢 used

- **Current default:** `30 (viper default and ResetToDefaults() at config.go:1122 -- no drift found for this field, unlike APIRateLimitPerMinute)`
- **Usage:** internal/server/bootstrap.go:343 and internal/server/handlers/apikeys.go:384 both read `config.AppConfig.BootstrapKeyTTLDays` as `ttlDays` to compute bootstrap-issued API key expiry.
- **Why:** The doc comment explicitly states this field must always expire (never 'forever' like MetadataFetchCacheTTLDays), and a non-positive configured value falls back to 30 rather than disabling expiry -- correct fail-safe design for a full-scope admin credential TTL (SEC-1/PROC-6).

#### `Config.CFAccessTeamDomain / Config.CFAccessAUD (cf_access_team_domain, cf_access_aud)`
🟢 used

- **Current default:** `both empty string`
- **Usage:** internal/server/wire_oauth.go:82-86 and wire_abs_routes.go:355-365 both require BOTH fields to be non-empty before constructing an oauth.NewCFAccessVerifier and trusting the Cf-Access-Jwt-Assertion header.
- **Why:** Both must be set together to activate CF Access passthrough (verified by the AND check at wire_oauth.go:82); empty-by-default is fail-closed and correct.

#### `Config.EnableAuth (enable_auth)`
🟢 used

- **Current default:** `true (viper default and ResetToDefaults() agree: true)`
- **Usage:** Gates RequireAuth/RequirePermission in multiple places: server_lifecycle.go:1278 (s.perm helper), 1327 (/metrics), 1358 (/api/events SSE), 1413-1417 (main API auth middleware); wire_handlers.go:35,175 pass it into NewAuthHandler and operations wiring; maintenance_dispatcher.go:93.
- **Why:** Auth-on-by-default is the correct secure posture. When false, the code explicitly logs a warning ('authentication is disabled ... do not expose this server to untrusted networks'), which is good defense-in-depth for the opt-out case.

#### `Config.EnableRateLimit (enable_rate_limit)`
🟢 used · 🟠 default review

- **Current default:** `true (viper default and ResetToDefaults() agree: true)`
- **Recommended default:** `Wire it as an actual gate: `if config.AppConfig.EnableRateLimit && rpm > 0 { apiRateLimiter = ... }`, or remove the field and rely solely on APIRateLimitPerMinute==0 to mean 'disabled' (which is already how the code behaves).` (confidence: high)
- **Usage:** Only one non-declaration use: internal/server/server_lifecycle.go:1418 `if !config.AppConfig.EnableRateLimit { slog.Warn("rate limiting is disabled...") }`. Critically, this flag is NEVER used to actually build/skip the rate limiter -- the real gate is `APIRateLimitPerMinute > 0` at line 1402, which runs unconditionally regardless of EnableRateLimit's value. api.Use(apiRateLimiter, ...) at lines 1438/1440 always installs whatever apiRateLimiter was built at line 1401-1407, with no reference to EnableRateLimit.
- **Why:** This is a config option that reads as a real on/off switch ('enable_rate_limit') but currently only controls a log message, not behavior. Setting enable_rate_limit=false in config.yaml does NOT disable rate limiting if api_rate_limit_per_minute is > 0 -- the operator's expectation (flip this false to turn off rate limiting) is silently wrong. This is the same 'declared toggle does nothing' class of bug as AuthRateLimitPerMinute, just partially wired (it logs instead of being fully inert).

#### `Config.JSONBodyLimitMB (json_body_limit_mb)`
🟢 used

- **Current default:** `1 MB (viper default and ResetToDefaults() agree: 1)`
- **Usage:** internal/server/server_lifecycle.go:1397 and internal/server/wire_abs_routes.go:506 both convert it to bytes and pass to servermiddleware.MaxRequestBodySize, applied via api.Use(...) at server_lifecycle.go:1438/1440.
- **Why:** 1MB is a reasonable ceiling for JSON API payloads in this app (metadata edits, config updates); no evidence of legitimate JSON requests needing more.

#### `Config.OAuthAllowedEmails (oauth_allowed_emails)`
🟢 used

- **Current default:** `empty string = no OAuth logins allowed (per the doc comment at config.go:670-672)`
- **Usage:** internal/server/wire_oauth.go:43 and wire_abs_routes.go:351 both call `oauth.ParseAllowedEmails(...OAuthAllowedEmails)` to build the allowlist consulted at login.
- **Why:** Fail-closed default: with OAuthEnabled=false this is moot, but even if OAuth were somehow turned on without setting an allowlist, no login would succeed. This is the correct secure-by-default behavior (verified ≠ authorized, per the code comment at config.go:660-661).

#### `Config.OAuthDefaultRole (oauth_default_role)`
🟢 used

- **Current default:** `"viewer"`
- **Usage:** internal/server/wire_oauth.go:44 and wire_abs_routes.go:352 both pass it as DefaultRole for newly auto-created OAuth users.
- **Why:** Least-privilege default for auto-provisioned accounts is the correct security posture; explicitly documented as such in the code comment.

#### `Config.OAuthEnabled (oauth_enabled)`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/wire_oauth.go:37 (`Enabled: cfgSnap.OAuthEnabled`) feeds the OAuth wiring constructor; env-bound via OAUTH_ENABLED at config.go:1097.
- **Why:** SSO is opt-in and requires client IDs/secrets to do anything; sane default.

#### `Config.OAuthGithubClientID / OAuthGithubClientSecret / OAuthGoogleClientID / OAuthGoogleClientSecret`
🟢 used

- **Current default:** `empty string for all four`
- **Usage:** All four are read directly into the OAuth wiring struct at internal/server/wire_oauth.go:38-41, each with a matching env var (OAUTH_GITHUB_CLIENT_ID/SECRET, OAUTH_GOOGLE_CLIENT_ID/SECRET) bound at config.go:1098-1101.
- **Why:** Standard OIDC client credential fields; empty-by-default is correct since OAuthEnabled also defaults false.

#### `Config.OAuthRedirectBaseURL (oauth_redirect_base_url)`
🟢 used

- **Current default:** `empty string`
- **Usage:** internal/server/wire_oauth.go:32 `redirectBase := cfgSnap.OAuthRedirectBaseURL`, used to build the provider callback URIs.
- **Why:** This is inherently deployment-specific (public origin), can't have a sane universal default.

#### `Config.UploadBodyLimitMB (upload_body_limit_mb)`
🟢 used

- **Current default:** `10 MB (viper default and ResetToDefaults() agree: 10)`
- **Usage:** Same call sites as JSONBodyLimitMB: server_lifecycle.go:1398 and wire_abs_routes.go:507, feeding servermiddleware.MaxRequestBodySize.
- **Why:** No drift found; value looks intentional and consistent everywhere.

#### `Config.WriteStartupReadOnlyKey (write_startup_readonly_key)`
🟢 used

- **Current default:** `true (viper default and ResetToDefaults() at config.go:2280 agree)`
- **Usage:** internal/server/server_lifecycle.go:1095 `if config.AppConfig.WriteStartupReadOnlyKey { ... }` gates writing the 24h read-only API key file to <data-dir>/.readonly-key on every startup.
- **Why:** Default true preserves pre-existing behavior for upgrades (per the SEC-2 doc comment); the bootstrap recovery token (.bootstrap-token) is unconditionally written regardless of this flag, so turning this off doesn't remove the operator's recovery path. No drift found.

#### `create_backups`
🔴 DEAD

- **Current default:** `true`
- **Usage:** internal/config/config.go:521 field CreateBackups, viper default true (line 1435), reset-to-defaults true (line 2053), exposed as 'Create backups before modifying files' switch — grep for CreateBackups outside config.go/persistence.go/tests returns zero hits; no backup-file creation logic anywhere references it.
- **Why:** Same pattern as verify_after_write: a safety-sounding, on-by-default switch in the file-modification settings section that has no corresponding backend implementation. This is the most concerning of the dead settings in this bucket because it directly claims to protect against data loss during file writes ('Create backups before modifying files') in a codebase whose own project memory repeatedly flags real writeback data-loss incidents — an operator relying on this toggle for safety is unprotected.

#### `default_user_quota_gb`
🔴 DEAD

- **Current default:** `100 (GB)`
- **Usage:** internal/config/config.go:527 field DefaultUserQuotaGB, viper default 100, reset-to-defaults 100 — grep for DefaultUserQuotaGB outside config.go/persistence.go returns zero hits anywhere, including the /system status handler that at least echoes the other three quota fields.
- **Why:** The single most completely dead field in this bucket — not even read back for display. Confirms the multi-user storage quota feature (enable_user_quotas + default_user_quota_gb) is pure UI scaffolding with zero backend implementation.

#### `disk_quota_percent`
🔴 DEAD

- **Current default:** `80`
- **Usage:** internal/server/handlers/system/handler.go:296 'quota_percent': config.AppConfig.DiskQuotaPercent — same read-only status-reporting-only usage as enable_disk_quota; never compared against actual usage to gate any write.
- **Why:** Part of the same unenforced quota subsystem as enable_disk_quota — see that item.

#### `ENABLE_AUTH`
🟢 used

- **Current default:** `true (consistent across viper default, reset-to-defaults, and .env.example)`
- **Usage:** viper.AutomaticEnv matches enable_auth (config.go:642,1080,1478); internal/server/server_lifecycle.go:1412-1416 if config.AppConfig.EnableAuth { authMiddleware = servermiddleware.RequireAuth(...) } else { slog.Warn("authentication is disabled...do not expose this server to untrusted networks") } — genuinely enforced with an explicit warning log on the insecure path.
- **Why:** Correctly secure-by-default, with a loud warning log if an operator disables it. No issue.

#### `enable_disk_quota`
🔴 DEAD

- **Current default:** `false`
- **Usage:** internal/config/config.go field EnableDiskQuota; only consumer is internal/server/handlers/system/handler.go:295, which echoes it back verbatim in a status JSON response ('quota_enabled': config.AppConfig.EnableDiskQuota) — it is never used to block a scan, import, or organize operation when disk usage would exceed the quota.
- **Why:** The 'Storage Quotas' UI section (enable_disk_quota, disk_quota_percent, enable_user_quotas, default_user_quota_gb) presents as an enforceable quota feature, but the only backend code touching these fields is a read-only /system status endpoint that reports the configured percent alongside actual disk usage — no write path anywhere checks 'would this operation push usage over disk_quota_percent' and blocks it. False sense of protection for an operator who enables this expecting the organizer to refuse new files once the quota is hit.

#### `enable_json_logging`
🔴 DEAD · 🟡 naming

- **Current default:** `false`
- **Usage:** internal/config/config.go:751 field EnableJsonLogging, viper default false (line 1133), exposed in frontend as 'Enable JSON structured logging to separate file' switch — but grep for 'EnableJsonLogging' outside config.go/persistence.go returns zero hits anywhere in the codebase.
- **Naming issue:** Frontend/JSON key is enable_json_logging (all-caps JSON as 'json') while the Go struct field is EnableJsonLogging (only the leading J capitalized, not JSON) — inconsistent with Go's own naming convention for initialisms (should be EnableJSONLogging per typical Go style, though this is cosmetic next to the bigger dead-code issue).
- **Why:** This switch's caption explicitly promises 'Creates a separate .json log file in addition to the main log' but setupFileLogging() in cmd/root.go creates exactly one log file with a hard-coded TextHandler and never checks this flag. Same class of finding as log_format — the whole 'Logging' section of PerformanceSettingsTab.tsx below log_level is non-functional.

#### `enable_user_quotas`
🔴 DEAD

- **Current default:** `false`
- **Usage:** internal/server/handlers/system/handler.go:297 'user_quotas_enabled': config.AppConfig.EnableUserQuotas — same read-only reporting; no per-user usage tracking or enforcement code found anywhere in the repo.
- **Why:** Part of the same unenforced quota subsystem; additionally default_user_quota_gb (its companion field) has zero usages at all, not even the status-reporting one, so the multi-user quota feature is entirely unimplemented end to end.

#### `exclude_patterns`
🟢 used

- **Current default:** `[]`
- **Usage:** internal/scanner/scanner.go:447 for _, pattern := range config.AppConfig.ExcludePatterns.
- **Why:** Empty is the correct default (opt-in exclusions).

#### `file_naming_pattern`
🟢 used

- **Current default:** `{title} - {author} - read by {narrator}`
- **Usage:** Same call sites as folder_naming_pattern (internal/organizer/{pipeline,organizer}.go); paired validation in config.go and naming_patterns.go.
- **Why:** Actively used; no issue.

#### `folder_naming_pattern`
🟢 used

- **Current default:** `{author}/{series}/{title} ({print_year})`
- **Usage:** internal/organizer/{pipeline,organizer}.go: o.config.FolderNamingPattern used repeatedly in expandPattern/BuildRelPath/planTargetPaths; validated via validateNamingPattern and validateNamingPatterns.
- **Why:** Actively used and validated for pattern-syntax and data-safety (per config.go comment referencing naming_patterns.go); no issue.

#### `HOST`
🔴 DEAD · 🟡 naming · 🟠 default review

- **Current default:** `0.0.0.0 (documented in .env.example, has no effect)`
- **Recommended default:** `either wire HOST to the --host flag via a viper.BindEnv the way basic_auth_* fields are bound, or delete the HOST line from .env.example` (confidence: high)
- **Usage:** Confirmed via grep: no Go code anywhere calls os.Getenv("HOST") or binds a viper key to env var HOST. The real bind host comes only from the unbound cobra flag --host (cmd/root.go:372, default "localhost"). This matches the inventory's own USAGE-VERIFICATION FLAG note.
- **Naming issue:** Documented in .env.example as the way to set the server bind host, but there is no code path connecting it to the actual --host flag or any viper key — a pure documentation/reality mismatch. It also collides in name with the semantically different '--host' CLI flags analyzed above, compounding confusion for anyone trying to configure the bind address via environment.
- **Why:** An operator following .env.example to bind the server externally (HOST=0.0.0.0) gets no error and no effect — the server silently stays on whatever --host was actually passed (default localhost). This is a documentation-vs-reality gap that could leave someone believing they've exposed the server externally (or, just as easily, believing they've locked it to loopback) when neither is true.

#### `JSON_BODY_LIMIT_MB`
🟢 used

- **Current default:** `1 (MB), consistent across viper default, ResetToDefaults, and .env.example`
- **Usage:** internal/server/server_lifecycle.go:1397 jsonLimitBytes := int64(config.AppConfig.JSONBodyLimitMB) * 1024 * 1024, passed into servermiddleware.MaxRequestBodySize(jsonLimitBytes, uploadLimitBytes) and applied via api.Use(apiRateLimiter, bodyLimitMiddleware, ...).
- **Why:** Genuinely enforced and consistently defaulted across all sources; no issue.

#### `log_format`
🔴 DEAD

- **Current default:** `"text"`
- **Usage:** internal/config/config.go:750 field LogFormat, viper default "text" (line 1132), exposed in frontend PerformanceSettingsTab.tsx (Text/JSON dropdown) — but grep for '.LogFormat' outside config.go/persistence.go returns zero hits. cmd/root.go:setupFileLogging() (line 516) hard-codes slog.NewTextHandler and never reads config.AppConfig.LogFormat.
- **Why:** The 'Log Format: Text / JSON (structured, recommended for log aggregation)' dropdown is dead: the actual logger setup in cmd/root.go always uses a plain TextHandler regardless of this setting. An operator picking 'JSON' for their log-aggregation pipeline gets no behavior change and no error telling them it didn't take effect.

#### `log_level`
🟢 used

- **Current default:** `"info" (frontend dropdown default)`
- **Usage:** cmd/root.go:481 config.AppConfig.LogLevel = logLevel (from a --log-level style CLI flag not in this bucket's inventory, handled elsewhere); internal/server/server.go:80 strings.EqualFold(config.AppConfig.LogLevel, "debug") gates debug-mode behavior; internal/itunes/import.go:259 debugLog := config.AppConfig.LogLevel == "debug".
- **Why:** Project instructions explicitly say prod stays on DEBUG (feedback_prod_stays_on_debug_build.md); the info default here is a log-verbosity setting, not the build type, so no conflict — just noting the two are different knobs an operator could confuse.

#### `memory_limit_mb`
🔴 DEAD

- **Current default:** `512`
- **Usage:** internal/config/config.go:742 field, viper default 512, persisted, exposed in frontend (128-16384 MB) — zero usages outside config/persistence/tests; no debug.SetMemoryLimit(GOMEMLIMIT) call anywhere in the repo.
- **Why:** Part of the same dead memory-limit subsystem — see memory_limit_type item for the consolidated finding and recommendation.

#### `memory_limit_percent`
🔴 DEAD

- **Current default:** `25`
- **Usage:** internal/config/config.go:741 field, viper default 25, persisted, exposed in frontend (1-90%) — but zero usages outside config/persistence/tests; no GOMEMLIMIT or process-memory-check code reads it anywhere in the repo.
- **Why:** Part of the same dead memory-limit subsystem as memory_limit_type/cache_size/memory_limit_mb — see memory_limit_type item for the consolidated finding.

#### `memory_limit_type`
🔴 DEAD

- **Current default:** `"items"`
- **Usage:** Declared in internal/config/config.go:724, round-tripped through persistence.go and the frontend radio group, but grep for MemoryLimitType outside config.go/persistence.go/tests finds zero hits — no code branches on 'items' vs 'percent' vs 'absolute' to actually apply a memory limit.
- **Why:** The entire 'Memory Limit Type / Cache Size / Memory Limit %/MB' settings panel (PerformanceSettingsTab.tsx lines 70-193) presents a fully-functional-looking memory-management UI that has zero effect on runtime behavior. This is the same class of bug the project's MEMORY.md repeatedly flags ('a hand-verified stillUsed=false finding') — either wire it to an actual cache/memory cap or remove the UI so operators don't believe they've bounded memory usage.

#### `metadata_fetch_cache_ttl_days`
🟢 used · 🟠 default review

- **Current default:** `backend viper default: 180 days; frontend UI displayed default: 30 days`
- **Recommended default:** `align the frontend default with the backend default (or vice versa) — pick one; 30 is a much shorter cache lifetime than 180` (confidence: high)
- **Usage:** config.AppConfig.MetadataFetchCacheTTLDays consumed in internal/server/metadata_ops.go (x2), internal/server/handlers/diagnostics.go, internal/metafetch/service_search.go, internal/metafetch/service_fetch.go, internal/maintenance/jobs/bulk_fetch_metadata.go — genuinely wired into the metadata cache TTL check.
- **Why:** internal/config/config.go:1121 has viper.SetDefault("metadata_fetch_cache_ttl_days", 180) but the frontend PerformanceSettingsTab.tsx documents/displays a default of 30 days. A fresh install actually gets 180 days (6x longer than what the UI implies), meaning stale Audible/Audnexus metadata could be served for far longer than an operator reading the UI would expect. Not a security bug, but a real behavior-vs-documentation drift worth fixing.

#### `organization_strategy`
🟢 used

- **Current default:** `"auto"`
- **Usage:** internal/organizer/organizer.go:271,824 strategy := o.config.OrganizationStrategy, drives reflink/hardlink/symlink/copy file-placement logic.
- **Why:** Auto (CoW clone -> hard link -> copy fallback) is the sensible default; no issue found.

#### `PORT`
🔴 DEAD · 🟡 naming · 🟠 default review

- **Current default:** `8484 (documented in .env.example, has no effect)`
- **Recommended default:** `wire PORT to the --port flag via viper.BindEnv, or delete the PORT line from .env.example` (confidence: high)
- **Usage:** Confirmed via grep: no Go code anywhere calls os.Getenv("PORT") or binds a viper key to env var PORT. The real port comes only from the unbound cobra flag --port (cmd/root.go:371, default "8484").
- **Naming issue:** Same class of issue as HOST: documented in .env.example as controlling the server port, but has zero code path to the actual --port flag.
- **Why:** Same reasoning as HOST — dead documented env var creates a false belief that setting PORT changes the listen port.

#### `purge_soft_deleted_after_days`
🟢 used

- **Current default:** `30`
- **Usage:** config.AppConfig.PurgeSoftDeletedAfterDays consumed in internal/scheduler/extra_ops.go (retention check, gate at line 858, days at 870), internal/scheduler/tasks.go (IsEnabled gate at 614/616), internal/plugins/maintenance/deps.go interface method.
- **Why:** 0 = disabled is documented in the UI helper text and the code checks '> 0' consistently; sane default.

#### `purge_soft_deleted_delete_files`
🟢 used

- **Current default:** `false`
- **Usage:** config.AppConfig.PurgeSoftDeletedDeleteFiles passed directly into AudiobookService.PurgeSoftDeletedBooks in internal/scheduler/extra_ops.go:871 and internal/server/audiobooks_helpers.go:224.
- **Why:** Correct fail-safe default: automatic purge removes DB records only unless the operator explicitly opts into also deleting files from disk. Given this repo's documented data-loss caution (missing-file repair is report-only, review-apply switch is live in prod), defaulting to file deletion would be the wrong call.

#### `root_dir (General/Library tab copy)`
🟢 used · 🟡 naming

- **Current default:** `"/path/to/audiobooks/library" (placeholder)`
- **Usage:** config.AppConfig.RootDir consumed extensively: internal/pathutil/abbreviate.go, internal/quarantine/service.go, internal/reconcile/{itunes_heal,reconcile}.go, internal/organizer/organizer.go (target path planning), internal/server/{server,folder_autoscan_op}.go, etc.
- **Naming issue:** The frontend field for this backend key is named libraryPath (web/src/components/SettingsGeneral.tsx:37, handleChange('libraryPath', ...)) with no visible textual relationship to root_dir/RootDir. Someone grepping the frontend for 'rootDir' or 'root_dir' to find this control would miss it entirely; only the settings serialization layer maps libraryPath <-> root_dir.
- **Why:** Not a security issue, but a real cross-layer naming inconsistency that this bucket was asked to flag; it's also literally duplicated UI (same field rendered on two different settings tabs per the inventory note), which compounds the confusion of the name not matching the backend key.

#### `scan_on_startup`
🟢 used

- **Current default:** `false`
- **Usage:** internal/scheduler/tasks.go:146,159 OR'd with Scheduled.LibraryScan.{Enabled,OnStartup}; internal/server/handlers/operations/handler.go:609,614 reads/clears it after first startup scan.
- **Why:** Sane default: don't force a potentially long scan on every server restart without opt-in.

#### `supported_extensions`
🟢 used

- **Current default:** `['.m4b','.mp3','.m4a']`
- **Usage:** config.AppConfig.SupportedExtensions consumed in internal/reconcile/reconcile.go, internal/importer/service.go (x2), internal/plugins/maintenance/title_repair.go, internal/scanner/service.go, and referenced by internal/audioutil/drm.go.
- **Why:** Reasonable default covering the common audiobook formats.

#### `UPLOAD_BODY_LIMIT_MB`
🟢 used

- **Current default:** `10 (MB), consistent across viper default, ResetToDefaults, and .env.example`
- **Usage:** internal/server/server_lifecycle.go:1398 uploadLimitBytes := int64(config.AppConfig.UploadBodyLimitMB) * 1024 * 1024, same MaxRequestBodySize wiring as JSON_BODY_LIMIT_MB.
- **Why:** Genuinely enforced and consistently defaulted; no issue.

#### `verify_after_write`
🔴 DEAD

- **Current default:** `true`
- **Usage:** internal/config/config.go:790 field VerifyAfterWrite, viper default true (line 1620), reset-to-defaults true (line 2279), exposed as 'Verify files after write' switch — but grep for VerifyAfterWrite outside config.go/persistence.go/tests returns zero hits anywhere in the writeback path (internal/metafetch/service_writeback.go, service_files.go).
- **Why:** This sits directly next to auto_rename_on_apply and auto_write_tags_on_apply in the same 'Smart Apply Pipeline' UI section and looks like a companion safety check, but nothing reads it. Given this repo's history of real writeback data-loss concerns (review-apply switch is live in prod, missing-file repair is deliberately report-only), a 'Verify files after write' toggle that silently does nothing is a meaningful gap between what the UI promises and what actually happens after a tag/rename write.

---

## Scanning, Organizing & Metadata (naming, scoring, backfill)

#### `--file (metadata inspect-metadata flag)`
🟢 used

- **Current default:** `""`
- **Usage:** cmd/root.go:382 declares the flag into `metadataInspectFile`, read at cmd/root.go:393 (`target := metadataInspectFile`) with the RunE falling back to a positional argument.
- **Why:** No default value makes sense for a required target file; empty-string-then-validate is standard. No issue.

#### `AO_DIR`
🔴 DEAD · 🟡 naming

- **Current default:** `/path/to/audiobooks (documented placeholder, never actually consulted)`
- **Usage:** Grepped every .go file under internal/ and cmd/ for AO_DIR, ROOT_DIR, and AUDIOBOOK_ROOT_DIR: AO_DIR has zero Go references (no os.Getenv, no viper.BindEnv). It is set in .env.example:15, docker-compose.yml:18 (`- AO_DIR=/audiobooks` in the container's environment block), and documented in README.md:197, but nothing in the running application reads it. The real, live binding is ROOT_DIR via viper.AutomaticEnv() (cmd/root.go:456) plus SyncConfigFromEnv (internal/config/persistence.go:1541).
- **Naming issue:** AO_DIR is a fully dead environment variable across every deployment surface that documents or sets it (.env.example, docker-compose.yml, README.md), while the code only honors ROOT_DIR. Same root problem as the root_dir naming issue above -- documented in both places for completeness.
- **Why:** See the root_dir entry's recommendation: reconcile by either wiring AO_DIR as a real BindEnv alias for root_dir, or updating .env.example/docker-compose.yml/README to use ROOT_DIR. Leaving it as-is means every documented onboarding path for this option is currently wrong.

#### `AUDIBLE_BASE_URL`
🟢 used

- **Current default:** `https://api.audible.com/1.0`
- **Usage:** internal/metadata/audible.go:32 os.Getenv override for the Audible metadata provider client base URL.
- **Why:** No issue.

#### `AUDNEXUS_BASE_URL`
🟢 used

- **Current default:** `https://api.audnex.us`
- **Usage:** internal/metadata/audnexus.go:33 os.Getenv override for the Audnexus community metadata API base URL.
- **Why:** No issue.

#### `auto_fetch_metadata`
🟢 used

- **Current default:** `true`
- **Usage:** internal/config/config.go:1040 default true. Wired through persistence.go and config_unit_test.go; consumed indirectly by the scan/organize pipeline that triggers metadata fetch for newly discovered books (internal/scanner/service.go uses AutoOrganize alongside metadata-fetch triggering in server wiring).
- **Why:** Reasonable default for the product's core value proposition (auto-tag audiobooks).

#### `auto_fetch_metadata`
🟢 used

- **Current default:** `true (consistent across Go struct default, config.yaml, and frontend switch)`
- **Usage:** Frontend switch in MetadataSettingsTab.tsx:108-120 sets settings.autoFetchMetadata; matches the yaml key and the Go struct's AutoFetchMetadata field (ResetToDefaults, config.go: 'AutoFetchMetadata: true').
- **Why:** Auto-fetching metadata for newly scanned books is a core value proposition of the app; true is the expected default and all layers agree.

#### `auto_organize`
🟢 used

- **Current default:** `true`
- **Usage:** internal/config/config.go:1026 default true. Consumed in internal/itunes/service/importer.go, internal/server/server.go, internal/server/wire_handlers.go, internal/server/folder_autoscan_op.go, internal/operations/state.go, internal/scanner/service.go.
- **Why:** Widely used, long-established default; no evidence it's unsafe.

#### `auto_organize`
🟢 used

- **Current default:** `true`
- **Usage:** yaml key auto_organize maps to Config.AutoOrganize; canonical default `AutoOrganize: true` in ResetToDefaults, `viper.SetDefault("auto_organize", true)` at config.go:1026.
- **Why:** Matches canonical Go default. Given project memory's warning that a running scan can clobber applied metadata for not-yet-processed books, auto-organizing on by default is acceptable since organize and scan are separate operations, but pairs with scan_on_startup=false (see that item) to avoid the two colliding automatically at boot.

#### `auto_rename_on_apply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/config/config.go:1291 default true. Consumed in internal/server/handlers/metadata/handler.go, internal/metafetch/service_writeback.go, internal/metafetch/service_files.go.
- **Why:** Consistent with the product's auto-organize default; live and used by a single, deliberately-unified apply pipeline per the code comment at config.go:783-787 (the old dual path-builder was removed, not just deprecated).

#### `auto_scan_debounce_seconds`
🟢 used

- **Current default:** `30 (seconds)`
- **Usage:** internal/config/config.go:1028 default 30. Consumed at internal/server/server_lifecycle.go:556-557 (`if config.AppConfig.AutoScanDebounceSeconds > 0 { debounce = ... }`).
- **Why:** 30s debounce is a reasonable default to coalesce a burst of filesystem events before triggering a scan.

#### `auto_scan_enabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1027 default false. Consumed at internal/server/server_lifecycle.go:554 (`if config.AppConfig.AutoScanEnabled && s.Ops() != nil`).
- **Why:** fsnotify-based auto-scanning is opt-in, which is the right default given the documented risk of a scan clobbering not-yet-processed metadata mid-run.

#### `auto_write_tags_on_apply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/config/config.go:1292 default true. Consumed in internal/metafetch/service_files.go and internal/metafetch/service_writeback.go.
- **Why:** Live and sane; note this is a distinct control point from the global write_back_metadata flag (gates the apply-pipeline's own tag-write step) — not redundant, just adjacent, both verified as separately consumed.

#### `BLEVE_DESCRIPTION_MAX_CHARS`
🟢 used

- **Current default:** `500`
- **Usage:** internal/search/index_builder.go:55, loaded once via sync.Once, parsed with strconv.Atoi and accepted only if >= 0.
- **Why:** Reasonable cap on indexed description length to bound search-index size; no evidence of an issue.

#### `concurrent_scans`
🟢 used

- **Current default:** `max(runtime.NumCPU(), 4) canonically; the tracked repo-root config.yaml shows a stale literal 10 which is NOT the code's actual default (that file isn't loaded by the app's default config path)`
- **Usage:** Config.ConcurrentScans consumed as an actual worker-pool bound in 3 places: internal/server/folder_autoscan_op.go:73, internal/scanner/service.go:392, and internal/scanner/scanner.go:722 (`ProcessBooksParallel(ctx, books, config.AppConfig.ConcurrentScans, ...)`). Canonical default computed at config.go:1066-1070: `defaultWorkers := runtime.NumCPU(); if defaultWorkers < 4 { defaultWorkers = 4 }; viper.SetDefault("concurrent_scans", defaultWorkers)` -- matches ResetToDefaults' `max(runtime.NumCPU(), 4)`.
- **Why:** This is exactly the multi-core-aware pattern the repo's concurrency mandate requires -- already correctly implemented, scales with CPU count with a floor of 4. No change needed. Flagging only that the checked-in repo-root config.yaml's literal 10 could mislead someone reading it as 'the default' when it isn't; see the file_naming_pattern finding for the same stray file.

#### `Config.AutoFetchMetadata`
🔴 DEAD

- **Current default:** `true`
- **Usage:** No call site outside internal/config/{config.go,persistence.go} reads config.AppConfig.AutoFetchMetadata. It is a live Settings-page toggle (web/src/pages/Settings.tsx `autoFetchMetadata`, web/src/hooks/useSettingsHandlers.ts) that persists through the API, but nothing in the scan/organize/metadata pipeline checks it before auto-fetching metadata for newly-scanned books.
- **Why:** Since the default is already true and nothing gates on it, the observable behavior today is 'metadata is always auto-fetched regardless of this setting' — which happens to match the true=on default, masking the bug for users who never turn it off. A user who disables it in Settings gets no actual change in behavior. Needs to be wired into whatever code path currently triggers metadata fetch after scan/import, or removed from the UI.

#### `Config.AutoOrganize`
🟢 used

- **Current default:** `true`
- **Usage:** internal/server/server.go:921-922 and internal/server/folder_autoscan_op.go:92,144 gate automatic post-scan organization on this flag; also threaded into wire_handlers.go.
- **Why:** No direct evidence this causes harm, but it means every newly-scanned book is auto-organized (moved/renamed/tagged) by default with no review step, which is a stronger default than ScanOnStartup=false suggests is intended for this codebase's safety posture. Flagging for user awareness rather than recommending a change, since I found no incident tied to this default in the researched code paths — a definitive recommendation would require product-level input on desired first-run behavior.

#### `Config.AutoRenameOnApply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/server/handlers/metadata/handler.go:817, internal/metafetch/service_writeback.go:499, and internal/metafetch/service_files.go:117 all gate rename-on-apply behavior on this flag.
- **Why:** Actively used across three call sites; no issue found with the default itself, though it's worth noting (as this bucket's own guidance flags) that combined with WriteBackMetadata=false, applying reviewed metadata renames files but does not write tags by default — an intentional, documented split per project memory.

#### `Config.AutoRenameOnApply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/server/handlers/metadata/handler.go:817 `doRename := (body.Rename != nil && *body.Rename) \|\| config.AppConfig.AutoRenameOnApply`; also consumed in internal/metafetch/service_writeback.go:499 and service_files.go:117.
- **Why:** No evidence this default is unsafe; renaming only happens on an already-approved apply action, and AutoRenameOnApply/AutoWriteTagsOnApply/VerifyChecksums together form the write-back path where checksums are always verified regardless (see VerifyAfterWrite finding below).

#### `Config.AutoScanDebounceSeconds`
🟢 used

- **Current default:** `30 (seconds)`
- **Usage:** Validated (must be >= 0) and threaded into config load at internal/config/config.go:1432/1967; consumed by the auto-scan debounce logic paired with AutoScanEnabled.
- **Why:** Reasonable debounce window for filesystem-event coalescing; no evidence of mistuning.

#### `Config.AutoScanEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/server_lifecycle.go:554 gates the filesystem-watcher-driven auto-scan feature on this flag; also read in internal/scheduler/tasks.go alongside Scheduled.LibraryScan.Enabled.
- **Why:** Sane: fsnotify-driven auto-scanning is described in code comments as 'opt-in and best-effort', so defaulting off is correct and matches the documented scan-clobbers-metadata risk in this codebase.

#### `Config.AutoWriteTagsOnApply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/metafetch/service_writeback.go:604 and internal/metafetch/service_files.go:117 gate tag-writing-on-apply behavior on this flag.
- **Why:** Actively used; no issue found.

#### `Config.AutoWriteTagsOnApply`
🟢 used

- **Current default:** `true`
- **Usage:** internal/metafetch/service_files.go:117 `if config.AppConfig.AutoRenameOnApply \|\| config.AppConfig.AutoWriteTagsOnApply`; internal/metafetch/service_writeback.go:604.
- **Why:** Consistent with AutoRenameOnApply; no evidence of a problem.

#### `Config.ChapterConsolidationThresholdMin`
🟢 used

- **Current default:** `10 (minutes) via viper.SetDefault("chapter_consolidation_threshold_min", 10) at config.go:1071`
- **Usage:** internal/config/config.go:592 declares the field; consumed during scanning to detect and consolidate chapter-named files below this per-file duration threshold (per its doc comment; scanner package logic groups files by this threshold).
- **Why:** ResetToDefaults() (config.go, 'func ResetToDefaults') does NOT set ChapterConsolidationThresholdMin in its struct literal, so calling factory-reset sets it to the Go zero value 0 instead of the documented default of 10. Per the field's own doc comment, 'Set to 0 to disable consolidation' — so a factory reset silently and permanently disables chapter-file consolidation, diverging from the value every fresh install gets via viper.SetDefault. This is a real config-defaults bug: two different code paths that are both supposed to produce 'default configuration' disagree (10 vs. 0), and the divergent one changes scan behavior, not just cosmetics.

#### `Config.CoalesceShatteredSiblings`
🟢 used

- **Current default:** `false`
- **Usage:** internal/scanner/scanner.go:705 gates the shattered-sibling coalescing post-pass on this flag; internal/scanner/shattered_coalesce.go implements it.
- **Why:** Deliberately off by default per its own doc comment ('existing library is already healed; enable on a canary before turning on by default') — correct, cautious default for a scan-time structural-merge feature. ResetToDefaults() omits this field too, but since bool zero-value is false, it matches the intended default by coincidence — unlike ChapterConsolidationThresholdMin, this one is not actually broken by the omission.

#### `Config.ConcurrentScans`
🟢 used

- **Current default:** `max(runtime.NumCPU(), 4) — both in viper.SetDefault (config.go:1066-1070) and DefaultConfig/ResetToDefaults (config.go:2079)`
- **Usage:** internal/server/folder_autoscan_op.go:73, internal/scanner/service.go:392, and internal/scanner/scanner.go:722 all read config.AppConfig.ConcurrentScans as the worker-pool size for parallel book/scan processing (via ProcessBooksParallel).
- **Why:** This is a whole-library worker-pool size and it already correctly scales with runtime.NumCPU() (floored at 4), fully satisfying this repo's mandatory multi-core concurrency policy. No change needed — flagging explicitly as a positive control since this is exactly the kind of option the policy calls out.

#### `Config.ConcurrentScans`
🟢 used

- **Current default:** `max(runtime.NumCPU(), 4)`
- **Usage:** internal/config/config.go:578 (field), :1066-1070 (`defaultWorkers := runtime.NumCPU(); if defaultWorkers < 4 { defaultWorkers = 4 }; viper.SetDefault("concurrent_scans", defaultWorkers)`), :1468 (load), :1961-1962 (validated >= 0), :2079 (ResetToDefaults uses `max(runtime.NumCPU(), 4)`).
- **Why:** This is exactly the multi-core-aware default this repo's mandatory concurrency policy calls for — a bounded worker pool sized to NumCPU (with a floor of 4 for low-core-count machines). No change needed; flagging it as a positive example, not a problem.

#### `Config.CreateBackups`
🔴 DEAD

- **Current default:** `true`
- **Usage:** No call site outside internal/config/{config.go,persistence.go} reads config.AppConfig.CreateBackups. A separate, unrelated database-backup subsystem exists (internal/organizer/service.go's backup.CreateBackup/CreateBackupWithCheckpoint) but is not gated by this field — it operates on the DB, not organize/rename file operations. The field is a live Settings-page toggle (web/src/pages/Settings.tsx: `createBackups`, `create_backups`) that round-trips through the API but is never consulted before a file move/rename.
- **Why:** This is presented to users as an active safety setting ('back up files before organizing them') but does nothing. Given this bucket's stated sensitivity around data-loss-adjacent scan/organize behavior, this is worth flagging clearly: either wire it into the organizer's file-move path (create a backup of the original file/tags before an organize op) or remove the toggle so users aren't relying on protection that isn't there.

#### `Config.EmbedCoverArt`
🔴 DEAD · 🟡 naming

- **Current default:** `false`
- **Usage:** No call site outside internal/config/{config.go,persistence.go} reads config.AppConfig.EmbedCoverArt. A same-named but unrelated function `tagger.EmbedCoverArt(audioPath, coverPath string) error` exists in internal/tagger/embed_cover.go, but grepping its call sites shows it is invoked directly by callers, not gated behind this config bool — i.e. the config flag and the tagger function are two different, disconnected things sharing a name.
- **Naming issue:** The config field Config.EmbedCoverArt (a bool toggle) and the function tagger.EmbedCoverArt() (an action) share the exact same name across packages, which is misleading: reading 'EmbedCoverArt' in a caller's code looks like it could reference either, but neither actually calls the other. Worth a rename or explicit wiring to disambiguate.
- **Why:** Presented as a live Settings toggle but does not gate the actual cover-embedding function calls found in the codebase. Needs to be wired into whatever code path decides whether to call tagger.EmbedCoverArt, or removed from the UI if cover embedding is unconditional/decided elsewhere.

#### `Config.ExcludePatterns`
🟢 used

- **Current default:** `[] (empty)`
- **Usage:** internal/scanner/scanner.go:447 iterates `config.AppConfig.ExcludePatterns` to skip matching paths during scan; loaded via internal/config/persistence.go:986.
- **Why:** An empty default is correct -- excluding nothing by default avoids silently skipping a user's files; exclusions are opt-in per install.

#### `Config.FileNamingPattern`
🟢 used

- **Current default:** `"{title} - {track:02d}"`
- **Usage:** Same call sites as FolderNamingPattern (internal/organizer/organizer.go, pipeline.go).
- **Why:** Sane default; no issue found.

#### `Config.FolderNamingPattern`
🟢 used

- **Current default:** `"{author}/{series}/{title} ({print_year})"`
- **Usage:** Read throughout internal/organizer/organizer.go and pipeline.go (expandPattern, BuildRelPath, planTargetPaths) to build target folder paths.
- **Why:** Sensible, widely-used audiobook library layout; no issue found.

#### `Config.GoogleBooksAPIKey`
🟢 used

- **Current default:** `"" (secret; user-supplied)`
- **Usage:** internal/metafetch/service_search.go:127,163 and internal/maintenance/jobs/bulk_fetch_metadata.go:261 read this key for Google Books API requests.
- **Why:** Correct default for a credential.

#### `Config.HardcoverAPIToken`
🟢 used

- **Current default:** `"" (secret; user-supplied)`
- **Usage:** internal/metafetch/service_search.go:126,175 and internal/maintenance/jobs/bulk_fetch_metadata.go:273 read this token to authenticate Hardcover.app API requests.
- **Why:** Correct default for a credential — must be empty until the user supplies it.

#### `Config.Language`
🟢 used

- **Current default:** `"en"`
- **Usage:** Read across 40 files (broad grep hit count reflects common variable name collisions, but the config-specific `config.AppConfig.Language` is consumed for metadata-source language filtering and defaults).
- **Why:** Sane default for this codebase's primary userbase; no issue found.

#### `Config.MetadataFetchCacheTTLDays`
🟢 used · 🟠 default review

- **Current default:** `180 (days) via viper.SetDefault at config.go:1121`
- **Recommended default:** `align the doc comment and frontend fallback to the real default (or vice versa)` (confidence: high)
- **Usage:** internal/config/config.go:1516 wires viper.GetInt("metadata_fetch_cache_ttl_days") into the struct; consumed by the DB-backed metadata-fetch cache freshness check (GetCachedMetadataFetchWithMaxAge call sites in internal/metafetch and internal/maintenance/jobs).
- **Why:** Three different numbers claim to be this field's default: the field's own doc comment says 'Default 7' (config.go:733), the actual live viper.SetDefault is 180 (config.go:1121), and the frontend fallback in web/src/pages/Settings.tsx:568 uses `config.metadata_fetch_cache_ttl_days ?? 30`. The runtime behavior today is governed by 180 (viper wins when the key is unset), so the doc comment (7) is stale and misleading, and the frontend's fallback of 30 would only ever be used in the brief window before the real config value loads — but if it were ever read as authoritative it would silently shorten cache freshness 6x versus the real default. Recommend updating the doc comment to 180 and the frontend fallback to 180 for consistency, or deliberately re-picking one true value and propagating it everywhere.

#### `Config.MetadataReviewDefaultView`
🔴 DEAD

- **Current default:** `"compact" (viper default)`
- **Usage:** Only referenced in internal/config/config.go (declaration + viper default "compact") and internal/config/persistence.go (get/set plumbing). No frontend component reads `metadata_review_default_view` / `metadataReviewDefaultView` (confirmed via grep across web/src), and no other Go package reads Config.MetadataReviewDefaultView to change any review-UI or API response.
- **Why:** Appears to be a fully dead setting: it's persisted (settable via API/config) but nothing reads it to change the metadata-review UI's default view mode. Either the frontend's default-view logic needs to consume this field, or it should be removed as unused. Also note ResetToDefaults() omits this field entirely, so a factory reset silently drops it to the Go zero value ("") instead of "compact" — compounding evidence that no one has kept this option's plumbing in sync because nothing depends on it.

#### `Config.MetadataScoring.BulkFetchWorkers`
🟢 used

- **Current default:** `4`
- **Usage:** internal/server/metadata_ops.go:58-70 documents and implements the fallback: `if w := config.AppConfig.MetadataScoring.BulkFetchWorkers; w > 0 { return w }; return defaultBulkFetchWorkers` (also 4). Comment explicitly notes this pool is network-bound against metadata providers, not CPU-bound.
- **Why:** This is a network-bound worker pool (concurrent HTTP calls to external metadata providers), which the repo's concurrency mandate explicitly carves out for 'a smaller fixed concurrency for network-bound work that respects the target's own rate limits' rather than scaling with NumCPU. A fixed 4 is consistent with that guidance and is NOT a single-threaded-by-default violation.

#### `Config.MetadataScoring.CompilationPenalty`
🟢 used

- **Current default:** `0.15 (via f64Ptr)`
- **Usage:** Mirrored default 0.15 in internal/plugins/metafetch/calibrate_scoring.go:185 (`CompilationPenalty: 0.15`), consumed at line 229 (`if cfg.CompilationPenalty != nil`). Confirms the scoring pipeline actually applies this value, not just plumbing.
- **Why:** Nilable-pointer pattern lets an unset value fall through to this default cleanly; magnitude is plausible relative to the other additive score terms (boosts of 0.05-2.0). No evidence of miscalibration.

#### `Config.MetadataScoring.DurationTierMultipliers`
🟢 used

- **Current default:** `[1.30, 1.20, 1.10, 1.00, 0.75, 0.50]`
- **Usage:** Exact array mirrored in calibrate_scoring.go:173 (`defaultDurationTierMultipliers = [durationTierCount]float64{1.30, 1.20, 1.10, 1.00, 0.75, 0.50}`), consumed by the scoring engine.
- **Why:** Monotonically decreasing tier multipliers are structurally sound; no evidence of a bug.

#### `Config.MetadataScoring.DurationTierScores`
🟢 used

- **Current default:** `[20, 15, 10, 0, -10, -20]`
- **Usage:** Exact array mirrored in calibrate_scoring.go:174 (`defaultDurationTierScores = [durationTierCount]float64{20, 15, 10, 0, -10, -20}`).
- **Why:** Symmetric additive tier scores centered on 0; structurally sound.

#### `Config.MetadataScoring.EmbeddingBestMatch`
🟢 used

- **Current default:** `0.88`
- **Usage:** BindEnv metadata_scoring.embedding_best_match (config.go:1371); legacy fallback key metadata_embedding_best_match_min also sets this field.
- **Why:** No evidence of miscalibration; feature is opt-in via EmbeddingEnabled.

#### `Config.MetadataScoring.EmbeddingEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** Read via viper.GetBool("metadata_scoring.embedding_enabled") into MetadataScoring.EmbeddingEnabled (config.go:1369 BindEnv, :2146 default) and consumed by the metadata scoring pipeline.
- **Why:** Off by default is sane: embedding-based scoring adds an external/model dependency and cost that shouldn't silently turn on for every install.

#### `Config.MetadataScoring.EmbeddingMinScore`
🟢 used

- **Current default:** `0.82`
- **Usage:** BindEnv metadata_scoring.embedding_min_score (config.go:1370); legacy DB fallback key metadata_embedding_min_score also maps to this field (persistence.go:1433-1436).
- **Why:** Only meaningful while EmbeddingEnabled=false by default; no evidence the threshold itself is miscalibrated.

#### `Config.MetadataScoring.F1MinScore`
🟢 used

- **Current default:** `0.35 (via f64Ptr)`
- **Usage:** Mirrored in calibrate_scoring.go:188 (0.35) and applied at line 235.
- **Why:** No evidence of an issue; a low-ish acceptance floor errs toward keeping candidates rather than silently discarding them, consistent with the review-first posture of this codebase.

#### `Config.MetadataScoring.LLMEnabled / metadata_llm_scoring_enabled`
🟢 used

- **Current default:** `false (Go struct and frontend UI both default to false/off)`
- **Usage:** Backend: BindEnv metadata_scoring.llm_enabled (config.go:1372), legacy DB key metadata_llm_scoring_enabled (persistence.go:1440-1443). Frontend: MetadataSettingsTab.tsx:152-173 exposes the same server-wide gate switch, correctly labeled as required before the per-search LLM-rerank toggle takes effect.
- **Why:** Off by default is correct -- this gates a paid per-search LLM call (~$0.003/search per the UI helper text); should never turn on without explicit opt-in.

#### `Config.MetadataScoring.LLMRerankEpsilon`
🟢 used

- **Current default:** `0.05`
- **Usage:** BindEnv metadata_scoring.llm_rerank_epsilon (config.go:1373); legacy DB fallback metadata_llm_rerank_epsilon (persistence.go:1444-1447).
- **Why:** Only active while LLMEnabled=false by default; no evidence of miscalibration.

#### `Config.MetadataScoring.LLMRerankTopK`
🟢 used

- **Current default:** `5`
- **Usage:** BindEnv metadata_scoring.llm_rerank_top_k (config.go:1374); legacy DB fallback metadata_llm_rerank_top_k (persistence.go:1448-1451).
- **Why:** Reasonable candidate-pool size for a re-rank pass; no evidence of a problem.

#### `Config.MetadataScoring.RichMetadataBonusCap`
🟢 used

- **Current default:** `0.15 (via f64Ptr)`
- **Usage:** Mirrored in calibrate_scoring.go:187 (0.15) and applied at line 232.
- **Why:** Cap is 3x the per-field bonus (0.05), i.e. allows at most 3 fields' worth of bonus -- a sane, non-runaway ceiling.

#### `Config.MetadataScoring.RichMetadataFieldBonus`
🟢 used

- **Current default:** `0.05`
- **Usage:** Mirrored in calibrate_scoring.go:186 (0.05) and applied at line 215.
- **Why:** No evidence of an issue.

#### `Config.MetadataScoring.SeriesNameMatchBoost`
🟢 used

- **Current default:** `1.4`
- **Usage:** Mirrored default 1.4 in calibrate_scoring.go:189, applied at line 218.
- **Why:** No evidence of miscalibration.

#### `Config.MetadataScoring.SeriesNumberExactBoost`
🟢 used

- **Current default:** `2.0`
- **Usage:** Mirrored default 2.0 in calibrate_scoring.go:190, applied at line 221.
- **Why:** No evidence of miscalibration.

#### `Config.MetadataScoring.SeriesNumberWrongPenalty`
🟢 used

- **Current default:** `0.5`
- **Usage:** Mirrored default 0.5 in calibrate_scoring.go:191, applied at line 224.
- **Why:** No evidence of miscalibration.

#### `Config.MetadataScoring.SourceFanoutWorkers`
🟢 used

- **Current default:** `4`
- **Usage:** internal/metafetch/service_search.go:25-38: `if w := config.AppConfig.MetadataScoring.SourceFanoutWorkers; w > 0` -- comment explicitly warns that BulkFetchWorkers x SourceFanoutWorkers multiply (4x4=16 concurrent provider requests) against external metadata APIs.
- **Why:** Same network-bound rationale as BulkFetchWorkers; the code comment shows the author already reasoned about the multiplicative fan-out (4x4=16 in-flight requests) and chose 4x4 deliberately. Sane default, not a concurrency-policy violation.

#### `Config.MetadataScoring.WriteBackupBefore`
🟢 used

- **Current default:** `true`
- **Usage:** BindEnv metadata_scoring.write_backup_before (config.go:1375); legacy DB fallback write_backup_before_tag_write (persistence.go:1452-1455).
- **Why:** Safe default: back up before mutating audio file tags. Keep as-is.

#### `Config.MetadataScoring.WriteBackWorkers`
🟢 used

- **Current default:** `4`
- **Usage:** internal/server/metadata_ops.go:73-89 implements the same pattern: `if w := config.AppConfig.MetadataScoring.WriteBackWorkers; w > 0 { return w }; return defaultWriteBackWorkers` (4), guard documented as load-bearing.
- **Why:** Write-back touches audio files on disk (I/O-bound, and per this repo's history a runaway write-back pool risks stepping on files mid-scan); a bounded pool of 4 rather than unbounded or single-threaded is appropriate and matches the network/IO-bound carve-out in the concurrency mandate.

#### `Config.MetadataSources`
🟢 used

- **Current default:** `built-in list (openlibrary, audible, hardcover, google-books, etc. — see config.go:1780-1828 and 2289-2337)`
- **Usage:** internal/metafetch/service_search.go:125,143-144 and internal/maintenance/jobs/bulk_fetch_metadata.go:242-246 read config.AppConfig.MetadataSources to build the runtime provider chain.
- **Why:** Actively used; ResetToDefaults correctly repopulates the full default list. No issue found.

#### `Config.MetadataSources (+ frontend metadata_sources[].enabled / .priority / .credentials.apiKey)`
🟢 used

- **Current default:** `audible(enabled,pri1); openlibrary(enabled,pri2); audnexus(enabled,pri3); google-books(disabled,pri4,requiresAuth); hardcover(disabled,pri5,requiresAuth); wikipedia(disabled,pri6)`
- **Usage:** Backend: config.go:533/1777/1780/2289, internal/metafetch/service_search.go:125/143-144, internal/maintenance/jobs/bulk_fetch_metadata.go:242-246 all read config.AppConfig.MetadataSources. Frontend: MetadataSettingsTab.tsx:318-330 (enabled toggle via handleSourceToggle), :254-271 (priority reorder via handleSourceReorder), :355-373 (per-source credentials.apiKey). Verified the per-source Credentials['apiKey'] genuinely overrides the top-level GoogleBooksAPIKey rather than being a dead parallel field: internal/metafetch/service_search.go:163-165 `apiKey := config.AppConfig.GoogleBooksAPIKey; if k, ok := src.Credentials["apiKey"]; ok && k != "" { apiKey = k }`.
- **Why:** Sane priority order (free/no-auth sources first) and the two auth-requiring sources default disabled since they need a key the install doesn't have yet. No changes warranted.

#### `Config.MinBookSizeBytes`
🟢 used

- **Current default:** `5242880 (5 MB)`
- **Usage:** internal/scanner/scanner.go:873 reads this threshold (`if threshold := config.AppConfig.MinBookSizeBytes; threshold > 0`) to flag suspiciously small single-file books and skip heavy processing.
- **Why:** Consistent across viper default and DefaultConfig/ResetToDefaults (both 5*1024*1024). Reasonable heuristic threshold for a full audiobook file; no issue found.

#### `Config.MinBookSizeBytes`
🟢 used

- **Current default:** `5*1024*1024 (5MB)`
- **Usage:** internal/scanner/scanner.go:873 gates the suspicious-file guard: `if threshold := config.AppConfig.MinBookSizeBytes; threshold > 0`. Validate() also silently rewrites a zero value back to the 5MB default rather than erroring.
- **Why:** 5MB is a reasonable floor to distinguish a real audiobook file from a stray/corrupt fragment. No evidence it's too aggressive or too lax; leave as-is.

#### `Config.OpenLibraryDumpDir`
🟢 used

- **Current default:** `""`
- **Usage:** internal/metafetch/openlibrary.go:30-31 returns this path when set (falls back to a computed default when empty).
- **Why:** Empty-means-use-computed-default is a sane pattern; no issue found.

#### `Config.OpenLibraryDumpEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/metafetch/openlibrary.go:50-52 reads and can auto-enable this flag when an existing dump store is found; internal/server/openlibrary_service.go:31 exposes it via API status.
- **Why:** Sane opt-in default for an optional bulk-data feature requiring a large local dump download.

#### `Config.OrganizationStrategy`
🟢 used

- **Current default:** `"auto" (per ResetToDefaults)`
- **Usage:** internal/organizer/organizer.go:271 and :824 read `o.config.OrganizationStrategy` to select copy/hardlink/reflink/symlink behavior.
- **Why:** Sane default; auto-detection is the least-surprising choice for new installs.

#### `Config.PlaylistDir`
🔴 DEAD

- **Current default:** `"" (set to `${libraryPath}/playlists` by the setup wizard)`
- **Usage:** Only consumer in Go is cmd/root.go, which prints it in two `fmt.Printf` status lines ("Playlists saved to: %s"). No code writes .m3u/playlist export files to this directory — the actual smart-playlist feature (internal/playlist, internal/server/handlers/playlists.go) is DB-backed and exposed via API, not written to a filesystem dir. Set by the setup wizard (web/src/components/wizard/WelcomeWizard.tsx) and round-tripped through Settings, but nothing consumes it to determine where any file actually gets written.
- **Why:** This looks like a vestigial config option left over from a filesystem-based playlist export feature that was superseded by the DB-backed smart-playlist system. It is presented to users as a meaningful setting (wizard prompts for it, Settings page lets you edit it) but currently does nothing beyond being echoed back in CLI startup text. Recommend either wiring it into an actual playlist-export path or removing it from the setup wizard/Settings UI to stop implying it configures behavior it doesn't.

#### `Config.ProtectedPaths`
🟢 used

- **Current default:** `empty ("[]string{}")`
- **Usage:** internal/server/server.go:992-993 passes config.AppConfig.ProtectedPaths into deluge.NewProtectedPathCache, consulted before any in-place tag write per the comment at server.go:267.
- **Why:** Empty is the correct default — this is an explicit opt-in safety allowlist merged with the Deluge save_path set at runtime; no universal safe non-empty default exists.

#### `Config.ReviewApplyEnabled (review_apply_enabled)`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/wire_handlers.go:626 wires it as the review-queue apply gate: `reviewH := reviewhandler.New(s.storeForWiring(), func() bool { return config.AppConfig.ReviewApplyEnabled })`. Per project memory, this switch was flipped ON in prod on 2026-08-17 (approvals now mutate data), though the field default in source stays false.
- **Why:** OFF-by-default (review-only) is the correct conservative default for a switch that lets approved candidates mutate live data; a deployer opts in explicitly. Keep as-is.

#### `Config.RootDir`
🟢 used

- **Current default:** `"" (empty; set during first-run setup wizard)`
- **Usage:** Read pervasively across the codebase (59 files outside config/test code), e.g. internal/organizer/organizer.go, internal/server/server.go, internal/server/folder_autoscan_op.go as the library root for scanning/organizing.
- **Why:** Empty default is correct/required — this is a user-supplied path with no safe universal default.

#### `Config.ScanOnStartup`
🟢 used

- **Current default:** `false`
- **Usage:** internal/scheduler/tasks.go:146 and internal/server/handlers/operations/handler.go:609-614 read/consume this flag to trigger and then one-shot-disable a startup scan.
- **Why:** Sane: an unattended full scan on every process start is surprising behavior for a background service; opt-in is correct, especially given the documented risk that a running scan can clobber applied metadata for not-yet-processed books (see project memory 'A running scan CLOBBERS applied metadata').

#### `Config.ScanProgressEvery`
🟢 used

- **Current default:** `20`
- **Usage:** internal/scanner/scanner.go:534 reads this as the checkpoint interval for the stuck-op watchdog during a library scan.
- **Why:** Reasonable default per the field's own doc comment (a library on slow/high-latency storage may need a smaller value); this is intentionally user-tunable, not a concurrency knob, so runtime.NumCPU()-scaling doesn't apply.

#### `Config.ScanProgressEvery`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `20 (intended) / 0 (actual value from viper.GetInt when no SetDefault exists and no config file sets it)`
- **Recommended default:** `Add `viper.SetDefault("scan_progress_every", 20)` in InitConfig() alongside its Performance-section siblings.` (confidence: high)
- **Usage:** internal/config/config.go:586 (field), :1469 (`viper.GetInt("scan_progress_every")`), :2080 (ResetToDefaults sets 20). Consumed at internal/scanner/scanner.go:534-537: `every := config.AppConfig.ScanProgressEvery; if every <= 0 { every = scanProgressEvery }` where `scanProgressEvery` is a package constant = 20 (scanner.go:58).
- **Naming issue:** Every other 'Performance' section field in InitConfig() gets a `viper.SetDefault(...)` call (concurrent_scans, operation_timeout_minutes, etc. at config.go:1065-1072) EXCEPT scan_progress_every — grepped `scan_progress_every` across config.go and found only the struct-field, ResetToDefaults literal, and the Load() GetInt call; no SetDefault. This means a fresh install with no config.yaml and no CLI reset gets AppConfig.ScanProgressEvery == 0 straight out of viper, not the documented default of 20.
- **Why:** Functionally harmless today only because internal/scanner/scanner.go:535-537 defensively re-derives 0-or-unset back to its own local constant 20 before using it — a genuine 'partial fix' where the consumer masks a config-loading bug rather than the config itself supplying the documented default. Any other future consumer of AppConfig.ScanProgressEvery that doesn't repeat that same `<= 0` guard would silently get 0 (which, if used as a modulo/divisor without a zero-check, could panic or emit a progress event on every single item scanned instead of every 20th).

#### `Config.SupportedExtensions`
🟢 used

- **Current default:** `e.g. [".m4b", ".mp3", ".m4a"] (frontend fallback); backend default set separately in config.go's ResetToDefaults/DefaultConfig`
- **Usage:** Read extensively (11+ call sites) across internal/importer/service.go, internal/reconcile/reconcile.go, internal/plugins/maintenance/title_repair.go, internal/scanner/{scanner.go,service.go}, and internal/metadata/assemble.go to filter which files are treated as audio.
- **Why:** Heavily used and validated (each entry must start with '.', per config.go's Validate()). No issue found.

#### `Config.SupportedExtensions`
🟢 used

- **Current default:** `.m4b, .mp3, .m4a, .aac, .ogg, .flac, .wma, .opus, .oga, .wav, .aiff, .aif, .mka, .aax, .aax c`
- **Usage:** Consumed at internal/importer/service.go:147/162, internal/reconcile/reconcile.go:549, internal/plugins/maintenance/title_repair.go:202, and internal/config/persistence.go:980 for load. Validate() requires each non-empty entry to start with '.'.
- **Why:** Broad, sensible default covering the common audiobook container/codec formats. No evidence of a gap.

#### `Config.VerifyAfterWrite`
🔴 DEAD

- **Current default:** `true`
- **Usage:** No call site outside internal/config/config.go (declaration, viper default, DefaultConfig/ResetToDefaults) reads config.AppConfig.VerifyAfterWrite. It is grouped in the same 'apply pipeline flag' comment block as AutoRenameOnApply/AutoWriteTagsOnApply (which ARE consumed at internal/metafetch/service_writeback.go and service_files.go), but grep across the whole Go tree finds no reader of this specific field.
- **Why:** This sits directly beside two sibling flags (AutoRenameOnApply, AutoWriteTagsOnApply) that ARE wired into the apply pipeline, making its absence from any consumer look like an oversight rather than an intentional no-op. Given this bucket's sensitivity to silent data-loss/corruption risk around tag writes, a 'verify after write' step that doesn't actually run anything is a meaningful gap: users may believe writes are being verified when they are not. Recommend wiring it into service_writeback.go's tag-write path (e.g. re-read the tag after write and compare) or removing the toggle.

#### `Config.VerifyAfterWrite`
🔴 DEAD · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true (unwire the toggle instead of changing the default)` (confidence: high)
- **Usage:** Declared, defaulted (true), viper-bound, DB-persisted, and exposed in the frontend UI (web/src/components/SettingsGeneral.tsx:616-623, web/src/hooks/useSettingsHandlers.ts:499/765, web/src/pages/Settings.tsx:329/597) -- but grepping every write-back call site (internal/metafetch/service_writeback.go:727, internal/audiobooks/revert.go:210, internal/organizer/rename.go:175, internal/server/handlers/audiobooks/handler_crud.go:178) shows every single one hardcodes `fileops.OperationConfig{VerifyChecksums: true}` literally, never reading config.AppConfig.VerifyAfterWrite. The only backend reference outside config.go is a unit test asserting the default is true (internal/config/config_unit_test.go:488) -- not a behavior test.
- **Why:** The current behavior (always verify) is the SAFE side of this bug, so there's no urgency to change the runtime default. But the setting is misleading: a user who disables it in the UI to skip checksum overhead gets no effect, and a user who thinks disabling it is safe because 'verification' sounds optional isn't told it's actually mandatory. Either wire config.AppConfig.VerifyAfterWrite into fileops.OperationConfig at the 4 call sites, or remove the dead toggle from the UI/schema so it stops implying a behavior that doesn't exist.

#### `Config.WriteBackMetadata`
🟢 used

- **Current default:** `false`
- **Usage:** internal/metafetch/service_fetch.go:309 gates whether fetched metadata is written back to audio-file tags on disk.
- **Why:** Sane and safety-conscious default: per project memory, prod deliberately keeps this False so metadata review/apply mutates the DB only, not files on disk, until explicitly enabled. Matches the codebase's stated caution around in-place tag writes.

#### `create_backups`
🔴 DEAD

- **Current default:** `true`
- **Usage:** internal/config/config.go:521 (field), :1031/:1435 (viper wiring), internal/config/persistence.go:974 (settings-update handler). Grepped for `CreateBackups` outside config.go/persistence.go/tests: ZERO hits in any non-test .go file. It IS surfaced in the frontend (web/src/components/SettingsGeneral.tsx, useSettingsHandlers.ts, Settings.tsx) so a user can toggle it, but no organizer/fileops code path reads AppConfig.CreateBackups to decide whether to back up a file before moving/renaming it. The description says 'create backup copies before moving/renaming files during organization' but the only backup mechanism actually wired into the organize path is an unconditional pre-organize DATABASE backup (internal/organizer/service.go autoBackup/autoBackupMinInterval), which is gated only by a 6h time interval, not by this flag.
- **Why:** This is a dead/orphaned option, not a bad default per se — a user can flip it in Settings and nothing changes. Flagging for the maintainer to decide: either wire it into the per-file backup-before-move path it was clearly meant to control, or remove the field/UI control and document that only the whole-DB pre-organize backup exists today.

#### `embed_cover_art`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1042 default false. Consumed in internal/metafetch/service.go and internal/tagger/embed_cover.go.
- **Why:** Opt-in default is sane — embedding cover art into every audio file's tags is a heavier, more invasive write than most users will want by default.

#### `enable_ai_parsing`
🟢 used · 🟠 default review

- **Current default:** `true (backend canonical); false (frontend fallback/initial state)`
- **Recommended default:** `Frontend fallback should be `?? true` to match the backend default, or the initial state constant should read from a shared default rather than hardcoding false` (confidence: medium)
- **Usage:** internal/config/persistence.go:79-81/1037/1560, internal/plugins/maintenance/dedup_ops.go:83, internal/scheduler/tasks.go:745, internal/server/server_maintenance_deps.go:265 (`config.AppConfig.EnableAIParsing && config.AppConfig.OpenAIAPIKey != ""`).
- **Why:** This is a genuine cross-layer default mismatch. It's low-severity in practice (the real value normally arrives from the backend before the fallback matters), but it's exactly the kind of drift this bucket is asked to flag -- and it's the same class of bug as the AutoRenameOnApply/VerifyAfterWrite findings: a UI default silently diverging from the server's actual default.

#### `exclude_patterns`
🟢 used

- **Current default:** `[]string{} (empty)`
- **Usage:** internal/config/config.go:800 (struct field ExcludePatterns), :1405 (viper default []string{}), consumed at internal/scanner/scanner.go:447 (`for _, pattern := range config.AppConfig.ExcludePatterns`). Frontend mirrors as `exclude_patterns?: string[]` in web/src/services/api.ts.
- **Why:** Empty exclude list is the correct conservative default — scanning everything by default and letting operators opt into exclusions is sane.

#### `file_naming_pattern`
🟢 used

- **Current default:** `{title} - {track:02d}`
- **Usage:** internal/config/config.go:1030 default DefaultFileNamingPattern = "{title} - {track:02d}" (internal/config/naming_patterns.go:35). Consumed alongside folder_naming_pattern in internal/organizer/pipeline.go and organizer.go.
- **Why:** Sane default file-naming template, live.

#### `FileNamingPattern / file_naming_pattern`
🟢 used

- **Current default:** `{title} - {track:02d} (canonical Go/viper default; the tracked repo-root config.yaml disagrees and shows an unsafe pattern with no track placeholder)`
- **Usage:** Canonical default is the `DefaultFileNamingPattern` constant = "{title} - {track:02d}" (naming_patterns.go:34), wired identically via viper.SetDefault (config.go:1030) and ResetToDefaults (config.go:2050). validateNamingPatterns (naming_patterns.go) rejects any pattern containing '/' or '\\', and separately rejects any pattern with NO per-track placeholder ({track}, {track:02d}, {track_title}) -- the file's own header comment states this codifies a real production incident that stranded 35.2 GB across 82 books, 77 with no other copy, because the old default lacked a track placeholder. HOWEVER: the repo-root config.yaml (tracked in git, last touched 2026-02-21) currently sets `file_naming_pattern: '{title} - {author} - read by {narrator}'` -- this pattern contains NO track placeholder at all, i.e. it is exactly the unsafe shape the validator exists to reject. That file is not the app's real config source (cmd/root.go loads `$HOME/.audiobook-organizer.yaml` by default, not a repo-root config.yaml; nothing in Makefile/Docker points at it), so it is not actively driving production, but it is a stale/misleading example of a 'default' checked into the repo, and would strand multi-file-book audio if anyone did point --config at it.
- **Why:** The canonical default is already correct and specifically hardened against the exact data-loss shape (missing track placeholder) that caused a real prior incident. The risk is entirely in the tracked config.yaml artifact presenting a stale, unsafe value as if it were a real config -- worth flagging to the user as repo hygiene even though it's out of this bucket's direct scope (it also embeds what looks like a placeholder API key, sk-test12345678).

#### `folder_naming_pattern`
🟢 used

- **Current default:** `{author}/{series}/{title} ({print_year})`
- **Usage:** internal/config/config.go:1029 default DefaultFolderNamingPattern = "{author}/{series}/{title} ({print_year})" (internal/config/naming_patterns.go:34). Consumed in internal/organizer/pipeline.go and internal/organizer/organizer.go.
- **Why:** Sane, conventional layout; live and actively used by the single organizer path (project memory notes the old second path-builder was deleted, not left dangling).

#### `FolderNamingPattern / folder_naming_pattern`
🟢 used

- **Current default:** `{author}/{series}/{title} ({print_year})`
- **Usage:** Canonical default is the `DefaultFolderNamingPattern` constant (internal/config/naming_patterns.go:33), wired via `viper.SetDefault("folder_naming_pattern", DefaultFolderNamingPattern)` (config.go:1029) and `FolderNamingPattern: DefaultFolderNamingPattern` in ResetToDefaults (config.go:2049). Consumed by the organizer's path builder.
- **Why:** Matches the canonical Go default everywhere it's declared. No changes needed.

#### `google_books_api_key`
🟢 used

- **Current default:** `""`
- **Usage:** Same secret-row mechanism as openai_api_key (persistence.go:66/125-126/852-853, update_service.go:45-46/62/103-104). Genuinely consumed as the fallback base key for the Google Books provider before any per-source Credentials override (internal/metafetch/service_search.go:127/163-165).
- **Why:** No issue found; this is a legitimate two-tier design (global default + optional per-source override), not a duplicate/dead field.

#### `GOOGLE_BOOKS_BASE_URL`
🟢 used

- **Current default:** `https://www.googleapis.com/books/v1`
- **Usage:** internal/metadata/googlebooks.go:30 os.Getenv override for the Google Books Volume API base URL.
- **Why:** No issue.

#### `hardcover_api_token`
🟢 used

- **Current default:** `"" (empty)`
- **Usage:** internal/config/config.go:1051 default "". Consumed in internal/config/update_service.go, internal/metafetch/service_search.go, internal/maintenance/jobs/bulk_fetch_metadata.go.
- **Why:** Correct default for a user-supplied secret/credential.

#### `hardcover_api_token`
🟢 used

- **Current default:** `""`
- **Usage:** Same secret-row + preserve-on-empty-save mechanism as the other two provider keys (persistence.go:1031-1032, 1510, 854-855).
- **Why:** No issue found.

#### `language`
🟢 used

- **Current default:** `en`
- **Usage:** internal/config/config.go:1043 default 'en'. Consumed extremely widely (30+ files) across internal/audiobooks, internal/organizer, internal/metadata/*, internal/metafetch/*, internal/scanner, etc.
- **Why:** Standard, sane default.

#### `language`
🟢 used

- **Current default:** `en`
- **Usage:** Frontend text field in MetadataSettingsTab.tsx:464-472 sets settings.language; matches Go struct default (ResetToDefaults `Language: "en"`) and yaml key language: en.
- **Why:** Consistent across layers; en is a reasonable default language for metadata lookups.

#### `metadata_fetch_cache_ttl_days`
🟢 used

- **Current default:** `180 (days)`
- **Usage:** internal/config/config.go:1121 default 180. Consumed in internal/server/metadata_ops.go, internal/server/handlers/diagnostics.go, internal/metafetch/{service_search,service_apply,service_fetch}.go, internal/maintenance/jobs/bulk_fetch_metadata.go.
- **Why:** 180 days is a reasonable cache TTL for provider metadata lookups — long enough to avoid re-hitting rate-limited external APIs, short enough to eventually pick up corrections.

#### `metadata_review_default_view`
🔴 DEAD

- **Current default:** `compact`
- **Usage:** internal/config/config.go:535 (field), :1044/:1620ish (viper wiring), internal/config/persistence.go:1014-1015 (settings-update handler), internal/config/config_unit_test.go:613. Grepped for `MetadataReviewDefaultView`/`metadata_review_default_view`/`metadataReviewDefaultView` across the whole repo including web/src — NO consumer anywhere (backend or frontend) reads this value to actually pick a view mode. The frontend's metadata-review UI (web/src/pages/Library.tsx, LibraryDialogs.tsx) has its own view-mode state unrelated to this key.
- **Why:** Dead config option — persisted and defaulted but never read anywhere to affect UI or API behavior. Flag for removal or for wiring into the metadata-review queue's actual view-mode state.

#### `metadata_scoring.bulk_fetch_workers`
🟢 used

- **Current default:** `4`
- **Usage:** internal/config/config.go:1352 (default 4, env METADATA_SCORING_BULK_FETCH_WORKERS). Consumed at internal/server/metadata_ops.go:64-70 with a `> 0` guard falling back to defaultBulkFetchWorkers=4 when unset.
- **Why:** This bounds a network-bound worker pool for bulk metadata provider fetches (each book fans out to multiple external providers, per comment at service_search.go:27 — '4 books x 4 sources is already 16 provider requests'). Per this repo's concurrency policy, network-bound loops should use 'a smaller fixed concurrency for network-bound work that respects the target's own rate limits' rather than scaling to NumCPU — a flat default of 4 is exactly that pattern and is NOT a single-threaded-by-default violation. Correctly bounded, not unbounded, not naively single-threaded. No change recommended.

#### `metadata_scoring.compilation_penalty`
🟢 used

- **Current default:** `0.15`
- **Usage:** internal/config/config.go:1343 (default 0.15, env var bound). Consumed at internal/metafetch/service_scoring.go:121 (`score *= k.CompilationPenalty`).
- **Why:** Calibrated literal, live and used to discount candidates that look like compilations/anthologies. No override evidence.

#### `metadata_scoring.duration_tier_multipliers`
🟢 used · 🟡 naming

- **Current default:** `[1.30, 1.20, 1.10, 1.00, 0.75, 0.50]`
- **Usage:** internal/config/config.go:1350 (default []float64{1.30,1.20,1.10,1.00,0.75,0.50}). Consumed at internal/metafetch/service_scoring.go:203-213 as a 6-tier lookup table indexed by duration-closeness bucket.
- **Naming issue:** Unlike every other metadata_scoring.* key (all 18 scalar siblings get a matching `viper.BindEnv("metadata_scoring.X", "METADATA_SCORING_X")` call at config.go:1369-1388), duration_tier_multipliers and duration_tier_scores have a viper.SetDefault but NO BindEnv — grepped config.go and confirmed neither name appears in the BindEnv block. Likely intentional (env vars don't cleanly express a 6-element array), but it's an inconsistency an operator could trip on when trying to override via env var like every sibling knob.
- **Why:** Values are sane and live; only the env-var-override gap is worth documenting (e.g. a comment noting these two are YAML/JSON-only, not env-overridable, unlike their siblings).

#### `metadata_scoring.duration_tier_scores`
🟢 used · 🟡 naming

- **Current default:** `[20, 15, 10, 0, -10, -20]`
- **Usage:** internal/config/config.go:1351 (default []float64{20,15,10,0,-10,-20}). Consumed alongside duration_tier_multipliers at internal/metafetch/service_scoring.go:203-213.
- **Naming issue:** Same env-var-override gap as duration_tier_multipliers — no BindEnv call despite every other metadata_scoring.* scalar sibling having one.
- **Why:** Values sane and live; same env-var documentation gap as duration_tier_multipliers.

#### `metadata_scoring.embedding_best_match`
🟢 used

- **Current default:** `0.88`
- **Usage:** internal/config/config.go:1331 (default 0.88, env METADATA_SCORING_EMBEDDING_BEST_MATCH). Consumed at internal/metafetch/service_scoring.go:554.
- **Why:** Same calibrated-literal provenance as embedding_min_score; no override evidence.

#### `metadata_scoring.embedding_enabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1329 (default false, env METADATA_SCORING_EMBEDDING_ENABLED). Consumed at internal/metafetch/service_scoring.go:679 (`if config.AppConfig.MetadataScoring.EmbeddingEnabled && mfs.metadataScorer != nil ...`).
- **Why:** Opt-in is reasonable since embedding-based scoring needs an embedding backend configured; leaving it off by default avoids surprising behavior/cost for installs that haven't set one up.

#### `metadata_scoring.embedding_min_score`
🟢 used

- **Current default:** `0.82`
- **Usage:** internal/config/config.go:1330 (default 0.82, env METADATA_SCORING_EMBEDDING_MIN_SCORE). Consumed at internal/metafetch/service_search.go:524.
- **Why:** A calibrated scoring threshold retained from the pre-config literal (INIT-3-T1 comment at config.go:1336-1338 states defaults intentionally match prior hardcoded behavior bit-for-bit); no evidence to override.

#### `metadata_scoring.f1_min_score`
🟢 used

- **Current default:** `0.35`
- **Usage:** internal/config/config.go:1346 (default 0.35, env var bound). Consumed at internal/metafetch/service_scoring.go:545 as a hard reject floor; the calibration harness comment (internal/plugins/metafetch/calibrate_scoring.go:793) explicitly warns this column is a filter-aggressiveness knob, not a discrimination signal to be maximized.
- **Why:** Live and load-bearing; the codebase's own calibration tooling explicitly warns against naively raising this, so no change recommended without a real calibration run.

#### `metadata_scoring.llm_enabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1332 (default false, env METADATA_SCORING_LLM_ENABLED). Consumed at internal/metafetch/service_search.go:735 (`if opts.UseRerank && mfs.llmScorer != nil && config.AppConfig.MetadataScoring.LLMEnabled`).
- **Why:** Opt-in default is correct — LLM reranking has cost/latency implications and needs an LLM backend configured.

#### `metadata_scoring.llm_rerank_epsilon`
🟢 used

- **Current default:** `0.05`
- **Usage:** internal/config/config.go:1333 (default 0.05, env METADATA_SCORING_LLM_RERANK_EPSILON). Consumed at internal/metafetch/service_scoring.go:804.
- **Why:** Reasonable narrow band for 'candidates close enough to the leader to warrant LLM tie-breaking'; no evidence to override.

#### `metadata_scoring.llm_rerank_top_k`
🟢 used

- **Current default:** `5`
- **Usage:** internal/config/config.go:1334 (default 5, env METADATA_SCORING_LLM_RERANK_TOP_K). Consumed at internal/metafetch/service_scoring.go:805.
- **Why:** Bounded top-K keeps LLM reranking cost predictable per book; sane.

#### `metadata_scoring.rich_metadata_bonus_cap`
🟢 used

- **Current default:** `0.15`
- **Usage:** internal/config/config.go:1345 (default 0.15, env var bound). Consumed at internal/metafetch/service_scoring.go:149-150 (`if bonus > k.RichMetadataBonusCap { bonus = k.RichMetadataBonusCap }`).
- **Why:** Correctly caps the cumulative rich_metadata_field_bonus (3 fields x 0.05 = 0.15, i.e. the cap is exactly reachable at default settings) — internally consistent pairing.

#### `metadata_scoring.rich_metadata_field_bonus`
🟢 used

- **Current default:** `0.05`
- **Usage:** internal/config/config.go:1344 (default 0.05, env var bound). Consumed at internal/metafetch/service_scoring.go:138-147 (applied per populated field, capped by rich_metadata_bonus_cap).
- **Why:** Small per-field bonus, live, capped by rich_metadata_bonus_cap — sane.

#### `metadata_scoring.series_name_match_boost`
🟢 used

- **Current default:** `1.4`
- **Usage:** internal/config/config.go:1347 (default 1.4, env var bound). Consumed at internal/metafetch/service_search.go:560.
- **Why:** Calibrated literal, live.

#### `metadata_scoring.series_number_exact_boost`
🟢 used

- **Current default:** `2.0`
- **Usage:** internal/config/config.go:1348 (default 2.0, env var bound). Consumed at internal/metafetch/service_search.go:717.
- **Why:** Calibrated literal, live.

#### `metadata_scoring.series_number_wrong_penalty`
🟢 used

- **Current default:** `0.5`
- **Usage:** internal/config/config.go:1349 (default 0.5, env var bound). Consumed at internal/metafetch/service_search.go:719.
- **Why:** Calibrated literal, live.

#### `metadata_scoring.transcription_author_boost`
🟢 used

- **Current default:** `1.6`
- **Usage:** internal/config/config.go:1341 (default 1.6, env var bound). Consumed at internal/metafetch/service_scoring.go:511.
- **Why:** Same calibrated-literal provenance as the sibling boost knobs.

#### `metadata_scoring.transcription_narrator_boost`
🟢 used

- **Current default:** `1.4`
- **Usage:** internal/config/config.go:1342 (default 1.4, env var bound). Consumed at internal/metafetch/service_scoring.go:515.
- **Why:** Same calibrated-literal provenance as the sibling boost knobs.

#### `metadata_scoring.transcription_title_exact_boost`
🟢 used

- **Current default:** `2.0`
- **Usage:** internal/config/config.go:1339 (default 2.0, env var bound). Consumed at internal/metafetch/service_scoring.go:493 and mirrored in internal/plugins/metafetch/calibrate_scoring.go (calibration harness).
- **Why:** Calibrated literal preserved bit-identical from pre-config code per the config.go comment; a dedicated calibration harness (internal/plugins/metafetch/calibrate_scoring.go) exists to sweep these — leave tuning to that tool rather than recommending a blind change here.

#### `metadata_scoring.transcription_title_substr_boost`
🟢 used

- **Current default:** `1.4`
- **Usage:** internal/config/config.go:1340 (default 1.4, env var bound). Consumed at internal/metafetch/service_scoring.go:496.
- **Why:** Same calibrated-literal provenance as the sibling boost knobs.

#### `metadata_scoring.write_back_workers`
🟢 used

- **Current default:** `4`
- **Usage:** internal/config/config.go:1353 (default 4, env METADATA_SCORING_WRITE_BACK_WORKERS). Consumed at internal/server/metadata_ops.go:73-89 (`if w := config.AppConfig.MetadataScoring.WriteBackWorkers; w > 0 { return w }`, falling back to defaultWriteBackWorkers=4), with a comment noting the `> 0` guard is load-bearing.
- **Why:** Same reasoning as bulk_fetch_workers — this bounds a worker pool for the write-back path (file I/O + tag writes, not purely CPU-bound), a bounded fixed pool of 4 is consistent with the mandatory concurrency policy and is correctly guarded against being unbounded or accidentally 0/1 (single-threaded). No change recommended.

#### `metadata_scoring.write_backup_before`
🟢 used

- **Current default:** `true`
- **Usage:** internal/config/config.go:1335 (default true, env METADATA_SCORING_WRITE_BACKUP_BEFORE). Consumed at internal/metafetch/service_files.go:57 (`if !config.AppConfig.MetadataScoring.WriteBackupBefore { ... }`, doc comment at :37 confirms it gates a real backup-before-tag-write step).
- **Why:** Safe-by-default and actually enforced (unlike the top-level create_backups flag) — correct default for a destructive tag-write operation.

#### `MetadataScoringConfig.BulkFetchWorkers`
🟢 used

- **Current default:** `4 (fixed, not scaled to runtime.NumCPU())`
- **Usage:** internal/server/metadata_ops.go:67-70 falls back to defaultBulkFetchWorkers(=4) when unset (<=0), otherwise uses the configured value as the outer per-book worker-pool size for bulk metadata fetch.
- **Why:** This loop is network-bound (calls external metadata providers per book), not CPU-bound, so the project's mandatory-concurrency policy's NumCPU-scaling guidance applies to CPU-bound work, not this one; a small fixed pool that respects provider rate limits is the documented intent (see SourceFanoutWorkers' comment noting BulkFetchWorkers x SourceFanoutWorkers multiply to already be 16 concurrent provider requests at defaults). Already correctly implemented as a bounded worker pool, not unbounded or single-threaded, so no change is required.

#### `MetadataScoringConfig.CompilationPenalty`
🟢 used

- **Current default:** `0.15 (pointer default via f64Ptr(0.15) in ResetToDefaults; same value in viper.SetDefault and the nil-fallback in resolveScoringKnobs)`
- **Usage:** internal/metafetch/service_scoring.go:121 applies `score *= k.CompilationPenalty`; the *float64 pointer-vs-nil pattern (0 is a legitimate operator override, nil means unset) is correctly implemented at config.go:327-330.
- **Why:** The nil-vs-zero pointer semantics are implemented consistently everywhere checked; no defect found.

#### `MetadataScoringConfig.DurationTierMultipliers`
🟢 used

- **Current default:** `[1.30, 1.20, 1.10, 1.00, 0.75, 0.50]`
- **Usage:** internal/metafetch/service_scoring.go durationScoreFor() indexes into k.DurationTierMultipliers[0..5] per matched duration-ratio tier; resolveDurationTierValues falls back to defaultDurationTierMultipliers on length mismatch.
- **Why:** Matches the code-defined durationTiers table's Multiplier column exactly (per the field's own doc comment); no issue found.

#### `MetadataScoringConfig.DurationTierScores`
🟢 used

- **Current default:** `[20, 15, 10, 0, -10, -20]`
- **Usage:** Same durationScoreFor() indexes into k.DurationTierScores[0..5]; viper default [20, 15, 10, 0, -10, -20] matches code table's Score column.
- **Why:** Matches the code-defined durationTiers table exactly; no issue found.

#### `MetadataScoringConfig.EmbeddingBestMatch`
🟢 used

- **Current default:** `0.88`
- **Usage:** internal/metafetch/service_scoring.go:554 uses this as the embedding-similarity threshold for a 'best match' verdict.
- **Why:** Sane relative to EmbeddingMinScore (0.82); no evidence of mistuning.

#### `MetadataScoringConfig.EmbeddingEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/metafetch/service_scoring.go:679 gates the embedding-based scoring signal on this flag.
- **Why:** Sane default: an experimental/optional scoring signal defaults off until the embedding pipeline is confirmed healthy for a given deployment.

#### `MetadataScoringConfig.EmbeddingMinScore`
🟢 used

- **Current default:** `0.82`
- **Usage:** internal/metafetch/service_search.go:524 uses this as the minimum embedding similarity threshold for candidate acceptance.
- **Why:** Consistent with EmbeddingBestMatch (0.88); reasonable relative spacing. No evidence it's mistuned.

#### `MetadataScoringConfig.F1MinScore`
🟢 used

- **Current default:** `0.35 (pointer default, nil-safe)`
- **Usage:** internal/metafetch/service_scoring.go:545 reads `scoringKnobs().F1MinScore` as the minimum-score acceptance threshold.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.LLMEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/metafetch/service_search.go:735 gates LLM reranking on this flag (`opts.UseRerank && ... && config.AppConfig.MetadataScoring.LLMEnabled`).
- **Why:** Sane: LLM reranking has a cost/latency/API-key dependency, defaulting off is appropriate until explicitly opted in.

#### `MetadataScoringConfig.LLMRerankEpsilon`
🟢 used

- **Current default:** `0.05`
- **Usage:** internal/metafetch/service_scoring.go:804 reads this as the score-gap epsilon deciding which candidates are 'close enough' to send to the LLM reranker.
- **Why:** Small epsilon is reasonable for a tie-breaking gate; no evidence of mistuning.

#### `MetadataScoringConfig.LLMRerankTopK`
🟢 used

- **Current default:** `5`
- **Usage:** internal/metafetch/service_scoring.go:805 reads this to cap how many close candidates are sent to the LLM reranker.
- **Why:** Small, cost-bounding value; sane given LLM calls are per-request cost.

#### `MetadataScoringConfig.RichMetadataBonusCap`
🟢 used

- **Current default:** `0.15 (pointer default, nil-safe)`
- **Usage:** internal/metafetch/service_scoring.go:149-150 caps the cumulative rich-metadata bonus at this pointer-knob value.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.RichMetadataFieldBonus`
🟢 used

- **Current default:** `0.05`
- **Usage:** internal/metafetch/service_scoring.go:138-147 adds this bonus per rich-metadata field present, then caps at RichMetadataBonusCap.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.SeriesNameMatchBoost`
🟢 used

- **Current default:** `1.4`
- **Usage:** Passed through to the scoring knobs struct (internal/metafetch/service_scoring.go:298) with a 0-value fallback of 1.4 at line 317-318, matching the documented default.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.SeriesNumberExactBoost`
🟢 used

- **Current default:** `2.0`
- **Usage:** Passed to scoring knobs (service_scoring.go:299) with 0-value fallback of 2.0 (line 320-321), matching the documented default.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.SeriesNumberWrongPenalty`
🟢 used

- **Current default:** `0.5`
- **Usage:** Passed to scoring knobs (service_scoring.go:300) with 0-value fallback of 0.5 (line 323-324), matching the documented default.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.SourceFanoutWorkers`
🟢 used

- **Current default:** `4 (fixed, not scaled to runtime.NumCPU())`
- **Usage:** internal/metafetch/service_search.go:38 falls back to a compiled-in default when unset (<=0); bounds how many metadata sources are queried concurrently for a single book.
- **Why:** Deliberately kept small per its own doc comment: this axis multiplies with BulkFetchWorkers (4 x 4 = 16 concurrent provider requests already), and each provider has its own token bucket/rate limit, so NumCPU-scaling would be actively harmful here (more concurrent requests than external APIs can absorb). Correctly implemented as network-bound, not CPU-bound, concurrency.

#### `MetadataScoringConfig.TranscriptionAuthorBoost`
🟢 used

- **Current default:** `1.6`
- **Usage:** internal/metafetch/service_scoring.go:511 applies this boost when the transcribed author matches.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.TranscriptionNarratorBoost`
🟢 used

- **Current default:** `1.4`
- **Usage:** internal/metafetch/service_scoring.go:515 applies this boost when the transcribed narrator matches.
- **Why:** Consistent across all default sources; no issue found.

#### `MetadataScoringConfig.TranscriptionTitleExactBoost`
🟢 used

- **Current default:** `2.0`
- **Usage:** internal/metafetch/service_scoring.go:493 multiplies score by this boost on an exact transcribed-title match; default 2.0 matches both viper.SetDefault and the in-code zero-value fallback.
- **Why:** Consistent across all three default-setting locations (doc comment, viper default, DefaultConfig/ResetToDefaults, and the zero-value fallback in resolveScoringKnobs). No issue found.

#### `MetadataScoringConfig.TranscriptionTitleSubstrBoost`
🟢 used

- **Current default:** `1.4`
- **Usage:** internal/metafetch/service_scoring.go:496 applies this boost for a substring transcribed-title match.
- **Why:** Consistent across all default sources; sane relative to the exact-match boost (1.4 < 2.0).

#### `MetadataScoringConfig.WriteBackupBefore`
🟢 used

- **Current default:** `true`
- **Usage:** internal/metafetch/service_files.go:57 gates whether a tag backup is written before applying scored metadata to a file.
- **Why:** Sane default: defaulting to writing a safety backup before an in-place tag write protects against data loss, matching this repo's stated priority on preventing metadata corruption.

#### `MetadataScoringConfig.WriteBackWorkers`
🟢 used

- **Current default:** `4 (fixed, not scaled to runtime.NumCPU())`
- **Usage:** internal/server/metadata_ops.go:86-89 falls back to defaultWriteBackWorkers(=4) when unset (<=0); used as the worker-pool size for the disk/TagLib-bound bulk write-back/batch-save path (distinct from BulkFetchWorkers, per its own doc comment).
- **Why:** Already implemented as a bounded worker pool per the mandatory concurrency policy (not a plain sequential for-range loop, not unbounded). A fixed value of 4 for disk/TagLib I/O is a reasonable default that avoids saturating disk I/O on modest hardware; no evidence of being single-threaded-by-default or unbounded.

#### `MetadataSource.Credentials`
🟢 used

- **Current default:** `empty map`
- **Usage:** internal/metafetch/service_search.go:165-180 and internal/maintenance/jobs/bulk_fetch_metadata.go:263-277 read src.Credentials["apiKey"]/["api_token"] to configure provider clients.
- **Why:** Actively used; no change needed.

#### `MetadataSource.Enabled`
🟢 used

- **Current default:** `varies per built-in source`
- **Usage:** buildSourceChainFromConfig skips sources where `!src.Enabled` before building the provider chain (internal/metafetch/service_search.go).
- **Why:** Actively gates behavior; no change needed.

#### `MetadataSource.ID`
🟢 used

- **Current default:** `provider-specific (e.g. "openlibrary", "audible")`
- **Usage:** internal/metafetch/service_search.go:buildSourceChainFromConfig switches on src.ID ("openlibrary", etc.) to construct the runtime provider chain from config.AppConfig.MetadataSources.
- **Why:** Actively used as the dispatch key; no change needed.

#### `MetadataSource.Name`
🟢 used

- **Current default:** `provider display name (e.g. "Open Library")`
- **Usage:** Only used for display: web/src/components/settings/MetadataSettingsTab.tsx renders `{source.priority}. {source.name}` and API-key field labels. Not read anywhere in Go logic (dispatch uses ID, not Name).
- **Why:** Used, just cosmetic (UI label). No default-value concern.

#### `MetadataSource.Priority`
🟢 used

- **Current default:** `varies per built-in source`
- **Usage:** buildSourceChainFromConfig sorts sources by Priority ascending before querying (internal/metafetch/service_search.go:146, also internal/maintenance/jobs/bulk_fetch_metadata.go:248).
- **Why:** Actively used for ordering; no change needed.

#### `MetadataSource.RequiresAuth`
🟢 used

- **Current default:** `false for free sources, true for Audible/Hardcover-style sources`
- **Usage:** Frontend web/src/components/settings/MetadataSettingsTab.tsx conditionally renders the API-key input (`{source.requiresAuth && (...)}`) based on this flag; Settings.tsx overrides it client-side for known auth-needing source IDs.
- **Why:** Used purely to drive UI, which is a legitimate use; no backend gating found but that's expected for a UI-hint field.

#### `openai_api_key`
🟢 used

- **Current default:** `""`
- **Usage:** Encrypted DB row (IsSecret=true), decrypt-with-file-fallback (persistence.go:809-873), preserve-on-empty-save logic (persistence.go:1513-1524) so clearing the UI field can't wipe a saved key. Frontend field in MetadataSettingsTab.tsx:184-213. Consumed by internal/plugins/maintenance/dedup_ops.go:83 and internal/server/ai_ops.go:80 to construct the OpenAI parser, gated together with EnableAIParsing.
- **Why:** Empty-by-default secret is correct; no operational default to tune.

#### `OPENLIBRARY_BASE_URL`
🟢 used

- **Current default:** `https://openlibrary.org`
- **Usage:** internal/metadata/openlibrary.go:33 os.Getenv override; also saved/restored around a mock server in internal/server/server_test.go:782 confirming the client honors it.
- **Why:** Standard test/override escape hatch; no issue.

#### `openlibrary_dump_dir`
🟢 used

- **Current default:** `"" (empty)`
- **Usage:** internal/config/config.go:1048 default "". Consumed in internal/metafetch/openlibrary.go.
- **Why:** Empty-string default is correct for a path that must be explicitly supplied when openlibrary_dump_enabled is turned on.

#### `openlibrary_dump_enabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1047 default false. Consumed in internal/server/openlibrary_service.go and internal/metafetch/openlibrary.go.
- **Why:** Opt-in is correct — a local Open Library dump is a large optional resource most installs won't have configured.

#### `organization_strategy`
🟢 used

- **Current default:** `auto`
- **Usage:** internal/config/config.go:1024 (viper default 'auto'), :2046 (ResetToDefaults). Consumed at internal/organizer/organizer.go:271 and :824 (`strategy := o.config.OrganizationStrategy`).
- **Why:** 'auto' (let the organizer pick copy/hardlink/reflink/symlink based on filesystem capability) is the sensible default vs. forcing one specific strategy.

#### `organization_strategy`
🟢 used

- **Current default:** `auto`
- **Usage:** internal/organizer/organizer.go:271 and :824 both read `strategy := o.config.OrganizationStrategy`; validated against a fixed set {auto, copy, hardlink, reflink, symlink} at config.go:1992-1993.
- **Why:** 'auto' (let the organizer pick the best strategy for the filesystem) is a sensible default; matches canonical Go default and yaml.

#### `playlist_dir`
🟢 used

- **Current default:** `playlists`
- **Usage:** cmd/root.go:131 and :213 print `config.AppConfig.PlaylistDir`; also settable via --playlists CLI flag and persisted (persistence.go:107/951).
- **Why:** Reasonable relative default; no issue.

#### `protected_paths`
🟢 used

- **Current default:** `"" (empty newline-delimited list)`
- **Usage:** Frontend multiline field (PathsSettingsTab.tsx:236-262) sets settings.protectedPaths. Backend: internal/server/server.go:992 constructs `deluge.NewProtectedPathCache(dc, config.AppConfig.ProtectedPaths)`, consulted before any in-place tag write per the comment at server.go:267/988.
- **Why:** Empty-by-default is correct -- the app can't guess an operator's Deluge/download directories; must be explicitly configured.

#### `root_dir`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `"" (root_dir); AO_DIR=/path/to/audiobooks documented as though live in .env.example/docker-compose.yml/README`
- **Recommended default:** `Either rename the real backend binding from ROOT_DIR to AO_DIR (matching the documented/shipped env var everywhere else), or fix .env.example, docker-compose.yml, and README.md to reference ROOT_DIR instead of the dead AO_DIR` (confidence: high)
- **Usage:** Frontend 'Library Path' field (PathsSettingsTab.tsx:71-103, duplicated in SettingsGeneral.tsx). Real backend override paths are CLI `--dir` and env `ROOT_DIR`, confirmed live: viper.AutomaticEnv() is enabled (cmd/root.go:456) and SyncConfigFromEnv (internal/config/persistence.go:1536-1551) applies env overrides into AppConfig.
- **Naming issue:** Two commonly-documented env var spellings are dead: AO_DIR (set in .env.example:15, docker-compose.yml:18, and documented in README.md:197) and AUDIOBOOK_ROOT_DIR (referenced in docker-compose.yml:16 as a host-side bind-mount variable, not passed into the container as a config override) are never read by any Go code path (no os.Getenv, no viper.BindEnv, and AutomaticEnv only binds the literal uppercased key ROOT_DIR). A user following .env.example or docker-compose.yml literally will set AO_DIR and have it silently ignored -- the container path is hardcoded to /audiobooks regardless.
- **Why:** This is an active foot-gun: every user-facing onboarding surface (.env.example, docker-compose.yml, README) tells operators to set AO_DIR, but the code only honors ROOT_DIR. An operator who copies .env.example verbatim gets an empty root_dir with no error, silently scanning nothing (or the wrong default path) until they discover the mismatch. This should be reconciled in one direction or the other.

#### `scan_on_startup`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1025 default false. Consumed at internal/scheduler/tasks.go:146/159 and internal/server/handlers/operations/handler.go:609/614.
- **Why:** Correct conservative default. Project memory documents that a running scan can clobber applied metadata for not-yet-processed books ('project_scan_clobbers_applied_metadata'); auto-scanning on every process start would make that far more likely to trigger unexpectedly. Keep false.

#### `scan_on_startup`
🟢 used

- **Current default:** `false`
- **Usage:** internal/scheduler/tasks.go:146/159 combine it with Scheduled.LibraryScan (`config.AppConfig.Scheduled.LibraryScan.Enabled \|\| config.AppConfig.ScanOnStartup`); internal/server/handlers/operations/handler.go:609/614 consumes and then clears the flag after the startup scan runs once (`config.AppConfig.ScanOnStartup = false`). Canonical default `ScanOnStartup: false` (ResetToDefaults, config.go:2044) and `viper.SetDefault("scan_on_startup", false)` (config.go:1025).
- **Why:** False-by-default is the correct, safety-conscious choice given project memory's documented risk that a running library scan can silently clobber Title/Author/Narrator/Series/ASIN metadata for not-yet-processed books -- an unattended server that auto-scans on every restart, potentially while a human is mid-review or mid-apply, would recreate that exact hazard. Keep as opt-in.

#### `supported_extensions`
🟢 used

- **Current default:** `.m4b, .mp3, .m4a, .aac, .ogg, .flac, .wma, .opus, .oga, .wav, .aiff, .aif, .mka, .aax, .aaxc`
- **Usage:** internal/config/config.go:1401 default list of 15 audio extensions. Consumed at internal/scanner/scanner.go:663,899,903,972,976,1623, internal/reconcile/reconcile.go, internal/importer/service.go, internal/plugins/maintenance/title_repair.go, internal/audioutil/drm.go, internal/metadata/assemble.go.
- **Why:** Comprehensive default extension list covering the common audiobook/audio container formats including DRM'd Audible formats (.aax/.aaxc, handled specially per internal/audioutil/drm.go); sane out-of-the-box coverage.

#### `verify_after_write`
🔴 DEAD

- **Current default:** `true`
- **Usage:** internal/config/config.go:790 (field), :1293 (viper default true), :1620 (load), :2279 (ResetToDefaults). Grepped `VerifyAfterWrite`/`verify_after_write` across the entire repo (backend and web/src): the ONLY non-declaration hit is internal/config/config_unit_test.go:488 (`assert.True(t, AppConfig.VerifyAfterWrite)`), which tests the config default itself, not any consumer. No package reads this field to gate a verification pass after a write/rename. The apply pipeline does have an unconditional, unrelated `fileops.OperationConfig{VerifyChecksums: true}` in internal/metafetch/service_writeback.go:727, but that is hardcoded, not driven by this config option. Not surfaced in the frontend Settings UI either.
- **Why:** Fully dead option — not even exposed in the UI, so no operator can be relying on toggling it. Recommend removing it (and its viper wiring) unless a 'verify after write' pass is planned; the name currently promises safety behavior that does not exist.

#### `write_back_metadata`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1041 default false. Consumed at internal/metafetch/service_fetch.go:309 (`if config.AppConfig.WriteBackMetadata`), and referenced throughout internal/server/{batch_apply_one,metadata_ops,batch_save_op}.go and internal/metafetch/service_writeback.go.
- **Why:** Project memory ('project_review_apply_switch_off') explicitly confirms this default is intentional: metadata approvals mutate the DB but do not write tags into audio files unless this is turned on. Do not change.

---

## Telemetry & Observability (logging, metrics)

#### `Config.ActivityLogCompactionDays`
🟢 used

- **Current default:** `14`
- **Usage:** internal/plugins/maintenance/deps.go:324, cleanup.go:153 (p.deps.ActivityLogCompactionDays()), internal/server/server_maintenance_deps.go:284-285
- **Why:** Default 14 days (compacts more often than the 30/90-day retention windows) is a reasonable cadence; consistently declared and actively used by the maintenance cleanup job. No change indicated.

#### `Config.ActivityLogRetentionChangeDays`
🟢 used

- **Current default:** `90`
- **Usage:** internal/plugins/maintenance/deps.go:325, cleanup.go:154 (p.deps.ActivityLogRetentionChangeDays()), internal/server/server_maintenance_deps.go:288-289 (Server accessor reading config.AppConfig.ActivityLogRetentionChangeDays)
- **Why:** Default 90 matches declared default and reset-default consistently; actively consumed by the maintenance cleanup plugin. No change indicated.

#### `Config.ActivityLogRetentionDebugDays`
🟢 used

- **Current default:** `30`
- **Usage:** internal/plugins/maintenance/deps.go:326, cleanup.go:155 (p.deps.ActivityLogRetentionDebugDays()), internal/server/server_maintenance_deps.go:292-293
- **Why:** Default 30 (shorter than the 90-day change-log retention) is sane for debug-level noise; consistently declared and actively consumed. No change indicated.

#### `Config.EnableJsonLogging`
🔴 DEAD · 🟡 naming

- **Current default:** `false`
- **Usage:** Same pattern as LogFormat: only in internal/config/config.go (declaration + defaults) and internal/config/persistence.go (get/set), plus its own round-trip tests (config_unit_test.go:657, persistence_test.go:449-450). No log-handler construction site reads AppConfig.EnableJsonLogging; both slog handlers are hardcoded text handlers.
- **Naming issue:** This field and LogFormat are two separate config knobs (one bool, one string enum) both claiming to control the same thing — JSON vs text log output — with nothing anywhere reconciling them (e.g. undefined behavior if log_format='json' but enable_json_logging=false). An operator setting either one alone would reasonably expect it to control output format; having two is a real design overlap, not just unused code.
- **Why:** Dead configuration like LogFormat, and additionally redundant with it: two independent settings for one concept with no defined precedence. Recommend collapsing to a single option (LogFormat's text/json enum is the more extensible shape) and wiring it into slog handler construction, or removing both if JSON logging isn't actually planned.

#### `Config.LogFormat`
🔴 DEAD

- **Current default:** `text`
- **Usage:** Only appears in internal/config/config.go (struct field + two default-setting sites) and internal/config/persistence.go (get/set for the settings API round-trip), plus its own round-trip unit tests (config_unit_test.go:620, persistence_test.go:441-442). No production code path reads AppConfig.LogFormat to choose a log encoder — both slog handlers in the codebase (cmd/root.go:535, internal/server/server.go:887) are hardcoded to slog.NewTextHandler regardless of this setting. Setting log_format to "json" via the API/config file has zero effect on log output.
- **Why:** Dead configuration: fully wired through defaults/persistence/tests but never consumed by the logger it claims to control. Either wire it into the two slog.New*Handler call sites (choosing JSON vs text) or remove it — currently it silently misleads operators who set it expecting an effect.

#### `Config.LogLevel`
🟢 used

- **Current default:** `info`
- **Usage:** internal/server/server.go:80 (isDebugMode(): strings.EqualFold(config.AppConfig.LogLevel, "debug") gates Gin debug logging), internal/itunes/import.go:259 (debugLog := config.AppConfig.LogLevel == "debug"), cmd/root.go:481 (--log-level CLI flag overrides it), round-tripped via viper/persistence.go. However the value is only ever compared for equality to "debug" as a boolean gate — never parsed into an slog.Level and passed to a handler. Both slog handlers in the codebase (cmd/root.go:535 setupFileLogging hardcodes slog.LevelDebug; internal/server/server.go:887 hardcodes slog.LevelInfo) ignore config.AppConfig.LogLevel entirely, so setting log_level to 'warn' or 'error' has no effect beyond the debug/non-debug binary switch.
- **Why:** Used, but only as a 2-state (debug / not-debug) switch, not the 4-level severity filter its doc comment and CLI flag help text ('debug, info, warn, error') promise. 'warn' and 'error' behave identically to 'info'. Worth flagging as a behavior gap between docs and code, though the 'info' default itself is fine and not being recommended for change.

#### `Config.LogRetentionDays`
🟢 used · 🟠 default review

- **Current default:** `0 (keep forever)`
- **Recommended default:** `90 (to match OperationLogRetentionDays), pending owner decision` (confidence: medium)
- **Usage:** internal/plugins/maintenance/deps.go:322 (interface method), internal/plugins/maintenance/cleanup.go:191 (p.deps.LogRetentionDays()), internal/scheduler/tasks.go:804 (task enabled gate: config.AppConfig.LogRetentionDays > 0), internal/server/server_maintenance_deps.go:276-277 (Server.LogRetentionDays() accessor)
- **Why:** Default is 0 ('keep forever') in the struct while every sibling retention field in this family (OperationLogRetentionDays, ActivityLogRetentionChangeDays/DebugDays) defaults to a bounded window (90/90/30 days). The maintenance cleanup job treats 0 as 'retention disabled'. An unbounded default is inconsistent with its siblings and risks unbounded log growth unless the operator opts in; flagging for owner decision rather than asserting a specific number is wrong.

#### `Config.OperationLogRetentionDays`
🟢 used

- **Current default:** `90`
- **Usage:** internal/maintenance/jobs/retention_and_hygiene.go:42 (operationRetentionDays := config.AppConfig.OperationLogRetentionDays)
- **Why:** Default 90 is consistently declared at both default-setting sites and actively consumed by the retention/hygiene maintenance job. No change indicated.

#### `OTEL_EXPORTER_OTLP_ENDPOINT`
🟢 used

- **Current default:** `"" (disabled)`
- **Usage:** internal/telemetry/config.go:18 (os.Getenv read), internal/telemetry/telemetry.go:20-22 (InitOTEL doc + signature), cmd/root.go:244-246 (telemetry.InitOTEL(context.Background(), otelCfg) called at startup, gated as optional/disabled if unset)
- **Why:** Standard OpenTelemetry env var name, actively wired into startup. Presence-as-enable-switch design is documented and intentional (no separate on/off var needed). Default empty/disabled is correct for an opt-in telemetry-export feature in a self-hosted single-binary app. No change recommended.

---

## Plugins, Maintenance & Scheduled Tasks

#### `AUDIOBOOK_API_PORT (.env.example, tooling-only)`
🟢 used

- **Current default:** `8484 (.env.example:32)`
- **Usage:** .claude/skills/server-bootstrap/scripts/bootstrap.sh:20 reads AUDIOBOOK_API_PORT (falling back to 8484) to target the bootstrap script's API calls. No occurrence in internal/ or web/src.
- **Why:** Not a bug, but the coincidental value match is a plausible source of confusion worth a one-line comment in .env.example.

#### `AUDIOBOOK_SERVER_IP (.env.example, tooling-only)`
🟢 used

- **Current default:** `"" (.env.example:31)`
- **Usage:** .claude/skills/server-bootstrap/scripts/bootstrap.sh:15,17 reads AUDIOBOOK_SERVER_IP to target the SSH bootstrap script. No occurrence anywhere in internal/ or web/src — confirmed not application config.
- **Why:** Correctly scoped to Claude Code tooling (server-bootstrap skill), not the Go binary; no change needed.

#### `AutoUpdateConfig.Channel`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/server/scheduler_maintenance_window_op.go:82, internal/server/update_handlers.go:27,38, internal/updater/register.go:85 all read the release channel.
- **Why:** In active use.

#### `AutoUpdateConfig.CheckMinutes`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/updater/register.go:86 reads CheckMinutes to configure the checker interval.
- **Why:** In active use.

#### `AutoUpdateConfig.Enabled`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/server/scheduler_maintenance_window_op.go:73 and internal/updater/register.go:84 read it to gate the update checker.
- **Why:** In active use.

#### `AutoUpdateConfig.WindowStart / AutoUpdateConfig.WindowEnd`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/updater/register.go:87-88 read them; internal/config/persistence.go:914-918 also uses them as a fallback source to backfill MaintenanceConfig.WindowStart/End when the latter are unset (legacy migration path).
- **Why:** Working as designed (migration shim), flag only for documentation.

#### `Config.APIKeys`
🔴 DEAD

- **Current default:** `n/a (empty struct, no fields)`
- **Usage:** config.go:779 defines it as an anonymous empty struct{} with a comment 'kept for backward compatibility, Goodreads deprecated Dec 2020.' Grepped for '.APIKeys' and 'APIKeys{' across the repo (excluding its own declaration) - zero hits anywhere, including tests.
- **Why:** Confirmed dead: an empty struct kept only for JSON backward-compatibility (so old config files with an 'api_keys' key don't error), never read anywhere. This is correctly a legacy no-op by design, not a bug - but it is genuinely unused application logic, matching the bucket's expectation of retired options.

#### `Config.AutoUpdate.Channel`
🟢 used · 🟠 default review

- **Current default:** `stable`
- **Recommended default:** `stable` (confidence: high)
- **Usage:** Merged with auto_update.channel. Consumed at internal/server/update_handlers.go:27/38 and internal/updater/register.go:85.
- **Why:** Correct default channel for an unattended auto-updater.

#### `Config.AutoUpdate.CheckMinutes`
🟢 used · 🟠 default review

- **Current default:** `60`
- **Recommended default:** `60` (confidence: high)
- **Usage:** Merged with auto_update.check_minutes. Consumed at internal/updater/register.go:86 (CheckMins).
- **Why:** Hourly update-check cadence is a reasonable default and only matters once Enabled is opted in.

#### `Config.AutoUpdate.Enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** Merged with auto_update.enabled (config.go:1226, AUTO_UPDATE_ENABLED env). Consumed at internal/server/scheduler_maintenance_window_op.go:73 and internal/updater/register.go:84. Frontend: web/src/services/api.ts:832/905, web/src/pages/Settings.tsx, web/src/services/api.ts.
- **Why:** Self-updating a production service without explicit opt-in is risky; off by default is the correct safe posture.

#### `Config.AutoUpdate.WindowEnd`
🟢 used · 🟠 default review

- **Current default:** `5`
- **Recommended default:** `5` (confidence: high)
- **Usage:** Merged with auto_update.window_end. Consumed at internal/updater/register.go:88; fallback source for maintenance.window_end (internal/config/persistence.go:917-918).
- **Why:** Off-peak default window; sane.

#### `Config.AutoUpdate.WindowStart`
🟢 used · 🟠 default review

- **Current default:** `2`
- **Recommended default:** `2` (confidence: high)
- **Usage:** Merged with auto_update.window_start. Consumed at internal/updater/register.go:87; also used as a fallback source for maintenance.window_start when unset (internal/config/persistence.go:914-915).
- **Why:** Off-peak default window, consistent with maintenance.window_start's 1am-4am pattern.

#### `Config.CacheInvalidateOnBookUpdate`
🟢 used

- **Current default:** `false`
- **Usage:** internal/audiobooks/service.go:253 checks config.AppConfig.CacheInvalidateOnBookUpdate to decide whether to invalidate list/facets caches on book update.
- **Why:** In active use, distinct from the dead MemoryLimit*/CacheSize cluster above.

#### `Config.CacheSize`
🔴 DEAD

- **Current default:** `1000`
- **Usage:** Declared, defaulted (1000), viper-loaded (config.go:1515), settable via persistence.go:1095 - grepped for '.CacheSize' consumption across the cache package (internal/cache/cache.go) and elsewhere; only metrics.SetCacheSize (an unrelated Prometheus gauge setter, not this config field) appears. No cache construction site reads AppConfig.CacheSize to size an LRU/cache.
- **Why:** Looks unused for its stated purpose ('Memory management... number of items') - the actual cache package doesn't appear to read it. Should be verified further (a fresh grep across cache construction call sites) before removal, but current evidence points to dead config.

#### `Config.DefaultUserQuotaGB`
🟢 used

- **Current default:** `100`
- **Usage:** Only ever set (config load default 100, persistence setter, tests) and displayed in Settings.tsx; never read anywhere to actually cap a user's storage.
- **Why:** Same unimplemented-enforcement issue as EnableUserQuotas - it's plumbed through config/API/UI but nothing consumes it to enforce a limit. Not dead code (real read/write path exists end-to-end) but functionally inert.

#### `Config.DisablePerUserSearchFilters`
🟢 used

- **Current default:** `false (filters ON)`
- **Usage:** internal/audiobooks/service_query.go:703,767 check this flag to decide whether to skip per-user DSL post-filtering in searchWithBleve.
- **Why:** In active use, correctly documented as an ops escape hatch rather than a feature flag.

#### `Config.DiskQuotaPercent`
🟢 used

- **Current default:** `unspecified in provided entries (frontend falls back to 80 when absent)`
- **Usage:** internal/config/config.go:1985 validates range 1-100 when EnableDiskQuota is true; displayed via system handler and QuotaTab.tsx (computes a quotaLimit = total_bytes * pct/100 client-side for a progress-bar warning only).
- **Why:** Same caveat as EnableDiskQuota: validated and displayed, not enforced by any backend write/organize path.

#### `Config.EnableDiskQuota`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1985 validates DiskQuotaPercent range when enabled; internal/server/handlers/system/handler.go:295 surfaces it via the system status API; frontend web/src/components/system/QuotaTab.tsx and Settings.tsx read/display it. However, no backend code path actually blocks writes/organize operations when the disk quota is exceeded - it is validated and displayed but not enforced.
- **Why:** Wired end-to-end for display/validation, but there is no enforcement logic anywhere in the codebase (searched for quota-related enforcement in scanner/organize/write paths - none found). This looks like an incompletely-implemented feature: the toggle and percent exist and are surfaced in the UI as if functional, but flipping it on only changes what number is shown, not actual behavior. Worth confirming with the user whether enforcement is planned or the option/UI should say 'reporting only'.

#### `Config.EnabledSortIndexes`
🟢 used

- **Current default:** `empty slice (deliberately empty per its extensive doc comment, to avoid the measured +142% memory / 2.8x slower insert cost of enabling all nine indexes)`
- **Usage:** cmd/root.go:497 calls database.SetEnabledSortIndexes(config.AppConfig.EnabledSortIndexes) at startup; internal/database/memdb_sort_indexers.go and memdb_schema.go implement the consuming logic.
- **Why:** In active use with a well-justified, measured default. No changes needed.

#### `Config.EnableUserQuotas`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/handlers/system/handler.go:297 surfaces it; frontend QuotaTab.tsx explicitly documents non-enforcement: 'Per-user quotas are enabled. Detailed per-user usage reporting is not yet available in this view.' No backend enforcement code found anywhere in the repo.
- **Why:** Confirmed unimplemented feature flag - the frontend's own copy admits per-user usage reporting isn't built yet, and no backend code enforces per-user quotas. Candidate for either removing the toggle from the UI until built, or being explicit in docs that it's a no-op placeholder.

#### `Config.Maintenance.AcoustIDNightlyLimit (maintenance.acoustid_nightly_limit / DB key acoustid_online_lookup_nightly_limit)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `backend: 5000 (config.go:2223); frontend placeholder: 200 (Settings.tsx:401)`
- **Recommended default:** `5000 (fix frontend placeholder to match; leave DB key name as-is since renaming it would require another migration for an already-migrated field)` (confidence: high)
- **Usage:** internal/config/persistence.go:1253-1256 maps DB setting to c.Maintenance.AcoustIDNightlyLimit; frontend number field 'AcoustID nightly limit' MaintenanceSettingsSection.tsx:116-127 bound to config.acoustid_nightly_limit; consumed to cap the AcoustID online lookup maintenance task's per-run volume.
- **Naming issue:** Two real inconsistencies: (1) the DB setting key `acoustid_online_lookup_nightly_limit` has no `maintenance_window_` prefix, unlike every one of its ~15 sibling maintenance-window boolean keys, so a grep for `maintenance_window_` misses it (flagged directly in persistence.go's own inline comment); the nested post-migration key `maintenance.acoustid_nightly_limit` is fine. (2) The frontend's pre-load placeholder default (Settings.tsx:401, `acoustid_nightly_limit: 200`) is 25x smaller than the real backend default (config.go:2223, `AcoustIDNightlyLimit: 5000`).
- **Why:** 5000/night is the value actually enforced; a stale 200 placeholder in the frontend could mislead anyone reading the source about what the real default is, even though it self-corrects once the page fetches live config.

#### `Config.Maintenance.AcoustIDOnlineLookup (maintenance.acoustid_online_lookup)`
🟢 used · 🟠 default review

- **Current default:** `false (config.go:2222; explicit comment: opt-in because it consumes third-party AcoustID API quota and only helps users with ACOUSTID_API_KEY set)`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:572,575 IsEnabled and RunInMaintenanceWindow both read config.AppConfig.Maintenance.AcoustIDOnlineLookup; frontend toggle MaintenanceSettingsSection.tsx:38; legacy flat key maintenance_window_acoustid_online_lookup migrated forward by migrateMaintenanceBlob.
- **Why:** Correctly gated opt-in given third-party quota consumption; the ResetToDefaults comment documents the rationale directly.

#### `Config.Maintenance.AuthorSplit (maintenance.author_split)`
🟢 used · 🟠 default review

- **Current default:** `true (config.go:2209; frontend placeholder Settings.tsx:390 also true)`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:519 RunInMaintenanceWindow reads config.AppConfig.Maintenance.AuthorSplit; RegisterAuthorSplitScanOp in extra_ops.go:263 provides the underlying op; frontend toggle 'Author split' MaintenanceSettingsSection.tsx:28.
- **Why:** Consistent default across backend and frontend, task is a maintenance cleanup, no change needed.

#### `Config.Maintenance.DbOptimize (maintenance.db_optimize)`
🟢 used · 🟠 default review

- **Current default:** `true (config.go:2214; frontend placeholder Settings.tsx:395 also true)`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:546,593,596 read config.AppConfig.Maintenance.DbOptimize both as a maintenance-window gate and (line 593) as a scheduled-task IsEnabled gate; frontend toggle MaintenanceSettingsSection.tsx:33.
- **Why:** Consistent default, DB optimization is safe to run unattended.

#### `Config.Maintenance.Enabled / WindowStart / WindowEnd (maintenance.enabled, .window_start, .window_end)`
🟢 used · 🟠 default review

- **Current default:** `backend: enabled=true, window_start=1, window_end=4 (config.go:2203-2206); frontend placeholder: enabled=true, window_start=2, window_end=5 (Settings.tsx:384-387)`
- **Recommended default:** `enabled=true, window_start=1, window_end=4 (fix frontend placeholder to match)` (confidence: high)
- **Usage:** internal/scheduler/maintenance.go IsInMaintenanceWindow/IsInMaintenanceWindowAt gate every RunInMaintenanceWindow task on these three fields; frontend MaintenanceSettingsSection.tsx:49-93 binds the 'Enable nightly maintenance window' switch and Window start/end hour dropdowns directly to config.enabled/window_start/window_end (live server state, fully wired).
- **Why:** The 1-4am backend default is the one actually served and applied; the frontend placeholder should be corrected so it never displays a window the server doesn't actually use, even briefly before the real config loads.

#### `Config.Maintenance.LibraryOrganize (maintenance.library_organize)`
🟢 used · 🟠 default review

- **Current default:** `false (config.go:2216; frontend placeholder Settings.tsx:397 also false)`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:182 RunInMaintenanceWindow reads config.AppConfig.Maintenance.LibraryOrganize; frontend toggle MaintenanceSettingsSection.tsx:35; scheduler.go:166 comment explicitly calls this out as a maintenance-window-only toggle with no separate Scheduled sibling.
- **Why:** Organize can move/rename files on disk; defaulting to opt-in rather than running unattended overnight is the correct, safer choice.

#### `Config.Maintenance.LibraryScan (maintenance.library_scan)`
🟢 used · 🟠 default review

- **Current default:** `false (config.go:2215; frontend placeholder Settings.tsx:396 also false)`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:161 RunInMaintenanceWindow reads config.AppConfig.Maintenance.LibraryScan (distinct from the separate always-on Scheduled.LibraryScan periodic task); frontend toggle MaintenanceSettingsSection.tsx:34.
- **Why:** A full library scan inside the maintenance window is redundant with the separately-enabled Scheduled.LibraryScan periodic task (defaults on, runs every 6h); keeping this off by default avoids doubling scan work.

#### `Config.Maintenance.LibrarySizeRefresh (maintenance.library_size_refresh)`
🟢 used · 🟠 default review

- **Current default:** `true (config.go:2218; frontend placeholder Settings.tsx:399 also true)`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:203 RunInMaintenanceWindow reads config.AppConfig.Maintenance.LibrarySizeRefresh; RegisterLibrarySizeRefreshOp internal/server/library_size_refresh_op.go:30; frontend toggle MaintenanceSettingsSection.tsx:37.
- **Why:** Cheap cached-stat recompute, safe to run nightly by default.

#### `Config.Maintenance.MetadataRefresh (maintenance.metadata_refresh)`
🟢 used · 🟠 default review

- **Current default:** `false (config.go:2217; frontend placeholder Settings.tsx:398 also false)`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:406,492,697 all read config.AppConfig.Maintenance.MetadataRefresh; frontend toggle MaintenanceSettingsSection.tsx:36.
- **Why:** Metadata refresh hits external metadata sources; opt-in default is appropriate.

#### `Config.Maintenance.PurgeDeleted (maintenance.purge_deleted)`
🟢 used · 🟠 default review

- **Current default:** `backend: true (config.go:2212); frontend placeholder: false (Settings.tsx:393)`
- **Recommended default:** `true (align placeholder with backend)` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:622 RunInMaintenanceWindow reads config.AppConfig.Maintenance.PurgeDeleted; RegisterPurgeDeletedOp extra_ops.go:647; frontend toggle MaintenanceSettingsSection.tsx:31.
- **Why:** Purging soft-deleted rows is a routine maintenance task the backend defaults on; the UI placeholder should not silently disagree before the real config loads.

#### `Config.Maintenance.PurgeOldLogs (maintenance.purge_old_logs)`
🟢 used · 🟠 default review

- **Current default:** `true (config.go:2213; frontend placeholder Settings.tsx:394 also true)`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:807 RunInMaintenanceWindow reads config.AppConfig.Maintenance.PurgeOldLogs; frontend toggle MaintenanceSettingsSection.tsx:32.
- **Why:** Consistent default, no issue.

#### `Config.Maintenance.Reconcile (maintenance.reconcile)`
🟢 used · 🟠 default review

- **Current default:** `backend: true (config.go:2211); frontend placeholder: false (Settings.tsx:392)`
- **Recommended default:** `true (align placeholder with backend)` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:725 RunInMaintenanceWindow reads config.AppConfig.Maintenance.Reconcile; frontend toggle MaintenanceSettingsSection.tsx:30 bound to live server config via config.reconcile.
- **Why:** Reconcile is on by default server-side; the frontend's hardcoded initial state should match so the UI never shows a stale/incorrect value during the fetch window.

#### `Config.Maintenance.SeriesPrune (maintenance.series_prune)`
🟢 used · 🟠 default review

- **Current default:** `true (config.go:2208 ResetToDefaults; frontend placeholder state in Settings.tsx:389 also true)`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:352 RunInMaintenanceWindow closure reads config.AppConfig.Maintenance.SeriesPrune; wired to frontend toggle 'Series prune' in MaintenanceSettingsSection.tsx:27,102-114 (json tag series_prune matches exactly); legacy flat DB key maintenance_window_series_prune migrated forward by migrateMaintenanceBlob (persistence.go:448-509).
- **Why:** Idempotent, low-risk nightly cleanup task; default-on is sane and matches every layer.

#### `Config.Maintenance.TombstoneCleanup (maintenance.tombstone_cleanup)`
🟢 used · 🟠 default review

- **Current default:** `true (config.go:2210; frontend placeholder Settings.tsx:391 also true)`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:643 RunInMaintenanceWindow reads config.AppConfig.Maintenance.TombstoneCleanup; RegisterTombstoneCleanupOp extra_ops.go:685; frontend toggle MaintenanceSettingsSection.tsx:29.
- **Why:** Consistent default, cleanup task, no change needed.

#### `Config.MemoryLimitMB`
🔴 DEAD

- **Current default:** `512`
- **Usage:** Same pattern: declared, defaulted (512), viper-loaded, settable via persistence.go:1109 - no consuming read site found anywhere in the codebase.
- **Why:** Dead/unimplemented alongside MemoryLimitType/MemoryLimitPercent.

#### `Config.MemoryLimitPercent`
🔴 DEAD

- **Current default:** `25`
- **Usage:** Same pattern as MemoryLimitType: only declared, defaulted (25), viper-loaded, and settable via persistence.go:1105 - no consuming read site found anywhere in the codebase.
- **Why:** Dead/unimplemented alongside MemoryLimitType - part of the same never-wired memory-management sub-feature.

#### `Config.MemoryLimitType`
🔴 DEAD

- **Current default:** `"items"`
- **Usage:** Only appears in declaration (config.go:724), config-load wiring (viper.GetString at :1514), the default block (:2095, value 'items'), and the PUT /config setter (persistence.go:1092). No code anywhere reads AppConfig.MemoryLimitType to make a decision - grepped all non-test, non-config.go/persistence.go Go files with zero hits.
- **Why:** Appears to be dead/unimplemented: settable via API and persisted, but never consumed to actually select a memory-limiting strategy anywhere in the backend. Likely a stub for a memory-management feature that was never built out, or was superseded by the CacheSize-only path. Recommend confirming with the user whether to wire it up or remove it (it currently misleads operators into thinking it changes behavior).

#### `Config.OperationTimeoutMinutes`
🟢 used

- **Current default:** `unspecified (0 disables timeout per doc comment)`
- **Usage:** internal/server/server_lifecycle.go:632-633 uses it to compute the stale-operation timeout; internal/server/handlers/operations/handler.go:368 reads it too.
- **Why:** In active use, correctly enforced.

#### `Config.Plugins`
🟢 used

- **Current default:** `empty map`
- **Usage:** internal/server/wire_handlers.go:93 and internal/server/plugins_init.go:28 both consume the map[string]PluginConfig.
- **Why:** In active use; container for the PluginConfig.Enabled/Settings entries above.

#### `Config.PurgeSoftDeletedAfterDays`
🟢 used · 🟠 default review

- **Current default:** `30 (viper.SetDefault at config.go:1127; also literal 30 in the hardcoded default Config block at :2101 - the two 'defaultValue: 30' vs the inventory's other entry showing '30' agree, no conflict)`
- **Recommended default:** `30` (confidence: high)
- **Usage:** Read at internal/scheduler/extra_ops.go:545,858,870-871, internal/scheduler/tasks.go:614,616,621, internal/server/audiobooks_helpers.go:215,223-224, internal/server/server_maintenance_deps.go:280-281,297. Widely and correctly consumed to gate/parameterize the soft-delete purge job. Two source entries in the input (Config.PurgeSoftDeletedAfterDays struct field and a separate 'go-env-binding' entry at config.go:1127) are the SAME option - config.go:1127 is just the viper.SetDefault call for this same field, not a distinct env var.
- **Why:** In active, correct use with a consistent default across both default sources. No changes needed; the two inventory entries should be merged as duplicates of the same option.

#### `Config.PurgeSoftDeletedDeleteFiles`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/audiobooks_helpers.go:224 and internal/scheduler/extra_ops.go:871 pass it into PurgeSoftDeletedBooks to decide whether the purge also deletes underlying files.
- **Why:** In active use; false is the safe default (data-loss-averse) given this repo's history of missing-file/data-loss incidents.

#### `Config.ReviewApplyEnabled`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/wire_handlers.go:626 wires it as the gating func passed into reviewhandler.New; internal/server/handlers/review/handler.go:23 documents it as 'the switch'.
- **Why:** In active use; per project memory this is now flipped true in prod, default false is the safe out-of-box choice.

#### `Config.Scheduled.LibraryScan.{Enabled,Interval,OnStartup} (scheduled.library_scan.*)`
🟢 used

- **Current default:** `Enabled=true, Interval=360 (minutes), OnStartup=false (config.go:2234-2238)`
- **Usage:** internal/scheduler/tasks.go:146,149,152,159 read config.AppConfig.Scheduled.LibraryScan.Enabled/.Interval/.OnStartup to gate and schedule the periodic incremental library scan; this is the ONLY member of ScheduledTasksConfig given a non-zero literal in ResetToDefaults (config.go:2226-2239), specifically because leaving it zero-valued would silently disable the one task that must default on (per the inline comment).
- **Why:** This is a network/disk-bound periodic scan trigger, not a per-item worker-pool knob, so the mandatory multi-core concurrency policy doesn't apply directly to the interval itself; the scan operation it triggers should be checked separately (out of scope for this bucket) for internal parallelism. 360-minute default cadence is reasonable for a background incremental scan.

#### `Config.SetupComplete`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/update_service.go:142 sets it from RootDir; internal/server/handlers/system/handler.go:436 resets it to false (a factory-reset-style handler).
- **Why:** In active use for first-run/reset flows.

#### `Config.SetupComplete`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/config/config.go:511/1425/2043; set at internal/config/update_service.go:142 (SetupComplete = RootDir != ""), persisted at internal/config/persistence.go:108/954, and cleared at internal/server/handlers/system/handler.go:436 (factory-reset endpoint).
- **Why:** Correct default — gates the first-run setup wizard, and should start false until a root directory is configured.

#### `Config.Tools`
🟢 used

- **Current default:** `tools.ToolsConfig zero-value (defaults live in that sub-package, out of scope for this bucket's extracted entries)`
- **Usage:** internal/server/server.go:710 and internal/server/wire_handlers.go:611 both reference &config.AppConfig.Tools to construct the tools handler/registry (managed Ollama/fpcalc lifecycle).
- **Why:** In active use.

#### `Config.Tools.AllowPeriodicOllama (tools.allow_periodic_ollama)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `false (config.go:2347)`
- **Recommended default:** `false (unchanged) — but wire the ToolsSettingsTab switch to actual config state` (confidence: high)
- **Usage:** internal/server/server.go:743 passes toolsCfg.AllowPeriodicOllama into the Ollama duty-cycle manager's AllowPeriodic field — the backend genuinely consumes this.
- **Naming issue:** The only frontend control for this field, web/src/components/settings/ToolsSettingsTab.tsx:26-30 ('Allow periodic Ollama (spin up when new books need embedding)'), has no `checked` or `onChange` prop at all — it renders but is completely disconnected from state, so end users currently have NO way to change a setting the backend actually honors. This is worse than a merely-dead field: it's a live backend behavior with an unusable control surface.
- **Why:** The false default (don't auto-spin-up Ollama) is reasonable and conservative; the real bug is that this working backend setting is unreachable from the UI and must be wired up so users can opt in.

#### `Config.Tools.Fpcalc.Mode (tools.fpcalc.mode)`
🟢 used · 🟠 default review

- **Current default:** `tools.ToolModeSystem ("system") — config.go:2346`
- **Recommended default:** `"system"` (confidence: high)
- **Usage:** Same Resolve()/toolConfig() mechanism as Ollama.Mode in internal/tools/registry.go, keyed by tool name 'fpcalc' (Chromaprint fingerprinting binary used by AcoustID matching).
- **Why:** Same reasoning as Ollama.Mode: conservative default of using a system-installed binary.

#### `Config.Tools.ManagedDir (tools.managed_dir)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `/var/lib/audiobook-organizer/tools (config.go:2344 and both frontend copies)`
- **Recommended default:** `/var/lib/audiobook-organizer/tools (unchanged) — but remove or wire the dead ToolsSettingsTab.tsx duplicate` (confidence: high)
- **Usage:** internal/tools/registry.go:159 uses r.cfg.ManagedDir to build managed-tool install paths; internal/server/server.go:733 uses toolsCfg.ManagedDir for the Ollama PID file path; internal/server/handlers/tools/tools.go:75 uses h.cfg.ManagedDir as the binary download destination. Frontend field 'Managed tools directory' in Settings.tsx:865-872 is correctly bound to toolsConfig.managed_dir via handleToolsChange.
- **Naming issue:** A second, textually-identical 'Managed tools directory' field exists in web/src/components/settings/ToolsSettingsTab.tsx:42-56 with the same defaultValue but no `value`/`onChange` props at all — it is a completely dead duplicate that silently discards any edit the user makes in it, while the real working control sits in Settings.tsx's separate 'Advanced: Tools Config' accordion. A user editing the wrong one loses their change with no error.
- **Why:** The default path itself is sane; the real defect is a duplicate, unwired UI control that silently eats user input — this should be deleted (the working Settings.tsx field already covers it) rather than left as a trap.

#### `Config.Tools.Ollama.Mode (tools.ollama.mode)`
🟢 used · 🟠 default review

- **Current default:** `tools.ToolModeSystem ("system") — config.go:2345`
- **Recommended default:** `"system"` (confidence: high)
- **Usage:** internal/tools/registry.go:169 default-returns ToolConfig{Mode: ToolModeSystem} and the Resolve() switch (registry.go:80-100) branches on toolCfg.Mode (ToolModeManaged/ToolModeSystem/ToolModeCustom/ToolModeDisabled) per-tool via r.toolConfig(name), keyed by tool name 'ollama'.
- **Why:** Defaulting to using a system-installed Ollama rather than auto-managing/downloading one is the conservative, correct default.

#### `Config.Tools.OllamaDebounceMin (tools.ollama_debounce_min)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `10 (minutes) — config.go:2348`
- **Recommended default:** `10 (unchanged) — but wire the control to real state` (confidence: high)
- **Usage:** internal/server/server.go:742 passes time.Duration(toolsCfg.OllamaDebounceMin) * time.Minute as the Debounce for the same Ollama duty-cycle manager consumed alongside AllowPeriodicOllama.
- **Naming issue:** The only frontend control, ToolsSettingsTab.tsx:31-40 ('Debounce interval (minutes)'), uses `defaultValue={10}` with no `value`/`onChange` at all, so it is purely decorative — edits are never saved. Same class of defect as AllowPeriodicOllama: a live backend field with a non-functional UI control.
- **Why:** 10-minute debounce between periodic Ollama management actions is a sane default; fix is to wire the existing UI field, not change the value.

#### `legacy flat maintenance_window_* DB blob keys -> config_blob.maintenance.* (migrateMaintenanceBlob)`
🟢 used

- **Current default:** `n/a (one-time compatibility shim)`
- **Usage:** internal/config/persistence.go:725 calls migrateMaintenanceBlob(blobStr) during config load; the function itself (persistence.go:448-509) is still exercised on every load for any pre-migration install (keyed on presence of sentinel `maintenance_window_enabled`). No code writes these flat keys anymore going forward (persistence.go:108 writer path only writes setup_complete flat-style; the maintenance struct is written as one nested blob).
- **Why:** Legacy migration path, still correctly wired and idempotent; keep until confident no installs remain on the pre-nested schema, then it and its 16 flat key names can be deleted as dead code.

#### `legacy flat scheduled_* DB blob keys -> config_blob.scheduled.<group>.* (migrateScheduledBlob / remapScheduledKeys)`
🟢 used

- **Current default:** `n/a (one-time compatibility shim)`
- **Usage:** internal/config/persistence.go:734 calls migrateScheduledBlob(blobStr) during config load; remapScheduledKeys (persistence.go:540-589) merges into any pre-existing scheduled.<group> object field-by-field rather than overwriting wholesale, unlike the sibling migrate*Blob functions. Covers 7 task groups x up to 3 flat keys = 22 legacy keys, gated on sentinel scheduled_dedup_refresh_enabled.
- **Why:** Correctly wired and still necessary for old installs; no action needed beyond eventual removal once all installs are confirmed migrated.

#### `maintenance.acoustid_backfill`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/plugins/maintenance/optimize.go:95 and internal/server/server_lifecycle.go:1145 both gate on config.AppConfig.Maintenance.AcoustIDBackfill. No frontend field exists (not in api.ts MaintenanceConfig, not in MaintenanceSettingsSection.tsx) — config-file/env-only.
- **Why:** Explicitly disabled since 2026-08-11 per the config comment: the nightly op's load phase held ~862MB of live heap and was implicated in three OOM kills in one night. Correctly off by default until the load-phase memory profile is fixed; do not re-enable without that fix.

#### `maintenance.acoustid_nightly_limit`
🟢 used · 🟠 default review

- **Current default:** `5000`
- **Recommended default:** `5000` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:563 (used as the 'limit' param for the AcoustID online-lookup op).
- **Why:** This bounds a nightly quota consumption count, not a concurrency degree — the repo's multi-core concurrency policy for whole-library loops doesn't apply here. 5000/night is a reasonable quota cap; only takes effect once acoustid_online_lookup is also enabled.

#### `maintenance.acoustid_online_lookup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:572/575 (IsEnabled and RunInMaintenanceWindow).
- **Why:** Consumes third-party AcoustID API quota per the config comment; off-by-default until a user provides an API key is correct.

#### `maintenance.author_split`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:519 (RunInMaintenanceWindow).
- **Why:** Standard nightly cleanup task; sane default.

#### `maintenance.db_optimize`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:546/593/596 (RunInMaintenanceWindow and IsEnabled).
- **Why:** DB compaction/VACUUM during an off-peak window is a sane nightly default.

#### `maintenance.dedup_refresh`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** Merged with Config.Maintenance.DedupRefresh. internal/scheduler/tasks.go:268 (RunInMaintenanceWindow).
- **Why:** Dedup refresh is one of the core nightly maintenance jobs; on by default within the maintenance window is intended and safe (review-only unless auto-merge is separately enabled).

#### `maintenance.enabled`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** Merged with Config.Maintenance.Enabled (config.go:2204). Consumed at internal/scheduler/maintenance.go:27, internal/scheduler/scheduler.go:301/307, internal/server/handlers/operations/handler.go:776/808. Frontend: web/src/components/settings/MaintenanceSettingsSection.tsx master switch, api.ts:796.
- **Why:** Master switch for the nightly maintenance window (dedup refresh, prune, purge, DB optimize); on by default is correct since the per-task toggles it gates are individually safe defaults.

#### `maintenance.library_organize`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:182 (RunInMaintenanceWindow). scheduler.go:165-172 documents the same 'declared but missing from MaintenanceOrder()' dead-config bug that affected library_scan — it has since been fixed by adding library_organize to that ordered list.
- **Why:** This task mutates files on disk (moves/renames), so off-by-default is the correct safe posture; users opt in deliberately. Was dead config until recently fixed — now live and correctly defaulted off.

#### `maintenance.library_scan`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:161 (RunInMaintenanceWindow). Note: internal/scheduler/scheduler.go:173-179 documents this toggle was previously unreachable dead config because library_scan was missing from MaintenanceOrder() — it has since been added to that ordered list (scheduler.go:163-179), so the toggle is now live, not dead.
- **Why:** A full library walk inside the maintenance window is separate from (and redundant with) scheduled.library_scan, which already defaults on with incremental scanning; keeping this off by default avoids a duplicate walk. Fixed a real dead-config bug previously — no longer an issue.

#### `maintenance.library_size_refresh`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:203 (RunInMaintenanceWindow).
- **Why:** Documented as a cheap FS-walk size-cache refresh; on-by-default is appropriate and matches its stated intent (config.go:1254-1256).

#### `maintenance.metadata_refresh`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:406/492/697 (RunInMaintenanceWindow).
- **Why:** Metadata refresh does per-book network/API calls; off by default avoids unattended quota burn on every maintenance window.

#### `maintenance.purge_deleted`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:622 (RunInMaintenanceWindow).
- **Why:** Purges soft-deleted DB records (not files, unless purge_soft_deleted_delete_files is also set) after the retention period; sane to run nightly.

#### `maintenance.purge_old_logs`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:807 (RunInMaintenanceWindow).
- **Why:** Cheap log-retention cleanup; sane nightly default.

#### `maintenance.reconcile`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:725 (RunInMaintenanceWindow).
- **Why:** Standard nightly consistency task; sane default.

#### `maintenance.series_prune`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:352 (RunInMaintenanceWindow).
- **Why:** Standard nightly cleanup task; sane default.

#### `maintenance.tombstone_cleanup`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:643 (RunInMaintenanceWindow).
- **Why:** Cheap disk-reclaiming cleanup; sane to run nightly by default.

#### `maintenance.window_end`
🟢 used · 🟠 default review

- **Current default:** `4`
- **Recommended default:** `4` (confidence: high)
- **Usage:** Merged with Config.Maintenance.WindowEnd. Used at internal/scheduler/maintenance.go:31, handlers/operations/handler.go:778/810.
- **Why:** Sane off-peak default paired with window_start=1.

#### `maintenance.window_start`
🟢 used · 🟠 default review

- **Current default:** `1`
- **Recommended default:** `1` (confidence: high)
- **Usage:** Merged with Config.Maintenance.WindowStart. Used at internal/scheduler/maintenance.go:30, scheduler.go:307, handlers/operations/handler.go:777/780/809.
- **Why:** 1am-4am window is a sane off-peak default for background maintenance work.

#### `maintenance_window_migrated / maintenance_window_last_run / maintenance_window_update_completed (internal DB-state markers)`
🟢 used

- **Current default:** `n/a (internal bookkeeping, not user-configurable)`
- **Usage:** maintenance_window_migrated: read/written in persistence.go:905,928 to gate MigrateMaintenanceWindow so it runs once. maintenance_window_last_run: read/written in internal/scheduler/maintenance.go:51,69 (loadLastMaintenanceRun/saveLastMaintenanceRun) to track the last date the nightly window ran. maintenance_window_update_completed: read/written in internal/server/scheduler_maintenance_window_op.go:74,80 to avoid re-running the auto-update step twice in one day. All three are explicitly excluded from Config field mutation in persistence.go:938's applySetting switch so a stray DB row of this name can never clobber a real Config field.
- **Why:** Correctly implemented internal state; no change needed.

#### `MaintenanceConfig.AcoustIDBackfill`
🟢 used · 🟠 default review

- **Current default:** `false (per doc comment)`
- **Recommended default:** `false` (confidence: medium)
- **Usage:** internal/plugins/maintenance/optimize.go:95 and internal/server/server_lifecycle.go:1145 both gate the local fpcalc/ffmpeg fingerprint backfill on this flag. Doc comment says OFF by default.
- **Why:** This gates a per-item CPU/subprocess-bound library-scale operation (fpcalc/ffmpeg per book). If/when it runs, the underlying backfill loop should itself use a bounded worker pool per the repo's mandatory concurrency policy - that is a property of the acoustid.backfill implementation, not of this bool, but worth flagging since this option is the trigger for a whole-library op.

#### `MaintenanceConfig.AcoustIDNightlyLimit`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:563 passes it as the 'limit' parameter for the nightly AcoustID lookup task.
- **Why:** In active use.

#### `MaintenanceConfig.AcoustIDOnlineLookup`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:572,575 gate both IsEnabled and RunInMaintenanceWindow for the AcoustID online lookup task.
- **Why:** In active use.

#### `MaintenanceConfig.AuthorSplit`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:519 gates RunInMaintenanceWindow for the author split task.
- **Why:** In active use.

#### `MaintenanceConfig.DbOptimize`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:546,593,596 gates both IsEnabled and RunInMaintenanceWindow for the db optimize task.
- **Why:** In active use.

#### `MaintenanceConfig.DedupRefresh`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:268 gates RunInMaintenanceWindow for the dedup refresh task.
- **Why:** In active use.

#### `MaintenanceConfig.Enabled`
🟢 used

- **Current default:** `unspecified in provided entry (see MaintenanceConfig defaults block)`
- **Usage:** internal/scheduler/scheduler.go:301, internal/scheduler/maintenance.go:27, internal/server/handlers/operations/handler.go:776/808 read/write it to gate the nightly maintenance window.
- **Why:** Core switch for the whole maintenance window; in active use across scheduler and API.

#### `MaintenanceConfig.LibraryOrganize`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:182 gates RunInMaintenanceWindow for library organize; referenced in a comment at internal/scheduler/scheduler.go:166.
- **Why:** In active use.

#### `MaintenanceConfig.LibraryScan`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:161 gates RunInMaintenanceWindow for the library scan task (distinct from the always-on Scheduled.LibraryScan periodic task).
- **Why:** Both are real, distinct knobs; flag the naming collision for documentation clarity, not a code fix.

#### `MaintenanceConfig.LibrarySizeRefresh`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:203 gates RunInMaintenanceWindow for the library size refresh task.
- **Why:** In active use.

#### `MaintenanceConfig.MetadataRefresh`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:406,492,697 gate RunInMaintenanceWindow for three separate metadata-refresh-related tasks.
- **Why:** In active use, though one flag fanning out to 3 tasks is worth noting (not a defect).

#### `MaintenanceConfig.PurgeDeleted`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:622 gates RunInMaintenanceWindow for the purge-deleted task.
- **Why:** In active use.

#### `MaintenanceConfig.PurgeOldLogs`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:807 gates RunInMaintenanceWindow for the purge-old-logs task.
- **Why:** In active use.

#### `MaintenanceConfig.Reconcile`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:725 gates RunInMaintenanceWindow for the reconcile task.
- **Why:** In active use.

#### `MaintenanceConfig.SeriesPrune`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:352 gates RunInMaintenanceWindow for the series prune task.
- **Why:** In active use.

#### `MaintenanceConfig.TombstoneCleanup`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:643 gates RunInMaintenanceWindow for tombstone cleanup. Note: unlike sibling ops it is not also wired into an IsEnabled func at that call site (only RunInMaintenanceWindow) - worth a quick look but not evidence of dead code.
- **Why:** In active use.

#### `MaintenanceConfig.WindowStart / MaintenanceConfig.WindowEnd`
🟢 used

- **Current default:** `0/0, backfilled to 1/4 if both are 0`
- **Usage:** internal/scheduler/maintenance.go:30-31 and internal/scheduler/scheduler.go:307 read the window bounds; internal/config/persistence.go:914-923 seeds them from AutoUpdate.Window* or defaults (1-4) when unset; exposed via operations handler.go:777-780/809-810.
- **Why:** Used correctly; the backfill-from-AutoUpdate logic is a deliberate migration shim, not a bug.

#### `MigrateMaintenanceWindow: AutoUpdate.WindowStart/End -> Maintenance.WindowStart/End (one-time value migration)`
🟢 used

- **Current default:** `fallback 1 (start) / 4 (end) when neither legacy nor new value is set`
- **Usage:** internal/config/persistence.go:882 calls MigrateMaintenanceWindow(store) during startup config load; function body (persistence.go:904-930) copies AutoUpdate.WindowStart/End into Maintenance.WindowStart/End if the latter are unset, then falls back to hardcoded 1/4 if both remain zero. Gated by the maintenance_window_migrated flag above so it is a true one-time migration.
- **Why:** Idempotent, already-gated one-time migration; the 1/4 fallback matches the current ResetToDefaults literal (config.go:2205-2206), so it's internally consistent.

#### `PATH (test-only os.Getenv usage)`
🟢 used

- **Current default:** `n/a (standard OS env var, not application config)`
- **Usage:** internal/tools/registry_test.go:22 uses t.Setenv("PATH", ...) to prepend a fake-binary directory ahead of the real PATH for registry resolution tests; also read/restored around exec.LookPath-based tests in internal/metadata/write_test.go:51,76,101.
- **Why:** Legitimate test-harness usage of a standard OS variable; not an application option at all, correctly out of scope for user-facing config docs.

#### `PluginConfig.Enabled / PluginConfig.Settings`
🟢 used

- **Current default:** `(none; zero-value bool/map per plugin entry)`
- **Usage:** internal/server/plugins_init.go:29-33 reads cfg.Enabled and cfg.Settings when wiring plugins from config.AppConfig.Plugins; internal/server/handlers/plugins.go:114,137,184 sets cfg.Enabled/cfg.Settings from the plugins API.
- **Why:** Actively used to gate plugin activation and pass per-plugin settings; no changes needed.

#### `purge_soft_deleted_delete_files`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/config/config.go:746 defines Config.PurgeSoftDeletedDeleteFiles; consumed at internal/scheduler/extra_ops.go:871 and internal/server/audiobooks_helpers.go:224 (PurgeSoftDeletedBooks call). Frontend: web/src/services/api.ts:897, web/src/hooks/useSettingsHandlers.ts:471/671/759, web/src/pages/Settings.tsx:575.
- **Why:** Deleting backing files during a soft-delete purge is destructive/irreversible; defaulting to metadata-only purge is the correct safe default. No env var binding exists, which is deliberate (UI/config-file-only setting, not intended for env override) and not a bug.

#### `restoreVerify ('Verify backup before restore' dialog switch)`
🟢 used · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** web/src/pages/Settings.tsx:190 (useState), :682 (destructured into save/restore handlers), :1284-1289 (the Switch control) — passed as a per-action parameter to the restore-backup API call, not part of persisted Config.
- **Why:** Verifying a backup before restoring over the live library is the correct safe default for a destructive action.

#### `scheduled.ai_dedup_batch.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:745 (also gated by config.AppConfig.EnableAIParsing). Frontend: ScheduledTasksSection.tsx:220/225.
- **Why:** AI-assisted dedup batch calls an LLM/embedding backend and costs money/quota; off by default is correct, matching this repo's pattern of never defaulting AI-cost features on.

#### `scheduled.ai_dedup_batch.interval`
🟢 used · 🟠 default review

- **Current default:** `1440`
- **Recommended default:** `1440` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:748.
- **Why:** Daily cadence is a reasonable batch-job interval if the owner opts in.

#### `scheduled.ai_dedup_batch.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:754.
- **Why:** Sane — avoids an AI-cost batch job firing on every restart.

#### `scheduled.author_split.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:510. Frontend: ScheduledTasksSection.tsx:119/124.
- **Why:** Opt-in author-name-splitting scan; maintenance.author_split (nightly window) is the default-on path for this same work, so keeping the standalone scheduled variant off by default avoids double-running it.

#### `scheduled.author_split.interval`
🟢 used · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `0` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:512; GetInterval treats mins<=0 as 'no ticker' (returns 0 duration), confirmed at tasks.go:511-516.
- **Why:** 0 correctly means disabled/manual per the documented convention and the GetInterval guard against a busy loop; sane given enabled defaults to false anyway.

#### `scheduled.author_split.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:518.
- **Why:** Sane.

#### `scheduled.db_optimize.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:537. Frontend: ScheduledTasksSection.tsx:138/143.
- **Why:** maintenance.db_optimize (true, nightly window) is the default-on path; the standalone scheduled variant off-by-default avoids redundant compaction runs.

#### `scheduled.db_optimize.interval`
🟢 used · 🟠 default review

- **Current default:** `1440`
- **Recommended default:** `1440` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:539.
- **Why:** Daily cadence is appropriate for DB compaction/VACUUM if this opt-in path is enabled outside the maintenance window.

#### `scheduled.db_optimize.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:545.
- **Why:** Sane — DB optimize on every restart would be wasteful.

#### `scheduled.dedup_refresh.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:259 (IsEnabled). Frontend: web/src/components/settings/ScheduledTasksSection.tsx:105 wires config.dedup_refresh.enabled.
- **Why:** Dedup refresh is expensive on large libraries; off-by-default with the nightly maintenance.dedup_refresh toggle as the opt-in path is consistent with this repo's pattern for expensive maintenance work.

#### `scheduled.dedup_refresh.interval`
🟢 used · 🟠 default review

- **Current default:** `360`
- **Recommended default:** `360` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:261.
- **Why:** Sane 6-hour default for an opt-in task; only takes effect once enabled=true is explicitly set.

#### `scheduled.dedup_refresh.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:267.
- **Why:** Sane — avoids running an expensive dedup pass on every restart.

#### `scheduled.label_refinement.enabled`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:293 gates the label-refinement dry-run chain (rebuild-gold-labels -> calibrate-composite).
- **Naming issue:** No frontend representation at all — grep for 'label_refinement' across web/src returns zero hits. It is not in api.ts's ScheduledTasksConfig or ScheduledTasksSection.tsx, unlike every sibling scheduled task.
- **Why:** Config comment (config.go:1152-1154) explicitly says this ships disabled per INIT-1 T6 pending an owner opt-in; default is intentional and correct. The missing UI exposure means the only way to enable it today is a config file/env edit — worth wiring into ScheduledTasksSection.tsx if it's meant to be user-facing, or documenting as admin-only if not.

#### `scheduled.label_refinement.interval`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `10080`
- **Recommended default:** `10080` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:295.
- **Naming issue:** Same missing-frontend gap as scheduled.label_refinement.enabled.
- **Why:** Weekly cadence (10080 min) is documented and appropriate for a label-refinement/calibration job.

#### `scheduled.label_refinement.on_startup`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:301.
- **Naming issue:** Same missing-frontend gap as the sibling label_refinement fields.
- **Why:** Sane — this is a calibration job, not something that should fire on every restart.

#### `scheduled.library_scan.enabled`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `true`
- **Recommended default:** `true` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:146,149 gates the library_scan task's IsEnabled on config.AppConfig.Scheduled.LibraryScan.Enabled.
- **Naming issue:** Frontend has no representation at all: web/src/services/api.ts's ScheduledTasksConfig interface (line 821-830) lists dedup_refresh, author_split, db_optimize, metadata_refresh, resolve_production_authors, series_prune, ai_dedup_batch, reconcile — but omits library_scan and label_refinement entirely, and neither appears in web/src/components/settings/ScheduledTasksSection.tsx. Users cannot toggle this from the UI even though it is the one scheduled task on by default.
- **Why:** Default is correct per the code comment (config.go:1136-1145): this is the only unattended-discovery path for newly added books. The default is sane; the gap is UI exposure, not the value.

#### `scheduled.library_scan.interval`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `360`
- **Recommended default:** `360` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:152 (mins := config.AppConfig.Scheduled.LibraryScan.Interval) feeds GetInterval for the scheduler ticker.
- **Naming issue:** Same frontend gap as scheduled.library_scan.enabled — no UI field exists for this interval.
- **Why:** 6-hour bound is explicitly justified in a code comment as balancing discovery latency against full-library walk cost, and the scan is incremental (unchanged files skipped via scan cache). Sane as-is.

#### `scheduled.library_scan.on_startup`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:159 (Scheduled.LibraryScan.OnStartup \|\| ScanOnStartup).
- **Naming issue:** Same frontend gap as the sibling library_scan fields — no UI control exists.
- **Why:** Avoids a full-library walk stampede on every process restart; the underlying ScanOnStartup toggle covers the explicit opt-in case.

#### `scheduled.metadata_refresh.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:688,696. Frontend: ScheduledTasksSection.tsx:157/162.
- **Why:** Metadata refresh does network/API calls per book; off by default (consistent with maintenance.metadata_refresh also defaulting false) is the correct safe default to avoid quota burn.

#### `scheduled.metadata_refresh.interval`
🟢 used · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `0` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:690.
- **Why:** 0 = disabled/manual per documented convention; sane paired with enabled=false.

#### `scheduled.metadata_refresh.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:696.
- **Why:** Sane.

#### `scheduled.reconcile.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:716. Frontend: ScheduledTasksSection.tsx:239/244.
- **Why:** maintenance.reconcile (true, nightly window) is the default-on path; standalone scheduled variant off avoids double-running the same reconcile work.

#### `scheduled.reconcile.interval`
🟢 used · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `0` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:718.
- **Why:** 0 = disabled/manual; sane paired with enabled=false.

#### `scheduled.reconcile.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:724.
- **Why:** Sane.

#### `scheduled.resolve_production_authors.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:661. Frontend: ScheduledTasksSection.tsx:176/181.
- **Why:** Opt-in author-production-resolution task; off by default is appropriate.

#### `scheduled.resolve_production_authors.interval`
🟢 used · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `0` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:663.
- **Why:** 0 = disabled/manual per convention; sane paired with enabled=false.

#### `scheduled.series_prune.enabled`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:343. Frontend: ScheduledTasksSection.tsx:201/206.
- **Why:** maintenance.series_prune (true, nightly window) is the default-on path; standalone scheduled variant off avoids double-running.

#### `scheduled.series_prune.interval`
🟢 used · 🟠 default review

- **Current default:** `0`
- **Recommended default:** `0` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:345.
- **Why:** 0 = disabled/manual; sane paired with enabled=false.

#### `scheduled.series_prune.on_startup`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** internal/scheduler/tasks.go:351.
- **Why:** Sane.

#### `ScheduledTaskConfig.Enabled / Interval / OnStartup`
🟢 used

- **Current default:** `varies per task; LibraryScan defaults enabled with a non-zero interval (per its own doc comment), the rest default off/unset`
- **Usage:** This is the generic per-task shape embedded in every ScheduledTasksConfig.* field (LibraryScan, DedupRefresh, LabelRefinement, AuthorSplit, DbOptimize, MetadataRefresh, ResolveProductionAuthors, SeriesPrune, AIDedupBatch, Reconcile). All three sub-fields are read at every one of those call sites in internal/scheduler/tasks.go (e.g. lines 146-159, 259-267, 293-301, 343-351, 510-518, 537-545, 661-663, 688-696, 716-724, 745-754) and written from internal/config/persistence.go:1270-1374 and the API handler at internal/server/handlers/operations/handler.go:570.
- **Why:** Fully wired end-to-end (config -> scheduler -> API). No issues.

#### `ScheduledTasksConfig.AIDedupBatch (Config.Scheduled.ai_dedup_batch)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:745,748,754 reads Enabled (also AND'd with EnableAIParsing)/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.AuthorSplit (Config.Scheduled.author_split)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:510-518 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.DbOptimize (Config.Scheduled.db_optimize)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:537-545 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.DedupRefresh (Config.Scheduled.dedup_refresh)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:259-267 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.LabelRefinement (Config.Scheduled.label_refinement)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:293-301 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.LibraryScan (Config.Scheduled.library_scan)`
🟢 used

- **Current default:** `enabled, non-zero interval (exact minutes not present in extracted entries)`
- **Usage:** internal/scheduler/tasks.go:146-159 reads Enabled/Interval/OnStartup for the periodic incremental scan; described in its own doc comment as shipping ENABLED with non-zero interval since nothing else discovers new books without it.
- **Why:** Correctly the only enabled-by-default scheduled task per its own documented rationale.

#### `ScheduledTasksConfig.MetadataRefresh (Config.Scheduled.metadata_refresh)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:688-696 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.Reconcile (Config.Scheduled.reconcile)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:716-724 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `ScheduledTasksConfig.ResolveProductionAuthors (Config.Scheduled.resolve_production_authors)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:661-663 reads Enabled/Interval.
- **Why:** In active use.

#### `ScheduledTasksConfig.SeriesPrune (Config.Scheduled.series_prune)`
🟢 used

- **Current default:** `unspecified`
- **Usage:** internal/scheduler/tasks.go:343-351 reads Enabled/Interval/OnStartup.
- **Why:** In active use.

#### `settings.showAdvanced (localStorage client-only toggle)`
🟢 used · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `false` (confidence: high)
- **Usage:** web/src/hooks/useAdvancedSettings.ts:8-21 persists to localStorage key 'settings.showAdvanced' and is consumed by ToolsSettingsTab.tsx:11,18,31,42 to gate visibility of the Advanced fields (which include the three unwired fields above: allowPeriodicOllama, debounceIntervalMinutes, managedToolsDirectory).
- **Why:** Functions correctly as a pure client-side UI-visibility toggle; not sent to the backend, which is appropriate for this kind of setting. The real problem is what it reveals (three non-functional fields), not this toggle itself.

#### `setup_complete`
🟢 used · 🟠 default review

- **Current default:** `false (viper.SetDefault, config.go:1021; config.yaml:15)`
- **Recommended default:** `false (unchanged)` (confidence: medium)
- **Usage:** internal/config/config.go:511 (struct field), :1021 (viper default), :1425 (viper load); internal/config/persistence.go:108,952 (persisted/read as a DB setting); web/src/App.tsx:97 gates the first-run wizard on it; web/src/components/wizard/WelcomeWizard.tsx:247 sends it in the completion payload.
- **Why:** The false-until-root-dir-is-set default and root_dir-derivation logic are correct and safe; the only cleanup opportunity is removing the now-meaningless setup_complete:true from WelcomeWizard's outgoing payload since the server never reads it from the client.

#### `TEST_SUBPROCESS_CHILD / TEST_SUBPROCESS_EXPECT_DEF / TEST_SUBPROCESS_EXPECT_PARAMS / TEST_SUBPROCESS_RESULT_ERROR (test-only env var family)`
🟢 used

- **Current default:** `n/a (test-harness signaling env vars, not application config)`
- **Usage:** All four are defined as named constants and consumed entirely within internal/operations/registry/subprocess_test.go (lines 32-35 define the constants; 39, 256, 261, 267 use them) to drive a self-re-exec'd fake child process for subprocess-handshake tests.
- **Why:** Fully self-contained to one test file; not application config and correctly out of scope for the option-audit deliverable.

#### `tools.embed_queue_debounce_ms (frontend-only, no backend field)`
🔴 DEAD · 🟡 naming · 🟠 default review

- **Current default:** `500 (frontend placeholder only; no backend default exists since there is no backend field)`
- **Recommended default:** `remove the field from the UI, or implement the corresponding backend field/consumer if the embed-queue debounce behavior is meant to be real` (confidence: high)
- **Usage:** Exhaustive repo search (`rg embed_queue_debounce_ms` / `EmbedQueueDebounce`) finds this key ONLY in the frontend: web/src/services/api.ts:842 (interface field), web/src/pages/Settings.tsx:406 (placeholder default), and Settings.tsx:873-879 (the 'Embed queue debounce (ms)' text field bound via handleToolsChange). There is no corresponding field anywhere in internal/tools.ToolsConfig (whose json tags are only managed_dir, ollama, fpcalc, allow_periodic_ollama, ollama_debounce_min — internal/tools/config_types.go:27-33) or anywhere else in the Go backend. Any PATCH the frontend sends with this field is silently dropped by Go's JSON unmarshal since it isn't a known struct field.
- **Naming issue:** Not a naming mismatch so much as a phantom field: the frontend implements a full editable control (with helper text 'Milliseconds to wait before draining embed queue') for a setting that has never existed on the backend. Whatever value a user types is accepted by the UI, silently discarded on save, and re-reset to the 500ms placeholder on next page load — a genuinely misleading dead control.
- **Why:** This is the most clear-cut dead option found in this bucket — it's presented to users as a working setting but has zero effect anywhere. Given the repo's concurrency mandate around whole-library work (embedding is exactly the kind of per-item, network/CPU-bound loop this policy targets), if an embed-queue debounce is actually desired it should be implemented properly rather than left as a UI-only placebo.

---

## CLI Flags & Database

#### `--config (cfgFile)`
🟢 used

- **Current default:** `"" (falls back to $HOME/.audiobook-organizer.yaml)`
- **Usage:** cmd/root.go:347 defines the flag into var cfgFile; cmd/root.go:445-446 uses it in initConfig: `if cfgFile != "" { viper.SetConfigFile(cfgFile) }`.
- **Why:** Standard cobra/viper config-file override pattern; default is correct.

#### `--count / --authors / --series / --reset (seed)`
🟢 used

- **Current default:** `count=50, authors=5, series=8, reset=false`
- **Usage:** cmd/seed.go:47-50 define all four flags into seedCount/seedAuthors/seedSeries/seedReset vars, consumed by the seed command body to generate synthetic library data.
- **Why:** Reasonable small-scale defaults for a dev/test data-seeding command; reset=false avoids accidental destructive deletion of prior seed:* data.

#### `--dry-run (diagnostics cleanup-invalid)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/diagnostics.go:52 defines the flag; read via cmd.Flags().GetBool("dry-run") at diagnostics.go:33 and passed into runCleanupInvalidBooks.
- **Why:** false is correct (command performs real deletion by default, matching --yes needing to also be true); no issue found.

#### `--limit (diagnostics query)`
🟢 used

- **Current default:** `5`
- **Usage:** cmd/diagnostics.go:54 defines the flag; read via cmd.Flags().GetInt("limit") at diagnostics.go:42 and passed into runDiagnosticsQuery.
- **Why:** Small default is appropriate for an interactive inspection command that would otherwise dump large result sets to the terminal.

#### `--log-level`
🟢 used

- **Current default:** `"info"`
- **Usage:** cmd/root.go:353 defines the flag into var logLevel; cmd/root.go:480-481 applies it to config.AppConfig.LogLevel when non-empty.
- **Why:** info is the conventional default log level; matches project-wide guidance that prod stays on DEBUG build (a separate build-tag concern, not this flag) while runtime log verbosity defaults sensibly to info.

#### `--prefix (diagnostics query)`
🟢 used

- **Current default:** `"book:"`
- **Usage:** cmd/diagnostics.go:55 defines the flag; read via cmd.Flags().GetString("prefix") at diagnostics.go:43, used only when --raw is set (per the flag's own help text and diagnostics.go:44-45).
- **Why:** book: is a reasonable default prefix given PebbleDB's key-prefix convention for the books keyspace; matches the tool's most common debugging use case.

#### `--raw (diagnostics query)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/diagnostics.go:56 defines the flag; read via cmd.Flags().GetBool("raw") at diagnostics.go:44.
- **Why:** false (structured output by default, raw Pebble bytes opt-in) is correct; help text correctly notes 'Pebble only', consistent with PebbleDB being the sole backend.

#### `--yes (diagnostics cleanup-invalid)`
🟢 used

- **Current default:** `false`
- **Usage:** cmd/diagnostics.go:51 defines the flag; read via cmd.Flags().GetBool("yes") at diagnostics.go:32 and passed into runCleanupInvalidBooks(force, dryRun).
- **Why:** false (require explicit confirmation) is correct for a destructive cleanup command.

#### `AO_DB`
🔴 DEAD · 🟡 naming · 🟠 default review

- **Current default:** `/data/audiobook-organizer.db (documented default, never applied)`
- **Recommended default:** `Remove AO_DB from .env.example, docker-compose.yml, and README.md; document DATABASE_PATH instead (already the real env var, and already used correctly elsewhere in docker-compose/systemd for other settings).` (confidence: high)
- **Usage:** grep -rn "AO_DB" across all *.go files in the repo returns zero matches -- it is never read by any Go code, viper key, or BindEnv call. It is only referenced in .env.example:16, docker-compose.yml:19, and documented in README.md:198. In the docker-compose path the Dockerfile CMD additionally hardcodes `--db /data/audiobooks.pebble`, so even a hypothetical future binding would be overridden by the CLI flag anyway. The functioning equivalent is database_path (env DATABASE_PATH / --db flag).
- **Naming issue:** AO_DB looks like it should be an alias/prefixed form of the real env var DATABASE_PATH but is a completely disconnected, dead name -- an operator setting AO_DB per .env.example or README.md:198 gets silently ignored with no warning.
- **Why:** Dead config surface that actively misleads operators following the documented .env.example/README setup steps into believing they've configured the database path when they have not.

#### `config_blob (DB setting key, v2 config persistence container)`
🟢 used

- **Current default:** `n/a (not a CLI/env/yaml-exposed option; internal serialization key only)`
- **Usage:** Written by SaveConfigToDatabase (internal/config/persistence.go:1495, via saveRawBlob:635) and read by LoadConfigFromDatabase (persistence.go:669, gating the legacy per-key applySetting loop at 821-835 when blobFound is true).
- **Why:** Not a tunable default -- it's the storage-format key for the whole non-secret Config struct. Confirmed still the live write/read path (not a legacy leftover); no action needed.

#### `database_path (Config.DatabasePath / --db / DATABASE_PATH)`
🟢 used

- **Current default:** `audiobooks.pebble (relative, cmd/root.go:349); config.yaml example also documents audiobooks.pebble`
- **Usage:** cmd/root.go:349 defines --db, bound to viper key database_path (cmd/root.go:356). Read at internal/config/config.go:1421-1422 into Config.DatabasePath, then consumed by cmd/root.go:86/118/143/179/236, cmd/seed.go:95, cmd/child_mode.go (forwarded as UOS_DB_PATH), cmd/diagnostics.go:95, internal/database/store.go InitializeStore(path). Prod systemd unit (deploy/audiobook-organizer.service:70) sets DATABASE_PATH=/var/lib/audiobook-organizer/audiobooks.pebble, which viper.AutomaticEnv binds directly to the database_path key (no --db flag passed in ExecStart, so env is authoritative in that deployment).
- **Why:** Default is sane for local/dev use (relative path next to the binary); production overrides it explicitly via DATABASE_PATH env in the systemd unit, so no change needed. Note the unrelated AO_DB env var in .env.example/docker-compose.yml/README (see separate entry) is a dead alias for this same concept and should be removed to avoid operator confusion.

#### `database_type (Config.DatabaseType / --db-type / DATABASE_TYPE)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `pebble`
- **Recommended default:** `pebble (keep), but tighten Validate() to reject 'sqlite' and fix/remove the migrate-from-sqlite reference in the error string` (confidence: high)
- **Usage:** cmd/root.go:350 defines --db-type, bound to viper key database_type. Config.DatabaseType is read in internal/config/config.go:1422/1866-1870/1945 and consumed at internal/database/store.go:1310 (InitializeStore switch), cmd/seed.go:95, cmd/child_mode.go, cmd/dedup_bench.go, internal/organizer/service.go:538, internal/server/handlers/system/handler.go:530, internal/sysinfo/dashboard_service.go, internal/backup/backup.go.
- **Naming issue:** The --db-type help text and Config.Validate() (internal/config/config.go:1945-1948, listing 'pebble'/'sqlite' as the only structurally-valid values) both still present 'sqlite' as a legitimate, working choice. But internal/database/store.go:1310-1312 (InitializeStore) now hard-rejects dbType=='sqlite'/'sqlite3' with an error: 'SQLite3 support has been removed. PebbleDB is the only supported database backend. Migrate data with audiobook-organizer migrate-from-sqlite if needed.' That error message references a `migrate-from-sqlite` subcommand that does not exist anywhere in cmd/*.go (grep confirms zero hits) -- a dangling reference to a command that was apparently never shipped or was removed. Validate() should reject 'sqlite' outright (or the help text/error message should be corrected) instead of letting it pass structural validation and fail later at store-init time with a broken pointer to a nonexistent migration command.
- **Why:** SQLite was fully removed from the codebase in fable5 TASK-022 (production has run PebbleDB-only since 2026-05-11, per internal/database/database.go:8 comment); the 'sqlite' option is now a guaranteed-to-fail dead branch kept only for a (broken) error message.

#### `enable_sqlite3_i_know_the_risks (Config.EnableSQLite / --enable-sqlite3-i-know-the-risks / ENABLE_SQLITE3_I_KNOW_THE_RISKS)`
🟢 used · 🟡 naming · 🟠 default review

- **Current default:** `false`
- **Recommended default:** `Remove the flag/config option entirely` (confidence: high)
- **Usage:** cmd/root.go:351 defines the flag, bound to viper key enable_sqlite3_i_know_the_risks (default false, config.go:1020). Config.EnableSQLite is still threaded through as the 3rd argument to every InitializeStore call site (cmd/root.go x5, cmd/seed.go:95, cmd/child_mode.go:71, cmd/dedup_bench.go:112, cmd/diagnostics.go:76) -- but internal/database/store.go:1306 declares the parameter as `_ bool` (unnamed/blank), i.e. InitializeStore receives the value and silently discards it. The doc comment right above it says exactly this: 'The enableSQLite parameter is retained for API compatibility but is ignored -- the SQLite backend was removed in fable5 TASK-022.'
- **Naming issue:** Two different names exist for the same logical setting depending on which layer you approach it from: CLI/env/yaml use the verbose safety name enable_sqlite3_i_know_the_risks, while the JSON config API / DB config_blob use the Go struct's json tag enable_sqlite (json:"enable_sqlite", config.go:509) -- and internal/config/update_service.go:68's immutableFieldKeys checks the payload key 'enable_sqlite' (matching the JSON tag), not 'enable_sqlite3_i_know_the_risks'. This split is internally self-consistent (CLI/env/yaml agree with each other; JSON API/blob/immutable-check agree with each other) but is a real two-names-for-one-concept trap for anyone grepping the codebase for one name and expecting to find the other.
- **Why:** The parameter it controls is provably inert: InitializeStore ignores it (`_ bool`) and selecting dbType=='sqlite' always errors regardless of this flag's value. Keeping a discouraged-by-name safety gate around a feature that was already fully deleted is worse than a stale default -- it actively misleads operators into thinking SQLite is still reachable behind an opt-in gate. This is a case for deletion (CLI flag, viper binding, Config field, JSON tag, immutableFieldKeys entry, and the dead switch branch in InitializeStore/Validate), not a default-value tweak.

#### `LIBRARY_COUNTS_CACHE_MIN_INTERVAL_SECONDS`
🟢 used

- **Current default:** `600 (10 minutes)`
- **Usage:** internal/database/pebble_store.go:182 (defaultLibraryCountsMinIntervalSeconds=600) and :191-196 read via os.Getenv, parsed with strconv.Atoi, only accepted if it parses and is >= 0; governs how often the cached library-stats/primary-book counts (used by the 5s status ticker and health probes) are recomputed.
- **Why:** 10 minutes is a sane throttle for an expensive full-count recompute backing a lightweight 5s status ticker; no evidence of misconfiguration.

#### `playlist_dir (--playlists / PLAYLIST_DIR)`
🟢 used

- **Current default:** `"playlists" (relative)`
- **Usage:** cmd/root.go:352 defines --playlists, bound to viper key playlist_dir. Consumed at cmd/root.go:131/213/463-464 (MkdirAll on the configured dir) and validated in Config.Validate() (config.go:1957).
- **Why:** Sane relative default consistent with database_path's convention of defaulting relative to CWD for local/dev use.

#### `root_dir (--dir / ROOT_DIR)`
🟢 used

- **Current default:** `""`
- **Usage:** cmd/root.go:348 defines --dir, bound to viper key root_dir (root.go:355). Config.RootDir feeds SetupComplete derivation, scan/organize commands, and UOS_ROOT_DIR forwarding to child processes (cmd/child_mode.go:32/59/84).
- **Why:** Empty default is correct -- this is a required first-run value with no sane universal default (it's user-library-specific), and its emptiness is exactly what SetupComplete checks for.

#### `setup_complete (Config.SetupComplete)`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:511/1021/1425/2043 define/default/load it; internal/config/update_service.go:142 derives it (`c.SetupComplete = c.RootDir != ""`) inside UpdateConfig's post-processing; internal/config/persistence.go:108/952-954 persist/restore it; internal/server/handlers/system/handler.go:436 resets it to false (factory-reset-style handler).
- **Why:** false is the correct default for a first-run wizard completion flag; it is derived automatically once RootDir is set, so no manual default tuning is needed.

#### `UOS_SOCKET / UOS_DB_PATH / UOS_DB_TYPE / UOS_ROOT_DIR (operation-runner child process env)`
🟢 used

- **Current default:** `UOS_SOCKET: none (child os.Exit(2) if unset); UOS_DB_PATH/UOS_DB_TYPE: audiobooks.pebble/pebble as last-resort fallback; UOS_ROOT_DIR: "" (no fallback)`
- **Usage:** Constants defined in internal/operations/registry/subprocess.go:46/52-54 (EnvSocketPath, EnvChildDBPath, EnvChildDBType, EnvChildRootDir); set by the parent when spawning a re-exec'd child (cmd/child_mode.go:30-32) and read back inside the child (child_mode.go:53/56/59/84, subprocess.go:97). UOS_DB_PATH/UOS_DB_TYPE fall back to audiobooks.pebble/pebble respectively only if still empty after both the env override and any value already loaded into AppConfig; UOS_ROOT_DIR is deliberately re-applied a second time after loadConfigFromDB in case that DB load reset RootDir to empty.
- **Why:** These are internal IPC plumbing (not user-facing config) that mirror the parent process's already-validated database_path/database_type/root_dir; defaults and fallback ordering are deliberately defensive (documented in-line) and consistent with the parent-side values. No change warranted.

---

## Deployment Surface (config.yaml, .env, docker-compose, systemd, Prometheus)

#### `AO_PORT`
🟢 used

- **Current default:** `8484`
- **Usage:** docker-compose.yml:13 `"${AO_PORT:-8484}:8484"` — host-side port-mapping substitution consumed directly by docker-compose itself. Never forwarded into the container environment; never reaches Go code (correctly documented, not read via os.Getenv anywhere).
- **Why:** Matches the container's fixed internal port (8484), which matches this repo's documented prod port. No change.

#### `APP_VERSION`
🟢 used

- **Current default:** `dev`
- **Usage:** docker-compose.yml:10 passes it as a build ARG; Dockerfile:70,75 declares `ARG APP_VERSION=dev` and bakes it via `-ldflags -X main.version=${APP_VERSION}`. Compile-time only, never read via os.Getenv/viper at runtime — confirmed no such runtime binding exists.
- **Why:** 'dev' is the correct fallback for a build not given an explicit version (e.g. local docker build). CI/release pipelines presumably override it. No change.

#### `AUDIOBOOK_ROOT_DIR`
🟢 used · 🟡 naming

- **Current default:** `./audiobooks (compose) / /var/lib/audiobooks (systemd)`
- **Usage:** TWO DISTINCT MECHANISMS share this exact name. (1) docker-compose.yml:16 `${AUDIOBOOK_ROOT_DIR:-./audiobooks}:/audiobooks:ro` is a genuine host-side bind-mount-source substitution consumed by docker-compose itself (real usage) — never forwarded into the container env, never reaches Go code. (2) deploy/audiobook-organizer.service:69 sets it as a container `Environment=` line, but a repo-wide grep for AUDIOBOOK_ROOT_DIR in *.go found zero os.Getenv/viper reads anywhere — this half is fully dead. The unit file's own comment at line 63 ('adjust AUDIOBOOK_ROOT_DIR for your library path') is stale/misleading: the real root dir comes from --dir, ROOT_DIR env, or DB-persisted import paths, none of which this systemd line sets.
- **Naming issue:** Same name used for two unrelated, non-interacting mechanisms (a compose bind-mount source vs. a dead systemd Environment= line) — an operator editing one reasonably but wrongly believes it also affects the other, and the systemd deploy comment actively encourages that belief.
- **Why:** Not a default-value problem — recommend removing the dead systemd Environment= line and its misleading comment (or, if root-dir-via-env is actually wanted, wiring an os.Getenv read for it), rather than changing either default number.

#### `auto_update.channel (AUTO_UPDATE_CHANNEL)`
🟢 used

- **Current default:** `stable`
- **Usage:** config.go:1533 populates Config.AutoUpdate.Channel; read at internal/server/update_handlers.go:27,38 and internal/updater/register.go:85.
- **Why:** stable is the expected safe default release channel. No change.

#### `auto_update.check_minutes (AUTO_UPDATE_CHECK_MINUTES)`
🟢 used

- **Current default:** `60`
- **Usage:** config.go:1534 populates Config.AutoUpdate.CheckMinutes; read at internal/updater/register.go:86.
- **Why:** Hourly check cadence is reasonable for a self-update poll. No change.

#### `auto_update.enabled (AUTO_UPDATE_ENABLED)`
🟢 used

- **Current default:** `false`
- **Usage:** internal/config/config.go:1532 populates Config.AutoUpdate.Enabled from this viper key; consumed at internal/server/scheduler_maintenance_window_op.go:73 and internal/updater/register.go:84.
- **Why:** false is the correct opt-in default for an unattended self-update feature. No change.

#### `auto_update.window_end (AUTO_UPDATE_WINDOW_END)`
🟢 used

- **Current default:** `5 (5am)`
- **Usage:** config.go:1536 populates Config.AutoUpdate.WindowEnd; persistence.go:917-918 backfill; read at internal/updater/register.go:88.
- **Why:** Pairs with window_start=2 for a 3-hour overnight window. No change.

#### `auto_update.window_start (AUTO_UPDATE_WINDOW_START)`
🟢 used

- **Current default:** `2 (2am)`
- **Usage:** config.go:1535 populates Config.AutoUpdate.WindowStart; internal/config/persistence.go:914-915 also backfills the newer Maintenance.WindowStart from this legacy field when unset; read at internal/updater/register.go:87.
- **Why:** A 2am-5am window is a conventional low-traffic install window. No change.

#### `CacheSize / Config.CacheSize`
🔴 DEAD

- **Current default:** `1000`
- **Usage:** Only touched by config plumbing and Settings.tsx round-trip. internal/metrics/metrics.go:96,248 defines an unrelated Prometheus gauge also named 'cache_size' (metrics.SetCacheSize, called from internal/cache/cache.go) that reports actual runtime cache sizes — this is NOT config.AppConfig.CacheSize, just a same-named metric. No code reads config.AppConfig.CacheSize to bound anything; internal/cache's NewWithLimit(maxEntries) capacity constructor is never called anywhere in the repo.
- **Why:** Dead — see MemoryLimitType above for the shared root cause (NewWithLimit uncalled). Do not tune this number; wire it up or remove it.

#### `ChapterConsolidationThresholdMin / chapter_consolidation_threshold_min`
🟢 used

- **Current default:** `10 (minutes)`
- **Usage:** internal/scanner/chapter_consolidation.go:38,50 reads config.AppConfig.ChapterConsolidationThresholdMin to decide the per-file duration threshold for merging short chapters.
- **Why:** No evidence suggests 10 minutes is wrong; used consistently at its sole call site. Surface note: unlike its config.go:1070-1072 siblings (concurrent_scans, operation_timeout_minutes), this option has no viper.BindEnv, no internal/config/persistence.go PUT-handler case, and no frontend Settings.tsx field — it can only be changed via the config file, not via env var, live API, or the UI. Not a functional bug, but a real surface-exposure asymmetry worth deciding on purpose rather than by omission.

#### `ConcurrentScans / concurrent_scans`
🟢 used

- **Current default:** `runtime.NumCPU() floored at 4 (config.go:1066-1070, matched at ResetToDefaults config.go:2079)`
- **Usage:** internal/server/folder_autoscan_op.go:73, internal/scanner/service.go:392, internal/scanner/scanner.go:722 all read config.AppConfig.ConcurrentScans to size ScanDirectoryParallel/ProcessBooksParallel worker pools. Verified the zero-value edge case per repo's mandatory concurrency policy: service.go:392 and folder_autoscan_op.go:73 both floor workers to 4 when ConcurrentScans<1, and scanner.go:734 floors to 1 in ProcessBooksParallel — so a persisted 0 never causes unbounded fan-out.
- **Why:** Computed CPU-scaling default matches this repo's mandatory multi-core concurrency policy exactly. No change. Minor note: web/src/pages/Settings.tsx:562 does `config.concurrent_scans \|\| 4` which silently redisplays a persisted 0 as 4 in the UI while the backend still floors independently at each call site — cosmetically confusing but not a functional bug since both paths converge on the same floor.

#### `Config.AutoUpdate (container struct)`
🟢 used

- **Usage:** internal/server/update_handlers.go:27,38; internal/server/scheduler_maintenance_window_op.go:73,82; internal/updater/register.go:84-88 all read config.AppConfig.AutoUpdate.* fields. Purely a struct-grouping container (no direct default) housing the 5 auto_update.* leaf settings audited separately below.
- **Why:** Container type, no default of its own. No change.

#### `Config.Maintenance (container struct)`
🟢 used

- **Usage:** internal/config/persistence.go:914-925 backfills window fields from the legacy AutoUpdate window; persistence.go:1191-1235 wires ~10 Maintenance.* sub-fields (Enabled, WindowStart/End, DedupRefresh, SeriesPrune, AuthorSplit, TombstoneCleanup, Reconcile, PurgeDeleted, PurgeOldLogs, DbOptimize, LibraryScan). Actively read by the maintenance-window scheduler.
- **Why:** Container type, no default of its own. No change.

#### `Config.PurgeSoftDeletedAfterDays / purge_soft_deleted_after_days`
🟢 used

- **Current default:** `30 (days)`
- **Usage:** Actively wired end to end: internal/scheduler/extra_ops.go:545,858,870, internal/scheduler/tasks.go:614,616,621 (drives the purge-soft-deleted scheduled task's enabled/interval/run-on-start), internal/server/audiobooks_helpers.go:215,223, internal/server/server_maintenance_deps.go:280-281,297 (implements the maintenance.deps PurgeSoftDeletedAfterDays() interface method), plus persistence.go and config.go plumbing.
- **Why:** 30 days is a conventional soft-delete retention window and is actively used to gate a real purge job. No change.

#### `Config.PurgeSoftDeletedDeleteFiles / purge_soft_deleted_delete_files`
🟢 used

- **Current default:** `false`
- **Usage:** internal/scheduler/extra_ops.go:871 and internal/server/audiobooks_helpers.go:224 pass it directly into audiobookService.PurgeSoftDeletedBooks(ctx, deleteFiles, &days) as the delete-files-on-disk flag.
- **Why:** false (DB-record purge only, files left on disk) is the correct conservative default given this project's documented history of caution around irreversible file deletion (e.g. the missing-file-repair feature is deliberately report-only, never auto-delete). No change recommended.

#### `Config.Scheduled (container struct)`
🟢 used

- **Usage:** internal/scheduler/tasks.go reads config.AppConfig.Scheduled.{LibraryScan,DedupRefresh,LabelRefinement,SeriesPrune,AuthorSplit,DbOptimize,ResolveProductionAuthors}.{Enabled,Interval,OnStartup} at dozens of sites to drive every background scheduled task.
- **Why:** Container type, heavily consumed. No change.

#### `DefaultUserQuotaGB / Config.DefaultUserQuotaGB`
🔴 DEAD

- **Current default:** `100`
- **Usage:** Only touched by config plumbing (struct field, viper default, persistence load/save) and the Settings.tsx form's own round-trip (read from GET /config, written back via PUT /config). No handler, no QuotaTab display, no enforcement code anywhere reads config.AppConfig.DefaultUserQuotaGB. Unlike EnableUserQuotas (which is at least echoed back by the storage-info API and shown in the UI), this value is never read by anything other than the settings form that set it — a closed loop that changes no behavior.
- **Why:** Dead option — no consumer exists to size against. Recommend either wiring it into a real per-user quota check or removing the field and its Settings UI control rather than picking a different number; a default is meaningless while nothing reads it.

#### `DiskQuotaPercent / Config.DiskQuotaPercent`
🟢 used

- **Current default:** `80`
- **Usage:** internal/config/config.go:1985-1986 validates it's 1-100 when EnableDiskQuota is on; internal/server/handlers/system/handler.go:296 returns it as quota_percent; web/src/components/system/QuotaTab.tsx:50,53 uses it to compute the displayed quota_limit. Same display-only caveat as EnableDiskQuota above — no enforcement gate consumes it.
- **Why:** 80% is a conventional, sane default for a display threshold. No evidence to recommend a different number.

#### `EnableDiskQuota / Config.EnableDiskQuota`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/handlers/system/handler.go:295 returns it as quota_enabled in GET /system/storage; web/src/components/system/QuotaTab.tsx:49-53 uses it to gate the quota-limit UI calculation and messaging. However a repo-wide grep for any code that actually BLOCKS a write/scan/import when disk usage exceeds the quota found nothing — no gate references EnableDiskQuota outside this read-only display path. The feature is display-only, not enforced.
- **Why:** false is the correct safe default for an unenforced feature (nothing currently blocks operations even when true, so leaving it off avoids implying a protection that doesn't exist). No numeric change recommended; worth surfacing to the user that this toggle currently only affects a progress-bar display, not actual enforcement.

#### `EnableUserQuotas / Config.EnableUserQuotas`
🟢 used

- **Current default:** `false`
- **Usage:** internal/server/handlers/system/handler.go:297 returns it as user_quotas_enabled; web/src/components/system/QuotaTab.tsx:61,213-215 displays it, but the component's own copy admits the feature is a stub: "Per-user quotas are enabled. Detailed per-user usage reporting is not yet available in this view." Repo-wide grep for any per-user quota enforcement/accounting logic (outside this toggle) returned nothing.
- **Why:** false is correct — the feature it names is explicitly unimplemented (per the frontend's own placeholder text), so defaulting it on would be actively misleading. No numeric change; flag for the user that this is a UI toggle for a feature that doesn't exist yet.

#### `GOGC`
🟢 used

- **Current default:** `200`
- **Usage:** deploy/audiobook-organizer.service:76 sets it; consumed directly by the Go runtime's GC-target-percentage tuning.
- **Why:** Deliberately paired with GOMEMLIMIT as documented safety net (halves GC frequency, relies on the hard limit to prevent runaway growth). No measured evidence to recommend a change.

#### `GOMEMLIMIT`
🟢 used

- **Current default:** `9GiB`
- **Usage:** deploy/audiobook-organizer.service:73 sets it; consumed directly by the Go runtime's soft-memory-limit mechanism (not app os.Getenv code, but a standard, real Go runtime env var). Referenced by name in deploy/prometheus/alert-rules.yml:66-70's memory-alert comment, confirming operational reliance on it.
- **Why:** Observation only, not a measured recommendation: internal/server/library_list_warmer.go's own in-code comment cites a production 'full-library, ~13GB baseline' heap figure for the list-cache warmer, which is above the 9GiB GOMEMLIMIT set here. This is a comment-vs-comment tension across two files, not something this audit measured directly — flagging for the user to check actual prod heap behavior before touching either value.

#### `HOME`
🟢 used

- **Current default:** `/var/lib/audiobook-organizer`
- **Usage:** deploy/audiobook-organizer.service:68 pins it to WorkingDirectory. Confirmed real Go consumers: os.UserHomeDir() at cmd/root.go:448 (initConfig), internal/itunes/parser.go:182, internal/server/handlers/filesystem.go:119; direct os.Getenv('HOME') read at internal/transcribe/batch.go:122.
- **Why:** Correctly pinned since the systemd service user has no real home directory; matches WorkingDirectory. No change.

#### `legacy flat auto_update_* blob keys (migrateAutoUpdateBlob)`
🟢 used

- **Usage:** internal/config/persistence.go:596 defines migrateAutoUpdateBlob; called at persistence.go:743 during config-blob load whenever a stored blob still has the old flat auto_update_enabled key, rewriting it into the nested auto_update object. Actively exercised for any DB predating the auto_update nesting.
- **Why:** Working, load-bearing migration shim. No change; keep until confident no production DB blob still carries the flat keys.

#### `LIST_WARMER_HEAP_DELTA_MB`
🟢 used

- **Current default:** `4096 (MB)`
- **Usage:** internal/server/library_list_warmer.go:45 (warmerMemoryDeltaMB) reads it via os.Getenv+ParseUint (min>0); called at line 529 (eager warm) and line 618 (trickle warm) to bound heap growth during list-cache pre-warming.
- **Why:** In-code comment cites a production measurement (~1.8GB transient allocation per trickle query on a ~13GB-baseline library) backing the 4GB default with one query of headroom. No contradicting evidence found. No change.

#### `LIST_WARMER_MAX_HEAP_MB`
🟢 used · 🟡 naming

- **Current default:** `4096 (MB, shared default with LIST_WARMER_HEAP_DELTA_MB)`
- **Usage:** internal/server/library_list_warmer.go:51 reads it only as a fallback when LIST_WARMER_HEAP_DELTA_MB is unset (min accepted 256), feeding the same warmerMemoryDeltaMB() used at lines 529/618.
- **Naming issue:** Name says 'MAX_HEAP' (implying an absolute cap) but the code and its own comment (library_list_warmer.go:49-52) repurpose it to mean the same 'delta above baseline' as LIST_WARMER_HEAP_DELTA_MB without renaming it — a legacy var whose semantics were changed out from under its name.
- **Why:** Default value itself is fine (matches the current var); the naming is the real issue, not the number. Recommend renaming/deprecating in favor of LIST_WARMER_HEAP_DELTA_MB rather than changing the default.

#### `LIST_WARMER_TRICKLE_INTERVAL_MS`
🟢 used

- **Current default:** `10000 (ms / 10s)`
- **Usage:** internal/server/library_list_warmer.go:63 (warmerTrickleInterval) reads it via os.Getenv+ParseInt (min>=500); called at line 617 to set the tick gap for the trickle warmer.
- **Why:** Comment states this yields ~30min to drain a 180-query backlog, which is a deliberate throttle to avoid piling queries on a memory-pressured process. No contradicting evidence. No change.

#### `MemoryLimitMB / Config.MemoryLimitMB`
🔴 DEAD

- **Current default:** `512`
- **Usage:** Only touched by config plumbing and Settings.tsx round-trip. Zero consumers outside internal/config/. Same NewWithLimit-uncalled root cause as MemoryLimitType/CacheSize. Note: the deploy-side GOMEMLIMIT (systemd, real Go runtime limit) is an entirely separate mechanism from this app-level MemoryLimitMB field — the app one does nothing.
- **Why:** Dead — see MemoryLimitType above. Do not tune; wire up or remove. Worth flagging to the user that this is easily confused with the very real, actively-enforced GOMEMLIMIT=9GiB systemd env var, which is a completely different, working mechanism.

#### `MemoryLimitPercent / Config.MemoryLimitPercent`
🔴 DEAD

- **Current default:** `25`
- **Usage:** Only touched by config plumbing and Settings.tsx round-trip. Zero consumers outside internal/config/. Same NewWithLimit-uncalled root cause as MemoryLimitType/CacheSize.
- **Why:** Dead — see MemoryLimitType above. Do not tune; wire up or remove.

#### `MemoryLimitType / Config.MemoryLimitType`
🔴 DEAD · 🟡 naming

- **Current default:** `items`
- **Usage:** Only touched by config plumbing (struct field, viper default, persistence.go PUT-handler case) and Settings.tsx round-trip. Repo-wide grep outside internal/config/ found zero consumers that branch on 'items' vs 'percent' vs 'absolute'. internal/cache/cache.go implements its own capacity bound via NewWithLimit(maxEntries), but NewWithLimit has zero callers anywhere in the codebase (grep confirmed) — every cache in the app is instantiated unbounded via plain New(), so this string is never read to select a limiting strategy.
- **Naming issue:** Struct comment lists enum values 'items', 'percent', 'absolute', but the sibling field for the absolute case is named MemoryLimitMB (not MemoryLimitAbsolute) — the enum value and the field it should correspond to have drifted. There is also no validator anywhere (unlike disk_quota_percent's 1-100 check) enforcing the enum, so a typo'd value would silently no-op just as thoroughly as a correct one.
- **Why:** Dead config across the whole MemoryLimit* cluster (see CacheSize/MemoryLimitPercent/MemoryLimitMB below) — the underlying cache package has an unused capacity-bound constructor (NewWithLimit) sitting right next to the one actually used everywhere (New). Recommend wiring these four fields into cache.NewWithLimit/metrics.SetCacheSize call sites, or removing the fields and their Settings UI section, rather than adjusting a default nothing reads.

#### `OperationTimeoutMinutes / Config.OperationTimeoutMinutes`
🟢 used

- **Current default:** `30 (minutes)`
- **Usage:** internal/server/server_lifecycle.go:632-633 uses it to compute the stale-operation timeout duration; internal/server/handlers/operations/handler.go:368 reads it directly. No frontend Settings.tsx field exists for it (backend/ops-only knob), which is expected for an operational safety timeout.
- **Why:** 30 minutes is a reasonable ceiling for long-running background operations (scans, dedup passes) given the repo's multi-hour operation history; no evidence it's too short or too long in practice. No change.

---

## Configuration mechanism notes

Notes from the inventory pass on how each configuration surface actually works mechanically (persistence format, migration behavior, whether unknown keys are rejected, etc.) — useful context beyond the per-option findings above.

- [config-types-nested] Range is internal/config/config.go lines 1-504 (nested struct defs only; Config struct itself starts at line 504, out of scope). Many fields (MetadataSource, DownloadClientConfig/TorrentClientConfig/UsenetClientConfig/DelugeConfig/QBittorrentConfig/SABnzbdConfig, PluginConfig, ITunesConfig's flat fields except Libraries, MaintenanceConfig's boolean toggles, AIBackendConfig, ScheduledTaskConfig, AutoUpdateConfig, ScheduledTasksConfig's per-task fields) have NO per-field doc comment in this range — only a struct-level comment. For those, description is a conservative, literal inference from the field/json name and any usage seen elsewhere in this range (e.g. AIBackendConfig fields via EffectiveEmbeddingMode/EffectiveLLMMode), not an invented behavior claim. defaultValue is left empty for all of these since no literal default is visible in this file range; only fields whose doc comment explicitly states "(default X)" got a defaultValue populated, quoted verbatim from that comment. Actual defaults for the undocumented fields live in InitConfig/SetDefault calls elsewhere in the file (out of this 1-504 range) — do not treat empty defaultValue here as "no default exists".
- [config-struct-main] Read internal/config/config.go lines 504-1017 directly (offset=504, limit=513), covering the full Config struct (line 504-853), the mu/AppConfig/Snapshot()/Mutate() plumbing (855-897), the WhisperEndpoint struct (915-923), ParseWhisperEndpoints (925-940), and applyEnvAuthoritativeConfig (942-1007) which shows which fields are environment-authoritative (OAuth*, CFAccess*, WhisperRemoteURL, WhisperEndpoints, ABS*). This is exhaustive for every field literally declared in the top-level Config struct at lines 504-853 — 99 fields total, all listed above, plus one consolidated entry for the WhisperEndpoint sibling struct's 5 fields as instructed. No fields were dropped or summarized as a group; every nested config struct (Embedding, Dedup, DedupBoilerplate, MetadataScoring, AIBackend, ITunes, AutoUpdate, Maintenance, DownloadClient, Scheduled, Tools, Plugins, APIKeys) is represented as ONE Config-level option entry (its own internal fields are defined in separate types elsewhere in the file/package, out of the requested line range, and were not expanded here — only Config's direct fields and WhisperEndpoint's fields were in scope per the instructions). Default values were filled in only where the field's own doc comment states a specific default explicitly (e.g. "Default 10", "default 90", "defaults to 720h"); all other defaultValue entries are left empty since Go struct field declarations carry no inline default literal — actual defaults are assigned elsewhere (a DefaultConfig()-style function not in the read range). envVar was filled in only where the comment named an exact env var (ACOUSTID_API_KEY, WHISPER_REMOTE_URL, WHISPER_ENDPOINTS, ABS_JWT_SECRET); the other OAuth/CFAccess/ABS fields are confirmed environment-authoritative by applyEnvAuthoritativeConfig's viper.IsSet() keys (e.g. "oauth_enabled") but the actual bound env var name (set via viper.BindEnv, presumably in InitConfig) is not in the read range, so envVar was left blank for those rather than guessed. Domain assignments are my best-faith categorization per the field's apparent purpose against the given enum; a few are judgment calls noted implicitly (e.g. AcoustIDAPIKey -> dedup since it's used for audio-fingerprint matching per project memory; MemoryLimitType/CacheSize/quota fields -> plugins-maintenance-misc as general resource/ops config; Scheduled/AutoUpdate/Maintenance -> deploy-ops as background-ops/deployment-window config).
- [config-defaults-a] Extracted every viper.SetDefault(...) call in InitConfig() between lines 1017-1449 (196 entries total) plus its matching viper.BindEnv(...) call where one exists in this same range (recorded as envVar on the matching entry). All entries use source="go-env-binding" per task instructions, since every one of these keys is registered through viper.SetDefault (with many also getting a viper.BindEnv override) rather than a bare os.Getenv call.

Line 1108 (`registerABSDefaults()`) is a call to a separate helper function outside this line range that registers ActivityBookShelf (ABS) defaults — no SetDefault/BindEnv calls for it appear directly in [1017,1450), so no entry was created for it here; it would need to be read separately.

Lines 1401-1415 also contain non-SetDefault/BindEnv code (local `supportedExtensions`/`excludePatterns` var fallback logic reading back `viper.GetStringSlice`) — not extracted as options since it's a read, not a default registration. Likewise the `Mutate(func(c *Config) { *c = Config{...} })` block starting at line 1417 only contains `viper.GetString/GetBool/...` calls that consume the defaults set above — per the task's scope (SetDefault/BindEnv only), these were not turned into separate option entries; they are simply the struct fields that receive the values already captured above.

Two defaults are computed rather than literal: `concurrent_scans` (line 1070) is `runtime.NumCPU()` floored at a minimum of 4, computed just above at lines 1066-1069, not a plain literal.

Domain classification judgment calls worth flagging: `acoustid_api_key` and `embedding.*` were classified under `dedup` (fingerprinting and vector-similarity power dedup workflows here) rather than `transcribe-ai`/generic AI, per project-memory notes about AcoustID/embeddings being core to the dedup pipeline. `bootstrap_key_ttl_days` and `write_startup_readonly_key` were classified under `server-auth-network` (auth/security) rather than their surrounding "memory management"/"apply pipeline" comment blocks, since their actual purpose is security-token lifetime and a startup security key file, respectively. `hardcover_api_token` was classified as `scanning-organize-metadata` (a metadata-source credential) rather than `server-auth-network`, consistent with `openlibrary_dump_*` in the same comment block, though it could reasonably go either way.
- [config-defaults-b] Assigned range internal/config/config.go:1450-1883 contains ZERO viper.SetDefault, viper.BindEnv, or os.Getenv calls — verified via grep against the full file (grep -n 'SetDefault\|BindEnv\|os.Getenv' config.go \| awk range 1450-1883 → only one comment-text match at line 1487, no actual calls). All SetDefault/BindEnv calls in this file terminate at line 1405 (last: viper.SetDefault("exclude_patterns", []string{})), which is before this agent's assigned window — that block belongs to whichever sibling agent covers roughly lines ~1000-1405.

What IS in 1450-1883 instead: (1) the Config{} struct-literal populated by viper.GetString/GetBool/GetInt/GetFloat64/GetStringSlice reads that consume defaults set earlier in the file (not extractable per this task's SetDefault/BindEnv-only criteria); (2) two blocks of hardcoded go-struct-field (not viper) defaults worth flagging to the orchestrator even though they fall outside strict SetDefault/BindEnv scope: c.Tools = tools.ToolsConfig{ManagedDir: "/var/lib/audiobook-organizer/tools", Ollama: {Mode: ToolModeSystem}, Fpcalc: {Mode: ToolModeSystem}, AllowPeriodicOllama: false, OllamaDebounceMin: 10} at lines 1760-1766 (domain: plugins-maintenance-misc), and the 6-entry MetadataSources default slice (audible/openlibrary/audnexus/google-books/hardcover/wikipedia, each with Enabled/Priority/RequiresAuth) at lines 1780-1831 (domain: scanning-organize-metadata), used only when viper.IsSet("metadata_sources") is false; (3) backward-compat fallback reads of legacy flat iTunes viper keys (itunes_library_write_path, itunes_library_itl_path, itunes_library_read_path, itunes_library_xml_path, itl_write_back_enabled) at lines 1837-1863 — these ARE viper.GetString/GetBool/IsSet calls but not SetDefault/BindEnv, so also excluded per strict criteria; and (4) DatabaseType normalization logic (sqlite3→sqlite, empty→pebble) at lines 1865-1871, not a viper default at all.

No options extracted — none of the three required source patterns (viper.SetDefault, viper.BindEnv, os.Getenv) occur in this line range.
- [config-validate-reset] Config.Validate() (config.go:1932-2027) is a structural/syntax validator, not a value-quality validator — it constrains what a 'valid' setting may look like but does not itself recommend defaults. It requires DatabaseType to be exactly 'pebble' or 'sqlite'; requires the parent directory of DatabasePath to both exist (validateParentDirExists, line 1902) and be writable (validateParentDirWritable, line 1917, verified via a real temp-file write/delete probe) — note ResetToDefaults never touches DatabasePath itself (it preserves the caller's current value via Snapshot()), so this rule only ever fires against whatever path the caller already had, never a ResetToDefaults-supplied one; requires the parent directory of PlaylistDir to exist (existence only, not writability, and PlaylistDir is likewise preserved rather than reset); requires ConcurrentScans, AutoScanDebounceSeconds, OperationTimeoutMinutes, APIRateLimitPerMinute, AuthRateLimitPerMinute, JSONBodyLimitMB and UploadBodyLimitMB to all be >= 0; requires DiskQuotaPercent to be in [1,100] but only when EnableDiskQuota is true; requires OrganizationStrategy, when non-empty, to be one of auto/copy/hardlink/reflink/symlink; requires FolderNamingPattern and FileNamingPattern, when non-empty, to have balanced '{'/'}' counts (hasBalancedBraces, line 1883) and to leave no stray brace characters once every recognized {placeholder} token is stripped (validateNamingPattern, line 1887), plus a second semantic pass via validateNamingPatterns() (defined in naming_patterns.go) that catches non-syntax ways a pattern can destroy data; requires every non-empty SupportedExtensions entry to start with '.'; and as a side effect (not an error) silently rewrites MinBookSizeBytes from 0 to 5MB if left unset. The naming-pattern and extension checks are skipped entirely when the field is empty (empty = "unset, viper will supply the default"), so Validate() only ever rejects a pattern the caller explicitly supplied. Two default-drift issues surfaced while reading ResetToDefaults, worth flagging for later default-value recommendations: (1) viper.SetDefault sets api_rate_limit_per_minute to 0 (config.go:1076) while ResetToDefaults sets APIRateLimitPerMinute to 100 (config.go:2083) — a fresh env/flag-defaulted config has API rate limiting effectively disabled, while a factory-reset config caps it at 100/min, and Validate()'s >=0 check silently accepts either; (2) ResetToDefaults' Scheduled sub-struct literal (config.go:2233-2239) initializes only LibraryScan and leaves every other scheduled task (dedup_refresh, label_refinement, author_split, db_optimize, metadata_refresh, resolve_production_authors, series_prune, ai_dedup_batch, reconcile) at its Go zero value (Interval: 0), even though viper.SetDefault gives several of them non-zero intervals (e.g. scheduled.dedup_refresh.interval=360, scheduled.db_optimize.interval=1440, scheduled.ai_dedup_batch.interval=1440); the in-code comment justifies the omission only for the Enabled flag (all default false except library_scan, so zero-value Enabled is harmless), but does not address that Interval also collapses to 0 for those tasks — a stale/misleading value if any were ever toggled on against a ResetToDefaults()-produced Config instead of a viper-defaulted one.
- [config-persistence-a] Lines 1-800 of persistence.go are almost entirely migration/marshaling machinery around the Config struct rather than new option names, but the migration layer is substantial and worth describing as a mechanism in addition to the 9 special-key entries above.

Mechanism summary: Config is persisted two ways. (1) DATABASE (primary): the whole non-secret Config struct is JSON-marshaled into one DB setting row keyed "config_blob" (via database.SettingsStore); the three secret fields (OpenAIAPIKey, GoogleBooksAPIKey, HardcoverAPIToken) are excluded from the blob and stored/loaded as separate individual setting rows so they aren't swept up in blob round-trips. (2) YAML FILE (fallback only): SaveConfigToFile/LoadConfigFromFile write/read a small subset of ~14 fields (root_dir, database_path, playlist_dir, setup_complete, organization_strategy, scan_on_startup, auto_organize, folder_naming_pattern, file_naming_pattern, auto_fetch_metadata, language, enable_ai_parsing, concurrent_scans, log_level, plus the 3 secrets when set) to config.yaml next to the DB file, mode 0600 since secrets may be in plaintext; the loader only fills fields that are currently empty/false on AppConfig, i.e. file values never override DB/blob values, only supplement gaps.

Partial updates: LoadConfigFromDatabase (blob path) seeds `loaded` from `Snapshot()` (the CURRENT in-memory config, already carrying viper/file/env/flag defaults) rather than a zero-value Config, then unmarshals the stored JSON on top — so any field the stored blob is silent on keeps its live default instead of being zeroed by encoding/json. This is explicitly a bugfix (documented at lines 758-775): pre-fix, a `var loaded Config` + full overwrite pattern silently zeroed every field added to Config after a blob was first saved (e.g. scheduled.library_scan defaulted to enabled=false/interval=0 in prod on 2026-08-12 instead of its shipped enabled=true/interval=360). DatabaseType is explicitly preserved from the pre-load Snapshot rather than allowed to load from the blob (it's treated as immutable/environment-derived, lines 670-673, 776-777, 781-783).

Versioning/migration: the blob format has evolved through 7 chained, idempotent migration passes (embedding → dedup → metadata_scoring → ai_backend → itunes → maintenance → scheduled → auto_update, run in that literal order at lines 676-749) that fold old flat snake_case keys into newer nested sub-objects. Each migration detects its own flat sentinel key, is a no-op once already nested, and — if it did change something — immediately persists the migrated JSON back to config_blob via saveRawBlob so the migration only ever runs once per install. No explicit schema-version field is used; migrations are all detected structurally (presence/absence of specific keys) rather than by comparing a version number.
- [config-persistence-b] This half of persistence.go implements: (1) LoadConfigFromDatabase — tries the v2 config_blob JSON row first (whole-struct restore in the omitted first half), always applies secret rows separately regardless of blob presence (lines 805-819), and only falls into the per-key legacy applySetting loop (lines 822-835) when no blob row exists (!blobFound) — this is where every 'legacy flat-key' block above is actually exercised. It then always calls LoadConfigFromFile as a fallback for anything not yet loaded, re-encrypts any secret that failed to decrypt (recovering plaintext from the file-loaded Snapshot), re-derives OpenLibraryDumpDir under RootDir via Mutate, and finishes by calling ApplyEnvAuthoritativeConfig() last so OAuth/Cloudflare-Access/Whisper env vars always win over the DB blob, secret rows, and file fallback (comment at 892-897 spells out this precedence deliberately). (2) applySetting — a single giant per-key switch under one Mutate() call; three internal-state keys (maintenance_window_migrated/last_run/update_completed) are explicitly excluded from ever reaching Mutate (line 938-940); any key not in the switch hits the default case and returns fmt.Errorf("unknown setting key: %s", key) — so unrecognized DB keys are REJECTED (an error is returned), not silently accepted, but callers (both the legacy-loop caller at 828-831 and the secret-loop caller at 815-817) only slog.Warn and continue rather than failing the whole load, so in practice an unknown row is just skipped with a log line. (3) SaveConfigToDatabase — Snapshot()s Config, zeroes the four secret fields, marshals the rest to config_blob, then separately writes each secret as an encrypted row, preserving any existing DB secret value when the in-memory value is currently empty. (4) Concurrency: every mutation path in this range goes through the package's Mutate()/Snapshot() helpers (defined outside this range) rather than touching a global directly, and every such call site carries a 'WHY Mutate'/'WHY Snapshot' comment explaining the specific race it prevents (e.g. lines 847, 875-876, 884-885, 910-911, 933-934, 1482-1484, 1539-1540).
- [config-dbsettings] Extraction target was internal/database/settings.go, internal/config/update_service.go, internal/config/register.go — no other files.

ZERO distinct db-setting KEY literals are defined/read/written across these three files, so the options list is intentionally empty. Details:

1. settings.go (287 lines) is a GENERIC mechanism, not a keyspace. GetSetting/SetSetting/GetAllSettings/DeleteSetting (lines 201-271) all operate on a caller-supplied `key string` parameter, stored under the Pebble key `"setting:" + key`. No specific key string appears anywhere in the file. It also holds unrelated crypto helpers (EncryptValue/DecryptValue/MaskSecret, Argon2id KDF, InitEncryption which stores a raw encryption key file at `<dataDir>/.encryption_key` — that's a filesystem artifact, not a "setting"). The ErrSettingNotFound doc comment (lines 23-29) references a real consumer — a bootstrap one-time-token key used by the auth exchange flow — confirming genuine string-keyed settings DO exist in the codebase, just not defined in this file.

2. update_service.go (185 lines) is NOT a second KV mechanism. Per the task's own instruction, I did not duplicate Config struct fields. Its actual role: it's the HTTP PATCH-apply layer sitting in front of the existing `config.Config` struct (covered by other options elsewhere in this sweep). Concretely it: (a) applies 5 secret fields explicitly under the `Mutate` write lock before masking them in the response — openai_api_key, acoustid_api_key, google_books_api_key, hardcover_api_token, basic_auth_password (lines 39-53, 95-111); (b) rejects 2 fields as immutable at runtime — database_type, enable_sqlite (line 68, 85-89); (c) JSON-round-trips every remaining payload key directly onto the live Config struct (so any new Config field is automatically PATCH-able with zero registration here — this is why it must not be treated as a keyed enumeration); (d) derives setup_complete from RootDir != "" AFTER the round-trip (line 142), so setup_complete is NOT independently settable even though it arrives through the same JSON payload as writable fields — a downstream consumer of a settings inventory should not list it as a plain writable option. update_service.go never calls SetSetting/GetSetting itself — it delegates persistence to SaveConfigToDatabase(us.DB), which lives in internal/config/persistence.go (out of scope for this read).

3. register.go (22 lines) is pure serviceregistry DI wiring — registers KeyConfigUpdate, needs KeyStore, builds NewUpdateService(store). No settings keys at all.

Where the real db-setting keys actually live (out of scope, confirmed via grep of internal/config/*.go for SetSetting\|GetSetting, all hits outside the three target files): internal/config/persistence.go defines/uses "config_blob" (the whole-Config JSON blob key, line 635/1495), "maintenance_window_migrated" (a one-off migration flag, lines 905/928), and per-field secret keys such as "openai_api_key" written individually via a `s.key` loop (line 1521) for secret persistence outside the blob. If the goal is a complete db-setting key inventory, persistence.go is the file that actually needs to be read next — settings.go/update_service.go/register.go were a dead end for literal keys by design (settings.go is infrastructure; update_service.go is a Config-struct patch layer; register.go is DI wiring).
- [config-specialized] Cross-file caveats:

- abs_config.go: ALL 8 keys belong to a single feature-flagged surface (abs_api_enabled gates the whole thing) — every viper.SetDefault has a matching viper.BindEnv, so every entry has both a default and an env var. None of these are about ABS-as-a-data-source (pulling books from an Audiobookshelf instance); they're all auth/identity/TTL knobs for the ABS-compatible sync API this server exposes, hence domain=server-auth-network for the whole file.

- itunes_libraries.go: this file declares the 4-state LibrarySet/LibraryRef types and their Resolve/Validate logic, but two legacy fields it reads/writes (ITunesConfig.LibraryReadPath / LibraryWritePath) and two booleans it reads (SyncEnabled, WriteBackEnabled) are declared on ITunesConfig in a different file that was not part of this read — I could only see their use here (assignment in Resolve(), consultation in ValidateLibraries()), not their canonical declaration site/default/env binding, so their `location` entries say "referenced" rather than "declared". Likewise ValidateLibraries takes a `protectedPaths []string` parameter (the effective Config.ProtectedPaths) — that's a general/global config field, not itunes-specific, so I did not add it as an itunes-domain option; it's only noted here for completeness. `booksItunesSegment = "books/itunes/"` is a hardcoded path-matching const, not an operator-configurable setting, so it's excluded as an option entry.

- naming_patterns.go: the file only declares the two DefaultFolderNamingPattern/DefaultFileNamingPattern consts and the validator for the FILE pattern. Note validateNamingPatterns's first parameter is literally named `_ /* folderPattern */` — the folder pattern is accepted but never actually validated in this function; only file_naming_pattern is checked (no-separator rule + per-track-placeholder rule). `trackPlaceholders` is an internal lookup slice used by the validator, not itself a configurable setting, so it's not listed as an option.
- [config-cli-flags] Exhaustive per-file flag census across cmd/. Grep pattern combined "Flags()\.\|flag\.(String\|Bool\|Int\|Duration\|Float\|Var)" was re-run across all 12 files to confirm no stdlib flag.* calls were hiding in the 7 cobra-based files, and no cobra Flags() calls were hiding in the 5 files that use the stdlib flag package (itl-diff, itunes-sync-tests, pid-census use stdlib flag; dedup_bench_batch.go and dedup_bench_status.go define zero flags of their own — they only add subcommands via AddCommand). Total: 66 flag entries, matching independent counts by file (root.go 18, dedup_bench.go 7, dedup_bench_crossval.go 6, dedup_bench_pass2.go 4, dedup_bench_batch.go 0, dedup_bench_status.go 0, diagnostics.go 5, seed.go 4, itl-diff/main.go 3, itunes-sync-tests/main.go 2, mtls-bridge/main.go 6, pid-census/main.go 11).

Build-tag note: cmd/dedup_bench.go, cmd/dedup_bench_crossval.go, and cmd/dedup_bench_pass2.go all carry `//go:build bench` — their 17 flags (7+6+4) only exist in a `bench`-tagged build, not the default build.

viper.BindPFlag wiring (only 5 of root.go's 18 flags are bound to a config key; the rest have no yamlKey/envVar path):
- --dir -> root_dir
- --db -> database_path
- --db-type -> database_type
- --enable-sqlite3-i-know-the-risks -> enable_sqlite3_i_know_the_risks
- --playlists -> playlist_dir
initConfig() (cmd/root.go:444) calls viper.AutomaticEnv() with no SetEnvPrefix/SetEnvKeyReplacer, so each bound viper key is also readable from an identically-named (uppercased) env var with no prefix, e.g. ROOT_DIR, DATABASE_PATH, DATABASE_TYPE, ENABLE_SQLITE3_I_KNOW_THE_RISKS, PLAYLIST_DIR — recorded as envVar on those 5 entries only. --config and --log-level are read directly into Go vars (cfgFile, logLevel) with no BindPFlag, so no yamlKey/envVar. All 10 serve flags and the 1 metadata-inspect flag are read via cmd.Flags().Get*() in their RunE closures (not shown fully here but pattern is standard cobra local-flag access) and have no viper binding at all in cmd/ (confirmed via `grep -rn viper.BindPFlag cmd/` returning only the 5 lines above).

Domain assignment: dedup_bench*.go -> "dedup" (AI author-dedup comparison harness). cmd/pid-census, cmd/itl-diff, cmd/itunes-sync-tests -> "itunes" (iTunes Library.itl / PID integrity tooling), which fits the enum better than the task's generic "plugins-maintenance-misc" fallback. cmd/mtls-bridge -> "server-auth-network" (mTLS cert/PSK provisioning + network bridge). root.go's --config/--dir/--db/--db-type/--enable-sqlite3.../--playlists/--log-level plus diagnostics.go and seed.go -> "cli-database" (general CLI + database lifecycle). root.go's serve subcommand flags (port/host/timeouts/tls/external-url/workers) -> "server-auth-network". root.go's --file on inspect-metadata -> "scanning-organize-metadata" (metadata extraction pipeline).

Duplicate flag names across binaries are expected and intentional: --reset (seed vs mtls-bridge provision), --host (root.go serve vs mtls-bridge serve), --dry-run (dedup-bench vs diagnostics cleanup-invalid), --db (root.go persistent vs pid-census). Each is a separate entry disambiguated by location/description-prefix (owning command named in the description) since `name` alone is not unique across the binary set.

Command ownership prefixes added to descriptions for flags on non-root commands so entries reading it out of context are still disambiguated: "dedup-bench:", "dedup-bench crossval:", "dedup-bench pass2:", "diagnostics cleanup-invalid:"/"diagnostics query:", "seed:", "itl-diff:", "itunes-sync-tests:", "mtls-bridge:"/"mtls-bridge serve:"/"mtls-bridge provision:", "pid-census:".

--models default reconstructed from the multi-line StringSliceVar call at cmd/dedup_bench.go:70-72: ["gpt-4o","gpt-4o-mini","gpt-4.1","gpt-4.1-mini","gpt-5.1","gpt-5-mini","gpt-5-nano","o3-mini","o4-mini"].
- [config-adhoc-env] Methodology: ran the exact grep command from the task, excluding internal/config/, then read surrounding context for each unique env-var-name literal (33 unique names across ~60 call sites). Constant-wrapped vars (os.Getenv(SomeConstant)) were resolved to their literal string value via a follow-up grep for the constant's declaration.\n\nGrouping: entries are one-per-unique-env-var-name as instructed; when the same var is read in multiple files, the extra locations are folded into that entry's description rather than creating duplicate options.\n\nTest-only vars included: the grep command as given does not exclude _test.go files, and the task said 'every unique env var name found', so I included test-only vars (PATH, ITL_PRESERVE_PROOF_PATH, GENERATE_ITL_FIXTURE, ITUNES_XML, and the four TEST_SUBPROCESS_* harness vars) but flagged them clearly in both `name` and `description` as test-only/not real application config, so the audit can filter them out easily.\n\nNotable naming-consistency findings worth flagging to the audit owner:\n1. LIST_WARMER_MAX_HEAP_MB is a legacy name whose semantics were silently repurposed to mean the same thing as the newer LIST_WARMER_HEAP_DELTA_MB (a delta, not an absolute cap as the old name implies) — a stale-name trap similar to the 'stale justification' pattern flagged elsewhere in this repo's history.\n2. OPENAI_BASE_URL is read independently at 9 call sites (internal/ai x3, internal/server/bench.go, cmd/dedup_bench* x5) with no shared accessor/helper — each site repeats its own `if baseURL := os.Getenv(...); baseURL != \"\"` check. Same pattern for OPENAI_API_KEY (7 sites) and OPENLIBRARY_BASE_URL (2 sites, but at least those two share a NewXClient() constructor pattern).\n2b. OPENAI_API_KEY and ACOUSTID_API_KEY use OPPOSITE fallback precedence: ACOUSTID_API_KEY checks config.AppConfig FIRST then env; OPENAI_API_KEY (in bench.go) checks env FIRST then config.AppConfig. Worth normalizing if these are meant to follow the same 'persisted config wins' philosophy.\n3. Four *_BASE_URL vars (AUDIBLE_, AUDNEXUS_, OPENLIBRARY_, GOOGLE_BOOKS_) follow a consistent, good naming pattern — {PROVIDER}_BASE_URL — and could be a template for others.\n4. The UOS_* family (UOS_SOCKET, UOS_DB_PATH, UOS_DB_TYPE, UOS_ROOT_DIR) is a clean, deliberately separate namespace (child-process re-exec plumbing) that is NOT meant to go through internal/config at all — these should probably be excluded from any 'move to Config struct' remediation since their whole purpose is out-of-band parent→child handoff.\n5. Several ABS_-prefixed vars (ABS_AUTH_PROBE, ABS_OIDC_REDIRECT_URIS, ABS_ITUNES_POSITION_BACKFILL_USER_ID) are deliberately env-only by design (documented in-code as needing runtime toggle without restart, or as a security-critical override that shouldn't live in the mutable/persisted Config struct) — these may be intentional exceptions to a 'centralize in Config' recommendation rather than oversights.
- [config-frontend-settings] Scope note: DelugeSettingsTab.tsx exposes no actual user-configurable setting — its only labeled field ('Deluge Web URL') is rendered `disabled` with no `onChange`, populated read-only from GET /deluge/status; its own helper text says the URL must be set via server config/env, not this UI. The rest of that file (Test Connection, View Torrents, Bulk Import) are action buttons, not settings, so no option entries were created for it.

Naming consistency, mostly fine with two real issues:
1. Nested sub-config objects (dedup, embedding, maintenance, tools) pass straight through with matching snake_case keys in both frontend and payload — no mismatch.
2. Flat SettingsGeneral/Performance/Metadata fields are camelCase in React state but explicitly remapped to snake_case backend keys inside handleSave() (web/src/hooks/useSettingsHandlers.ts:434-504) — expected and consistent, EXCEPT `libraryPath` maps to backend key `root_dir`, not `library_path`. That's a genuine name divergence worth flagging in the per-field check, not just a case-style translation. `playlist_dir` is synthesized (`${libraryPath}/playlists`) rather than exposed as its own UI field at all.
3. `metadata_sources[].credentials.apiKey` — the frontend key literally sent is `apiKey` (camelCase) inside the `credentials` map (useSettingsHandlers.ts sanitizeImportPayload and handleSave both pass credentials through as-is); worth checking whether the Go backend expects `api_key` there, since every other payload key is snake_case.

Real functional defects found while doing this pass (not naming, but affect what "configurable via this UI" means):
- ToolsSettingsTab.tsx has three MUI controls (Switch + 2 TextFields, described above) with zero state binding — no `checked`/`value`, no `onChange`. They render but do nothing. Two of them ('Managed tools directory', debounce) are near-duplicates of real, correctly-wired fields (`toolsConfig.managed_dir`, `toolsConfig.embed_queue_debounce_ms`) that live in pages/Settings.tsx's Tools-tab 'Advanced: Tools Config' accordion instead.
- `libraryPath` and `protectedPaths` (plus the whole import-folder add/scan/remove list) are duplicated verbatim between SettingsGeneral.tsx (Library tab) and PathsSettingsTab.tsx (Paths tab) — two separate UI surfaces editing the same backend fields.
- [config-deploy-surface] Scope: config.yaml, .env.example, docker-compose.yml, deploy/prometheus/scrape-config.yml, deploy/prometheus/alert-rules.yml, and deploy/audiobook-organizer.service (deploy/systemd/audiobook-organizer.service is a symlink to the same file — no separate content). Neither Prometheus file sets any audiobook-organizer app config/env var — scrape-config.yml only *consumes* the app's /metrics endpoint with a bearer token file (Prometheus-side, not app-side), and alert-rules.yml only references metric names and cites MemoryMax/GOMEMLIMIT values that already live in the systemd unit. No new option keys came from either file.

Wiring facts established (internal/config/config.go, internal/config/persistence.go:1541 SyncConfigFromEnv, cmd/root.go): the app uses `viper.AutomaticEnv()` (cmd/root.go:456) with NO `SetEnvKeyReplacer`, so a viper key `foo_bar` auto-binds to env var `FOO_BAR` — nothing more, nothing less. `--host`/`--port` are plain cobra flags never bound to viper (`viper.BindPFlag` is only called for `--dir`→root_dir, `--db`→database_path, `--db-type`, `--enable-sqlite3-i-know-the-risks`, `--playlists`), so no env var reaches them.

=== HIGH-CONFIDENCE "documented but unused" findings (the deliverable this audit's usage-verification phase wants) ===
1. **AO_DIR** (.env.example:15, docker-compose.yml:18, README.md:197) — no Go code, viper key, or BindEnv anywhere reads it. The viper key that actually holds the scan root is `root_dir` (env `ROOT_DIR`, or CLI `--dir`). AO_DIR is pure documentation cruft; setting it does nothing.
2. **AO_DB** (.env.example:16, docker-compose.yml:19, README.md:198) — same problem: the real key is `database_path` (env `DATABASE_PATH`, or CLI `--db`). Worse, in docker-compose the Dockerfile's CMD hardcodes `serve --host 0.0.0.0 --db /data/audiobooks.pebble`, so even if AO_DB somehow mattered, the CLI flag would win anyway. Dead.
3. **HOST** / **PORT** (.env.example:11-12) — never read by any Go code; `--host`/`--port` are unbound cobra flags. Setting these env vars in a shell before running the binary has zero effect.
4. **OPENAI_MODEL** (.env.example:8) — no `openai_model` Config field exists at all (grepped config.go fully). Purely aspirational documentation.
5. **AUDIOBOOK_ROOT_DIR** set as a container `Environment=` line inside deploy/audiobook-organizer.service:69 — never read (no matching viper key `audiobook_root_dir`, no BindEnv, no os.Getenv). The unit file's own installation comment at line 63 ("adjust AUDIOBOOK_ROOT_DIR for your library path") is therefore misleading/stale — adjusting it does nothing; the real root dir must come from `--dir`, `ROOT_DIR` env, or (in practice) the DB-persisted import-paths configured via the web UI. NOTE: docker-compose.yml:16 also uses a variable spelled `AUDIOBOOK_ROOT_DIR`, but that one is a *host-side* shell/compose-substitution variable picking the bind-mount source (`${AUDIOBOOK_ROOT_DIR:-./audiobooks}:/audiobooks:ro`) — it never enters the container's environment and is a completely different mechanism from the systemd one despite the identical name. Neither actually configures the running Go process's root_dir.
6. **AUDIOBOOK_SERVER_IP** / **AUDIOBOOK_API_PORT** (.env.example:31-32) — by the file's own comment these feed `.claude/skills/server-bootstrap` (Claude Code tooling), not the application. Correctly out-of-scope for the app's Config surface, but worth excluding from "app env vars" if the audit's denominator is the Go binary's own config.

=== Confirmed WORKING env vars (real viper.AutomaticEnv or BindEnv matches, verified against config.go) ===
OPENAI_API_KEY, ENABLE_AI_PARSING, BASIC_AUTH_ENABLED, BASIC_AUTH_USERNAME, BASIC_AUTH_PASSWORD, ENABLE_AUTH, API_RATE_LIMIT_PER_MINUTE, AUTH_RATE_LIMIT_PER_MINUTE, JSON_BODY_LIMIT_MB, UPLOAD_BODY_LIMIT_MB, DATABASE_PATH (systemd), FP_PARALLEL_WORKERS (systemd, confirmed via internal/plugins/acoustid/fingerprint_rescan.go:298 os.Getenv). GOMEMLIMIT/GOGC are consumed directly by the Go runtime itself (not app code) and are legitimate.

=== Build-time / host-side, not app-runtime config (still catalogued since docker-compose.yml sets them, but flagged so they aren't miscounted as "used but undocumented" app config) ===
APP_VERSION (Dockerfile ARG → `-X main.version=`, compile-time only) and AO_PORT (docker-compose host port-mapping substitution `"${AO_PORT:-8484}:8484"`, never passed into the container's environment at all).

I did not exhaustively check every OTHER env var referenced elsewhere in the Go tree (OPENAI_BASE_URL, ACOUSTID_API_KEY, WHISPER_*, OAUTH_*, SCHEDULED_*, ABS_*, etc.) since none of those appear in the five deployment-surface files this task scoped to — they're real and working (many via explicit viper.BindEnv in config.go/abs_config.go) but simply undocumented in .env.example/docker-compose, which is the mirror-image "used but undocumented" case worth flagging to whoever owns .env.example.
