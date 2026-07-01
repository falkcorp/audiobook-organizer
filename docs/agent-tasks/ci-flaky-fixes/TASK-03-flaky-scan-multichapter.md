<!-- file: docs/agent-tasks/ci-flaky-fixes/TASK-03-flaky-scan-multichapter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 46d8d5df-576d-48c9-a9a2-86160f17b2a8 -->
<!-- last-edited: 2026-07-01 -->

# TASK-03 — Root-cause + fix TestScanService_MultiChapterAudiobook (flaky-scan)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none (but should rebase onto `origin/main` AFTER TASK-01 merges — see workstream README collision note; do not run concurrently with TASK-01).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cf-flaky-scan-multichapter" -b agent/cf-flaky-scan-multichapter origin/main
cd "$REPO/.worktrees/cf-flaky-scan-multichapter"
git rebase origin/main
```

## Goal

`TestScanService_MultiChapterAudiobook` is flaky/environment-sensitive — it has
failed on `main`, not just on feature branches. In isolation it passes
consistently on a dev machine (verified: `go test -run TestScanService_MultiChapterAudiobook -count=30` all green). That means the flake only shows up
under different conditions than a solo run — most likely running as part of
the FULL test suite/CI (different machine load, possibly `ffprobe` absent,
concurrency from other tests/goroutines touching shared package-level state).
Diagnose the actual root cause and fix it deterministically; do not silence it
with a retry, a longer sleep, or `t.Skip`.

## Background (verify before editing)

- Locate the test and re-confirm the line numbers below (they drift):
  ```bash
  grep -n "func TestScanService_MultiChapterAudiobook" -A 25 internal/server/scan_edge_cases_test.go
  ```
  As of this writing it is at `internal/server/scan_edge_cases_test.go:180-201`.
  It creates 5 FAKE audio files (`env.CreateFakeAudiobook`, which writes plain
  text bytes `"fake-audiobook-data-..."` with an `.mp3` extension — see
  `internal/testutil/integration.go` around line 110, `CreateFakeAudiobook`),
  runs `scanner.NewScanService(env.Store).PerformScan(...)` against the
  directory containing all 5 files, and asserts exactly 1 book results
  ("all chapter files under one directory should be one book").
- Trace the scan path to find the concurrency/environment-sensitive parts:
  ```bash
  grep -n "func (ss \*ScanService) performScanInternal\|func (ss \*ScanService) scanFolder" internal/scanner/service.go
  grep -n "func ScanDirectoryParallel\|func ProcessBooksParallel\|func groupFilesIntoBooks" internal/scanner/scanner.go
  ```
  Key facts as of this writing:
  - `scanFolder` (`internal/scanner/service.go`, ~line 261) reads
    `workers := config.AppConfig.ConcurrentScans` (default 4) and calls
    `ScanDirectoryParallel(folderPath, workers, ...)`.
  - `ScanDirectoryParallel` (`internal/scanner/scanner.go`, ~line 351) walks
    all directories, then spawns ONE GOROUTINE PER DIRECTORY (bounded by a
    `workers`-sized semaphore) to read that directory's files and call
    `groupFilesIntoBooks(audioFiles)` on them, merging results into a shared
    `books` slice under a `sync.Mutex`. For this specific test there is only
    ONE leaf directory (`bookDir`) containing all 5 chapter files, so the
    per-directory grouping itself is NOT racing against another directory —
    but confirm this by re-reading `groupFilesIntoBooks` to see whether IT
    internally does anything concurrent or depends on file mtime/order:
    ```bash
    grep -n "func groupFilesIntoBooks" -A 60 internal/scanner/scanner.go
    ```
  - `PerformScan`/`performScanInternal` (`internal/scanner/service.go`) also
    installs PACKAGE-LEVEL global mutable state before scanning and clears it
    via `defer` afterward: `SetScanCache(scanCache)` / `defer ClearScanCache()`
    and `InitWorksLookupCache()` / `defer ClearWorksLookupCache()`. These are
    process-global package vars (search `internal/scanner/` for their
    definitions with `grep -rn "func SetScanCache\|func InitWorksLookupCache\|var activeScanner"`).
    If ANY other test in the same test binary (a) runs concurrently
    (check for `t.Parallel()` — currently none in this package, confirm with
    `grep -rn "t.Parallel()" internal/server/*_test.go internal/scanner/*_test.go`)
    or (b) leaves a background goroutine alive past its own cleanup that still
    touches these globals (e.g. a prior test's scan/registry goroutine that
    hasn't fully drained when this test starts), the shared cache/lookup state
    can be stomped mid-scan, producing an inconsistent grouping result.
  - Also check `internal/testutil/integration.go`'s `SetupIntegration` cleanup
    (around line 90) — it clears scan hooks and closes the store, but confirm
    it actually WAITS for any in-flight scan goroutines / registry workers to
    fully drain before returning, rather than just closing the store
    underneath them (a goroutine still writing after `store.Close()` would
    error/panic rather than silently corrupt state, so also check test logs
    for a swallowed error from a background goroutine around scan time).
- Reproduce under conditions closer to CI: run the FULL package suite
  repeatedly (not just this test in isolation) and with `-race`, since a
  scheduling-dependent global-state issue is much more likely to surface
  under load / with the race detector:
  ```bash
  go test ./internal/server/... -race -count=5 -v 2>&1 | tee /tmp/scan-flake.log
  go test ./internal/scanner/... -race -count=5 -v 2>&1 | tee -a /tmp/scan-flake.log
  go test ./... -race -count=3 2>&1 | tee -a /tmp/scan-flake.log
  grep -n "FAIL\|DATA RACE" /tmp/scan-flake.log
  ```
  If `-race` flags a concrete data race touching `ScanCache`, the works-lookup
  cache, or `activeScanner`, that IS the root cause — fix it by scoping that
  state per-scan (pass it through the call chain / a struct) instead of a
  package-level var, or by adding proper synchronization if scoping isn't
  feasible in this change's blast radius.
  If `-race` finds nothing but the test still occasionally reports the wrong
  book count, check whether `groupFilesIntoBooks` has any ordering dependency
  on `os.ReadDir`'s returned order (which is filesystem-dependent, not
  guaranteed sorted on every OS) combined with a control-flow bug (e.g. an
  early return / off-by-one only triggered by a specific file ordering) —
  reproduce by manually sorting/reversing the input file list in a focused
  unit test against `groupFilesIntoBooks` directly.

## Step-by-step

1. Reproduce first (see Background) — do NOT write a fix before you have a
   concrete, repeatable failure mode (a race-detector hit, an ordering-
   dependent bug reproduced via a focused table test, or evidence that
   `ffprobe`/`ffmpeg` availability changes behavior). Record what you found in
   your PR description.
2. If it's a data race on package-level scanner state (`ScanCache`,
   works-lookup cache, `activeScanner`): fix the actual race — thread the
   cache/lookup data through function parameters or a per-scan struct instead
   of a shared package var, or guard remaining unavoidable shared state with a
   mutex. Keep the change minimal and scoped to what the race detector
   actually flagged; don't refactor unrelated scanner internals.
3. If it's an `os.ReadDir` ordering dependency in `groupFilesIntoBooks`: make
   grouping order-independent (sort the input file list before grouping, or
   fix the specific control-flow bug that depends on order) rather than
   depending on filesystem iteration order.
4. If `ffprobe`/`ffmpeg` availability changes the grouping outcome for these
   fake (non-real-audio) files: make the metadata-extraction failure path
   deterministic (always fall back the same way when tag/probe reads fail),
   and add an explicit fallback-path assertion or comment in the test noting
   the fake files intentionally have no real audio metadata.
5. Whatever the root cause, add or extend a regression check that would catch
   a recurrence — either strengthen this test's assertions or add a narrow
   unit test on `groupFilesIntoBooks` (or the affected function) with fixed
   inputs.
6. Bump the file header on every file you touch (test file(s) and/or
   `internal/scanner/service.go` / `internal/scanner/scanner.go`).

## How to test

```bash
go test ./internal/server/... -run TestScanService_MultiChapterAudiobook -count=20 -v
go test ./internal/server/... -race -count=5
go test ./internal/scanner/... -race -count=5
go build ./...
go vet ./...
```
All must pass, including the `-race` runs (this is the load-bearing check —
the isolated `-count=20` run alone already passed before your fix, per the
investigation above, so it does not by itself prove the fix).

## Acceptance criteria

- [ ] Concrete root cause identified and documented in the PR description
      (race / ordering-dependency / ffprobe-fallback — state which one and
      the evidence).
- [ ] Fix addresses that root cause (not a retry/sleep/skip workaround).
- [ ] `go test ./internal/server/... -run TestScanService_MultiChapterAudiobook -count=20 -v` passes every iteration.
- [ ] `go test ./internal/server/... -race -count=5` and `go test ./internal/scanner/... -race -count=5` pass with no data races reported.
- [ ] A regression check (strengthened assertion or new focused unit test) added.
- [ ] File headers bumped.

## Commit message

```
fix(scanner): make TestScanService_MultiChapterAudiobook deterministic (flaky-scan)

<one line stating the confirmed root cause, e.g. "ScanCache/works-lookup
cache were package-level globals racing with a leftover goroutine from a
prior test's scan" — fill in with what you actually found>

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cf-flaky-scan-multichapter
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency check: re-run the `-race -count=5` commands above on
`origin/main` — if they've been clean for several consecutive CI runs and no
known race is flagged, this is done. Rollback = revert the commit (restores
the pre-existing flaky behavior, which is safe to revert into since it was
already broken on main).
