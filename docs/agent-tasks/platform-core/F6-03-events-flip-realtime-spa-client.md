<!-- file: docs/agent-tasks/platform-core/F6-03-events-flip-realtime-spa-client.md -->
<!-- version: 1.1.8 -->
<!-- guid: c4bebd8c-0dc9-4e78-a620-116332cca63d -->
<!-- last-edited: 2026-11-19 -->

# F6-03 — F6 flip, one PR: outbox and realtime wired, books publishers on typed topics, hub A and `/api/events` deleted, the SPA's `eventSourceManager` rewritten onto `/api/core/v1/events`, the Activity page on the stream

| Field | Value |
|---|---|
| ID | F6-03 |
| Release / flip | R2, **the events flip F6** (one atomic PR, 09 §9.2.1) |
| Tier | **T4** (Opus, xhigh–max): the events flip moves the SPA's realtime client |
| Wave | W36 — the only PR of its wave; touches `main.go`, `internal/server/`, books' registration and the kernel and module wiring files; runs inside an announced **move window** (`internal/realtime` → core `realtime`) |
| Depends on | F4-08 (the bus, `Deps.Events`, `Registrar.Topics`, `Registrar.Subscribe`: named here since 2026-11-20, it was only transitive through F5-18), F5-18 (which also publishes the `core.task.*` topics, see the amendment), F6-01, F6-02, F6-04 (the outbox operator routes it mounts), F6-05 (the journal's live tail the SPA's log view moves onto) |
| Gate | Plan, then execute autonomously. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f6-03-events-flip
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F5-18, F6-01, F6-02, F6-04 and F6-05 are merged and the move window is announced. Read the four dark PRs'
descriptions for the deferred cells they handed to this PR.


## Amendment 2026-11-20 (pre-scope slice E7): READ BEFORE THE GOAL

Source: the coordinator's pre-scope of origin/main `f18ad2686`. Its content is inlined in this brief; the pre-scope notes (`.claude/notes/pc-tools/`) are gitignored coordinator scratch and are not in a clone, so do not look for them. This brief was last amended on 11-19 and
none of the 11-19 findings reached it. **Where this section and the text below disagree, this section wins.** The
sections that #4798 and #4785 supersede (Goal 3, Goal 4, Goal 5, STOP 3, the producer table, the re-verify block, Steps
3 to 6, the tests and the acceptance) are also rewritten in place, so the two should not conflict; if they do, STOP and
tell the coordinator.

**Pre-slices that already exist (status, not proposals).**
- **#4785 (MERGED, `986b3e518`):** the SPA reader, `web/src/services/coreEvents/`, unwired (`index.ts:7`).
- **#4798 (OPEN, head `b1a30db39`, 0B/0M as #4795):** the relocation staged once in the store's single path-write
  funnel (`updateBookLockedMode`, `stageBookRelocated`), plus its consumer and dedupe key. Merge it before this flip is
  cut. Its `seams.d/F6-03.md` "Left for the flip" is this PR's checklist (blocker 2).

**Coordinator rulings of 2026-11-20 that bind this row.**
- **Merge-order ruling.** F6-03 and F8-04a do not depend on each other. Whichever is green first after F5-18 merges goes
  first; the second rebases the shared lines (blocker 15) and the second to land deletes
  `internal/server/toplevel_auth_chain.go` and its test. This amends the 11-19 23:22 ruling, which put F8-04a after
  F6-03. The plan's wave numbers (F6-03 W36, F8-04a W38) are the dispatch order, not a merge gate.
- **Status-publisher ruling.** F8-04a owns `Registry.SetStatusPublisher(health.PublishStatus(...))` and so the publisher of
  `core.system.status-changed.v1`. F6-03 publishes nothing on it (blocker 14).
- **Task-topics ruling.** F5-18 publishes `core.task.run-changed.v1`, `core.task.log-appended.v1` and
  `core.task.finished.v1` (slice E6b, see the F5-18 brief's amendment). F6-03 STOPs at dispatch if they are not
  published on `main` (blocker 1).
- **Deluge-moves ruling.** The version-swap move (`internal/server/version_lifecycle.go:335`) and the import move
  (`internal/deluge/import.go`) stay in-process. They are not book relocations, the path-update kind acts on the Active
  version only, and "one publisher per event" holds because the organize move is the only one the consumer replaces. A
  test pins it (Tests). File a TODO fragment for durable swap and import events after R2.

**The 15 blockers the brief did not list** (anchors re-verified at `f18ad2686`; *carried* = found 11-19, *new* = since,
*changed* = a PR since changed what the flip must do).

1. **Nothing declares or publishes the three task topics** *(carried; decided but not written)*. `core.task.run-changed.v1`,
   `core.task.log-appended.v1` and `core.task.finished.v1` have 0 hits in the diff of #4796, #4805, #4803, #4800, #4772
   and #4765. `kernel.coreTopics()` returns only `health.StatusTopicSpec()` (`core/kernel/events.go:74`; the comment at
   `:68-73` says the task topics come "with the tasks flip (F5)"). F6-03 needs them for Goal 5's Activity page and keyed
   log stream, `TestHub_TaskTopicVisibleWithoutTasksView`, `TestRunRedaction_SameOnStreamAndREST`, and the merged client,
   which already names `core.task.log-appended.v1` as keyed-only (`web/src/services/coreEvents/manager.ts:35`). Owner:
   F5-18 (task-topics ruling). Slice E6a (specs in `core/tasks/topics.go` plus one line in `coreTopics()`) may land dark on `main` first.
