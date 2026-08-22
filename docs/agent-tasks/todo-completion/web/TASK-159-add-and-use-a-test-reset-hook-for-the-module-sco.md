<!-- file: docs/agent-tasks/todo-completion/web/TASK-159-add-and-use-a-test-reset-hook-for-the-module-sco.md -->
<!-- version: 1.0.0 -->
<!-- guid: c02b9d0e-3544-4b9f-bfa7-9bc6244b67f0 -->
<!-- last-edited: 2026-08-21 -->

# TASK-159 — Add and use a test-reset hook for the module-scope path-alias/path-var promise caches (2026-08-20-dual-path-settings-panel.md#3)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · web subagent · **Why:** Small, mechanical: add one exported reset function per file plus a beforeEach call in up to 6 test files; no design ambiguity, pattern is identical to existing `let cached...Promise` code already in front of you. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 0 as of commit 46628240 (later edits shift lines) — re-find it with `this item lives in a todo.d/ fragment (see src_id), not in TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-16.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-159-add-and-use-a-test-reset-hook-for-the-module-sco" -b agent/web-159-add-and-use-a-test-reset-hook-for-the-module-sco origin/main
cd "$REPO/.worktrees/web-159-add-and-use-a-test-reset-hook-for-the-module-sco"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Export a test-only reset function from both PathLinks.tsx and formatPath.ts that sets the module-scope promise cache back to null, and call it from beforeEach (or afterEach) in every test file that renders a component depending on usePathAliases/usePathVars, so each test starts with a fresh config fetch instead of sharing one seeded alias/var set across the whole file.

## Background (verify before editing)

- loadPathAliases() in PathLinks.tsx (lines 104-117) and loadPathVars() in formatPath.ts (lines 52-63) are structurally identical: both memoize a Promise in a module-level `let`, populated on first call and never cleared except on fetch failure (`.catch(() => { cached...Promise = null; ... })`).
- Because the promise is module-scope, it survives across every test in the same test file (and across files if Vitest reuses the module instance within a worker) -- today this is harmless because every test in the affected files uses the same mocked getConfig() response (PathLinks.test.tsx:17-19 mocks getConfig to always resolve `{ root_dir: '/library/books/audiobooks' }`), but the TODO flags this as a landmine for a future test that needs different alias/var data per test case within one file.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "cachedAliasesPromise" web/src/components/common/PathLinks.tsx   # 5 hits L102,105,106,112,116, all internal to loadPathAliases/usePathAliases, none exported — cachedAliasesPromise is a module-scope let with no reset export
  grep -n "cachedVarsPromise" web/src/utils/formatPath.ts   # 5 hits L50,53,54,58,62 — cachedVarsPromise is the parallel cache in formatPath.ts, same shape
  grep -rln "usePathAliases\|usePathVars\|PathLinks\b" web/src --include="*.test.tsx"   # 6 file hits — 6 test files currently depend on (or could be affected by) these caches without resetting them
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In web/src/components/common/PathLinks.tsx, after the `usePathAliases` export (after line ~131 where the function closes), add: `/** Test-only: clears the module-scope config-fetch cache so the next usePathAliases()/loadPathAliases() call re-fetches. Call from beforeEach/afterEach in any test file that needs a fresh or per-test alias set. */\nexport function __resetPathAliasesCacheForTests(): void {\n  cachedAliasesPromise = null;\n}`
2. In web/src/utils/formatPath.ts, after the `usePathVars` export (after line ~81), add the mirror: `export function __resetPathVarsCacheForTests(): void {\n  cachedVarsPromise = null;\n}` with an equivalent test-only doc comment.
3. In each of the 6 test files listed in exact_files, import the relevant reset function(s) (PathLinks.test.tsx and formatPath-adjacent tests import `__resetPathAliasesCacheForTests` from '../PathLinks' or the correct relative path; any file that also exercises usePathVars imports `__resetPathVarsCacheForTests` from '../../utils/formatPath') and call it inside the file's existing `beforeEach(() => { ... })` block (each of these files already has one, e.g. PathLinks.test.tsx:36) -- add the reset call as the first line of that block so it runs before any mock setup for that test.
4. Do not change any getConfig mock or test assertions in these files -- this step is purely additive isolation; behavior for every existing test should be unchanged since they all use the same mocked config value today (verify with the test run in acceptance).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_159.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Calling the reset function while a fetch promise is still in flight (not yet resolved) should be safe -- setting the `let` to null just means the in-flight promise's resolution no longer gets cached; the in-flight consumer's `.then` still fires against its own promise reference. No special-casing needed.

## Tests

