<!-- file: docs/plans/platform-core/f6-03-flip-review-tables.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1acfe8d5-8c99-4961-bf54-6f825f46e637 -->
<!-- last-edited: 2026-11-28 -->

# F6-03 events flip (PR #5085): review tables

The tables and lists below were in the body of PR #5085, which was within about 800 characters of GitHub's 65,536-character limit. They are moved here unchanged, so the body can carry the option (b) section (the organize copy's switch-off Deluge fallback). Nothing was trimmed; every section is verbatim from the body as it stood at head `8381a6445`, and each heading below names the body section it came from. Line numbers, SHAs and "this branch" are as of that head (r7).

## Auth-off parity of the log tail and the outbox routes (finding 25)

With auth off, the log tail and the five non-purge outbox routes are admitted. That is parity with HEAD, shown from HEAD's files. Line numbers are this branch's (`internal/server/legacy_manifest.go` is one line higher on the base, above the deleted `/api/events` row).

| core surface | nearest HEAD route | `internal/server/legacy_manifest.go` | HEAD requirement |
|---|---|---|---|
| live log tail, topic `core.log.recorded.v1` | `GET /api/v1/system/logs` | line 853 | `settings.manage` |
| `GET /api/core/v1/events/topics` | `GET /api/v1/system/status` | line 854 | `settings.manage` |
| `GET /api/core/v1/outbox/consumers` | `GET /api/v1/system/status` | line 854 | `settings.manage` |
| `GET /api/core/v1/outbox/dead` | `GET /api/v1/operations/stale` | line 788 | `library.view` (weaker than this PR's requirement, so it is a tightening) |
| `POST /api/core/v1/outbox/dead/{consumer}/{seq}/retry` | `POST /api/v1/operations/v2/:id/retry` | line 797 | `settings.manage` |
| `DELETE /api/core/v1/outbox/dead/{consumer}/{seq}` | `DELETE /api/v1/operations/v2/:id/record` | line 796 | `settings.manage` |
| `DELETE /api/core/v1/outbox/consumers/{consumer}/backlog` (purge) | `POST /api/v1/operations/clear-stale` | line 774 | `settings.manage`; still refused with auth off by `Guards.Interactive` (`interactive_required`) |

What HEAD does with auth off, from HEAD's code and not the spec:

- `(*Server).perm` (`internal/server/server_lifecycle.go:2720`) wraps every `settings.manage` route above. Without core identity it is a pass-through when `config.AppConfig.EnableAuth` is false (`server_lifecycle.go:2731`). Under core identity it is `s.identity.Bridge.RequirePerm(atom)`.
- `Bridge.RequirePerm` (`internal/server/legacyauth/gin.go:99`, doc at 93 to 98): "the auth-disabled principal is judged by its own checker, under which every s.perm route passes except one whose requirement is all AuthRequired". `holds` (`gin.go:78`) refuses it with `forbidden(msgAuthOffRefused)` only when `rbac.RequireRequirement` fails.
- The `settings.manage` atom row (`internal/books/atoms.go:73` to `75`) is `core:settings.manage, books:settings.manage` and then the six other core scopes. `core:system.manage` is the only AuthRequired member, so the auth-off checker (which drops AuthRequired members of an AllOf) admits the auth-off principal on every one of those routes today.
- `rbac.OperatorRequirement()` (`core/rbac/operator.go`) is the same seven core scopes as one AllOf. With auth off it is judged the same way, so the tail and the five routes are admitted exactly as the HEAD routes in the table are. `TestJournalTail_AuthOffReachIsTheOperatorRequirements` and the rows in `core/kernel/identity_authoff_coreroutes_table_test.go` pin it.

## G3 cells to change when this is re-cut onto main (#5074, G3-r2)

#5074 lands before this PR, so `core/internal/outboxhttp/routes_test.go` as #5074 leaves it (`6d9cfc8d6`) conflicts with this PR's version of the same file. Nothing is edited now. At the re-cut, resolve to this PR's version of the cells below; they are the ones in #5074's file that still say the old requirement:

1. `wantRoutes` (six rows, lines 310 to 315): the `perm` column of every row, `core:system.manage` for the dead-letter, retry, discard and purge rows and `core:system.view` for consumers and topics, becomes the operator AllOf string `all(core:settings.manage,core:tasks.manage,core:system.view,core:system.manage,core:library.manage,core:library.browse,core:integrations.manage)`. The `interactive` column is unchanged (purge only).
2. Callers (`viewer`, `manager`, `managerKey`, `managerABS`, `managerOff`): `manager`, `managerKey`, `managerABS` and `managerOff` hold `permManage` alone and must hold all seven scopes (`operatorScopes`); `viewer` stays `core:system.view` alone. Add two refused callers: `core:system.manage` alone, and six of the seven operator scopes.
3. `TestOutboxHTTP_DeadLetterRoutesNeedSystemManage` (list, retry, discard): `{viewer, lacking(permManage)}` becomes `{viewer, lacking("core:settings.manage")}`, because a 403 names the first member of the AllOf the caller does not satisfy (`core/httpapi/stage13.go` `missingScope`), and that is `core:settings.manage` for a caller holding only `core:system.view`. `{manager, letThrough}` needs `manager` to hold the seven scopes (item 2). Add the two refused callers.
4. `TestOutboxHTTP_PurgeNeedsInteractive`: `{viewer, lacking(permManage)}` becomes `{viewer, lacking("core:settings.manage")}`; the `managerKey` (`interactiveOnly`), `managerABS` (`letThrough`), `managerOff` (`interactiveOnly`) and `manager` (`letThrough`) cells keep their answers but their callers hold the seven scopes (item 2). Add the two refused callers.
5. `TestOutboxHTTP_ConsumersAndTopicsNeedSystemView` is renamed `...NeedTheOperatorRequirement` and its cells change in meaning: `{nothing, lacking(permView)}` becomes `lacking("core:settings.manage")`; `{viewer, letThrough}` becomes a refusal (`lacking("core:settings.manage")`), because the reads are no longer open to `core:system.view`; add `{manager, letThrough}` and the two refused callers.
6. Unchanged: the audit-row assertions that read `core:system.manage` or `core:system.view` (the retry audit's `r.Permission`, and the catalog topic permissions near lines 1159 and 2112), because they are the handler's own recorded permission and the topics' own, not the route requirement; and the `managerOff` purge cell.

## Mutants carried from the earlier revisions (r2, r3)

Every behaviour change has mutants; 76 killed of 76 that build, plus one (B15) the type system rejects at build.

| Group | Killed / total | Killing tests |
|---|---|---|
| view rule (`tasks.WithViewRule`, `viewRuleVisible`) | 8 / 8 | `TestViewRuleVisible`, `TestWithViewRule`, `TestHub_TaskTopicVisibleWithoutTasksView`, `TestEventsFlip_TaskTopicsFollowTheDefinitionsViewRule` |
| kernel wiring K1-K15 (outbox binding, owner claim, hub subscriptions, tail predicate, executor binding, nudger start/stop, OnShutdown hooks, relay registration, disabled-module consumers, handler registration, freeze, view rule) | 15 / 15 | `TestEventsFlip_*` (P5BuildsOutboxAndHubOnlyForTheIdentityBoot, SwitchOffBuildsNothing, HubSubscriptionsAreTakenAtP7, TailIsDeclaredWithTheModulePredicate, StagedMessageStartsTheConsumersTask, OutboxFrozenAndNudgerFollowTheExecutor, RunHandsTheListenerTheHubClose, RelayIsRegisteredAndTheExecutorBound, ExecutorPublishesTaskTopics, UnlistedConsumerFailsBoot), `TestDepsScoping`, `TestDisabledModuleOutboxRetained` |
| m1 in-run retry and resume | 9 / 9 | `TestPathUpdateRetry`, `TestIsTransient`, `TestDelugeCores_PathUpdateParity`, `TestDelugeKinds_MatchHEADDefs` |
| store relocation (R1, R3, R4: stage in the copy's batch, only from copy-organize, refusal refuses the copy) | 3 / 3 | `TestBookRelocated_*` (CopyOrganizeStagesOneMessage, EveryOtherPathWriterStagesNothing, FailedCommitStagesNothingAndCreatesNoCopy, StageRefusalRefusesTheCopy) |
| consumer C1-C5 (target path read at run time, missing target no-op, permanent on empty id, dedupe key includes the target) | 5 / 5 | `TestPathUpdateTarget_MovesToTheTargetsCurrentStoredPath`, `TestPathUpdateTarget_MissingTargetIsANoOp`, `TestRelocationRunParams`, `TestPathUpdate_DedupeKeyIsTheBook` |
| books topics, module and organizer B1-B16 | 15 / 16 killed, B15 build-rejected | `TestBookChangedTopicSpec`, `TestPublishBookChanges`, `TestRegisteredHarness_EventStreamWriterChain`, `TestRegisteredHarness_RelocationIsStagedByTheBootedModule`, `TestNewOrganizeService_WiresTheRelocatingCopyCreator`, `TestCreateOrganizedVersion_OnlyTheFollowTorrentsVariantStagesTheRelocation`, `TestCreateOrganizedVersionFollowTorrents_RefusesWithoutACreator` |
| container wiring and boot check (creator dropped by `ServiceDefs`; `RequireRelocatingCopyCreator` made a no-op) | 2 / 2 | `TestServiceDefs_OrganizeCopyNeedsTheCreatorTheStoreHas`, `TestRequireRelocatingCopyCreator` |
| SPA W1-W19 (`coreEvents`, bookChanges batching, connection and shutdown handling) | 19 / 19 | `coreEvents/*.test.ts`, `bookChanges.test.ts` (W2, W3 and W11 were first survivors and are now killed by the constants/topic-name and empty-id tests) |

## B1 mutants: the outbox consumer's start

From the body's section "B1: the outbox consumer's start (`Perms.OutboxSystemStart`)".

| B1 mutant | FAIL line |
|---|---|
| `StartIn` takes the flag for any trigger | `--- FAIL: TestOutboxSystemStart_OnlyTheOutboxPathTakesIt` |
| the plain system start path takes the flag | `--- FAIL: TestOutboxSystemStart_OnlyTheOutboxPathTakesIt` |
| flag unset still grants | `--- FAIL: TestOutboxSystemStart_UnflaggedKindIsRefused`, `--- FAIL: TestCheckOutboxStart` |
| another module's actor takes the flag | `--- FAIL: TestOutboxSystemStart_OtherModulesActorIsRefused`, `--- FAIL: TestCheckOutboxStart` |
| the P7 check is not called | `--- FAIL: TestEventsFlip_OutboxKindTheOwnerCannotStartFailsBoot`, `--- FAIL: TestEventsFlip_OutboxKindNobodyRegisteredFailsBoot` |
| an unregistered kind passes the check | `--- FAIL: TestEventsFlip_OutboxKindNobodyRegisteredFailsBoot`, `--- FAIL: TestCheckOutboxStart` |
| the flag is ignored (relocation start refused) | `--- FAIL: TestEventsFlip_StagedMessageStartsTheConsumersTask`, `--- FAIL: TestEventsFlip_OutboxFrozenAndNudgerFollowTheExecutor`, `--- FAIL: TestCheckOutboxStart` |
| the consumer starts Preview, not Live | `--- FAIL: TestRelocationFollowUpDurable` |
| path-update drops the `OutboxSystemStart` flag | `--- FAIL: TestRelocationFollowUpDurable` |

## Review fixes in this revision (SF1 to SF8)

From the body's section "Review fixes in this revision". The `--- FAIL` lines are quoted from runs on the r5 stack.

| Item | Change | Test | Mutant and its FAIL line |
|---|---|---|---|
| SF1 listener race | the test's `Listen` callback and its poll share `got` under a mutex | `TestEventsFlip_RunHandsTheListenerTheHubClose` (`-race -count=3`) | callback writes without the lock: `--- FAIL: TestEventsFlip_RunHandsTheListenerTheHubClose (0.68s)` |
| SF2 HTTP/3 shutdown | `R1Listener.Shutdown` runs the shutdown hooks (the hub's `closed{shutdown}` frame) before HTTP/3 closes, and HTTP/3 drains with `Shutdown(ctx)` beside the main server | `TestR1Listener_Shutdown_AnHTTP3ClientReceivesTheShutdownFrame` | HTTP/3 closed hard before the hooks, as the base did: `--- FAIL: TestR1Listener_Shutdown_AnHTTP3ClientReceivesTheShutdownFrame (10.01s)`. Two weaker mutants SURVIVE: dropping the explicit hook call (the same `sync.OnceFunc` is also registered with `httpServer.RegisterOnShutdown`, so `httpServer.Shutdown` runs it a moment later; equivalent for this test) and `Close()` in place of `Shutdown(ctx)` for the HTTP/3 drain (the test pins the ordering, not the drain mode: a request in flight on HTTP/3 during shutdown is not covered) |
| SF3 several Active torrents | with a target (the relocation case) every Active torrent of the original goes to the target's directory, as the base sent every one to `Dir(newPath)`; the `.versions/<id>` subdirectory stays only for a book organized in place with several Active versions and no target | `TestPathUpdateTarget_SeveralActiveTorrentsAllGoToTheTargetsDirectory` | version dir joined onto the target: `--- FAIL: TestPathUpdateTarget_SeveralActiveTorrentsAllGoToTheTargetsDirectory (0.00s)` |
| SF4 transient classifier | a DNS failure is judged before the dial `*net.OpError` that wraps it (a real NXDOMAIN is permanent, a DNS timeout transient) | `TestIsTransient` (new cells: undecodable 4xx body, DNS timeout from a failed dial, NXDOMAIN from a failed dial, plain errors) | 5xx to 4xx: `--- FAIL: TestIsTransient/an_undecodable_4xx_body_is_permanent`. Drop `dns.IsTimeout`: `--- FAIL: TestIsTransient/a_DNS_timeout_from_a_failed_dial_is_transient`. Drop `io.EOF`: `--- FAIL: TestIsTransient/plain_errors`. OpError judged before DNS: `--- FAIL: TestIsTransient/an_NXDOMAIN_from_a_failed_dial_is_permanent` |
| minor: `MoveStorage` ctx | `deluge.Client.MoveStorageContext` carries the run's context through login and `core.move_storage`; path-update passes the run's ctx (the 5-minute client timeout no longer outlives the kind's 1-minute one) | `TestMoveStorageContext_EndsWithTheCallersDeadline`, `TestPathUpdateRetry_TheRunsContextReachesMoveStorage` | `Background()` to MoveStorage: `--- FAIL: TestPathUpdateRetry_TheRunsContextReachesMoveStorage (5.00s)`. Move drops ctx: `--- FAIL: TestMoveStorageContext_EndsWithTheCallersDeadline/a_hung_core.move_storage (5.00s)`. Login drops ctx: `--- FAIL: TestMoveStorageContext_EndsWithTheCallersDeadline/a_hung_auth.login (5.00s)` |
| SF6 CF Access | a session-less `ext:cfaccess`-shaped principal (extension method, no session) survives the hub heartbeat while its token does, ends at the token's own end, and ends when its user is disabled; the fixture's clock is now movable | `TestHub_CFAccessPrincipalSurvivesTheHeartbeat` (3 subtests) | `stands` refuses every extension principal: `--- FAIL: TestHub_CFAccessPrincipalSurvivesTheHeartbeat/stands_while_the_token_does`. `stands` ignores the token's end: `--- FAIL: TestHub_CFAccessPrincipalSurvivesTheHeartbeat/ends_at_the_token's_own_end (5.00s)`. Heartbeat skips `Revalidate`: SURVIVES, equivalent (the hub's `Check` calls `rightsChanged`, i.e. `Refresh`, before `revalidate` on every beat and ends the stream for the same refusals) |
| SF7 re-drive | documented only (above): no code change | n/a | n/a |
| SF8 rehearsal stages | answered below | n/a | n/a |
