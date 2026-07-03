<!-- file: docs/reference/config-api-shape.md -->
<!-- version: 1.2.0 -->
<!-- guid: 2b7f9c31-a4e8-4f1d-b8a2-6c5d9e3f2a17 -->
<!-- last-edited: 2026-07-03 -->

# Config API Shape Reference

> **Audience:** AI agents and frontend engineers building the WebUI settings UI.
> This document describes the exact JSON shape returned by `GET /config` and accepted by `PUT /config`.

---

## GET /config

Returns the current application config with all secrets masked. Shape is the full `Config` struct
serialized to JSON. Requires `settings.read` permission.

```
GET /api/v1/config
Authorization: Bearer <token>
```

Response envelope:
```json
{
  "status": "ok",
  "data": {
    "config": { ... }
  }
}
```

**Secret fields are always masked** in GET responses. The masking format is `sk-****` (first 3 chars + `****`). The five masked fields are:
- `openai_api_key`
- `acoustid_api_key`
- `google_books_api_key`
- `hardcover_api_token`
- `basic_auth_password`

---

## PUT /config

Updates one or more config fields. Requires `settings.manage` permission. Accepts a partial payload — only the keys you send are updated; omitted keys are left unchanged.

```
PUT /api/v1/config
Authorization: Bearer <token>
Content-Type: application/json

{ "root_dir": "/mnt/books", "embedding": { "enabled": true } }
```

### Immutable fields (rejected if present)

These two fields cannot be changed at runtime. The request will return `400` if they appear in the payload:
- `database_type`
- `enable_sqlite`

### Secret fields (accepted as flat top-level keys only)

Secrets are never stored in the JSON config blob. Send them as flat top-level keys (not nested):
```json
{
  "openai_api_key": "sk-...",
  "acoustid_api_key": "...",
  "google_books_api_key": "...",
  "hardcover_api_token": "...",
  "basic_auth_password": "..."
}
```

### Backwards-compatible flat key aliases

The PUT endpoint accepts **both** nested keys (preferred) and legacy flat keys (deprecated aliases that still work). The backend remaps flat keys to nested automatically at runtime. Flat aliases are documented per sub-struct below.

---

## Full Config Shape

The `config` object inside the response data has this structure:

```typescript
interface Config {
  // ── Core paths ──────────────────────────────────────────────────────────
  root_dir: string;                      // Root audiobook directory
  database_path: string;                 // PebbleDB path (read-only; set at startup)
  
  // ── Server / auth ────────────────────────────────────────────────────────
  port: number;                          // HTTP listen port (default 8080)
  basic_auth_enabled: boolean;
  basic_auth_username: string;
  basic_auth_password: string;           // MASKED in GET responses
  api_token_enabled: boolean;
  
  // ── External API keys (MASKED in GET responses) ──────────────────────────
  openai_api_key: string;
  acoustid_api_key: string;
  google_books_api_key: string;
  hardcover_api_token: string;

  // ── AI model selection ───────────────────────────────────────────────────
  metadata_review_model: string;         // OpenAI model for metadata review (default "gpt-5-mini")
  dedup_review_model: string;            // OpenAI model for dedup review (default "gpt-5-mini")

  // ── Sub-structs (nested after CFG-1 refactor) ────────────────────────────
  embedding:       EmbeddingConfig;
  dedup:           DedupConfig;
  metadata_scoring: MetadataScoringConfig;
  ai_backend:      AIBackendConfig;
  itunes:          ITunesConfig;
  maintenance:     MaintenanceConfig;
  scheduled:       ScheduledTasksConfig;
  auto_update:     AutoUpdateConfig;
  tools:           ToolsConfig;

  // ── Metadata sources ─────────────────────────────────────────────────────
  metadata_sources: MetadataSource[];
  metadata_review_default_view: string;
  metadata_fetch_cache_ttl_days: number;

  // ── Misc UI / library settings ───────────────────────────────────────────
  setup_complete: boolean;
  library_scan_on_startup: boolean;
  auto_write_tags_on_apply: boolean;
  write_back_metadata: boolean;
  organize_on_apply: boolean;
  // ... (additional top-level scalar fields not covered by sub-structs)
}
```

---

## Sub-Struct Shapes

