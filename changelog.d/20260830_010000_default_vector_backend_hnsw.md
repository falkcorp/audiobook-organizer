### Fixed

#### Dedup vector index now defaults to HNSW instead of the brute-force chromem scan

`embedding.vector_backend` selects the in-memory ANN index used by dedup
Layer 2. Despite the name, `chromem` is not an approximate index — the code
itself calls it a "brute-force cosine scan" — so its per-query cost grows
linearly with the number of indexed vectors. `hnsw` (coder/hnsw) is
sub-linear.

Measured on this repo at 1024 dimensions with the `is_primary_version` filter:

| corpus | chromem | HNSW | ratio |
|---|---|---|---|
| 10K vectors | 9.18 ms/op | 0.51 ms/op | 17.9x |
| 50K vectors | 111.9 ms/op | 0.53 ms/op | 210x |

`dedup.full-scan` issues roughly one query per book. Across a ~61,000-book
library that is the difference between about 32 seconds and about 1.9
CPU-hours per full scan — with no error, no unusual log line, and nothing in
the UI to distinguish the two.

`chromem` was the shipped default in **two** places: the
`viper.SetDefault("embedding.vector_backend", ...)` call in `InitConfig` and a
hardcoded struct literal in `ResetToDefaults`. Both now default to `hnsw`.

A third, less obvious default was doing the same damage: the selection site in
`internal/server/registry_wire.go` tested `cfg.Embedding.VectorBackend ==
"hnsw"` and fell through to chromem for *every* other value, including the
empty string. An upgraded install whose stored `config_blob` predates the field
gets `""` out of `migrateEmbeddingBlob`, and `viper.SetDefault` never runs on
that path — so flipping only the two literals would have left the trap live for
the entire upgraded population. An empty value now resolves to the default,
both in `Config.Validate()` and at the selection site itself.

**Production is unaffected by this change.** The deployed instance already
selects HNSW through the `VECTOR_INDEX_BACKEND` environment binding, which
takes precedence over every default here. Deploying this does not rebuild,
re-hydrate, or otherwise change the index it is already running. What changes is
fresh installs, installs that lose the environment variable, and installs whose
persisted config never carried the field — all three of which silently got the
brute-force scan before.

### Added

#### Selecting the chromem vector backend is now audible

`chromem` remains a deliberately selectable fallback — coder/hnsw had a SIGSEGV
crash loop in June 2026 and chromem is the simple escape hatch — but a fallback
nobody can tell they are running is a trap rather than a fallback. Building the
vector store on chromem now emits a WARN naming the backend, stating that it is
a brute-force scan rather than an approximate index, and that query cost grows
linearly with the corpus. HNSW stays silent, so the warning carries information.

The warning deliberately omits a vector count: the store is constructed empty
at wiring time and hydrated later, so there is no count available there that
would not require doing real work purely to log it.

#### `embedding.vector_backend` is validated

The setting is enum-like but was never validated, so an unknown value — a typo
such as `hnws` — was accepted silently and the exact-match selection test
downgraded it to the brute-force scan with no error surface at all. This is the
same defect `database_type` and `organization_strategy` were already guarded
against. `Config.Validate()` now normalizes an empty value to `hnsw` and rejects
anything that is not `hnsw` or `chromem`. The normalization runs before the
enum check on purpose: the upgrade path legitimately produces `""`, and
rejecting that would stop those installs from starting.
