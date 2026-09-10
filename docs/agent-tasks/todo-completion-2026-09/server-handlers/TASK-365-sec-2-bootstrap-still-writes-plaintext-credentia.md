<!-- file: docs/agent-tasks/todo-completion-2026-09/server-handlers/TASK-365-sec-2-bootstrap-still-writes-plaintext-credentia.md -->
<!-- version: 1.0.0 -->
<!-- guid: bd5dacbd-2b01-52fc-b4eb-ce5a8ae3272e -->
<!-- last-edited: 2026-09-10 -->

# TASK-365 — SEC-2 — bootstrap still writes plaintext credential files (`internal/server/bo (TODO.md:10906)

> **Status 2026-09-10:** 🆕 NEW — `TODO.md` heading “2026-06-22 security-sweep: the items still open after the status pass” (L10899), items at lines 10906
> **Dispatch 2026-09-10 (`state/final/todo_sections_validation.json`): DISPATCH** — shape: CODE (SEC-2 has a light decision: opt-in/local-only default) · class: security — legit: plaintext credential file (SEC-2) and missing CSP header (SEC-4) · Cleanest match in the batch.
> **Design fit 2026-09-10 (`audiobook-organizer:expert`, `state/final/design_fit_rows_*.json`): FITS** — bootstrap.go:108-112 still writes a plaintext 0600 credential file; #3171 changed WHERE it lives, not WHETHER it is plaintext.
> **Needs first:** Opt-in vs local-only policy for plaintext credential files.
**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · server-handlers subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` heading “2026-06-22 security-sweep: the items still open after the status pass” (L10899), items at lines 10906. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-365" -b agent/server-handlers-365-sec-2-bootstrap-still-writes-plaintext-c origin/main
cd "$REPO/.worktrees/server-handlers-365"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Close the 1 still-open `TODO.md` item(s) under the heading “2026-06-22 security-sweep: the items still open after the status pass” (TODO.md line 10899; items at lines 10906 as of HEAD 42d187168):
  - L10906: **SEC-2** — bootstrap still writes plaintext credential files (`internal/server/bootstrap.go:108,:153`). Decide opt-in/local-only.

Each item's own text is the spec; the reconciliation evidence below says what still shows the gap. Items whose text says *decide* / *measure* / *run in prod* end at the measurement or the decision request — do not improvise the write.

## Background (verify before editing)

- L10906 — **SEC-2** — bootstrap still writes plaintext credential files (`internal/server/bootstrap.go:108,:153`). Decide opt-in/local-only. — evidence: internal/server/bootstrap.go still writes a plaintext (unencrypted) 0600 credential file via `os.WriteFile(keyPath, []byte(raw+"\n"), 0o600)`, with an adjacent comment about CRIT-1 that addresses only LOGGING the key (not writing the file) — 'Instead write it to a 0600 file... so local tooling can still pick it up, and log only the non-secret ID/expiry.' The file write itself, and the opt-in/local-only decision the item asks for, remain unaddressed.

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  grep -n -F "**SEC-2** — bootstrap still writes plaintext credential file" TODO.md   # item L10906 still exists (line numbers drift; text is the anchor)
  sed -n '10899p' TODO.md   # the enclosing heading: 2026-06-22 security-sweep: the items still open after the status pass
  test -e internal/server/bootstrap.go   # anchor file from the reconciliation evidence
  ```

## Step-by-step

1. Read the full `TODO.md` section (prose + every item), not just the checkbox lines; the section carries the constraints.
2. Re-run the anchors; for each item confirm the gap at HEAD or report it closed.
3. Implement item by item, smallest first; one commit per item where they are separable.
4. Regression tests per item; run the gate; changelog fragment; bump headers; report the exact `TODO.md` line text to check off.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_server_handlers_365.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- One regression test per item that fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).
- ONLY if an item's fix adds or changes a write/apply/repair path (see Idempotency / Rollback): a test proving that path is fail-closed and dry-run by default.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_server_handlers_365.md`.

## Commit message

```
fix(server-handlers): SEC-2 — bootstrap still writes plaintext credential files (`internal/s (TODO.md:10906)

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
