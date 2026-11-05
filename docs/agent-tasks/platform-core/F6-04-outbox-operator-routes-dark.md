<!-- file: docs/agent-tasks/platform-core/F6-04-outbox-operator-routes-dark.md -->
<!-- version: 1.0.2 -->
<!-- guid: 939e1586-1e99-43dd-b555-319f2d5b85cb -->
<!-- last-edited: 2026-11-05 -->

# F6-04 — Outbox operator routes, dark: `GET /api/core/v1/outbox/dead`, a dead letter's retry and discard, and a consumer's backlog purge, all audited with `core:system.manage`, the purge also interactive

> **SPEC-GATED.** Step 0 below checks one piece of spec text before any code is written. If it is missing, STOP and
> report; do not invent the names.

| Field | Value |
|---|---|
| ID | F6-04 |
| Release / flip | R2, events flip F6 (dark PR; mounted at F6-03) |
| Tier | **T2** (Sonnet / high): audited, interactive routes that replay or discard messages; every route, permission and guard is fixed by the spec |
| Wave | W29 (beside F3-07, F5-01 … F5-12, F5-19, F7-07, F9-01; file-disjoint) |
| Depends on | F6-01 (the outbox and its store), F4-01 (`rbac`), F4-02 (`auth`, the principal and its method), F2-07 (`audit.Logger`) |
| Owns gate tests | none of 09's gate list; the route tests below (names coined here) |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f6-04-outboxhttp
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F6-01, F4-01, F4-02 and F2-07 are merged. If not, stop. No `go work init` (CLAUDE.md). Run core commands from
inside `core/`.

## Step 0 (SPEC-GATE): the audit action names

