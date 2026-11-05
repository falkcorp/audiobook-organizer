<!-- file: docs/agent-tasks/platform-core/F6-02-core-realtime-hub-sse-eventhttp.md -->
<!-- version: 1.0.0 -->
<!-- guid: 246f9170-1d0e-4e5e-b734-1c29f5e86e69 -->
<!-- last-edited: 2026-11-05 -->

# F6-02 — Core realtime hub, dark: `realtime`, `internal/sse` (from opsv3's broker algorithm), `internal/eventhttp` serving `/api/core/v1/events`, permission-filtered topics with `Redact` and `Coalesce`

| Field | Value |
|---|---|
| ID | F6-02 |
| Release / flip | R2, events flip F6 (dark PR; live at F6-03) |
| Tier | **T3** (Opus, high): permission-filtered, resumable streams |
| Wave | W26 |
| Depends on | F4-04, F2-05 |
| Owns gate tests | the hub stream tests of 05-realtime: `TestHub_ResumeFromLastEventID`, `TestHub_PerEventPermissionFilter`, `TestHub_RefusalsAreStatusesBeforeStream` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f6-02-realtime-hub
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-04, F2-05 **and F4-02** (see Planner notes) are merged. If not, stop.

## Goal

Create `github.com/falkcorp/mediacore/realtime` (L6; imports `auth`, `events`, `log`, `metrics`), its implementation
`core/internal/sse`, and the route package `core/internal/eventhttp` registering `GET /api/core/v1/events` on
`CoreGroup` with `Perm: rbac.AnyAuthenticated` and `HandleRaw`, exactly as 05-realtime §3–§10 specify. The broker
algorithm comes from HEAD's opsv3 broker (`internal/server/handlers/opsv3/events.go`: the `bootNonce-seq` id scheme, the
ring, overflow reset, buckets, send-time filtering). Dark: the kernel creates the hub in P5 and `eventhttp` mounts the
route only when F6-03 wires it for ABO (hello may run it).

## Spec

- 05-realtime §3 (copy the package block: `Hub`, `NewHub`, `Options` with defaults, `Deps`, `Handler`, `Run`, `Close`,
  `Envelope`, the four errors), §4.1 (subscription grammar: `topic` exact, `<module>.*`, `*`; `exclude`; `key`;
  `last_event_id`; refusals before the stream as problem+json; response headers; first frames `retry: 3000` and
  `core.stream.opened.v1`), §4.2 (unnamed frames, `id: <bootNonce>-<seq>`, `: hb` heartbeat, the three control frames
  with no `id:`), §4.3 (ring resume and resets), §4.4 (send-time filtering: catalog `Permission` via
  `Checker.Satisfies`, `Visible`, `Redact`), §4.5 (one raw bus subscription per Realtime topic, coalescing, overflow
  reset, write deadlines through `http.ResponseController`, caps `MaxClients` 503 and `MaxPerBucket` 429 with
  `Retry-After`), §4.6 (`core.principal.invalidated.v1`, `core.rbac.changed.v1`, `auth.Service.Revalidate` on every
  heartbeat), §4.7 (streamed core topics), §5–§10.
- 05-events §5E.3.1 (`RedactFunc`, `RealtimeMeta.Coalesce`; `Realtime` never on a `Durable` topic), §5E.13.
- 05-http-api §4.2 stage 9 (compression skips `HandleRaw` and `text/event-stream`), §5 item 4 (SSE handlers end on
  `Server.RegisterOnShutdown` + `Hub.Close`).
- 04 §8.1 F6 row (`realtime`, `internal/sse`, `internal/eventhttp`).

## Files

`core/realtime/`, `core/internal/sse/`, `core/internal/eventhttp/` (all created).

## Re-verify at BASE

```bash
ls -d core/realtime core/internal/sse core/internal/eventhttp 2>/dev/null | wc -l     # 0
grep -rn 'type Catalog interface' core/events/ | wc -l                                # 1 (F4-04)
grep -rn 'func (s \*Service) Revalidate' core/auth/ | wc -l                           # 1 (F4-02; see Planner notes)
grep -rn 'HandleRaw' core/httpapi/ | wc -l                                            # >= 1 (R1-13)
grep -rn 'func OnRaw' core/events/ | wc -l                                            # 1
# HEAD's broker you port (HEAD f7ac5b11e)
ls internal/server/handlers/opsv3/events.go | wc -l                                   # 1
grep -c 'bootNonce\|nonce' internal/server/handlers/opsv3/events.go                  # >= 1
grep -c 'withUserText' internal/server/handlers/opsv3/events.go                      # >= 1 (HEAD :615)
```

