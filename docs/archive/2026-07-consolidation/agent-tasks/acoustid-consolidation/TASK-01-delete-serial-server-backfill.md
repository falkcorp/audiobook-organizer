<!-- file: docs/agent-tasks/acoustid-consolidation/TASK-01-delete-serial-server-backfill.md -->
<!-- version: 1.0.0 -->
<!-- guid: de88000b-3250-4beb-b47f-3b3bef4c218e -->
<!-- last-edited: 2026-07-05 -->

# TASK-01 — Delete the serial server-side AcoustID backfill and route startup to the parallel plugin op (CONC-9)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Opus-class · go-backend subagent · **Why:** Delete/redirect a code path with caller wiring + a possibly-shared helper — dangling-reference risk. · ⚠ review-critical · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/acoustid-consolidation-delete-serial-server-backfill" -b agent/acoustid-consolidation-delete-serial-server-backfill origin/main
cd "$REPO/.worktrees/acoustid-consolidation-delete-serial-server-backfill"
git rebase origin/main
```

(Protocol is also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove the single s.backfillAcoustIDs(s.bgCtx) call at server_lifecycle.go:~910 and delete internal/server/acoustid_backfill.go, ensuring the already-registered parallel plugin op `acoustid.backfill` (internal/plugins/acoustid/backfill.go, uses registry.RunItems) covers the startup fingerprinting need. VERIFY no other symbol in acoustid_backfill.go (e.g. synthesizeBookSignatureForBook, fingerprintBookFile) is referenced elsewhere before deleting — if a helper is shared, move it rather than delete it.

This is a **delete-and-redirect** task, NOT a pool-add: the parallel path already exists in the plugin op `acoustid.backfill` (`internal/plugins/acoustid/backfill.go`, which uses `registry.RunItems`). Your job is to remove the serial duplicate and its single caller, after proving nothing else depends on the file.

## Background (verify before editing)

- Fix pattern (from `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`): Consolidation (delete/redirect), not a pool-add: verify caller wiring, ensure the parallel op covers the case (fix-pattern: delete the serial duplicate).
- Current behavior: The server variant duplicates the plugin's work but serially and with an explicit per-item sleep. The plugin sibling does the same via registry.RunItems with Concurrency.
- **Correctness constraint (READ TWICE):** Deletion/redirect task — the risk is a dangling reference. Grep every symbol defined in acoustid_backfill.go for external callers before removing. This is why it is Opus-class and ⚠ review-critical, not mechanical.
- Source audit finding: `CONC-9` in `docs/audits/2026-07-05-concurrency-single-threaded-hotspots.md`.

- **Re-verify these anchors before editing** — line numbers drift, they are a starting point only:
  ```bash
  grep -n "func (s \*Server) backfillAcoustIDs" internal/server/acoustid_backfill.go   # expect: 1 hit (~line 109)
  grep -rn "s.backfillAcoustIDs(" --include=*.go .   # expect: 1 hit: server_lifecycle.go ~line 910
  grep -rn "func (p \*Plugin) runBackfill\|defID:  \"acoustid.backfill\"" --include=*.go internal/plugins/   # expect: runBackfill ~backfill.go:56; op id in maintenance/optimize.go:79
  ```

## Step-by-step

1. Confirm the single caller: `grep -rn "s.backfillAcoustIDs(" --include=*.go .` — expect exactly ONE hit in `internal/server/server_lifecycle.go` (~line 910). If there is more than one, STOP and report.
2. Enumerate every symbol defined in `internal/server/acoustid_backfill.go` and check for external references BEFORE deleting: `for sym in $(grep -oE 'func [A-Za-z]*\(?s? ?\*?Server?\)? ?[A-Za-z]+' internal/server/acoustid_backfill.go); do :; done` — or more simply, for each top-level func/type in that file, `grep -rn "<name>" --include=*.go . | grep -v acoustid_backfill.go`. Any hit means the symbol is shared (e.g. `synthesizeBookSignatureForBook`, `fingerprintBookFile`) — MOVE it to a shared location instead of deleting it. Record what you moved.
3. Remove the `s.backfillAcoustIDs(s.bgCtx)` call at `internal/server/server_lifecycle.go:~910` (and any now-unused imports it required).
4. Delete `internal/server/acoustid_backfill.go` (`git rm`) — unless step 2 found a shared symbol, in which case delete only the serial `backfillAcoustIDs` + its private helpers and keep/move the shared ones.
5. Confirm the plugin op `acoustid.backfill` is still registered and covers the startup need: `grep -rn 'defID:  "acoustid.backfill"' --include=*.go internal/plugins/` — expect 1 hit. If startup previously relied on the server call firing automatically, note whether the plugin op runs on startup or on-demand and flag any gap in the PR description (do NOT silently drop startup fingerprinting).
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go build ./...
go test ./internal/server/... ./internal/plugins/acoustid/... -count=1
make ci
```

## Acceptance criteria

- [ ] `internal/server/acoustid_backfill.go` no longer defines the serial `backfillAcoustIDs` (verify: `! grep -rn "func (s \*Server) backfillAcoustIDs" internal/server/`).
- [ ] No caller remains (verify: `grep -rn "s.backfillAcoustIDs(" --include=*.go .` returns 0 hits).
- [ ] The parallel plugin op is intact (verify: `grep -rn 'defID:  "acoustid.backfill"' --include=*.go internal/plugins/` returns 1 hit).
- [ ] Any shared symbol from the old file (if step 2 found one) was MOVED, not lost (verify: `go build ./...` clean).
- [ ] Anti-over-suppression: N/A (this task deletes a serial duplicate; correctness = the parallel plugin op still covers the case, verified above — no filter/guard/veto path is added).
- [ ] `make ci` green; `go vet` clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-07-05" <file>`).

## Commit message

```
refactor(server): delete the serial server-side AcoustID backfill and route startup to the parallel plugin op (CONC-9)

The serial server-side backfill duplicated the already-parallel plugin op
acoustid.backfill (registry.RunItems). Remove the duplicate and its single caller;
startup fingerprinting is covered by the plugin op.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts; the coordinator owns push/PR/merge.

## Idempotency / Rollback

Idempotency: `! test -f internal/server/acoustid_backfill.go && ! grep -rq "s.backfillAcoustIDs(" --include=*.go .` — if the file is already gone AND no caller remains, this task is complete; run the acceptance checks instead of re-applying. Rollback: revert the single commit to restore the file and its call site; no data or schema is touched.