### EmbeddingConfig — `config.embedding`

Controls the embedding backend used for vector similarity (dedup Layer-2, entity matching).

```typescript
interface EmbeddingConfig {
  enabled: boolean;        // Enable embedding generation (default: false)
  model: string;           // Model name (default: "text-embedding-3-large")
  dimensions: number;      // Vector dimensions (default: 3072; use 1024 for bge-m3)
  base_url: string;        // OpenAI-compatible base URL ("" = use OpenAI; set to http://localhost:11434/v1 for Ollama)
  vector_backend: string;  // "hnsw" (default) or "chromem"
}
```

**Flat aliases accepted by PUT /config:**
| Legacy flat key | Maps to |
|---|---|
| `embedding_enabled` | `embedding.enabled` |
| `embedding_model` | `embedding.model` |
| `embedding_dimensions` | `embedding.dimensions` |
| `embedding_base_url` | `embedding.base_url` |
| `vector_index_backend` | `embedding.vector_backend` |

**Environment variables:**
| Env var | Config field |
|---|---|
| `EMBEDDING_ENABLED` | `embedding.enabled` |
| `EMBEDDING_MODEL` | `embedding.model` |
| `EMBEDDING_DIMENSIONS` | `embedding.dimensions` |
| `EMBEDDING_BASE_URL` | `embedding.base_url` |
| `VECTOR_INDEX_BACKEND` | `embedding.vector_backend` |

---

### AIBackendConfig — `config.ai_backend`

Backend-mode toggle for the AI cluster. Selects, independently for embeddings and
LLM/chat, whether the corresponding client runs against OpenAI, a local
OpenAI-compatible backend (e.g. Ollama), or is disabled.

```typescript
interface AIBackendConfig {
  embedding_mode: string;         // "" | "disabled" | "openai" | "local" | "openai-fallback-local"
  llm_mode: string;               // same enum as embedding_mode
  local_base_url: string;         // local OpenAI-compatible endpoint (default placeholder: "http://192.168.0.20:11434/v1")
  local_embedding_model: string;  // model name for local embeddings (default: "bge-m3")
  local_llm_model: string;        // model name for local LLM (default: "qwen2.5:7b-instruct")
}
```

**Mode enum values:**
| Value | Meaning |
|---|---|
| `""` (empty) | Mode is derived at load time from the legacy fields (see below). |
| `disabled` | No client is constructed for that pipeline. |
| `openai` | Real OpenAI cloud API (`openai_api_key` required). |
| `local` | Local endpoint at `local_base_url`; the API key is ignored by the backend. |
| `openai-fallback-local` | Primary OpenAI with a local fallback. At construction time it behaves like `openai`; the fallback *trigger* is wired in the retry/error-classification layer. |

**Empty-mode derivation (legacy-field write-through).**
The legacy flat fields `embedding.base_url`, `openai_api_key`, `enable_ai_parsing`,
and `metadata_scoring.llm_enabled` remain readable on `GET` and are still accepted
on `PUT` for one release. When a mode is empty, the effective mode is derived
from them:

- **embedding_mode**: `embedding.enabled == false` → `disabled`; else
  `embedding.base_url != ""` → `local`; else `openai_api_key != ""` → `openai`;
  else `disabled`.
- **llm_mode**: `openai_api_key != "" && (enable_ai_parsing || metadata_scoring.llm_enabled)`
  → `openai`; else `disabled`.

On first load after upgrade, a one-time blob migration (`migrateAIBackendBlob`)
writes the derived `ai_backend` object into the stored config. Setting a legacy
field on `PUT` therefore updates the derived mode via the same rule on the next
load (write-through). Note that `local_base_url` uses a placeholder host in
committed defaults — real endpoints belong in local, gitignored config.

**Environment variables:**
| Env var | Config field |
|---|---|
| `AI_BACKEND_EMBEDDING_MODE` | `ai_backend.embedding_mode` |
| `AI_BACKEND_LLM_MODE` | `ai_backend.llm_mode` |
| `AI_BACKEND_LOCAL_BASE_URL` | `ai_backend.local_base_url` |
| `AI_BACKEND_LOCAL_EMBEDDING_MODEL` | `ai_backend.local_embedding_model` |
| `AI_BACKEND_LOCAL_LLM_MODEL` | `ai_backend.local_llm_model` |

