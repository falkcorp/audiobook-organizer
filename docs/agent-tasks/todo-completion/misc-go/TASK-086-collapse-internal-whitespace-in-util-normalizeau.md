<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-086-collapse-internal-whitespace-in-util-normalizeau.md -->
<!-- version: 1.0.0 -->
<!-- guid: cad429fe-9b4b-4dda-9315-d66e493c421b -->
<!-- last-edited: 2026-08-21 -->

# TASK-086 — Collapse internal whitespace in util.NormalizeAuthor so double-spaced names dedupe correctly (TODO.md L3790)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · misc-go subagent · **Why:** single-function one-line body change plus a couple of test cases; no call sites need touching because every author/series/role/playlist index already funnels through this helper. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3790 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Normalize whitespace (and probably case) in author" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-086-collapse-internal-whitespace-in-util-normalizeau" -b agent/misc-go-086-collapse-internal-whitespace-in-util-normalizeau origin/main
cd "$REPO/.worktrees/misc-go-086-collapse-internal-whitespace-in-util-normalizeau"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change internal/util/normalize.go's NormalizeAuthor to also collapse runs of internal whitespace (via the existing CollapseSpaces helper) so 'Raymond  L.  Weil' and 'Raymond L. Weil' produce byte-identical normalized keys, closing the gap that let author 45616 be minted as a duplicate of 40775/42117.

## Background (verify before editing)

- NormalizeAuthor is used as the index key for author:name:, author_alias:name:, narrator_name:, and series:name: (verified: internal/database/pebble_store_authors.go, pebble_store_auth.go:148/257, pebble_store_playlists.go:220, pebble_store_series.go:99 etc all call util.NormalizeAuthor).
- CollapseSpaces(s string) string already exists in the same file (internal/util/normalize.go:36) and has its own passing test (TestCollapseSpaces, internal/util/normalize_test.go:78) -- it is a ready-made building block, not new logic to invent.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func NormalizeAuthor" internal/util/normalize.go   # 1 hit L26 — NormalizeAuthor only trims+lowercases, no internal-whitespace collapse
  grep -n "func CollapseSpaces" internal/util/normalize.go   # 1 hit L36 — CollapseSpaces already exists in the same package, unused by NormalizeAuthor
  grep -n "util.NormalizeAuthor(name)" internal/database/pebble_store_authors.go   # >=3 hits incl L96, L138, L241 — Author creation/lookup indexes through util.NormalizeAuthor
  ```

### Reuse — don't invent

- Use `CollapseSpaces` in `internal/util/normalize.go` (verify: `grep -n "func CollapseSpaces" internal/util/normalize.go`) — do NOT write a parallel helper.

## Step-by-step

1. Open internal/util/normalize.go. Change the body of `func NormalizeAuthor(s string) string` (line 26-28) from `return strings.ToLower(strings.TrimSpace(s))` to `return CollapseSpaces(strings.ToLower(s))` -- CollapseSpaces already trims, so the explicit TrimSpace becomes redundant and can be dropped.
2. Update the doc comment directly above (line 25) to read: 'NormalizeAuthor trims whitespace, collapses internal whitespace runs, and lowercases an author name for comparison.'
3. Bump the file's version header (currently 1.0.0) and last-edited date per the repo's mandatory file-header rule.
4. In internal/util/normalize_test.go, add a case to TestNormalizeAuthor's `cases` slice: {"Raymond  L.  Weil", "raymond l. weil"} (double internal space collapses to one) and {"J.R.R.\tTolkien", "j.r.r. tolkien"} (internal tab collapses too).
5. Do NOT touch any of the ~15 call sites in internal/database/*.go -- they already call util.NormalizeAuthor for their index keys, so the fix propagates automatically.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_086.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Empty string -> "" (CollapseSpaces already handles this, verified by existing TestCollapseSpaces case {"   ", ""}).
- All-whitespace string -> "".
- String with only a case difference, no whitespace difference -> unaffected, still lowercases correctly.

## Tests

- internal/util/normalize_test.go TestNormalizeAuthor -- add double-internal-space and internal-tab cases asserting the collapsed, lowercased result.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/util/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/util/... -run TestNormalizeAuthor -v exits 0.
- [ ] grep -n 'CollapseSpaces(strings.ToLower(s))' internal/util/normalize.go returns 1 hit inside NormalizeAuthor.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/util/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_086.md`.

## Commit message

```
refactor(misc-go): Collapse internal whitespace in util.NormalizeAuthor so doub (TODO L3790)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This fixes the FORWARD path only (prevents new duplicates). It does not retroactively fix the 4 already-existing type-3 duplicate groups from the 2026-08-14 snapshot (Karen Joy Fowler, Valery Starsky, Raymond L. Weil, Time Pebbles) -- that is TODO L3795's merge op. Also does not touch NormalizeTitle/NormalizeString, which the TODO does not ask about and which are out of scope per the 'stay on target' rule.
