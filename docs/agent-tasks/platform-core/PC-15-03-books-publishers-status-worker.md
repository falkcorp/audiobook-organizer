<!-- file: docs/agent-tasks/platform-core/PC-15-03-books-publishers-status-worker.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6fc01818-d5ef-4641-8a4b-83b410bcc31e -->
<!-- last-edited: 2026-11-04 -->

# PC-15-03 — Books publishers on the bus: the 12 legacy event types, books.book.changed.v1 and the books.system.status-ticked.v1 worker

| Field | Value |
|---|---|
| ID | PC-15-03 |
| Tier | **T2 Sonnet/high** — event mapping with a fixed payload; the frames must stay byte-shape-equal |
| Wave | W15 (S6, release `S6`; live PR before the flip) |
| Depends on | PC-15-02 |
| Serialized | PC-15-01 → PC-15-02 → **PC-15-03** → PC-15-04 |
| Trailer | `Stage-Release: S6 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 "Books-side (ABO)" paragraph, §5 rules 3 and 5, footnotes [3], [4] |
| Owning spec | 05-realtime §4.7 (topic table), §4.9 (projection A by stage); 09 S6 "Moves"; 05-events §5E.13 (Order step 4), §5E.5.1 (publish timing, coalescing); 05-notify §10 (the 12 legacy types move at S7) |

## Goal

Books declares and publishes its two S6 realtime topics on the bus:

- `books.book.changed.v1` (permission `books:view`, payload `{kind: created|updated|deleted, id}`, `Key` = book ID,
  `Realtime{Coalesce: 250ms}`), published from the book-change observer after every committed book write;
- `books.system.status-ticked.v1` (`Realtime`, permission `books:view`, payload unchanged
  `{books, folders, memory_alloc, goroutines, timestamp}`), published every 5 s by a new books worker that takes over
  HEAD's ticker (`internal/server/server_lifecycle.go:404-441` at `748fd787a`), including the Prometheus gauge updates
  that ticker makes today.

Until the flip PC-15-04 swaps stream A to projection A, HEAD's hub A must keep receiving exactly what it receives today,
so this PR **adds** the bus publishes beside the existing hub-A sends; PC-15-04 deletes the hub-A sends with hub A.

**The 12 legacy event types are not moved here** (see Planner notes): 05-events §5E.13 Order step (4) and 05-notify §10
keep the plugin `EventBus` feeding the webhook plugin, unmoved, through S6; their publishers move at S7 (PC-16-06).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-15-03" -b feat/pc-15-03-books-publishers origin/main
cd "$REPO/../audiobook-organizer-pc-15-03"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create:

- `internal/books/status_worker.go` — a `Registrar.Worker` (or the books module's equivalent long-running component)
  ticking every 5 s.
- `internal/books/status_worker_test.go`.

Modify:

- `internal/books/module.go` — `Registrar.Topics`: declare both topics with the specs above; register the worker.
- `internal/server/server_lifecycle.go` — delete HEAD's 5-second ticker goroutine (HEAD lines ~404-441: `time.NewTicker(5
  * time.Second)` through `s.hub.SendSystemStatus(...)`); the worker does the same work.
- `internal/operations/registry/bus.go` — **no change** (see Planner notes). If you find you must touch it, STOP.

Also modify (not in the inventory row; see Planner notes): `internal/server/server.go` — the
`database.SetBookChangeObserver` callback (HEAD `server.go:1088-1090`) additionally publishes `books.book.changed.v1`.

## Re-verify greps (identical at `748fd787a` and the plan checkout)

```bash
grep -n "ticker := time.NewTicker(5 \* time.Second)" internal/server/server_lifecycle.go          # expect 1 (~:404)
grep -n "s.hub.SendSystemStatus(map\[string\]any{" internal/server/server_lifecycle.go             # expect 1 (~:436)
grep -c '"books":\|"folders":\|"memory_alloc":\|"goroutines":\|"timestamp":' internal/server/server_lifecycle.go   # expect >= 5 (HEAD payload keys)
grep -n "metrics.SetBooks(bc)\|metrics.SetFolders(folderCount)\|metrics.SetMemoryAlloc(alloc.Alloc)\|metrics.SetGoroutines(runtime.NumGoroutine())\|s.updateSearchIndexMetrics()" internal/server/server_lifecycle.go | wc -l   # expect 5 (the gauge updates that move with the ticker)
grep -n "libraryCountTick%12 == 0" internal/server/server_lifecycle.go                             # expect 1 (counts re-read once a minute)
grep -n "database.SetBookChangeObserver(func(kind database.BookChangeKind, id string) {" internal/server/server.go   # expect 1 (~:1088)
grep -n 'const EventBooksChanged EventType = "books.changed"' internal/realtime/book_changes.go      # expect 1
grep -n "const BooksChangedMaxIDs = 500" internal/realtime/book_changes.go                          # expect 1
grep -rn "func Publish\[" core/events --include=*.go | wc -l                                         # PC-10-01; expect 1
grep -rn "func (r \*Registry) .*Projection\|Projection(p LegacyProjection)" core/realtime --include=*.go | wc -l   # PC-15-02; expect >= 1
grep -rn '"books:view"' internal/books --include=*.go | wc -l                                        # expect >= 1 (books' permission exists since S4); 0: STOP
```

Note: at `748fd787a` the store package is `internal/database`; since PC-07-04 it is `internal/books/store` (the call is
`store.SetBookChangeObserver` or the moved name). Grep for `SetBookChangeObserver` and use what you find.

## Steps

1. Tests first (below).
2. Topics in `internal/books/module.go` through `Registrar.Topics` (05-events §5E.3.1), names exactly
   `books.book.changed.v1` and `books.system.status-ticked.v1`, permission `books:view`, both `Realtime`; the book-change
   topic sets `Coalesce: 250 * time.Millisecond` with the "deleted supersedes updated, created stays created" merge
   (05-events §5E.5.1). Typed payload structs (names coined here): `BookChanged{Kind string "kind"; ID string "id"}` and
   `StatusTicked{Books int "books"; Folders int "folders"; MemoryAlloc uint64 "memory_alloc"; Goroutines int
   "goroutines"; Timestamp int64 "timestamp"}` with exactly HEAD's JSON names and value types.
3. Book changes: in the observer callback, keep `bookChanges.Add(...)` (hub A until the flip) and add
   `events.Publish(d.Events, ctx, BooksBookChanged, id, BookChanged{Kind: string(kind), ID: id})` after it. Publish is
   non-blocking and happens after the commit (the observer runs after the write returns).
4. Status worker: move the ticker loop body verbatim into `status_worker.go`: every 5 s read `runtime.MemStats`; every
   12th tick re-read `CountPrimaryBooks` and `GetAllImportPaths` and call `metrics.SetBooks`/`SetFolders`; every tick call
   `metrics.SetMemoryAlloc`, `metrics.SetGoroutines`, the search-index metrics update; publish
   `books.system.status-ticked.v1` with the payload; **and**, until PC-15-04, call hub A's `SendSystemStatus` with the
   identical map so stream A is unchanged. Stop on the worker's context (shutdown), as HEAD's loop stops on `shutdown`.
5. Delete HEAD's ticker goroutine from `server_lifecycle.go`.
6. Acceptance; headers; `changelog.d/` fragment.

## Code contract

```go
// 05-events §5E.3.2
func Publish[T any](p Publisher, ctx context.Context, t Topic[T], key string, payload T)
// payloads (05-realtime §4.7)
books.book.changed.v1         -> {"kind": "created"|"updated"|"deleted", "id": "<book id>"}, Key = book ID
books.system.status-ticked.v1 -> {"books": n, "folders": n, "memory_alloc": n, "goroutines": n, "timestamp": unix}
```

## Tests to write first

- Row done-test: "`system.status` frames byte-shape-equal to S5's" — `TestStatusWorker_FrameShapeEqualsHEAD` (name
  coined here, in `status_worker_test.go`): with a fake clock and fake counts, the hub-A frame the worker sends and the
  projection-A frame built from the published topic (PC-15-02's `ProjectionA().Frame`) have the same JSON keys, value
  types and order as HEAD's golden captured in PC-15-02.
- `TestStatusWorker_CountsOncePerMinute` (coined): `CountPrimaryBooks` is called on ticks 0, 12, 24 only.
- `TestStatusWorker_GaugesUpdated` (coined): the five gauge updates happen each tick as at HEAD.
- `TestBookChanged_PublishedPerCommittedWrite` (coined): one book create, update, delete each publish one
  `books.book.changed.v1` with the right `kind` and `Key`; the hub-A coalescer still receives them.
- `TestBookChanged_BulkWriteBounded` (coined): 10 000 book updates inside one 250 ms window reach a projection-A
  subscriber without `reset{overflow}` (hub coalescing per key). If it fails, STOP (see Planner notes).
- `TestProjectionTopicsExistAtStage` (PC-15-02's) now passes with books' real registrations in addition to its fixture.

## Acceptance

```bash
go build ./... && go vet ./internal/books/... ./internal/server/...                      # exit 0
go test -race ./internal/books/ -run 'TestStatusWorker_|TestBookChanged_' -count=1 -v | grep -c '^--- PASS'   # expect 5
go test ./internal/realtime/ -run TestProjectionTopicsExistAtStage -count=1              # ok
grep -n "ticker := time.NewTicker(5 \* time.Second)" internal/server/server_lifecycle.go | wc -l   # expect 0
grep -rn "books.system.status-ticked.v1\|books.book.changed.v1" internal/books --include=*.go | grep -v _test | wc -l   # expect >= 2
git diff --stat origin/main -- internal/operations/registry/bus.go internal/plugin | tail -1   # expect empty
make ci                    # exit 0
make -C core ci            # exit 0
```

## Out of scope

- The 12 legacy plugin event types and their publishers, the plugin `EventBus`, the webhook plugin (S7, PC-16-06).
- Deleting hub A, `BookChangeCoalescer`, `registry.EventHub`, the v3 broker or the S5 bridge (PC-15-04).
- `core.system.status-changed.v1` (05-health, S8; never added to projection A).

## STOP conditions

- `TestBookChanged_BulkWriteBounded` overflows: HEAD's stream A batches up to 500 ids per `books.changed` frame
  (`BooksChangedMaxIDs`), while the spec's topic carries one `id` per event; report before changing the payload.
- Any need to edit `internal/operations/registry/bus.go` or `internal/plugin/`.

## Rollback

Binary swap. The topics carry no durable state; hub A keeps receiving everything it did, so stream A is unchanged on
either side of a rollback.

## Repo rules

- File headers on every new or changed file; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S6 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`
  after required checks; never squash; `check_soak.py` needs the `floor` row.
- CLAUDE.md concurrency: one worker goroutine; the once-a-minute count stays once a minute (it is a full scan when the
  memdb is not serving it).

## Planner notes

- The row's title says "the 12 legacy event types" and lists `internal/operations/registry/bus.go`, but 05-events §5E.13
  Order step (4) and 05-notify §10 move the 12 plugin event types' publishers at S7, through `Deps.Notify`, in the PR that
  deletes `internal/plugin` (PC-16-06); `bus.go` is `registry.EventHub` (stream B), which the S6 flip deletes. 09 S6
  "Moves" still lists `bus.go → events` with the 12 topics, which conflicts with those two sections. This brief moves
  neither; the row title and file list should drop them, or the spec should say what S6 does with the 12 topics.
- The book-change observer callback is in `internal/server/server.go`, not in the row's list.
- HEAD's `books.changed` frame batches up to 500 ids per kind per 250 ms window; 05-realtime §4.7 gives
  `books.book.changed.v1` a per-book payload with per-key coalescing. A bulk write then yields one event per book, which
  can overflow a projection-A subscriber's 128-entry queue, and stream A has no reset frame. The spec should settle the
  payload (per book, or `{kind, ids}` batches) before PC-15-04 relies on it.
