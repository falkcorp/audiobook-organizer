<!-- file: docs/agent-tasks/platform-core/seams.d/F9-05-activity.md -->
<!-- version: 0.1.0 -->
<!-- guid: 0e5c8b13-72a4-4d96-b318-a4f7d90c2e65 -->
<!-- last-edited: 2026-11-22 -->

# F9-05 Activity cut: seams, deviations and what F9-03 must know

This covers F9-05 items 1 and 2 only (the Activity page and `OperationTrackerProvider`), cut **in place** on the F9-04 pattern ([F9-04.md](F9-04.md) deviation 1): the shell half stays in `web/src`, the books parts move to `web/src/modules/books/activity/`, and the old path becomes ABO's binding. Nothing moved into `core/web`: the generated core `paths` is still empty and the page still calls `/api/v1` through `services/api`, so a `git mv` into the shell would make a shell page call a HEAD route (I1, I5). Same SPA (type N): `activityPage.equivalence.test.tsx` was written against the untouched page in its own commit and passes unchanged after the cut.

## What is where

| File | What it is |
|---|---|
| `web/src/components/activity/ActivityLogFrame.tsx` | The shell half, moved from `pages/ActivityLog.tsx` (`git mv`; the old path is rewritten in the same commit, so use `git diff -B -M` to see the rename): the operations panel (sections, groups, paging, pin, per-row actions, log tail), the feed (loading, error, empty, partial, stale), filters, sources, digests, compact, auto-refresh with its in-flight and abort bookkeeping, the undo dialog. Takes one prop, `bindings`. Imports no books code and nothing from `services/api` (pinned by an allow-list test over its import specifiers). Residue that is still audiobook-shaped but is data, not code: it reads `entry.book_id` and the digest items' `book`/`book_id` and renders a "book →" link through `bookHref`, and it compares the legacy status literals listed under "Run-state mapping". |
| `web/src/components/activity/activityFrameTypes.ts` | `ActivityBindings` and the structural types the frame is typed over (`ActivityEntry`, `ActivityFilter`, `ActivityOpRow`, `ActivityOpsFeed`, ...). Declared locally rather than imported from `services/activityApi`, which stays compatible with them. |
| `web/src/components/activity/activityUrlCore.ts` | The URL contract, parameterised: `readActivityUrl(params, defaultSince, eventTypes)`. |
| `web/src/pages/activityUrl.ts` | Now the bound version (passes the books event types), so its importers and `activityUrl.test.ts` are unchanged. |
| `web/src/pages/ActivityLog.tsx` | ABO's binding: `<ActivityLogFrame bindings={booksActivityBindings} />`. F9-03 deletes it. |
| `web/src/modules/books/activity/activityBindings.tsx` | `booksActivityBindings`, one module-level object over the legacy services (see the table below). |
| `web/src/modules/books/activity/PendingFileOpsPane.tsx`, `activityPanes.ts` | One prop-less pane, `pending-file-ops` (`PendingFileOpsBanner` with its hook), and `BOOKS_ACTIVITY_PANES`, the list of `{ id, label, Component }` (one file per pane, so each is one `lazy: () => import(...)` line later). |
| `web/src/modules/books/activity/eventTypes.ts` | The fifteen audiobook event types the Type filter offers and `?type=` accepts. |
| `web/src/modules/books/activity/useActivityFeed.ts` | The legacy store as the page's feed; builds on `useBooksOperationsFeed` (the bell's) and adds the running subset and the latest log line. |
| `web/src/modules/books/activity/OperationTrackerProvider.tsx` | The merge/combine/purge lock provider, moved from `components/operations/` with the old path re-exporting it (`Library.tsx`, `usePurgeRun.ts`, `main.tsx` are untouched). |

## The bindings, and what each becomes at F9-03

