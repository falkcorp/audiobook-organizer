<!-- file: docs/agent-tasks/ux-small-items/TASK-08-slog-prod-verify.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4db4a6c0-616f-474d-ae90-b6183458a25f -->
<!-- last-edited: 2026-07-10 -->

# TASK-08 — SLOG-PROD-VERIFY: smoke-test the op-ID chain on prod, read-only by default (Lane B write path AskUserQuestion-gated) (#1255) — NOT AGENT WORK

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none for code; the one-line TODO.md checkoff shares `TODO.md` with TASK-03/TASK-07 — wave 4 (after TASK-03 merges, before TASK-07 starts).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** ⛔ NOT AGENT WORK — the coordinator session runs this itself over SSH · operational-verification role · **Why:** live-prod judgment (service health, log interpretation) + provisioned SSH access; do not dispatch to a weak-model subagent · **Depends on:** TASK-03 (TODO.md serialization only — the verification itself can run any time)

## ⛔ START HERE (do this first, exactly)

The verification itself touches NO repo files. The worktree below is ONLY for the final one-line TODO.md checkoff PR — create it after the evidence is captured:

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-slog-prod-verify" -b agent/ux-small-items-slog-prod-verify origin/main
cd "$REPO/.worktrees/ux-small-items-slog-prod-verify"
git rebase origin/main
```

## Goal

Prove, on prod (172.16.2.30 — "the server"), that the structured-logging op-ID chain works end-to-end: trigger an operation, confirm the opID appears in journalctl log lines, and confirm `GET /api/v1/operations/:id/activity` returns rows for it (GitHub issue #1255, TODO SLOG-PROD-VERIFY). The endpoint exists — [this-session verified] wired at `internal/server/wire_library_routes.go:39` (handler `ListOperationActivity`; NOT in `wire_operations_routes.go`).

⚠ **THE PROCEDURE DOC'S OP CHOICE IS NOT READ-ONLY.** `docs/slog-prod-verify.md` says to trigger a `metadata-fetch` op on "a test book that is safe to refresh" — but [this-session verified] that op is fetch+APPLY: `fetchAudiobookMetadataImpl` (`internal/server/handlers/metadata/handler.go:421`) applies the fetched metadata ("fetch+apply rewrites book identity (title, author, etc)" per its own comment) and enqueues an iTunes write-back. That is a prod-data WRITE, which the initiative gate routes to AskUserQuestion — it must NOT be fired autonomously under a "read-only" label.

**Therefore this task has two lanes:**
- **Lane A (autonomous, read-only — the default):** verify the opID→journalctl→`/activity` chain using the READ-ONLY `scan-duration-mismatch` maintenance job ([this-session verified] `internal/maintenance/jobs/scan_duration_mismatch.go` only reads `GetAllBooksCore` and logs; dispatched via `POST /api/v1/maintenance/jobs/scan-duration-mismatch`, route at `server_lifecycle.go:1249`; the op wrapper `maintenance_job_op.go` emits activity rows for the returned `operation_id`). Zero prod writes.
- **Lane B (human-gated, OPTIONAL):** if the metadata-domain tags in the procedure doc's checklist (`action: metadata-apply`, `domain:metadata`) must also be verified, that requires the metadata-fetch WRITE path. AskUserQuestion FIRST — name a designated throwaway test book, capture its full metadata JSON before the run (that snapshot is the revert), and only proceed on explicit approval. Never run Lane B on your own judgment.

If anything else observed suggests a prod change is needed, raise it via a real AskUserQuestion — do not act.

## Background (verify before running)

- Procedure doc `docs/slog-prod-verify.md` is the historical detail source for WHAT to check (opID in logs, `/activity` rows, tag shapes) — but do NOT follow its op choice verbatim: its `metadata-fetch` op writes (see Goal). Lane A substitutes a read-only op.
- Auth: `Authorization: Bearer <abk_...>` only (X-API-Key unsupported); token via the server-bootstrap procedure (`.claude/.api-token` is 3 lines — use the api_key line only).
- TODO item at ~:1409 with a 2026-07-01 note: "code/endpoint exist ... remaining is a live-prod smoke-test run."

- **Re-verify these anchors before running**:
  ```bash
  grep -n "SLOG-PROD-VERIFY" TODO.md                     # TODO line, 1 hit, currently [ ]
  test -f docs/slog-prod-verify.md && echo PROCEDURE-OK  # must print PROCEDURE-OK
  grep -n "operations/:id/activity" internal/server/wire_library_routes.go   # endpoint route, 1 hit (~:39)
  grep -n "maintenance/jobs/:job_id" internal/server/server_lifecycle.go     # Lane-A dispatch route, 1 hit (~:1249)
  grep -n "GetAllBooksCore" internal/maintenance/jobs/scan_duration_mismatch.go  # Lane-A op is read-only, 1 hit
  ```

## Step-by-step

1. Re-verify anchors; read `docs/slog-prod-verify.md` in full (for the checklist shapes, not the op choice).
2. Bootstrap prod API auth (server-bootstrap skill/procedure). `ssh 172.16.2.30` is the provisioned access — use it, do not ask for credentials.
3. **Lane A (read-only):** trigger the scan job: `curl -s -X POST -H "Authorization: Bearer <abk_...>" -H "Content-Type: application/json" -d '{"dry_run": true}' http://172.16.2.30:<port>/api/v1/maintenance/jobs/scan-duration-mismatch` — capture the returned `operation_id` (the opID). ⛔ Do NOT trigger `metadata-fetch` in this lane — it writes.
4. Over SSH: `journalctl -u <service> --since "-10 min" | grep <opID>` — capture matching lines (expect ≥1 with the opID field, e.g. the op registry's start/complete lines).
5. `curl -s -H "Authorization: Bearer <abk_...>" http://172.16.2.30:<port>/api/v1/operations/<opID>/activity` — capture the response (expect non-empty rows; the endpoint is eventually consistent — retry after a few seconds if empty while running).
6. If EITHER check fails: do NOT fix prod; record exact evidence and report — the failure becomes a new bug task. Any prospective prod file/data change → AskUserQuestion first.
7. **Lane B (OPTIONAL, human-gated):** only if the metadata-domain tags (`action: metadata-apply` etc.) must be verified per the doc's checklist — AskUserQuestion first, naming the throwaway test book and the revert plan (pre-run metadata JSON snapshot). On approval, run the doc's metadata-fetch procedure against that book only; on refusal or no need, record "chain verified read-only; metadata-domain tags deferred" in the checkoff note.
8. On success: in the worktree, tick TODO.md's SLOG-PROD-VERIFY to `[x]` and append ` — ✅ verified live 2026-07-XX: opID <id> (read-only scan-duration-mismatch job) present in journalctl + /activity returned <N> rows (INIT-10 TASK-08)`. Touch nothing else. Bump the TODO.md header.
9. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path added).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. (Docs-only checkoff PR; the REAL test is the captured prod evidence pasted in the PR body.)

