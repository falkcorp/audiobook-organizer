<!-- file: docs/agent-tasks/platform-core/F6-03-events-flip-realtime-spa-client.md -->
<!-- version: 1.1.4 -->
<!-- guid: c4bebd8c-0dc9-4e78-a620-116332cca63d -->
<!-- last-edited: 2026-11-05 -->

# F6-03 — F6 flip, one PR: outbox and realtime wired, books publishers on typed topics, hub A and `/api/events` deleted, the SPA's `eventSourceManager` rewritten onto `/api/core/v1/events`, the Activity page on the stream

| Field | Value |
|---|---|
| ID | F6-03 |
| Release / flip | R2, **the events flip F6** (one atomic PR, 09 §9.2.1) |
| Tier | **T4** (Opus, xhigh–max): the events flip moves the SPA's realtime client |
| Wave | W36 — the only PR of its wave; touches `main.go`, `internal/server/`, books' registration and the kernel and module wiring files; runs inside an announced **move window** (`internal/realtime` → core `realtime`) |
| Depends on | F5-18, F6-01, F6-02, F6-04 (the outbox operator routes it mounts), F6-05 (the journal's live tail the SPA's log view moves onto) |
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

## Goal

1. **Wire** the outbox (relay kind registered, `Deps.Outbox`, `Registrar.OutboxHandler`, `Manifest.OutboxConsumers`, the
   F6 members of 06 §6.4.10, added here to `core/module/manifest.go`, `registrar.go` and `deps.go`) and the realtime hub
   (created in P5, `GET /api/core/v1/events` mounted) for ABO, in `core/kernel/boot.go` and `kernel.go`. Mount F6-04's
   outbox operator routes (`core/internal/outboxhttp`) and F6-05's live tail (`core.log.recorded.v1` declared and
   delivered). Regenerate `core/openapi/core.json` and the client under `core/web/packages/api-client/src/generated/`
   for the routes this PR mounts.
2. **Books' topics** (05-events §5E.13, 05-realtime §11): HEAD's hub A (`internal/realtime/events.go`) and
   `book_changes.go` are replaced by the hub plus books' catalog topic `books.book.changed.v1`
   (`Realtime{Coalesce: 250ms}`, the coalescing rule of `BookChangeCoalescer`); hub A's `system.status` frame becomes
   `core.system.status-changed.v1` for readiness; HEAD's 5-second library counts in that frame get no topic here. Every
   `realtime.EventHub` publisher moves to typed `events.Publish` through `Deps.Events`.
3. **Durable relocation follow-up:** books stages the durable topic `books.book.relocated.v1` in the same batch that
   moves a book, at every book-moving producer (the organizer, revert, undo and the fixers that name the path-update
   follow-up; list below), and a books consumer starts `books.deluge.path-update` (or its 05-download successor) with
   the book ID through `outbox.ConsumeIntoTask` with `Live()` (the relocation follow-up becomes durable for the first
   time).
4. **Delete** hub A, `/api/events` and its gin route and compression exclusion, and `internal/realtime` (04 key: F6 move
   `internal/realtime` → `mediacore/realtime`); re-point `internal/itunes/service`'s `Realtime *realtime.EventHub` field
   onto `Deps.Events`.
