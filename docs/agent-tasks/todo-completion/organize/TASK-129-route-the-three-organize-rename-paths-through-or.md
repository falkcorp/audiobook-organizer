<!-- file: docs/agent-tasks/todo-completion/organize/TASK-129-route-the-three-organize-rename-paths-through-or.md -->
<!-- version: 1.0.0 -->
<!-- guid: 546f8aeb-c667-4032-8848-137d5583e76d -->
<!-- last-edited: 2026-08-21 -->

# TASK-129 — Route the three organize/rename paths through organizer.MoveBookFile's verify-move-DB-update-rollback pattern (F5)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · organize subagent · **Why:** a structural refactor across the three rename paths in different packages (organizer.OrganizeBookDirectory/OrganizeBook, metafetch.ensureLibraryCopy), each with its own multi-file/single-file/version-record nuances -- needs an architect-level pass to fit MoveBookFile's per-file contract around OrganizeBookDirectory's per-book pathMap contract without changing external behavior for the (majority) success path · **Depends on:** TASK-128 · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 872 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**F5 (remainder) — `OrganizeBookDirectory` still c" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-129-route-the-three-organize-rename-paths-through-or" -b agent/organize-129-route-the-three-organize-rename-paths-through-or origin/main
cd "$REPO/.worktrees/organize-129-route-the-three-organize-rename-paths-through-or"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Refactor OrganizeBookDirectory, OrganizeBook (single-file), and ensureLibraryCopy so each individual file move goes through (or is refactored to match) MoveBookFile's verify-source / verify-destination-absent / move / DB-update / rollback-on-DB-failure pattern, retiring the ad-hoc SameFile/size-heuristic/organizeFile logic those three paths currently duplicate.

## Background (verify before editing)

- TODO.md L872's 'Also worth doing' paragraph: 'MoveBookFile... is the one function in the repo with the correct pattern... It is on none of the three rename paths. Routing them through it would retire most of the above rather than patching each.'
- MoveBookFile operates per-file with a DB rollback-on-failure guarantee; OrganizeBookDirectory operates per-book with a batched pathMap returned to the caller, who then does its own DB writes -- reconciling these two shapes (per-file transactional vs per-book batch) is the actual design work here, not a mechanical swap.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln 'MoveBookFile(' --include=*.go . | grep -v _test   # 2 hits: internal/organizer/move.go, internal/server/file_move.go — MoveBookFile is defined once and has exactly one caller outside its own package
  grep -n 'func MoveBookFile' internal/organizer/move.go   # 1 hit L32 — MoveBookFile signature (store, bookID, oldPath, newPath, extraUpdates)
  ```

### Reuse — don't invent

- Use `organizer.MoveBookFile(store, bookID, oldPath, newPath, extraUpdates) -- verify/move/DB-update/rollback pattern` in `internal/organizer/move.go` (verify: `grep -n 'func MoveBookFile' internal/organizer/move.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/organizer/move.go in full to understand MoveBookFile's exact contract (what store interface it needs, what 'extraUpdates' does, what rollback covers).
2. Read internal/organizer/organizer.go's OrganizeBookDirectory and OrganizeBook, and internal/metafetch/service_apply.go's ensureLibraryCopy end-to-end to map each one's current file-move + DB-write sequence.
3. Design (write up before implementing, given effort=L and review_critical=true) how a per-book batch operation adopts MoveBookFile's per-file rollback guarantee -- likely: call MoveBookFile once per file inside the existing loop, accumulating successes into pathMap as today, but now each individual move is already DB-consistent rather than deferring all DB writes to the caller.
4. Implement incrementally, one rename path at a time (OrganizeBook single-file first, as the simplest case), keeping existing regression tests green after each step.
5. Once all three paths route through MoveBookFile (or its extracted core logic), remove the now-dead SameFile/size-or-hash-heuristic duplication this TODO section (F5, and part 1 above) was built to patch -- confirm with the reviewer whether part 1's hash-based fix should be kept as MoveBookFile's OWN destination-exists check instead of being retired outright.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_organize_129.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Multi-file books: MoveBookFile's contract is per-file; OrganizeBookDirectory must still produce one coherent pathMap and one book-level empty-organize guard (the L860/F6 fix at organizer.go:813) -- the refactor must not lose that book-level check by decomposing into independent per-file calls.
- Partial failure mid-book (file 500 of 1,189 fails after 499 succeeded): decide and document whether MoveBookFile's rollback is per-file-only (leaving 499 successfully moved) or whether the book-level caller needs its own compensating rollback for the whole batch -- this is a real design question, not an implementation detail.

## Tests

- Full existing internal/organizer/organizer_regression_test.go and internal/organizer/unit_test.go suites must stay green throughout.
- New integration-style test exercising ensureLibraryCopy's multi-file organize-then-DB-write sequence to confirm a DB write failure now rolls back the file move (the property MoveBookFile provides that the current ad-hoc path does not).

Anti-over-suppression test: `N/A -- this is a correctness/robustness refactor, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/organizer/... ./internal/metafetch/... passes.
- [ ] grep -n 'MoveBookFile' internal/organizer/organizer.go internal/metafetch/service_apply.go returns >=1 hit each after the refactor.
- [ ] Anti-over-suppression test: `N/A -- this is a correctness/robustness refactor, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_organize_129.md`.

## Commit message

```
refactor(organize): Route the three organize/rename paths through organizer.Move (F5)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Part 2 of 2 for TODO.md L872 (F5). Given effort=L and review_critical=true, this likely needs its own PLAN.md and worktree per CLAUDE.md's Plan Before Execution rule rather than being done inline -- flagging that explicitly for the coordinator rather than assuming a single PR.
