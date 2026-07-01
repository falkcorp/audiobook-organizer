<!-- file: docs/agent-tasks/perf-cleanup/TASK-03-protected-path-cache.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a19781a-6f10-4a40-b8d6-b991464adfd5 -->
<!-- last-edited: 2026-07-01 -->

# TASK-03 — TTL-cache isProtectedPath / GetAllImportPaths at hot sites (MAYDEPLOY-H7)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-protected-path-cache" -b agent/pc-protected-path-cache origin/main
cd "$REPO/.worktrees/pc-protected-path-cache"
git rebase origin/main
```

## Goal

Two hot-path call sites re-fetch **every** configured import path on every
call to decide if a filesystem path is "protected" (i.e. should not be
deleted/moved by cleanup jobs): `(*Server).isProtectedPath` in
`internal/server/server_middleware.go` and the package-level `isProtectedPath`
helper in `internal/audiobooks/helpers.go`. `GetAllImportPaths()` has a memdb
fast path already, but there is still a full store round-trip + slice scan on
every call, and neither site caches the result at all. Add a small,
short-TTL in-process cache (design decision below) so repeated calls within a
few seconds reuse the same import-path list instead of hitting the store
every time.

**Scope decision — keep this simple:** import paths change *extremely*
infrequently (an admin action via the settings UI), so a **TTL-only cache**
(no explicit invalidation hook wired into `CreateImportPath` /
`UpdateImportPath` / `DeleteImportPath`) is the right tradeoff for this
effort size. Use a short TTL — **5 seconds** — so a worst case is a 5-second
staleness window after an admin edits import paths, which is acceptable for a
protected-path guard (it only widens or narrows the protected set slightly,
it does not silently disable protection for longer than the TTL). Do **not**
build a pub/sub invalidation mechanism across packages for this task — that
is out of scope for an S-effort cleanup.

## Background (verify before editing)

Re-run — line numbers drift:
```bash
grep -n "func (s \*Server) isProtectedPath\|GetAllImportPaths" internal/server/server_middleware.go
grep -n "func isProtectedPath\|GetAllImportPaths\|importPathLister" internal/audiobooks/helpers.go
grep -n "func.*GetAllImportPaths" internal/database/pebble_store.go internal/database/memdb_reads.go
```

At authoring time:
- `internal/server/server_middleware.go:88` — `func (s *Server)
  isProtectedPath(filePath string) bool` — calls `s.Store()` then
  `store.GetAllImportPaths()` (line ~93) every invocation.
- `internal/audiobooks/helpers.go:232-248` — `type importPathLister
  interface { GetAllImportPaths() ([]database.ImportPath, error) }` and
  `func isProtectedPath(store importPathLister, filePath string) bool` — same
  pattern, but takes the store as a parameter rather than a receiver.
- `internal/database/pebble_store.go:4008` — `func (p *PebbleStore)
  GetAllImportPaths() ([]ImportPath, error)` (memdb-backed fast path exists
  inside this function already — confirm with `grep -n
  "GetAllImportPaths_Pebble\|memdb" internal/database/pebble_store.go` around
  that function). This task adds a cache **above** this call, not inside it.
- These are two **separate packages** (`server` and `audiobooks`) with two
  separate `isProtectedPath` implementations — there is no shared type to
  extend without introducing a new shared package, which is out of scope.
  Implement **one small unexported TTL-cache struct per package** (duplicated
  ~15 lines of code is acceptable here; do not create a new shared package
  for this).

## Step-by-step

1. Re-run the grep commands to confirm anchors.
2. In `internal/server/server_middleware.go`, add a small TTL cache guarding
   the `GetAllImportPaths()` call inside `isProtectedPath`:
   ```go
   var (
       importPathCacheMu   sync.Mutex
       importPathCache     []database.ImportPath
       importPathCacheAt   time.Time
   )

   const importPathCacheTTL = 5 * time.Second

   func cachedImportPaths(store database.Store) ([]database.ImportPath, error) {
       importPathCacheMu.Lock()
       defer importPathCacheMu.Unlock()
       if time.Since(importPathCacheAt) < importPathCacheTTL {
           return importPathCache, nil
       }
       paths, err := store.GetAllImportPaths()
       if err != nil {
           return nil, err
       }
       importPathCache = paths
       importPathCacheAt = time.Now()
       return paths, nil
   }
   ```
   Add `"sync"` and `"time"` to the import block if not already present
   (`time` may already be imported elsewhere in the file — check first).
   Call `cachedImportPaths(store)` in place of `store.GetAllImportPaths()` at
   the existing call site inside `isProtectedPath`.
3. In `internal/audiobooks/helpers.go`, apply the equivalent pattern for the
   package-level `isProtectedPath(store importPathLister, filePath string)
   bool` function — a package-level `sync.Mutex` + cached slice + `time.Time`
   timestamp, gated the same way, wrapping the `store.GetAllImportPaths()`
   call at line ~248. Use a distinct variable/function name (e.g.
   `cachedImportPathsForHelper`) so it does not collide with the `server`
   package's version (they are different packages, so no actual name
   collision risk — just keep naming consistent/obvious per file).
4. Do **not** add invalidation hooks into `CreateImportPath` /
   `UpdateImportPath` / `DeleteImportPath` in
   `internal/database/pebble_store.go` — out of scope per the Scope decision
   above.
5. Bump file headers on both changed files.
6. Add tests:
   - `internal/server/server_middleware_test.go` (new, if it does not exist —
     check with `find . -iname server_middleware_test.go -not -path
     '*.worktrees*'`): using a mock/fake store, call `isProtectedPath` twice
     in quick succession and assert `GetAllImportPaths` was only invoked
     once (use a counting wrapper around `database.MockStore`, or check if
     `MockStore` already exposes a call counter — search
     `internal/database/mock_store.go` for existing counter fields before
     adding a new one). Then advance past the TTL (inject a fake clock if the
     codebase has one, or make the TTL a package var overridable in tests —
     e.g. `var importPathCacheTTL = 5 * time.Second` instead of a `const`, so
     tests can shrink it to a few milliseconds) and assert a third call
     re-fetches.
   - `internal/audiobooks/helpers_test.go` (check if it exists; if so, add to
     it, otherwise create it): same pattern for the package-level
     `isProtectedPath`.

## How to test

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/pc-protected-path-cache
go build ./...
go test ./internal/server/... -run TestIsProtectedPath -v -count=1
go test ./internal/audiobooks/... -run TestIsProtectedPath -v -count=1
go test ./internal/server/... ./internal/audiobooks/... -count=1
go vet ./internal/server/... ./internal/audiobooks/...
```

