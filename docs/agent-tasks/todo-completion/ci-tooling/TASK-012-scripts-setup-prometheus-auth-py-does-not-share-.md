<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-012-scripts-setup-prometheus-auth-py-does-not-share-.md -->
<!-- version: 1.0.0 -->
<!-- guid: d93c4b26-6858-435b-b313-cd7ddf9a1e7d -->
<!-- last-edited: 2026-08-21 -->

# TASK-012 — scripts/setup-prometheus-auth.py does NOT share the server-side shell script's dead-indentation bug (TODO.md L4312)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Why:** documentation-only comment addition, no logic change needed · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4312 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Check `scripts/setup-prometheus-auth.py` for the" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-012-scripts-setup-prometheus-auth-py-does-not-share-" -b agent/ci-tooling-012-scripts-setup-prometheus-auth-py-does-not-share- origin/main
cd "$REPO/.worktrees/ci-tooling-012-scripts-setup-prometheus-auth-py-does-not-share-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a short comment near the job-block template in scripts/setup-prometheus-auth.py (around L78) noting it was checked against the abo-prometheus-auth.sh v1.0.1 indentation bug (patched on the server 2026-08-14) and confirmed NOT to share the pattern, so a future reader doesn't re-open this investigation.

## Background (verify before editing)

- The buggy shell script computed indentation from a whitespace-only regex capture and called .index('-') on it, guaranteeing a ValueError on any real prometheus.yml with list-style '- job_name:' entries. This Python script sidesteps the whole problem by using a fixed, hardcoded indentation in its template string rather than introspecting the existing file's indentation at all.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "index('-')\|indent" scripts/setup-prometheus-auth.py   # 0 hits — no .index('-') or computed-indent logic exists in the script
  grep -n "job_name: '{JOB_NAME}'" scripts/setup-prometheus-auth.py   # 1 hit at L78 — the new job block is a hardcoded template, not a computed indent
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Add a one-line comment above the JOB block template (near scripts/setup-prometheus-auth.py:78) stating: '# Hardcoded indent, not computed from existing YAML — does not share the abo-prometheus-auth.sh v1.0.1 dead-indentation bug (checked 2026-08-2x).' Bump the file's version header per this repo's mandatory file-header rule.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_012.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- (none)

## Tests

- (none)

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -n "does not share" scripts/setup-prometheus-auth.py returns 1 hit.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_012.md`.

## Commit message

```
feat(ci-tooling): scripts/setup-prometheus-auth.py does NOT share the server-s (TODO L4312)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `grep -n "does not share" scripts/setup-prometheus-auth.py returns 1 hit.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The investigation itself (the actual ask of this TODO item) is complete via this scout's grep — the only remaining deliverable is recording the negative result so it isn't re-investigated.
