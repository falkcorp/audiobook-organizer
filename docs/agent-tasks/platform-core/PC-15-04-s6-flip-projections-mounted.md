<!-- file: docs/agent-tasks/platform-core/PC-15-04-s6-flip-projections-mounted.md -->
<!-- version: 1.0.0 -->
<!-- guid: 391a8d65-37f3-49b0-8159-16cf7c747f7e -->
<!-- last-edited: 2026-11-04 -->

# PC-15-04 — S6 flip: books mounts the projections at the legacy stream paths; the S5 bridge, registry.EventHub, hub A and the v3 broker are deleted

| Field | Value |
|---|---|
| ID | PC-15-04 |
| Tier | **T3 Opus/high** — three live streams change server at once; the SPA (Activity page, Library patches, shutdown banner) and external SSE consumers must see byte-equal frames |
| Wave | W15 (S6), **the flip**: sets `shipped_stage: S6` and `release: S6` together |
| Depends on | PC-15-03 |
| Serialized | last PR of W15 |
| Trailer | `Stage-Release: S6 flip` (instead of `S6 1`) |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §1 (S6 flip), §5 rules 2–6, §7 S6 |
| Owning spec | 09 S6 "Flip PR", "Endpoints", "Done when"; 05-realtime §4.9, §10, §11; 09 §9.2.1 (the `sse` compat surface; cells a, b, c of the R-test, §9.10) |

## Goal

In one PR: books obtains the three projection handlers from `Registrar.Realtime().Projection(p)` during P6 (PC-15-02's
`ProjectionA/B/C`) and mounts them in its gin manifest at `GET /api/events`, `GET /api/v1/operations/events` and
`GET /api/v3/ops/events` (`Perm: rbac.AnyAuthenticated`, `OwnAuth: false`, `NoCompress`), counted on compat surface
`sse`; and deletes the S5 bridge, `registry.EventHub`, hub A and the v3 broker. Deleting any of them earlier breaks
stream parity; deleting them later leaves two servers per stream. UI: no change.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-15-04" -b feat/pc-15-04-s6-flip origin/main
cd "$REPO/../audiobook-organizer-pc-15-04"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Modify:

- `internal/books/stream_bridge.go` — delete (with `stream_bridge_test.go`; their S5-only tests
  `TestS5Bridge_LegacyStreamsShowCoreRuns` and `TestS5Bridge_RepairsRunHiddenFromViewer` retire with them, 05-realtime §10
  "S5 only").
- `internal/server/` — remove hub A's construction and routes (`server.hub`, `realtime.NewEventHub`,
  `NewBookChangeCoalescer`, the `/api/events` route at HEAD `server_lifecycle.go:1782`, both `s.hub.Broadcast` sites),
  stream B's handler (`OperationsSSE`, HEAD `handlers/operations_v2.go:1063-1110`), the v3 broker
  (`handlers/opsv3/events.go`, `NewBroker`) and its route; mount the three projection handlers through books' gin
  manifest; keep HEAD's tracing skip list (`server.go:696`) and gzip exclusion (`server.go:1604`) equivalent for the three
  paths.
- `internal/operations/registry/` — delete `bus.go` (`registry.EventHub`) and its publish calls.
- `internal/books/module.go` — P6 `Projection` calls; the status worker stops forwarding to hub A; the book-change
  observer stops feeding the coalescer.
- `ci/stage.txt` — `shipped_stage: S6`, `release: S6`.

Also modify (not in the inventory row; see Planner notes): `internal/realtime/` (delete `events.go` and
`book_changes.go`, keep `projections.go`), `docs/compat-surfaces.md` and `internal/books/compat-surfaces.md` (the three
`sse` ledger rows), `internal/realtime/projections_test.go` (switch `TestProjectionTopicsExistAtStage` to books' real
registrations).

## Re-verify greps (at this wave's base; HEAD values in brackets)

