<!-- file: docs/agent-tasks/platform-core/F6-01-core-outbox-relay-dark.md -->
<!-- version: 1.1.2 -->
<!-- guid: db05b494-8e19-4d17-b633-9fa4ab8af310 -->
<!-- last-edited: 2026-11-05 -->

# F6-01 — Core outbox, dark: same-batch staging, the `core.outbox.relay` kind, `ConsumeIntoTask`, `OutboxConsumers` and `OutboxHandler`

| Field | Value |
|---|---|
| ID | F6-01 |
| Release / flip | R2, events flip F6 (dark PR; live at F6-03) |
| Tier | **T4** (Opus, xhigh–max): durable at-least-once delivery committed in the producer's batch |
| Wave | W30 (beside F2-16, F4-05, F5-15, F6-02, F7-02, F8-01, F8-03; file-disjoint) |
| Depends on | F4-04, F2-05 |
| Owns gate tests | the outbox stream tests of 05-events: `TestOutbox_StageCommitsWithBatch`, `TestOutbox_RedeliveryAfterCrashBeforeAck` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f6-01-outbox
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-04 and F2-05 are merged. If not, stop.

## Goal

Create `github.com/falkcorp/mediacore/outbox` and its native store `core/internal/outboxstore` over `c/outbox/` exactly
as 05-events §5E.4 and §5E.5.2–§5E.5.3 specify: `Topic[T]` (a catalog entry with `Durable`), `Writer` and `Stage` into
the caller's batch, commit-ordered intake by a single relay writer, per-topic sequencing with `GetForUpdate` on the
`Serialized` high-water mark, partitions and per-key order, consumers (`ConsumeIntoTask[Raw]` for modules, always a task
run; `Consume`/`ConsumeTx`/`ConsumeRaw` for core), retries, dead letters, retention by the declared-consumer set, and the
relay as the core task kind `core.outbox.relay`. Wire it in `core/kernel/outbox.go`, **dark for ABO** (ABO declares no
durable topic or consumer until F6-03; ABO's executor starts only at F5-18).

## Spec

- 05-events §5E.4 (copy the package block: `Topic`, `Spec`, `Outbox`, `Writer`, `Stage`, `StageOption`, `NotBefore`,
  `Message`, `RawMessage`, `Handler`, `ConsumeIntoTask`, `ConsumeIntoTaskRaw`, `HandlerFunc`, `Tx` with `Batch` and
  `StartRun`, `TxHandlerFunc`, `Consume`, `ConsumeTx`, `ConsumeRaw`, `ConsumerOption`s `Unordered`, `MaxAttempts`,
  `Backoff`, `FromBeginning`, `Live`, `Preview`, `Where`, `WhereRaw`, and the four errors), "Why module consumers are
  tasks", "No storage hook is required".
- 05-events §5E.5.2 (the `c/outbox/` key table and every property row), §5E.5.3 (the relay definition table), §5E.6
  (lifecycle), §5E.7, §5E.8, §5E.9 (`core.outbox.*` keys), §5E.10 (outbox metrics), §5E.11 (purge, audited), §5E.12
  (outbox tests), §5E.13 "Order" item (3).
- 05-tasks §5T.3.12 (`Executor.StartIn`, outbox-triggered runs), §5T.3.10 and §5T.7 (`Executor.WriteRuns`,
  `ErrWriteRunsNested`), §5T.3.8 (`Every` is reserved for this kind; the scheduled-writer rule).
- 05-storage §5S.3.2 (participant binding, `GetForUpdate`, `Serialized`).
- 06 §6.4.10 F6 row (`Manifest.OutboxConsumers`, `Registrar.OutboxHandler`, `Deps.Outbox`).

## Files

| Action | Path |
|---|---|
| create | `core/outbox/` |
| create | `core/internal/outboxstore/` |
| create | `core/kernel/outbox.go` (the `c/outbox/` spec, the relay definition, the static core consumer list check, retention sweep, the nudge) |

## Re-verify at BASE

```bash
ls -d core/outbox core/internal/outboxstore 2>/dev/null | wc -l        # 0
grep -rn 'type TopicSpec struct' core/events/ | wc -l                 # 1 (F4-04)
grep -rn 'Durable ' core/events/*.go | wc -l                          # >= 1
grep -rn 'func (e \*Executor) StartIn\|StartIn(' core/internal/executor/ | wc -l    # >= 1 (F2-03)
grep -rn 'WriteRuns' core/internal/executor/ | wc -l                  # >= 1
grep -rn 'func Every\|func RunItems\|func Fixed' core/tasks/ | wc -l   # >= 3 (F2-01)
grep -rn '"core.outbox' core/config/ core/kernel/core_sections.go | wc -l   # >= 1 (F2-09 declared the section)
grep -rn 'GetForUpdate' core/storage/*.go | wc -l                     # >= 1 (R1-20)
```

## STOP conditions

1. `Executor.StartIn` or `WriteRuns` is missing from F2-03's executor (outbox-triggered runs depend on them).
2. A module consumer could run its handler inside the relay (module consumers are always task runs, 05-events §5E.4
   "Why module consumers are tasks"): `archtest.OutboxModuleConsumersAreTasks` must flag it.
3. Anything is staged, sequenced or delivered in ABO's boot before F6-03.
4. A change to `core/module/` seems needed: `Manifest.OutboxConsumers`, `Registrar.OutboxHandler` and `Deps.Outbox`
   (06 §6.4.10 F6 row) are added by the flip F6-03, which owns `core/module/` in its wave. Build the outbox so the kernel
   can read the consumer set through a function F6-03 will call; do not edit `core/module/` here.

## Steps

1. Write the tests below first.
2. `outboxstore`: the key table of §5E.5.2 (`in/`, `m/<topic>/<pp>/<seq20>`, `seq/<topic>` `Serialized`, `cur/`, `try/`,
   `dead/`).
3. `Stage`: binds the outbox keyspace to the caller's batch and writes only `c/outbox/in/<topic>/<ulid>`; one monotonic
   ULID generator per `Outbox` (strictly increasing within the process, also within one millisecond and across a
   backward clock step); only the topic's owner may stage; nudges the relay.
4. Relay intake: single writer of `m/` and `seq/`; one batch per topic chunk deletes the intake rows, writes consecutive
   seqs, advances `seq/<topic>` read with `GetForUpdate`, through the relay run's fenced writer; a zombie relay fails its
   commit.
5. Delivery: `partition = fnv1a32(key) % partitions` (default 16); per (consumer, partition) one at a time in seq order
   with head-of-line blocking, or up to `core.outbox.unordered_parallelism` with `Unordered()`; at-least-once with
   `IdempotencyKey` stable across redelivery; `ConsumeTx` and `ConsumeIntoTask[Raw]` commit effect (or the run's queue
   row) and cursor in one batch through `Executor.WriteRuns` under `storage.RetryOnConflict`; backoff with full jitter;
   dead letter on `ErrPermanent`, `ErrSchema` or `MaxAttempts`, health `core.outbox` Degraded while any exists; stale
   messages acked and counted, never returned as errors; `Where`/`WhereRaw` ack unmatched without running.
6. Relay kind `core.outbox.relay` exactly per §5E.5.3's table (Task shape; `Writes(Res("core","outbox"))`;
   `ResumeFromZero`; `ConcurrencyKey` `core.outbox.relay`; `PrioritySystem`; `Every(time.Minute).OnStart().Live()`;
   `Timeout` = `core.outbox.relay_rotate` + 5 min; progress at every intake scan and delivery; nudge start `ModeLive`
   rate-limited to 1/s; per-consumer delivery with `tasks.RunItems` over (consumer, partition) pairs and
   `tasks.Fixed(min(partitions, core.outbox.relay_workers), "outbox relay workers")`); `core.outbox.relay_enabled=false`
   makes each run exit at once with Degraded `relay_disabled`.
