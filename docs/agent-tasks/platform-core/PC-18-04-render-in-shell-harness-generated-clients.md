<!-- file: docs/agent-tasks/platform-core/PC-18-04-render-in-shell-harness-generated-clients.md -->
<!-- version: 1.0.0 -->
<!-- guid: 13d5f4c4-3ba5-4fb8-b216-3d7409ba7837 -->
<!-- last-edited: 2026-11-04 -->

# PC-18-04 — Test harness onto `renderInShell`, and the remaining `api.ts` sections whose native routes exist move to generated clients

| Field | Value |
|---|---|
| ID | PC-18-04 |
| Title | Test harness onto renderInShell, and the remaining api.ts sections whose native routes exist move to generated clients |
| Tier | **T1 — Sonnet / medium.** A test-utility rewrite that keeps its signature, plus call-site swaps decided by a fixed table rule. |
| Wave | W18 (stage S9, release `S9`) — last PR of the release |
| Depends on | PC-18-03 |
| Trailer | `Stage-Release: S9 1` |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 12 ("**then** `renderWithProviders` onto `renderInShell`") |
| Owning spec | 07 §7.4.4 (`renderInShell`), §7.19 steps 4 and 6; 07 §7.8 (generated clients); 07 §7.11 (`services/api.ts` row) |