```bash
grep -rln 'text/event-stream' --include=*.go internal core | grep -v _test | sort      # base: HEAD's 3 under internal/ plus core/internal/sse (and eventhttp) [HEAD: 3]
test -f internal/books/stream_bridge.go && echo present                                # PC-12-08; expect present
grep -n "^type EventHub struct" internal/operations/registry/bus.go                    # expect 1
grep -n "^func NewBroker" internal/server/handlers/opsv3/events.go                     # expect 1
grep -n "^func (h \*EventHub) HandleSSE" internal/realtime/events.go                   # expect 1
grep -rn "hub.Broadcast(&realtime.Event{" --include=*.go internal | grep -v _test | wc -l   # expect 2 [scan.auto_triggered ~:487, system.shutdown ~:565]
grep -n 's.router.GET("/api/events"' internal/server/server_lifecycle.go               # expect 1 (~:1782)
grep -n 'SkipPaths: \[\]string{"/api/v1/operations/events", "/api/v3/ops/events"}' internal/server/server.go   # expect 1
grep -n 'gzip.WithExcludedPaths(\[\]string{"/api/events"' internal/server/server.go     # expect 1
grep -rn "func ProjectionA\|func ProjectionB\|func ProjectionC" internal/realtime --include=*.go | wc -l   # PC-15-02; expect 3
grep -rn "books.system.status-ticked.v1\|books.book.changed.v1" internal/books --include=*.go | grep -v _test | wc -l   # PC-15-03; expect >= 2
grep -n "^shipped_stage: S5$\|^release: floor$" ci/stage.txt | wc -l                      # expect 2
```

## Invariants

1. **Byte-equal frames.** For every event type of each stream, the mounted projection emits exactly HEAD's frame (the
   goldens PC-15-02 captured): A `data: {"type":…,"id":…,"timestamp":…,"data":…}` with `: ping` and no ids, `?operation=`
   → `key`, `system.shutdown` on close; B `event: op.*` with `: heartbeat`; C `event: run` with ids and `event: resync`.
   `system.status` frames are byte-shape-equal to S5's.
