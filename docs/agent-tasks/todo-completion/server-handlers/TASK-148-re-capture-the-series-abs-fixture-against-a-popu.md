<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-148-re-capture-the-series-abs-fixture-against-a-popu.md -->
<!-- version: 1.0.0 -->
<!-- guid: bc88100d-d7e2-428a-b0fb-ca533bb72d80 -->
<!-- last-edited: 2026-08-21 -->

# TASK-148 — Re-capture the series ABS fixture against a populated library (it currently contains zero series) (TODO.md L491)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · server-handlers subagent · **Why:** Requires actually running a real capture (hitting a populated library's /api/libraries/:id/series endpoint against a live server, presumably real ABS or this server itself pointed at a populated library) and hand-curating the result into fixture format — not a pure code edit, needs an environment with real data. · **Depends on:** TASK-147 · **Wave:** 5

Source: `TODO.md` line 491 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`testdata/abs-fixtures/get_api_libraries_id_seri" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-148-re-capture-the-series-abs-fixture-against-a-popu" -b agent/server-handlers-148-re-capture-the-series-abs-fixture-against-a-popu origin/main
cd "$REPO/.worktrees/server-handlers-148-re-capture-the-series-abs-fixture-against-a-popu"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Re-capture get_api_libraries_id_series.json against a library that actually has series membership (unlike the current empty-library capture), so the fixture can genuinely serve as an oracle for the `books` field's contract (per item L127's fixture-alignment work, which this fixture is also relevant to) rather than proving nothing.

## Background (verify before editing)

- The TODO notes the shape currently used in tests came from the upstream API reference doc instead of a real capture — same failure mode as the sessions fixture holding only 3 items against a page size of 10 (a related, not-in-this-scope fixture gap worth noting but not fixing here).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  python3 -c "import json; d=json.load(open('testdata/abs-fixtures/get_api_libraries_id_series.json')); print(len(d['response']['body']['results']))"   # 0 — the fixture's results array is empty
  ```

### Reuse — don't invent

- Use `whatever capture tooling/process produced the other 27 fixtures (find via a header/comment in one of the newer fixtures, or a docs/testdata README)` in `testdata/abs-fixtures/` (verify: `find testdata/abs-fixtures -iname 'README*' -o -iname '*.md'`) — do NOT write a parallel helper.

## Step-by-step

1. Locate or stand up a populated ABS-compatible library (real audiobookshelf server, or this repo's own server with ABS_API_ENABLED=true pointed at a library with actual series data) to capture against.
2. Issue the real GET /api/libraries/:id/series request against it with a library known to have series membership.
3. Save the response into testdata/abs-fixtures/get_api_libraries_id_series.json in the same {request, response: {body, headers, status}} shape as the existing fixtures.
4. Re-run any test currently asserting against this fixture (search via `grep -rn get_api_libraries_id_series internal/server/handlers/abs/*_test.go`) and confirm it still passes (or now correctly fails if the current handler doesn't yet match the real `books` shape — that would be a genuine finding, not a fixture bug).

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_148.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If no real ABS server is available to capture against, this may need to be re-scoped as 'construct a realistic fixture by hand from the upstream API reference plus a manually-verified real single-series response from THIS server' rather than a true external-oracle capture — note which approach was actually used in the fixture's own accompanying documentation/comment.

## Tests

- Whatever existing test(s) load this fixture — confirmed passing (or a newly-surfaced real gap documented) after the re-capture.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `python3 -c "import json; d=json.load(open('testdata/abs-fixtures/get_api_libraries_id_series.json')); assert len(d['response']['body']['results']) > 0"` succeeds.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_148.md`.

## Commit message

```
fix(server-handlers): Re-capture the series ABS fixture against a populated librar (TODO L491)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Directly feeds item L127's fixture-alignment work in this same scope — coordinate so the two are not done independently and inconsistently.
