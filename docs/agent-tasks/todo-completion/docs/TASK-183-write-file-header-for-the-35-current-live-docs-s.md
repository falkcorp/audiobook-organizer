<!-- file: docs/agent-tasks/todo-completion/docs/TASK-183-write-file-header-for-the-35-current-live-docs-s.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6c71c1ea-5e82-40ff-b9f0-6f8396ce174b -->
<!-- last-edited: 2026-08-21 -->

# TASK-183 — Write file-header for the 35 current live docs still missing one (TODO.md L101)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Haiku-class · docs subagent · **Why:** Purely mechanical: prepend the standard 4-line header block per CLAUDE.md's format to each of 35 files, no content judgment needed beyond picking today's date and a fresh guid. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 101 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "📚 **Docs consolidation follow-ups (from the 2026-0" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-183-write-file-header-for-the-35-current-live-docs-s" -b agent/docs-183-write-file-header-for-the-35-current-live-docs-s origin/main
cd "$REPO/.worktrees/docs-183-write-file-header-for-the-35-current-live-docs-s"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add the mandatory <!-- file/version/guid/last-edited --> header block to every live (non-archived) doc under docs/ currently missing one -- the 35 files listed in exact_files.

## Background (verify before editing)

- Per CLAUDE.md: 'Every file you create or modify must have its version header updated' -- this is the same rule applied retroactively to files that predate strict enforcement. todo.d/ and changelog.d/ fragments are explicitly EXEMPT; none of the 35 files are under those directories, so none are exempt on that basis.
- docs/superpowers/fleet-status/*.md and docs/superpowers/fleet-tasks/*.md (19 of the 35) are agent-written, ephemeral task-tracking files, similar in spirit to the exempt todo.d/changelog.d fragments -- but no exemption for them is documented anywhere in the repo (checked docs/superpowers/fleet-status/README.md, which describes only the naming/status-value convention). Per this repo's 'a documented exception loses to a global rule' standing convention, treat them as IN SCOPE unless the owner explicitly grants an exemption first.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  find docs -maxdepth 4 -iname '*.md' -not -path '*/archive/*' | xargs grep -L '^<!-- file:' | wc -l   # 35 — 35 live docs lack the required header at HEAD (drifted from the audit's 34)
  head -6 docs/architecture/2026-06-01-server-handler-extraction-design.md   # starts with '# Server Handler Extraction' and '**Date:**', no '<!-- file:' line — docs/architecture/2026-06-01-server-handler-extraction-design.md has no header comment (uses a bold-label ADR format instead)
  head -8 docs/superpowers/fleet-status/README.md   # describes the file-naming/status-value convention; no exemption from the file-header rule is stated — docs/superpowers/fleet-status files are agent-written status trackers, not yet documented as header-exempt anywhere
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Re-run `find docs -maxdepth 4 -iname '*.md' -not -path '*/archive/*' | xargs grep -L '^<!-- file:'` to confirm the current exact list (may have shifted from 35 by execution time).
2. Confirm none of the listed files use an alternative accepted header convention (checked: all 35 use either no header or a bold-label '**Date:**'/'**Author:**' ADR-style block, which is NOT the accepted docs convention per .standards/instructions/file-headers.md -- verify against that standard before batch-editing).
3. For each file, prepend: `<!-- file: <repo-relative-path> -->` / `<!-- version: 1.0.0 -->` / `<!-- guid: <fresh-uuid4> -->` / `<!-- last-edited: <today> -->`, generating each guid with `python3 -c "import uuid; print(uuid.uuid4())"` (never reuse one across files).
4. Do this in small batches (e.g. 5-10 files at a time) with a real diff review, not one blind pass across all 35.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_183.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- docs/architecture/2026-06-01-server-handler-extraction-design.md and docs/architecture/embedding-store-db-selection.md use a bold-label ADR header ('**Date:**', '**Status:**', '**Author:**') instead of HTML comments -- add the standard comment header ABOVE the existing title/ADR block rather than replacing it; the ADR metadata is content, not a competing header format.
- The 19 fleet-status/fleet-tasks files may warrant an owner decision to formally exempt them (matching the todo.d/changelog.d precedent) before this item is executed -- flag this to the coordinator rather than silently excluding them.

## Tests

- Confirm no repo-wide docs-header lint job exists to run locally: `grep -rn 'file-header' .github/workflows/*.yml` (the only header-lint job found, todo-header-lint, is TODO-fragment-specific, not general docs).

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `find docs -maxdepth 4 -iname '*.md' -not -path '*/archive/*' | xargs grep -L '^<!-- file:' | wc -l` returns 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_183.md`.

## Commit message

```
feat(docs): Write file-header for the 35 current live docs still missing (TODO L101)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``find docs -maxdepth 4 -iname '*.md' -not -path '*/archive/*' | xargs grep -L '^<!-- file:' | wc -l` returns 0.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

19 of the 35 files (fleet-status + fleet-tasks) are agent-generated tracking files that may deserve the same header exemption todo.d/changelog.d already have -- this is a real open question worth surfacing to the owner before a haiku agent blindly headers all 35, not a decision this rescope can make unilaterally.
