<!-- file: docs/compat-surfaces.md -->
<!-- version: 1.1.0 -->
<!-- guid: c3d4e5f6-7890-abcd-ef12-345678901234 -->
<!-- last-edited: 2026-10-09 -->

# Backward-Compatibility Surfaces

This document lists every backward-compatibility shim — type aliases,
re-exported constructors, forwarding adapters — so each one has a documented
owner and removal condition. New shims should be added here when they are
created.

Shims without a removal condition are **legacy debt**: they can be cleaned up
whenever a caller sweep is done.

---

## Internal Server Package → organizer

The `internal/server` re-export files from the `organizer` and `deluge`
extraction (PRs #1232–#1239) have all been removed; callers import those
packages directly.

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
