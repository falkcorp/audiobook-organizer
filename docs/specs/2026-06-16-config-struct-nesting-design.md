<!-- file: docs/superpowers/specs/2026-06-16-config-struct-nesting-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: a3f8b2c1-7e4d-4a9f-b6c3-8d2e1f5a0c7b -->
<!-- last-edited: 2026-06-16 -->

# Config Struct Nesting — Design Spec

**Status:** Approved — ready for implementation planning
**Scope:** Go backend only. UI/TypeScript changes are a follow-up after all waves land.
**Parent task:** CFG-1 (config architecture decision) from TODO.md
**Research basis:** `docs/research/2026-06-15-config-architecture-evaluation.md`

---

## Problem

`AppConfig` (`internal/config/config.go`) has grown to 155+ flat fields with no structural grouping. Symptoms:

- Embedding/dedup/scoring fields were added without proper viper wiring (fixed in PR #1464), which reveals the flat struct makes it easy to skip the standard pattern
- `applySetting()` switch is ~300 lines and growing
- The dedup team already escaped to a parallel Viper-only config system (`dedup.signals.*`) rather than extend the flat struct
- `ResetToDefaults` is 240+ lines of flat field assignments
- 40% of fields have no frontend representation — the TypeScript interface is hand-curated and drifting

**Goal:** Introduce logical nested sub-structs one wave at a time, so each group of related fields is self-contained, testable, and navigable.

---

## Approach: Wave-by-Wave (Option A)

Each wave is one PR that:
1. Defines a new sub-struct
2. Migrates the blob on startup (detect old flat keys → rewrite to nested format, once)
3. Adds an API compatibility shim so existing flat-key clients keep working during the transition
4. Updates `applySetting`, `ResetToDefaults`, `InitConfig`, and all callsites

This approach lets each wave be independently reviewed, deployed, and rolled back.

---

## Wave Schedule

| Wave | Sub-struct | Fields moved | Notes |
|------|-----------|-------------|-------|
| 1 | `EmbeddingConfig` | 5 | Tightest subsystem; viper just fixed in #1464 |
| 2 | `DedupConfig` | 9 + absorbs `unified.ScoreConfig` | Ends parallel Viper-only scoring system |
| 3 | `MetadataScoringConfig` | 7 | Same consumers as dedup |
| 4 | `ITunesConfig` | 10 | Aligns with existing `itunesservice.Config` slice |
| 5 | `MaintenanceConfig` | 17 | Scheduler-only consumers |
| 6 | `ScheduledTasksConfig` | ~18 | Same consumers as maintenance |
| 7 | `AutoUpdateConfig` | 5 | Small, self-contained |
| 8 | `ToolConfig` | 0→4 (new) | Reserved for TOOL-1..6 work; skip until then |

---

## Per-Wave Implementation Pattern

Every wave follows these 6 steps in order. Steps are the same for every wave — only the struct definitions and field names change.

### Step 1 — Define the sub-struct

In `config.go`, add a new named type above the `Config` struct:

```go
type EmbeddingConfig struct {
    Enabled       bool    `json:"enabled"        mapstructure:"enabled"`
    Model         string  `json:"model"          mapstructure:"model"`
    Dimensions    int     `json:"dimensions"     mapstructure:"dimensions"`
    BaseURL       string  `json:"base_url"       mapstructure:"base_url"`
    VectorBackend string  `json:"vector_backend" mapstructure:"vector_backend"`
}
```

Rules:
- All fields get both `json` and `mapstructure` tags (fixing the missing-mapstructure-tags gap identified in research)
- Field names inside the sub-struct are the **short** names (drop the prefix — `EmbeddingEnabled` → `Enabled` inside `EmbeddingConfig`)
- The sub-struct's field on `Config` uses the group name as the json key: `Embedding EmbeddingConfig \`json:"embedding" mapstructure:"embedding"\``

### Step 2 — Replace flat fields in Config + update InitConfig

Remove the flat fields from `Config`. Add the sub-struct field.

Update `viper.SetDefault` calls to use the nested viper key path:
```go
// Before:
viper.SetDefault("embedding_enabled", true)
// After:
viper.SetDefault("embedding.enabled", true)
```

**Env var compatibility is preserved automatically.** Viper's `AutomaticEnv()` maps env vars to keys by uppercasing and replacing `.` with `_`. So `embedding.enabled` → `EMBEDDING_ENABLED` — the same env var name as before. Users with existing env var setups do not need to change anything.

Update the `InitConfig` Mutate struct literal:
```go
// Before:
EmbeddingEnabled: viper.GetBool("embedding_enabled"),
// After (inside Embedding sub-struct):
Embedding: EmbeddingConfig{
    Enabled:       viper.GetBool("embedding.enabled"),
    Model:         viper.GetString("embedding.model"),
    Dimensions:    viper.GetInt("embedding.dimensions"),
    BaseURL:       viper.GetString("embedding.base_url"),
    VectorBackend: viper.GetString("embedding.vector_backend"),
},
```

### Step 3 — Startup blob migration in LoadConfigFromDatabase

After the blob is read but before `Mutate`, insert a one-time migration check:

```go
func migrateEmbeddingFields(store database.SettingsStore, blob string) (string, bool) {
    // Check if blob is old flat format
    var raw map[string]any
    if err := json.Unmarshal([]byte(blob), &raw); err != nil {
        return blob, false
    }
    if _, isFlat := raw["embedding_enabled"]; !isFlat {
        return blob, false // already nested or field absent
    }

    // Shim struct matching old flat shape
    type flatEmbedding struct {
        EmbeddingEnabled    bool    `json:"embedding_enabled"`
        EmbeddingModel      string  `json:"embedding_model"`
        EmbeddingDimensions int     `json:"embedding_dimensions"`
        EmbeddingBaseURL    string  `json:"embedding_base_url"`
        VectorIndexBackend  string  `json:"vector_index_backend"`
    }
    var old flatEmbedding
    json.Unmarshal([]byte(blob), &old)

    // Write nested keys into raw map
    raw["embedding"] = map[string]any{
        "enabled":        old.EmbeddingEnabled,
        "model":          old.EmbeddingModel,
        "dimensions":     old.EmbeddingDimensions,
        "base_url":       old.EmbeddingBaseURL,
        "vector_backend": old.VectorIndexBackend,
    }
    delete(raw, "embedding_enabled")
    delete(raw, "embedding_model")
    delete(raw, "embedding_dimensions")
    delete(raw, "embedding_base_url")
    delete(raw, "vector_index_backend")

    migrated, _ := json.Marshal(raw)
    return string(migrated), true
}
```

Call this before `json.Unmarshal` into `Config`. If it returns `true` (migrated), write the updated blob back to DB immediately and log `"config blob migrated: embedding fields nested"`.

**No migration flag needed** — the sentinel key check (`"embedding_enabled"` at the top level) is idempotent. If the blob was already migrated, the check returns false and the function is a no-op.

### Step 4 — API compatibility shim in UpdateConfig

In `update_service.go`, before `json.Unmarshal(payloadJSON, c)`, remap old flat keys:

```go
func remapEmbeddingKeys(payload map[string]any) map[string]any {
    emb := make(map[string]any)
    for old, newKey := range map[string]string{
        "embedding_enabled":    "enabled",
        "embedding_model":      "model",
        "embedding_dimensions": "dimensions",
        "embedding_base_url":   "base_url",
        "vector_index_backend": "vector_backend",
    } {
        if v, ok := payload[old]; ok {
            emb[newKey] = v
            delete(payload, old)
        }
    }
    if len(emb) > 0 {
        // Merge into existing nested map to avoid zeroing sibling fields
        if existing, ok := payload["embedding"].(map[string]any); ok {
            for k, v := range emb {
                existing[k] = v
            }
        } else {
            payload["embedding"] = emb
        }
    }
    return payload
}
```

The merge-into-existing logic handles the partial-update hazard: if a client sends both `"embedding_enabled": true` AND `"embedding": {"model": "bge-m3"}`, both are preserved.

This shim lives until the frontend is updated. It is then deleted.

### Step 5 — Update applySetting

Change each moved case to write to the nested path:

```go
// Before:
case "embedding_enabled":
    if b, err := strconv.ParseBool(value); err == nil {
        c.EmbeddingEnabled = b
    }
// After:
case "embedding_enabled":
    if b, err := strconv.ParseBool(value); err == nil {
        c.Embedding.Enabled = b
    }
```

The string key ("embedding_enabled") stays the same — only the Go assignment path changes. This preserves backward compatibility for any pre-blob installs restoring from individual DB rows.

### Step 6 — Update ResetToDefaults

Replace flat field assignments with a full sub-struct literal:

```go
// Before:
EmbeddingEnabled:    true,
EmbeddingModel:      "text-embedding-3-large",
EmbeddingDimensions: 3072,
EmbeddingBaseURL:    "",
VectorIndexBackend:  "chromem",

// After:
Embedding: EmbeddingConfig{
    Enabled:       true,
    Model:         "text-embedding-3-large",
    Dimensions:    3072,
    BaseURL:       "",
    VectorBackend: "chromem",
},
```

Then update all callsites: `config.AppConfig.EmbeddingEnabled` → `config.AppConfig.Embedding.Enabled` etc. (mechanical find-replace + compile to find all missed sites).

---

## Wave 1 Field Definitions: EmbeddingConfig

```go
type EmbeddingConfig struct {
    Enabled       bool   `json:"enabled"        mapstructure:"enabled"`
    Model         string `json:"model"          mapstructure:"model"`
    Dimensions    int    `json:"dimensions"     mapstructure:"dimensions"`
    BaseURL       string `json:"base_url"       mapstructure:"base_url"`
    VectorBackend string `json:"vector_backend" mapstructure:"vector_backend"`
}
// On Config:
Embedding EmbeddingConfig `json:"embedding" mapstructure:"embedding"`
```

Viper key mapping (old → new):

| Old flat key | New nested viper key |
|---|---|
| `embedding_enabled` | `embedding.enabled` |
| `embedding_model` | `embedding.model` |
| `embedding_dimensions` | `embedding.dimensions` |
| `embedding_base_url` | `embedding.base_url` |
| `vector_index_backend` | `embedding.vector_backend` |

---

## Wave 2 Field Definitions: DedupConfig

Absorbs 9 flat fields AND the `unified.ScoreConfig` Viper-only system (which currently lives outside `AppConfig` entirely).

```go
type DedupSignalConfig struct {
    BandCertainMin  float64 `json:"band_certain_min"  mapstructure:"band_certain_min"`
    BandHighMin     float64 `json:"band_high_min"     mapstructure:"band_high_min"`
    BandMediumMin   float64 `json:"band_medium_min"   mapstructure:"band_medium_min"`
    BandReviewMin   float64 `json:"band_review_min"   mapstructure:"band_review_min"`
    DurationBoost   float64 `json:"duration_boost"    mapstructure:"duration_boost"`
    FolderPathBoost float64 `json:"folder_path_boost" mapstructure:"folder_path_boost"`
}

type DedupConfig struct {
    BookHighThreshold          float64          `json:"book_high_threshold"           mapstructure:"book_high_threshold"`
    BookLowThreshold           float64          `json:"book_low_threshold"            mapstructure:"book_low_threshold"`
    AuthorHighThreshold        float64          `json:"author_high_threshold"         mapstructure:"author_high_threshold"`
    AuthorLowThreshold         float64          `json:"author_low_threshold"          mapstructure:"author_low_threshold"`
    AutoMergeEnabled           bool             `json:"auto_merge_enabled"            mapstructure:"auto_merge_enabled"`
    EmbeddingsEnabled          bool             `json:"embeddings_enabled"            mapstructure:"embeddings_enabled"`
    LLMAutoMergeHighConfidence bool             `json:"llm_auto_merge_high_confidence" mapstructure:"llm_auto_merge_high_confidence"`
    OnImportViaScheduler       bool             `json:"on_import_via_scheduler"       mapstructure:"on_import_via_scheduler"`
    ReviewModel                string           `json:"review_model"                  mapstructure:"review_model"`
    Signals                    DedupSignalConfig `json:"signals"                      mapstructure:"signals"`
}
// On Config:
Dedup DedupConfig `json:"dedup" mapstructure:"dedup"`
```

**Special:** Wave 2 also updates `internal/dedup/unified/config.go` (`LoadScoreConfig`) to read from `AppConfig.Dedup.Signals` instead of `viper.Get("dedup.signals.*")` directly. This ends the parallel config system.

---

## Wave 3 Field Definitions: MetadataScoringConfig

```go
type MetadataScoringConfig struct {
    EmbeddingEnabled   bool    `json:"embedding_enabled"    mapstructure:"embedding_enabled"`
    EmbeddingMinScore  float64 `json:"embedding_min_score"  mapstructure:"embedding_min_score"`
    EmbeddingBestMatch float64 `json:"embedding_best_match" mapstructure:"embedding_best_match"`
    LLMEnabled         bool    `json:"llm_enabled"          mapstructure:"llm_enabled"`
    LLMRerankEpsilon   float64 `json:"llm_rerank_epsilon"   mapstructure:"llm_rerank_epsilon"`
    LLMRerankTopK      int     `json:"llm_rerank_top_k"     mapstructure:"llm_rerank_top_k"`
    WriteBackupBefore  bool    `json:"write_backup_before"  mapstructure:"write_backup_before"`
}
// On Config:
MetadataScoring MetadataScoringConfig `json:"metadata_scoring" mapstructure:"metadata_scoring"`
```

---

## Wave 4 Field Definitions: ITunesConfig

Aligns with existing `itunesservice.Config` slice pattern. After this wave, the iTunes service construction becomes a direct struct copy.

```go
type ITunesPathMap struct {
    // existing type, unchanged
}

type ITunesConfig struct {
    SyncEnabled      bool            `json:"sync_enabled"       mapstructure:"sync_enabled"`
    SyncInterval     int             `json:"sync_interval"      mapstructure:"sync_interval"`
    WriteBackEnabled bool            `json:"write_back_enabled" mapstructure:"write_back_enabled"`
    LibraryWritePath string          `json:"library_write_path" mapstructure:"library_write_path"`
    LibraryReadPath  string          `json:"library_read_path"  mapstructure:"library_read_path"`
    AutoWriteBack    bool            `json:"auto_write_back"    mapstructure:"auto_write_back"`
    PathTrimEnabled  bool            `json:"path_trim_enabled"  mapstructure:"path_trim_enabled"`
    WindowsRootPath  string          `json:"windows_root_path"  mapstructure:"windows_root_path"`
    MediaRoot        string          `json:"media_root"         mapstructure:"media_root"`
    PathMappings     []ITunesPathMap `json:"path_mappings"      mapstructure:"path_mappings"`
}
// On Config:
ITunes ITunesConfig `json:"itunes" mapstructure:"itunes"`
```

---

## Wave 5 Field Definitions: MaintenanceConfig

```go
type MaintenanceConfig struct {
    Enabled              bool `json:"enabled"               mapstructure:"enabled"`
    WindowStart          int  `json:"window_start"          mapstructure:"window_start"`
    WindowEnd            int  `json:"window_end"            mapstructure:"window_end"`
    DedupRefresh         bool `json:"dedup_refresh"         mapstructure:"dedup_refresh"`
    SeriesPrune          bool `json:"series_prune"          mapstructure:"series_prune"`
    AuthorSplit          bool `json:"author_split"          mapstructure:"author_split"`
    TombstoneCleanup     bool `json:"tombstone_cleanup"     mapstructure:"tombstone_cleanup"`
    Reconcile            bool `json:"reconcile"             mapstructure:"reconcile"`
    PurgeDeleted         bool `json:"purge_deleted"         mapstructure:"purge_deleted"`
    PurgeOldLogs         bool `json:"purge_old_logs"        mapstructure:"purge_old_logs"`
    DbOptimize           bool `json:"db_optimize"           mapstructure:"db_optimize"`
    LibraryScan          bool `json:"library_scan"          mapstructure:"library_scan"`
    LibraryOrganize      bool `json:"library_organize"      mapstructure:"library_organize"`
    MetadataRefresh      bool `json:"metadata_refresh"      mapstructure:"metadata_refresh"`
    LibrarySizeRefresh   bool `json:"library_size_refresh"  mapstructure:"library_size_refresh"`
    AcoustIDOnlineLookup bool `json:"acoustid_online_lookup" mapstructure:"acoustid_online_lookup"`
    AcoustIDNightlyLimit int  `json:"acoustid_nightly_limit" mapstructure:"acoustid_nightly_limit"`
}
// On Config:
Maintenance MaintenanceConfig `json:"maintenance" mapstructure:"maintenance"`
```

---

## Wave 6 Field Definitions: ScheduledTasksConfig

```go
type ScheduledTaskConfig struct {
    Enabled   bool `json:"enabled"    mapstructure:"enabled"`
    Interval  int  `json:"interval"   mapstructure:"interval"`
    OnStartup bool `json:"on_startup" mapstructure:"on_startup"`
}

type ScheduledTasksConfig struct {
    DedupRefresh    ScheduledTaskConfig `json:"dedup_refresh"    mapstructure:"dedup_refresh"`
    AuthorSplit      ScheduledTaskConfig `json:"author_split"     mapstructure:"author_split"`
    DbOptimize       ScheduledTaskConfig `json:"db_optimize"      mapstructure:"db_optimize"`
    MetadataRefresh  ScheduledTaskConfig `json:"metadata_refresh" mapstructure:"metadata_refresh"`
    AIDedupBatch     ScheduledTaskConfig `json:"ai_dedup_batch"   mapstructure:"ai_dedup_batch"`
}
// On Config:
Scheduled ScheduledTasksConfig `json:"scheduled" mapstructure:"scheduled"`
```

---

## Wave 7 Field Definitions: AutoUpdateConfig

```go
type AutoUpdateConfig struct {
    Enabled      bool   `json:"enabled"       mapstructure:"enabled"`
    Channel      string `json:"channel"       mapstructure:"channel"`
    CheckMinutes int    `json:"check_minutes" mapstructure:"check_minutes"`
    WindowStart  int    `json:"window_start"  mapstructure:"window_start"`
    WindowEnd    int    `json:"window_end"    mapstructure:"window_end"`
}
// On Config:
AutoUpdate AutoUpdateConfig `json:"auto_update" mapstructure:"auto_update"`
```

---

## Wave 8 (deferred): ToolConfig

Deferred until TOOL-1..6 work begins. When ready:

```go
type ToolConfig struct {
    FfmpegPath  string `json:"ffmpeg_path"  mapstructure:"ffmpeg_path"`
    FfprobePath string `json:"ffprobe_path" mapstructure:"ffprobe_path"`
    FpcalcPath  string `json:"fpcalc_path"  mapstructure:"fpcalc_path"`
    OllamaURL   string `json:"ollama_url"   mapstructure:"ollama_url"`
}
// On Config:
Tools ToolConfig `json:"tools" mapstructure:"tools"`
```

---

## What Stays Flat

These fields remain at the top level of `Config` after all waves — they are either singletons, cross-cutting, or already appropriately named:

- Core paths: `RootDir`, `DatabasePath`, `DatabaseType`, `EnableSQLite`, `PlaylistDir`, `SetupComplete`
- Library organization: `OrganizationStrategy`, `ScanOnStartup`, `AutoOrganize`, `AutoScanEnabled`, `AutoScanDebounceSeconds`, `FolderNamingPattern`, `FileNamingPattern`, `CreateBackups`
- Storage quotas: `EnableDiskQuota`, `DiskQuotaPercent`, `EnableUserQuotas`, `DefaultUserQuotaGB`
- Metadata: `AutoFetchMetadata`, `WriteBackMetadata`, `EmbedCoverArt`, `Language`, `MetadataSources`, `MetadataReviewDefaultView`, `HardcoverAPIToken`, `GoogleBooksAPIKey`
- AI: `EnableAIParsing`, `OpenAIAPIKey`, `AcoustIDAPIKey`, `DedupReviewModel`, `MetadataReviewModel`, `FilenameParseModel`, `CoverArtModel`
- Performance: `ConcurrentScans`, `ChapterConsolidationThresholdMin`, `OperationTimeoutMinutes`, `MinBookSizeBytes`
- Logging: `LogLevel`, `LogFormat`, `EnableJsonLogging`
- Retention: `LogRetentionDays`, `OperationLogRetentionDays`, `ActivityLogRetention*`, `PurgeSoftDeleted*`
- Auth / rate limits: `EnableAuth`, `EnableRateLimit`, `APIRateLimitPerMinute`, `AuthRateLimitPerMinute`, `JSONBodyLimitMB`, `UploadBodyLimitMB`, `BasicAuth*`
- Memory / cache: `MemoryLimitType`, `CacheSize`, `MetadataFetchCacheTTLDays`, `MemoryLimitPercent`, `MemoryLimitMB`, `CacheInvalidateOnBookUpdate`
- Paths / apply: `PathFormat`, `SegmentTitleFormat`, `AutoRenameOnApply`, `AutoWriteTagsOnApply`, `VerifyAfterWrite`
- Open Library: `OpenLibraryDumpEnabled`, `OpenLibraryDumpDir`
- Deluge (flat): `DelugeWebURL`, `DelugeWebPassword`, `DelugeDiscoveryLabel`, `DelugeDiscoveryEnabled`, `DelugeMoveEnabled`, `ProtectedPaths`
- Already nested: `DownloadClient DownloadClientConfig`, `Plugins map[string]PluginConfig`

---

## Testing Strategy

Each wave:
1. Existing config tests must pass unchanged (the blob round-trip tests cover migration)
2. Add a test for the migration function: load a flat blob, assert the migrated blob has nested keys, assert field values are preserved
3. Add a test for the `UpdateConfig` compat shim: send flat keys, assert nested fields are updated
4. `go vet ./...` and `go build ./...` must be clean before PR opens
5. Deploy to prod and verify settings page still saves/loads correctly

---

## Files Modified Per Wave

| File | Change |
|---|---|
| `internal/config/config.go` | Add sub-struct type, replace flat fields, update `InitConfig` + `ResetToDefaults` |
| `internal/config/persistence.go` | Add migration function, update `applySetting` cases, add compat shim call in `UpdateConfig` path |
| `internal/config/update_service.go` | Add flat-key remapping shim |
| `internal/config/*_test.go` | Migration test + compat shim test |
| All callsite files | Mechanical find-replace of field path |

Wave 2 also modifies: `internal/dedup/unified/config.go` (switch from Viper reads to `AppConfig.Dedup.Signals`)
Wave 4 also modifies: `internal/itunes/service/config.go` (simplify construction from `ITunes ITunesConfig` directly)