**Status probe and model-pull endpoints (TASK-11).** These are separate from
`GET`/`PUT /config` — they probe the live local backend and, on demand, pull a
model into it via the managed Ollama lifecycle (the same `ToolRegistry`/
`OllamaDaemon` machinery behind `/api/v1/tools/:name/install`).

```
GET /api/v1/ai/backends/status
```

Response `data`:
```typescript
interface AIBackendsStatus {
  embedding_mode: string;          // effective mode, see EffectiveEmbeddingMode
  llm_mode: string;                // effective mode, see EffectiveLLMMode
  local_base_url: string;
  local_reachable: boolean;        // GET {local_base_url}/api/tags succeeded
  embedding_model?: { name: string; pulled: boolean };
  llm_model?: { name: string; pulled: boolean };
  fallback_reason?: string;        // set when local_reachable is false
}
```

The probe is skipped (all fields default) when neither `embedding_mode` nor
`llm_mode` resolves to `local`/`openai-fallback-local`, or when
`local_base_url` is empty.

```
POST /api/v1/ai/backends/pull-model
Content-Type: application/json

{ "model": "bge-m3" }
```

Resolves the managed `ollama` binary via `ToolRegistry`, ensures the managed
`OllamaDaemon` is running, then runs `ollama pull <model>` synchronously
(bounded by a server-side timeout) and stops the daemon back down when idle.
There is no streaming/op-registry progress channel for this endpoint — the
frontend re-polls `GET /ai/backends/status` after this call returns to
confirm the model is now pulled. Response `data`: `{ "model": string,
"pulled": true }`. Returns `503` if the tool registry or a resolvable
`ollama` binary is unavailable, and `500` if the pull itself fails (with the
`ollama` CLI's combined output in the error message).

Both endpoints require `settings.manage` permission, matching the tools
lifecycle endpoints.

---

### DedupConfig — `config.dedup`

Controls the deduplication engine thresholds and behaviour.

```typescript
interface DedupSignalConfig {
  band_certain_min: number;      // Default: 0.97 — score ≥ this → "certain" duplicate
  band_high_min: number;         // Default: 0.92
  band_medium_min: number;       // Default: 0.82
  band_review_min: number;       // Default: 0.70 — score ≥ this → show for review
  duration_boost: number;        // Default: 0.05 — bonus when durations match within 1s
  folder_path_boost: number;     // Default: 0.03 — bonus when folder paths share prefix
}

interface DedupConfig {
  book_high_threshold: number;        // Default: 0.92 — book-level merge threshold
  book_low_threshold: number;         // Default: 0.70
  author_high_threshold: number;      // Default: 0.92
  author_low_threshold: number;       // Default: 0.70
  auto_merge_enabled: boolean;        // Auto-merge certain duplicates (default: false)
  embeddings_enabled: boolean;        // Enable Layer-2 embedding comparison (default: false)
  llm_auto_merge_high_confidence: boolean;  // LLM auto-merge on high confidence (default: false)
  on_import_via_scheduler: boolean;   // Run dedup on each imported book (default: false)
  review_model: string;               // OpenAI model for LLM dedup review (default: "gpt-5-mini")
  signals: DedupSignalConfig;
}
```

**Flat aliases accepted by PUT /config:**
| Legacy flat key | Maps to |
|---|---|
| `dedup_book_high_threshold` | `dedup.book_high_threshold` |
| `dedup_book_low_threshold` | `dedup.book_low_threshold` |
| `dedup_author_high_threshold` | `dedup.author_high_threshold` |
| `dedup_author_low_threshold` | `dedup.author_low_threshold` |
| `dedup_auto_merge_enabled` | `dedup.auto_merge_enabled` |
| `dedup_embeddings_enabled` | `dedup.embeddings_enabled` |
| `dedup_llm_auto_merge_high_confidence` | `dedup.llm_auto_merge_high_confidence` |
| `dedup_on_import_via_scheduler` | `dedup.on_import_via_scheduler` |
| `dedup_review_model` | `dedup.review_model` |
| `dedup_band_certain_min` | `dedup.signals.band_certain_min` |
| `dedup_band_high_min` | `dedup.signals.band_high_min` |
| `dedup_band_medium_min` | `dedup.signals.band_medium_min` |
| `dedup_band_review_min` | `dedup.signals.band_review_min` |

---

### MetadataScoringConfig — `config.metadata_scoring`

Controls how embedding similarity and LLM reranking are used during metadata search.

```typescript
interface MetadataScoringConfig {
  embedding_enabled: boolean;       // Use embedding similarity in metadata scoring (default: false)
  embedding_min_score: number;      // Minimum embedding score to include a candidate (default: 0.82)
  embedding_best_match: number;     // Score ≥ this → treat as "best match" (default: 0.88)
  llm_enabled: boolean;             // Use LLM to rerank top-K candidates (default: false)
  llm_rerank_epsilon: number;       // Tie-break tolerance for LLM reranking (default: 0.05)
  llm_rerank_top_k: number;         // Send top-K candidates to LLM (default: 5)
  write_backup_before: boolean;     // Write a tag backup before applying metadata (default: true)
}
```

**Flat aliases accepted by PUT /config:**
| Legacy flat key | Maps to |
|---|---|
| `metadata_embedding_scoring_enabled` | `metadata_scoring.embedding_enabled` |
| `metadata_embedding_min_score` | `metadata_scoring.embedding_min_score` |
| `metadata_embedding_best_match_score` | `metadata_scoring.embedding_best_match` |
| `metadata_llm_rerank_enabled` | `metadata_scoring.llm_enabled` |
| `metadata_llm_rerank_epsilon` | `metadata_scoring.llm_rerank_epsilon` |
| `metadata_llm_rerank_top_k` | `metadata_scoring.llm_rerank_top_k` |
| `metadata_write_backup_before_apply` | `metadata_scoring.write_backup_before` |

---

### ITunesConfig — `config.itunes`

Controls iTunes XML library sync and write-back behaviour.

```typescript
interface ITunesPathMap {
  from: string;  // iTunes path prefix (e.g. "file://localhost/W:/itunes/iTunes%20Media")
  to: string;    // Local path prefix  (e.g. "file://localhost/mnt/bigdata/books/itunes/iTunes Media")
}

interface ITunesConfig {
  sync_enabled: boolean;          // Enable iTunes XML sync (default: false)
  sync_interval: number;          // Sync interval in minutes (default: 60)
  write_back_enabled: boolean;    // Allow write-back to iTunes library file (default: false)
  library_write_path: string;     // Full path to the iTunes .itl file for writes
  library_read_path: string;      // Full path to the iTunes XML for reads
  auto_write_back: boolean;       // Automatically write back after metadata apply (default: false)
  path_trim_enabled: boolean;     // Trim Windows path prefixes from iTunes URLs (default: true)
  windows_root_path: string;      // Windows root path to strip (e.g. "W:\\")
  media_root: string;             // Local media root to use instead of Windows path
  path_mappings: ITunesPathMap[]; // Bidirectional URL prefix mapping table
}
```

**Flat aliases accepted by PUT /config:**
| Legacy flat key | Maps to |
|---|---|
| `itunes_sync_enabled` | `itunes.sync_enabled` |
| `itunes_sync_interval` | `itunes.sync_interval` |
| `itl_write_back_enabled` | `itunes.write_back_enabled` |
| `itunes_library_write_path` | `itunes.library_write_path` |
| `itunes_library_read_path` | `itunes.library_read_path` |
| `itunes_auto_write_back` | `itunes.auto_write_back` |
| `itunes_path_trim_enabled` | `itunes.path_trim_enabled` |
| `itunes_windows_root_path` | `itunes.windows_root_path` |
| `itunes_media_root` | `itunes.media_root` |

---

### MaintenanceConfig — `config.maintenance`

Controls the nightly maintenance window that runs background cleanup tasks.

```typescript
interface MaintenanceConfig {
  enabled: boolean;               // Enable nightly maintenance window (default: true)
  window_start: number;           // Start hour 0–23 (default: 2)
  window_end: number;             // End hour 0–23 (default: 5)
  dedup_refresh: boolean;         // Re-run dedup during maintenance (default: true)
  series_prune: boolean;          // Prune orphaned series (default: true)
  author_split: boolean;          // Run author-split op (default: true)
  tombstone_cleanup: boolean;     // Clean up tombstone records (default: true)
  reconcile: boolean;             // Reconcile file paths (default: false)
  purge_deleted: boolean;         // Purge soft-deleted books older than 30d (default: false)
  purge_old_logs: boolean;        // Purge activity logs older than 90d (default: true)
  db_optimize: boolean;           // Compact PebbleDB (default: true)
  library_scan: boolean;          // Re-scan library root (default: false)
  library_organize: boolean;      // Organize files after scan (default: false)
  metadata_refresh: boolean;      // Refresh metadata for books with no ISBN (default: false)
  library_size_refresh: boolean;  // Recompute library size aggregates (default: true)
  acoustid_online_lookup: boolean;// AcoustID online lookup during maintenance (default: false)
  acoustid_nightly_limit: number; // Max AcoustID lookups per night (default: 200)
}
```

**Flat aliases accepted by PUT /config:**
| Legacy flat key | Maps to |
|---|---|
| `maintenance_window_enabled` | `maintenance.enabled` |
| `maintenance_window_start` | `maintenance.window_start` |
| `maintenance_window_end` | `maintenance.window_end` |
| `maintenance_dedup_refresh` | `maintenance.dedup_refresh` |
| `maintenance_series_prune` | `maintenance.series_prune` |
| `maintenance_author_split` | `maintenance.author_split` |
| `maintenance_tombstone_cleanup` | `maintenance.tombstone_cleanup` |
| `maintenance_reconcile` | `maintenance.reconcile` |
| `maintenance_purge_deleted` | `maintenance.purge_deleted` |
| `maintenance_purge_old_logs` | `maintenance.purge_old_logs` |
| `maintenance_db_optimize` | `maintenance.db_optimize` |
| `maintenance_library_scan` | `maintenance.library_scan` |
| `maintenance_library_organize` | `maintenance.library_organize` |
| `maintenance_metadata_refresh` | `maintenance.metadata_refresh` |
| `maintenance_library_size_refresh` | `maintenance.library_size_refresh` |
| `maintenance_acoustid_online_lookup` | `maintenance.acoustid_online_lookup` |
| `maintenance_acoustid_nightly_limit` | `maintenance.acoustid_nightly_limit` |

---

### ScheduledTasksConfig — `config.scheduled`

Each task group has three fields. The `resolve_production_authors` group does not have `on_startup`.

```typescript
interface ScheduledTaskConfig {
  enabled: boolean;      // Enable this scheduled task
  interval: number;      // Run interval in minutes
  on_startup: boolean;   // Also run once on server startup
}

interface ScheduledTasksConfig {
  dedup_refresh:              ScheduledTaskConfig;
  author_split:               ScheduledTaskConfig;
  db_optimize:                ScheduledTaskConfig;
  metadata_refresh:           ScheduledTaskConfig;
  resolve_production_authors: { enabled: boolean; interval: number };  // no on_startup
  series_prune:               ScheduledTaskConfig;
  ai_dedup_batch:             ScheduledTaskConfig;
  reconcile:                  ScheduledTaskConfig;
}
```

**Flat aliases accepted by PUT /config** (example group; same pattern for all 8):
| Legacy flat key | Maps to |
|---|---|
| `scheduled_dedup_refresh_enabled` | `scheduled.dedup_refresh.enabled` |
| `scheduled_dedup_refresh_interval` | `scheduled.dedup_refresh.interval` |
| `scheduled_dedup_refresh_on_startup` | `scheduled.dedup_refresh.on_startup` |
| `scheduled_author_split_enabled` | `scheduled.author_split.enabled` |
| `scheduled_author_split_interval` | `scheduled.author_split.interval` |
| `scheduled_author_split_on_startup` | `scheduled.author_split.on_startup` |
| `scheduled_db_optimize_enabled` | `scheduled.db_optimize.enabled` |
| `scheduled_db_optimize_interval` | `scheduled.db_optimize.interval` |
| `scheduled_db_optimize_on_startup` | `scheduled.db_optimize.on_startup` |
| `scheduled_metadata_refresh_enabled` | `scheduled.metadata_refresh.enabled` |
| `scheduled_metadata_refresh_interval` | `scheduled.metadata_refresh.interval` |
| `scheduled_metadata_refresh_on_startup` | `scheduled.metadata_refresh.on_startup` |
| `scheduled_resolve_production_authors_enabled` | `scheduled.resolve_production_authors.enabled` |
| `scheduled_resolve_production_authors_interval` | `scheduled.resolve_production_authors.interval` |
| `scheduled_series_prune_enabled` | `scheduled.series_prune.enabled` |
| `scheduled_series_prune_interval` | `scheduled.series_prune.interval` |
| `scheduled_series_prune_on_startup` | `scheduled.series_prune.on_startup` |
| `scheduled_ai_dedup_batch_enabled` | `scheduled.ai_dedup_batch.enabled` |
| `scheduled_ai_dedup_batch_interval` | `scheduled.ai_dedup_batch.interval` |
| `scheduled_ai_dedup_batch_on_startup` | `scheduled.ai_dedup_batch.on_startup` |
| `scheduled_reconcile_enabled` | `scheduled.reconcile.enabled` |
| `scheduled_reconcile_interval` | `scheduled.reconcile.interval` |
| `scheduled_reconcile_on_startup` | `scheduled.reconcile.on_startup` |

---

### AutoUpdateConfig — `config.auto_update`

Controls automatic binary update checking and installation.

```typescript
interface AutoUpdateConfig {
  enabled: boolean;       // Enable auto-update (default: false)
  channel: string;        // Release channel: "stable" | "beta" (default: "stable")
  check_minutes: number;  // Check interval in minutes (default: 360 = 6h)
  window_start: number;   // Earliest hour to install an update (default: 2)
  window_end: number;     // Latest hour to install an update (default: 5)
}
```

**Flat aliases accepted by PUT /config:**
| Legacy flat key | Maps to |
|---|---|
| `auto_update_enabled` | `auto_update.enabled` |
| `auto_update_channel` | `auto_update.channel` |
| `auto_update_check_minutes` | `auto_update.check_minutes` |
| `auto_update_window_start` | `auto_update.window_start` |
| `auto_update_window_end` | `auto_update.window_end` |

---

### ToolsConfig — `config.tools`

Controls managed external binary lifecycle (Ollama, fpcalc). Nested as `Config.Tools`.

```typescript
interface ToolsConfig {
  managed_dir: string;         // Download directory for managed binaries (default: /var/lib/audiobook-organizer/tools)
  embed_queue_debounce_ms: number;  // Milliseconds to wait before draining embed queue (default: 500)
}
```

---

## Example: Updating embedding settings

```bash
# Use nested keys (preferred):
curl -X PUT https://<host>/api/v1/config \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "embedding": {
      "enabled": true,
      "model": "bge-m3",
      "dimensions": 1024,
      "base_url": "http://localhost:11434/v1",
      "vector_backend": "hnsw"
    }
  }'

# Legacy flat keys also work (backwards compat):
curl -X PUT https://<host>/api/v1/config \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "embedding_enabled": true,
    "embedding_model": "bge-m3"
  }'
```

## Example: Setting API keys

```bash
curl -X PUT https://<host>/api/v1/config \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "openai_api_key": "sk-...",
    "acoustid_api_key": "..."
  }'
```

---

## Notes for WebUI agents

1. **Always read the full config on mount** via `GET /config` and populate form fields from the response. Never hardcode defaults — they may change.
2. **Patch-style updates** — only send changed fields in PUT. Sending the full config object is safe but wastes bandwidth and may trigger spurious validation.
3. **Secret fields**: display a masked placeholder (e.g. `sk-****`) when a value exists; only send the actual key if the user has changed it. Use an empty string to clear a key.
4. **Nested vs. flat keys**: prefer nested keys in new UI code (e.g. `embedding.enabled`). Legacy flat aliases will remain supported indefinitely but are deprecated.
5. **The `tools` sub-struct** (`Config.Tools`) is read/write via PUT /config but its fields are rarely user-facing — expose them only in the Advanced section of the Tools tab.
6. **`database_path` and `database_type`** are display-only in the UI; include them in the config display but never allow the user to edit them.
