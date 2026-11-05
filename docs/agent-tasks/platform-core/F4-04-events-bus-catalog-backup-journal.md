<!-- file: docs/agent-tasks/platform-core/F4-04-events-bus-catalog-backup-journal.md -->
<!-- version: 1.1.3 -->
<!-- guid: 927dc665-9438-462c-a5d9-a0e41b62bdb3 -->
<!-- last-edited: 2026-11-05 -->

# F4-04 — The events bus (`Bus`, `Publish`/`On`, the topic catalog with permissions, drop policy) and `backup/journal`'s revocation-journal types, dark

| Field | Value |
|---|---|
| ID | F4-04 |
| Release / flip | R2, identity flip F4 (dark PR; the bus goes live at F4-08) |
| Tier | **T3** (Opus, high): a bus and a catalog with permissions; leaf types other packages import |
| Wave | W24 |
| Depends on | OWNER-03 (NOT AGENT WORK: R1 released and deployed) |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-04-events-journal
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm in the tracking issue that OWNER-03 recorded the R1 release and deploy. If not, stop.

## Goal

1. Create `github.com/falkcorp/mediacore/events` exactly as 05-events §5E.3 specifies: `Topic[T]`, `TopicSpec` (the
   one catalog entry, with `Permission`, `Visible`, `Redact`, `Durable`, `Notify`, `Realtime`), `Catalog`, `Envelope`,
   `RawEnvelope`, `Bus`, `Scope`, `Publisher`, `Publish`, `On`, `OnRaw`, the subscription options, `Open`/`Close`,
   with 05-events §5E.5.1's delivery contract (one bounded FIFO and one goroutine per subscription, drop policy,
   panic isolation, bus-lifecycle handler context, coalescing).
2. Create `github.com/falkcorp/mediacore/backup/journal` (L4; imports `entity`, `storage`, `storage/restorejournal`
   only): `Kind` and its constants, `Revocation`, `Head`, `ServedBoot`, `RevocationJournal`, `RestoreKind`,
   `RestoreContext`, `RestoreHook`, the file-backed journal implementation and `StorageSpec` (the `c/backup/` spec the
   kernel registers from the identity flip), so `auth` and `rbac` (F4-01, F4-02) can import them without a cycle. The
   file-backed implementation and `StorageSpec` are this row's, not F8-01's: F4-01's `rbac.Deps.Revocations` is this
   package's `RevocationJournal`, and F4-08 registers the `c/backup/` spec at boot. The journal's authenticated format
   is **F4-11's**, not this row's: the header fields, the `kid`/`mac` chain behind a derive-key seam, mode 0600, the
   install check, `backup.location.json` and `c/backup/served_boot` (05-backup §5B.3.6). Build the append semantics
   below with the line encoding and the file open in one place each, so F4-11 extends them without a second writer.
3. `core/kernel/events.go`: build one `Bus` per App and validate the catalog at registration, **dark**: no module
   registers a topic or subscription before F4-08, and the kernel opens the bus only when F4-08 wires it.

## Spec

- 05-events §5E.1, §5E.3.1 (catalog, kernel validation rules, core topics table), §5E.3.2 (bus API), §5E.5.1
  (delivery), §5E.6 (lifecycle), §5E.7, §5E.8, §5E.9 (`core.events.queue_size` and the rest of the bus keys), §5E.10
  (bus metrics), §5E.12 (bus tests), §5E.13 "Order" item (1).
- 05-backup §5B.3.2 (the `journal` package block: copy it), §5B.3.6 (journal semantics: append before the revoking
  batch commits, head with `GetForUpdate`, gaps, coverage).
- 04 §8.1 F4 row (`backup/journal` lands at F4 because declaring its types in `auth` would make `rbac` → `auth` →
  `rbac` a cycle; `events` is the bus `auth`/`rbac` publish on).