- web/src/components/common/PathLinks.test.tsx -- no new test needed for the reset function itself (it's a 1-line test-only helper), but add one regression test 'usePathAliases re-fetches after __resetPathAliasesCacheForTests()' that: renders a consumer, waits for aliases to load, calls the reset export, changes the getConfig mock's resolved value, renders a second consumer, and asserts the second one reflects the new mocked value (proving the cache was actually cleared, not just resettable-in-theory).

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] _
- [ ] _
- [ ] r
- [ ] e
- [ ] s
- [ ] e
- [ ] t
- [ ] P
- [ ] a
- [ ] t
- [ ] h
- [ ] A
- [ ] l
- [ ] i
- [ ] a
- [ ] s
- [ ] e
- [ ] s
- [ ] C
- [ ] a
- [ ] c
- [ ] h
- [ ] e
- [ ] F
- [ ] o
- [ ] r
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] s
- [ ] '
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ] /
- [ ] c
- [ ] o
- [ ] m
- [ ] p
- [ ] o
- [ ] n
- [ ] e
- [ ] n
- [ ] t
- [ ] s
- [ ] /
- [ ] c
- [ ] o
- [ ] m
- [ ] m
- [ ] o
- [ ] n
- [ ] /
- [ ] P
- [ ] a
- [ ] t
- [ ] h
- [ ] L
- [ ] i
- [ ] n
- [ ] k
- [ ] s
- [ ] .
- [ ] t
- [ ] s
- [ ] x
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] 1
- [ ]  
- [ ] e
- [ ] x
- [ ] p
- [ ] o
- [ ] r
- [ ] t
- [ ]  
- [ ] (
- [ ] 0
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] n
- [ ]  
- [ ] '
- [ ] _
- [ ] _
- [ ] r
- [ ] e
- [ ] s
- [ ] e
- [ ] t
- [ ] P
- [ ] a
- [ ] t
- [ ] h
- [ ] V
- [ ] a
- [ ] r
- [ ] s
- [ ] C
- [ ] a
- [ ] c
- [ ] h
- [ ] e
- [ ] F
- [ ] o
- [ ] r
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] s
- [ ] '
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ] /
- [ ] u
- [ ] t
- [ ] i
- [ ] l
- [ ] s
- [ ] /
- [ ] f
- [ ] o
- [ ] r
- [ ] m
- [ ] a
- [ ] t
- [ ] P
- [ ] a
- [ ] t
- [ ] h
- [ ] .
- [ ] t
- [ ] s
- [ ]  
- [ ] r
- [ ] e
- [ ] t
- [ ] u
- [ ] r
- [ ] n
- [ ] s
- [ ]  
- [ ] 1
- [ ]  
- [ ] e
- [ ] x
- [ ] p
- [ ] o
- [ ] r
- [ ] t
- [ ]  
- [ ] (
- [ ] 0
- [ ]  
- [ ] a
- [ ] t
- [ ]  
- [ ] H
- [ ] E
- [ ] A
- [ ] D
- [ ] )
- [ ] ;
- [ ]  
- [ ] g
- [ ] r
- [ ] e
- [ ] p
- [ ]  
- [ ] -
- [ ] r
- [ ] l
- [ ]  
- [ ] '
- [ ] _
- [ ] _
- [ ] r
- [ ] e
- [ ] s
- [ ] e
- [ ] t
- [ ] P
- [ ] a
- [ ] t
- [ ] h
- [ ] .
- [ ] *
- [ ] C
- [ ] a
- [ ] c
- [ ] h
- [ ] e
- [ ] F
- [ ] o
- [ ] r
- [ ] T
- [ ] e
- [ ] s
- [ ] t
- [ ] s
- [ ] '
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ] /
- [ ] s
- [ ] r
- [ ] c
- [ ]  
- [ ] -
- [ ] -
- [ ] i
- [ ] n
- [ ] c
- [ ] l
- [ ] u
- [ ] d
- [ ] e
- [ ] =
- [ ] '
- [ ] *
- [ ] .
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ] .
- [ ] t
- [ ] s
- [ ] x
- [ ] '
- [ ]  
- [ ] l
- [ ] i
- [ ] s
- [ ] t
- [ ] s
- [ ]  
- [ ] a
- [ ] l
- [ ] l
- [ ]  
- [ ] 6
- [ ]  
- [ ] t
- [ ] a
- [ ] r
- [ ] g
- [ ] e
- [ ] t
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] f
- [ ] i
- [ ] l
- [ ] e
- [ ] s
- [ ] ;
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ]  
- [ ] -
- [ ] -
- [ ]  
- [ ] P
- [ ] a
- [ ] t
- [ ] h
- [ ] L
- [ ] i
- [ ] n
- [ ] k
- [ ] s
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ]  
- [ ] i
- [ ] n
- [ ] c
- [ ] l
- [ ] u
- [ ] d
- [ ] i
- [ ] n
- [ ] g
- [ ]  
- [ ] t
- [ ] h
- [ ] e
- [ ]  
- [ ] n
- [ ] e
- [ ] w
- [ ]  
- [ ] r
- [ ] e
- [ ] -
- [ ] f
- [ ] e
- [ ] t
- [ ] c
- [ ] h
- [ ] -
- [ ] a
- [ ] f
- [ ] t
- [ ] e
- [ ] r
- [ ] -
- [ ] r
- [ ] e
- [ ] s
- [ ] e
- [ ] t
- [ ]  
- [ ] t
- [ ] e
- [ ] s
- [ ] t
- [ ] ;
- [ ]  
- [ ] n
- [ ] p
- [ ] m
- [ ]  
- [ ] -
- [ ] -
- [ ] p
- [ ] r
- [ ] e
- [ ] f
- [ ] i
- [ ] x
- [ ]  
- [ ] w
- [ ] e
- [ ] b
- [ ]  
- [ ] r
- [ ] u
- [ ] n
- [ ]  
- [ ] l
- [ ] i
- [ ] n
- [ ] t
- [ ]  
- [ ] p
- [ ] a
- [ ] s
- [ ] s
- [ ] e
- [ ] s
- [ ] .
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_159.md`.

## Commit message

```
feat(web): Add and use a test-reset hook for the module-scope path-alia (2026-08-20-dual-path-settings-panel.md#3)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run grep -n "cachedAliasesPromise" web/src/components/common/PathLinks.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Purely a test-isolation hardening task; ships independently of items #1/#2/#4 in this fragment.
