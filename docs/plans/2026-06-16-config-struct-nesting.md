<!-- file: docs/plans/2026-06-16-config-struct-nesting.md -->
<!-- version: 1.0.0 -->
<!-- guid: f2a7c3e8-9b4d-4f1a-8c6e-2d5b0a3f7e9c -->
<!-- last-edited: 2026-06-16 -->

# Config Struct Nesting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor `AppConfig` from 155+ flat fields into logical nested sub-structs, one wave per PR, with startup blob migration and API compatibility shims.

**Architecture:** Each wave defines a new sub-struct type, replaces flat fields in `Config`, migrates old flat blobs to nested format on startup, and shims the update API to accept both old flat keys and new nested keys during the UI transition period. Wave 1 (EmbeddingConfig) is the template — Waves 2–7 follow the identical 6-step pattern with different field names.

**Tech Stack:** Go 1.24, Viper, PebbleDB (config_blob), `encoding/json`, `github.com/spf13/viper`, testify

**Spec:** `docs/specs/2026-06-16-config-struct-nesting-design.md`

---

## Files Modified (all waves)

| File | Role |
|------|------|
| `internal/config/config.go` | Sub-struct type definition, `Config` struct, `InitConfig` Mutate block, `ResetToDefaults` |
| `internal/config/persistence.go` | Blob migration function, `applySetting` case updates, `SyncConfigFromEnv` additions |
| `internal/config/update_service.go` | Flat-key compat shim called before `json.Unmarshal` |
| `internal/config/config_test.go` | Tests for `InitConfig` defaults + env var wiring |
| `internal/config/persistence_test.go` | Migration test + compat shim test |
| All callsite files (per wave) | Mechanical field path updates: `AppConfig.EmbeddingEnabled` → `AppConfig.Embedding.Enabled` |

Wave 2 also modifies: `internal/dedup/unified/config.go`
Wave 4 also modifies: `internal/itunes/service/config.go`

---

## WAVE 1 — EmbeddingConfig (5 fields)

Fields moving: `EmbeddingEnabled`, `EmbeddingModel`, `EmbeddingDimensions`, `EmbeddingBaseURL`, `VectorIndexBackend`

### Task W1-1: Create worktree + branch

- [ ] **Create isolated worktree**
  ```bash
  git worktree add ../audiobook-organizer-config-embedding -b refactor/config-embedding-struct
  cd ../audiobook-organizer-config-embedding
  git status  # must show clean working tree
  ```

---

### Task W1-2: Write failing tests for migration + compat shim

**Files:**
- Modify: `internal/config/persistence_test.go`

