<!-- file: docs/agent-tasks/platform-core/PC-15-02-realtime-hub-legacy-projections.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5fef4d5d-6afd-47be-a9f2-16efb70fa410 -->
<!-- last-edited: 2026-11-04 -->

# PC-15-02 — core/realtime hub, internal/sse and internal/eventhttp with legacy projections A, B and C

| Field | Value |
|---|---|
| ID | PC-15-02 |
| Tier | **T3 Opus/high** — resumable, permission-filtered streams whose legacy framing must stay byte-equal to three HEAD implementations |
| Wave | W15 (S6, release `S6`; live PR before the flip) |
| Depends on | PC-15-01 |
| Serialized | PC-15-01 → **PC-15-02** → PC-15-03 → PC-15-04 |
| Trailer | `Stage-Release: S6 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 (`realtime`, `internal/eventhttp` rows), §4 "05-realtime §3", §5 rules 2–5, §7 S6 |
| Owning spec | 05-realtime §3 (API), §4.1–§4.9 (behaviour), §5–§9, §10 (tests), §11 (migration); 09 S6 "Moves", "Endpoints"; 05-http-api §4.2 stage 9 (no compression for streams) |

## Goal

1. `core/realtime` (L6) with its engine `core/internal/sse`: the one hub — resume by `Last-Event-ID`, per-subscriber,
   per-event permission filtering through the events catalog, bounded queues with `reset{overflow}`, caps, credential
   revalidation — generalised from HEAD's stream C broker (`internal/server/handlers/opsv3/events.go`).
2. `core/internal/eventhttp`: the native `GET /api/core/v1/events` on `CoreGroup` (`rbac.AnyAuthenticated`, `HandleRaw`).
3. `Registrar.Realtime().Projection(p)`: the lazy legacy-projection handler mechanism (503 until P8).
4. Books' three projection definitions A, B, C (their `Frame`, `Heartbeat`, `WithIDs`, `OnClose`) in ABO, tested
   byte-for-byte against goldens captured from HEAD's three implementations **in this PR** (test first).

Nothing is mounted at the legacy paths yet: HEAD's hub A, `registry.EventHub` and the v3 broker keep serving streams A,
B and C, fed by the S5 bridge, until the flip PC-15-04 swaps all three at once. The native route is live from this PR.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-15-02" -b feat/pc-15-02-realtime-hub origin/main
cd "$REPO/../audiobook-organizer-pc-15-02"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create: `core/realtime/` (exactly 05-realtime §3: `Registrar`, `Hub`, `NewHub`, `Options`, `Deps`, `Handler`,
`Registrar(moduleID)`, `LegacyProjection`, `Run`, `Close`, `Envelope`, the four errors), `core/internal/sse/`
(subscriber queues, ring, framing, caps, write deadlines), `core/internal/eventhttp/` (the native route).

Modify:

- `internal/realtime/` — add `projections.go` (file name coined here) with three constructors returning
  `realtime.LegacyProjection` for A, B and C (names coined here: `ProjectionA`, `ProjectionB`, `ProjectionC`), plus their
  golden tests; HEAD's hub A (`events.go`) and `book_changes.go` stay untouched (PC-15-03 rewrites the coalescer,
  PC-15-04 deletes hub A).
- `internal/itunes/service/` — delete the unused `Realtime *realtime.EventHub` field (`service.go:28` at `748fd787a`;
  no reader or writer exists), so the package stops importing `internal/realtime` before the flip deletes hub A.
- `core/openapi/core.json` — add `GET /api/core/v1/events`.
- `core/kernel/` — build the hub at P5, hand `Registrar.Realtime()` at P6, bind projections at P7, start `Hub.Run`, and
  call `Hub.Close` from `http.Server.RegisterOnShutdown` (05-realtime §5). Not in the inventory row; see Planner notes.

## Re-verify greps (ABO paths identical at `748fd787a` and the plan checkout)

```bash
grep -rln 'text/event-stream' --include=*.go internal | grep -v _test | sort | tr '\n' ' '    # expect: internal/realtime/events.go internal/server/handlers/operations_v2.go internal/server/handlers/opsv3/events.go
grep -n "^func NewBroker\|^type BrokerOptions struct" internal/server/handlers/opsv3/events.go | wc -l   # expect 2 (the prior art)
grep -n "func (h \*OperationsV2Handler) OperationsSSE" internal/server/handlers/operations_v2.go          # expect 1 (stream B, ~:1063)
grep -n "^func (h \*EventHub) HandleSSE" internal/realtime/events.go                                     # expect 1 (stream A)
grep -n "Realtime *\*realtime.EventHub" internal/itunes/service/service.go                              # expect 1 (the unused field)
grep -rn "Realtime:" --include=*.go internal | grep -v _test | wc -l                                     # expect 0 (nobody sets it)
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/realtime"' --include=*.go . | grep -v _test | sort | tr '\n' ' '   # expect 3: itunes/service/service.go, server/server.go, server/server_lifecycle.go
grep -rn "func (.*) Stage\[\|func Stage\[" core/outbox --include=*.go | wc -l                             # PC-15-01; expect 1
test ! -e core/realtime && test ! -e core/internal/eventhttp && echo ok                                    # expect ok
```

## Invariants (05-realtime §4)

1. **Native framing:** no `event:` line; `id: <bootNonce>-<seq>` (8 random bytes hex per process, hub-wide `uint64`);
   `data:` = the `Envelope` JSON; heartbeat `: hb`; first frames `retry: 3000` and `core.stream.opened.v1`; control frames
   (`opened`, `reset{restart|gap|overflow}`, `closed{shutdown|credential_revoked|permission_revoked}`) carry no `id:`.
2. **Resume:** ring of `RingSize` (1024); `Last-Event-ID` header or `?last_event_id=`; unknown nonce, malformed id or seq
   out of the ring → `reset` first; replayed entries are filtered exactly like live ones.
3. **Permissions at send time:** catalog `Permission` via `Checker.Satisfies`, then `Visible`, then `TopicSpec.Redact`;
   an explicitly named forbidden topic → 403 before the stream starts; wildcards silently omit.
4. **Backpressure:** one raw bus subscription per Realtime topic; publish appends under a mutex and non-blocking-signals
   each subscriber's wake channel (capacity 1); no goroutine per publish; full queue (`ClientBuffer` 128) → drop queued,
   send `reset{overflow}`, continue; per-write deadline `WriteTimeout` (10 s) through `http.ResponseController`.
5. **Caps:** `MaxClients` 256 global (503 + `Retry-After` 5 s); per user per credential class `MaxPerBucket` 16 (native)
   and `MaxLegacy` 16 (projections), 429 + `Retry-After`.
6. **Credential lifetime:** `core.principal.invalidated.v1` → `closed{credential_revoked}`; `core.rbac.changed.v1` →
   re-resolve the checker; every heartbeat calls `auth.Service.Revalidate`.
7. **Projections are not aliases.** `Projection(p)` returns a handler during P6 that answers 503 + `Retry-After` until
   P8; an unknown or non-Realtime topic, or a duplicate `Name`, aborts boot.
8. **Projection A's topics** are `core.task.*`, `books.book.changed.v1`, `books.system.status-ticked.v1` at S6 and do not
   change at S8 (`core.system.status-changed.v1` is never added). Frames `data: {"type":…,"id":…,"timestamp":…,"data":…}`
   with HEAD's type names, `: ping`, no ids, `?operation=` → `key`, `OnClose("shutdown")` emits HEAD's `system.shutdown`.
   **B:** the task topics as `event: op.created|op.updated|op.terminal|op.log|op.current_item`, `: heartbeat`.
   **C:** `core.task.run-changed.v1` as `event: run`, ids kept (`WithIDs`), `event: resync` for the reset control frame.
9. **No globals** (`TestNoGlobals` archtest): the hub is per App.

## Steps

1. **Goldens first.** From HEAD's still-live implementations, capture one golden per event type per stream (A: hub A's
   `HandleSSE` frames for `operation.progress/status/log`, `system.status`, `books.changed`, `system.shutdown`; B: stream B's
   `op.*` frames; C: the v3 broker's `run` and `resync` frames) into `internal/realtime/testdata/` (file names coined
   here). Commit them.
2. Write the tests below.
3. Implement `core/internal/sse` and `core/realtime` per invariants 1–7 and 9; `eventhttp` registers
   `GET /api/core/v1/events` on `CoreGroup` with `rbac.AnyAuthenticated` and `HandleRaw`; responses carry
   `Content-Type: text/event-stream`, `Cache-Control: no-store`, `X-Accel-Buffering: no` and are never compressed.
4. Kernel wiring (Modify list).
5. `internal/realtime/projections.go`: the three `LegacyProjection` values (invariant 8) whose `Frame` reproduces the
   goldens byte-for-byte from the corresponding core and books topic payloads.
6. Remove the unused itunes field; regenerate `core.json`.
7. Acceptance; headers; `changelog.d/` fragment.

## Code contract (exact, 05-realtime §3)

```go
type Registrar interface { Projection(p LegacyProjection) (http.Handler, error) }
func NewHub(opts Options, deps Deps) *Hub
func (h *Hub) Handler() http.Handler
func (h *Hub) Registrar(moduleID string) Registrar
func (h *Hub) Run(ctx context.Context) error
func (h *Hub) Close(ctx context.Context) error
type LegacyProjection struct {
	Name string; Topics []string
	Frame func(topic string, data json.RawMessage) (event string, payload []byte, ok bool)
	WithIDs bool; Heartbeat string
	OnClose func(reason string) (event string, payload []byte, ok bool)
}
type Envelope struct { Topic string `json:"topic"`; Time time.Time `json:"ts"`; Key string `json:"key,omitempty"`; Data json.RawMessage `json:"data"` }
var ( ErrTooManyClients, ErrTooManyForUser, ErrUnknownTopic, ErrTopicDenied ) // messages as §3
```

## Tests to write first (05-realtime §10 names)

Core: `TestHub_ResumeFromLastEventID`, `TestHub_ResumeAfterScheduledReconnect`, `TestHub_NativeFramesUnnamed`,
`TestHub_RefusalsAreStatusesBeforeStream`, `TestHub_ResetOnRestartNonce`, `TestHub_ResetOnGap`,
`TestHub_SlowSubscriberDoesNotBlockPublisher` (synctest), `TestHub_PerEventPermissionFilter`,
`TestHub_TaskTopicVisibleWithoutTasksView`, `TestHub_ReplayIsFilteredLikeLive`, `TestHub_StreamClosesOnCredentialRevoke`,
`TestHub_RevalidateOnHeartbeat`, `TestHub_PermissionRevokedDropsTopic`, `TestHub_CapsGlobalBucketAndLegacy`,
`TestHub_CoalesceLatestPerKey` (synctest), `TestHub_WriteDeadlineDisconnects`, `TestHub_FilterPanicIsContained`,
`TestHub_ShutdownClosesStreams`, `TestHub_ExcludePattern`, `TestRunRedaction_SameOnStreamAndREST` (stream half),
`TestProjection_LazyHandler503UntilServing`, `TestProjection_RejectsUnknownOrNonRealtimeTopic`, `TestNoGlobals`.

ABO: `TestLegacyProjection_FramesGolden` (A, B, C: byte-for-byte from one publish, A's `system.shutdown` included);
`TestProjectionTopicsExistAtStage` (row done-test: P7 binds projection A with the S6 catalog and with the S8 catalog;
stream A's `system.status` frame keeps HEAD's payload at both). Until PC-15-03 registers `books.book.changed.v1` and
`books.system.status-ticked.v1`, run it over a catalog fixture that declares those two topics with the spec's settings
(`Realtime`, permission `books:view`, `Coalesce: 250ms` for the book-change topic); PC-15-04 switches the test to books'
real registrations.

## Acceptance

```bash
(cd core && go build ./... && go vet ./realtime/... ./internal/sse/... ./internal/eventhttp/...)      # exit 0
(cd core && go test -race ./realtime/... ./internal/sse/... ./internal/eventhttp/... -count=1)        # all ok
(cd core && go test ./realtime/... ./internal/sse/... -run 'TestHub_|TestProjection_' -count=1 -v | grep -c '^--- PASS')   # expect >= 21
go test ./internal/realtime/ -run 'TestLegacyProjection_FramesGolden|TestProjectionTopicsExistAtStage' -count=1 -v | grep -c '^--- PASS'   # expect 2
grep -c '"/api/core/v1/events"' core/openapi/core.json                                     # expect >= 1
grep -rln '"github.com/falkcorp/audiobook-organizer/internal/realtime"' internal/itunes --include=*.go | wc -l   # expect 0
grep -rln 'text/event-stream' --include=*.go internal | grep -v _test | wc -l              # expect 3 (HEAD's three, unchanged until the flip)
make -C core ci            # exit 0
make ci                    # exit 0
```

## Out of scope

- Mounting the projections at `/api/events`, `/api/v1/operations/events`, `/api/v3/ops/events`; deleting hub A, the S5
  bridge, `registry.EventHub` or the v3 broker (PC-15-04, in one PR).
- Books' `books.book.changed.v1` and `books.system.status-ticked.v1` topics and publishers (PC-15-03).
- Any UI change (the SPA keeps `eventSourceManager` on the legacy URLs until S9).

## Rollback

Binary swap. The native route is new and holds no state; HEAD's streams are untouched.

## Repo rules

- File headers on every new or changed file (goldens are data, no header); `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S6 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`
  after required checks; never squash; `check_soak.py` needs the `floor` row.
- CLAUDE.md concurrency: one goroutine per subscriber, none per publish; bounded queues.

## Planner notes

- The row's done-test `TestProjectionTopicsExistAtStage` needs books' two S6 topics, which PC-15-03 (a later row)
  registers; this brief runs it over a catalog fixture here and has PC-15-04 switch it to the real registrations.
- The hub's kernel wiring (P5 build, P6 `Registrar.Realtime()`, P7 bind, `RegisterOnShutdown`) and 06 §6.6's
  `Registrar.Realtime` field need `core/kernel/` and `core/module/`, which the row does not list.
