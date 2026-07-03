<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-20-hnsw-staleness-atomic-export.md -->
<!-- version: 1.0.0 -->
<!-- guid: d9bebcab-eae7-4162-9c13-f209b7248608 -->
<!-- last-edited: 2026-07-03 -->

# TASK-20 — HNSW snapshot staleness check, atomic export, wrap Graph.Delete

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Wave:** 2 · **Depends on:** TASK-06 (both touch `internal/server/server.go`; sequence to avoid a wave-2 rebase collision)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-20-hnsw-staleness-atomic-export" -b agent/cr-20-hnsw-staleness-atomic-export origin/main
cd "$REPO/.worktrees/cr-20-hnsw-staleness-atomic-export"
git rebase origin/main
```

## Goal

Close three related derived-index hardening gaps on the HNSW vector-ANN store
(source: `docs/consultancy/01-storage-architecture.md`, findings **ARCH-1**,
**ARCH-2**, **ARCH-5**):

- **ARCH-1 (High).** The HNSW snapshot fast-path skips Pebble hydration with
  **no staleness check**. After any unclean shutdown, every vector upserted
  since the last clean-shutdown export is silently missing from the graph,
  permanently, until a manual re-embed or snapshot delete — with zero error
  anywhere. Add a cheap comparison against the Pebble source of truth
  (`emb:` keyspace via `EmbeddingStore.CountByType`) and fall back to a full
  rebuild-by-hydration when the snapshot looks stale.
- **ARCH-2 (Medium).** `Export` writes `.bin`/`.meta.json` directly to their
  final paths (no temp+rename, no fsync) — a crash mid-export leaves a
  truncated snapshot. `Import` installs a graph into `s.graphs[entityType]`
  before its metadata sidecar is confirmed to parse; a meta failure leaves a
  graph installed with no metadata, and `FindSimilar`'s metadata filter then
  silently rejects every node. Make both operations all-or-nothing.
- **ARCH-5 (Low).** `Graph.Delete` is called bare, unlike `Graph.Add` which
  was wrapped in `safeAdd` (PR #1741) specifically because coder/hnsw v0.6.1
  has documented Delete+Add per-layer-invariant panics
  (`HNSW-CRASH-2026-06-18`, see `server.go:436-438`). `Delete` runs on live
  paths (book merge/removal) from goroutines where an unrecovered panic kills
  the process. Add a `safeDelete` mirroring `safeAdd`.

## Background (verify before editing)

All three findings live in `internal/database/hnsw_embedding_store.go`, with
two call sites in `internal/server/server.go` and `internal/dedup/lifecycle.go`.
**Line numbers below are from this brief's authoring pass (2026-07-03) and
may have drifted — re-verify with the greps in each subsection before
editing.**

### ARCH-1 — staleness check

- `HNSWEmbeddingStore.Import` (verified at `hnsw_embedding_store.go:372-430`)
  loads `.bin`/`.meta.json` files from disk into `s.graphs[entityType]` with
  **no comparison to the Pebble source of truth**. It already has one
  discard path — a dimension mismatch (`g.Dims() != s.dims`) causes it to
  `continue` and skip that entity type (lines ~406-410) — but nothing checks
  vector *count*.
- The caller, `internal/server/server.go` (verified at lines ~439-453),
  calls `hnswStore.Import(hnswDir)` **before** `regContainer.Build`'s
  `dedup.Engine.PostInit` runs (comment at server.go:434-438 explains why:
  reordering caused a documented crash loop, HNSW-CRASH-2026-06-18 — do
  **not** reorder these two calls).
- `internal/dedup/lifecycle.go`'s `PostInit` (verified at lines ~85-134, the
  relevant block ~115-131) then checks
  `chromemStore.CountByType(ctx, "book")` — if `>0` it sets
  `alreadyPopulated = true` and **skips hydration entirely**
  (`// Snapshot was loaded — nothing to hydrate.`). This is the exact skip
  path the finding describes: if the snapshot is stale (missing vectors
  upserted after the last clean-shutdown export), this skip permanently
  strands dedup Layer 2 in a degraded state.
- The Pebble source of truth's own count is `EmbeddingStore.CountByType`
  (verified at `internal/database/embedding_store.go:369`) — a cheap
  Pebble-side count already used elsewhere. This is a **different type**
  (`*database.EmbeddingStore`, the Pebble-backed store) from
  `HNSWEmbeddingStore.CountByType` (`hnsw_embedding_store.go:276-284`, the
  in-memory graph count) — do not conflate them.
