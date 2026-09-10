<!-- file: docs/agent-tasks/todo-completion-2026-09/misc-go/TASK-363-missing-op-now-built-run-pending-no-book-had-sto.md -->
<!-- version: 1.0.0 -->
<!-- guid: 849d4cee-6161-4732-919f-6b902eb2182f -->
<!-- last-edited: 2026-09-10 -->

# TASK-363 — MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` s (TODO.md:16927)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` section “MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown”, lines 16927, 16949, 16960, 16966

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Opus-class · misc-go subagent · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` section “MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown”, lines 16927, 16949, 16960, 16966. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/path/to/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-363" -b agent/misc-go-363-missing-op-now-built-run-pending-no-book origin/main
cd "$REPO/.worktrees/misc-go-363"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 4 still-open `TODO.md` item(s) under section “MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown” (lines 16927, 16949, 16960, 16966 as of HEAD 42d187168). Each item's own text is the spec; the reconciliation evidence below says what still shows the gap.

## Background (verify before editing)

- L16927 — - [ ] **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at — evidence: No scoped todo-completion brief covers this (TODO-SSO-EDGE), and per session context CF Access is still a live open topic; no repo evidence (Cloudflare config lives outside the repo) of the Mode C (WARP) fix being applied.
- L16949 — - [ ] **TODO-SEC-BIND** The service binds every interface — evidence: TODO-SEC-BIND: deploy/audiobook-organizer.service and deploy/systemd/audiobook-organizer.service both still have a live 'ExecStart=... --host 0.0.0.0 --port 8484' (confirmed byte-identical duplicates); needs_design per the coordinator's own earlier scoped-item pass — an owner decision (source-IP firewall rule vs narrower bind) is required before any fix, given the rpi1-3 tunnel-hop topology.
- L16960 — - [ ] **TODO-SEC-JWT** Rotate `ABS_JWT_SECRET` — it was pasted in plaintext into — evidence: TODO-SEC-JWT: deploy/local.conf (holding ABS_JWT_SECRET) is gitignored, confirming this is a pure prod config/rotation action with zero repo diff — still unexecuted (prod_run classification), no record found of a rotation having happened.
- L16966 — - [ ] **TODO-SEC-SYSTEMD** The unit has `User=audiobook`, `NoNewPrivileges`, — evidence: TODO-SEC-SYSTEMD: both systemd unit files confirmed still missing all 5 named hardening directives (ProtectSystem=strict, ReadWritePaths, CapabilityBoundingSet, SystemCallFilter, IPAddressDeny) — grep returns 0 hits for all five on both files; needs_design, blocked on reconciling the Whisper egress port (:19847 vs local.conf.example's :8000) with the owner first.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**TODO-SSO-EDGE** Neither native-app auth mode is actu" TODO.md   # the source item still exists (line numbers drift)
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_misc_go_363.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- A test proving any new guard/repair path is fail-closed and dry-run by default.

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
- [ ] Changelog fragment present: `test -f changelog.d/20260910_misc_go_363.md`.

## Commit message

```
fix(misc-go): MISSING (op now built, run pending): no book had stored chapters — `ma (TODO.md:16927)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; the apply path journals enough to undo; a test proves the dry-run writes nothing.

## Coordinator notes

review_critical=true: prod-data path per CLAUDE.md's review-critical definition — hold the PR for the owner.