7. Retention: delete a message when every consumer in its topic's declared-consumer set (all manifests' consumers,
   enabled and disabled, plus the kernel's static core consumer list) has a cursor past it; empty set → keep for
   `orphan_retention`; cursors of consumers in neither set dropped after `orphan_retention` with a metric. New consumer
   starts at "now" unless `FromBeginning()`. A core consumer not on the static list fails boot. The static list in
   `core/kernel/outbox.go` names `core.notify.deliver` from this PR (05-events §5E.5.2 Retention names it; it registers
   with F7-05's notify, and a declared consumer that has never run holds nothing); `core.acquire.download-events` is
   deferred with `acquire` (plan §(j)).
8. Kernel wiring (dark), metrics (§5E.10), headers, one `changelog.d/` fragment, Acceptance. The metrics add the core
   `outbox` scope: add its `testdata/series.golden`, asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5
   item 5).

## Tests first (05-events §5E.12, outbox rows; the gate pair first)

`TestOutbox_StageCommitsWithBatch`, `TestOutbox_RedeliveryAfterCrashBeforeAck` (crash injection), then
`TestOutbox_DiscardLeavesNothing`, `TestOutbox_IntakeOutOfOrderCommitsNoSkip`, `TestStageOrderWithinBatch`,
`TestOutbox_LeakedBatchDoesNotStall`, `TestOutbox_IntakeCrashNoReissue` (`CrashClone` at every point of the intake
batch), `TestOutbox_ZombieRelayCannotDoubleSequence`, `TestOutbox_TxHandlerEffectivelyOnce`,
`TestOutbox_PerKeyOrderAndHeadOfLine`, `TestOutbox_UnorderedNoHOL`, `TestOutbox_DeadLetterAfterMaxAttempts` (synctest),
`TestOutbox_RetryDeadLetter`, `TestOutbox_RawSchemaMismatchDeadLetters`, `TestOutbox_IdempotencyKeyStableAcrossRedelivery`,
`TestOutbox_DuplicateConsumerNameRefused`, `TestOutbox_ConsumeIntoTaskStartsOnce`, `TestOutbox_ConsumeIntoTaskRawWithoutImport`,
`TestArchtest_ModuleConsumeFlagged`, `TestOutbox_DisabledConsumerRetainsUntilReenableOrPurge`,
`TestCoreOnlyTopicRetainedUntilAck`, `TestOutbox_WhereAcksUnmatchedWithoutRun`, `TestOutbox_WhereWrongTypeRefused`,
`TestOutbox_TxStartRunSameBatchAsTransition`, `TestOutbox_ConsumeIntoTaskRequiresMode`,
`TestRelay_DefinitionRegistersAndDeliversLive`, `TestOutbox_RelayRestartsAfterCancel`,
`TestOutbox_RelayDisabledDegradedNoLoss`, `TestNoGlobals_EventsOutbox` (the outbox half). Run them on hello and coretest.

