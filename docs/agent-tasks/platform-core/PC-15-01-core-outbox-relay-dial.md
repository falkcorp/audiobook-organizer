<!-- file: docs/agent-tasks/platform-core/PC-15-01-core-outbox-relay-dial.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8091f031-ca28-4506-8661-9e553943b0aa -->
<!-- last-edited: 2026-11-04 -->

# PC-15-01 — core/outbox (c/outbox/, same-batch, at-least-once) with the relay dial

| Field | Value |
|---|---|
| ID | PC-15-01 |
| Tier | **T3 Opus/high** — durable delivery semantics and the first `c/outbox/` writer: a sequencing bug numbers a message twice or skips one, silently |
| Wave | W15 (S6, release `S6`; a **live**, state-writing PR before the flip) |
| Depends on | OWNER-18 (the `floor` soak row; S6's predecessor is always `floor`) |
| Serialized | **PC-15-01** → PC-15-02 → PC-15-03 → PC-15-04 |
| Trailer | `Stage-Release: S6 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §1, §3 (`outbox` row), §4 "05-events 5E.4", §5 rules 1 and 6, §7 S6 |
| Owning spec | 05-events §5E.4 (API), §5E.5.2 (storage and delivery), §5E.5.3 (the relay task), §5E.6 (lifecycle, boot writers), §5E.7, §5E.8, §5E.9 (config), §5E.10 (metrics), §5E.12 (tests); 05-tasks §5T.3.10 (`StartIn`, `WriteRuns`), §5T.3.12; 06 §6.6 S6 row (`Manifest.OutboxConsumers`, `Registrar.OutboxHandler`, `Deps.Outbox`); 09 S6 "Data/keys", "Switches" |

## Goal

Land `core/outbox`: `Stage` writes an intake row into the caller's batch (commit = durable, discard = nothing); the
relay, a core task `core.outbox.relay`, is the single writer that sequences intake into per-topic, per-partition
messages and delivers them at least once to declared consumers; the operator dial `core.outbox.relay_enabled` stops
delivery without losing anything. No ABO code stages anything in S6 (webhooks move at S7), so ABO's `c/outbox/` stays
empty apart from what the relay itself writes; this PR ships live and must be safe alone.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-15-01" -b feat/pc-15-01-core-outbox origin/main
cd "$REPO/../audiobook-organizer-pc-15-01"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create: `core/outbox/` (package `github.com/falkcorp/mediacore/outbox`: `topic.go`, `stage.go`, `consume.go`,
`relay.go`, `retention.go`, `metrics.go`, `errors.go`), `core/outbox/outboxtest/` (the conformance suite; its exported
entry point name is coined here, `outboxtest.Suite`).

Modify: `core/kernel/` (P5 build `Outbox`; P6/P7 accept `Registrar.OutboxHandler`, check every module consumer name
against `Manifest.OutboxConsumers` and every core consumer name against the static core consumer list
`kernel/catalog`; register `core.outbox.relay`; hand `Deps.Outbox`), `core/coretest/crashmatrix/` (the relay's boot
writes).

Also modify (not in the inventory row; see Planner notes): `core/module/` (the 06 §6.6 S6 fields), and ABO's
`internal/books/crashmatrix_abo_test.go` (`TestBootCrashMatrix_ABO` extended for the `c/outbox/` boot writer).

## Re-verify greps (all core paths exist from earlier waves; absent at the plan checkout)

```bash
test -d core/events && grep -rn "type TopicSpec struct" core/events --include=*.go | wc -l          # PC-10-01; expect 1
grep -rn "Durable" core/events --include=*.go | grep -v _test | wc -l                                # expect >= 1 (TopicSpec.Durable exists)
grep -rn "func (.*) StartIn(\|func (.*) WriteRuns(" core/internal/executor --include=*.go | wc -l    # PC-12-02; expect >= 2
grep -rn "func Every(" core/tasks --include=*.go | wc -l                                              # PC-12-01; expect 1
grep -rn "TestBootCrashMatrix\b" core --include=*_test.go | wc -l                                     # PC-07-03; expect >= 1
grep -n "^release: floor$" ci/stage.txt                                                               # expect 1
test ! -e core/outbox && echo ok                                                                     # expect ok
grep -rn '"c/outbox/' core internal --include=*.go | wc -l                                            # expect 0
```

## Storage (exact, 05-events §5E.5.2)

| Key | Value |
|---|---|
| `c/outbox/in/<topic>/<ulid>` | staged message awaiting intake; written only by `Stage`, inside the producer's batch |
| `c/outbox/m/<topic>/<pp>/<seq20>` | sequenced message (`pp` 2-digit partition; `seq20` zero-padded) |
| `c/outbox/seq/<topic>` | the topic's high-water mark; written only by the relay, in the intake batch; in a `Serialized` sub-prefix, read with `Batch.GetForUpdate` |
| `c/outbox/cur/<consumer>/<topic>/<pp>` | highest acked seq |
| `c/outbox/try/<consumer>/<topic>/<pp>` | `{seq, attempt, next_at, last_error_token}` for the head message |
| `c/outbox/dead/<consumer>/<topic>/<seq20>` | dead letter + error token + attempts |

## Invariants

1. **Same batch.** `Stage` binds the outbox keyspace to the caller's batch and writes only `c/outbox/in/` inside it;
   only the topic's owner may stage it (`ErrNotOwner`); an undeclared topic → `ErrUnknownTopic`. Intake ULIDs come from
   **one** monotonic generator per `Outbox` (strictly increasing within a millisecond and across a backward clock step),
   so two messages staged in one batch are sequenced in `Stage` order.
2. **Single sequencer, no reissue.** Only the relay writes `m/` and `seq/`; one batch per topic chunk deletes the intake
   rows, writes consecutive seqs and advances `seq/<topic>` under its `GetForUpdate` precondition, through the relay
   run's fenced write path; a zombie relay's commit fails. A crash before commit leaves intake rows to sequence again.
3. **At-least-once.** A message is redelivered until its consumer acks (handler nil and cursor committed); `ConsumeTx`
   and `ConsumeIntoTask[Raw]` commit effect (or the run's queue row via `Executor.StartIn`) and cursor in one batch built
   inside `Executor.WriteRuns` (the relay never calls `WriteRuns` from a handler: `ErrWriteRunsNested`).
4. **Ordering.** `partition = fnv1a32(key) % partitions` (default 16); per (consumer, partition) one message at a time in
   seq order; a failing head blocks its partition (head-of-line) except for `Unordered()` consumers
   (`core.outbox.unordered_parallelism`, default 4). No order across topics.
5. **Retry and dead letters.** Full-jitter backoff between `Backoff(min,max)` (1 s, 5 m); `ErrPermanent`, `ErrSchema` or
   `MaxAttempts` (20) → dead letter, cursor advances, health `core.outbox` Degraded while any dead letter exists.
6. **Retention.** Delete a message only when every consumer in its topic's declared-consumer set (all manifests'
   `OutboxConsumers`, enabled **and** disabled, plus the static core list) has a cursor past it; an empty set keeps
   messages for `core.outbox.orphan_retention` (7 d); dead letters stay until an operator acts.
7. **The relay is a task** (05-events §5E.5.3 table, exact): kind `core.outbox.relay`, shape Task, `Effects =
   tasks.Writes(tasks.Res("core","outbox"))`, `Resume = ResumeFromZero`, `ConcurrencyKey core.outbox.relay`,
   `Priority tasks.PrioritySystem`, `Schedule tasks.Every(time.Minute).OnStart().Live()`, `Timeout = relay_rotate + 5 min`,
   progress on every intake scan (at least every `intake_poll`, 1 s) and every delivery; a run ends `succeeded` at
   `relay_rotate` (1 h), never `timed_out`; a `Stage` nudge with no relay running starts one with `ModeLive`
   (rate-limited to 1/s). Per-consumer delivery uses `tasks.RunItems` over (consumer, partition) pairs with
   `tasks.Fixed(min(partitions, core.outbox.relay_workers), "outbox relay workers")`; each core handler runs under
   `core.outbox.handler_timeout` (30 s).
8. **The dial.** `core.outbox.relay_enabled=false` (hot reload) makes each relay run exit at once: staging still
   commits, intake grows, `core.outbox` reports Degraded `relay_disabled`; turning it back on delivers everything.
9. **No globals** (`TestNoGlobals_EventsOutbox`): the nudge, generator and metrics are `Outbox` fields.
10. **Boot writers** (05-events §5E.6, A-8): every outbox write happens inside the relay run started from its schedule's
    `OnStart` at the scheduler step; extend `TestBootCrashMatrix` and `TestBootCrashMatrix_ABO` with crash points inside
    the intake batch and the cursor write of a resumed relay run.

## Steps

1. Tests first (below).
2. Implement `core/outbox` per 05-events §5E.4 exactly (types, functions, options, errors) and invariants 1–9.
3. Config keys (05-events §5E.9): `core.outbox.max_attempts` 20, `backoff_min`/`backoff_max` 1s/5m, `relay_workers` 4,
   `relay_rotate` 1h (restart), `relay_enabled` true (hot), `handler_timeout` 30s, `intake_poll` 1s,
   `unordered_parallelism` 4, `orphan_retention` 7d.
4. Metrics (05-events §5E.10) under the app's namespace (`<ns>_outbox_*`); ABO's golden diff adds only these families.
5. Kernel wiring (Modify list); `core.outbox` health component registered through the S5 readiness core.
6. Crash matrix (invariant 10). Acceptance, headers, `changelog.d/` fragment.

## Code contract (exact, 05-events §5E.4; digest S6-S7 §4 quotes it)

```go
func NewTopic[T any](name string) Topic[T]
func (t Topic[T]) Spec(s events.TopicSpec, partitions int) events.TopicSpec // 0 = 16 partitions
func Stage[T any](w Writer, b *storage.Batch, t Topic[T], key string, payload T, opts ...StageOption) error
func NotBefore(t time.Time) StageOption
func ConsumeIntoTask[T any](t Topic[T], consumer string, kind tasks.Kind, params func(Message[T]) (any, error), opts ...ConsumerOption) Handler
func ConsumeIntoTaskRaw(topic, consumer string, kind tasks.Kind, params func(RawMessage) (any, error), opts ...ConsumerOption) Handler
func Consume[T any](t Topic[T], consumer string, h HandlerFunc[T], opts ...ConsumerOption) Handler
func ConsumeTx[T any](t Topic[T], consumer string, h TxHandlerFunc[T], opts ...ConsumerOption) Handler
func ConsumeRaw(topic, consumer string, h func(ctx context.Context, m RawMessage) error, opts ...ConsumerOption) Handler
func (tx *Tx) Batch() *storage.Batch
func (tx *Tx) StartRun(kind tasks.Kind, req tasks.StartRequest) (tasks.RunRef, error)
func Unordered() ConsumerOption; func MaxAttempts(n int) ConsumerOption; func Backoff(min, max time.Duration) ConsumerOption
func FromBeginning() ConsumerOption; func Live() ConsumerOption; func Preview() ConsumerOption
func Where[T any](f func(Message[T]) bool) ConsumerOption; func WhereRaw(f func(RawMessage) bool) ConsumerOption
var ( ErrUnknownTopic, ErrNotOwner, ErrPermanent, ErrSchema ) // messages as §5E.4
```

## Tests to write first (05-events §5E.12 names; row: "outbox conformance tests", "crash matrix extended")

`TestOutbox_StageCommitsWithBatch`, `TestOutbox_DiscardLeavesNothing`, `TestOutbox_IntakeOutOfOrderCommitsNoSkip`,
`TestStageOrderWithinBatch`, `TestOutbox_LeakedBatchDoesNotStall`, `TestOutbox_IntakeCrashNoReissue` (`CrashClone` at
every point of the intake batch), `TestOutbox_ZombieRelayCannotDoubleSequence`, `TestOutbox_RedeliveryAfterCrashBeforeAck`,
`TestOutbox_TxHandlerEffectivelyOnce`, `TestOutbox_PerKeyOrderAndHeadOfLine`, `TestOutbox_UnorderedNoHOL`,
`TestOutbox_DeadLetterAfterMaxAttempts` (synctest), `TestOutbox_RetryDeadLetter`, `TestOutbox_RawSchemaMismatchDeadLetters`,
`TestOutbox_IdempotencyKeyStableAcrossRedelivery`, `TestOutbox_DuplicateConsumerNameRefused`,
`TestOutbox_ConsumeIntoTaskStartsOnce`, `TestOutbox_ConsumeIntoTaskRawWithoutImport`, `TestArchtest_ModuleConsumeFlagged`,
`TestOutbox_DisabledConsumerRetainsUntilReenableOrPurge`, `TestCoreOnlyTopicRetainedUntilAck`,
`TestOutbox_WhereAcksUnmatchedWithoutRun`, `TestOutbox_WhereWrongTypeRefused`, `TestOutbox_TxStartRunSameBatchAsTransition`,
`TestOutbox_ConsumeIntoTaskRequiresMode`, `TestRelay_DefinitionRegistersAndDeliversLive`,
`TestOutbox_RelayRestartsAfterCancel`, `TestOutbox_RelayDisabledDegradedNoLoss`, `TestNoGlobals_EventsOutbox`;
plus `TestOutboxTriggered_StartInSameBatchAsCursor` and `TestOutboxTriggered_ModeFromConsumerOption` (05-tasks §5T.9);
plus the `TestBootCrashMatrix`/`TestBootCrashMatrix_ABO` extensions.

`TestCoreOnlyTopicRetainedUntilAck` uses `core.notify.delivery.requested.v1` with consumer `core.notify.deliver`, which
the static core list gains only at S7: write the test against a test-only core consumer list entry and leave the S7 name
for PC-16-04.

## Acceptance

```bash
(cd core && go build ./... && go vet ./outbox/... ./kernel/...)                                # exit 0
(cd core && go test -race ./outbox/... ./kernel/... ./coretest/... -count=1)                     # all ok, no DATA RACE
(cd core && go test ./outbox/... -run 'TestOutbox_|TestRelay_|TestStageOrderWithinBatch|TestCoreOnlyTopicRetainedUntilAck|TestNoGlobals_EventsOutbox' -count=1 -v | grep -c '^--- PASS')   # expect >= 28
go test ./internal/books/ -run TestBootCrashMatrix_ABO -count=1                                  # ok
grep -rn '"c/outbox/' internal --include=*.go | wc -l                                             # expect 0 (ABO names no core key)
make -C core ci            # exit 0
make ci                    # exit 0 (series golden: additions only, the <ns>_outbox_* families)
```

## Out of scope

- The realtime hub and projections (PC-15-02), books' publishers (PC-15-03), the flip (PC-15-04).
- Any ABO producer or consumer of the outbox (webhooks and notify are S7: PC-16-04); `core.notify.deliver` itself.
- The outbox HTTP routes of 05-events §5E.11 (see Planner notes).

## Rollback

Binary swap; the `floor` build ignores `c/outbox/` (09 S6 Rollback: "outbox rows are left unprocessed by S5"; nothing
correctness-critical rides the outbox before S7).

## Repo rules

- File headers on every new or changed file; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S6 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`
  after the required checks and the per-PR prev-build tests are green; never squash. `check_soak.py` check (b) refuses
  the merge until OWNER-18's `floor` row is current.
- CLAUDE.md concurrency: the relay's delivery pool is the declared `Fixed(...)`; no goroutine per message.

## Planner notes

- The row lists `core/coretest/crashmatrix/` only, but 05-events §5E.6 and digest S6-S7 §7 require
  `TestBootCrashMatrix_ABO` to be extended too (`internal/books/crashmatrix_abo_test.go`, not in the row).
- 06 §6.6's S6 row adds `Manifest.OutboxConsumers`, `Registrar.OutboxHandler` and `Deps.Outbox` in `core/module/`,
  which the row does not list.
- No W15 row owns the outbox HTTP routes of 05-events §5E.11 (`GET /api/core/v1/events/topics`,
  `/api/core/v1/outbox/consumers`, `/dead`, the purge route). This brief leaves them out; the plan should place them.
