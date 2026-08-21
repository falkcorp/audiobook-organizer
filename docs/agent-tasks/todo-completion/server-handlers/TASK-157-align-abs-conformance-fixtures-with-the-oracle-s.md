<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-157-align-abs-conformance-fixtures-with-the-oracle-s.md -->
<!-- version: 1.0.0 -->
<!-- guid: e52c2399-fe4a-48fc-89cb-261eab9850e3 -->
<!-- last-edited: 2026-08-21 -->

# TASK-157 — Align ABS conformance fixtures with the oracle so CompareValues stays green permanently (TODO.md L127)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · server-handlers subagent · **Why:** 767-line fixture-seeding file, 12 currently-red tests to diagnose one by one (distinguishing genuine fixture drift from real bugs from deliberate divergences), plus two named open design questions (track title source, author ordering) that need an actual decision before the corresponding tests can be written — this is judgment-heavy, not mechanical field renaming. · **Depends on:** TASK-153, TASK-154, TASK-155, TASK-156 · **Wave:** 5

Source: `TODO.md` line 127 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Align the ABS conformance fixtures with the orac" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-157-align-abs-conformance-fixtures-with-the-oracle-s" -b agent/server-handlers-157-align-abs-conformance-fixtures-with-the-oracle-s origin/main
cd "$REPO/.worktrees/server-handlers-157-align-abs-conformance-fixtures-with-the-oracle-s"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Re-seed the fake ABS library test fixture (library_fake_test.go) FROM the oracle capture (The Odyssey) so size/duration/publishedYear/track-titles/timeBase etc. match by construction rather than by coincidence, keep the deliberate divergences (user.type='user', Source='audiobook-organizer') whitelisted via assertConformantExcept, get an explicit decision on the two open questions, and leave the gate on (CompareValues:true, already default) permanently green.

## Background (verify before editing)

- The TODO is explicit that most of the 12 currently-red findings are FIXTURE drift (synthetic book vs real Odyssey capture), not code bugs — size 4096 vs 1.20828875e8, duration 9975 vs 9975.480544, publishedYear '800' vs '800BC', track titles 'The Odyssey: Book 06' vs 'odyssey_06_homer_butler_64kb.mp3', timeBase '1/1000' vs '1/14112000'.
- normalize.go:19-20 already documents a firm decision NOT to normalize size/duration/progress/currentTime/startOffset — this task must re-seed the FIXTURE to match those real values, never loosen the comparison for them.
- Two real decisions are named as open: (a) should media.tracks[].title be the filename (as real ABS sends) rather than a display title — this affects mapper.go's track-title construction, not just the fixture; (b) /personalized author ordering — also a real behavior question, not fixture drift.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'CompareValues: true' internal/server/handlers/abs/abs_test.go internal/server/handlers/abs/library_fake_test.go   # 2 hits, matching ABS-N2's evidence — the value gate is already default-on, so this item's premise ('so the value gate can be turned on permanently') is partially satisfied already — the remaining gap is the 12 red tests, not the gate switch itself
  wc -l internal/server/handlers/abs/library_fake_test.go   # line count near 767 (may have grown/shrunk slightly since the TODO was written) — library_fake_test.go is the large fixture-seeding file this item says is bounded-but-not-small (767 lines per the TODO)
  ```

### Reuse — don't invent

- Use `assertConformantExcept + allowance map (already exists for whitelisting deliberate divergences)` in `internal/server/handlers/abs/library_fake_test.go` (verify: `grep -n 'func assertConformantExcept\|type allowance' internal/server/handlers/abs/library_fake_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. Run the ABS handler test suite and capture the current 12 failing test names: `go test ./internal/server/handlers/abs/... -run Conformance -v 2>&1 | grep -E 'FAIL|--- FAIL'` (adjust the -run filter to whatever actually matches the conformance-tagged tests).
2. For each failure, classify it: fixture-drift (re-seed library_fake_test.go's synthetic book fields to match the Odyssey oracle's real values) vs. deliberate (add/confirm an assertConformantExcept allowance) vs. real-decision-needed (the two named ones).
3. Re-seed library_fake_test.go's fake book/file/track construction so size, duration, publishedYear, timeBase, and track titles are copied from (or computed to match) the oracle fixture's actual values, not hand-picked round numbers.
4. For user.type and Source: confirm both are already covered by an assertConformantExcept allowance (they should be, per dto.go's existing comment at line ~276); if not yet whitelisted under the now-default-on strict gate, add them explicitly with a comment citing this decision.
5. Get the owner's decision on (a) track title = filename vs display title, and (b) /personalized author ordering — these are real product-behavior questions, not test-fixture questions; do NOT guess an answer to unblock the fixture work, leave those two specific assertions using assertConformantExcept with an explicit TODO comment until decided.
6. Once all 12 are resolved (fixed, whitelisted, or explicitly deferred per step 5), confirm the full abs test package is green with the gate on and stays that way.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_157.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Re-seeding the fixture to match one specific book (The Odyssey) risks over-fitting the fake library to that book's exact shape in a way that stops exercising OTHER code paths (e.g. multi-author books, series membership) that the synthetic data previously covered for other tests sharing the same fixture — check whether library_fake_test.go's fixture is shared across non-conformance tests before wholesale replacing it; if so, consider a SEPARATE oracle-matched fixture used only by the conformance tests rather than mutating the shared one.

## Tests

- The existing conformance test suite itself IS the test surface here — no new test files needed, only fixture data changes inside library_fake_test.go and possibly new/updated allowance entries.

Anti-over-suppression test: `Do NOT chase green by widening normalize.go's normalized-field list (size/duration/progress/currentTime/startOffset) — the TODO explicitly forbids this ('that is the end of what normalization can honestly fix'); the only acceptable levers are re-seeding fixture data and adding narrowly-scoped assertConformantExcept allowances with named justifications.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/abs/... -v` is fully green (0 failures) after the fixture re-seed, with any remaining deliberately-deferred assertions clearly marked via assertConformantExcept and a comment, not silently skipped.
- [ ] Anti-over-suppression test: `Do NOT chase green by widening normalize.go's normalized-field list (size/duration/progress/currentTime/startOffset) — the TODO explicitly forbids this ('that is the end of what normalization can honestly fix'); the only acceptable levers are re-seeding fixture data and adding narrowly-scoped assertConformantExcept allowances with named justifications.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_157.md`.

## Commit message

```
fix(server-handlers): Align ABS conformance fixtures with the oracle so CompareVal (TODO L127)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Also still open per the TODO: 4 golden fixtures never loaded by any test — cross-reference item L53/ABS-N7 in this scope (marked stale_done there with a caveat to re-verify the pending-vs-strict distinction); if that re-verification finds real gaps, they belong here too.
