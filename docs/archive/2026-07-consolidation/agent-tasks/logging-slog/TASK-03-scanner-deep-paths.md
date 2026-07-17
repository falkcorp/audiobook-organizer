<!-- file: docs/agent-tasks/logging-slog/TASK-03-scanner-deep-paths.md -->
<!-- version: 1.0.0 -->
<!-- guid: c18a2171-aa93-4d28-81c0-a85828daac5e -->
<!-- last-edited: 2026-07-01 -->

# TASK-03 — Wire logging.Info(ctx) into scanner deep paths (SLOG-W13c)

## ⚠ SCOPE NOTE — re-scoped from S to M during authoring, 2026-07-01
This task was originally specced as a small mechanical swap. Verification found the fix requires widening an **exported** function signature (`scanner.ScanDirectoryParallel`) and the **exported `Scanner` interface**, across 9 files including a mockery-generated mock. This is real, valid, in-scope work — the raw `slog.Info` calls genuinely ARE reachable from the `performScanInternal(ctx, opID, ...)` op — but it is bigger and riskier than a one-line swap. If you are a Haiku-tier worker and find this task is taking more than ~45 minutes or you are unsure about an edit to `internal/scanner/mocks/mock_scanner.go`, stop and flag for Sonnet escalation rather than guessing.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Haiku · go-backend subagent (escalate to Sonnet if the mock edit is unclear) · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/sl-scanner-deep-paths" -b agent/sl-scanner-deep-paths origin/main
cd "$REPO/.worktrees/sl-scanner-deep-paths"
git rebase origin/main
```

## Goal
Replace the raw `slog.Info(...)` calls in `internal/scanner/scanner.go` (2 call sites), `internal/scanner/chapter_consolidation.go` (1 call site), and `internal/scanner/shattered_coalesce.go` (1 call site) with `logging.Info(ctx, ...)`, so scan-time log lines carry the `opID` of the enclosing scan op. This requires threading `ctx context.Context` from `performScanInternal` (which already has it) down through `scanFolder` → the exported `ScanDirectoryParallel` function/interface method → `groupFilesIntoBooks` → `consolidateChapterGroups` / `coalesceShatteredSiblings`, because today `ctx` is dropped at the `ScanDirectoryParallel` call.

## Background (verify before editing)
Re-run these greps yourself — line numbers drift and this is a wide change:

```bash
# The 4 raw slog.Info calls to fix:
grep -n "slog\." internal/scanner/scanner.go internal/scanner/chapter_consolidation.go internal/scanner/shattered_coalesce.go
# expected ~4 hits:
#   scanner.go: "scanner shattered-sibling coalesce" (near line 482)
#   scanner.go: "scanner multi-file group detected" (near line 1571)
#   chapter_consolidation.go: "scanner chapter consolidation merging files" (near line 135)
#   shattered_coalesce.go: "scanner coalesced shattered siblings" (near line 118)

# The op that already has ctx and is the source of truth for the opID:
grep -n "func (ss \*ScanService) performScanInternal\|func (ss \*ScanService) scanFolder" internal/scanner/service.go

# Where ctx currently gets dropped:
grep -n "ScanDirectoryParallel(" internal/scanner/service.go internal/scanner/scanner.go
# expected: service.go's scanFolder calls
#   ScanDirectoryParallel(folderPath, workers, log.With("scanner"))
# with no ctx argument, even though scanFolder itself has ctx in scope.

