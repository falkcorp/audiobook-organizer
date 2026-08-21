<!-- file: docs/agent-tasks/todo-completion/database/TASK-033-lock-the-three-bare-globalstore-accesses-in-init.md -->
<!-- version: 1.0.0 -->
<!-- guid: 846de2f8-a75c-4ebe-9b2c-95ffb926da16 -->
<!-- last-edited: 2026-08-21 -->

# TASK-033 — Lock the three bare globalStore accesses in InitializeStore/CloseStore (TODO.md L4678)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Why:** Mechanical: swap 3 bare accesses for the existing locked setter/mutex, delete the sleep workaround. No new types or design. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4678 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/database/store.go` — `globalStore` is gu" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-033-lock-the-three-bare-globalstore-accesses-in-init" -b agent/database-033-lock-the-three-bare-globalstore-accesses-in-init origin/main
cd "$REPO/.worktrees/database-033-lock-the-three-bare-globalstore-accesses-in-init"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make InitializeStore and CloseStore synchronize every globalStore read/write through globalStoreMu (matching GetGlobalStore/SetGlobalStore), and delete the time.Sleep(100ms) race workaround in CloseStore once the nil-out is properly locked against concurrent GetGlobalStore readers.

## Background (verify before editing)

- globalStoreMu sync.RWMutex guards globalStore (internal/database/store.go:1279-1280).
- GetGlobalStore (:1283-1289) and SetGlobalStore (:1291-1295) already lock/unlock correctly.
- InitializeStore writes `globalStore = s` at :1323 with no lock.
- CloseStore reads `store := globalStore` (:1337) and writes `globalStore = nil` (:1338) with no lock, then does `time.Sleep(100 * time.Millisecond)` at :1342, commented as a 'brief pause to let in-flight goroutines notice the nil' — a race workaround, not a fix.
- Blast radius is test-only today: every non-comment, non-test call to GetGlobalStore() is inside a _test.go file (internal/merge/service_test.go, internal/audiobooks/audiobook_service_tags_test.go, internal/itunes/service/*_test.go, internal/testutil/integration.go's SetGlobalStore calls); every non-test hit elsewhere is a comment describing why that path now avoids the global.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "var globalStore Store\|var globalStoreMu" internal/database/store.go   # 2 hits at :1279-1280 — globalStore/globalStoreMu declared
  grep -n "globalStore = s$" internal/database/store.go   # 1 hit ~L1323 — InitializeStore writes globalStore bare
  grep -n "store := globalStore\|globalStore = nil\|time.Sleep(100" internal/database/store.go   # 3 hits ~L1337-1342 — CloseStore reads+nils globalStore bare then sleeps as a race workaround
  grep -n "globalStoreMu.RLock\|globalStoreMu.Lock" internal/database/store.go   # 2 hits ~L1284,1292 — GetGlobalStore/SetGlobalStore already lock correctly
  grep -rn "GetGlobalStore()" --include="*.go" . | grep -v _test.go | grep -v '//' | grep -v 'func GetGlobalStore'   # 0 hits (excluding the func GetGlobalStore() definition itself, which also matches the bare literal text) — no production (non-test, non-comment, non-definition) caller of GetGlobalStore() exists today
  ```

### Reuse — don't invent

- Use `globalStoreMu sync.RWMutex + SetGlobalStore` in `internal/database/store.go` (verify: `grep -n "func SetGlobalStore" internal/database/store.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/database/store.go.
2. In InitializeStore (~L1306-1331): replace the bare `globalStore = s` at line 1323 with a call to the existing `SetGlobalStore(s)`.
3. In CloseStore (~L1334-1346): replace the unlocked `store := globalStore` / `globalStore = nil` pair (lines 1337-1338) with `globalStoreMu.Lock(); store := globalStore; globalStore = nil; globalStoreMu.Unlock()`.
4. Delete the `time.Sleep(100 * time.Millisecond)` call at line 1342 and its 'brief pause' comment — no longer needed once the nil-out is mutex-protected against GetGlobalStore's RLock.
5. If `time` becomes unused in store.go after removing the Sleep call, remove the import.
6. Bump the file's version header (file-headers.md) and last-edited date.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_033.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- CloseStore called when globalStore is already nil: the locked local `store` var is nil, function returns nil without calling Close() — preserve this early-return semantics inside the critical section.
- InitializeStore called twice without an intervening CloseStore: SetGlobalStore simply overwrites, same as today — no leak detection added.

## Tests

- New test internal/database/store_global_test.go: TestGlobalStoreConcurrentAccess — under `go test -race`, spawn goroutines calling InitializeStore-style SetGlobalStore, CloseStore-style clear, and GetGlobalStore concurrently; assert -race reports nothing.
- Existing InitializeStore/CloseStore callers (grep -rn "InitializeStore(\|CloseStore(" --include="*_test.go" .) must keep passing unmodified — this is a behavior-preserving locking fix, not a semantic change.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go build ./internal/database/... exits 0.
- [ ] go test -race ./internal/database/... exits 0, including the new concurrency test.
- [ ] grep -n "globalStore = \|:= globalStore" internal/database/store.go shows the only remaining bare accesses are inside GetGlobalStore/SetGlobalStore, which already hold the lock at that point.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_033.md`.

## Commit message

```
refactor(database): Lock the three bare globalStore accesses in InitializeStore/ (TODO L4678)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Preventive hardening, matching the item's own framing: 'production-critical the moment a GetGlobalStore() call is reintroduced' into a hot path. No known live incident.