2. **#4798 supersedes Goal 3, Step 4, STOP 3 and the 14-producer table** *(changed)*. No producer stages anything, so
   STOP 3 cannot fire. Of the 17 files the old producer grep finds, 14 move no book with their own batch (the grep
   matches the undo journal type `undo.ChangeTypeBookPathUpdate`; `kindrename/map_gen.go` and `deluge/kinds.go` are not
   producers). Books already runs on core storage in production (`newLegacyGatedDB`,
   `internal/books/store/legacyrange.go:116`), so `errRelocationNeedsCoreBatch` is not hit on the served store. **What the
   flip still owes** (from #4798's `seams.d/F6-03.md`):
   - call `SetRelocationOutbox` before the store takes writes
   - declare `BookRelocatedTopicSpec()` through F4-08's `Registrar.Topics`
   - add the outbox owner to the storage owners
   - list `books.deluge.book-relocated` in `Manifest.OutboxConsumers` and register it through `Registrar.OutboxHandler`
   - delete `NotifyDelugeAfterOrganize` (one caller, `internal/server/handlers/organize.go:428`, plus the re-export at
     `internal/server/deluge_integration.go:43-48`) and the `handleBookRelocated` placeholder
   - write `TestRelocationFollowUpDurable`, pinning `Live()` (m5)
   - queued minors: m1 (`ResumeFromZero` plus a retryable `MoveStorage`), m2 (unused `from_path`/`to_path` in the payload),
     m4 (O(N^2) `stageFold` decodes). m2 and m4 are slice G1, after #4798 merges; m1 and m5 belong to the flip.
3. **The SPA client is a new module, not a rewrite in place** *(changed)*. #4785 landed `web/src/services/coreEvents/`.
   The flip **switches the consumers onto `coreEvents` and deletes `web/src/services/eventSourceManager.ts` and its
   test**: `App.tsx:19,145`, `TopBar.tsx:24,53`, `Library.tsx:52-55,528,1096`. #4785's round-2 review left these as
   **flip preconditions** (slice E1): m2 (`hooks.ts:60` fans every reset to every handler, no `streamKey` filter, no
   debounce), m3 (a keyed-only guard on shared dispatch and a test pinning the `log-appended` exclusion), m1 (per-connection
   backoff reset), m4 (double resync on a hidden reopen), n1, n2.
4. **Hub A's six frames map to different successors** *(carried)*.
   - `system.shutdown` (`internal/server/server_lifecycle.go:1000`): read by `App.tsx:146` (the shutdown backdrop) and
     relied on by backup restore (`web/src/hooks/useBackupHandlers.ts:130`). Successor `core.stream.closed.v1` with
     reason `shutdown` (`coreEvents/types.ts:30-34`).
   - `scan.auto_triggered` (`:926`): no SPA reader. Delete it.
   - `system.status` (`:885`): no SPA reader; its publisher is F8-04a's (status-publisher ruling). No topic here.
   - `books.changed` (`internal/realtime/book_changes.go`) is a batched `{kind, ids[]}` where deleted supersedes created
     and updated; the topic `books.book.changed.v1` is one frame per book (`coreEvents/types.ts:52`, keyed, latest-wins
     `Coalesce`). `ClientBuffer` defaults to 128 (`core/realtime/realtime.go:72`), so a bulk organize of a few hundred
     books overflows it and the client gets `reset{overflow}` and a full resync. **Ruling Q3:** keep the per-book topic; the
     SPA batches per 250 ms window and keeps deleted-supersedes client-side (slice E2); raise `ClientBuffer` only if a
     bulk-organize test shows repeated overflow resyncs.
   - `operation.log`, `operation.progress`, `operation.status` (listeners at `Library.tsx:533-562`): `SendOperation*`
     (`internal/realtime/events.go:146-198`) has no caller. Dead listeners; delete them.
5. **The iTunes service's `Realtime` field is never set: delete it, do not re-point it** *(carried)*
   (`internal/itunes/service/service.go:28`). Two `internal/arch/layering_test.go` exceptions go stale (`:328`, `:329`),
   and so do the `internal/realtime` layer entries (`:268`, `:516`) and the comment at `:80`.
6. **Hub A plumbing not in the file table** *(carried)*: the `system.EventStreamer` interface
   (`handlers/system/interfaces.go:125-127`), its mock and its `.mockery.yaml:240` entry; `HandleEvents`
   (`handlers/system/handler.go:773`) and the `getHub` plumbing (`:78`, `:258`, `:291`, `wire_handlers.go:373`); the manifest
   row `legacy_manifest.go:294` (`fNoCompress`); the gzip exclusion at `server_lifecycle.go:2712`, which **shares one
   condition with `/api/health` at `:2711` (F8-04a's)**; `toplevel_auth_chain.go` (serves only `/metrics` and `/api/events`:
   the second of F6-03 and F8-04a to land deletes it and its test). Step 5's "`SkipPaths`" does not exist in non-test code.
7. **The reach oracle** *(carried)*. Deleting the gin route makes `GET /api/events` R0-only: add it to `reachWantR0Only`
   (`internal/books/cutover/reach_gate_test.go:369`), drop `reach_golden_test.go:45-47`, re-pin `reach_baseline_test.go`.
   F4-08 owns the oracle's core-route half; F8-04a and F8-04b edit the same three files, so the flips merge one at a time.
8. **The acceptance greps cannot reach 0 as written** *(carried; one new hit)*. `grep -rn '/api/events'` hits the R0
   recordings (`internal/books/rbacreach/`, `scripts/r2corpus/record_r0_reach.py:497`,
   `scripts/tests/test_record_r0_reach.py:586-590`) and two comments (`internal/server/server.go:253`, `:435`). Use the
   `git grep` with pathspec excludes below. `new EventSource(` stays at 1 at BASE: `openOperationsSSE` (`web/src/services/api.ts:773`, URL `/operations/events`), whose one caller is
   `useOperationsStore.ts:411` (plus `appApiMock.ts:59`, `App.health.test.tsx:69` and 6 mock sites in `useOperationsStore.test.ts`).
   F5-18's brief never deletes it, so **F6-03 owns it**: switch `useOperationsStore` onto `coreEvents` (the `core.task.*` topics) and delete
   `openOperationsSSE`, `OperationSSEHandler` and the mocks. Record the BASE count.
9. **The e2e harness mocks `window.EventSource`, so a fetch reader escapes it** *(new)*. `mockEventSource`
   (`web/tests/e2e/utils/test-helpers.ts:314-356`) is installed for every mocked page (`:2093`, `setup-modes.ts:63`). The
   new client calls `fetch('/api/core/v1/events')`, which that mock does not intercept, so every e2e page would open a real
   stream request against the mock server and never stop reconnecting (by design). `error-handling.spec.ts:126-157`
   asserts on the `/api/events` mock instance; there are 54 `EventSource` references in 15 e2e files. **Requirement: a
   `page.route` fetch-stream mock** (new `web/tests/e2e/utils/mockCoreEvents.ts`, slice E3: a controllable `ReadableStream`
   body with frames, `closed{shutdown}` and a 429 with `Retry-After`), every spec switched to it, and no request reaching
   `/api/core/v1/events` unmocked. `web/tests/` is now in this row's file list.
10. **F6-05's obligations** *(new)*. Declare `logjournal.TailSpec(pred)` **after** P7 has collected the module's
    `LogOtherUserData` predicate and **before** `bus.Freeze()` (declared earlier with nil, the tail never flags another
    user's line; `seams.d/F6-05.md:18`). Size `realtime.Options.RingSize` for the tail: it defaults to 1024 and at info
    level the tail takes most slots in the shared ring, so every other topic's `Last-Event-ID` window collapses
    (`seams.d/F6-05.md:20`). **Ruling Q8:** set `RingSize` to 4096 in the kernel realtime wiring and expose it in the core
    section's existing keys; a per-topic ring is a spec change and too large for R2. About 13 dark `logjournal` identifiers in
    `scripts/deadcode-baseline.txt` (`:40`) become reachable; prune them.
11. **F6-02's obligations** *(carried; one correction)*. Build the hub at P5 and call `Hub.Subscriptions()` once before
    `Freeze` (`seams.d/F6-02.md:19`). `Subscriptions()` does not fail when the control topics are undeclared: it logs a
    warning and skips them (`core/realtime/realtime.go:295-302`), so a dark `core/kernel/realtime.go` can be written
    before F4-08 (slice E4). `Deps.Refresh` is required: size it for `MaxClients / Heartbeat` calls a second, make it safe
    for concurrent calls, and rebuild a key principal over the key's own scopes (`:21`). A test that `SetWriteDeadline` and
    `Flush` reach the writer through the real `httpmw` and router chain, on HTTP/1.1 and HTTP/2 (`:29`). `Hub.Close` from
    `RegisterOnShutdown` and `Hub.Run` from the lifecycle (`:54`).
12. **F6-01's obligations** *(carried; `seams.d/F6-01.md:23-31`)*. Append `outboxOwnerSpec()` to `storageOwners` (built and dark
    at `core/kernel/outbox.go:73`). Pass `RelayDefinition()` to the executor and call `BindRuns`. `DeclareConsumers` for
    every manifest before `Register`, then `Freeze`; a disabled module's raw consumers are declared with no handler.
    Extend `archtest.OutboxModuleConsumersAreTasks` to the root module.
13. **The brief did not name its F4-08 dependency** *(carried)*. It needs `Registrar.Topics`, `Registrar.Subscribe` and
    `Deps.Events` (0 hits in `core/module/{registrar,deps,manifest}.go` on `main`); the bus is built by nothing until F4-08
    (`core/kernel/events.go:21-36`). `Depends on` now lists F4-08.
14. **F6-03 and F8-04 both claimed the status-topic publisher** *(new)*. Resolved by the status-publisher ruling: F8-04a (F8-03's follow-up 2,
    `seams.d/F8-03.md:81`, and TODO-F8-03-BOOT-WIRING, `TODO.md:353`). Goal 2's `system.status` mapping is deleted.
15. **Hot spots shared with F8-04a, which can now merge on either side** *(new)*: `server_lifecycle.go:2711-2712` (one
    condition); `App.tsx` (the stream at `:19,145`, the health poll at `:173`); `web/tests/e2e/utils/test-helpers.ts` (the
    EventSource mock and the health mocks at `:473,1837`); `legacy_manifest.go`; `toplevel_auth_chain.go`; the reach tests;
    every wiring file. Whichever lands second rebases these lines; neither PR may reformat them.

**Smaller corrections.** The re-verify grep `OutboxConsumers\|OutboxHandler` expects 0 and gets 1 (a comment at
`core/module/validation.go:94`): grep the type members. `TestJournal_RequiresSystemManage` is F5-19's
(`core/internal/loghttp/permissions_test.go`), not F6-05's. The F6-04 cell bodies exist after each `t.Skip`
(`core/internal/outboxhttp/routes_test.go:609`, `:619`, `:646-647`); slice G3 is the core-only PR that wires `stage13Do` once
F4-08 merges.

**Dark slices that may start on `main` before this flip** (pre-scope section 3; none is part of this PR):
E1 coreEvents preconditions, E2 consumer adapters (batcher, shutdown hook), E3 e2e stream mock, E4 `core/kernel/realtime.go`
(dark, `RingSize` 4096), E6a task topic specs. Gated: G1 #4798 minors, G2 kernel wiring branch, G3 outboxhttp stage-13
cells, G4 Activity and system-log views onto the stream (after F5-18). If a slice has merged by your BASE, this PR does not
redo it.

## Goal

1. **Wire** the outbox (relay kind registered, `Deps.Outbox`, `Registrar.OutboxHandler`, `Manifest.OutboxConsumers`, the
   F6 members of 06 §6.4.10, added here to `core/module/manifest.go`, `registrar.go` and `deps.go`) and the realtime hub
   (created in P5, `GET /api/core/v1/events` mounted) for ABO, in `core/kernel/boot.go` and `kernel.go`. Mount F6-04's
   outbox operator routes (`core/internal/outboxhttp`) and F6-05's live tail (`core.log.recorded.v1` declared and
   delivered). Regenerate `core/openapi/core.json` and the client under `core/web/packages/api-client/src/generated/`
   for the routes this PR mounts.
