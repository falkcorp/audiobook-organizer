<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-078-add-a-user-configurable-activity-log-retention-w.md -->
<!-- version: 1.0.0 -->
<!-- guid: 078d4604-af4c-4983-894f-df57c288e4d3 -->
<!-- last-edited: 2026-08-21 -->

# TASK-078 — Add a user-configurable activity-log retention window (default 7 days, 0=never) (TODO.md L3488)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** spans backend config + an existing maintenance op + a new frontend control; needs the 0=never semantics wired correctly through both layers · **Depends on:** none · **Wave:** 6

Source: `TODO.md` line 3488 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Activity log: auto-compact after 7 days, user-co" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-05.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-078-add-a-user-configurable-activity-log-retention-w" -b agent/maintenance-078-add-a-user-configurable-activity-log-retention-w origin/main
cd "$REPO/.worktrees/maintenance-078-add-a-user-configurable-activity-log-retention-w"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add config.AppConfig.ActivityLogRetentionDays (json key activity_log_retention_days, default 7, 0 = never compact) and use it to drive the existing maintenance.cleanup-activity-log op's compaction cutoff instead of (or alongside) the current ActivityLogCompactionDays constant, then expose a control to edit it directly on the ActivityLog page (not buried in general Settings), with a header line reading 'entries older than N days are compacted automatically' (or 'automatic compaction is off' when N=0).

## Background (verify before editing)

- internal/metadata/cover.go:117 already calls validateCoverURL() (scheme allowlist) before the fetch at cover.go:137, and the client used there is built with a custom safeCoverDialContext (cover.go:71-89) that resolves the host and rejects private/loopback/link-local IPv4 AND IPv6 ranges. This is real, comprehensive SSRF protection already in place for alert #662.
- internal/covers/covers.go:82's http.Get(coverURL) has exactly one caller: internal/server/covers.go's handleCoverProxy, which calls covers.IsAllowedCoverSource(coverURL) (a 4-domain allowlist: openlibrary/google books/amazon) and rejects before ever calling FetchAndCacheCover. This is real, comprehensive SSRF protection already in place for alert #645.
- internal/metadata/cover_test.go already asserts a 169.254.169.254 cover URL is SSRF-blocked; internal/covers/covers_test.go already has TestIsAllowedCoverSource asserting both the blocked case and several legitimate URLs pass. Do not re-add these tests — verify they exist and pass.

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

1. Read internal/metadata/cover.go top-to-bottom around lines 20-137: confirm validateCoverURL (scheme allowlist) and safeCoverDialContext (private/loopback/link-local IPv4+IPv6 block via DNS-resolved-IP check) are both wired into the client used at line 137.
2. Read internal/covers/covers.go's only caller (internal/server/covers.go:24-45) and confirm covers.IsAllowedCoverSource(coverURL) gates every call to FetchAndCacheCover.
3. Given both sites have real, host/IP-resolved validation already present, add `// lgtm[go/request-forgery]` directly above cover.go:137 and covers.go:82, each citing the exact validating function/file:line found above (do not add a duplicate/parallel validator).
4. Run `grep -n 169.254 internal/metadata/cover_test.go` and `grep -n TestIsAllowedCoverSource internal/covers/covers_test.go` to confirm the anti-over-suppression and blocked-case tests already exist and pass; do not add duplicate tests.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_078.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
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
make ci && npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `grep -n activity_log_retention_days internal/config/config.go` returns >=2 hits (field + default)
- [ ] `make ci` passes
- [ ] Manually: setting the control to 0 on the ActivityLog page and running the op leaves entries uncompacted; setting it to a positive N compacts entries older than N days
- [ ] Anti-over-suppression test: `server_maintenance_deps_test.go: TestCompactActivityLog_PositiveRetentionStillCompacts` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_078.md`.

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
