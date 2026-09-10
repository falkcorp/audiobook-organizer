<!-- file: docs/agent-tasks/todo-completion/database/TASK-033-repoint-store-go-17-s-broken-doc-reference-to-th.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6d59abcd-cebf-40a8-91d7-a5ffba7abc80 -->
<!-- last-edited: 2026-09-02 -->

# TASK-033 — Repoint store.go:17's broken doc reference to the archived design spec (TODO.md L4721)

> **Status 2026-09-02:** ✅ DONE — PR #2732 merged 2026-08-22 (fd666bff3).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Why:** One-line comment edit repointing a path. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 4721 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/database/store.go:17` cites" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-033-repoint-store-go-17-s-broken-doc-reference-to-th" -b agent/database-033-repoint-store-go-17-s-broken-doc-reference-to-th origin/main
cd "$REPO/.worktrees/database-033-repoint-store-go-17-s-broken-doc-reference-to-th"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Repoint the dead doc reference in internal/database/store.go:17 to docs/archive/superpowers/plans/2026-04-17-store-interface-segregation.md so a reader following the comment lands on a real file.

## Background (verify before editing)

- The owner-decision list does not cover this specific file-move question, but the resolution is unambiguous: the archived file is the same document (same date, same topic, only the trailing '-design' dropped from the filename), so this is a mechanical repoint, not a design call.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "store-interface-segregation-design" internal/database/store.go   # 1 hit ~L17 — the broken reference exists at the cited line
  grep -n "docs/superpowers/" .gitignore   # 1 hit, with narrow !docs/superpowers/fleet-tasks/ etc. exceptions that do not cover specs/ — docs/superpowers/ is now gitignored, so nothing there is on main
  git log --oneline --all --grep="untrack docs/superpowers"   # 1 hit: ff6607fe — the commit that untracked it
  find docs/archive/superpowers/plans -iname "*store-interface-segregation*"   # 1 hit: docs/archive/superpowers/plans/2026-04-17-store-interface-segregation.md — the archived counterpart exists on main under a slightly different filename
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/database/store.go.
2. At line 17, change `See docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md.` to `See docs/archive/superpowers/plans/2026-04-17-store-interface-segregation.md.`
3. Diff the two files' content (`diff <(git show 29e256ac:docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md) docs/archive/superpowers/plans/2026-04-17-store-interface-segregation.md`) to confirm they are the same document before repointing — if they materially differ, restore the original file into docs/archive/ instead of repointing to a divergent one.
4. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_033.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the diff step finds the archived file has drifted from the original (unlikely for an archived/frozen doc), restore the exact original via `git show 29e256ac:<path>` into docs/archive/ instead, and repoint to that.

## Tests

- N/A — comment-only change; no test asserts comment content today. Optional: a doc-link-checker script if one exists in scripts/ (grep -rl "doc.*link.*check\|linkcheck" scripts/) could be extended to catch this class, but that is out of scope for this item.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -n "docs/archive/superpowers/plans/2026-04-17-store-interface-segregation.md" internal/database/store.go returns 1 hit.
- [ ] test -f docs/archive/superpowers/plans/2026-04-17-store-interface-segregation.md exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_033.md`.

## Commit message

```
refactor(database): Repoint store.go:17's broken doc reference to the archived d (TODO L4721)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Tiny, isolated fix; safe for a haiku-tier pass alongside item L4694 (same file family, same PR is reasonable, but keep as separate commits since they touch different lines for different reasons).