# Every file that references ScanDirectoryParallel (you must update ALL of these):
grep -rln "ScanDirectoryParallel" --include="*.go" .
# expected 9 files:
#   internal/server/folder_autoscan_op.go
#   internal/scanner/unit_test.go
#   internal/scanner/service.go
#   internal/scanner/integration_format_test.go
#   internal/scanner/scanner_coverage_test.go
#   internal/scanner/scanner.go
#   internal/scanner/multi_format_test.go
#   internal/scanner/scanner_test.go
#   internal/scanner/mocks/mock_scanner.go
```

Key facts:
- `ScanDirectoryParallel` is declared TWICE: as a package-level function (`internal/scanner/scanner.go`) and as a method on the `Scanner` interface (also `scanner.go`, used for test doubles/dependency injection). Both need the new `ctx` parameter, and the package-level function's body calls `activeScanner.ScanDirectoryParallel(...)` — that internal delegation call also needs `ctx` threaded through.
- `internal/scanner/mocks/mock_scanner.go` is a **mockery-generated mock**. Per this repo's known gotcha (local mockery versions can regenerate ALL mocks repo-wide with unrelated changes), do **NOT** run `mockery` / `go generate` to regenerate this file. Hand-edit only the `ScanDirectoryParallel` method (and its `_Expecter`/`_Call`/`RunAndReturn` helper methods) to add the new `ctx context.Context` parameter in the same position, mirroring the style of any other mock method in the same file that already takes `ctx` as its first parameter (grep `context.Context` in that file for a template to copy).
- `internal/server/folder_autoscan_op.go` is itself an op — check whether it already has a `ctx` in scope at its `ScanDirectoryParallel` call site (it should, since it is triggered from an op `Run` function) and pass it through instead of inventing a new context.
- The 4 test files (`unit_test.go`, `integration_format_test.go`, `scanner_coverage_test.go`, `multi_format_test.go`, `scanner_test.go` — that's actually 5, recount with the grep above) call `ScanDirectoryParallel` directly or via the mock; update each call site to pass `context.Background()` unless the surrounding test already has a `ctx` variable in scope (use that instead, don't shadow it).

## Step-by-step
1. Run all the Background greps above and confirm you have the exact list of call sites (line numbers WILL differ from this doc — trust your own grep output).
2. In `internal/scanner/scanner.go`:
   a. Find the `Scanner` interface declaration (`grep -n "ScanDirectoryParallel(rootDir string, workers int, scanLog logger.Logger) (\[\]Book, error)" internal/scanner/scanner.go`) and add `ctx context.Context` as the first parameter to the interface method signature.
   b. Find the package-level `func ScanDirectoryParallel(rootDir string, workers int, scanLog logger.Logger) ([]Book, error)` function and add `ctx context.Context` as its first parameter. Update its body's delegation call `activeScanner.ScanDirectoryParallel(rootDir, workers, scanLog)` to `activeScanner.ScanDirectoryParallel(ctx, rootDir, workers, scanLog)`.
   c. Find `func ScanDirectory(rootDir string, scanLog logger.Logger) ([]Book, error)` — it calls `ScanDirectoryParallel(rootDir, 1, scanLog)` internally. Add `ctx context.Context` as its first parameter too, and pass `ctx` through. Check its callers with `grep -rn "scanner.ScanDirectory(" --include="*.go" .` and update them the same way as step 6 below (use `context.Background()` unless a ctx is already in scope).
   d. Find `groupFilesIntoBooks(files []string) []Book` and its caller inside `ScanDirectoryParallel`'s concrete implementation. Add `ctx context.Context` as its first parameter, thread it in, and use it at the `slog.Info("scanner multi-file group detected", ...)` call site — replace with `logging.Info(ctx, "scanner multi-file group detected", ...)` keeping the same key/value attrs.
   e. At the call site around line 480 (`books = coalesceShatteredSiblings(books)`), you'll need `ctx` in scope in the enclosing function too — thread it the same way. Replace `slog.Info("scanner shattered-sibling coalesce", ...)` with `logging.Info(ctx, "scanner shattered-sibling coalesce", ...)`.
3. In `internal/scanner/chapter_consolidation.go`: add `ctx context.Context` as the first parameter to `consolidateChapterGroups(files []string) []Book`, thread it from its caller in `scanner.go` (from step 2d's call site, `books = append(books, consolidateChapterGroups(noAlbum)...)`), and replace `slog.Info("scanner chapter consolidation merging files", ...)` with `logging.Info(ctx, "scanner chapter consolidation merging files", ...)`.
4. In `internal/scanner/shattered_coalesce.go`: add `ctx context.Context` as the first parameter to `coalesceShatteredSiblings(books []Book) []Book`, and replace `slog.Info("scanner coalesced shattered siblings", ...)` with `logging.Info(ctx, "scanner coalesced shattered siblings", ...)`.
5. In `internal/scanner/service.go`'s `scanFolder`, update the call `ScanDirectoryParallel(folderPath, workers, log.With("scanner"))` to `ScanDirectoryParallel(ctx, folderPath, workers, log.With("scanner"))` — `scanFolder` already has `ctx` as a parameter, confirm with `grep -n "func (ss \*ScanService) scanFolder" internal/scanner/service.go`.
6. Update every remaining caller found by the `grep -rln "ScanDirectoryParallel"` search in Background:
   - `internal/server/folder_autoscan_op.go`: use the `ctx` already in scope at that call site (it's inside an op `Run` function).
   - Each of the 5 scanner test files: pass `context.Background()` unless the test already declares/receives a `ctx` variable, in which case reuse it. Add `"context"` to each file's import block if missing.
   - `internal/scanner/mocks/mock_scanner.go`: hand-edit (do NOT regenerate) the `ScanDirectoryParallel` method and its `_Expecter`/`Run`/`Return`/`RunAndReturn` helpers to add the `ctx context.Context` parameter in the same position as the interface method. Look at another method in the same file that already takes `ctx` as a template for the exact mockery-generated style (parameter matching via `mock.Anything` etc.).
7. Also update `ScanDirectory`'s own callers if step 2c required it (same pattern as step 6).
8. Bump the file-header `version` and `last-edited` fields on every file you touched.

## How to test
```bash
cd "$REPO/.worktrees/sl-scanner-deep-paths"
go build ./internal/scanner/... ./internal/server/...
go vet ./internal/scanner/... ./internal/server/...
go test ./internal/scanner/... -v
go test ./internal/server/... -run 'Scan|Autoscan' -v
```
All of the above must pass with no compile errors and no test failures. Do NOT run `mockery` or `go generate ./...` — hand-edit the mock as instructed in step 6.

## Acceptance criteria
- [ ] `Scanner` interface's `ScanDirectoryParallel` method and the package-level `ScanDirectoryParallel`/`ScanDirectory` functions all take `ctx context.Context` as their first parameter.
- [ ] `groupFilesIntoBooks`, `consolidateChapterGroups`, `coalesceShatteredSiblings` all take `ctx context.Context` as their first parameter and use it for `logging.Info(ctx, ...)` at their former `slog.Info` call sites.
- [ ] `internal/scanner/service.go`'s `scanFolder` passes its own `ctx` into `ScanDirectoryParallel` (no more ctx-drop).
- [ ] All 9 files found by `grep -rln "ScanDirectoryParallel" --include="*.go" .` compile and their call sites are updated consistently.
- [ ] `internal/scanner/mocks/mock_scanner.go` was hand-edited, not regenerated (confirm `git diff --stat` shows only the `ScanDirectoryParallel`-related lines changed in that file, not a full-file rewrite).
- [ ] `go build ./internal/scanner/... ./internal/server/...` and `go test ./internal/scanner/...` pass.
- [ ] File headers (version + last-edited) bumped on every touched file.

## Commit message
```
fix(scanner): wire logging.Info(ctx) into scan deep paths (SLOG-W13c)

Thread ctx from performScanInternal through ScanDirectoryParallel,
groupFilesIntoBooks, consolidateChapterGroups, and
coalesceShatteredSiblings so scan-op log lines carry opID. Widens the
exported Scanner interface and ScanDirectoryParallel/ScanDirectory
signatures; mock_scanner.go hand-edited (not regenerated) to match.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/sl-scanner-deep-paths
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "slog.Info" internal/scanner/scanner.go internal/scanner/chapter_consolidation.go internal/scanner/shattered_coalesce.go` returning no hits means this task is already done — stop, do not re-edit. Rollback: `git revert <commit-sha>` on the merge commit; this is a pure signature-widening change with no data migration, safe to revert wholesale if it breaks another in-flight PR that also touches `ScanDirectoryParallel`.