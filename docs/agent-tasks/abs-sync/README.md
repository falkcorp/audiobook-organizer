<!-- file: docs/agent-tasks/abs-sync/README.md -->
<!-- version: 1.2.0 -->
<!-- guid: 7b287737-c26b-4b06-abb2-52d222ec0bcf -->
<!-- last-edited: 2026-07-30 -->

# abs-sync — Audiobookshelf-compatible sync API

Implementation workstream for [`docs/specs/2026-07-29-abs-sync-api-design.md`](../../specs/2026-07-29-abs-sync-api-design.md).
Read **§1.7–1.9** of that spec before any task — the client contract is empirical
(derived from a real ABS 2.36.0 server plus source audits of AudioBooth and Absorb),
**not** from the published ABS docs, which are formally abandoned.

**Target clients: AudioBooth (primary) + Absorb (secondary) only** (§1.9). Both are open
source, so their behavior is auditable and re-auditable. Other clients do not constrain
the design.

## Already shipped (do not redo)

| Merged | What |
|---|---|
| #2062 | Design spec, Phase 0 plan, client audit, ABS 2.36.0 Docker oracle, 22 golden fixtures |
| #2063 | `internal/syncapi/conformance` — presence-and-type differ (the merge gate for DTO work) |
| #2064 | `internal/audioutil` — `ProbeChapters`, `CumulativeOffsets`, `SynthesizeChapters`, `ShiftChapters` |
| #2065 | `internal/httputil` — `ServeFileWithRange` (206/416/If-Range/suffix ranges, verified on a real 115 MB m4b) |
| #2070 | `sync_item` keyspace — durable 36-char UUID identity, redirect-chain resolution (TASK-01) |
| #2068 | `sync_file` keyspace — durable per-file IDs for ABS `ino` (TASK-02) |
| #2069 | Persisted `Chapter` type + Pebble keyspace + `DeleteBook` cascade (TASK-06) |

## Waves and file ownership

Waves exist to prevent same-file collisions. **Tasks in the same wave never touch the
same file.** The two hard rules, learned from prior sweeps:

1. **Nobody edits `internal/database/store.go`.** Every new type goes in its own new file.
2. **Only TASK-11 may touch `go.mod`/`go.sum`.** No other task adds a dependency.

| Wave | Tasks | Owns (exclusive) | Depends on |
|---|---|---|---|
| **1** | TASK-01 | `internal/database/pebble_store_syncid.go` (+`_test`) | — |
| **1** | TASK-02 | `internal/database/pebble_store_syncfile.go` (+`_test`) | — |
| **1** | TASK-06 | `internal/database/pebble_store_chapters.go` (+`_test`) | — |
| **1** | TASK-08 | `internal/syncapi/progress/` (new pkg, pure) | — |
| **1** | TASK-10 | `internal/audioutil/drm.go` (+`_test`) | — |
| **2** | TASK-03 | `internal/merge/**` (merge-follow hook) | 01 |
| **2** | TASK-07 | `internal/scanner/process_file.go` | 06 |
| **2** | TASK-09 | `internal/syncapi/progress/` (bookmarks) | 08 |
| **3** | TASK-04 | new op file under `internal/operations/` or `internal/maintenance/` | 01, 02 |
| **3** | TASK-05 | `*_test.go` only (survival suite) | 01, 02, 03 |
| **2** | TASK-12 | `internal/dedup/book_dedup.go`, `internal/merge/**` (CombineBooks), `internal/scanner/scanner.go` | 01, 02 |
| **4** | TASK-11 | `internal/server/**` ABS group, `go.mod` | — (header question RESOLVED, §1.9.3) |

Waves 1–3 are **independent of the auth question**. Wave 4 (auth) is **no longer gated** —
§1.9.3 resolved it: both target clients attach custom headers to `/status` and `/ping`, so
**every endpoint requires the Cloudflare service token** and the only bypass is the
cover/image endpoints (§1.9.5).