## Acceptance criteria

- [ ] PR body contains BOTH evidence artifacts verbatim: the journalctl line(s) with the opID, and the non-empty `/activity` JSON (row count stated) — produced by the READ-ONLY Lane-A op.
- [ ] `grep -n "SLOG-PROD-VERIFY" TODO.md` shows `[x]` with the dated evidence note.
- [ ] Zero prod writes performed on Lane A (read-only op trigger + reads only). If Lane B ran, the PR body links the AskUserQuestion approval, names the test book, and includes the pre-run metadata snapshot (the revert artifact).
- [ ] Anti-over-suppression: N/A
- [ ] Minimal CI green; TODO.md header bumped.

## Commit message

```
docs(todo): check off SLOG-PROD-VERIFY — op-ID chain verified live on prod (#1255)

Read-only scan-duration-mismatch job triggered on 172.16.2.30; opID confirmed
in journalctl and /api/v1/operations/:id/activity returned rows. Zero prod
writes (the procedure doc's metadata-fetch op is fetch+apply and was not used
autonomously; metadata-domain tags deferred to a human-gated run if needed).

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-slog-prod-verify
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "SLOG-PROD-VERIFY" TODO.md` already shows `[x]` with a 2026-07 dated evidence note, this task is already applied — run the acceptance checks instead of re-running the smoke test (a Lane-A re-run is harmless but unnecessary). Rollback = revert the checkoff commit; Lane A never modifies prod, so nothing else to undo. If Lane B ran, revert prod by restoring the test book's pre-run metadata JSON snapshot captured at the AskUserQuestion gate.
