<!-- file: docs/proposals/2026-10-holistic/07-design-decisions-and-modularity/C-server-state-library.md -->
<!-- version: 1.0.1 -->
<!-- guid: f099b451-33cd-4639-b9c7-cba9562fdd34 -->
<!-- last-edited: 2026-10-09 -->

# Appendix C: server-state library for the Review lanes (D48)

Reviewer `r4`, round 2, 2026-10-09. D48 says "pilot a server-state library on
the Repairs lane; keep it only if it removes code". The owner asked to see the
choice. Every number below was measured in the `aorg-review2` worktree at
`ebda30d47` with the lockfile's `node_modules` (`npm ci`), or read from
`npm view` and the cited pages on 2026-10-09. Nothing is from memory.

**Recommendation in one line:** TanStack Query v5 (`@tanstack/react-query`
5.104.1; 10.7 KB gzip, 1.7 % of today's 636 KB of JS), piloted on the Repairs
lane's three reads, with an acceptance bar of at least 150 of the lane's 215
server-state lines removed and its 22 tests still green in about 3 s.

## 1. What the Review lanes do today

### 1.1 Size and mechanism per lane

| Lane hook | Lines | Server-state lines (regex, §1.4) | How it avoids stale responses | Abort on unmount / supersede | Refresh trigger | Polling |
|---|---:|---:|---|---|---|---|
| `useRepairsLane.ts` | 893 | 72 (regex); **215 by reading, §1.2** | compares the loaded page's `fixer_id`, `plan_op_id`, `filter`, `class` with the on-screen ones (`:548-556`) | `AbortController` per effect, 14 references; two `useRef` sets of controllers for trial and apply polls | two nonces: `fixersNonce`, `rowsNonce` | `api.pollOperationV2` every 1,500 ms per trial and per apply |
| `useMetadataLane.ts` | 2,365 | 83 | `fetchIdRef` counter: a response whose id is not the newest is dropped (`:968-976`) | no `AbortController` (0 references): superseded requests run to completion and are discarded | `refreshKey` | bounded apply timer; `inFlightApplyRef` per book |
| `useDupesLane.ts` | 1,223 | 32 | request generation counter (4 references) plus `AbortController` (4) | partial | `reload` callbacks | none |
| `useRegroupLane.ts` | 904 | 43 | generation / token (15 references) plus `AbortController` (5) | yes | `reload` | none |

Shared pieces: `lanes/types.ts` (51 lines, the descriptor; keep), `lanes/index.ts`
(37), the per-lane descriptors (30-41 lines each; keep), `utils/apiFetch.ts`
(timeout and auth-redirect detection; keep, it becomes the `queryFn` body),
`services/eventSourceManager.ts` (SSE client for `/api/events`; see §3.4).
Four different stale-response mechanisms exist for one problem.

### 1.2 The Repairs lane's server-state code, by line range

Read in full. These ranges do nothing but fetch, cache, abort, poll or guard
against a stale copy; none of them is lane logic:

| Range | Lines | What |
|---|---:|---|
| `:88-90` | 3 | `isAbort` helper |
| `:284-286`, `:300-302` | 6 | loading, error and nonce state for the two reads |
| `:332-385` | 54 | fixer-list effect: controller, loading, error, 404 copy, `finally` |
| `:390-403` | 14 | owner-status effect and `reloadFixers` |
| `:406-464` | 59 | `followTrial`: a controller map plus `pollOperationV2` and its abort and "lost track" branches |
| `:468-494` | 27 | auto-follow effect, inactive-lane abort, unmount abort of both controller sets |
| `:516-544` | 29 | rows-page effect and `reloadRows` |
| `:548-556` | 9 | stale-page guard |
| `:709-725` | 17 | apply poll (`pollOperationV2`) inside `runApply` |
| `:877-878` | 2 | the "loading flag stuck after a switch" guard on the return |
| **Total** | **220** | about 215 after allowing for blank lines |

What stays as lane logic whatever the library: `withNewerPlan` and the
`knownPlans` rule (the server keeps only the newest plan; `:14-23`), the
settled-row and outcome maps (`:496-513`, `:568-578`, `:755-759`), selection,
the three confirm messages, `collectApplicableIds` (the 500-id page walk) and
`dispatch`. That is about 670 lines; a library does not touch them.

### 1.3 Bugs this class of code has had (lanes and `api.ts`, `git log`)

| Commit | Date | What went wrong | Would a query library have prevented it? |
|---|---|---|---|
| `fbf317ed9` | 2026-09-01 | a stale regroup reload repainted another kind's holds (a response for an old filter landed under a new one) | **Yes.** A response is stored under the key it was requested with; the component reads the current key's entry. |
| `456a4b9a6` | 2026-08-20 | a fast keystroke merged the same dupes pair twice | **Partly.** `useMutation` exposes `isPending` per mutation; the double-submit guard still has to read it. Mutations are not de-duplicated by key. |
| `273e244d2` | 2026-08-29 | the metadata lane's bounded apply timer was not cleared on unmount | **Partly.** A `refetchInterval` query stops when its observer unmounts; a hand-written `setTimeout` chain does not. |
| `9f3690888` | 2026-09-01 | dupes selection kept rows from the previous page | No. Selection is client state. |
| `a513a7ba8` | 2026-08-29 | the metadata reconcile clobbered skipped and pending rows | No. Merge rule between optimistic client state and server rows; see §3.1. |
| `33de88527`, `915f522f9`, `cf97a98a7` | 2026-09-30 to 10-02 | stale book rows had to be refetched by hand after details moved | **Yes.** `invalidateQueries` on `['books', ...]` after an apply replaces the hand-written refetch paths. |
| `66694a4ea`, `cd3767357` | 2026-09-01 | the dupes lane blocked the main thread while loading; a regroup payload was parsed twice per render | Partly. Structural sharing and one parse per key help; the progressive-mount work is UI. |
| `6d9996790` | 2026-07-11 | Library list loads had no cancel and no timeout | Yes for cancel (the `signal` is supplied), no for the timeout (`apiFetch` keeps it). |
| `c5b9e379d` | 2026-05-20 | 41 memory leaks across 25 components, most of them un-cleared timers and subscriptions after unmount | Partly. Queries and polls are owned by the cache, not the component. |
| `5b6a99f78` | 2026-03-21 | a mount-only `useEffect` warning suppressed instead of fixed | Yes. There is no mount effect to write. |

Not found in the log: a lane that fetched twice on mount. `useRepairsLane` has
one effect per read, so this project's known "two effects both call the loader"
bug shape (it shipped in other pages) is not present in the lanes today.

### 1.4 Reproduce the counts

```bash
cd web/src/components/review/lanes
wc -l useRepairsLane.ts useMetadataLane.ts useDupesLane.ts useRegroupLane.ts
RE='AbortController|\.abort\(|signal|aborted|isAbort|Loading\(|Error\(null\)|Nonce|refreshKey|reload|refetch|stale|\.then\(|\.catch\(|\.finally\(|inFlight|generation|requestId|cancelled|cancelRef'
for f in use*Lane.ts; do printf '%s %s\n' "$f" "$(grep -cE "$RE" "$f")"; done
git log --format='%h %ad %s' --date=short --no-merges -- web/src/components/review/lanes/ web/src/services/api.ts \
  | grep -iE 'stale|twice|double|abort|refresh|loop|race|refetch|leak|reload|cancel'
```

The regex undercounts (the Repairs lane's 72 against 215 by reading) because it
matches only the marker lines, not the bodies of the effects. It is given so
the ratio between lanes can be re-measured; the acceptance bar in §4.6 uses the
by-reading count.

## 2. Candidates

Versions from `npm view <pkg> version` on 2026-10-09. Bundle cost measured here:
each library bundled alone with esbuild (`--bundle --minify --format=esm
--external:react --external:react-dom`), then `gzip -9`. The app today: 636 KB
of gzipped JS across all chunks (`npx vite build` into the scratchpad, every
`assets/*.js` concatenated and gzipped).

| | TanStack Query v5 | SWR | RTK Query | zustand + a small fetch helper | Keep the hand-written hooks |
|---|---|---|---|---|---|
| Version | `@tanstack/react-query` **5.104.1** (2026-10-02; 17 releases in 90 days) | `swr` **2.5.1** (2026-08-12; 2 releases in 90 days) | `@reduxjs/toolkit` **2.13.0** (2026-09-29) | `zustand` 5.0.15 (installed) | — |
| Cost, min / gzip, measured | 36.4 KB / **10.7 KB** (`QueryClient`, provider, `useQuery`, `useMutation`, `useInfiniteQuery`) | 17.5 KB / **7.8 KB** (`useSWR`, `SWRConfig`, `mutate`, `useSWRMutation`, `useSWRInfinite`) | 77.2 KB / **27.2 KB** (`createApi`, `fetchBaseQuery`, `configureStore`, `react-redux` `Provider`). Upstream says about 19 KB with React when RTK is new to the app ([overview](https://redux.js.org/toolkit/rtk-query/overview)) | 0.4 KB (already shipped) + the helper we write | 0 |
| React 19 / Compiler | peer `react ^18 \|\| ^19` ([npm]). The docs fetched (overview, installation, important defaults) say nothing about the React Compiler; the pilot must run the compiler logger from `docs/react-compiler-adoption.md` on the lane before and after | peer `react ^16.11 \|\| ^17 \|\| ^18 \|\| ^19` ([npm]); 2.3 added React 19 support (release notes); nothing found on the compiler | peer `react ^16.9 … ^19`, `react-redux ^7.2.1 … ^9` | n/a | n/a |
| Suspense / `use()` | `useSuspenseQuery`, `useSuspenseInfiniteQuery`, `useSuspenseQueries`; cannot be conditionally enabled, no `placeholderData`, key changes should be in `startTransition` ([suspense guide](https://tanstack.com/query/latest/docs/framework/react/guides/suspense)) | `suspense: true` option (legacy shape) | `useQuery` only; no Suspense story | hand-written | the lanes gate on `active` and would lose that under Suspense; **not wanted here** |
| Request de-dup | by query key, with `staleTime` | by key, `dedupingInterval` | by endpoint + args | we write it | the four mechanisms in §1.1 |
| Abort on unmount / supersede | `queryFn({ signal })`; passing the signal to `fetch` is what makes the cancel real; by default an unmounted query is not cancelled and its result is kept ([cancellation](https://tanstack.com/query/latest/docs/framework/react/guides/query-cancellation)) | no `AbortSignal` in the fetcher contract; stale results are discarded, not cancelled | `signal` in `queryFn` | we write it | `AbortController` in 3 of 4 lanes |
| Stale-while-revalidate | `staleTime` / `gcTime`, background refetch, structural sharing ([defaults](https://tanstack.com/query/latest/docs/framework/react/guides/important-defaults)) | the library's namesake | yes | we write it | none |
| Pagination / infinite | `placeholderData: keepPreviousData`, `useInfiniteQuery` | `useSWRInfinite` | manual `serializeQueryArgs` + `merge` | we write it | offset state per lane |
| Optimistic update and rollback | `onMutate` snapshot + `setQueryData`, `onError` rollback, `onSettled` invalidate; or render from `variables` while `isPending` ([optimistic updates](https://tanstack.com/query/latest/docs/framework/react/guides/optimistic-updates)) | `useSWRMutation` `optimisticData`, `rollbackOnError` (default true), `populateCache`, `revalidate` ([mutation](https://swr.vercel.app/docs/mutation)) | `onQueryStarted` + `updateQueryData` with `patchResult.undo()` | we write it | `inFlightApplyRef` and the reconcile rule (`a513a7ba8`) |
| Devtools | `@tanstack/react-query-devtools` 5.104.1, dev-only lazy import, 0 bytes in prod | SWR DevTools browser extension (third party) | Redux DevTools | none | none |
| SSR | irrelevant: this is a Vite SPA served by the Go binary; nothing in any candidate is needed for SSR | same | same | same | same |
| Tests under Vitest | one `QueryClient` per test via a wrapper, `retry: false`; set `gcTime: Infinity` only if you set `gcTime` at all ([testing](https://tanstack.com/query/latest/docs/framework/react/guides/testing)). Fake timers are not addressed in the docs: the retryer and `refetchInterval` use `setTimeout`, so tests that fake timers must advance them. Two lane test files use `vi.useFakeTimers` today (`useMetadataLane.test.ts`, `useRegroupLane.test.ts`); the Repairs test does not | `SWRConfig` with a fresh `provider: () => new Map()` per test | a store per test | the existing tests | the existing tests |
| Typing | generic over data and error; `queryOptions()` helper types keys and data together; `select` narrows | generic `useSWR<Data, Error>`; keys are loosely typed | generated hooks are fully typed from the endpoint definitions | ours | ours |
| Maintenance | active: 17 releases in the last 90 days, latest 2026-10-02 | quiet: 2 releases in 90 days, latest 2026-08-12 | active | ours to maintain | ours to maintain |

**What using zustand 5 already implies for RTK Query.** RTK Query needs a
Redux store, its reducer and middleware, and `react-redux`'s `Provider` (or
`<ApiProvider>` when there is no store). The app has four zustand stores and no
Redux. Adopting RTK Query means a second state container beside zustand for
the lifetime of the app, at 2.5 times the bundle cost of TanStack Query. It is
not a candidate.

**zustand-only with a small fetch helper.** A `useResource(key, fetcher)` hook
with de-dup, abort, stale-time and invalidation is the core of TanStack Query
rewritten in-house: about 300 lines plus tests, no devtools, no infinite
queries, and the same bug class the lanes already have, now in one shared
place. It wins only on bundle size (about 1 KB against 10.7 KB). D14 named this
as alternative (c) and rejected it for the same reason.

## 3. Fit against this app's needs

### 3.1 Repairs: trial → approve → apply

- **Fixers and owner status** are plain queries: `['repairs','fixers']`,
  `['repairs','owner-status']`. The 404 copy ("this server has no repairs
  endpoints yet") becomes a `retry` predicate that never retries 4xx and an
  `error` rendered as today.
- **A trial** is `useMutation(startRepairPlan)`; following it is a query
  `['ops','v2', opId]` with `refetchInterval: 1500` while the status is
  in flight and `false` once it is terminal. That replaces `followTrial`'s
  controller map; an inactive lane sets `enabled: false` and the poll stops.
- **Rows page**: `['repairs','rows', fixerId, planOpId, { filter, rowClass, offset, limit }]`
  with `placeholderData: keepPreviousData`, so a page change shows the previous
  page dimmed instead of blanking. The stale-page guard (`:548-556`) is deleted:
  the key is the guard.
- **Apply** is `useMutation(startRepairApply)` followed by the same op poll.
  Optimistic updates are **not** wanted for apply (the file header at
  `ActionBar.tsx:9-27` already records why `useOptimistic` is wrong for a
  multi-minute server write); the outcome map stays client state fed from the
  mutation result. `onSettled` invalidates `['repairs','fixers']` (the
  `last_plan` pointer moved) and `['books']` (rows were written). The stored
  plan's rows never change, so `['repairs','rows', fixerId, planOpId]` is not
  invalidated; `settledRowIds` stays local.
- **Owner apply** is a third mutation with the same follow.
- **Stays as is:** `knownPlans` / `withNewerPlan` (a cache rule the server
  does not implement), `collectApplicableIds` (a page walk inside a mutation's
  `mutationFn`, not a query), the confirm messages, `dispatch`.

### 3.2 Batched `getBooksByIds` (max 500 ids)

`api.ts:1325-1346` already splits ids into chunks of 500. Under a library the
unit of caching should be the chunk, not the call: `useQueries` over
`['books','byIds', chunk]` with each chunk sorted so that the same set of ids
always produces the same key. The Library page's `books.changed` patch path
then becomes `queryClient.invalidateQueries({ queryKey: ['books'] })` plus
`setQueryData` for the rows it already holds.

### 3.3 The D36 server-side Metadata lane

D36 (accepted) replaces the full-load index with server-side filter, count and
page queries plus an ID list for "select all N". That is exactly the shape a
query library serves:

- `['review','metadata','page', q]`, `['review','metadata','count', q]`,
  `['review','metadata','ids', q]` where `q` is the filter object **after** the
  150 ms debounce. The debounce stays outside the library (a `useDeferredValue`
  or the existing timer); the library de-dups the key that results.
- `keepPreviousData` for the page while the count query is in flight.
- The 2,365-line hook is being rewritten by D36's R3 anyway. **Do not port the
  full-load mode to the library**: the pilot order in §4.9 puts Metadata last,
  with R3.

### 3.4 SSE invalidation

An event stream exists: `GET /api/events` (`services/eventSourceManager.ts`,
reconnect with backoff, 5 attempts). The server publishes `books.changed`
(`internal/realtime/book_changes.go:18`), `operation.progress`,
`operation.status`, `operation.log` and `system.status`
(`internal/realtime/events.go:22-25`). Today the lanes do not subscribe; only
`Library.tsx` and `stores/useOperationsStore.ts` do, and the lanes refresh by
nonces and polling.

With a query cache the stream becomes one subscriber, in `main.tsx` next to
the provider:

```ts
eventSourceManager.subscribe((ev) => {
  if (ev.type === 'books.changed') queryClient.invalidateQueries({ queryKey: ['books'] });
  if (ev.type === 'operation.status') queryClient.invalidateQueries({ queryKey: ['ops', 'v2', ev.data?.id] });
});
```

`operation.status` lets the trial and apply polls run at a slower
`refetchInterval` (say 5 s) with the event forcing an immediate refetch. The
PR must check the event payload's id field name before relying on it. Keep the
polling: the stream gives up after 5 reconnects.

### 3.5 The stale `book.file_path` rule

Project rule: `book.file_path` is stale; the `book_file` rows are the truth
(`feedback_book_file_path_is_stale_use_book_files`). Under a cache this becomes
two rules: a lane never reads `file_path` from a cached `Book` (the same rule
as today), and after any apply the lane invalidates `['books']` rather than
patching cached rows by hand, so a repaired book re-reads its `book_file` rows
from the server. `setQueryData` is used only for rows the server just returned.

### 3.6 Sequencing against 05 PR 14

05 PR 14 retires `/api/v1/repairs` and `repairs.plan` / `repairs.apply` once the
fixers are ported to the v3 Fixer kind (12D); the lane then talks to
`/api/v3/ops/*`. The pilot must land **before 12D starts**, or be folded into
the lane's re-point, so the lane is not rewritten twice. 08 v1.1.0 moved 07 F3 to wave 2, right after
03 PR 10 and before 05 PR 10 (R23), so it finishes before 12D.

## 4. Recommendation and pilot spec

**Pick TanStack Query v5.** Reasons: it is the only candidate with abort on
supersede, infinite queries, typed keys and devtools in one package; its
mutation model matches trial → apply (mutation, then a polled query) without
optimistic cache writes, which the project does not want for applies; its cost
is 10.7 KB gzip (1.7 % of the JS the app ships); it is actively released. SWR
is 3 KB smaller but has no `AbortSignal` contract, a quieter release cadence
and a weaker pagination story. RTK Query is excluded by the zustand decision.

### 4.1 PR 07-F3, files

| File | Change |
|---|---|
| `web/package.json`, `web/package-lock.json` | add `@tanstack/react-query@^5.104.1`; `@tanstack/react-query-devtools@^5.104.1` as a devDependency |
| `web/src/lib/queryClient.ts` (new) | the `QueryClient` and its defaults (§4.2); the `queryKeys` factory (§4.3); the SSE subscriber (§3.4) |
| `web/src/main.tsx` | `<QueryClientProvider client={queryClient}>` inside `ErrorBoundary`, outside `BrowserRouter`; a lazy `ReactQueryDevtools` behind `import.meta.env.DEV`; on sign-out (`AuthProvider`), `queryClient.clear()` |
| `web/src/components/review/lanes/useRepairsLane.ts` | the three reads become `useQuery`; the trial, apply and owner-apply become `useMutation` plus an op-poll query; delete the ranges in §1.2 |
| `web/src/components/review/lanes/useRepairsLane.test.ts` | a `wrapper` that builds a fresh `QueryClient` per test (`retry: false`); the existing `vi.mock('../../../services/api')` stays; 22 tests, no new ones required, no test deleted |
| `web/src/test/queryWrapper.tsx` (new) | the shared test wrapper |
| `changelog.d/<new>.md` | fragment |

Not touched: `api.ts` (every `api.*` function is reused as the `queryFn` or
`mutationFn` body; `apiFetch`'s timeout and auth-redirect checks keep working
because the signal is chained), the lane descriptor, `ReviewWorkspace` and the
other three lanes.

### 4.2 QueryClient defaults

| Option | Value | Why |
|---|---|---|
| `staleTime` | 30 s | lanes invalidate explicitly after writes; a 30 s window stops a tab switch from refetching every read |
| `gcTime` | 5 min (default) | pages are revisited within minutes |
| `retry` | queries: `(count, err) => count < 1 && !is4xx(err)`; mutations: `0` | a 404 ("no repairs endpoints") or 403 must show at once; a write is never retried by the client |
| `refetchOnWindowFocus` | `false` | a focus refetch during a multi-minute apply churns the rows table for nothing; D36's server queries are cheap but not free |
| `refetchOnReconnect` | `true` | |
| `refetchOnMount` | `true` (default) | with the 30 s stale window |
| `throwOnError` | `false` | errors render in place (the four-state rule: loading, error, empty, populated) |
| `structuralSharing` | `true` (default) | keeps row references stable for `useMemo` chains |

### 4.3 Query key conventions

- First segment is the domain: `books`, `repairs`, `review`, `ops`, `system`.
- Keys are built by one `queryKeys` object, never inline:
  `queryKeys.repairs.rows(fixerId, planOpId, params)`.
- Parameter objects go last and are the only non-string segment, so
  `invalidateQueries({ queryKey: ['repairs','rows', fixerId] })` hits every
  page of one fixer.
- Id lists are sorted before they enter a key (§3.2).
- A key never contains a `file_path` (§3.5).

### 4.4 Invalidation after an apply

`useMutation(startRepairApply, { onSettled })` → the op-poll query runs until
terminal → the result is read → `invalidateQueries(['repairs','fixers'])` and
`invalidateQueries(['books'])`. The `operation.status` event (§3.4) triggers the
same invalidation earlier if the stream is up. The plan rows are not
invalidated (they are immutable per plan).

### 4.5 Test approach

- One `QueryClient` per test through `web/src/test/queryWrapper.tsx`.
- `retry: false` in the test client; `gcTime` left at default.
- Fake timers are not used in the Repairs test today and the pilot does not
  introduce them. The poll is tested by mocking `api.pollOperationV2` as now.
- The compiler logger from `docs/react-compiler-adoption.md` runs on
  `useRepairsLane.ts` before and after; the bailout count must not rise.

### 4.6 Acceptance bar: keep only if it removes code

| Measure | Today | Bar |
|---|---|---|
| Server-state lines in `useRepairsLane.ts` (§1.2) | 215 | **at least 150 removed**; the file ends under 750 lines |
| Lines added outside the lane (`queryClient.ts`, wrapper, provider) | 0 | at most 80 |
| `useRepairsLane.test.ts` | 22 tests, 2.72 s wall (`vitest run`, this Mac) | 22 tests, at most 3.5 s |
| `tsc --noEmit -p web` | 0 errors, 8.9 s | 0 errors |
| Gzipped JS, all chunks | 636 KB | at most 650 KB |
| Compiler bailouts in the lane | measured in the PR | not higher |
| Bugs the pilot must make impossible by construction | — | a response for an old fixer / plan / filter landing under the new one (`fbf317ed9`'s shape); a poll surviving unmount or an inactive lane (`273e244d2`'s shape) |

If the bar is missed, the PR is reverted and D48 is closed as "no".

### 4.7 Rollback

Revert the PR. It adds one dependency and touches one lane; no server change,
no data, no stored state. The devtools import is dev-only.

### 4.8 Risks

- The React Compiler's view of `useQuery` results is not documented upstream;
  the logger run in §4.5 is the check.
- `refetchInterval` plus fake timers in later lanes (Metadata, Regroup tests
  use `vi.useFakeTimers`): advance timers explicitly or switch those tests to
  real timers with mocked polls.
- 05 PR 14 (§3.6): sequence the pilot before 12D or inside the re-point.

### 4.9 Follow-on order, if the pilot passes

1. **Regroup** (904 lines; at most 500 rows; its generation counter and 5
   controllers go) — M.
2. **Dupes** (1,223 lines; server-paged; counts from the server already) — M.
3. **Library.tsx** `books.changed` patch path (§3.2) — S.
4. **Metadata**, only together with D36 R3, never as a port of the full-load
   mode — M (inside R3).
5. The `api.ts` module-level caches (`cachedCandidatesQuery`,
   `cachedReviewResults` and their two test files) are deleted once no caller
   remains outside the query cache — S.

## 5. Owner decision

| Choice | Cost | What you get |
|---|---|---|
| **A. TanStack Query v5 pilot (recommended)** | +10.7 KB gzip; one PR of size M; one new concept for contributors | about 150-215 fewer lines in the pilot lane, the stale-response and orphan-poll bug shapes gone by construction, one invalidation path for SSE, devtools |
| B. SWR pilot | +7.8 KB; same PR size | no abort contract, weaker pagination; otherwise similar |
| C. In-house `useResource` on zustand | +1 KB; about 300 lines of new shared code to own | no devtools; we maintain the de-dup and gc logic ourselves |
| D. Keep the hand-written hooks | 0 | the four mechanisms in §1.1 stay; D36 R3 writes a fifth |
