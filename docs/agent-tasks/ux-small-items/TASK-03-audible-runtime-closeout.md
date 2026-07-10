<!-- file: docs/agent-tasks/ux-small-items/TASK-03-audible-runtime-closeout.md -->
<!-- version: 1.0.0 -->
<!-- guid: ce824e62-5427-4db4-bb83-1099473fc3d4 -->
<!-- last-edited: 2026-07-10 -->

# TASK-03 — Close out Audible runtime-vs-duration mismatch detection with a fresh read-only prod scan (AUDIBLE-RUNTIME-MISMATCH)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.
**File-ownership:** none for `internal/server/` (no Go changes); shares `TODO.md` with TASK-02 — do not start until TASK-02's PR is merged (wave 3).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · verification-and-report subagent · **Why:** interprets a live prod scan result and must respect the read-only boundary · **Depends on:** TASK-02 (TODO.md same-file serialization only)

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ux-small-items-audible-runtime-closeout" -b agent/ux-small-items-audible-runtime-closeout origin/main
cd "$REPO/.worktrees/ux-small-items-audible-runtime-closeout"
git rebase origin/main
```

## Goal

The master plan listed "Audible runtime vs book-duration mismatch detection" as open, but the whole TODO section is already shipped (`[x]` on all five items, PRs #549/#561), including a bulk scan maintenance job. Your job: (1) re-verify the shipped surface in code, (2) run the EXISTING read-only scan job against prod and capture the mismatch count as closing evidence, (3) fix the one drifted citation in TODO.md and append a dated closeout note. Do NOT build a new detection op — it exists.

## Background (verify before editing)

- TODO.md section `## ⏱️ Audible Runtime vs Book Duration Mismatch Detection` (~:1876-1888): all five items `[x]` (PR #549/#561 per the TODO lines' own citations).
- ⚠ **THE SCAN IS A MAINTENANCE JOB, NOT A GET ENDPOINT** [this-session verified 2026-07-10 at HEAD `fce58498`]: `internal/maintenance/jobs/scan_duration_mismatch.go` registers job ID `scan-duration-mismatch` via `maintenance.Register`. Its ONLY param is `dry_run` (the job is read-only regardless — it reads `GetAllBooksCore` and logs; the dry-run flag is not even consumed by `Run`). The mismatch threshold is **HARDCODED at 120 seconds** — there is NO `max_delta_min` param, and NO `GET /api/v1/maintenance/scan-duration-mismatch` route exists. Dispatch is `POST /api/v1/maintenance/jobs/scan-duration-mismatch` (wired at `internal/server/server_lifecycle.go:1249`, permission settings.manage), which returns `{ "operation_id": "<ULID>" }`.
- The mismatch COUNT is emitted only in the job's log line `"scan-duration-mismatch complete mismatches"` (slog), NOT in any HTTP JSON response — capture it from `journalctl` over SSH or from `GET /api/v1/operations/<operation_id>/logs`.
- The DUR-1 line cites `MetadataReviewDialog.tsx:604` — DRIFTED: the warning chip now renders around line ~774; cite it by symbol (`runtime differs by` chip), not by line number.
- Prod = 172.16.2.30 ("the server"). Auth: `Authorization: Bearer <abk_...>` via the `server-bootstrap` procedure (`.claude/.api-token` is 3 lines — extract the api_key line only). `X-API-Key` is NOT supported.
- The scan endpoint is a GET/read-only report. **You are authorized for read-only prod access only.** If anything you observe suggests a prod file or data change is needed, STOP that thread and raise it via AskUserQuestion — do not act.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "Audible Runtime vs Book Duration Mismatch" TODO.md                                   # section header, 1 hit
  grep -n "MetadataReviewDialog.tsx:604" TODO.md                                                # stale citation, 1 hit BEFORE edit
  grep -n "runtime differs by" web/src/components/audiobooks/MetadataReviewDialog.tsx           # actual chip, >=1 hit (~:774)
  grep -rn "scan-duration-mismatch" internal/maintenance/jobs/scan_duration_mismatch.go         # shipped job, >=2 hits
  grep -n "maintenance/jobs/:job_id" internal/server/server_lifecycle.go                        # dispatch route, 1 hit (~:1249)
  grep -n "delta > 120" internal/maintenance/jobs/scan_duration_mismatch.go                     # hardcoded 120s threshold, 1 hit
  ```
  Zero-hit on any expected-hit grep means STOP and report.

## Step-by-step

1. Run all re-verify greps; confirm the section is fully `[x]` (if ANY item is unchecked, STOP and report — the closeout premise fails).
2. Get a prod API token (server-bootstrap procedure). Trigger the read-only scan job via its real dispatch route:
   `curl -s -X POST -H "Authorization: Bearer <abk_...>" -H "Content-Type: application/json" -d '{"dry_run": true}' "http://172.16.2.30:<port>/api/v1/maintenance/jobs/scan-duration-mismatch"` — capture the returned `operation_id`. (Find the port from the systemd unit / existing docs. There is NO `max_delta_min` param — the 120s threshold is hardcoded in the job; do not invent query params.)
3. Capture the mismatch COUNT from the job's log output once it completes: over SSH, `journalctl -u <service> --since "-15 min" | grep "scan-duration-mismatch complete"` (the line carries `mismatches=<N>`), or `curl -s -H "Authorization: Bearer <abk_...>" "http://172.16.2.30:<port>/api/v1/operations/<operation_id>/logs"`. Record `<N>` and a couple of the per-book `duration mismatch` warn lines as samples.
4. In `TODO.md`, on the DUR-1 line, replace the stale `MetadataReviewDialog.tsx:604` citation with `the "runtime differs by" chip in web/src/components/audiobooks/MetadataReviewDialog.tsx` (symbol-based; survives drift).
5. Append one dated line at the end of the section: `<!-- 2026-07-10 closeout: section verified fully shipped; prod scan-duration-mismatch job (fixed 120s threshold) reported <N> mismatches — INIT-10 TASK-03 -->` with the real count.
6. Touch ONLY this section of TODO.md; keep edits purely corrective — no reflow, no other sections.
7. Bump the TODO.md header (version + last-edited); keep the guid.
8. Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path added).

## How to test

```bash
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green. (Docs-only PR; Minimal CI green is the merge condition.)

## Acceptance criteria

- [ ] `grep -c "MetadataReviewDialog.tsx:604" TODO.md` returns 0.
- [ ] `grep -n "2026-07-10 closeout" TODO.md` hits, and the line contains a NUMERIC mismatch count from the live scan.
- [ ] Anti-over-suppression: N/A
- [ ] Tests green (Minimal CI); vet/lint clean.
- [ ] File headers bumped on every changed file.
- [ ] Zero prod writes performed (the scan job only reads books and logs; triggering it creates an operation record, which is operational telemetry, not library data — report any actual write temptation via AskUserQuestion instead).

## Commit message

```
docs(todo): close out Audible runtime-mismatch section with prod scan evidence (AUDIBLE-RUNTIME-MISMATCH)

Section was fully shipped (PRs #549/#561) but the master plan listed it open
and DUR-1 cited a drifted line number. Citation made symbol-based; fresh
read-only prod scan count recorded as closing evidence.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/ux-small-items-audible-runtime-closeout
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "2026-07-10 closeout" TODO.md` hits AND `grep -c "MetadataReviewDialog.tsx:604" TODO.md` returns 0, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; prod was never written to (read-only scan), so nothing else to undo.
