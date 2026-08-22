<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-009-teach-the-abs-fixture-capture-harness-to-record-.md -->
<!-- version: 1.0.0 -->
<!-- guid: b49ef6d6-f483-4d5d-ad23-5aeae3e74359 -->
<!-- last-edited: 2026-08-21 -->

# TASK-009 — Teach the ABS fixture-capture harness to record request headers (TODO.md L2568)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · ci-tooling subagent · **Why:** Small, self-contained script change with a clear existing pattern (KEPT_HEADERS) to extend to the request side. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 2568 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`deviceInfo.deviceType` is always `\"unknown\"`, a" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-009-teach-the-abs-fixture-capture-harness-to-record-" -b agent/ci-tooling-009-teach-the-abs-fixture-capture-harness-to-record- origin/main
cd "$REPO/.worktrees/ci-tooling-009-teach-the-abs-fixture-capture-harness-to-record-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a request-headers capture to scripts/abs_capture_fixtures.py's fixture-writing logic, mirroring the existing KEPT_HEADERS response-header capture (line 37-90), so a subsequent re-capture preserves the User-Agent (and any other client-identifying headers) that produced each fixture. Do not attempt to derive deviceType in this change — that is a separate, blocked follow-up (part 2 of this item).

## Background (verify before editing)

- The real ABS server derived deviceType='wearable' for a request whose captured JSON body carried only clientName and deviceId — the distinguishing signal must have been in the User-Agent header, which the harness currently discards.
- 0 of 28 existing fixtures record any request header (verified: no `req.headers` write anywhere in the harness).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'header\|Header' scripts/abs_capture_fixtures.py   # hits at L37,L89-90 (response headers) and L121/135/144 (auth headers SENT, not recorded) — the harness only preserves response headers, never request headers
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In scripts/abs_capture_fixtures.py, find where each fixture JSON is written (the function containing the KEPT_HEADERS response-header block around line 89).
2. Add a REQUEST_HEADERS_TO_KEEP set (start with at least 'user-agent') alongside the existing KEPT_HEADERS constant.
3. At the point the outgoing request is issued (the `sess.get`/`sess.post`/etc. call), capture the headers actually sent (session default headers merged with any per-call headers) and filter through REQUEST_HEADERS_TO_KEEP.
4. Add a 'request_headers' key to the written fixture JSON structure, alongside the existing 'headers' (response) key.
5. Re-run the harness against the real ABS docker-compose instance (testdata/abs-fixtures/docker-compose.yml) to confirm at least one regenerated fixture now has a non-empty request_headers.user-agent.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_009.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Auth headers (Authorization, x-return-tokens, x-refresh-token) must NOT be captured into fixtures — they are credentials, not diagnostic data; keep REQUEST_HEADERS_TO_KEEP to a strict allowlist, mirroring KEPT_HEADERS' allowlist approach rather than capturing all headers.

## Tests

- No new Go test required for the script itself; verify manually per step 5 since this is a Python capture tool, not shipped application code.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] grep -c 'request_headers' testdata/abs-fixtures/*.json returns >0 after a re-capture run.
- [ ] grep -n 'REQUEST_HEADERS_TO_KEEP' scripts/abs_capture_fixtures.py returns 1 hit.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_009.md`.

## Commit message

```
fix(ci-tooling): Teach the ABS fixture-capture harness to record request head (TODO L2568)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`grep -c 'request_headers' testdata/abs-fixtures/*.json returns >0 after a re-capture run.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This unblocks part 2 (deriving the UA->deviceType mapping) but is independently useful as harness hygiene even if part 2 never happens.
