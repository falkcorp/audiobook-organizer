<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-088-route-acoustid-lsh-backfill-s-lshindexchecker-lo.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1dc6cf2b-fb46-4466-994a-70aff714d4fb -->
<!-- last-edited: 2026-08-21 -->

# TASK-088 — Route acoustid lsh_backfill's lshIndexChecker lookup through database.AsCapability (TODO.md L4698)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · misc-go subagent · **Why:** One-line body swap, same pattern as part 1. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4698 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/merge/service.go:34-42` — `AsExternalIDR" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-088-route-acoustid-lsh-backfill-s-lshindexchecker-lo" -b agent/misc-go-088-route-acoustid-lsh-backfill-s-lshindexchecker-lo origin/main
cd "$REPO/.worktrees/misc-go-088-route-acoustid-lsh-backfill-s-lshindexchecker-lo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Replace the bare `p.store.(lshIndexChecker)` assertion in runLSHBackfill with database.AsCapability[lshIndexChecker](p.store) so the fast-skip check keeps working if p.store is ever handed a decorator-wrapped store.

## Background (verify before editing)

- This is the same defect class as part 1, in a different file, per the TODO item's own 'Same shape at ... lsh_backfill.go:86' citation.
- Effect if triggered: hasChecker becomes false, HasLSHIndex fast-skip is silently disabled, and the backfill re-writes every row instead of skipping already-indexed ones — a performance regression, not a correctness one (checker is optional, `hasChecker := checker != nil` already degrades gracefully).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "lshIndexChecker" internal/plugins/acoustid/lsh_backfill.go   # 3 hits: type decl ~L24, bare assertion ~L86, a test-file comment — bare assertion at the cited line
  grep -n "type lshIndexChecker interface" -A 3 internal/plugins/acoustid/lsh_backfill.go   # 1 hit ~L24 — lshIndexChecker is a 1-method interface
  grep -n "serviceregistry.Get\[pluginStore\]" internal/plugins/acoustid/register.go   # 1 hit ~L32 — p.store is sourced from the service-registry container's KeyStore (bare store today)
  ```

### Reuse — don't invent

- Use `database.AsCapability[T]` in `internal/database/store_capability.go` (verify: `grep -n "func AsCapability" internal/database/store_capability.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/plugins/acoustid/lsh_backfill.go.
2. Replace `checker, _ := p.store.(lshIndexChecker)` (~L86) with `checker, _ := database.AsCapability[lshIndexChecker](p.store)`.
3. Confirm `github.com/falkcorp/audiobook-organizer/internal/database` is imported in this file (it already is, for other database.* references).
4. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_088.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- p.store does not implement lshIndexChecker at all (plain PebbleStore without HasLSHIndex, or a mock): AsCapability returns (nil, false), hasChecker stays false, per-row fallback taken — same as today.

## Tests

- internal/plugins/acoustid/lsh_backfill_test.go: add a case wrapping the fake store in a minimal StoreUnwrapper-implementing decorator and asserting the fast-skip path (hasChecker=true) still activates through it.
- Keep the existing direct (non-decorated) case passing unchanged.

Anti-over-suppression test: `N/A — this is a performance fast-path, not a filter/guard that could over-suppress results.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./internal/plugins/acoustid/... exits 0.
- [ ] go test ./internal/plugins/acoustid/... -run LSHBackfill exits 0.
- [ ] Anti-over-suppression test: `N/A — this is a performance fast-path, not a filter/guard that could over-suppress results.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/acoustid/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_088.md`.

## Commit message

```
refactor(misc-go): Route acoustid lsh_backfill's lshIndexChecker lookup through (TODO L4698)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Low priority relative to part 1: this defect degrades performance, not correctness, and per the service-registry binding order is not currently reachable in production.
