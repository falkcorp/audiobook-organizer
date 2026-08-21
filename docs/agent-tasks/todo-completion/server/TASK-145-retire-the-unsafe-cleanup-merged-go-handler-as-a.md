<!-- file: docs/agent-tasks/todo-completion/server/TASK-145-retire-the-unsafe-cleanup-merged-go-handler-as-a.md -->
<!-- version: 1.0.0 -->
<!-- guid: a60cf7ad-12a5-46fb-8e66-b96d1ef971b1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-145 — Retire the unsafe cleanup_merged.go handler as a guarded no-op (owner decision: MEASURE-AND-STOP, no bulk removal) (TODO.md L10372)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · server subagent · **Why:** Small diff, but it is a prod data-loss guard on a route that currently CAN delete real library tracks — precision matters more than size · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10372 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**iTunes 2-way-sync P3 (cleanup) — decision: MEASU" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-13.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-145-retire-the-unsafe-cleanup-merged-go-handler-as-a" -b agent/server-145-retire-the-unsafe-cleanup-merged-go-handler-as-a origin/main
cd "$REPO/.worktrees/server-145-retire-the-unsafe-cleanup-merged-go-handler-as-a"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make POST /api/v1/itunes/cleanup-merged a guarded no-op: dry_run continues to return the preview (harmless, useful for future re-derivation), but the apply path (dry_run=false or omitted) must refuse to call SafeWriteITL and instead respond with a clear 'retired, dry-run only' message. Do not build any new bulk-removal machinery — this is a retirement, not a redesign.

## Background (verify before editing)

- Owner's P0 cleanup provenance census (97,999 .itl tracks) found provable merge orphans = 1 and SHA-gated removable = 0 — i.e. there is essentially nothing safe to remove today, and the existing IsPrimaryVersion==false criterion this handler's preview/removal logic depends on is separately documented (TODO L10435) as UNSAFE because it would delete real chapter files, not just true duplicates.
- cleanupMergedHandler is registered and reachable in production today (internal/server/itl_cleanup.go implements POST /api/v1/itunes/cleanup-merged) with no guard preventing an operator from calling it with dry_run=false and actually removing tracks.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'SafeWriteITL(itlPath, \*ops)' internal/server/itl_cleanup.go   # 1 hit ~L53 — cleanupMergedHandler is live and can still apply a real ITL track removal (dry_run=false path)
  grep -n 'no-op\|noop\|disabled\|guard' internal/server/itl_cleanup.go   # 0 hits — the handler has no disabled/no-op guard today
  grep -n 'IsPrimaryVersion' internal/itunes/cleanup_merged.go   # 1 hit ~L87 — the removal criterion the handler relies on is IsPrimaryVersion==false, which the owner separately flagged unsafe (see TODO L10435)
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/server/itl_cleanup.go's cleanupMergedHandler, after computing ops/preview via itunes.ComputeMergedTrackCleanup (line 34), change the `if c.Query("dry_run") == "true"` branch (line 40) to be the ONLY successful path: keep it as-is, but replace the block below it (lines 44-60, the apply path) with an unconditional refusal — e.g. `httputil.RespondWithSuccess(c, http.StatusGone, gin.H{"applied": false, "preview": preview, "error": "cleanup-merged apply is retired (P3 decision: MEASURE-AND-STOP); dry_run=true only"})` — never calling itunesservice.SafeWriteITL.
2. Update the function's doc comment (lines 24-26) to state the apply path is permanently retired, citing the P0 census finding (0 SHA-gated removable) and pointing to docs/specs/2026-07-23-itunes-2way-p0-findings.md §F4.
3. Do NOT remove ComputeMergedTrackCleanup or the dry_run preview path — the item explicitly wants the measurement capability kept, only the apply/removal path retired.
4. Bump the file's version header.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server_145.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- ops.IsEmpty() (line 45 today) becomes unreachable/irrelevant once apply always refuses — remove or repurpose that branch rather than leaving dead code.

## Tests

- internal/server/itl_cleanup_test.go: TestCleanupMergedHandler_DryRun_StillReturnsPreview — dry_run=true still returns a 200 with the preview payload (proves the measurement capability survives).
- internal/server/itl_cleanup_test.go: TestCleanupMergedHandler_Apply_RefusesAndNeverWrites — dry_run=false (or omitted) returns the retirement error, and (via a spy/mock on SafeWriteITL or by asserting the .itl fixture file's bytes are unchanged on disk after the call) proves no write occurred.

Anti-over-suppression test: `TestCleanupMergedHandler_DryRun_StillReturnsPreview is the anti-over-suppression check — proves the retirement did not also kill the harmless measurement path` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/server/... -run TestCleanupMergedHandler` passes both new tests
- [ ] a manual curl of `POST /api/v1/itunes/cleanup-merged?dry_run=false` against a test server returns the retirement message and the target .itl file's mtime/bytes are unchanged
- [ ] Anti-over-suppression test: `TestCleanupMergedHandler_DryRun_StillReturnsPreview is the anti-over-suppression check — proves the retirement did not also kill the harmless measurement path` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server_145.md`.

## Commit message

```
refactor(server): Retire the unsafe cleanup_merged.go handler as a guarded no- (TODO L10372)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

This is a live prod data-loss path today (an operator or a stale UI button could still trigger a real removal via an unsafe criterion) — treat as review-critical and prioritize over the lower-urgency ABS-SYNC items in this scope.
