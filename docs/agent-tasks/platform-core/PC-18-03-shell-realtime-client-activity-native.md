<!-- file: docs/agent-tasks/platform-core/PC-18-03-shell-realtime-client-activity-native.md -->
<!-- version: 1.0.0 -->
<!-- guid: d3f182e2-2ae6-4061-96d4-ee497af28234 -->
<!-- last-edited: 2026-11-04 -->

# PC-18-03 — Shell realtime client replaces `eventSourceManager` and the operations stream; the Activity page moves to native REST and stream together

| Field | Value |
|---|---|
| ID | PC-18-03 |
| Title | Shell realtime client replaces eventSourceManager and the operations stream; the Activity page moves to native REST and stream together |
| Tier | **T2 — Sonnet / high.** The last first-party user of the legacy streams; one store must never mix native runs with legacy `op.*` frames. (See Planner notes on the tier.) |
| Wave | W18 (stage S9, release `S9`) — after the S9 flip |
| Depends on | PC-18-02 |
| Trailer | `Stage-Release: S9 1` |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 12, §7 S9 ("no first-party request to the legacy streams") |
| Owning spec | 07 §7.9.2 (realtime client, "When the UI moves to the native stream"), §7.11 (`ActivityLog`, `useOperationsStore`, `eventSourceManager` rows), §7.19 step 4; 05-realtime §4 (native stream, topics), §11 (`useTopic`, `useResync`); 05-tasks (native run routes); 09 S9 "Moves", "Done when" |