| Binding | Today (legacy) | At F9-03 |
|---|---|---|
| `useFeed` | `useBooksActivityFeed` over `useOperationsStore` | the shell's `useOperationsStore` (`providers/operationsStore.tsx`) over `/api/core/v1/tasks`, mapped to `ActivityOpRow` |
| `activity` (`fetch`, `fetchSources`, `compact`) | `services/activityApi` | stays books' (the book activity feed is a books route); `compact` becomes the maintenance task start |
| `operations` (`cancel`, `discard`, `clearStale`, `retry`, `finish`, `logs`) | `services/api` operation routes | the shell's task actions. Not checked at this base: whether core has routes for Discard (`DELETE .../record`), Finish without resuming and Clear Stale. The shell store has a `remove`-a-run hook ("the run was discarded") and an `awaitingDecision` flag, but no action routes, so F9-03 must find or file them before those three buttons can bind |
| `status` | `utils/operationPolling` | the shell's state helpers over `TaskRunView` (`providers/taskRuns.ts`) |
| `nameOf` | `operationDisplayName` (def-id label table) | books `taskKinds` (`modules/books/layout/taskKinds.ts`), looked up by kind |
| `tagChip` | `utils/activityTagColors` (the `book:` and `component:` colours are books') | stays books' |
| `eventTypes` | the fifteen audiobook types | the books module's contribution |
| `undo` (`offers`, `preflight`, `isRefused`, `revert`) | `versionApi.getUndoPreflight` + `revertResult` | stays books' |
| `bookHref` | `/library/:id` | `/books/library/:id` (F9-03 owns the `/books/` URLs) |
| `renderBatchedEntry` | `BatchActivityEntry` | a books renderer |
| `renderOpDetails` | `TrashCleanupResultPanel` for a finished trash-cleanup run | the books `taskKinds` entry's `resultView` (`TaskKindPresentation.resultView`: "renders a finished run's result in the Activity tasks pane"); it needs the run id, so it is not a pane |
| `panes` | `BOOKS_ACTIVITY_PANES`, rendered above the operations panel | `activityPanes` entries (`{ id, label, lazy, permission?, order? }`, id `pending-file-ops`). **Layout change:** the shell's `ActivityPage` renders panes as tabs (`TabbedSlots`), so the banner stops sitting above the operations panel and becomes its own tab; `activityPage.equivalence.test.tsx` pins today's position and F9-03 rewrites that case, as F9-04 did for `maintenanceSplit.test.tsx` |

Keep `bindings` and each of its members at one identity: the frame keys its fetch callbacks on them, so a per-render object refetches on every render. `reload` in particular is an effect dependency.

## Run-state mapping (frame vocabulary to core)

The shell's store (`providers/operationsStore.tsx`, `toRow`) sets a row's `status` to core's state string (`status: run.state`) and adds `awaitingDecision`, so the rows F9-03 feeds the frame carry **core state names**, not the legacy statuses the frame compares today. The frame reads `status.isTerminal` and `status.isInterrupted` through `bindings.status`, but it still compares these legacy literals directly, and each needs a `status` method (or an adapter that rewrites the row's status) before the shell's rows render correctly:

Twelve comparison sites, as `web/src/components/activity/ActivityLogFrame.tsx:line` at the head of F9-05 (re-grep for `'queued'|'completed'|'failed'|'canceled'|'running'|'interrupted_ask'` if the file has moved on; lines 95-97 are section keys, not comparisons):

| # | Line | Legacy literal | Core state / `shellRunStatus` method |
|---|---|---|---|
| 1 | 2165 | Active section: `!== 'queued'` and not terminal | not `isQueued`, and not terminal |
| 2 | 2167 | Pending section: `=== 'queued'` | `queued`, `waiting_deps` (`isQueued`) |
| 3 | 2171 | Completed section: `=== 'completed'` | `succeeded` (`isSuccess`) |
| 4 | 2177 | Failed section: `=== 'failed'` | `failed`, `timed_out` (`isFailure`) |
| 5 | 2183 | Canceled section: `=== 'canceled'` | `canceled` (`isCanceled`) |
| 6 | 2328-2336 | status chip text (`queued` shown as `pending`) and colour (queued default, completed success, failed error, canceled warning, else info) | the same four methods |
| 7 | 2401 | Finish without resuming: `=== 'interrupted_ask'` with `canFinish` | `awaitingDecision` (`awaiting_decision`) |
| 8 | 2487 | "Waiting to start…": `=== 'queued'` | `isQueued` |
| 9 | 2498-2502 | static terminal bar: the list `completed`, `failed`, `canceled`, `interrupted_dropped`, `interrupted_restart` | `isTerminal` plus the interrupted family |
| 10 | 2523-2525 | terminal bar colour: `completed` / `failed` | `isSuccess` / `isFailure` |
| 11 | 2545-2547 | the same colour test, second bar | `isSuccess` / `isFailure` |
| 12 | 2586 | current-item line: `=== 'running'` | `running`, `stopping` (`isRunning`) |

