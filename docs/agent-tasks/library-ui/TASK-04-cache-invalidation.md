<!-- file: docs/agent-tasks/library-ui/TASK-04-cache-invalidation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 953b5506-8cb5-4468-9e39-32375cebd3ad -->
<!-- last-edited: 2026-07-01 -->

# TASK-04 — Clear `useLibraryCache` on all mutation handlers in Library.tsx (library-cache-bug)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/lu-cache-invalidation" -b agent/lu-cache-invalidation origin/main
cd "$REPO/.worktrees/lu-cache-invalidation"
git rebase origin/main
```

## Goal

Close a stale-data bug: `web/src/stores/useLibraryCache.ts` caches Library
pages for 60 seconds (`CACHE_TTL_MS = 1 * 60 * 1000`). `useLibraryQuery`'s
`loadAudiobooks` checks this cache before every fetch and serves a cache hit
as-is. Only two mutation handlers in `web/src/pages/Library.tsx`
(`handleMergeAsVersions`, `handleCombineIntoOneBook`) call
`clearLibraryCache()` before reloading. Roughly a dozen other mutation
handlers call `loadAudiobooks()` right after a mutation **without** clearing
the cache first, so a page that was cached before the mutation can be served
stale (e.g. purged/restored/reorganized/re-metadata'd books still shown in
their pre-mutation state) for up to 60 seconds after the action completes.

## Background (verify before editing)

- `web/src/stores/useLibraryCache.ts` — a zustand store, 60s TTL
  (`CACHE_TTL_MS = 1 * 60 * 1000`), keyed cache of `{ audiobooks, totalCount,
  totalPages, importPaths, timestamp }`.
- `web/src/hooks/useLibraryQuery.ts` — `loadAudiobooks` reads from this cache
  before hitting the API; a hit is returned as-is (no revalidation). The hook
  also exposes `clearLibraryCache`, documented in the file at the comment
  directly above its definition:
  ```
  // clearLibraryCache drops every cached page. Call before loadAudiobooks()
  // after any mutation that hard/soft-deletes, merges, or combines books —
  // otherwise the next reload can serve a stale cached page (same
  // page/itemsPerPage/search/filter/sort key) that still lists books which
  // no longer exist, until the cache's own TTL expires on its own.
  const clearLibraryCache = useCallback(() => {
    useLibraryCache.getState().clear();
  }, []);
  ```
  — this comment already states the intended contract; the bug is that it
  isn't followed consistently in `Library.tsx`.
- `web/src/pages/Library.tsx` currently calls `clearLibraryCache()` in exactly
  two places, right before `loadAudiobooks()`:
  - `handleMergeAsVersions` (around line 895–896 as of this writing)
  - `handleCombineIntoOneBook` (around line 928–929 as of this writing)
- The following handlers call `loadAudiobooks()` (or `await loadAudiobooks()`)
  after performing a mutation, but do **not** call `clearLibraryCache()`
  first (re-verify exact line numbers with the grep below — this list is a
  starting point, not a guarantee of completeness):
  - `handleConfirmDelete`
  - `handleBatchDelete`
  - `handleBatchRestore`
  - `handlePurgeOne`
  - `handleRestoreOne`
  - `handleConfirmPurge`
  - `handleBulkOrganize`
  - `handleOrganizeRollback`
  - `handleVersionUpdate`
  - `handleFetchMetadata`
  - `handleBulkFetchMetadata`
  - `handleParseWithAI`
  - `handleImportFile` (two call sites)
  - any other handler that calls `loadAudiobooks()` after a mutating API call
    — the grep below finds every call site so you can audit the full set.

- **Re-verify every call site before editing** — line numbers drift:
  ```bash
  grep -n "const handle[A-Za-z]* = \|loadAudiobooks()\|clearLibraryCache()" web/src/pages/Library.tsx
  ```
  For each `loadAudiobooks()` call site found, look at the enclosing handler
  and classify it:
  - **Mutating** (calls a POST/PUT/PATCH/DELETE API endpoint, or otherwise
    changes book state on the server) → needs `clearLibraryCache()` before
    the reload if it doesn't already have one.
  - **Non-mutating** (e.g. a poll/refresh, a filter change, initial load,
    pagination) → does NOT need `clearLibraryCache()` — do not add it
    reflexively to every call site, only to handlers that follow a mutation.

## Step-by-step

1. Run the grep above and build a complete, current list of every
   `loadAudiobooks()` call site in `web/src/pages/Library.tsx` and which
   handler encloses it.
2. For each handler that performs a mutating API call and then calls
   `loadAudiobooks()` (or `await loadAudiobooks()`) without already calling
   `clearLibraryCache()` first, add `clearLibraryCache();` immediately before
   that `loadAudiobooks()` call — matching the existing pattern used in
   `handleMergeAsVersions` / `handleCombineIntoOneBook` exactly (call
   `clearLibraryCache()` synchronously, then `await loadAudiobooks()` on the
   next line).
3. Do not add `clearLibraryCache()` to non-mutating reload/poll call sites
   (e.g. the periodic active-scan-op poll in `useLibraryQuery.ts`, or a plain
   filter/page-change reload) — that would defeat the purpose of the cache.
4. `clearLibraryCache` is already destructured from `useLibraryQuery()` in
   `Library.tsx` (verify with `grep -n "clearLibraryCache" web/src/pages/Library.tsx`)
   — you should not need to add a new import or hook call, only additional
   call sites.
5. Add a test that reproduces the bug and proves the fix: pick one previously
   unguarded handler (e.g. `handlePurgeOne` or `handleBatchDelete`) and, in
   the relevant Library test file (check for an existing
   `Library.*.test.tsx` that already exercises that handler, e.g. a purge/
   delete flow test — extend it rather than creating a new file if one
   exists), assert that after the mutation, a subsequent `loadAudiobooks()`
   does NOT return data from `useLibraryCache` (i.e. the cache was cleared).
   This can be done by:
   - Pre-populating `useLibraryCache` with a stale entry for the current
     query key before triggering the handler, then asserting the store's
     cache map no longer contains that key (or that the mocked API list-call
     was invoked again) after the handler completes.
6. Bump the file header on every file you touch (version bump + `last-edited`
   date) per `.standards/instructions/file-headers.md`. Note:
   `useLibraryCache.ts`'s current header has no `last-edited` line — if you
   touch that file, bring its header up to the standard 4-line format.

## How to test

```bash
cd web && npm install && npm run build && npm test
```

## Acceptance criteria

- [ ] Every handler in `Library.tsx` that performs a mutating API call and
      then reloads via `loadAudiobooks()` also calls `clearLibraryCache()`
      immediately beforehand.
- [ ] Non-mutating reload/poll call sites (filter/page changes, the
      active-scan-op poll) are left unchanged — no unnecessary cache clears.
- [ ] `handleMergeAsVersions` and `handleCombineIntoOneBook` are unchanged
      (already correct).
- [ ] A new or extended test proves at least one previously-unguarded handler
      (e.g. purge, restore, or batch-delete) no longer serves a stale cached
      page after the mutation.
- [ ] `npm run build` and `npm test` are green.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(library): clear library cache on all mutating handlers (library-cache-bug)

useLibraryCache serves a 60s-TTL cached page as-is on every hit, but only
handleMergeAsVersions and handleCombineIntoOneBook cleared it before reload.
A dozen other mutation handlers (purge, restore, batch-delete, organize,
metadata fetch/parse, import) reloaded without clearing the cache, so a
cached page could serve stale rows for up to 60s after the mutation. Call
clearLibraryCache() before loadAudiobooks() in every mutation handler.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/lu-cache-invalidation
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If every mutating handler in `Library.tsx` already calls `clearLibraryCache()`
before its `loadAudiobooks()` call, this task is done — verify with the grep
in the Background section and manually confirm each mutating handler pairs
the two calls. Rollback = revert the commit; `useLibraryCache`'s TTL-based
expiry (60s) remains as a fallback safety net even without this fix, so
reverting only restores the up-to-60s staleness window, not permanent data
loss.