- **Re-verify:**
  ```bash
  grep -n "func (s \*HNSWEmbeddingStore) Import\|func (s \*HNSWEmbeddingStore) CountByType\|Dims() != s.dims" internal/database/hnsw_embedding_store.go
  grep -n "func (s \*EmbeddingStore) CountByType" internal/database/embedding_store.go
  grep -n "hnswStore.Import\|HNSW-CRASH-2026-06-18" internal/server/server.go
  grep -n "CountByType(ctx, \"book\")\|alreadyPopulated" internal/dedup/lifecycle.go
  ```

### ARCH-2 — atomic export/import

- `Export` (verified at `hnsw_embedding_store.go:333-368`) does
  `os.Create(binPath)` and `os.WriteFile(metaPath, ...)` directly to their
  final names inside a loop over `s.graphs` — a crash between the `.bin`
  write and the `.meta.json` write (or mid-write of either) leaves a
  truncated/mismatched pair on disk for that entity type.
- `Import` (verified at `hnsw_embedding_store.go:372-430`) assigns
  `s.graphs[entityType] = g` (line ~411) **before** reading/unmarshalling
  the `.meta.json` sidecar (lines ~413-426). If the meta read/unmarshal
  returns a non-`os.IsNotExist` error, `Import` returns an error to its
  caller — but `s.graphs[entityType]` was already mutated in place (no
  rollback), so the store is left in a partial state for that entity type,
  and because the caller only logs a warning on error (server.go:446), the
  process continues running with that corrupted graph.
- **Re-verify:**
  ```bash
  grep -n "func (s \*HNSWEmbeddingStore) Export\|func (s \*HNSWEmbeddingStore) Import" internal/database/hnsw_embedding_store.go
  sed -n '333,430p' internal/database/hnsw_embedding_store.go
  ```

### ARCH-5 — wrap Graph.Delete

- `HNSWEmbeddingStore.Delete` (verified at `hnsw_embedding_store.go:189-199`)
  calls `g.Delete(entityID)` bare — no recover. `hnsw.Graph.Delete` (coder/hnsw
  v0.6.1, `graph.go:460`, `func (h *Graph[K]) Delete(key K) bool`) shares the
  same per-layer-invariant class of bug as `Add` (see `safeAdd`'s comment,
  `hnsw_embedding_store.go:143-150`, and `server.go:436-438`'s
  HNSW-CRASH-2026-06-18 note).
- `safeAdd` (verified at `hnsw_embedding_store.go:163-171`) is the pattern to
  mirror: recover any panic from the library call and turn it into an
  `error` return, never letting it escape.
- `hnsw_panic_safe_test.go` (verified, root of `internal/database/`) is the
  existing regression-test pattern for `safeAdd` (repeated re-insert must not
  crash the process) — add a companion test for `safeDelete` alongside it.
- **Re-verify:**
  ```bash
  grep -n "func (s \*HNSWEmbeddingStore) Delete\|func (s \*HNSWEmbeddingStore) safeAdd\|g.Delete(entityID)" internal/database/hnsw_embedding_store.go
  grep -n "func.*Delete" "$(go env GOPATH)/pkg/mod/github.com/coder/hnsw@v0.6.1/graph.go"
  ```

## Step-by-step

1. **ARCH-5 first (smallest, unblocks nothing else — do it first to warm up
   on the file):**
   - Add `safeDelete(g *hnsw.Graph[string], entityID string) (deleted bool, err error)`
     next to `safeAdd`, recovering any panic into `err` exactly like `safeAdd`
     does, and returning `g.Delete(entityID)`'s bool result on the
     non-panicking path.
   - In `Delete`, replace the bare `g.Delete(entityID)` call with
     `safeDelete`, logging at `slog.Warn` (mirroring the log shape used
     elsewhere in this file, e.g. the Import dimension-mismatch warning) if
     an error is returned, and still proceeding to delete the metadata
     sidecar entry regardless (deletion must remain best-effort, matching
     the file's "single bad mirror cannot abort" philosophy documented on
     `safeAdd`). `Delete`'s exported signature (`error` return) does not need
     to change — a recovered panic should be logged, not propagated as a
     hard error, to match `safeAdd`'s "never crash the caller" contract.
   - Add a test in `internal/database/hnsw_panic_safe_test.go` (or a new
     adjacent file) that deletes a key that was never added (and/or deletes
     the same key twice) and asserts no panic escapes.

