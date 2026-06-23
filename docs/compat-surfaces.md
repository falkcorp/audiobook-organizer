<!-- file: docs/compat-surfaces.md -->
<!-- version: 1.0.0 -->
<!-- guid: c3d4e5f6-7890-abcd-ef12-345678901234 -->
<!-- last-edited: 2026-06-23 -->

# Backward-Compatibility Surfaces

This document lists every backward-compatibility shim — type aliases,
re-exported constructors, forwarding adapters — so each one has a documented
owner and removal condition. New shims should be added here when they are
created.

Shims without a removal condition are **legacy debt**: they can be cleaned up
whenever a caller sweep is done.

---

## Internal Server Package → organizer

All four files below exist because types were extracted from `internal/server`
into `internal/organizer` (PRs #1232–#1239). The server package needs the old
names until callers in other packages are updated to import `organizer` directly.

| File | Re-exports | Removal condition |
|---|---|---|
| `internal/server/file_move.go` | `MoveBookFileResult`, `MoveBookFile` from `organizer` | Update all `server.MoveBookFileResult`/`server.MoveBookFile` usages (check with `findReferences`) then delete file |
| `internal/server/pipeline_checkpoint.go` | Phase constants + `CheckpointData` etc. from `organizer` | Same: sweep `server.phaseRename` etc., then delete |
| `internal/server/file_pipeline.go` | `FileRenameEntry`, `FilePipelineResult`, `RenameResult`, `RelocateRequest/Result` from `organizer` | Sweep + delete |
| `internal/server/deluge_importer_adapter.go` | `LibraryImporterAdapter` from `deluge` | Sweep callers inside `internal/server/`, update to `deluge.LibraryImporterAdapter`, then delete |

**How to remove:** `grep -r 'server\.MoveBookFile\|server\.FileRename\|server\.FilePipeline\|server\.RelocateRequest\|server\.LibraryImporterAdapter' internal/` to find callers; update imports; delete shim file.

---

## Internal audiobooks Package → organizer

Same extraction wave as above.

| File | Re-exports | Removal condition |
|---|---|---|
| `internal/audiobooks/rename.go` | `RenameService`, `TagChange`, `NewRenameService`, `NewRenameServiceWithMap` from `organizer` | Sweep `audiobooks.RenameService` etc. → `organizer.RenameService`, delete file |
| `internal/audiobooks/organize_preview.go` | `OrganizePreviewStep/Response/Service` + `NewOrganizePreviewService` from `organizer` | Sweep → `organizer.PreviewXxx`, delete file |

---

## Database Package — Legacy/Deprecated Methods

| Location | Surface | Removal condition |
|---|---|---|
| `internal/database/embedding_store.go:1242` | Old blob keyspace format compatibility (pre-T021) | Dead after all blobs written before T021 are re-encoded; no live blobs remain from before May 2025 — safe to remove on next embedding schema bump |
| `internal/database/metadata_fetch_cache.go:115` | Deprecated cache key format | Remove when `MetadataFetchCache` schema version bumped past v2 |

---

## Config — Goodreads API Key

| Location | Surface | Removal condition |
|---|---|---|
| `internal/config/config.go:378` | `GoodreadsAPIKey` field | Goodreads deprecated their API in Dec 2020; field still accepted in config for existing deployments. Remove when config v2 migration ships (all deployments have migrated). |

---

## Logger / Operations — ProgressReporter

| Location | Surface | Removal condition |
|---|---|---|
| `internal/logger/operation.go:159` | `Log()` method satisfying deprecated `ProgressReporter.Log` interface | Remove when `ProgressReporter` interface callers have all been updated to use `Logf` or slog directly |

---

## How to Add a New Compat Surface

When you create a backward-compatibility shim:

1. Add an entry to this file **in the same PR** as the shim itself.
2. Fill in `Owner` (your name or PR number) and `Removal condition`.
3. Add a `// TODO(compat): remove when <condition>` comment at the top of the shim file.