2. **Books' topics** (05-events §5E.13, 05-realtime §11): HEAD's hub A (`internal/realtime/events.go`) and
   `book_changes.go` are replaced by the hub plus books' catalog topic `books.book.changed.v1`
   (`Realtime{Coalesce: 250ms}`, the coalescing rule of `BookChangeCoalescer`); hub A's `system.status` and `scan.auto_triggered` frames
   are deleted (no SPA reader; the status topic's publisher is F8-04a's, the status-publisher ruling); HEAD's 5-second library counts get no
   topic here. Every other `realtime.EventHub` publisher moves to typed `events.Publish` through `Deps.Events`, with the
   frame mapping of blocker 4.
3. **Durable relocation follow-up** (superseded by #4798, blocker 2): books already stages `books.book.relocated.v1`
   once, in the store's single path-write funnel (`updateBookLockedMode` through `stageBookRelocated`), so no producer
   stages anything and there is no producer list. This PR wires what #4798 left for the flip: `SetRelocationOutbox`,
   `BookRelocatedTopicSpec()` through `Registrar.Topics`, the outbox storage owner, the consumer
   `books.deluge.book-relocated` in `Manifest.OutboxConsumers` registered through `Registrar.OutboxHandler`
   (`outbox.ConsumeIntoTask` with `Live()` onto `books.deluge.path-update` or its 05-download successor), m1
   (`ResumeFromZero`, retryable `MoveStorage`) and m5 (the `Live()` pin); it deletes `NotifyDelugeAfterOrganize` and the
   `handleBookRelocated` placeholder, so the relocation follow-up becomes durable for the first time. The version-swap and
   import moves stay in-process (Deluge-moves ruling).
4. **Delete** hub A, `/api/events` and its gin route and compression exclusion, and `internal/realtime` (04 key: F6 move
   `internal/realtime` → `mediacore/realtime`); delete `internal/itunes/service`'s `Realtime *realtime.EventHub` field (it is never set, blocker 5) with the layering entries it justified.
5. **SPA** (05-realtime §4.8 items 1–6, §11 table; 07 §7.9.2; superseded in shape by #4785, blocker 3): the reader already
   exists as `web/src/services/coreEvents/` (`fetch` + `ReadableStream`, per-tab mode, `Last-Event-ID`, 3 s → 60 s backoff
   with jitter and no give-up, `redirect: "manual"` with the `opaqueredirect` → `ApiAuthRedirectError` rule, unnamed
   frames, `core.stream.reset.v1` resync, `core.stream.closed.v1` reconnect, visible-tab-only). This PR **switches the
   consumers onto it and deletes `web/src/services/eventSourceManager.ts` and its test**: `App.tsx`, `TopBar.tsx`,
   `Library.tsx` (through the E2 book-change batcher and the shutdown hook), the Activity page (from polling
   `GET /api/core/v1/tasks/runs` to `useTopic` over `core.task.run-changed.v1`, a run's log on its keyed stream
   `topic=core.task.log-appended.v1&key=<run id>` or the REST log route) and the system-log view (from polling
   `GET /api/core/v1/system/logs` to the live tail `core.log.recorded.v1`, 05-logging §5). It lands the #4785 round-2
   preconditions (E1) if they have not merged, and the e2e fetch-stream mock (blocker 9).

## Spec

09 §9.4 R2 flips table F6 row, §9.6 row 2; 03 §3.8 F6; 05-realtime §4.7, §4.8, §11; 05-events §5E.11 (the operator
routes F6-04 built), §5E.13 (table and "Order" item (3)); 05-logging §5 (live tail; when the surface lands); 07 §7.9.2,
§7.19 step 3; 06 §6.4.10 F6 row; 05-http-api §8.1 F6 row, §4.2 stage 9.

## Files (the row's list)

| Group | Paths |
|---|---|
| wiring (this PR owns them in W36) | `main.go`, `internal/books/module.go`, `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| generated | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` |
| relocation follow-up (the 14-producer table is deleted: #4798 stages in the store's single funnel, blocker 2) | `internal/books/store/book_relocated_outbox.go`, `internal/books/store/pebble_store.go` (the `SetRelocationOutbox` call site; m2 and m4 are slice G1), `internal/plugins/deluge/relocation_consumer.go`, `internal/plugins/deluge/kinds.go`, `internal/plugins/deluge/path_update.go` (the `handleBookRelocated` placeholder), `internal/server/handlers/organize.go` and `internal/server/deluge_integration.go` (delete the in-process organize move and `NotifyDelugeAfterOrganize`). No producer file is edited |
| hub A and its callers | `internal/realtime/` (deleted), `internal/server/` (including `server_lifecycle.go:2711-2712`, `legacy_manifest.go:294`, `handlers/system/{interfaces,handler}.go`, `wire_handlers.go`, the `system.EventStreamer` mock and `.mockery.yaml:240`, and `toplevel_auth_chain.go` plus its test if F8-04a has landed), `internal/itunes/service/` (delete the unset `Realtime` field) |
| SPA | `web/src/services/coreEvents/**` (E1 preconditions, E2 adapters), `web/src/services/eventSourceManager.ts` and its test (deleted), `web/src/App.tsx`, `web/src/components/layout/TopBar.tsx`, `web/src/pages/Library.tsx`, `web/src/hooks/useBackupHandlers.ts` (the shutdown contract), `web/src/services/api.ts` (delete `openOperationsSSE`), `web/src/stores/useOperationsStore.ts` and its test (onto `coreEvents`), `web/src/test/appApiMock.ts`, `web/src/App.health.test.tsx`, the Activity page and the system-log view, `web/src/` |
| e2e | `web/tests/e2e/utils/mockCoreEvents.ts` (new, E3), `web/tests/e2e/utils/test-helpers.ts`, `web/tests/e2e/utils/setup-modes.ts`, `web/tests/e2e/error-handling.spec.ts` and the other 12 e2e files that reference `EventSource` (54 references in 15 files at `f18ad2686`) |
| kernel and reach | `core/kernel/realtime.go` (new, E4), `core/archtest/outbox_consumers.go` (extend to the root module), `internal/books/cutover/reach_gate_test.go`, `reach_golden_test.go`, `reach_baseline_test.go` (blocker 7), `scripts/deadcode-baseline.txt` (prune the `logjournal`, outbox and #4798 dark lines) |
| move bookkeeping | `internal/arch/layering_test.go` (`internal/realtime` leaves `layerOf`; the `:80`, `:268`, `:328-329`, `:516` entries go stale), `scripts/ci/short_test_weights.json` |
| F6-04's skipped cells | `core/internal/outboxhttp/routes_test.go` (remove the `t.Skip("needs stage 13: F4-08")` calls AND wire `stage13Do`: F6-04 wrote every cell's body, the 401, 403 and interactive-guard matrix, after its `t.Skip`, but `stage13Do` builds its router with `httpapi.Deps{Log}` only, which `Build` refuses (`ErrNoAuthService`) once F4-08 lands; point it at F4-08's `Deps.Auth`, RBAC and audit and authenticate the caller the way stage 12 does, so the principal the cell binds reaches the route) |

Paths outside this table and the inventory row are pre-registered in `inventory.json` (amendment 2026-11-20). The old
producer-list paragraph is deleted: with #4798 no producer file belongs to this PR.

## Re-verify at BASE (HEAD `ac4368bdb` values shown)

```bash
ls internal/realtime/                                                         # HEAD: book_changes.go book_changes_test.go events.go events_test.go
grep -rln 'internal/realtime"' --include=*.go . | grep -v _test | grep -v '^./internal/realtime'   # HEAD: internal/itunes/service/service.go, internal/server/server.go, internal/server/server_lifecycle.go
grep -c 'realtime.NewEventHub()' internal/server/server.go                    # HEAD 1 (:1082)
git grep -c '/api/events' -- internal/server/server_lifecycle.go internal/server/legacy_manifest.go   # f18ad2686: route :2820, gzip :2712, manifest :294 (server.go has 0: the old grep was wrong)
grep -rln 'eventSourceManager' web/src | grep -v test | wc -l                 # f18ad2686: 5 (App.tsx, TopBar.tsx, Library.tsx, the manager, a comment at coreEvents/index.ts:7); 0 after this PR
grep -rn 'tasks/runs' web/src --include=*.ts --include=*.tsx | wc -l          # f18ad2686: 0 (the Activity page reads /api/v1/activity); > 0 once F5-18 merges
ls web/src/services/coreEvents/ | wc -l                                       # > 0 (#4785 merged)
grep -rn 'func NewHub' core/realtime/ | wc -l                                 # 1 (F6-02)
grep -rn 'func ConsumeIntoTask\[' core/outbox/ | wc -l                        # 1 (F6-01)
grep -rln 'path_update\|path-update' --include=*.go internal/plugins/deluge | wc -l   # >= 1 (the books.deluge.path-update kind)
# Superseded: the 14-file producer grep is deleted (17 hits at f18ad2686, of which 14 move no book). Instead:
grep -rn 'stageBookRelocated' internal/books/store/ --include=*.go | grep -v _test | wc -l   # >= 2 once #4798 has merged (STOP if it has not)
grep -rn 'core.task.run-changed.v1\|core.task.log-appended.v1\|core.task.finished.v1' core/ --include=*.go | grep -v _test | wc -l   # >= 3 (E6a declarations alone satisfy this; it proves nothing about publishing)
grep -rln 'Publish' core/internal/executor/ --include=*.go | grep -v _test | wc -l   # >= 1 (the E6b publisher; STOP if 0)
(cd core && go test -count=1 ./internal/executor/ -run 'TestExecutor_PublishesRunChangedAfterCommit|TestExecutor_LogAppendedKeyedByRunID')   # ok (F5-18's tests, names coined by its amendment; STOP if no tests match or they fail)
grep -rn 'func (r \*Registrar) Topics\|func (r \*Registrar) Subscribe' core/module/ | wc -l   # >= 2 (F4-08; STOP if 0)
grep -rn 'package outboxhttp' core/internal/outboxhttp/ | wc -l               # >= 1 (F6-04)
grep -c 'needs stage 13: F4-08' core/internal/outboxhttp/routes_test.go       # >= 3 (F6-04's skipped permission tables)
grep -rn 'core.log.recorded.v1' core/internal/logjournal/ | wc -l             # >= 1 (F6-05)
grep -rnE 'OutboxConsumers +\[\]|\) OutboxHandler\(' core/module/ | wc -l       # 0 (this PR adds the members; a plain grep finds 1 comment, core/module/validation.go:94)
```

## STOP conditions

1. A first-party consumer of `/api/events` or of hub A's frames remains after this PR (the SPA, `scripts/`, skills).
2. The new client gives up reconnecting, follows a cross-origin redirect, or reads `response.redirected`.
3. (Replaced 2026-11-20.) #4798 is not merged, or a book path write reaches the store outside `updateBookLockedMode` (a raw
   engine write that bypasses the funnel): report its file and line; do not stage in a second batch and do not edit a
   producer. With #4798 merged and the served store core-backed (`legacyrange.go:116`) this cannot fire.
4. Any subscriber receives an event its rights do not allow.
5. The three `core.task.*` topics are not **published** on `main`: the executor has no `Publish` call (second grep above is 0) or F5-18's two publisher tests are absent or red; declarations from slice E6a alone do not satisfy this. Or F4-08's `Registrar.Topics` is absent.
   Do not declare or publish them here: that is F5-18's slice E6b.
6. An e2e page reaches `/api/core/v1/events` without the fetch-stream mock.

## Steps (one PR; every commit builds)

1. Write the tests below first.
2. Add the F6 members to `core/module/` (06 §6.4.10 F6 row: `Manifest.OutboxConsumers`, `Registrar.OutboxHandler`,
   `Deps.Outbox`); wire the relay, the hub (P5), `eventhttp`, F6-04's `outboxhttp` (its `Register` takes the outbox's
   `Operator`, the `events.Catalog` and the `audit.Logger`) and F6-05's tail topic in `core/kernel/boot.go` and
   `kernel.go`; regenerate `core/openapi/core.json` and the generated client. Un-skip the permission, guard and 401
   cells of `outboxhttp` (`core/internal/outboxhttp/routes_test.go`: delete each `t.Skip("needs stage 13: F4-08")` and wire `stage13Do` to F4-08's auth, RBAC and audit `Deps`, with the caller authenticated as stage 12 does),
   which run now that F4-08's stage 13 is live and the package is mounted.
3. Books' topic declarations through `Registrar.Topics` in `internal/books/module.go` (`books.book.changed.v1` with
   `Realtime{Coalesce: 250ms}`, `BookRelocatedTopicSpec()`); move the hub A publishers to `events.Publish` with the frame
   mapping of blocker 4 (delete `scan.auto_triggered`, `system.status` and the dead `operation.*` listeners).
4. The durable relocation wiring only (#4798 staged it): `SetRelocationOutbox` before the store takes writes, the outbox
   storage owner, the consumer in `Manifest.OutboxConsumers` through `Registrar.OutboxHandler` with `Live()`, m1 and m5;
   delete `NotifyDelugeAfterOrganize`, its re-export and `handleBookRelocated`. Keep the version-swap and import moves
   (Deluge-moves ruling).
5. Delete hub A, `internal/realtime`, `/api/events` (the route at `server_lifecycle.go:2820`, the `:2712` exclusion, the
   manifest row `legacy_manifest.go:294`, `EventStreamer` with its mock and `.mockery.yaml` entry, `HandleEvents`, and
   `toplevel_auth_chain.go` if F8-04a landed first), and delete the iTunes `Realtime` field. Update every test that mounts
   `/api/events` (`handler_test.go`, `perimeter_stages_test.go`, `cf_recovery_test.go`, `server_extra_test.go`,
   `toplevel_auth_chain_test.go`, `wire_abs_routes_test.go`, `legacy_manifest_test.go`,
   `internal/books/perimeter_app_test.go`). Reach oracle (blocker 7).
6. SPA: switch `App.tsx`, `TopBar.tsx`, `Library.tsx`, the Activity page, `useOperationsStore` (and delete `openOperationsSSE`) and the system-log view onto `coreEvents`
   (`useTopic` / `useResync`, the E2 batcher and shutdown hook); **delete `eventSourceManager.ts` and its test**; switch
   the e2e suite to `mockCoreEvents.ts` (E3); update each switched consumer's test.
7. Layering (`layerOf`, `:80`, `:268`, `:328-329`, `:516`) and shard weights; the deadcode-baseline prune; `core.json` and the
   generated client; headers; one `changelog.d/` fragment; Acceptance.

## Tests first

- The stream tests now run on ABO's real topics: `TestHub_TaskTopicVisibleWithoutTasksView` and
  `TestRunRedaction_SameOnStreamAndREST` on `core.task.run-changed.v1` (deferred by F6-02);
  `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` (05-rbac §12: a key scoped only `books:scan` passes
  `AnyAuthenticated` on `GET /api/core/v1/events`).
- `TestRelocationFollowUpDurable` (name coined here): pins `Live()` (#4798 m5); a path write stages
  `books.book.relocated.v1` in its own batch (through the store funnel); a crash after the commit and before delivery
  still starts exactly one `books.deluge.path-update` run after restart.
- A version-swap and an import produce **no** relocation message (pins the Deluge-moves ruling and the "no create event" rule).
- A shutdown frame reaches the SPA as `core.stream.closed.v1{shutdown}` over HTTP/2 (`seams.d/F6-02.md:29`) and `App.tsx`
  shows the backdrop (backup restore relies on it, `useBackupHandlers.ts:130`).
- A bulk book change of more than `ClientBuffer` books yields one batched update or one `reset{overflow}` followed by one
  resync, never a stuck list (ruling Q3). The tail's Redact and other-user flag hold on the real predicate
  (`seams.d/F6-05.md:18`).
- `TestAPIEventsGone` (name coined here): `/api/events` answers the legacy mount's not-found and no handler serves it.
- vitest: the existing `coreEvents` suite (#4785) stays green with the E1 preconditions added (per-`streamKey` reset
  filtering and debounce, the keyed-only guard with a test that pins the `log-appended` exclusion, per-connection backoff
  reset, a single resync on a hidden reopen), plus one test per switched consumer (`App.tsx`, `TopBar.tsx`, `Library.tsx`,
  the Activity page, the system-log view). `eventSourceManager.test.ts` is deleted with its manager.
- The event half of `TestJournal_RequiresSystemManage` (F6-05's) and F6-04's route tests now run on ABO's mounted
  routes and topics, the un-skipped cells of `TestOutboxHTTP_DeadLetterRoutesNeedSystemManage`,
  `TestOutboxHTTP_PurgeNeedsInteractive` and `TestOutboxHTTP_ConsumersAndTopicsNeedSystemView` among them (403, the
  interactive guard and 401 enforced by stage 13).
- `TestDisabledModuleOutboxRetained` (06 §6.10, COR-12): a message for a disabled module's declared consumer survives
  `orphan_retention` and another consumer's ack, and is delivered after re-enable (it needs this PR's
  `Manifest.OutboxConsumers`).
- Extend `TestDepsScoping` (06 §6.10; R1-20's, grown at F4-08) with the F6 cell: staging through `Deps.Outbox` to a
  topic outside the module's catalog entries is refused.
- Extend `TestCoreRoutesReplaceGin` (05-http-api §12; F4-08's) with the F6 cell: no gin route remains at a path this
  flip's native routes replace (`/api/events` included).
- e2e: the Activity page shows a run's live progress without polling; the system-log view shows a new journal line
  without polling. **Every e2e page installs the fetch-stream mock** (`web/tests/e2e/utils/mockCoreEvents.ts`, a
  `page.route` for `/api/core/v1/events` with a controllable `ReadableStream`: frames, `closed{shutdown}`, a 429 with
  `Retry-After`); `mockEventSource` is removed (F6-03 deletes `openOperationsSSE`, so nothing needs it), and a
  guard asserts that no request reaches `/api/core/v1/events` unmocked.

## Invariants

- Every intermediate `main` boots and passes e2e; the PR is atomic.
- One publisher per event on `main` (05-events §5E.13 "Order").
- No old stream URL is redirected or kept (D10).

## Acceptance

```bash
go build ./... && go vet ./...                                                         # exit 0
go test -count=1 ./internal/server/... ./internal/books/... ./internal/itunes/...       # ok
ls -d internal/realtime 2>/dev/null | wc -l                                            # 0
git grep -n '/api/events' -- internal web/src scripts .claude/skills ':!internal/books/rbacreach' ':!scripts/r2corpus' ':!scripts/tests/test_record_r0_reach.py' | grep -v '/api/core/v1/events' | wc -l   # 0 (fix the two server.go comments)
grep -rn 'new EventSource(' web/src | grep -v test | wc -l                             # 0 (this PR deletes api.ts openOperationsSSE and switches useOperationsStore; record the BASE value, 1)
ls web/src/services/eventSourceManager.ts 2>/dev/null | wc -l                          # 0
go test -count=1 ./internal/books/cutover/ -run 'TestReach'                            # ok (the reach oracle, blocker 7)
(cd web && npm run build && npm test -- --run)                                         # exit 0 (the coreEvents suite plus one test per switched consumer)
(cd core && go test -race -count=1 ./realtime/... ./outbox/... ./internal/outboxhttp/...)   # ok
grep -c 'needs stage 13: F4-08' core/internal/outboxhttp/routes_test.go                 # 0 (un-skipped)
go test -count=1 -run 'TestDisabledModuleOutboxRetained|TestDepsScoping|TestCoreRoutesReplaceGin' ./... ; (cd core && go test -count=1 -run 'TestDisabledModuleOutboxRetained|TestDepsScoping' ./...)   # ok
make -C core ci                                                                        # exit 0
make ci                                                                                # exit 0
make test-e2e                                                                          # exit 0, with the fetch-stream mock and no unmocked /api/core/v1/events request
```

## Out of scope

The status-topic publisher (F8-04a, the status-publisher ruling); durable version-swap and import relocation events (a TODO after R2, Deluge-moves ruling); `@mediacore/shell`'s client and tab leader election (F9, or R3); webhook notifications on `notify` (F7); the plugin
`EventBus`, which keeps feeding the webhook plugin until F7 (05-events §5E.13 "Order" item (4)).

## Rollback

As F3: a deployed build rolls back with `make rollback-restore`; a revert on `main` is the lead's decision.

## Repo rules

Worktree only; file headers on every file, TS included (fragments headerless); move PR rules (old ==> new list, every
commit builds); conventional commit ending `Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer;
`gh pr merge <n> --rebase` alone in its wave with every required check green, never squash, never `--admin`.

```
feat(events)!: events flip: outbox and realtime live, SPA on /api/core/v1/events, hub A deleted

Co-Authored-By: Claude <noreply@anthropic.com>
```