**Gate.** Merges only after the `S8` soak row exists and PC-18-03 has merged.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-18-04" -b feat/pc-18-04-render-in-shell origin/main
cd "$REPO/../abo-pc-18-04"
git worktree list
make web-deps
```

## Goal

1. `web/src/test/renderWithProviders.tsx` renders through `@mediacore/shell/testing`'s `renderInShell`, with **its
   exported name and parameters unchanged**, so none of its callers changes.
2. Every remaining hand-written call in `web/src/services/api.ts` (and, if PC-18-02 split it, in
   `web/src/modules/books/legacyApi.ts`) whose legacy path is an `alias` of a native route in `core/openapi/core.json`
   (or of a books native route in the books document) moves to the generated client, keeping each exported function's
   name, parameters and return type. Everything else stays.

## Files

Modify only: `web/src/test/renderWithProviders.tsx`, `web/src/services/api.ts` (and its split
`web/src/modules/books/legacyApi.ts` if it exists), their tests, and a test file only where step 4 says so.

## Re-verify before editing

Expected values at `748fd787a`. A different value: STOP and report.

```bash
grep -n '^export function renderWithProviders' web/src/test/renderWithProviders.tsx      # 1
grep -n 'initialEntries' web/src/test/renderWithProviders.tsx | head -2                 # the option this PR keeps
grep -rl 'renderWithProviders' web/src | grep -v 'src/test/renderWithProviders.tsx' | wc -l   # 40 files at 748fd787a (record your base's count)
for f in $(grep -rl 'renderWithProviders' web/src | grep -v src/test/renderWithProviders.tsx); do grep -Hn 'container:\|baseElement:\|hydrate:\|queries:\|wrapper:' "$f"; done | wc -l   # 0 (no caller passes other RenderOptions)
for f in $(grep -rl 'renderWithProviders' web/src | grep -v src/test/renderWithProviders.tsx); do grep -Hn "initialEntries: \[[^]]*,[^]]*\]" "$f"; done | wc -l   # 0 (every caller passes at most one entry)
grep -rn "export function renderInShell" core/web/packages/shell/src | head -1           # 1 (PC-18-01)
grep -c "apiFetch(\`\${API_BASE}" web/src/services/api.ts                                # the hand-written calls left; record it
test -f web/src/api/core.ts && echo ok                                                   # ok
```

If a caller passes other `RenderOptions` or more than one initial entry, STOP: the rewrite below would change it.

## Steps

1. **Rewrite `renderWithProviders`** exactly like this (adapt only the import paths and the theme import to your base;
   the theme is whatever the current file wraps with):

   ```tsx
   import React from 'react';
   import { ThemeProvider } from '@mui/material';
   import { renderInShell } from '@mediacore/shell/testing';
   import { appTheme } from '<the theme the current file imports>';

   interface ProviderOptions {
     /** Initial URL entries (default: ['/']); only the last one is used, as MemoryRouter's initial index did. */
     initialEntries?: string[];
   }

   export function renderWithProviders(ui: React.ReactElement, options: ProviderOptions = {}) {
     const { initialEntries = ['/'] } = options;
     return renderInShell(<ThemeProvider theme={appTheme}>{ui}</ThemeProvider>, {
       route: initialEntries[initialEntries.length - 1],
       appId: 'abo',
       themeStorageKey: 'app-theme-mode',
       defaultMode: 'dark',
     });
   }
   ```

   Bump the file's header version and add a `guid` if it has none (the file at `748fd787a` has `file`, `version` and
   `last-edited` only).
2. Run `npm --prefix web test -- --run`. For each failing test, the only allowed fixes are: (a) a component that calls
   `useBlocker` or a data-router hook now works, so delete a test-local `MemoryRouter` wrapper the test added around
   `renderWithProviders` (07 §7.4.4: the harness is a data router); (b) a test that read the theme mode from
   `localStorage` now passes `appId`/`themeStorageKey` as above. Any other failure: STOP and report the test and its
   error.
3. **Build the call table.** For every hand-written `apiFetch` call left in `api.ts` (and `legacyApi.ts`), look up its
   method + path in `docs/compat-surfaces.md`. Write a table in the PR description: function → legacy path → `alias` row
   (or "none") → canonical route → move/stay.
4. **The move rule.** A call moves **only** if (a) its exact legacy path is the legacy side of an `alias` row, (b) the
   canonical route exists in `core/openapi/core.json` (or the books document), and (c) the canonical route's
   `x-mediacore-permission` is the same permission class the legacy route had. Otherwise it stays, listed as "stay" with
   the failing condition. Calls on `/api/v1/operations*` always stay (books gin routes at every stage, 07 §7.9.2).
5. For each call that moves: copy the pattern of a function already on the generated client (`grep -n "from '../api/core'"
   web/src/services/api.ts`); keep the exported signature and return type; map the native response onto the existing
   type by copying the field mapping of the server-side alias for that path (`git grep -n '<legacy path>' -- core internal |
   grep -i alias`). Do not invent a field. If the native route needs a value the function does not receive and the alias
   cannot supply it the same way: leave that call, mark it "stay: needs input", and continue.
6. Update the tests of each moved function to mock the generated operation instead of the URL.

## Code contract

```ts
// unchanged
export function renderWithProviders(ui: React.ReactElement, options?: { initialEntries?: string[] }): RenderResult & { router: ...; realtime: FakeRealtime };
// 07 §7.4.4
export function renderInShell(ui: React.ReactElement, opts?: RenderInShellOptions): RenderResult & { router: ReturnType<typeof createMemoryRouter>; realtime: FakeRealtime };
```

## Tests

- `vitest green with renderInShell`: the whole ABO web suite passes through the new harness.
- Each moved `api.ts` function keeps its existing tests green with the generated client mocked.

## Acceptance

```bash
grep -n "MemoryRouter" web/src/test/renderWithProviders.tsx | wc -l                     # 0
grep -n "renderInShell" web/src/test/renderWithProviders.tsx | wc -l                   # >= 1
npm --prefix web run typecheck                                                        # exit 0
npm --prefix web test -- --run                                                        # all suites pass
npm --prefix web run lint                                                             # exit 0 (the permission-literal rule included)
make test-e2e                                                                         # exit 0
make compat-rehearsal                                                                 # exit 0; cells a/b for the moved calls (CI skips it on this PR)
make ci                                                                               # exit 0
```

Report: files using `renderWithProviders` (count), tests changed (count, all under step 2's two rules), `api.ts` calls
moved (count) and stayed (count, with reasons).

## Out of scope

Rewriting the 40 test files onto `renderInShell` directly; deleting `api.ts`; books' `operationPolling`, `ops.ts`,
`opsgen`; any server change.

## Rollback

Revert the PR. Aliases keep serving every legacy path for the window.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Bump the versioned header of every changed file; one headerless `changelog.d/` fragment.
- Conventional commit, e.g. `test(web): renderWithProviders renders through renderInShell`, with
  `Stage-Release: S9 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- 07 §7.19 step 6 moves `api.ts` sections "as their native routes ship", which runs past S9; this row moves only those
  native at S9's base. Whatever stays is the work of later step-6 PRs, which the inventory does not list.