5. **SPA** (05-realtime §4.8 items 1–6, §11 table; 07 §7.9.2): **rewrite in place**
   `web/src/services/eventSourceManager.ts` onto `/api/core/v1/events` as a `fetch` + `ReadableStream` reader in
   per-tab mode: visible refusals with problem+json and `Retry-After`; `Last-Event-ID` on every reconnect; 3 s → 60 s
   backoff with jitter and no give-up; `redirect: "manual"` with the `opaqueredirect` → `ApiAuthRedirectError` rule;
   unnamed frames; `core.stream.reset.v1` resync and `core.stream.closed.v1` reconnect; a tab opens its stream only while
   visible. Expose `useTopic<T>(topic, onEvent, {key})` and `useResync(handler)`. The Activity page moves from polling
   `GET /api/core/v1/tasks/runs` to the stream; a run's log uses its own keyed stream
   (`topic=core.task.log-appended.v1&key=<run id>`) or the REST log route. The SPA's system-log view moves from polling
   `GET /api/core/v1/system/logs` (F5-18) to the live tail `core.log.recorded.v1` (05-logging §5 "When the surface
   lands").

## Spec

09 §9.4 R2 flips table F6 row, §9.6 row 2; 03 §3.8 F6; 05-realtime §4.7, §4.8, §11; 05-events §5E.11 (the operator
routes F6-04 built), §5E.13 (table and "Order" item (3)); 05-logging §5 (live tail; when the surface lands); 07 §7.9.2,
§7.19 step 3; 06 §6.4.10 F6 row; 05-http-api §8.1 F6 row, §4.2 stage 9.

## Files (the row's list)

| Group | Paths |
|---|---|
| wiring (this PR owns them in W36) | `main.go`, `internal/books/module.go`, `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| generated | `core/openapi/core.json`, `core/web/packages/api-client/src/generated/` |
| book-moving producers and the consumer kind (plan base `f7ac5b11e`) | `internal/organizer/inplace_multifile.go`, `internal/organizer/pipeline.go`, `internal/audiobooks/revert.go`, `internal/undo/current.go`, `internal/undo/engine.go`, `internal/undo/restorable.go`, `internal/plugins/deluge/path_update.go`, `internal/plugins/deluge/plugin.go`, `internal/plugins/maintenance/folder_books_fixer.go`, `internal/plugins/maintenance/fragment_consolidation_fixer.go`, `internal/plugins/maintenance/fs_regroup_xml.go`, `internal/plugins/maintenance/reconcile_fixer.go`, `internal/plugins/maintenance/retire_into.go`, `internal/books/store/change_type_class.go` |
| hub A and its callers | `internal/realtime/` (deleted), `internal/server/`, `internal/itunes/service/` |
| SPA | `web/src/services/eventSourceManager.ts`, `web/src/` |
| move bookkeeping | `internal/arch/layering_test.go` (`internal/realtime` leaves `layerOf`), `scripts/ci/short_test_weights.json` |
| F6-04's skipped cells | `core/internal/outboxhttp/routes_test.go` (un-skip only: remove the `t.Skip("needs stage 13: F4-08")` calls; no other change) |

The producer list was computed at the plan's base. F5-18 moves or deletes much of `internal/plugins/` before this PR,
so re-grep at BASE (below): every non-test file that names the path-update follow-up is this PR's file, wherever F5-18
put it.

## Re-verify at BASE (HEAD `f7ac5b11e` values shown)

```bash
ls internal/realtime/                                                         # HEAD: book_changes.go book_changes_test.go events.go events_test.go
grep -rln 'internal/realtime"' --include=*.go . | grep -v _test | grep -v '^./internal/realtime'   # HEAD: internal/itunes/service/service.go, internal/server/server.go, internal/server/server_lifecycle.go
grep -c 'realtime.NewEventHub()' internal/server/server.go                    # HEAD 1 (:1082)
grep -c '"/api/events"' internal/server/server.go                             # HEAD >= 1 (gzip exclusion :1604)
grep -rln 'eventSourceManager' web/src | grep -v test | wc -l                 # HEAD 4 (App.tsx, TopBar.tsx, Library.tsx, the manager)
grep -rn 'tasks/runs' web/src --include=*.ts --include=*.tsx | wc -l          # > 0 (F5-18's polling)
grep -rn 'func NewHub' core/realtime/ | wc -l                                 # 1 (F6-02)
grep -rn 'func ConsumeIntoTask\[' core/outbox/ | wc -l                        # 1 (F6-01)
grep -rln 'path_update\|path-update' --include=*.go internal/plugins/deluge | wc -l   # >= 1 (the books.deluge.path-update kind)
# The producer set: every non-test file naming the path-update follow-up. Plan base f7ac5b11e: 14 files (the table above,
# with change_type_class.go then at internal/database/). Your base: the same set wherever F5-18 moved it; any extra file is
# yours too, any file missing from the table and not deleted by F5-18 is a STOP.
grep -rli 'path-update\|PathUpdate' --include=*.go internal | grep -v _test | sort    # plan base: 14
grep -rn 'package outboxhttp' core/internal/outboxhttp/ | wc -l               # >= 1 (F6-04)
grep -c 'needs stage 13: F4-08' core/internal/outboxhttp/routes_test.go       # >= 3 (F6-04's skipped permission tables)
grep -rn 'core.log.recorded.v1' core/internal/logjournal/ | wc -l             # >= 1 (F6-05)
grep -rn 'OutboxConsumers\|OutboxHandler' core/module/ | wc -l               # 0 (this PR adds them)
```

## STOP conditions

1. A first-party consumer of `/api/events` or of hub A's frames remains after this PR (the SPA, `scripts/`, skills).
2. The new client gives up reconnecting, follows a cross-origin redirect, or reads `response.redirected`.
3. A book-moving producer cannot stage `books.book.relocated.v1` in the batch that moves the book (it commits the move
   outside a batch it can hand to `Stage`): report it with its file and line, do not stage in a second batch.
4. Any subscriber receives an event its rights do not allow.

## Steps (one PR; every commit builds)

1. Write the tests below first.
2. Add the F6 members to `core/module/` (06 §6.4.10 F6 row: `Manifest.OutboxConsumers`, `Registrar.OutboxHandler`,
   `Deps.Outbox`); wire the relay, the hub (P5), `eventhttp`, F6-04's `outboxhttp` (its `Register` takes the outbox's
   `Operator`, the `events.Catalog` and the `audit.Logger`) and F6-05's tail topic in `core/kernel/boot.go` and
   `kernel.go`; regenerate `core/openapi/core.json` and the generated client. Un-skip the permission, guard and 401
   cells of `outboxhttp` (`core/internal/outboxhttp/routes_test.go`: delete each `t.Skip("needs stage 13: F4-08")`),
   which run now that F4-08's stage 13 is live and the package is mounted.
3. Books' topic declarations in `books.Module()`'s `Register`; move every hub A publisher to `events.Publish`; book-change
   coalescing via `RealtimeMeta.Coalesce`.
4. The durable relocation topic and its `ConsumeIntoTask(…, Live())` consumer; declare the consumer in
   `Manifest.OutboxConsumers`; stage the topic at every producer of the re-grep, in the batch that moves the book.
5. Delete hub A, `internal/realtime`, `/api/events` (route, gzip exclusion, `SkipPaths`), and re-point
   `internal/itunes/service`.
6. Rewrite `eventSourceManager.ts`; switch `App.tsx`, `TopBar.tsx`, `Library.tsx`, the Activity page and the system-log
   view (onto `core.log.recorded.v1`) to `useTopic` / `useResync`; update their tests.
7. Layering and shard weights; headers; one `changelog.d/` fragment; Acceptance.

## Tests first

- The stream tests now run on ABO's real topics: `TestHub_TaskTopicVisibleWithoutTasksView` and
  `TestRunRedaction_SameOnStreamAndREST` on `core.task.run-changed.v1` (deferred by F6-02);
  `TestAnyAuthenticated_ModuleOnlyKeyReachesCoreEvents` (05-rbac §12: a key scoped only `books:scan` passes
  `AnyAuthenticated` on `GET /api/core/v1/events`).
- `TestRelocationFollowUpDurable` (name coined here): moving a book stages `books.book.relocated.v1` in the move's batch;
  a crash after the commit and before delivery still starts exactly one `books.deluge.path-update` run after restart.
- `TestAPIEventsGone` (name coined here): `/api/events` answers the legacy mount's not-found and no handler serves it.
- vitest for the rewritten manager (`eventSourceManager.test.ts`): refusals surface with status and `Retry-After`;
  `Last-Event-ID` on every reconnect; backoff 3 s doubling to 60 s with jitter, never sooner than `Retry-After`, no give-up;
  `opaqueredirect` raises `ApiAuthRedirectError`; reset triggers the resync handlers; closed reconnects; the stream closes
  when the tab is hidden.
- The event half of `TestJournal_RequiresSystemManage` (F6-05's) and F6-04's route tests now run on ABO's mounted
  routes and topics, the un-skipped cells of `TestOutboxHTTP_ListDeadNeedsSystemManage`,
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
  without polling.

## Invariants

- Every intermediate `main` boots and passes e2e; the PR is atomic.
- One publisher per event on `main` (05-events §5E.13 "Order").
- No old stream URL is redirected or kept (D10).

## Acceptance

```bash
go build ./... && go vet ./...                                                         # exit 0
go test -count=1 ./internal/server/... ./internal/books/... ./internal/itunes/...       # ok
ls -d internal/realtime 2>/dev/null | wc -l                                            # 0
grep -rn '/api/events' internal web/src scripts .claude/skills | grep -v '/api/core/v1/events' | wc -l   # 0
grep -rn 'new EventSource(' web/src | grep -v test | wc -l                             # 0 (fetch-based reader)
(cd web && npm run build && npm test -- --run)                                         # exit 0
(cd core && go test -race -count=1 ./realtime/... ./outbox/... ./internal/outboxhttp/...)   # ok
grep -c 'needs stage 13: F4-08' core/internal/outboxhttp/routes_test.go                 # 0 (un-skipped)
go test -count=1 -run 'TestDisabledModuleOutboxRetained|TestDepsScoping|TestCoreRoutesReplaceGin' ./... ; (cd core && go test -count=1 -run 'TestDisabledModuleOutboxRetained|TestDepsScoping' ./...)   # ok
make -C core ci                                                                        # exit 0
make ci                                                                                # exit 0
make test-e2e                                                                          # exit 0
```

## Out of scope

`@mediacore/shell`'s client and tab leader election (F9, or R3); webhook notifications on `notify` (F7); the plugin
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