- 06 §6.4.10 F4 row (`Registrar.Topics`, `Registrar.Subscribe`, `Deps.Events`; the kernel registers `backup/journal`'s
  `storage.Spec` for `c/backup/` with `Serialized: ["journal_head", "served_boot"]` and prefixes `journal_gap/` and
  `restore_hooked/`); 06 §6.5.2 (`Bus.Open` after module hooks and workers start; `Bus.Close` after the executor drain).

## Files

| Action | Path |
|---|---|
| create | `core/events/` |
| create | `core/backup/journal/` |
| create | `core/kernel/events.go` |

## Re-verify at BASE

```bash
ls -d core/events core/backup 2>/dev/null | wc -l                         # expect 0
grep -rn 'type Requirement' core/rbac/ | wc -l                            # expect 1 (R1-12 leaf)
grep -rn 'func For\[' core/internal/jsonschemax/ | wc -l                  # expect 1 (R1-12)
grep -rn 'type Ref struct' core/entity/ | wc -l                           # expect 1 (R1-09)
grep -rn 'type Record struct' core/storage/restorejournal/ | wc -l        # expect 1 (R1-20)
grep -rn 'Serialized' core/storage/*.go | wc -l                           # expect >= 1 (storage.Spec)
# HEAD's buses this replaces later (HEAD ac4368bdb; informational)
grep -c 'type EventType' internal/plugin/events.go                        # expect 1
ls internal/realtime/events.go | wc -l                                    # expect 1
```

## STOP conditions

1. `events` would need to import `notify`, `realtime`, `tasks` or `auth` (it imports none: `NotifyMeta` and
   `RealtimeMeta` are plain data).
2. `backup/journal` would import anything but `entity`, `storage`, `storage/restorejournal` and the standard library.
3. Anything here changes ABO's behaviour (no ABO file is in this row).

## Steps

1. Write the tests below first.
2. `events` per §5E.3: names `<moduleID>.<noun>.<verb-past>.v<N>`; kernel validation at registration (bad grammar,
   duplicate, foreign namespace, zero `Permission`, a `Permission` naming another module's scope, `Realtime`/`Notify`
   with an empty schema, `Realtime` on a `Durable` topic refused with "Realtime on a Durable topic: declare a companion
   bus topic"). `Publish` never blocks and never errors; an undeclared, foreign or `Durable` topic is counted
   (`publish_rejected`) and logged once per topic. Subscription names `<moduleID>.<name>`, unique per bus; frozen when
   `Register` returns; no delivery before `Open`, none after `Close`. Default queue 256, max 65536, `DropNewest`
   unless `DropOldest()`; drops counted in `mediacore_events_dropped_total{topic,subscriber,reason}` with at most one
   warning per subscriber per minute. The bus families of §5E.10 add the core `events` metrics scope: add its
   `testdata/series.golden` under `core/events/`, asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5). `OnRaw` validates the JSON payload against the declared schema; the JSON form is
   encoded once per publish, lazily, only when a raw subscriber exists.