**Wave 1 is complete and merged** (#2070, #2068, #2069 above; TASK-08 progress policy and
TASK-10 DRM are in PRs #2066/#2067 awaiting a flaky-timeout re-run of the coverage gate).

## Task index

| Task | Title | Effort | Tier |
|---|---|---|---|
| [TASK-01](TASK-01-syncitem-keyspace.md) | `sync_item` keyspace + 36-char UUID minting + reverse index | M | Sonnet |
| [TASK-02](TASK-02-syncfile-keyspace.md) | `sync_file` keyspace — durable per-file IDs for `ino` | M | Sonnet |
| [TASK-03](TASK-03-merge-follow-hook.md) | Merge-follow hook so IDs survive dedup merges | **L** | **Opus** |
| [TASK-04](TASK-04-syncid-backfill.md) | Idempotent sync-ID backfill over the existing library | M | Sonnet |
| [TASK-05](TASK-05-id-survival-tests.md) | ID-survival suite: move/rename/retag/merge/file-replace | M | Sonnet |
| [TASK-06](TASK-06-chapter-persistence.md) | Persisted `Chapter` type + Pebble keyspace + store methods | M | Sonnet |
| [TASK-07](TASK-07-scanner-chapter-hook.md) | Extract + persist chapters at scan time | M | Sonnet |
| [TASK-08](TASK-08-progress-merge-policy.md) | Pure progress-merge policy package | **L** | **Opus** |
| [TASK-09](TASK-09-bookmarks.md) | Bookmarks CRUD (no bookmark feature exists today) | M | Sonnet |
| [TASK-10](TASK-10-drm-detection.md) | DRM detection (AAX/AAXC) → unplayable-with-reason | S | Haiku |
| TASK-11 | ABS auth core — both credential modes | **L** | **Opus** |
| TASK-12 | Close identity gaps: `dedup.MergeBooks` (hard-delete), `CombineBooks`, untagged move | **L** | **Opus** |

## Non-negotiables for every task

- **File version headers** on every created/modified file; bump `version` + `last-edited`.
- **TDD**: failing test first, run it, confirm it fails for the right reason, then implement.
- **Conventional commits**; worktree + PR per task; **never commit to main**; rebase/FF only.
- **PebbleDB is the only production DB** — implement store methods fully on `PebbleStore`.
- **Whole-library loops must use bounded worker pools** (`registry.RunItems` /
  `errgroup` + `SetLimit`), per the CLAUDE.md concurrency rule. Never a bare
  `for range books` doing per-item I/O.
- **Add a `changelog.d/` fragment** (CI gate `changelog-check.yml`), one per PR, unique filename.
- Module path is `github.com/falkcorp/audiobook-organizer` (**falkcorp**, not jdfalk).
- Verify with real pasted output: `go test <pkg> -race -count=1`, `go vet`, `gofmt -l`.

## The five findings that most constrain this work

Each was caught by auditing real clients, and each is a silent failure if violated:

1. **`libraryItemId` must be a 36-char UUID.** Absorb splits IDs by fixed offset
   `substring(0,36)` in seven places; our Book ULIDs are 26 chars (§1.7.1).
2. **Never paginate `user.mediaProgress`.** AudioBooth *deletes* local progress rows
   absent from the server's list — a truncated list destroys listening positions on
   every home-screen refresh (§1.8.1). **Data loss.**
3. **`timeListened` (on `/sync`) is a DELTA to add; `timeListening` (on
   `/session/local*`) is a CUMULATIVE set.** Reading the wrong key silently records
   zero listening time (§1.8.4).
4. **Omitting `lastUpdate` (ms epoch) makes the server permanently lose every
   conflict** — clients compare it against their own wall clock (§1.7.3).
5. **An empty `audioTracks: []` is worse than omitting the key** — `[]` defeats
   AudioBooth's local-track fallback and kills playback of already-downloaded books
   (§1.8.5).
