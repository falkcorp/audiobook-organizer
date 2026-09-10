<!-- file: docs/agent-tasks/todo-completion/server-handlers/TASK-144-n-5-search-narrators-must-omit-numbooks-not-emit.md -->
<!-- version: 1.1.0 -->
<!-- guid: d32a4de6-524e-4998-844f-3c41a0472678 -->
<!-- last-edited: 2026-09-02 -->

# TASK-144 — N-5: /search narrators must omit numBooks, not emit 0 (ABS-N5)

> **Status 2026-09-02:** ✅ DONE — PR #2731, completed by #2738 (narrator id half).

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · server-handlers subagent · **Why:** One-line field removal mirroring an existing sibling handler's shape in the same file — fully mechanical once the reference pattern is located. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 53 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🔌 **ABS coverage gaps N-1 … N-10** (audit:" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/server-handlers-144-n-5-search-narrators-must-omit-numbooks-not-emit" -b agent/server-handlers-144-n-5-search-narrators-must-omit-numbooks-not-emit origin/main
cd "$REPO/.worktrees/server-handlers-144-n-5-search-narrators-must-omit-numbooks-not-emit"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove the numBooks key from the narrator objects emitted by the /search handler (browse.go:1338) so they match the shape /narrators already emits (no numBooks field at all), per the ABS contract which says omit rather than report a fake 0.

## Background (verify before editing)

- The sibling /narrators endpoint already omits numBooks correctly (per the TODO text); the /search endpoint's narrator block is the only one still emitting the field with a hardcoded 0.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'numBooks.: 0' internal/server/handlers/abs/browse.go   # 1 hit ~L1338 — the /search narrator block hardcodes numBooks: 0
  ```

### Reuse — don't invent

- Use `/narrators handler's omission pattern (whatever gin.H shape it uses without numBooks)` in `internal/server/handlers/abs/browse.go` (verify: `grep -n 'func (h \*Handler) .*Narrators' internal/server/handlers/abs/browse.go`) — do NOT write a parallel helper.

## Step-by-step

1. Locate the /narrators handler's gin.H construction for a narrator entry (grep above) to confirm its exact key set as the target shape.
2. In browse.go at the numBooks:0 line (~1338), change `gin.H{"name": n.Name, "numBooks": 0}` to `gin.H{"name": n.Name}`.
3. Bump browse.go's version header.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_server-handlers_144.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Zero search matches: Narrators slice stays empty/nil, unaffected by this change.

## Tests

- internal/server/handlers/abs/abs_test.go or browse_test.go: TestSearch_NarratorsOmitNumBooks — GET /api/libraries/:id/search?q=<narrator substring>, assert the returned narrator objects' map does NOT contain a "numBooks" key (use `_, ok := narrator["numBooks"]; require.False(t, ok)`), not merely that it's 0.

Anti-over-suppression test: `N/A — field removal, not a filter.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/server/handlers/abs/... -run TestSearch_NarratorsOmitNumBooks -v` passes.
- [ ] `grep -n 'numBooks' internal/server/handlers/abs/browse.go` no longer shows a hardcoded 0 in the search block.
- [ ] Anti-over-suppression test: `N/A — field removal, not a filter.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/server/handlers/abs/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_server-handlers_144.md`.

## Commit message

```
refactor(server-handlers): N-5: /search narrators must omit numBooks, not emit 0 (ABS-N5)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

If this now runs under the N-2 value-comparison conformance gate (CompareValues:true), re-run the existing /search conformance test to see if it was ALREADY catching this — if IgnoreExtra:true is masking it (extra/wrong-shape fields are ignored, not just wrong values on expected fields), note that explicitly since it affects how much the N-2 fix actually covers N-5-shaped bugs.
