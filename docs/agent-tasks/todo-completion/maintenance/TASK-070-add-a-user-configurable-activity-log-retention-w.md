<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-070-add-a-user-configurable-activity-log-retention-w.md -->
<!-- version: 1.0.0 -->
<!-- guid: 32d572b1-e25f-44d4-8f5a-5bad671c0f60 -->
<!-- last-edited: 2026-08-21 -->

# TASK-070 — Add a user-configurable activity-log retention window (default 7 days, 0=never) (TODO.md L3488)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** spans backend config + an existing maintenance op + a new frontend control; needs the 0=never semantics wired correctly through both layers · **Depends on:** none · **Wave:** 5

Source: `TODO.md` line 3488 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Activity log: auto-compact after 7 days, user-co" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-05.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-070-add-a-user-configurable-activity-log-retention-w" -b agent/maintenance-070-add-a-user-configurable-activity-log-retention-w origin/main
cd "$REPO/.worktrees/maintenance-070-add-a-user-configurable-activity-log-retention-w"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add config.AppConfig.ActivityLogRetentionDays (json key activity_log_retention_days, default 7, 0 = never compact) and use it to drive the existing maintenance.cleanup-activity-log op's compaction cutoff instead of (or alongside) the current ActivityLogCompactionDays constant, then expose a control to edit it directly on the ActivityLog page (not buried in general Settings), with a header line reading 'entries older than N days are compacted automatically' (or 'automatic compaction is off' when N=0).

## Background (verify before editing)