2. **ARCH-2 — atomic Export:**
   - In `Export`, for each entity type write `.bin` to
     `binPath+".tmp"` and `.meta.json` to `metaPath+".tmp"` first (still via
     `os.Create`/`os.WriteFile`), `f.Sync()` the `.bin` file handle before
     `f.Close()`, then `os.Rename(tmpPath, finalPath)` for each of the two
     files only after both writes for that entity type have succeeded. If
     any step fails, return the error before renaming anything for that
     entity type (leave the previously-committed snapshot, if any, intact —
     do not delete a good existing snapshot just because a new export
     failed).

3. **ARCH-2 — atomic Import:**
   - Change `Import` to build into **local** maps
     (`newGraphs := map[string]*hnsw.Graph[string]{}`,
     `newMeta := map[string]map[string]map[string]string{}`) instead of
     writing directly into `s.graphs`/`s.meta` inside the loop. Keep the
     existing dimension-mismatch discard behavior (skip that entity type,
     it's not a hard failure), but any **hard** error (bin open/parse
     failure, meta unmarshal failure that isn't "file not found") aborts the
     whole `Import` call — return the error without touching `s.graphs`/
     `s.meta` at all, so the caller's existing hydration-fallback path
     (`dedup/lifecycle.go`'s `alreadyPopulated` check seeing `CountByType==0`)
     actually runs, per the ARCH-2 recommendation. Only after every entity
     type in the loop parses successfully (or is legitimately skipped for
     dimension mismatch), commit: `s.graphs = newGraphs; s.meta = newMeta`
     under `s.mu.Lock()` (which is already held for the duration of `Import`).

4. **ARCH-1 — staleness check:**
   - Give `HNSWEmbeddingStore.Import` (or its caller in `server.go`) access to
     a Pebble-side truth count. The cleanest seam: change `Import`'s
     signature to accept an optional truth-count function
     `truthCount func(entityType string) (int, error)` (nil-safe — pass
     `nil` from any test/caller that doesn't have a Pebble store handy), OR
     add a second method `ImportWithStalenessCheck(dir string, truth
     func(entityType string) (int, error)) error` that wraps `Import` and
     performs the check, to avoid changing the existing `Import` signature
     used elsewhere. Prefer the wrapping-method approach — smaller, additive,
     matches this codebase's "additive guard" convention (see
     `dedup-hardening/TASK-01` for the established style of adding a guard
     without touching an existing function's signature).
   - In `server.go`'s HNSW-load block (~lines 439-453), after a successful
     `hnswStore.Import(hnswDir)`, for each entity type compare
     `hnswStore.CountByType(ctx, entityType)` (in-memory graph count) against
     `embeddingStore.CountByType(entityType)` (Pebble truth count, resolved
     from the registry the same way `chromemstore` is resolved). Use "book"
     as the entity type to check (the type dedup Layer 2 cares about,
     matching what `dedup/lifecycle.go` already checks). If the graph count
     is more than a small tolerance below the truth count (e.g. graph count
     `< truthCount` at all, since any undercounting is unsafe — no positive
     tolerance is safe here per the finding's "silently misses duplicates"
     risk), treat the snapshot as stale: drop the imported HNSW state for
     that entity type (or all entity types, simplest) so `dedup/lifecycle.go`'s
     `alreadyPopulated` check sees `CountByType==0` and the existing
     hydration-from-Pebble path runs normally. Do not invent a new hydration
     mechanism — reuse the one that already exists and already runs when
     `CountByType==0`.
   - Log at `slog.Warn` when discarding a stale snapshot, including both
     counts, mirroring the existing dimension-mismatch warning's log-field
     shape (`entity_type`, plus the two counts).
   - Keep this check cheap: both `CountByType` calls are O(1)/map-lookup-ish
     (verified: HNSW's is `g.Len()`, Pebble's is a stored aggregate per the
     citation `embedding_store.go:369`) — no full scans.

5. Bump the file header (version bump + `last-edited`) on every file touched,
   per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/database/... -run HNSW -count=1 -v
go test ./internal/database/... -count=1
go test ./internal/dedup/... -count=1
go test ./internal/server/... -count=1
go vet ./internal/database/... ./internal/dedup/... ./internal/server/...
```

Add/extend tests to cover:
- `safeDelete` on an absent key and a double-delete do not panic.
- `Export` followed by a simulated interrupted write (e.g. by making one of
  the two files fail to write) leaves any prior good snapshot for that
  entity type untouched (no partial overwrite of the final `.bin`/`.meta.json`).
- `Import` on a directory with a valid `.bin` but a corrupt/truncated
  `.meta.json` for one entity type returns an error and leaves `s.graphs`/
  `s.meta` at their pre-call state (nothing partially installed).
- A staleness scenario: HNSW graph count lower than the Pebble truth count
  causes the imported snapshot to be discarded and hydration to proceed
  (assert via the same "alreadyPopulated stays false / CountByType==0"
  behavior `dedup/lifecycle.go` already relies on — an integration-style
  test at the `HNSWEmbeddingStore` + `EmbeddingStore` boundary, not a full
  server boot).

## Acceptance criteria

- [ ] `HNSWEmbeddingStore.Delete` no longer calls `g.Delete` bare; a
      `safeDelete` helper mirroring `safeAdd` recovers any library panic,
      and a regression test proves a delete-of-absent-key / double-delete
      does not crash the process.
- [ ] `Export` writes via temp file + rename (with fsync on the `.bin`
      handle) so a crash mid-export cannot leave a truncated file at the
      final path, and does not clobber a prior good snapshot on failure.
- [ ] `Import` is all-or-nothing per call: any hard failure (not the
      existing dimension-mismatch discard) leaves `s.graphs`/`s.meta`
      completely untouched, so the caller's existing hydrate-from-Pebble
      fallback actually runs.
- [ ] After HNSW snapshot import, a staleness check compares the graph's
      "book" count against the Pebble `emb:` truth count
      (`EmbeddingStore.CountByType`) and discards the imported state
      (triggering the existing hydration path) when the graph undercounts
      the truth.
- [ ] The existing dimension-mismatch discard behavior in `Import` is
      unchanged in effect (still skips/rebuilds that entity type at the new
      dimension).
- [ ] The existing HNSW-CRASH-2026-06-18 ordering constraint (Import before
      PostInit) in `server.go` is preserved — do not reorder those calls.
- [ ] `go build ./...`, the targeted `go test` runs, and `go vet` are green.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(database): harden HNSW derived index (staleness check, atomic export/import, safe Delete)

The HNSW snapshot fast-path skipped Pebble hydration with no staleness check
(ARCH-1), so any unclean shutdown silently stranded dedup Layer 2 on a graph
missing every vector upserted since the last clean export. Export wrote
directly to final paths and Import could install a partial graph on a meta
read failure (ARCH-2). Graph.Delete ran unwrapped despite the same
Delete+Add per-layer-invariant panics that safeAdd (#1741) already contains
for Add (ARCH-5). Add a truth-count staleness check against
EmbeddingStore.CountByType, make Export/Import atomic (temp+rename, build-
then-swap), and add safeDelete mirroring safeAdd.

Co-Authored-By: Claude <model> <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-20-hnsw-staleness-atomic-export
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Before starting, re-run the verification greps above. If they show:
- `Delete` already calls a `safeDelete`/recover-wrapped helper — ARCH-5 is
  done; skip step 1.
- `Export` already writes to a `.tmp` path and renames — ARCH-2's export half
  is done; skip step 2.
- `Import` already builds into local maps before committing to
  `s.graphs`/`s.meta` — ARCH-2's import half is done; skip step 3.
- `server.go`'s HNSW-load block already compares an HNSW count against a
  Pebble/`EmbeddingStore` truth count and discards on mismatch — ARCH-1 is
  done; skip step 4.

If all four are already present, this task is fully done — verify with:
```bash
grep -n "safeDelete\|\.tmp\"\|newGraphs\|truthCount\|CountByType" internal/database/hnsw_embedding_store.go internal/server/server.go
```
and report the finding as pre-fixed rather than duplicating work.

Rollback = revert the commit. `safeAdd`, the existing dimension-mismatch
discard path, and the HNSW-CRASH-2026-06-18 Import-before-PostInit ordering
in `server.go` are untouched by this change and remain in effect regardless
of rollback.