- [ ] **Add migration test (will fail — function doesn't exist yet)**

Open `internal/config/persistence_test.go` and add at the end of the file:

```go
func TestMigrateEmbeddingFields_FlatBlob(t *testing.T) {
	flatBlob := `{
		"embedding_enabled": true,
		"embedding_model": "text-embedding-3-large",
		"embedding_dimensions": 3072,
		"embedding_base_url": "http://localhost:11434/v1",
		"vector_index_backend": "hnsw",
		"root_dir": "/data"
	}`

	migrated, changed := migrateEmbeddingBlob(flatBlob)
	require.True(t, changed, "flat blob should be migrated")

	var result map[string]any
	require.NoError(t, json.Unmarshal([]byte(migrated), &result))

	emb, ok := result["embedding"].(map[string]any)
	require.True(t, ok, "embedding key should exist as object")
	assert.Equal(t, true, emb["enabled"])
	assert.Equal(t, "text-embedding-3-large", emb["model"])
	assert.Equal(t, float64(3072), emb["dimensions"])
	assert.Equal(t, "http://localhost:11434/v1", emb["base_url"])
	assert.Equal(t, "hnsw", emb["vector_backend"])

	// flat keys must be gone
	assert.NotContains(t, result, "embedding_enabled")
	assert.NotContains(t, result, "embedding_model")
	assert.NotContains(t, result, "embedding_dimensions")
	assert.NotContains(t, result, "embedding_base_url")
	assert.NotContains(t, result, "vector_index_backend")

	// unrelated keys must be preserved
	assert.Equal(t, "/data", result["root_dir"])
}

func TestMigrateEmbeddingFields_AlreadyNested(t *testing.T) {
	nestedBlob := `{"embedding": {"enabled": true, "model": "bge-m3"}, "root_dir": "/data"}`
	_, changed := migrateEmbeddingBlob(nestedBlob)
	assert.False(t, changed, "already-nested blob should be a no-op")
}

func TestMigrateEmbeddingFields_EmptyBlob(t *testing.T) {
	_, changed := migrateEmbeddingBlob(`{}`)
	assert.False(t, changed, "empty blob should be a no-op")
}

func TestRemapEmbeddingKeys_FlatKeys(t *testing.T) {
	payload := map[string]any{
		"embedding_enabled":    true,
		"embedding_model":      "bge-m3",
		"embedding_dimensions": float64(1024),
		"root_dir":             "/data",
	}
	result := remapEmbeddingKeys(payload)

	emb, ok := result["embedding"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, emb["enabled"])
	assert.Equal(t, "bge-m3", emb["model"])
	assert.Equal(t, float64(1024), emb["dimensions"])
	assert.Equal(t, "/data", result["root_dir"]) // untouched
	assert.NotContains(t, result, "embedding_enabled")
	assert.NotContains(t, result, "embedding_model")
}

func TestRemapEmbeddingKeys_MixedKeys(t *testing.T) {
	// Client sends both flat legacy key AND new nested key — merge, don't overwrite
	payload := map[string]any{
		"embedding_enabled": false,
		"embedding":         map[string]any{"model": "bge-m3"},
	}
	result := remapEmbeddingKeys(payload)
	emb := result["embedding"].(map[string]any)
	assert.Equal(t, false, emb["enabled"])
	assert.Equal(t, "bge-m3", emb["model"])
}

func TestRemapEmbeddingKeys_NoFlatKeys(t *testing.T) {
	payload := map[string]any{"root_dir": "/data"}
	result := remapEmbeddingKeys(payload)
	assert.Equal(t, map[string]any{"root_dir": "/data"}, result)
}
```

- [ ] **Run tests — expect compile failure** (functions not defined yet)
  ```bash
  cd ../audiobook-organizer-config-embedding
  go test ./internal/config/... 2>&1 | head -20
  ```
  Expected: `undefined: migrateEmbeddingBlob` and `undefined: remapEmbeddingKeys`

---

### Task W1-3: Write failing test for InitConfig viper wiring

**Files:**
- Modify: `internal/config/config_test.go`

- [ ] **Add env var wiring tests**

Add to `internal/config/config_test.go`:

```go
func TestInitConfig_EmbeddingDefaults(t *testing.T) {
	viper.Reset()
	InitConfig()
	snap := Snapshot()

	assert.True(t, snap.Embedding.Enabled)
	assert.Equal(t, "text-embedding-3-large", snap.Embedding.Model)
	assert.Equal(t, 3072, snap.Embedding.Dimensions)
	assert.Equal(t, "", snap.Embedding.BaseURL)
	assert.Equal(t, "chromem", snap.Embedding.VectorBackend)
}

func TestInitConfig_EmbeddingFromEnv(t *testing.T) {
	t.Setenv("EMBEDDING_ENABLED", "false")
	t.Setenv("EMBEDDING_BASE_URL", "http://localhost:11434/v1")
	t.Setenv("EMBEDDING_MODEL", "bge-m3")
	t.Setenv("EMBEDDING_DIMENSIONS", "1024")
	t.Setenv("VECTOR_INDEX_BACKEND", "hnsw")
	viper.Reset()
	InitConfig()
	snap := Snapshot()

	assert.False(t, snap.Embedding.Enabled)
	assert.Equal(t, "http://localhost:11434/v1", snap.Embedding.BaseURL)
	assert.Equal(t, "bge-m3", snap.Embedding.Model)
	assert.Equal(t, 1024, snap.Embedding.Dimensions)
	assert.Equal(t, "hnsw", snap.Embedding.VectorBackend)
}
```

- [ ] **Run — expect compile failure** (`snap.Embedding` undefined)
  ```bash
  go test ./internal/config/... 2>&1 | head -10
  ```

---

### Task W1-4: Define EmbeddingConfig struct + replace flat fields

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Add EmbeddingConfig type above the Config struct**

Find the line that says `type Config struct {` and insert immediately before it:

```go
// EmbeddingConfig holds all settings for the local/remote embedding pipeline.
type EmbeddingConfig struct {
	Enabled       bool   `json:"enabled"        mapstructure:"enabled"`
	Model         string `json:"model"          mapstructure:"model"`
	Dimensions    int    `json:"dimensions"     mapstructure:"dimensions"`
	BaseURL       string `json:"base_url"       mapstructure:"base_url"`
	VectorBackend string `json:"vector_backend" mapstructure:"vector_backend"`
}
```

- [ ] **Replace the 5 flat fields in Config with the sub-struct**

Inside `type Config struct`, find:
```go
EmbeddingEnabled bool   `json:"embedding_enabled"` // default true
EmbeddingModel   string `json:"embedding_model"`   // default "text-embedding-3-large"
// EmbeddingDimensions is the vector dimension of the configured embedding
// ...
EmbeddingDimensions int `json:"embedding_dimensions"` // default 3072
// EmbeddingBaseURL, when non-empty, points the embedding client at an
// ...
EmbeddingBaseURL string `json:"embedding_base_url"` // default ""
// VectorIndexBackend selects the in-memory ANN backend for dedup Layer 2:
// ...
VectorIndexBackend       string  `json:"vector_index_backend"`        // default "chromem"
```
Replace all 5 fields (including any multi-line comments between them) with:
```go
// Embedding holds configuration for the embedding pipeline (model, provider, vector backend).
Embedding EmbeddingConfig `json:"embedding" mapstructure:"embedding"`
```

- [ ] **Update viper.SetDefault calls in InitConfig**

Find the block:
```go
viper.SetDefault("embedding_enabled", true)
viper.SetDefault("embedding_model", "text-embedding-3-large")
viper.SetDefault("embedding_dimensions", 3072)
viper.SetDefault("embedding_base_url", "")
viper.SetDefault("vector_index_backend", "chromem")
```
Replace with:
```go
viper.SetDefault("embedding.enabled", true)
viper.SetDefault("embedding.model", "text-embedding-3-large")
viper.SetDefault("embedding.dimensions", 3072)
viper.SetDefault("embedding.base_url", "")
viper.SetDefault("embedding.vector_backend", "chromem")
```

- [ ] **Update the Mutate struct literal in InitConfig**

Inside the `*c = Config{...}` literal, find the section that had the flat embedding fields (they were in the post-struct block — already moved to the struct literal in PR #1464). Find:
```go
// Embedding + vector index
EmbeddingEnabled:   viper.GetBool("embedding_enabled"),
EmbeddingModel:     viper.GetString("embedding_model"),
EmbeddingDimensions: viper.GetInt("embedding_dimensions"),
EmbeddingBaseURL:   viper.GetString("embedding_base_url"),
VectorIndexBackend: viper.GetString("vector_index_backend"),
```
Replace with:
```go
Embedding: EmbeddingConfig{
    Enabled:       viper.GetBool("embedding.enabled"),
    Model:         viper.GetString("embedding.model"),
    Dimensions:    viper.GetInt("embedding.dimensions"),
    BaseURL:       viper.GetString("embedding.base_url"),
    VectorBackend: viper.GetString("embedding.vector_backend"),
},
```

- [ ] **Update ResetToDefaults**

Find the 5 flat embedding field lines in `ResetToDefaults` and replace with:
```go
Embedding: EmbeddingConfig{
    Enabled:       true,
    Model:         "text-embedding-3-large",
    Dimensions:    3072,
    BaseURL:       "",
    VectorBackend: "chromem",
},
```

- [ ] **Verify the package compiles (many callsite errors expected)**
  ```bash
  go build ./internal/config/... 2>&1
  ```
  Expected: compile errors in other packages referencing `AppConfig.EmbeddingEnabled` etc.

---

### Task W1-5: Implement migration function + compat shim

**Files:**
- Modify: `internal/config/persistence.go`
- Modify: `internal/config/update_service.go`

- [ ] **Add migrateEmbeddingBlob to persistence.go**

Add after the import block but before any existing functions:

```go
// migrateEmbeddingBlob rewrites a flat-format config blob to the nested EmbeddingConfig
// format. Returns the (possibly modified) blob and whether a migration occurred.
// Safe to call repeatedly: returns (blob, false) if already nested.
func migrateEmbeddingBlob(blob string) (string, bool) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(blob), &raw); err != nil {
		return blob, false
	}
	if _, isFlat := raw["embedding_enabled"]; !isFlat {
		return blob, false
	}

	type flatShape struct {
		EmbeddingEnabled    bool    `json:"embedding_enabled"`
		EmbeddingModel      string  `json:"embedding_model"`
		EmbeddingDimensions int     `json:"embedding_dimensions"`
		EmbeddingBaseURL    string  `json:"embedding_base_url"`
		VectorIndexBackend  string  `json:"vector_index_backend"`
	}
	var old flatShape
	json.Unmarshal([]byte(blob), &old) //nolint:errcheck — already parsed above

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

	migrated, err := json.Marshal(raw)
	if err != nil {
		return blob, false
	}
	return string(migrated), true
}
```

- [ ] **Call migrateEmbeddingBlob inside LoadConfigFromDatabase**

In `LoadConfigFromDatabase`, find the line that reads the blob value and does `json.Unmarshal`. Add the migration call immediately after reading the blob string, before unmarshal:

```go
// After reading blob.Value:
blobStr := blob.Value
if migrated, changed := migrateEmbeddingBlob(blobStr); changed {
    slog.Info("config: migrated embedding fields to nested format")
    blobStr = migrated
    // Write migrated blob back immediately so next startup skips migration
    if saveErr := saveRawBlob(store, migrated); saveErr != nil {
        slog.Warn("config: failed to persist migrated blob", "err", saveErr)
    }
}
// Use blobStr (not blob.Value) for json.Unmarshal below
```

You also need a `saveRawBlob` helper (add near `SaveConfigToDatabase`):

```go
// saveRawBlob writes a pre-marshaled JSON string directly as the config blob.
// Used only by startup migration to persist migrated blobs without re-marshaling.
func saveRawBlob(store database.SettingsStore, rawJSON string) error {
	return store.Set(database.Setting{
		Key:   "config_blob",
		Value: rawJSON,
	})
}
```

- [ ] **Update SyncConfigFromEnv in persistence.go**

Find the block added in PR #1464:
```go
if viper.IsSet("embedding_base_url") {
    if val := viper.GetString("embedding_base_url"); val != "" {
        c.Embedding.BaseURL = val
    }
}
```
Update to use the new viper key:
```go
if viper.IsSet("embedding.base_url") {
    if val := viper.GetString("embedding.base_url"); val != "" {
        c.Embedding.BaseURL = val
    }
}
```
(The env var `EMBEDDING_BASE_URL` still works — viper maps `embedding.base_url` → `EMBEDDING_BASE_URL` automatically via `AutomaticEnv`.)

- [ ] **Update applySetting cases for the 5 moved fields**

In `persistence.go`, in the `applySetting` switch, find and update:
```go
// Before:
case "embedding_enabled":
    if b, err := strconv.ParseBool(value); err == nil {
        c.EmbeddingEnabled = b
    }
case "embedding_model":
    c.EmbeddingModel = value
// (and embedding_dimensions, embedding_base_url, vector_index_backend)
```
Update each to write to the nested path:
```go
case "embedding_enabled":
    if b, err := strconv.ParseBool(value); err == nil {
        c.Embedding.Enabled = b
    }
case "embedding_model":
    c.Embedding.Model = value
case "embedding_dimensions":
    if n, err := strconv.Atoi(value); err == nil {
        c.Embedding.Dimensions = n
    }
case "embedding_base_url":
    c.Embedding.BaseURL = value
case "vector_index_backend":
    c.Embedding.VectorBackend = value
```

- [ ] **Add remapEmbeddingKeys to update_service.go**

Add before the `UpdateConfig` function:

```go
// remapEmbeddingKeys translates legacy flat embedding keys in a config update
// payload to the nested EmbeddingConfig format. Merges into any existing
// "embedding" sub-object to avoid zeroing sibling fields.
// Remove this shim once the frontend sends nested keys.
func remapEmbeddingKeys(payload map[string]any) map[string]any {
	flatToNested := map[string]string{
		"embedding_enabled":    "enabled",
		"embedding_model":      "model",
		"embedding_dimensions": "dimensions",
		"embedding_base_url":   "base_url",
		"vector_index_backend": "vector_backend",
	}
	nested := make(map[string]any)
	for flat, short := range flatToNested {
		if v, ok := payload[flat]; ok {
			nested[short] = v
			delete(payload, flat)
		}
	}
	if len(nested) == 0 {
		return payload
	}
	if existing, ok := payload["embedding"].(map[string]any); ok {
		for k, v := range nested {
			existing[k] = v
		}
	} else {
		payload["embedding"] = nested
	}
	return payload
}
```

- [ ] **Call remapEmbeddingKeys inside UpdateConfig**

In `update_service.go`, in `UpdateConfig`, find the line that builds `filtered` (the cleaned payload map) before the JSON round-trip. Add the remap call immediately after building `filtered`:

```go
filtered = remapEmbeddingKeys(filtered)
```

- [ ] **Run tests — all new tests should now pass**
  ```bash
  go test ./internal/config/... -run "TestMigrateEmbedding|TestRemapEmbedding|TestInitConfig_Embedding" -v
  ```
  Expected: all 8 new tests PASS

---

### Task W1-6: Update all callsites

**Files:**
- All files that reference `AppConfig.EmbeddingEnabled`, `AppConfig.EmbeddingModel`, `AppConfig.EmbeddingDimensions`, `AppConfig.EmbeddingBaseURL`, `AppConfig.VectorIndexBackend` (and any `cfg.EmbeddingXxx` where `cfg` is a `*Config`)

- [ ] **Find all callsites**
  ```bash
  grep -rn "\.EmbeddingEnabled\|\.EmbeddingModel\|\.EmbeddingDimensions\|\.EmbeddingBaseURL\|\.VectorIndexBackend" \
    --include="*.go" . | grep -v "_test.go" | grep -v "worktrees/"
  ```

- [ ] **Apply mechanical renames** (one sed per field):
  ```bash
  find . -name "*.go" -not -path "./.worktrees/*" -not -path "./docs/*" | xargs sed -i '' \
    -e 's/\.EmbeddingEnabled/.Embedding.Enabled/g' \
    -e 's/\.EmbeddingModel/.Embedding.Model/g' \
    -e 's/\.EmbeddingDimensions/.Embedding.Dimensions/g' \
    -e 's/\.EmbeddingBaseURL/.Embedding.BaseURL/g' \
    -e 's/\.VectorIndexBackend/.Embedding.VectorBackend/g'
  ```

- [ ] **Verify clean build**
  ```bash
  go build ./... 2>&1
  ```
  Expected: no errors

- [ ] **Run full config test suite**
  ```bash
  go test ./internal/config/... -v 2>&1 | tail -20
  ```
  Expected: all tests pass

- [ ] **Run full backend test suite**
  ```bash
  make test 2>&1 | tail -10
  ```
  Expected: PASS

---

### Task W1-7: Update file headers + commit + PR

**Files:**
- Modify: `internal/config/config.go` (header)
- Modify: `internal/config/persistence.go` (header)
- Modify: `internal/config/update_service.go` (header)

- [ ] **Bump version headers** on all 3 modified files (increment minor version, update last-edited to today)

- [ ] **Commit**
  ```bash
  git add -u
  git commit -m "refactor(config): nest embedding fields into EmbeddingConfig sub-struct

  Moves EmbeddingEnabled, EmbeddingModel, EmbeddingDimensions,
  EmbeddingBaseURL, VectorIndexBackend into a nested EmbeddingConfig
  sub-struct at AppConfig.Embedding.

  - Startup migration rewrites old flat blobs to nested format on first boot
  - API compat shim maps legacy flat PUT /config keys to nested paths
  - applySetting updated for pre-blob legacy installs
  - Env vars unchanged: EMBEDDING_ENABLED, EMBEDDING_BASE_URL etc. still work
    (viper maps embedding.enabled -> EMBEDDING_ENABLED automatically)

  Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"
  ```

- [ ] **Push + open PR**
  ```bash
  git push -u origin HEAD
  gh pr create --title "refactor(config): nest embedding fields into EmbeddingConfig sub-struct" \
    --body "See docs/specs/2026-06-16-config-struct-nesting-design.md — Wave 1 of 7."
  ```

- [ ] **Admin-merge + deploy**
  ```bash
  gh pr merge <number> --rebase --admin --delete-branch
  # In primary checkout:
  git checkout main && git pull --ff-only origin main
  make deploy
  ```

---

## WAVE 2 — DedupConfig (9 fields + ScoreConfig absorption)

Same 6-step pattern as Wave 1. Field map:

### Sub-struct definitions

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
	BookHighThreshold          float64          `json:"book_high_threshold"            mapstructure:"book_high_threshold"`
	BookLowThreshold           float64          `json:"book_low_threshold"             mapstructure:"book_low_threshold"`
	AuthorHighThreshold        float64          `json:"author_high_threshold"          mapstructure:"author_high_threshold"`
	AuthorLowThreshold         float64          `json:"author_low_threshold"           mapstructure:"author_low_threshold"`
	AutoMergeEnabled           bool             `json:"auto_merge_enabled"             mapstructure:"auto_merge_enabled"`
	EmbeddingsEnabled          bool             `json:"embeddings_enabled"             mapstructure:"embeddings_enabled"`
	LLMAutoMergeHighConfidence bool             `json:"llm_auto_merge_high_confidence" mapstructure:"llm_auto_merge_high_confidence"`
	OnImportViaScheduler       bool             `json:"on_import_via_scheduler"        mapstructure:"on_import_via_scheduler"`
	ReviewModel                string           `json:"review_model"                   mapstructure:"review_model"`
	Signals                    DedupSignalConfig `json:"signals"                       mapstructure:"signals"`
}
// On Config:
Dedup DedupConfig `json:"dedup" mapstructure:"dedup"`
```

### Flat → nested field map

| Old flat field | New path | Old viper key | New viper key |
|---|---|---|---|
| `DedupBookHighThreshold` | `Dedup.BookHighThreshold` | `dedup_book_high_threshold` | `dedup.book_high_threshold` |
| `DedupBookLowThreshold` | `Dedup.BookLowThreshold` | `dedup_book_low_threshold` | `dedup.book_low_threshold` |
| `DedupAuthorHighThreshold` | `Dedup.AuthorHighThreshold` | `dedup_author_high_threshold` | `dedup.author_high_threshold` |
| `DedupAuthorLowThreshold` | `Dedup.AuthorLowThreshold` | `dedup_author_low_threshold` | `dedup.author_low_threshold` |
| `DedupAutoMergeEnabled` | `Dedup.AutoMergeEnabled` | `dedup_auto_merge_enabled` | `dedup.auto_merge_enabled` |
| `DedupEmbeddingsEnabled` | `Dedup.EmbeddingsEnabled` | `dedup_embeddings_enabled` | `dedup.embeddings_enabled` |
| `DedupLLMAutoMergeHighConfidence` | `Dedup.LLMAutoMergeHighConfidence` | `dedup_llm_auto_merge_high_confidence` | `dedup.llm_auto_merge_high_confidence` |
| `DedupOnImportViaScheduler` | `Dedup.OnImportViaScheduler` | `dedup_on_import_via_scheduler` | `dedup.on_import_via_scheduler` |
| `DedupReviewModel` | `Dedup.ReviewModel` | `dedup_review_model` | `dedup.review_model` |

### Signal fields absorbed from Viper-only ScoreConfig

| Old viper key | New viper key | `DedupSignalConfig` field |
|---|---|---|
| `dedup.signals.band_certain_min` | `dedup.signals.band_certain_min` | `BandCertainMin` |
| `dedup.signals.band_high_min` | `dedup.signals.band_high_min` | `BandHighMin` |
| `dedup.signals.band_medium_min` | `dedup.signals.band_medium_min` | `BandMediumMin` |
| `dedup.signals.band_review_min` | `dedup.signals.band_review_min` | `BandReviewMin` |
| `dedup.signals.duration.boost` | `dedup.signals.duration_boost` | `DurationBoost` |
| `dedup.signals.folder_path.boost` | `dedup.signals.folder_path_boost` | `FolderPathBoost` |

**Extra step for Wave 2:** After absorbing signals into `AppConfig.Dedup.Signals`, update `internal/dedup/unified/config.go` — `LoadScoreConfig()` should read from `config.AppConfig.Dedup.Signals` instead of `viper.Get("dedup.signals.*")`. The `ScoreConfig` struct in that file can then be reduced to a thin wrapper or removed if no longer needed.

Blob migration sentinel: check for `"dedup_book_high_threshold"` at top level.
Compat shim flat keys: all `dedup_*` keys listed in the table above.

---

## WAVE 3 — MetadataScoringConfig (7 fields)

### Sub-struct definition

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

### Flat → nested field map

| Old flat field | New path | Old viper key | New viper key |
|---|---|---|---|
| `MetadataEmbeddingScoringEnabled` | `MetadataScoring.EmbeddingEnabled` | `metadata_embedding_scoring_enabled` | `metadata_scoring.embedding_enabled` |
| `MetadataEmbeddingMinScore` | `MetadataScoring.EmbeddingMinScore` | `metadata_embedding_min_score` | `metadata_scoring.embedding_min_score` |
| `MetadataEmbeddingBestMatchMin` | `MetadataScoring.EmbeddingBestMatch` | `metadata_embedding_best_match_min` | `metadata_scoring.embedding_best_match` |
| `MetadataLLMScoringEnabled` | `MetadataScoring.LLMEnabled` | `metadata_llm_scoring_enabled` | `metadata_scoring.llm_enabled` |
| `MetadataLLMRerankEpsilon` | `MetadataScoring.LLMRerankEpsilon` | `metadata_llm_rerank_epsilon` | `metadata_scoring.llm_rerank_epsilon` |
| `MetadataLLMRerankTopK` | `MetadataScoring.LLMRerankTopK` | `metadata_llm_rerank_top_k` | `metadata_scoring.llm_rerank_top_k` |
| `WriteBackupBeforeTagWrite` | `MetadataScoring.WriteBackupBefore` | `write_backup_before_tag_write` | `metadata_scoring.write_backup_before` |

Blob migration sentinel: check for `"metadata_embedding_scoring_enabled"` at top level.

---

## WAVE 4 — ITunesConfig (10 fields)

### Sub-struct definition

```go
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

### Flat → nested field map

| Old flat field | New path | Old viper key | New viper key |
|---|---|---|---|
| `ITunesSyncEnabled` | `ITunes.SyncEnabled` | `itunes_sync_enabled` | `itunes.sync_enabled` |
| `ITunesSyncInterval` | `ITunes.SyncInterval` | `itunes_sync_interval` | `itunes.sync_interval` |
| `ITLWriteBackEnabled` | `ITunes.WriteBackEnabled` | `itl_write_back_enabled` | `itunes.write_back_enabled` |
| `ITunesLibraryWritePath` | `ITunes.LibraryWritePath` | `itunes_library_write_path` | `itunes.library_write_path` |
| `ITunesLibraryReadPath` | `ITunes.LibraryReadPath` | `itunes_library_read_path` | `itunes.library_read_path` |
| `ITunesAutoWriteBack` | `ITunes.AutoWriteBack` | `itunes_auto_write_back` | `itunes.auto_write_back` |
| `ITunesPathTrimEnabled` | `ITunes.PathTrimEnabled` | `itunes_path_trim_enabled` | `itunes.path_trim_enabled` |
| `ITunesWindowsRootPath` | `ITunes.WindowsRootPath` | `itunes_windows_root_path` | `itunes.windows_root_path` |
| `ITunesMediaRoot` | `ITunes.MediaRoot` | `itunes_media_root` | `itunes.media_root` |
| `ITunesPathMappings` | `ITunes.PathMappings` | `itunes_path_mappings` | `itunes.path_mappings` |

Blob migration sentinel: check for `"itunes_sync_enabled"` at top level.

**Extra step for Wave 4:** Update `internal/itunes/service/config.go` — the local `Config` slice struct is now redundant. Simplify service construction to take `config.ITunesConfig` directly, or just embed `config.AppConfig.ITunes` at construction time.

---

## WAVE 5 — MaintenanceConfig (17 fields)

### Sub-struct definition

```go
type MaintenanceConfig struct {
	Enabled              bool `json:"enabled"                mapstructure:"enabled"`
	WindowStart          int  `json:"window_start"           mapstructure:"window_start"`
	WindowEnd            int  `json:"window_end"             mapstructure:"window_end"`
	DedupRefresh         bool `json:"dedup_refresh"          mapstructure:"dedup_refresh"`
	SeriesPrune          bool `json:"series_prune"           mapstructure:"series_prune"`
	AuthorSplit          bool `json:"author_split"           mapstructure:"author_split"`
	TombstoneCleanup     bool `json:"tombstone_cleanup"      mapstructure:"tombstone_cleanup"`
	Reconcile            bool `json:"reconcile"              mapstructure:"reconcile"`
	PurgeDeleted         bool `json:"purge_deleted"          mapstructure:"purge_deleted"`
	PurgeOldLogs         bool `json:"purge_old_logs"         mapstructure:"purge_old_logs"`
	DbOptimize           bool `json:"db_optimize"            mapstructure:"db_optimize"`
	LibraryScan          bool `json:"library_scan"           mapstructure:"library_scan"`
	LibraryOrganize      bool `json:"library_organize"       mapstructure:"library_organize"`
	MetadataRefresh      bool `json:"metadata_refresh"       mapstructure:"metadata_refresh"`
	LibrarySizeRefresh   bool `json:"library_size_refresh"   mapstructure:"library_size_refresh"`
	AcoustIDOnlineLookup bool `json:"acoustid_online_lookup" mapstructure:"acoustid_online_lookup"`
	AcoustIDNightlyLimit int  `json:"acoustid_nightly_limit" mapstructure:"acoustid_nightly_limit"`
}
// On Config:
Maintenance MaintenanceConfig `json:"maintenance" mapstructure:"maintenance"`
```

### Flat → nested field map

| Old flat field | New path | Old viper key | New viper key |
|---|---|---|---|
| `MaintenanceWindowEnabled` | `Maintenance.Enabled` | `maintenance_window_enabled` | `maintenance.enabled` |
| `MaintenanceWindowStart` | `Maintenance.WindowStart` | `maintenance_window_start` | `maintenance.window_start` |
| `MaintenanceWindowEnd` | `Maintenance.WindowEnd` | `maintenance_window_end` | `maintenance.window_end` |
| `MaintenanceWindowDedupRefresh` | `Maintenance.DedupRefresh` | `maintenance_window_dedup_refresh` | `maintenance.dedup_refresh` |
| `MaintenanceWindowSeriesPrune` | `Maintenance.SeriesPrune` | `maintenance_window_series_prune` | `maintenance.series_prune` |
| `MaintenanceWindowAuthorSplit` | `Maintenance.AuthorSplit` | `maintenance_window_author_split` | `maintenance.author_split` |
| `MaintenanceWindowTombstoneCleanup` | `Maintenance.TombstoneCleanup` | `maintenance_window_tombstone_cleanup` | `maintenance.tombstone_cleanup` |
| `MaintenanceWindowReconcile` | `Maintenance.Reconcile` | `maintenance_window_reconcile` | `maintenance.reconcile` |
| `MaintenanceWindowPurgeDeleted` | `Maintenance.PurgeDeleted` | `maintenance_window_purge_deleted` | `maintenance.purge_deleted` |
| `MaintenanceWindowPurgeOldLogs` | `Maintenance.PurgeOldLogs` | `maintenance_window_purge_old_logs` | `maintenance.purge_old_logs` |
| `MaintenanceWindowDbOptimize` | `Maintenance.DbOptimize` | `maintenance_window_db_optimize` | `maintenance.db_optimize` |
| `MaintenanceWindowLibraryScan` | `Maintenance.LibraryScan` | `maintenance_window_library_scan` | `maintenance.library_scan` |
| `MaintenanceWindowLibraryOrganize` | `Maintenance.LibraryOrganize` | `maintenance_window_library_organize` | `maintenance.library_organize` |
| `MaintenanceWindowMetadataRefresh` | `Maintenance.MetadataRefresh` | `maintenance_window_metadata_refresh` | `maintenance.metadata_refresh` |
| `MaintenanceWindowLibrarySizeRefresh` | `Maintenance.LibrarySizeRefresh` | `maintenance_window_library_size_refresh` | `maintenance.library_size_refresh` |
| `MaintenanceWindowAcoustIDOnlineLookup` | `Maintenance.AcoustIDOnlineLookup` | `maintenance_window_acoustid_online_lookup` | `maintenance.acoustid_online_lookup` |
| `AcoustIDOnlineLookupNightlyLimit` | `Maintenance.AcoustIDNightlyLimit` | `acoustid_online_lookup_nightly_limit` | `maintenance.acoustid_nightly_limit` |

Blob migration sentinel: check for `"maintenance_window_enabled"` at top level.

---

## WAVE 6 — ScheduledTasksConfig (~15 fields)

### Sub-struct definition

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

### Flat → nested field map

| Old flat field | New path |
|---|---|
| `ScheduledDedupRefreshEnabled` | `Scheduled.DedupRefresh.Enabled` |
| `ScheduledDedupRefreshInterval` | `Scheduled.DedupRefresh.Interval` |
| `ScheduledDedupRefreshOnStartup` | `Scheduled.DedupRefresh.OnStartup` |
| `ScheduledAuthorSplitEnabled` | `Scheduled.AuthorSplit.Enabled` |
| `ScheduledAuthorSplitInterval` | `Scheduled.AuthorSplit.Interval` |
| `ScheduledAuthorSplitOnStartup` | `Scheduled.AuthorSplit.OnStartup` |
| `ScheduledDbOptimizeEnabled` | `Scheduled.DbOptimize.Enabled` |
| `ScheduledDbOptimizeInterval` | `Scheduled.DbOptimize.Interval` |
| `ScheduledDbOptimizeOnStartup` | `Scheduled.DbOptimize.OnStartup` |
| `ScheduledMetadataRefreshEnabled` | `Scheduled.MetadataRefresh.Enabled` |
| `ScheduledMetadataRefreshInterval` | `Scheduled.MetadataRefresh.Interval` |
| `ScheduledMetadataRefreshOnStartup` | `Scheduled.MetadataRefresh.OnStartup` |
| `ScheduledAIDedupBatchEnabled` | `Scheduled.AIDedupBatch.Enabled` |
| `ScheduledAIDedupBatchInterval` | `Scheduled.AIDedupBatch.Interval` |
| `ScheduledAIDedupBatchOnStartup` | `Scheduled.AIDedupBatch.OnStartup` |

**Before implementing:** run `grep -n "Scheduled" internal/config/config.go` to get the definitive list of all scheduled task fields — there may be more than the 5 task groups listed here.

Blob migration sentinel: check for `"scheduled_dedup_refresh_enabled"` at top level.

---

## WAVE 7 — AutoUpdateConfig (5 fields)

### Sub-struct definition

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

### Flat → nested field map

| Old flat field | New path | Old viper key | New viper key |
|---|---|---|---|
| `AutoUpdateEnabled` | `AutoUpdate.Enabled` | `auto_update_enabled` | `auto_update.enabled` |
| `AutoUpdateChannel` | `AutoUpdate.Channel` | `auto_update_channel` | `auto_update.channel` |
| `AutoUpdateCheckMinutes` | `AutoUpdate.CheckMinutes` | `auto_update_check_minutes` | `auto_update.check_minutes` |
| `AutoUpdateWindowStart` | `AutoUpdate.WindowStart` | `auto_update_window_start` | `auto_update.window_start` |
| `AutoUpdateWindowEnd` | `AutoUpdate.WindowEnd` | `auto_update_window_end` | `auto_update.window_end` |

Blob migration sentinel: check for `"auto_update_enabled"` at top level.

---

## WAVE 8 — ToolConfig (deferred)

Implement as part of TOOL-1..6 work. See `docs/research/2026-06-15-config-architecture-evaluation.md` Finding 10 and `TODO.md` TOOL-1 for scope. Branch name suggestion: `refactor/config-tool-struct`.

---

## Cross-Wave Notes

**Testing command for any wave:**
```bash
go test ./internal/config/... -v
make test
```

**Verifying env vars still work after a wave:**
```bash
EMBEDDING_ENABLED=false go run ./cmd/... serve --db /tmp/test.pebble &
curl -s http://localhost:8080/api/v1/config | jq '.embedding.enabled'
# Expected: false
```

**Verifying migration ran on startup:**
```bash
grep "migrated.*nested" /var/log/audiobook-organizer.log
```

**Verifying compat shim works (PUT with flat keys):**
```bash
curl -X PUT http://localhost:8080/api/v1/config \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"embedding_enabled": false}'
curl http://localhost:8080/api/v1/config | jq '.embedding.enabled'
# Expected: false
```