- internal/plugins/maintenance/cleanup.go:150-165 runCleanupActivityLog already calls p.deps.CompactActivityLog(ctx, p.deps.ActivityLogCompactionDays(), p.deps.ActivityLogRetentionChangeDays(), p.deps.ActivityLogRetentionDebugDays())
- internal/server/server_maintenance_deps.go:233-266 CompactActivityLog treats compactionDays<=0 as 'fall back to 14', not 'skip'; this must change to 'skip entirely' for the new 0=never key
- internal/server/audiobooks_helpers.go:211-217 runAutoPurgeSoftDeleted already implements a real 0=never gate as precedent: `if config.AppConfig.PurgeSoftDeletedAfterDays <= 0 { return }`
- The maintenance.cleanup-activity-log op runs on schedule '0 0 * * *' (midnight daily) — internal/plugins/maintenance/cleanup.go:131

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n activity_log_retention_days internal/config/config.go   # 0 hits — no unified activity_log_retention_days key exists yet
  grep -n -A3 "func (s \*Server) CompactActivityLog" internal/server/server_maintenance_deps.go   # shows 'if compactionDays <= 0 { compactionDays = 14 }' at L238-240 — existing ActivityLogCompactionDays defaults to 14 and treats <=0 as 'use 14', not 'never'
  grep -n 'maintenance.cleanup-activity-log' internal/plugins/maintenance/cleanup.go   # 3 hits, def at L133 — the cleanup-activity-log op already exists on a midnight-daily schedule
  grep -n 'PurgeSoftDeletedAfterDays <= 0' internal/server/audiobooks_helpers.go   # 1 hit, L215, 'if config.AppConfig.PurgeSoftDeletedAfterDays <= 0 { return }' — an existing field uses a working 0=never sentinel as design precedent
  ```

### Reuse — don't invent

- Use `0=never sentinel pattern` in `internal/server/audiobooks_helpers.go` (verify: `grep -n 'PurgeSoftDeletedAfterDays <= 0' internal/server/audiobooks_helpers.go`) — do NOT write a parallel helper.
- Use `runCleanupActivityLog / CompactActivityLog wiring` in `internal/plugins/maintenance/cleanup.go` (verify: `grep -n runCleanupActivityLog internal/plugins/maintenance/cleanup.go`) — do NOT write a parallel helper.
- Use `existing per-setting PUT config pattern for a retention-days field` in `web/src/hooks/useSettingsHandlers.ts` (verify: `grep -n purge_soft_deleted_after_days web/src/hooks/useSettingsHandlers.ts`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/config/config.go, add `ActivityLogRetentionDays int json:"activity_log_retention_days"` near the existing ActivityLogCompactionDays/ActivityLogRetentionChangeDays/ActivityLogRetentionDebugDays fields (~L783-785), and set its default to 7 in the defaults block (~L2352-2354).
2. In internal/plugins/maintenance/deps.go, add `ActivityLogRetentionDays() int` to the ServerDeps interface next to the other three ActivityLog*Days() methods (~L337-339).
3. In internal/server/server_maintenance_deps.go, add `func (s *Server) ActivityLogRetentionDays() int { return config.AppConfig.ActivityLogRetentionDays }` next to ActivityLogCompactionDays (~L302-304).
4. Change CompactActivityLog's compaction-cutoff logic (server_maintenance_deps.go:238-241) so that when the days value is 0 the compaction step is skipped entirely (mirroring the PurgeSoftDeletedAfterDays<=0 pattern), rather than falling back to 14. Keep negative values treated the same as 0.
5. In internal/plugins/maintenance/cleanup.go's runCleanupActivityLog, pass p.deps.ActivityLogRetentionDays() as the primary compaction-days argument instead of p.deps.ActivityLogCompactionDays() (or thread both through if the coordinator decides compaction and change-summarization should stay independently configurable — flag this as a sub-decision, not a blocker).
6. Add a config GET/PUT round trip for activity_log_retention_days in web/src/services/api.ts's config type (mirroring purge_soft_deleted_after_days at line 901).
7. On web/src/pages/ActivityLog.tsx, add a small settings control (a numeric TextField or similar) near the page header that reads/writes activity_log_retention_days via the existing config API, and a header line rendering 'entries older than {days} days are compacted automatically' or 'automatic compaction is off' when days===0.
8. Bump version headers on every touched file per repo convention.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_070.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- 0 must mean 'never compact', distinct from an absent/omitted config key which should still default to 7 via viper's SetDefault or the AppConfig defaults block
- negative values should be treated identically to 0 (never), not as an error
- changing the value mid-run should not affect an already-in-flight compaction pass

## Tests

- internal/server/server_maintenance_deps_test.go: new test asserting CompactActivityLog(ctx, 0, changeDays, debugDays) does NOT call activityService.CompactByDay
- internal/server/server_maintenance_deps_test.go: existing/positive-value test asserting CompactActivityLog(ctx, 7, ...) DOES call CompactByDay with a cutoff of now-7days (anti-over-suppression: a positive retention value must still compact, proving the 0-skip branch didn't swallow the normal path)
- web/src/pages/ActivityLog.test.tsx: new test asserting the retention control renders the current value and PUTs an update on change

Anti-over-suppression test: `server_maintenance_deps_test.go: TestCompactActivityLog_PositiveRetentionStillCompacts` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/config/... ./internal/plugins/maintenance/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n activity_log_retention_days internal/config/config.go` returns >=2 hits (field + default)
- [ ] `make ci` passes
- [ ] Manually: setting the control to 0 on the ActivityLog page and running the op leaves entries uncompacted; setting it to a positive N compacts entries older than N days
- [ ] Anti-over-suppression test: `server_maintenance_deps_test.go: TestCompactActivityLog_PositiveRetentionStillCompacts` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/config/... ./internal/plugins/maintenance/... ./internal/server/... -count=1 && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_070.md`.

## Commit message

```
feat(maintenance): Add a user-configurable activity-log retention window (defau (TODO L3488)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``grep -n activity_log_retention_days internal/config/config.go` returns >=2 hits (field + default)`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

D111 'Stored zeros must not shadow defaults' (docs/handoffs/2026-08-14-task-board.md:83) is still open design status ('⬜ design') and is the general version of this field's 0-vs-absent ambiguity; PurgeSoftDeletedAfterDays already ships the same 0=never pattern without D111 being resolved, so this is not a hard blocker, but flag the D111 dependency to the coordinator in case a general resolution lands first and changes the config-loading convention.
