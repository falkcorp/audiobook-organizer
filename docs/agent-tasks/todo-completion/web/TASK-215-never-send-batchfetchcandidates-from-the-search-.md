<!-- file: docs/agent-tasks/todo-completion/web/TASK-215-never-send-batchfetchcandidates-from-the-search-.md -->
<!-- version: 1.0.0 -->
<!-- guid: a570c73b-97f3-40af-9daf-1652efeab5a1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-215 — Never send batchFetchCandidates({}) from the Search providers command (REV-EMPTY-1)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · web subagent · **Why:** One-line call-site edit plus a small, template-following new test file; no new types or cross-file wiring. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 90020 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90020p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-20.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-215-never-send-batchfetchcandidates-from-the-search-" -b agent/web-215-never-send-batchfetchcandidates-from-the-search- origin/main
cd "$REPO/.worktrees/web-215-never-send-batchfetchcandidates-from-the-search-"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

The 'Search providers…' command (CommandBar 'metadata' menu, id 'search-providers', scope 'library') must send a request the server can resolve to a real, library-wide target instead of an empty body that always 400s. Replace the empty-object call with an explicit selection that means 'every book without a matched candidate yet' -- the natural reading of a library-wide 'search providers' action.

## Background (verify before editing)

- web/src/components/review/ReviewWorkspace.tsx:271 currently reads `run: startJob('Provider search', () => api.batchFetchCandidates({})),` -- an empty body.
- internal/server/metadata_batch_candidates.go's handleBatchFetchCandidates resolves BookIDs first, then Selection; if both are empty it 400s 'book_ids or selection is required' (L69). An empty {} satisfies neither, so this command has never worked in production.
- internal/server/metadata_ops.go's resolveFilterToBookIDs (used when Selection.Filter is set) applies IsPrimaryVersion=true and, when f.OnlyUnmatched is true, filters the resolved list down to books with no 'matched' candidate on file (L530-539) -- this all happens server-side before the 400 check, so `{ selection: { filter: { only_unmatched: true } } }` alone is a complete, non-empty request.
- The TS client type BatchFetchRequest (web/src/services/api.ts:3629-3641) already models `selection.filter.only_unmatched?: boolean` -- no new field needs to be added to the API layer, only a new call site.
- The sibling command 'Bulk search selected…' (L274-282) is the one variant that already works, because it passes an explicit `{ book_ids: [...] }`.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "run: startJob('Provider search'" web/src/components/review/ReviewWorkspace.tsx   # 1 hit, L271: `run: startJob('Provider search', () => api.batchFetchCandidates({})),` — the 'Search providers…' command's run() calls batchFetchCandidates({}) with an empty body
  grep -n "book_ids or selection is required" internal/server/metadata_batch_candidates.go   # 1 hit, ~L69 — the server 400s when book_ids/selection resolve to zero ids
  grep -n "IsPrimaryVersion: &trueVal" internal/server/metadata_ops.go   # 1 hit, ~L480, inside resolveFilterToBookIDs — an empty FilterSpec (no Search/LibraryState/Tag/FieldFilters) resolves to every primary-version book, up to 100000, via GetAudiobooks
  grep -n "if f.OnlyUnmatched {" internal/server/metadata_ops.go   # 1 hit, ~L530, inside resolveFilterToBookIDs, after the primary GetAudiobooks call — FilterSpec.OnlyUnmatched is applied server-side during filter resolution itself, so selection.filter.only_unmatched alone (no book_ids) is a complete, valid whole-library request
  grep -n "only_unmatched" web/src/services/api.ts   # 2 hits: L3637 (nested under selection.filter) and L3640 (top-level BatchFetchRequest.only_unmatched) — the TS BatchFetchRequest type already has selection.filter.only_unmatched
  grep -n "run: startJob('Search for selected'" web/src/components/review/ReviewWorkspace.tsx   # 1 hit, L279, followed by `api.batchFetchCandidates({ book_ids: [...metadata.selectedIds] })` on the next line — the working sibling command already shows the pattern of passing an explicit target instead of {}
  grep -n "data-testid={\`command-menu-\${menu.id}\`}\|data-testid={\`command-\${cmd.id}\`}" web/src/components/review/CommandBar.tsx   # 2 hits, ~L82 and ~L99 — the CommandBar menu button and menu item carry stable testids for driving this from a test
  ```

### Reuse — don't invent

- Use `startJob(label, fn) uniform job-starter` in `web/src/components/review/ReviewWorkspace.tsx` (verify: `grep -n "const startJob = " web/src/components/review/ReviewWorkspace.tsx`) — do NOT write a parallel helper.
- Use `BatchFetchRequest.selection.filter.only_unmatched (existing TS type, no new field needed)` in `web/src/services/api.ts` (verify: `grep -n "only_unmatched?: boolean;" web/src/services/api.ts`) — do NOT write a parallel helper.

## Step-by-step

1. Open web/src/components/review/ReviewWorkspace.tsx. In the 'metadata' CommandMenu's commands array, find the 'search-providers' command (currently at L268-272).
2. Change its `run` from `startJob('Provider search', () => api.batchFetchCandidates({}))` to `startJob('Provider search', () => api.batchFetchCandidates({ selection: { filter: { only_unmatched: true } } }))`. This asks the server to resolve every primary-version book and narrow it to unmatched-only -- a real library-wide fetch, matching the command's `scope: 'library'` label and its 'library-wide' UI note.
3. Bump the file's version header at the top (currently `// version: 1.5.0`) to `1.5.1` and update `// last-edited:` to today's date (YYYY-MM-DD).
4. Create a new test file web/src/components/review/ReviewWorkspace.searchProviders.test.tsx (new). Copy the render harness verbatim from web/src/components/review/ReviewWorkspace.refetchStale.test.tsx: the `makeResult`, `renderWorkspace`, `seed`, `beforeEach`, and `openWorkspace` helpers (import { render, screen, waitFor } from '@testing-library/react'; MemoryRouter; userEvent; vi/describe/it/expect/beforeEach from 'vitest'; * as api from '../../services/api'; ReviewWorkspace; ToastProvider). Give the new file its own version header (version 1.0.0, a fresh v4 guid, today's last-edited).
5. In the new test file's `seed()` (or beforeEach), also mock `vi.mocked(api.batchFetchCandidates).mockResolvedValue({ operation_id: 'op-1' })`, exactly as ReviewWorkspace.refetchStale.test.tsx's `seed()` does at L72.
6. Write test 1: 'sends an explicit unmatched selection, never an empty body'. Render the workspace, wait for `screen.getByTestId('compare-spine')`, click `screen.getByTestId('command-menu-metadata')`, click `await screen.findByTestId('command-search-providers')`, then `await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1))` and assert `expect(api.batchFetchCandidates).toHaveBeenCalledWith({ selection: { filter: { only_unmatched: true } } })`. Also assert `expect(api.batchFetchCandidates).not.toHaveBeenCalledWith({})` for extra safety (this is the literal regression the bug report describes).
7. Write test 2: 'the working sibling command is unaffected' (regression guard so the fix does not accidentally touch the selection-scoped command). Seed one selected row is not straightforward without selection UI, so instead just assert the two commands' testids are both present and distinct: `expect(screen.getByTestId('command-search-providers')).toBeInTheDocument()` and `expect(screen.getByTestId('command-bulk-search-selected')).toBeInTheDocument()` after opening the metadata menu -- this guards against a future edit accidentally merging or removing one of the two commands.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_215.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the server ever returns zero unmatched books for `{ selection: { filter: { only_unmatched: true } } }` (a fully-matched library), handleBatchFetchCandidates's existing OnlyUnmatched branch already returns 200 with `operation_id: ''` and a 'already matched' message (internal/server/metadata_batch_candidates.go L83-90) -- no client change needed to handle that, `startJob`'s toast still fires 'started' generically; this is a pre-existing minor UX rough edge, out of scope for this fix.
- Do not change the sibling 'Bulk search selected…' command (L274-282) -- it already sends an explicit, correct `book_ids` selection and is the reference implementation this fix is modelled on.

## Tests

- web/src/components/review/ReviewWorkspace.searchProviders.test.tsx: 'sends an explicit unmatched selection, never an empty body' -- clicks the CommandBar's Metadata menu then 'Search providers…', asserts api.batchFetchCandidates was called exactly once with `{ selection: { filter: { only_unmatched: true } } }` and never with `{}`.
- web/src/components/review/ReviewWorkspace.searchProviders.test.tsx: 'the working sibling command is unaffected' -- asserts both 'command-search-providers' and 'command-bulk-search-selected' render as distinct menu items.

Anti-over-suppression test: `the working sibling command is unaffected` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npm --prefix web test -- src/components/review/ReviewWorkspace` passes, including the two new tests.
- [ ] `grep -n "api.batchFetchCandidates({})" web/src/components/review/ReviewWorkspace.tsx` returns no hits (the empty-body call site is gone).
- [ ] `npm --prefix web run lint` is clean on the two changed/new files.
- [ ] Anti-over-suppression test: `the working sibling command is unaffected` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_215.md`.

## Commit message

```
refactor(web): Never send batchFetchCandidates({}) from the Search provider (REV-EMPTY-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Do not touch todo_line 90020 part 2 (loading-skeleton fix) in this task -- that lives entirely in MetadataPanel.tsx, a different file, and is a separate JSON object. This task and that one can run in parallel with no file collision.