**Gate.** Merges only after the `S8` soak row exists and PC-18-02 has merged.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-18-03" -b feat/pc-18-03-realtime-activity origin/main
cd "$REPO/../abo-pc-18-03"
git worktree list
make web-deps
```

## Goal

After this PR, no first-party UI code opens a legacy stream (`/api/events`, `/api/v1/operations/events`,
`/api/v3/ops/events`): every subscriber uses the shell realtime client's `useTopic<T>(topic, onEvent, {key})` and
`useResync` over `GET /api/core/v1/events`, and the Activity page (with `useOperationsStore`) reads runs through the
generated core client's task operations **and** the native stream in the same PR, using the native run vocabulary. The
Activity page no longer imports `ops.ts` (`web/src/generated/ops.ts`) or `operationPolling`; `operationPolling` stays
books' (on `ops.ts`) for its books callers; `tools/cmd/opsgen` stays. `eventSourceManager.ts` is deleted.

## Files

The row names these paths; at your base PC-18-02 may have moved some of them (07 §7.11 sends `ActivityLog`,
`useOperationsStore` and `OperationsIndicator` to the shell's `/activity`). Locate each with the greps below and edit it
where it is:
- `web/src/services/eventSourceManager.ts` — deleted.
- `web/src/stores/useOperationsStore.ts` (or its moved copy) — native REST + stream.
- the Activity page: at `748fd787a` **`web/src/pages/ActivityLog.tsx`** (the row says `Activity.tsx`; see Planner notes).
- `web/src/hooks/useUnsavedChangesBlocker.ts` — must already be gone (PC-18-02); delete it if it is not.
- every other subscriber the greps find (at `748fd787a`: `web/src/App.tsx`, `web/src/components/layout/TopBar.tsx`,
  `web/src/pages/Library.tsx`, `web/src/services/api.ts`'s `openOperationsSSE`, `web/src/test/appApiMock.ts`).

## Re-verify before editing

Expected values are at `748fd787a`; at your base the paths may have moved under `web/src/modules/books/` or
`core/web/packages/shell/`. Record the base values in the PR description. Any count of 0 where a file must change:
STOP and report.

```bash
grep -rn "createEventSourceManager\|eventSourceManager" web/src core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules | grep -v '\.test\.' | cut -d: -f1 | sort -u
#   748fd787a: web/src/App.tsx, web/src/components/layout/TopBar.tsx, web/src/pages/Library.tsx, web/src/services/eventSourceManager.ts
grep -rn "openOperationsSSE" web/src core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules | cut -d: -f1 | sort -u
#   748fd787a: web/src/services/api.ts, web/src/stores/useOperationsStore.ts, web/src/test/appApiMock.ts
grep -rn "/api/events'\|/operations/events\|/api/v3/ops/events\|new EventSource(" web/src core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules | grep -v '\.test\.' | wc -l
#   748fd787a: 3 (eventSourceManager.ts:31 and :119, api.ts:733-734)
grep -rln "generated/ops'" web/src core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules
#   748fd787a: web/src/services/api.ts, web/src/utils/operationPolling.ts (07 §7.8); the Activity page reaches ops.ts through both
grep -n "from '../utils/operationPolling'" web/src/pages/ActivityLog.tsx              # 1 at 748fd787a (:65; it goes)
grep -rn "export function useTopic\|export function useResync" core/web/packages/shell/src | head -2   # 2 (PC-18-01)
grep -c '"/api/core/v1/tasks' core/openapi/core.json                                   # >= 1 (native run routes, S5)
```

## Steps

1. Read 07 §7.9.2 in full, the §7.11 rows for `ActivityLog`, `useOperationsStore`, `OperationsIndicator`,
   `eventSourceManager`, `operationPolling`; 05-realtime §4 (topic names, keys, resume) and §11; the native task routes
   in `core/openapi/core.json`.
2. **List every subscriber** (greps 1–3) and, for each, write in the PR description: legacy URL + event name(s) it
   reads → native topic(s) and key it will use (topic names from the events catalog: `core.task.*`,
   `books.book.changed.v1`, `books.system.status-ticked.v1`, `core.stream.closed.v1`, …). A legacy event with no native
   topic in the catalog: STOP and report.
3. **Simple subscribers first** (`App.tsx`'s shutdown/reconnect handling, `TopBar.tsx`, `Library.tsx`): replace
   `eventSourceManager.subscribe(...)` with `useTopic(...)`; move any "refetch on reconnect" logic into `useResync`. The
   shutdown backdrop is the shell's (driven by `core.stream.closed.v1` reason `shutdown`): delete ABO's copy if
   PC-18-02 left one.
4. **The Activity page and `useOperationsStore` together.** In one commit series inside this PR: replace every
   hand-written `api.ts` operations call they make (`getOperationTimeline` and the others they use) with the generated
   core client's task operations, and `openOperationsSSE` with `useTopic` on the `core.task.*` topics; switch their
   types and enums from `ops.ts`/legacy `op.*` names to the native run vocabulary (`tasks.RunState` values as the
   generated types give them). The page's audiobook op labels and colours come from books' `taskKinds` (07 §7.11). After
   this step the page imports neither `generated/ops` nor `operationPolling`.
5. Delete `openOperationsSSE` from `api.ts` if no caller is left; delete `eventSourceManager.ts`; update
   `web/src/test/appApiMock.ts` and every test that mocked them to use `renderInShell`'s `FakeRealtime` (`emit`,
   `reset`, `close`).
6. Keep `operationPolling.ts` (books' copy) and `ops.ts`/`opsgen` untouched: books pages still use them.
7. Add the e2e request-log check (below) and run it.

## Code contract

```ts
// 07 §7.9.2 / 05-realtime §11 (from @mediacore/shell)
useTopic<T>(topic: string, onEvent: (data: T) => void, opts?: { key?: string }): void
useResync(handler: () => void): void
// 07 §7.4.4 (tests)
renderInShell(ui, { realtime: FakeRealtime, ... })  // FakeRealtime.emit / reset / close / fail
```

## Tests

- e2e with a request log (name coined here: `no-legacy-streams.spec.ts`): load the Activity page, the library and the
  top bar, run one operation, and assert the log holds no request to `/api/events`, `/api/v1/operations/events` or
  `/api/v3/ops/events` from the page (09 S9 Done when).
- e2e (name coined here: `activity-live-progress.spec.ts`): start a run and see its progress update on the Activity
  page without a reload.
- vitest: the store applies a `core.task.*` event to the right run; a `FakeRealtime.reset('gap')` triggers the
  `useResync` refetch; a `close('shutdown')` shows the shell backdrop.
- Existing `ActivityLog`, `useOperationsStore`, `TopBar`, `Library` tests updated, none deleted.

## Acceptance

```bash
grep -rn "eventSourceManager\|openOperationsSSE\|new EventSource(" web/src core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules | wc -l   # 0
grep -rn "/api/events'\|/operations/events\|/api/v3/ops/events" web/src core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules | grep -v '\.test\.' | wc -l   # 0
grep -rln "generated/ops'\|operationPolling'" $(grep -rln 'ActivityLog\|useOperationsStore' web/src core/web/packages --include=*.tsx --include=*.ts | grep -v node_modules | grep -v test) | wc -l   # 0 (the Activity page and store import neither)
test -f web/src/generated/ops.ts && echo kept                                          # kept (opsgen stays)
npm --prefix web run typecheck && npm --prefix web test -- --run                       # exit 0
(cd core/web && npm test --workspaces -- --run)                                        # exit 0
make test-e2e                                                                          # exit 0, including the two new specs
make ci                                                                                # exit 0
```

## Out of scope

Deleting `ops.ts`, `opsgen` or books' `operationPolling`; the server's legacy projections (they stay for the window,
contract C6); `renderWithProviders` (PC-18-04); other `api.ts` sections (PC-18-04).

## Rollback

Revert the PR (or binary swap): the legacy projections still serve the three streams, so an older bundle keeps working.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Bump the versioned header of every changed file; one headerless `changelog.d/` fragment.
- Conventional commits, e.g. `refactor(web): activity page on native tasks REST and stream`, each with
  `Stage-Release: S9 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts (subscribers switched, legacy calls left) and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row's `web/src/pages/Activity.tsx` does not exist at `748fd787a`; the Activity page is `web/src/pages/ActivityLog.tsx`
  (3,320 lines, 07 §7.11). After PC-18-02 it and `useOperationsStore.ts` may live in `core/web/packages/shell/` (07 §7.11
  sends both to the shell's `/activity`), so the row's paths should be regenerated at the base.
- Tier: the Activity page is 3,320 lines and moves vocabulary (legacy `op.*` → native run states) and transport at once;
  T2 is the plan's choice, T3 may fit better.
- `web/src/hooks/useUnsavedChangesBlocker.ts` is deleted by step 4 of 07 §7.19 inside the shell adoption, which this
  plan puts in PC-18-02; listing it here duplicates that edit.
