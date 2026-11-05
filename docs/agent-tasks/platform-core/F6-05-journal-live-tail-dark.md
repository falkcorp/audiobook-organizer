<!-- file: docs/agent-tasks/platform-core/F6-05-journal-live-tail-dark.md -->
<!-- version: 1.0.0 -->
<!-- guid: 84230bcc-5f4f-40b5-81f4-6534033c49b6 -->
<!-- last-edited: 2026-11-05 -->

# F6-05 — The journal's live tail, dark: `core.log.recorded.v1` delivered through realtime's permission filter with `core:system.manage`, and the event half of `TestJournal_RequiresSystemManage`

| Field | Value |
|---|---|
| ID | F6-05 |
| Release / flip | R2, events flip F6 (dark PR; live at F6-03) |
| Tier | **T2** (Sonnet / high): a permission-filtered live topic over the journal; the topic, its permission and its user-data rule are fixed by the spec |
| Wave | W30 (beside F4-07, F5-13, F7-05, F9-02, F9-04, F9-05; file-disjoint) |
| Depends on | F5-15 (`core/internal/logjournal`, the journal `Tap`), F5-19 (`core/internal/loghttp` and the route half of `TestJournal_RequiresSystemManage`), F6-02 (the realtime hub and its permission filter), F4-04 (the `events` catalog and bus) |
| Owns gate tests | the event half of `TestJournal_RequiresSystemManage` (F5-19 owns the test and its route half) |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f6-05-journal-tail
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F5-15, F5-19, F6-02 and F4-04 are merged. If not, stop. No `go work init` (CLAUDE.md). Run core commands from
inside `core/`.

## Goal

Add the journal's live tail exactly as 05-logging §5 states it:

- **Topic.** `logjournal` (its owner) declares the catalog entry `core.log.recorded.v1` (05-events §5E.3.1: one
  catalog, the owner declares its topic): a bus topic with `Realtime` set (never `Durable`: 05-events §5E.12
  `TestCatalog_RejectsRealtimeOnDurable`), `Permission` `core:system.manage` (05-logging §5 "Live tail"), a `Visible`
  that omits a record the registered `log.OtherUserPredicate` flags unless the subscriber also holds `core:users.view`,
  and a `Redact` that blanks the record's `user_id` for a subscriber without `core:users.view` (05-logging §5, last
  bullet: "The query route and the live tail keep that rule").
- **Publisher.** Every record the journal tap appends is also published on that topic: the same already-redacted
  `log.Record` (05-logging §3 `Record`: "an immutable, already-redacted copy handed to taps"), in the same record view
  F5-19's query route returns, so REST and the stream agree. The tap never blocks (05-logging §6 "Tap queue full":
  fail-open; `TestTap_NeverBlocks`); a bus publish that would block drops and counts, as the bus does (05-events §5E.5.1).
- **Auth off.** `core:system.manage` is `AuthRequired`, so with auth off the tail delivers nothing (05-logging §5
  "Auth-off reach": a deliberate tightening).
- **Test.** The event half of `TestJournal_RequiresSystemManage`: a `core:system.view`-only principal receives no
  `core.log.recorded.v1` event (05-logging §10, "from F6, with realtime").

**Dark:** ABO's kernel creates the hub and registers the topic only at F6-03, which owns `core/kernel/` in its wave.
Until then the SPA's log view polls F5-19's query route (05-logging §5 "When the surface lands"). Tests run on hello and
`coretest` with a hub built in the test.

## Spec

- 05-logging §3 (`Logging.Tap`, `Record`, `OtherUserPredicate`), §5 (Live tail; Auth-off reach; When the surface lands;
  the user-data rule), §6 (Tap queue full), §10 (`TestJournal_RequiresSystemManage`, `TestRedact_CoreInvitePath`'s
  "realtime tail record" cell, `TestTap_NeverBlocks`).