The outbox-triggered task rows of 05-tasks §5T.9 (they prove `Tx.StartRun` through `Executor.StartIn`):

- `TestOutboxTriggered_StartInSameBatchAsCursor`: a crash between the run's queue row and the consumer's cursor cannot
  happen, because both commit in one batch.
- `TestOutboxTriggered_ModeFromConsumerOption`: a `ConsumeIntoTask` without `Live()` or `Preview()` fails
  registration, and an unset `Tx.StartRun` mode is refused.
- `TestOutboxTriggered_WritesNoIdemRecord`: an outbox-triggered start writes no idempotency record (the cursor is the
  dedup).

`metricstest.AssertSeries` over the `outbox` scope's `testdata/series.golden` (05-metrics §12).

## Invariants

- A staged message exists iff its producer's batch committed.
- No seq is issued twice; no cursor skips a committed message; no topic stalls another.
- Module consumers never run inside the relay.
- No order across topics; per-key order within a topic.

## Acceptance

```bash
(cd core && go vet ./outbox/... ./internal/outboxstore/... ./kernel/...)                 # exit 0
(cd core && go test -race -count=1 ./outbox/... ./internal/outboxstore/... ./kernel/...) # ok
(cd core && go test -count=1 -run 'TestOutbox_StageCommitsWithBatch|TestOutbox_RedeliveryAfterCrashBeforeAck' ./...)  # PASS
make -C core ci                                                                          # exit 0
make ci                                                                                  # exit 0
```

## Out of scope

Books' durable topic `books.book.relocated.v1` and its consumer into `books.deluge.path-update` (F6-03); the module
members `Manifest.OutboxConsumers`, `Registrar.OutboxHandler`, `Deps.Outbox` and the kernel boot calls (F6-03); notify's
`core.notify.deliver` consumer (F7-05); the operator routes of 05-events §5E.11 (dead-letter list, retry and discard,
the backlog purge), which F6-04 builds in `core/internal/outboxhttp/` over this row's `Outbox`. Expose the store
operations those routes need (list dead letters, retry, discard, advance a consumer's cursors to the head) as methods
F6-04 can call.

## Rollback

Revert the PR; nothing in ABO stages or consumes.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`. CLAUDE.md concurrency: the relay's per-consumer delivery is a bounded `tasks.RunItems` with an explicit
`tasks.Fixed` parallelism.

```
feat(core/outbox): same-batch staging, commit-ordered intake, the relay task, consumers (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