The four new `ActivityStatusModel` methods are `isQueued`, `isSuccess`, `isFailure` and `isCanceled` (the books binding supplies today's literal comparisons); `isRunning` and the row's `awaitingDecision` are two more for sites 7 and 12, six in all if F9-03 wants them as methods rather than an adapter that rewrites the row.

### Legacy status to core state (the mapping F9-03 needs)

| Legacy status | Core `TaskRunState` |
|---|---|
| `queued` | `queued`, `waiting_deps` |
| `running` | `running`, `stopping` |
| `completed` | `succeeded` |
| `failed` | `failed`, `timed_out` |
| `canceled` | `canceled` |
| `interrupted_*` (the family, matched by prefix) | `interrupted`, `interrupted_quiesced`, `interrupted_dropped` |
| `interrupted_ask` (with `canFinish`) | `awaiting_decision` (row flag `awaitingDecision`) |

Settled with the coordinator (2026-11-22): the literals stay out of F9-05 and are F9-03's, which maps rows to core state names. F9-03 takes these as is: the twelve sites and four methods above. The core state list is `TASK_RUN_STATES` in `providers/taskRuns.ts` and the shell's own mapping is `shellRunStatus` in `layout/shellOperations.tsx`; the legacy side was read from the frame, not from the server.

## Gaps and deviations

1. **The activity feed and its filters stay on the frame's side**, with the books event types and the book link injected. The brief's two sentences (the page is the shell's tasks pane; the book feed is books') pull apart here; one owner for URL sync, polling, the auto-refresh pause and the toast was the tie-break, so the whole page is one frame and the feed endpoint is a binding. F9-03 may split the feed into its own books `activityPanes` entry; it would then need the filter state lifted out, which today lives in the frame.
2. **`OperationTrackerProvider` moved whole, not split.** Its follow-a-run mechanics (poll a run to its end, persisted descriptors that survive a reload, `subscribe` for a showing page, outcome toasts) are generic in shape, but the shell's tracker (`tasks/TaskTracker.tsx`, `tasks/store.ts`) is only a poll-by-id store (`TaskTrackerProvider({ fetchRun })`, `useTask(runId)`, `useTaskError`): no persistence, no end-of-run subscription, no locks, no toasts. It is not a drop-in for the follow half, so there is no shell twin to split toward yet. The generic half stays bound to this one behaviour; F9-03 decides whether to grow the shell's tracker with persistence and `subscribe` (then this provider shrinks to the book locks `pendingIds` and `anyPending` and the three outcome toasts) or to keep the whole provider in the books module.
3. **`lib/storageKeys` is still imported by the frame** for `activity-source-prefs` and `activity-ops-pinned`; both become `storageKey()` names with the rest of F9-05's storage-key item, which this PR does not do.
4. **The frame imports `utils/apiFetch`** (`ApiTimeoutError`, `isAbortError`); it moves to the api-client runtime in F9-05 item 8.
5. **Pending-file-ops banner is a pane; the trash-cleanup panel and undo are not.** A pane takes no props; the trash panel needs the run id and undo needs the entry, the reload and the toast, so they are injected renderers and a binding like the bell's `renderDetails` and `rowActions`.
6. **Undo does not reuse `useBooksRowActions`.** The bell's hook is a row-actions hook with its own refused-operations set and calls `versionApi.revertOperation`, while the Activity page's revert calls `services/api.revertOperation` and keeps its refused set in the page; the shapes do not fit. They share the same primitives (`describeUndoPreflight`, `isUndoRefused`, `describeRevertResult`), which the binding reuses. Whether the two `revertOperation` functions should be one is a books question this PR leaves alone.