05-events §5E.11 says these routes are **audited**. 05-rbac §7.1 is the normative action table ("other files use these
names") and carries the `core.outbox.*` row (write path sync): `dead_retry` and `dead_discard` (consumer, seq, topic),
and `backlog_purge` (consumer, messages advanced past). The three audit actions are therefore `core.outbox.dead_retry`,
`core.outbox.dead_discard` and `core.outbox.backlog_purge`.

```bash
REPO_SPEC=docs/specs/platform-core
awk '/^### 7.1 /,/^### 7.2 /' "$REPO_SPEC/05-rbac.md" | grep -c '`core.outbox.\*`'      # must be >= 1
```

- **1 or more** (it is 1 at the spec's current text): check that the row names `dead_retry`, `dead_discard` and
  `backlog_purge`, use them as `core.outbox.<action>`, and continue with step 1.
- **0:** STOP. Report: "F6-04 is spec-gated: 05-rbac §7.1 has no `core.outbox.*` row naming the audit actions of the
  05-events §5E.11 dead-letter retry, dead-letter discard and backlog-purge routes." Write no code.

The `GET` list is a routine read and is not audited (05-rbac §7.1, last paragraph).

## Goal

Create the route package `core/internal/outboxhttp` (package name coined here; 03 §3.3 `internal/<capability>http`
row; layer L6 by 03 §3.5 R4), registering on `httpapi.Router.CoreGroup()` the four operator routes of 05-events
§5E.11:

| Route | Permission and guards (05-events §5E.11) |
|---|---|
| `GET /api/core/v1/outbox/dead` | `core:system.manage` |
| `POST /api/core/v1/outbox/dead/{consumer}/{seq}/retry` | `core:system.manage`, audited |
| `DELETE /api/core/v1/outbox/dead/{consumer}/{seq}` | `core:system.manage`, audited |
| `DELETE /api/core/v1/outbox/consumers/{consumer}/backlog` | `core:system.manage` + `Guards.Interactive`, audited |
| `GET /api/core/v1/outbox/consumers` | `core:system.view` (lag, pending, dead count, intake backlog; disabled modules' consumers with their retained backlog) |
| `GET /api/core/v1/events/topics` | `core:system.view` (the frozen topic catalog) |

Semantics (05-events §5E.5.2): dead letters live at `c/outbox/dead/<consumer>/<topic>/<seq20>` and "stay until an
operator retries or discards them"; a consumer name is bound to one topic (a second `Handler` with one name on another
topic is refused, `TestOutbox_DuplicateConsumerNameRefused`), so `{consumer}` plus `{seq}` names one dead letter. A
retry hands the message back to its consumer for delivery; a discard deletes the dead letter. The purge advances that
consumer's cursors to the head of its topic; it is "the only way a disabled module's messages are discarded" (§5E.11,
§5E.5.2 Retention). The `core.outbox` health component stays Degraded while any dead letter exists (§5E.5.2 Retry), so
a retry or discard of the last one clears it.

**Dark:** nothing mounts the package in ABO. The kernel mounts it at F6-03, which owns `core/kernel/kernel.go` in its
wave. Tests drive the routes through `coretest`'s harness and hello.

## Spec

- 05-events §5E.11 (the route table: copy it), §5E.5.2 (the `c/outbox/` key table, Retry, Retention), §5E.12
  (`TestOutbox_RetryDeadLetter`, `TestOutbox_DisabledConsumerRetainsUntilReenableOrPurge`, which F6-01 owns at the
  `outbox` level).
- 05-http-api §3.1 (`Route`, `Guards.Interactive`, `Handle`, `CoreGroup()`; a zero `Perm` is a registration error),
  §4.2 stage 13 (permission and guards).
- 05-rbac §3.5 (`audit.Logger.Record` or `RecordIn`, `audit.Event`), §7.1 (the action table; step 0).
- 03 §3.3 (`internal/<capability>http`: imported only by `kernel`), §3.5 R4 (L6).

## Files

| Action | Path |
|---|---|
| create | `core/internal/outboxhttp/` (`routes.go`, `routes_test.go`; package `outboxhttp`) |
| create | one `changelog.d/` fragment (headerless) |

Touch nothing else. If `core/outbox/` or `core/internal/outboxstore/` lacks an operation this package needs, STOP
(condition 2): F6-01 owns those files.

## Re-verify at BASE

```bash
ls -d core/internal/outboxhttp 2>/dev/null | wc -l                                  # 0
grep -rn 'package outbox$' core/outbox/*.go | wc -l                                  # >= 1 (F6-01)
grep -rn 'package outboxstore' core/internal/outboxstore/*.go | wc -l                # >= 1 (F6-01)
grep -rn 'c/outbox/dead\|"dead/"' core/internal/outboxstore/ | wc -l                 # >= 1 (the dead-letter rows)
grep -rniE 'func .*(Dead|Retry|Discard|Purge|Backlog)' core/outbox/ core/internal/outboxstore/ | grep -v _test   # the operations F6-01 exposed; read them
grep -rn 'func (r \*Router) CoreGroup' core/httpapi/ | wc -l                         # 1 (R1-13)
grep -rn 'Interactive bool' core/httpapi/ | wc -l                                    # 1
grep -rn 'func Need' core/rbac/ | wc -l                                              # 1 (F4-01)
grep -rn 'type Logger interface' core/audit/ | wc -l                                 # 1 (F2-07)
```

## STOP conditions

1. Step 0 fails.
2. F6-01's packages expose no way to list dead letters, retry one, discard one, or advance a consumer's cursors to the
   head (the re-verify grep above shows none of the four): report which is missing; do not edit `core/outbox/` or
   `core/internal/outboxstore/`.
3. A route would register with a zero `Perm`, or the purge without `Guards.Interactive`.
4. A state-changing route could succeed while its audit record fails to write (`audit.Logger.Record` is fail-closed:
   05-rbac §3.5, "the caller must fail the operation").
5. Anything in ABO's boot imports or mounts `outboxhttp` in this PR.

## Steps

1. Step 0.
2. Write the tests below first.
3. Declare in `outboxhttp` the narrow consumer interface the routes call (name coined here: `Operator`), with one
   method per operation F6-01 exposed (list dead letters with a cursor and limit, retry one, discard one, purge a
   consumer's backlog), so the kernel hands the package F6-01's implementation and nothing wider (03 §3.3: route
   packages receive interfaces from the kernel, as `modulehttp` receives `ModulesView`).
4. `func Register(g *httpapi.Group, op Operator, audit audit.Logger)` (name coined here) registers the four routes with
   `httpapi.Handle` and the `Route` values of the Goal table (`Name`s lowerCamel and unique, e.g. `listDeadLetters`,
   `retryDeadLetter`, `discardDeadLetter`, `purgeConsumerBacklog`; names coined here).
5. Handlers: path values `{consumer}` (a declared consumer name) and `{seq}` (decimal `uint64`; 400 on a bad value);
   404 problem when the dead letter or the consumer does not exist; on success write the audit record with the route's action
   (`core.outbox.dead_retry`, `core.outbox.dead_discard`, `core.outbox.backlog_purge`), `Outcome: audit.Success`,
   `Target` naming the consumer, `Details` the topic and seq (purge: the number of messages advanced past; never the
   payload: 05-rbac §7.7), and only then answer (retry and discard 204; purge 200 with the number of messages skipped).
6. Headers, the fragment, Acceptance.

## Tests first (names coined here)

- `TestOutboxHTTP_ListDeadNeedsSystemManage`: `core:system.view` only → 403; `core:system.manage` → 200 with the dead
  letters of a fixture consumer (consumer, topic, seq, attempts, error token; no payload).
- `TestOutboxHTTP_RetryRedeliversAndAudits`: a dead letter retried → the consumer receives it again, the dead row is
  gone, one audit record with action `core.outbox.dead_retry`.
- `TestOutboxHTTP_DiscardDeletesAndAudits`: discard → no dead row, no delivery, one audit record with action `core.outbox.dead_discard`; the `core.outbox`
  health component is no longer Degraded when it was the last dead letter.
- `TestOutboxHTTP_PurgeNeedsInteractive`: an API-key principal holding `core:system.manage` → refused by the guard; an
  interactive session → 200, the consumer's cursors at the head, a disabled module's retained backlog gone, one audit
  record with action `core.outbox.backlog_purge`.
- `TestOutboxHTTP_AuditFailureFailsRequest`: an `audit.Logger` fake whose `Record` errors → the request fails and the
  state is unchanged.
- `TestOutboxHTTP_UnknownConsumerOrSeq404`.
- `TestOutboxHTTP_ConsumersAndTopicsNeedSystemView`: no principal → 401; `core:system.view` → 200 on both read routes; the consumers view lists a disabled module's consumer with its retained backlog.

## Acceptance

```bash
(cd core && go vet ./internal/outboxhttp/...)                                       # exit 0
(cd core && go test -race -count=1 ./internal/outboxhttp/...)                       # ok
(cd core && go test -count=1 -run 'TestOutboxHTTP_' ./internal/outboxhttp/...)      # PASS
grep -rn 'internal/outboxhttp' --include=*.go . core | grep -v '^core/internal/outboxhttp' | wc -l   # 0 (dark: nothing imports it yet)
(cd core && go test -count=1 -run 'TestCoreImportsOnlyCore' ./archtest/...)          # ok
make -C core ci                                                                     # exit 0
make ci                                                                             # exit 0 (last line: ✅ All CI checks passed!)
```

## Out of scope

- Mounting the routes, the kernel wiring and the OpenAPI regeneration (F6-03).
- Any change to the outbox's delivery, retry or retention rules (F6-01).

## Rollback

Revert the PR; nothing imports the package.

## Repo rules

Worktree only; file headers on every new file (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer (09 §9.2.1 names none);
`gh pr merge <n> --rebase`, never squash, never `--admin`. The list route pages with a cursor and a bounded limit; no
handler loops over a whole topic in one request.

```
feat(core/outboxhttp): dead-letter list, retry, discard and backlog purge routes (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Planner notes

None.
