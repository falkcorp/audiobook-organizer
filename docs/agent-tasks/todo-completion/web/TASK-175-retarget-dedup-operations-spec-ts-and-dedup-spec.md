<!-- file: docs/agent-tasks/todo-completion/web/TASK-175-retarget-dedup-operations-spec-ts-and-dedup-spec.md -->
<!-- version: 1.0.0 -->
<!-- guid: af7061bc-c3ee-4c30-8969-eb47b2d4b09a -->
<!-- last-edited: 2026-08-21 -->

# TASK-175 — Retarget dedup-operations.spec.ts and dedup.spec.ts resolve-production status mocks to v2 (TODO.md L4960)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Mechanical but must match the exact v2 response envelope (data.operation with progress_current/progress_total/progress_message) that two already-fixed sibling mocks in this same repo demonstrate; a naive URL-only fix (as the item's own measurement showed) does not produce a passing test. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4960 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Six E2E mocks point at operation URLs that no lo" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-175-retarget-dedup-operations-spec-ts-and-dedup-spec" -b agent/web-175-retarget-dedup-operations-spec-ts-and-dedup-spec origin/main
cd "$REPO/.worktrees/web-175-retarget-dedup-operations-spec-ts-and-dedup-spec"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In both web/tests/e2e/dedup-operations.spec.ts:118 and web/tests/e2e/dedup.spec.ts:163, change the mocked route from '**/api/v1/operations/*/status' to '**/api/v1/operations/v2/*' (or a more specific '**/api/v1/operations/v2/resolve-prod-1' if only that id is polled in the test) and change the fulfilled body from the flat legacy shape ({id, status, progress, total, message}) to the v2 envelope: { data: { operation: { id: 'resolve-prod-1', def_id: 'authors.resolve-production', status: 'completed', progress_current: 100, progress_total: 100, progress_message: 'Done', error_message: null, queued_at: <ISO string> }, logs: [] } }.

## Background (verify before editing)

- web/src/services/api.ts:2044 getOperationStatus / getOperationV2 (L545) fetches GET /operations/v2/:id and requires the row under data.operation (L551, 'Unexpected response shape from GET /api/v1/operations/v2/{id}' if absent).
- web/tests/e2e/dynamic-ui-interactions.spec.ts:49 and web/tests/e2e/transcode-and-counting.spec.ts:97 already demonstrate the correct v2 mock shape in this same repo — both were already retargeted (confirmed at HEAD), so this task is applying the identical, already-proven pattern to the two remaining stale files.
- The TODO item's own 2026-08-16 measurement warns that retargeting the URL alone (without fixing the body shape) does not change dynamic-ui-interactions.spec.ts's pass/fail count — so this task must copy the FULL v2 envelope shape, not just the URL glob.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "operations/\*/status" web/tests/e2e/dedup-operations.spec.ts   # 1 hit, L118 — dedup-operations.spec.ts still mocks the retired /status URL
  grep -n "operations/\*/status" web/tests/e2e/dedup.spec.ts   # 1 hit, L163 — dedup.spec.ts still mocks the retired /status URL
  grep -n 'operations/v2/' web/src/services/api.ts   # >=1 hit including L545 — the live client polls GET /operations/v2/:id
  grep -n 'progress_current\|progress_total\|progress_message' web/src/services/api.ts   # >=1 hit — getOperationStatus reads the v2 field names
  ```

### Reuse — don't invent

- Use `routeRunningStatus (v2-shaped mock pattern to copy)` in `web/tests/e2e/dynamic-ui-interactions.spec.ts` (verify: `grep -n 'operations/v2/\*' web/tests/e2e/dynamic-ui-interactions.spec.ts`) — do NOT write a parallel helper.
- Use `transcode poll mock (another already-fixed v2-shaped example, response envelope under data.operation)` in `web/tests/e2e/transcode-and-counting.spec.ts` (verify: `grep -n 'operations/v2/op-transcode-1' web/tests/e2e/transcode-and-counting.spec.ts`) — do NOT write a parallel helper.

## Step-by-step

1. In web/tests/e2e/dedup-operations.spec.ts, locate the page.route call at line 118 matching '**/api/v1/operations/*/status'.
2. Change the route glob to '**/api/v1/operations/v2/*'.
3. Replace the fulfilled JSON body's flat shape ({ id: 'resolve-prod-1', status: 'completed', progress: 100, total: 100, message: 'Done' }) with the v2 envelope shown in the goal field above, keeping id='resolve-prod-1' and status='completed' but renaming progress/total/message to progress_current/progress_total/progress_message and adding def_id (use 'authors.resolve-production' unless grepping web/src for the real def_id used by the resolve-production trigger shows a different string — verify with: grep -n 'resolve-production' web/src/services/api.ts).
4. Repeat the identical change in web/tests/e2e/dedup.spec.ts at line 163 (same fix, same test scenario, different file).
5. Run the two specs locally: npx playwright test web/tests/e2e/dedup-operations.spec.ts web/tests/e2e/dedup.spec.ts --grep 'Find Real Author' to confirm the 'clicking Find Real Author calls resolve API' / 'triggers API call' tests still pass with the corrected mock.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_175.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the resolve-production trigger's real def_id differs from 'authors.resolve-production', the mock's def_id field is cosmetic (getOperationStatus does not filter on it), so getting it exactly right is nice-to-have, not required for the test to pass — but check anyway rather than guessing, since a coordinator-level convention may expect it to match wire_operations_routes.go's registered ID.

## Tests

- web/tests/e2e/dedup-operations.spec.ts: 'clicking Find Real Author calls resolve API' — still passes after the mock is retargeted to v2 (this is the existing test; verify it doesn't regress).
- web/tests/e2e/dedup.spec.ts: 'clicking Find Real Author triggers API call' — same.

Anti-over-suppression test: `N/A — this is a mock-accuracy fix, not a new filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -n "operations/v2" web/tests/e2e/dedup-operations.spec.ts returns a hit where line 118 used to be
- [ ] grep -n "operations/v2" web/tests/e2e/dedup.spec.ts returns a hit where line 163 used to be
- [ ] grep -n 'progress_current' web/tests/e2e/dedup-operations.spec.ts and web/tests/e2e/dedup.spec.ts each return >=1 hit
- [ ] npx playwright test web/tests/e2e/dedup-operations.spec.ts web/tests/e2e/dedup.spec.ts exits 0
- [ ] Anti-over-suppression test: `N/A — this is a mock-accuracy fix, not a new filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_175.md`.

## Commit message

```
refactor(web): Retarget dedup-operations.spec.ts and dedup.spec.ts resolve- (TODO L4960)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Per the TODO item's own 2026-08-16 measurement, fixing this mock is the RIGHT engineering fix regardless of whether it changes any test's pass/fail count — a stale mock 'fails silently' by falling through to a 404 rather than erroring loudly, which is a correctness bug in the test fixture even if today it happens not to flip a result.
