<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/TASK-369-todo-sso-edge-neither-native-app-auth-mode-is-ac.md -->
<!-- version: 1.0.0 -->
<!-- guid: e1822c5c-9e7e-5f67-8ce7-439e0cb62f42 -->
<!-- last-edited: 2026-09-10 -->

# TASK-369 — TODO-SSO-EDGE — Neither native-app auth mode is actually configured at the Cloudflare  (TODO.md:16927)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown › Decision needed” (L16881), items at lines 16927
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): HOLD-FOR-OWNER** — shape: PROD-RUN/DECISION — SSO edge needs external Cloudflare config; BIND and SYSTEMD are 'needs_design, owner decision required'; JWT is a prod secret rotation followed by 'redeploy' · class: security — the four items are real (SSO edge config, LAN bind, JWT rotation, systemd hardening) but none is a repo code change · ⛔ standing-ban contact: deploy ban (JWT item says 'redeploy'); scan ban (section offers 'run library.scan' for a sibling item) · Every cited item is un-dispatchable as a worktree+PR code task; this brief is an operator runbook, not a code brief.
> **Do NOT dispatch this brief to a worker.** It needs an owner decision or a prod run; it is listed in BREAKDOWN under *Held for the owner* and gated in PRIORITY-MATRIX.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · misc-go subagent · **Depends on:** none · **Wave:** owner-gated — not a worker task · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown › Decision needed” (L16881), items at lines 16927. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-369" -b agent/misc-go-369-todo-sso-edge-neither-native-app-auth-mo origin/main
cd "$REPO/.worktrees/misc-go-369"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown › Decision needed” (TODO.md line 16881; items at lines 16927 as of HEAD 42d187168):
  - L16927: **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at the Cloudflare edge, despite both being fully written up in `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via the CF API on 

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L16927 — **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at the Cloudflare edge, despite both being fully written up in `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via the CF API on 2026-07-31: the `books.jdfalk.com` Access app has exactly — evidence: No scoped todo-completion brief covers this (TODO-SSO-EDGE), and per session context CF Access is still a live open topic; no repo evidence (Cloudflare config lives outside the repo) of the Mode C (WARP) fix being applied.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**TODO-SSO-EDGE** Neither native-app auth mode is actually c" TODO.md   # item L16927 still exists (line numbers drift; text is the anchor)
  sed -n '16881p' TODO.md   # the enclosing heading: MISSING (op now built, run pending): no book had stored chapters — `ma
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_misc_go_369.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_misc_go_369.md`.

## Commit message

```
fix(misc-go): TODO-SSO-EDGE — Neither native-app auth mode is actually configured at (TODO.md:16927)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — **`git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing; the PR is held for the owner.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