## STOP conditions

1. `auth.Service.Revalidate` does not exist (F4-02 not merged): the hub needs it (Planner notes).
2. A refusal would be written as a stream frame instead of a status before the stream.
3. A subscriber could receive an event its checker does not satisfy, including on replay.
4. ABO would serve the route before F6-03.

## Steps

1. Write the tests below first (hello, `coretest`, `testing/synctest` where the spec says).
2. `internal/sse`: ring of `RingSize` entries `{seq, topic, key, payload}`, hub-wide `uint64` seq, `bootNonce` 8 random
   bytes hex per process, one queue per subscriber with a capacity-1 wake channel, overflow → drop queued entries and send
   `core.stream.reset.v1{"reason":"overflow"}`, per-write deadline, heartbeat, filters applied at send time.
3. `realtime.Hub`: one raw bus subscription per catalog topic with `Realtime != nil`, coalescing by `Key` within
   `RealtimeMeta.Coalesce`, the invalidation and rbac-generation handlers, the caps and buckets (session/interactive vs
   API key; auth-disabled principals share one bucket bounded by `MaxClients`), `Close` sends `closed{shutdown}` to every
   stream and is idempotent.
4. `internal/eventhttp`: parse the query (`topic`, `exclude`, `key`, `last_event_id` and the `Last-Event-ID` header),
   expand wildcards over topics the principal may see now, refuse an explicit forbidden topic with 403
   `topic_not_permitted`, unknown topic 400, caps 503/429 with `Retry-After`, all before any `text/event-stream` byte.
5. No per-module handle and no legacy projection (D10). Headers, one `changelog.d/` fragment, Acceptance.

## Tests first (05-realtime §10)

Gate tests `TestHub_ResumeFromLastEventID`, `TestHub_PerEventPermissionFilter`, `TestHub_RefusalsAreStatusesBeforeStream`,
then `TestHub_ResumeAfterScheduledReconnect`, `TestHub_NativeFramesUnnamed`, `TestHub_ResetOnRestartNonce`,
`TestHub_ResetOnGap`, `TestHub_SlowSubscriberDoesNotBlockPublisher` (synctest), `TestHub_ReplayIsFilteredLikeLive`,
`TestHub_StreamClosesOnCredentialRevoke`, `TestHub_RevalidateOnHeartbeat`, `TestHub_PermissionRevokedDropsTopic`,
`TestHub_CapsGlobalAndBucket`, `TestHub_CoalesceLatestPerKey` (synctest, 250 ms), `TestHub_WriteDeadlineDisconnects`,
`TestHub_FilterPanicIsContained`, `TestHub_ShutdownClosesStreams`, `TestHub_ExcludePattern`, `TestNoGlobals` (realtime).
`TestHub_TaskTopicVisibleWithoutTasksView` and `TestRunRedaction_SameOnStreamAndREST` need the executor's
`core.task.*` topics live (F5-18): write them against a fixture topic with the same `Visible`/`Redact` shape and leave a
note for F6-03 to run them on the real topics.

## Invariants

- Filtering at send time, per subscriber, with the subscriber's current rights; replay filtered like live.
- A slow subscriber never blocks a publisher; no goroutine per publish.
- Refusals are HTTP statuses before the stream.
- One hub per App (R5).

## Acceptance

```bash
(cd core && go vet ./realtime/... ./internal/sse/... ./internal/eventhttp/...)                 # exit 0
(cd core && go test -race -count=1 ./realtime/... ./internal/sse/... ./internal/eventhttp/...) # ok
(cd core && go test -count=1 -run 'TestHub_ResumeFromLastEventID|TestHub_PerEventPermissionFilter|TestHub_RefusalsAreStatusesBeforeStream' ./...)   # PASS
make -C core ci                                                                                # exit 0
make ci                                                                                        # exit 0
```

## Out of scope

Deleting hub A and `/api/events`, books' topics, the SPA client (F6-03); `@mediacore/shell`'s client (F9).

## Rollback

Revert the PR; ABO serves no new route.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/realtime): permission-filtered resumable SSE hub and /api/core/v1/events (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Planner notes

- **Missing dependency F4-02** (and F4-01). `realtime.Deps.Auth` is `*auth.Service` (for `Revalidate`) and filtering
  uses `rbac.Checker`; the row lists only F4-04 and F2-05. F4-02 merges in W24, before this row's W26, so the wave order
  holds, but the dependency is undeclared.
- Kernel wiring (P5 hub creation, mounting `eventhttp`) is in no row's files (`core/kernel/` is absent from F6-02 and
  F6-03).