3. `backup/journal` per §5B.3.2: copy the package block (types, constants, interface, comments' contracts). The file
   implementation appends one fsynced line per `Append` before the caller commits, stages `c/backup/journal_head =
   max(head, r.Seq)` into the caller's batch with `GetForUpdate`, and on error records nothing and lets the caller call
   `Gap(b, at)`. Seq 0 is the header line, then 1, 2, … without gaps; the header's fields and every line's MAC are
   F4-11's (do not invent a header shape here). `StorageSpec` returns the `c/backup/` spec with
   the two `Serialized` exact keys and the two prefixes above.
4. `core/kernel/events.go`: construct the bus and the catalog, validate core topics and module topics at P7, `Open` at
   06 §6.5.2's step, `Close` at shutdown, all behind the wiring F4-08 turns on. Register the core topics this file
   owns only when their owners land (`core.rbac.changed.v1` with F4-08; the `core.task.*` topics at F5).
5. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract (copy exactly from 05-events §5E.3.1–§5E.3.2 and 05-backup §5B.3.2)

```go
func NewTopic[T any](name string) Topic[T]
func (t Topic[T]) Name() string
func (t Topic[T]) Spec(s TopicSpec) TopicSpec
type VisibleFunc func(ctx context.Context, who rbac.Checker, e RawEnvelope) bool
type RedactFunc func(who rbac.Checker, payload json.RawMessage) json.RawMessage
type Durable struct{ Partitions int }
type Catalog interface {
	Lookup(name string) (TopicSpec, bool)
	All() iter.Seq[TopicSpec]
}
func (b *Bus) Scope(moduleID string) Scope
func Publish[T any](p Publisher, ctx context.Context, t Topic[T], key string, payload T)
func On[T any](t Topic[T], name string, h Handler[T], opts ...SubOption) Subscription
func OnRaw(topic, name string, h RawHandler, opts ...SubOption) Subscription
func QueueSize(n int) SubOption
func DropOldest() SubOption
func KeyFilter(f func(key string) bool) SubOption
func Coalesce(window time.Duration) SubOption
func (b *Bus) Open() error
func (b *Bus) Close(ctx context.Context) error

// package journal
type RevocationJournal interface {
	Append(ctx context.Context, b *storage.Batch, r Revocation) error
	Gap(b *storage.Batch, since time.Time) error
}
type RestoreHook func(ctx context.Context, rc RestoreContext) (counts map[string]int, err error)
```

## Tests first (05-events §5E.12, bus rows)

`TestTopicName_Grammar`, `TestCatalog_RejectsDuplicateZeroPermissionForeignScope`,
`TestCatalog_NotifyAndRealtimeReadNoSecondDeclaration`, `TestCatalog_RejectsRealtimeOnDurable`,
`TestBus_SlowSubscriberDropsAndCounts`, `TestBus_DropOldestKeepsNewest`, `TestBus_PanicIsolated_SubscriptionSurvives`,
`TestBus_HandlerContextIsBusNotPublisher`, `TestBus_NoDeliveryBeforeOpenNoneAfterClose`, `TestBus_PerPublisherFIFO`,
`TestBus_CoalesceLatestPerKey` (synctest), `TestBus_CloseDrainsWithinBudget` (synctest), `TestBus_NoGoroutinePerEvent`,
`TestOnRaw_CrossModuleWithoutImport`, `TestOnRaw_DisabledModuleWarns`, `TestNoGlobals_EventsOutbox` (the events half; R5
AST). For the journal: `TestJournal_AppendBeforeCommitHeadMonotone` and `TestJournal_AppendFailureStagesNothing` (names
coined here: two revocations committing out of order never move the head backwards; a failed append stages nothing and
`Gap` records the interval); `TestJournalImportsOnlyAllowed` (name coined here: `go list -deps` allow-list). For the
bus metrics: `metricstest.AssertSeries` over the `events` scope's `testdata/series.golden` (05-metrics §12). The
journal's integrity and re-apply tests of 05-backup §5B.9 are F4-11's.

## Invariants

- One `Bus` per App, never a package global (R5).
- A slow or panicking subscriber never blocks or breaks `Publish` or another subscriber.
- `backup/journal` imports nothing above L1 except `entity`, so `auth` → `journal` and `rbac` → `journal` close no cycle.

## Acceptance

```bash
(cd core && go vet ./events/... ./backup/... ./kernel/...)                              # exit 0
(cd core && go test -race -count=1 ./events/... ./backup/... ./kernel/...)              # ok
(cd core && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./backup/journal | sort)
#   expected: only github.com/falkcorp/mediacore/{backup/journal,entity,storage,storage/restorejournal} and the
#   packages storage itself imports
make -C core ci                                                                         # exit 0
make ci                                                                                 # exit 0
```

## Out of scope

The outbox (F6-01); the realtime hub (F6-02); module `Registrar.Topics`/`Subscribe` and `Deps.Events` wiring and the
live bus (F4-08); the journal's authenticated format, `backup.location.json`, `c/backup/served_boot` and the boot
re-apply (F4-11); backup's in-use check and archives (F8-01).

## Rollback

Revert the PR; nothing uses the packages yet.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/events): topic catalog and bus; backup/journal revocation types (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