## Acceptance criteria
- [ ] `(*Server).isProtectedPath` in `server_middleware.go` uses a short-TTL cache instead of calling `GetAllImportPaths()` on every invocation.
- [ ] The package-level `isProtectedPath` in `internal/audiobooks/helpers.go` uses the equivalent cache.
- [ ] TTL is short (≤5s) and overridable in tests (not a hardcoded `const` that blocks test control).
- [ ] No invalidation hooks added to import-path mutation functions (explicitly out of scope).
- [ ] Tests prove repeated calls within the TTL window skip the store call, and calls after the TTL re-fetch.
- [ ] `go build`, `go test`, `go vet` green for both packages.
- [ ] File headers bumped.

## Commit message
```
perf(server,audiobooks): TTL-cache import-path lookups in isProtectedPath (MAYDEPLOY-H7)

Caches GetAllImportPaths() for a short TTL at the two isProtectedPath call
sites (server_middleware.go and audiobooks/helpers.go) so repeated protected-
path checks in a tight loop don't re-fetch and re-scan the import-path list
every call. Import paths change rarely, so a TTL-only cache (no explicit
invalidation) is the right tradeoff.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/pc-protected-path-cache
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "importPathCacheTTL\|importPathCache" internal/server/server_middleware.go internal/audiobooks/helpers.go` — if both present, this task is done. Rollback: revert the commit; behavior degrades gracefully to "always fresh" (today's behavior), no data risk.