- 05-realtime §4.4 (send-time filtering: `Permission`, then `Visible`, then `Redact`; replay filtered like live), §4.8
  (the client contract the SPA's log view will use from F6-03).
- 05-events §5E.3.1 (`TopicSpec`, `VisibleFunc`, `RedactFunc`, `RealtimeMeta`).
- 05-rbac §6.1 (`core:users.view`, the read-side successor of HEAD's `users.manage` gate).

## Files

| Action | Path |
|---|---|
| create | `core/internal/logjournal/tail.go` (the topic declaration, `Visible`, `Redact`, the publish from the journal tap) |
| create | `core/internal/logjournal/tail_test.go` |
| modify | `core/internal/loghttp/permissions_test.go` (add the event half to `TestJournal_RequiresSystemManage`) |
| create | one `changelog.d/` fragment (headerless) |

Touch no other file of `logjournal` or `loghttp`. If the publish needs a hook in a file F5-15 wrote (the tap's batch
loop), STOP (condition 3).

## Re-verify at BASE

```bash
ls core/internal/logjournal/tail.go 2>/dev/null | wc -l                              # 0
grep -rn 'Tap(' core/internal/logjournal/*.go | grep -v _test | wc -l                # >= 1 (F5-15's journal tap)
grep -rn 'type OtherUserPredicate' core/log/ | wc -l                                 # 1
grep -rn 'func TestJournal_RequiresSystemManage' core/internal/loghttp/ | wc -l      # 1 (F5-19)
grep -ln 'func TestJournal_RequiresSystemManage' core/internal/loghttp/*_test.go     # core/internal/loghttp/permissions_test.go
grep -rn 'type VisibleFunc\|type RedactFunc' core/events/ | wc -l                    # 2 (F4-04)
grep -rn 'func NewHub' core/realtime/ | wc -l                                        # 1 (F6-02)
grep -rn 'core.log.recorded.v1' core/ | wc -l                                        # 0
```

## STOP conditions

1. `TestJournal_RequiresSystemManage` is not in `core/internal/loghttp/permissions_test.go` (F5-19 named another
   file): report the file; the row's file list must follow it.
2. A subscriber without `core:system.manage` could receive a record, or one without `core:users.view` a flagged record
   or a non-blank `user_id`, including on replay (05-realtime §4.4).
3. Publishing from the tap needs an edit of an existing `logjournal` file, or could block the logging caller.
4. Anything in ABO's boot registers the topic or publishes on it before F6-03.

## Steps

1. Write the tests below first.
2. `tail.go`: the `core.log.recorded.v1` catalog entry (owner `logjournal`), `Visible` (the registered
   `OtherUserPredicate`, called with the record and the viewer's user ID from the principal in `ctx`, exactly as
   F5-19's query route calls it; a nil predicate flags nothing; the predicate fails closed by its own contract),
   `Redact` (blank `user_id` without `core:users.view`), and a second tap or a publish from the existing tap's callback,
   whichever F5-15's code allows without editing its files, publishing the record view on the bus.
3. Extend `TestJournal_RequiresSystemManage` in `permissions_test.go` with the event half: a hub built in the test, a
   `core:system.view`-only subscriber on `topic=core.log.recorded.v1` gets 403 `topic_not_permitted` before the stream,
   and on `topic=*` receives no `core.log.recorded.v1` event while a `core:system.manage` subscriber receives it.
4. Headers, the fragment, Acceptance.

## Tests first

- `TestJournal_RequiresSystemManage` (spec name; F5-19's test, event half added here).
- `TestJournalTail_OtherUsersLinesNeedUsersView` (name coined here): the live-tail twin of F5-19's
  `TestJournal_OtherUsersLinesNeedUsersView`: a `core:system.manage` subscriber without `core:users.view` receives no
  flagged record and sees `user_id` blank on the others; with `core:users.view` it receives both; replay after
  `Last-Event-ID` is filtered the same way.
- `TestJournalTail_SameViewAsQuery` (name coined here): a record appended to the journal arrives on the tail with the
  same fields the query route returns for it.
- `TestJournalTail_AuthOffDeliversNothing` (name coined here): with auth disabled the stream refuses the topic.
- `TestJournalTail_TapNeverBlocks` (name coined here; `testing/synctest`): a subscriber that never reads and a full bus
  queue leave 10k log calls unblocked, drops counted.
- The tail cell of `TestRedact_CoreInvitePath` (05-logging §10): an invite token logged under `path` is absent from the
  tail record. If the test lives in `core/log/` (R1-32, F5-15), add the cell as a tail-side assertion in `tail_test.go`
  instead of editing that file.

## Acceptance

```bash
(cd core && go vet ./internal/logjournal/... ./internal/loghttp/...)                 # exit 0
(cd core && go test -race -count=1 ./internal/logjournal/... ./internal/loghttp/...)  # ok
(cd core && go test -count=1 -run 'TestJournal_RequiresSystemManage|TestJournalTail_' ./internal/...)   # PASS
grep -rn 'core.log.recorded.v1' core/kernel/ | wc -l                                  # 0 (dark: F6-03 registers it)
make -C core ci                                                                       # exit 0
make ci                                                                               # exit 0 (last line: ✅ All CI checks passed!)
```

## Out of scope

The kernel registration of the topic and the hub (F6-03); the SPA's log view on the tail (F6-03); the query route and
its other-user rule (F5-19); the journal store, its tap and M5 (F5-15).

## Rollback

Revert the PR; ABO declares and publishes nothing new.

## Repo rules

Worktree only; file headers on every created or modified file (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never
squash, never `--admin`. No goroutine per record: the publish runs on the tap's own goroutine.

```
feat(core/logjournal): live tail core.log.recorded.v1 behind core:system.manage (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