2. **Permissions.** Each projection route is `rbac.AnyAuthenticated` on the gin manifest; per-topic permission, `Visible`
   (a definition's `View`) and `Redact` (`tasks.RedactRunView`) apply per event inside the hub, so a Repairs run stays
   hidden from a `library.view` subscriber without the users permission on B and C, and labels stay blanked where HEAD
   blanked them. No stream becomes wider than at HEAD.
3. **Projections are not aliases** (05-realtime §4.9): books mounts the handlers from its gin manifest; no
   `httpapi.Alias` is added; they are recorded as compat surface `sse` with names `a`, `b`, `c` and counted in
   `<ns>_compat_legacy_use_total{surface="sse",name=…}`.
4. **One server per stream.** After this PR `grep -rln 'text/event-stream' --include=*.go internal core | grep -v _test`
   lists only the core hub and its projections (files under `core/`); none under `internal/`.
5. **Shutdown.** `Hub.Close` sends `closed{shutdown}` to native streams and projection A's `OnClose("shutdown")` emits
   HEAD's `system.shutdown` frame, so the SPA's shutdown banner still shows.

## Steps

1. Tests first (below).
2. P6: books calls `Registrar.Realtime().Projection(realtime.ProjectionA())` (and B, C) and adds three gin manifest routes
   with the handlers (`AnyAuthenticated`, `NoCompress`), keeping the tracing-skip and gzip-exclusion behaviour.
3. Delete, in this PR only: `internal/books/stream_bridge.go` (+test), `internal/operations/registry/bus.go` and every
   `EventHub` publish, hub A (`internal/realtime/events.go`, `book_changes.go`, `server.hub` and both `Broadcast` sites),
   stream B's `OperationsSSE`, the v3 broker and its route; the status worker's hub-A forward; the observer's coalescer
   call. Fix every resulting compile error by deletion, not by re-adding a server.
4. Compat ledger: three `sse` rows in `docs/compat-surfaces.md` and the embedded `internal/books/compat-surfaces.md`
   (byte-identical; `internal/books/compat_test.go`).
5. `ci/stage.txt`: `shipped_stage: S6`, `release: S6`.
6. `make compat-rehearsal` with cells a (previous release's UI against cur), b (cur's UI against prev stream events) and
   c (ABS conformance against prev and cur).
7. Acceptance; headers; `changelog.d/` fragment.

## Tests (row done-tests; names from the spec)

- The grep of invariant 4 (09 S6 Done when).
- Legacy-stream goldens per event type: `TestLegacyProjection_FramesGolden` (A, B, C) run against the **mounted** routes
  through the real gin engine (extend PC-15-02's test with a mounted-route case).
- `TestProjectionTopicsExistAtStage` over books' real registrations, with the S6 and the S8 catalogs.
- `TestProjection_LazyHandler503UntilServing` (mounted: a request before P8 answers 503 + `Retry-After`).
- `TestS6_StreamsPermissionParity` (name coined here): a Repairs run is not shown on B to a `library.view` subscriber
  without the users permission, C re-checks it, a kind without `LegacyDefID` reaches no stream, A carries the run with
  its labels blanked (the S5 bridge test's assertions, now through the hub).
- R-test cells a, b, c (`make compat-rehearsal`).

## Acceptance

```bash
grep -rln 'text/event-stream' --include=*.go internal core | grep -v _test | grep -c '^internal/'   # expect 0
grep -rln 'text/event-stream' --include=*.go internal core | grep -v _test | grep -c '^core/'       # expect >= 1
test ! -e internal/books/stream_bridge.go && test ! -e internal/operations/registry/bus.go && test ! -e internal/realtime/events.go && echo ok   # expect ok
grep -rn "NewBroker\|OperationsSSE\|registry.EventHub\|realtime.EventHub" --include=*.go internal | grep -v _test | wc -l   # expect 0
grep -n "^shipped_stage: S6$\|^release: S6$" ci/stage.txt | wc -l                              # expect 2
go test ./internal/realtime/ ./internal/server/ ./internal/books/ -run 'TestLegacyProjection_FramesGolden|TestProjectionTopicsExistAtStage|TestS6_StreamsPermissionParity' -count=1   # ok
git diff --stat origin/main -- web/src | tail -1                                              # expect empty (no UI change)
make compat-rehearsal      # exit 0 (cells a, b, c)
make test-e2e              # exit 0 (Activity live progress, Library row patches, shutdown banner)
make -C core ci            # exit 0
make ci                    # exit 0
```

## Out of scope

- Any SPA change (the shell's realtime client replaces the legacy URLs at S9).
- The plugin `EventBus` and webhooks (S7).
- `core.system.status-changed.v1` (S8; never on projection A).

## STOP conditions

- A golden differs between HEAD's implementation and the mounted projection for any event type.
- A frame type HEAD's stream A sends today has no topic in projection A's list (see Planner notes on
  `scan.auto_triggered`): report it in the PR description and get the owner's decision before merging.

## Rollback

Binary swap to the `floor` build (09 S6 Rollback: no row; outbox rows are left unprocessed by S5). A revert PR is the
exact inverse diff.

## Repo rules

- File headers on every changed file; `changelog.d/` fragment without a header; the two compat ledgers byte-identical.
- Commits end with `Stage-Release: S6 flip` then `Co-Authored-By: Claude <noreply@anthropic.com>`; required checks include
  `compat-rehearsal` and `unit-lifecycle` (flip PR); `gh pr merge <n> --rebase`, never squash, never `--admin`;
  `check_soak.py` check (a) needs OWNER-18's current `floor` row ≥ 14 days old.
- `docs/compat-surfaces.md` is shared with contract PRs: no W19 PR touching it may be open.
- After merge: hand off to OWNER-19 (P7 branch-B ABS check, P9 webhooks census).

## Planner notes

- The row omits `internal/realtime/` (hub A's `events.go` and `book_changes.go` are deleted here; PC-15-02 listed the
  package for its projection definitions) and the two compat ledgers, which need the three `sse` rows.
- HEAD's hub A also broadcasts `scan.auto_triggered` (`internal/server/server_lifecycle.go:487` at `748fd787a`), a frame
  05-realtime §2's stream-A inventory does not list and no projection-A topic carries; no SPA code reads it at HEAD, but
  the flip drops it from a compat surface. The spec should say whether it is dropped (a release-note line) or gets a books
  topic.
