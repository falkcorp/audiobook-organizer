<!-- file: docs/agent-tasks/platform-core/F5-19-journal-query-route-dark.md -->
<!-- version: 1.0.5 -->
<!-- guid: 4142bda4-22ac-4f63-8d52-07a0db51b22d -->
<!-- last-edited: 2026-11-05 -->

# F5-19 — The journal's query route, dark: `GET /api/core/v1/system/logs` with `core:system.manage` in a capability route package, the other-user rule and the route half of `TestJournal_RequiresSystemManage`

| Field | Value |
|---|---|
| ID | F5-19 |
| Release / flip | R2, tasks flip F5 (dark PR; the route is mounted by F5-18) |
| Wave | W31 |
| Tier | **T2** (Sonnet / high): one permission-checked query route over an existing store, with the other-user rule copied from HEAD; the contract types and both tests are fixed by the spec |
| Depends on | F5-15 (`log.JournalStore`, `core/internal/logjournal`, `logtest`), F4-01 (core rbac: `rbac.Need`, `rbac.Can`, `rbactest`, the `core:*` scopes), F4-02 (core auth: `auth.PrincipalFrom`, `authtest`) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-19-journal-query-route
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` in the worktree (CLAUDE.md: it breaks the build). `core/` is the nested Go module
`github.com/falkcorp/mediacore`; this PR's code is all inside it, so run its tests from inside `core/`
(`cd core && go test ./internal/loghttp/...`).

## Goal

Build, dark, the query half of the system-log journal's HTTP surface (05-logging §5): the route
`GET /api/core/v1/system/logs?level=&module=&since=&until=&q=&cursor=&limit=`, permission **`core:system.manage`**, over
`log.JournalStore.Query`, keeping HEAD's other-user rule. It lives in a new L6 capability route package,
`core/internal/loghttp/` (package name coined here), because the route is "mounted by an L6
`internal/<capability>http` package (03 §3.5 R4), because `logjournal` (L4) cannot mount routes" (05-logging §5,
"When the surface lands").

Nothing mounts it in this PR. The tasks flip F5-18 mounts it on `CoreGroup()`, deletes HEAD's
`GET /api/v1/system/logs` (`internal/server/wire_system_routes.go:31`) in the same PR and moves the SPA's log view
(05-logging §5: "HEAD's route and the native route switch in one PR"). The live tail `core.log.recorded.v1` and the
event half of `TestJournal_RequiresSystemManage` are F6-05's, which extends this PR's test file.

## Spec to read first (in this order)

1. 05-logging §5: the "Query", "Auth-off reach" and "When the surface lands (F5, F6)" bullets, and the "User data
   in system log lines" bullet (the other-user rule).
2. 05-logging §3: `Record`, `JournalStore`, `JournalQuery`, `JournalPage`, `OtherUserPredicate` (copy nothing; use
   them as F5-15 landed them).
3. 05-logging §10: rows `TestJournal_RequiresSystemManage` and `TestJournal_OtherUsersLinesNeedUsersView`.
4. 05-http-api §3 (`Route`, `Handle[Req, Resp]`, `Group`), §3.4 "JSON naming" (snake_case properties and query
   parameters), §3.5 (`Router.CoreGroup()`: "used only by core/internal/<capability>http").
5. 05-rbac §3 (`Need`, `Can`, `CheckerFrom`), §4.11 (auth disabled: an `AuthRequired` scope is never held), §6.1
   (`core:users.view` as the read-side successor of HEAD's `users.manage` gate).
6. 03 §3.3 (`internal/<capability>http` row: L6, imported only by `kernel`), §3.5 R4.

## Base state: re-verify before editing

```bash
cd core
# F5-15's journal (0 hits on any line = STOP)
grep -n 'type JournalStore interface' log/*.go | wc -l                                  # 1
grep -n 'type JournalQuery struct\|type JournalPage struct' log/*.go | wc -l           # 2
grep -n 'type OtherUserPredicate func(rec Record, viewer string) bool' log/*.go | wc -l   # 1
test -d internal/logjournal && test -d log/logtest && echo journal                      # journal
# httpapi, rbac, auth (R1-13, F4-01, F4-02)
grep -rn 'func Handle\[Req, Resp any\](g \*Group' httpapi/*.go | wc -l                  # 1
grep -rn 'func (r \*Router) CoreGroup() \*Group' httpapi/*.go | wc -l                   # 1
grep -rn 'func Need(s Permission) Requirement\|func Can(ctx context.Context, s Permission) bool' rbac/*.go | wc -l   # 2
grep -rn 'func PrincipalFrom(ctx context.Context) (\*Principal, bool)' auth/*.go | wc -l   # 1
grep -rn '"core:system.manage"\|"core:system.view"\|"core:users.view"' rbac/ --include=*.go | grep -v _test | wc -l   # >=3 (the scope catalog)
ls rbac/rbactest/*.go auth/authtest/*.go | wc -l                                         # >=2
# This PR's target is absent, and nothing serves the path yet
test ! -e internal/loghttp && echo absent                                                # absent
grep -rn 'system/logs' --include=*.go . | grep -v _test | wc -l                           # 0
cd ..
# HEAD's route and its rule (ac4368bdb): read them, do not edit them
grep -n 'protected.GET("/system/logs", s.perm(auth.PermSettingsManage), systemH.GetSystemLogs)' internal/server/wire_system_routes.go | wc -l   # 1
grep -n 'func (h \*Handler) GetSystemLogs(c \*gin.Context)' internal/server/handlers/system/handler.go | wc -l   # 1
grep -n 'func NamesOthersUserLog(source, message string) bool' internal/activity/user_data.go | wc -l           # 1
grep -n 'if limit > 1000 {' internal/httputil/parse.go | wc -l                           # 1 (HEAD's page cap)
```

A grep that misses only on formatting (a wrapped signature) is not a STOP: grep the bare symbol (`func Handle[`,
`func PrincipalFrom`) and STOP only if the symbol is absent. If `core/internal/loghttp` already exists with a
passing `TestJournal_RequiresSystemManage`, run the acceptance block instead (Idempotency).

## Files

| Action | Path |
|---|---|
| create | `core/internal/loghttp/routes.go`: `Deps`, `Register`, the request and response types, the handler |
| create | `core/internal/loghttp/routes_test.go`: the parameter-mapping and paging tests (names coined here) |
| create | `core/internal/loghttp/permissions_test.go`: `TestJournal_RequiresSystemManage` (route half) and `TestJournal_OtherUsersLinesNeedUsersView`; F6-05 adds the event half to this file |
| create | one `changelog.d/` fragment |

Do not modify `core/kernel/` (F5-18 mounts the route), `core/openapi/core.json` or the generated client (regenerated
by the mounting row, F5-18), `core/internal/logjournal/` (F5-15's; F6-05 adds the tail), `core/log/`, any ABO file,
`internal/server/` or `web/`. Core's `layerOf` already carries 03 R4's `internal/<capability>http` pattern (R1-01), so
no guard file changes; if `core/archtest`'s layer test rejects the new package at your base, STOP and report.

## Code contract (spec types unchanged; the names below are coined here)

```go
// Package loghttp serves the system-log journal's query route (05-logging §5). L6; imported only by the kernel.
package loghttp

// Deps is what the kernel hands the package at mount time (F5-18).
type Deps struct {
	Journal   log.JournalStore      // core's native store (core/internal/logjournal), never nil
	OtherUser log.OtherUserPredicate // the module-registered predicate (Registrar.LogOtherUserData, 06); nil = no module registered one
}

// Register adds the one route to g, which must be the router's CoreGroup(). It returns the registration error.
func Register(g *httpapi.Group, d Deps) error

type queryRequest struct { // query parameters, snake_case (05-http-api §3.4)
	Level  string `query:"level"`  // "", debug, info, warn, error: JournalQuery.MinLevel ("" = debug)
	Module string `query:"module"` // JournalQuery.Module ("" = all)
	Since  string `query:"since"`  // RFC 3339; JournalQuery.Since (absent = zero)
	Until  string `query:"until"`  // RFC 3339; JournalQuery.Until (absent = zero)
	Q      string `query:"q"`      // JournalQuery.Text
	Cursor string `query:"cursor"` // JournalQuery.Cursor, from a previous page's next
	Limit  int    `query:"limit"`  // JournalQuery.Limit: default 50, clamped to [1, 1000] (05-logging §5)
}

type recordView struct { // one journal record on the wire
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"`     // slog level name, lower case
	Module    string            `json:"module"`
	Subsystem string            `json:"subsystem"`
	Message   string            `json:"message"`
	Attrs     map[string]string `json:"attrs"`     // dotted keys, as the native store flattens them
	TraceID   string            `json:"trace_id,omitempty"`
	SpanID    string            `json:"span_id,omitempty"`
}

type pageView struct {
	Records []recordView `json:"records"`
	Next    string       `json:"next"` // "" when exhausted
}
```

Use the request-binding mechanism `httpapi.In[Req]` provides at your base (grep `type In\[` in `core/httpapi`); the
struct tags above show the parameter names, which are fixed by 05-logging §5, `limit` included (default 50, cap
1000, clamped: the same default and cap as HEAD's `ParsePaginationParams`).

The route (05-logging §5; 05-http-api §3):

| `Route` field | Value |
|---|---|
| `Method`, `Path` | `http.MethodGet`, `"/system/logs"` (on `CoreGroup()`, so the full path is `/api/core/v1/system/logs`) |
| `Name` | `listSystemLogs` (name coined here; lowerCamel, unique in core) |
| `Perm` | `rbac.Need("core:system.manage")` |
| `RateClass` | `RateDefault` |
| everything else | zero |

The handler, in this order:

1. Parse the parameters. A malformed `level`, `since`, `until` or a `limit` that is not an integer answers
   `400` through `httpapi`'s problem type (no partial query).
2. `page, err := d.Journal.Query(ctx, q)`; an error answers `500` (the store error is logged, never echoed).
3. **The other-user rule** (05-logging §5 "User data in system log lines"): `viewer` = the request principal's user
   ID (`auth.PrincipalFrom(ctx)`); `mayRead := rbac.Can(ctx, "core:users.view")`. For each record: if `mayRead`,
   keep it unchanged; else if `d.OtherUser != nil && d.OtherUser(rec, viewer)`, **omit** it; else keep it with the
   `user_id` attribute **blanked** (present, value `""`). The predicate runs on every record, imported and new
   alike (the books predicate classifies imported records by subsystem and message, as HEAD's
   `activity.NamesOthersUserLog(source, message)` does; that predicate is books' code and arrives through `Deps`).
4. Answer `200` with `pageView{Records, Next: page.Next}`. Omitted records are not counted anywhere on the wire
   (HEAD's "the total counts only what is served").

Auth-off reach needs no code here: `core:system.manage` is `AuthRequired` (05-rbac §4.11), so with auth off the
router's chain answers `401` before the handler runs. That is the deliberate tightening 05-logging §5 names; do not
add an auth-off branch.

## TDD: tests to write first

Spec tests (05-logging §10, names copied exactly), in `permissions_test.go`:

| Test | Proves (this PR's half) |
|---|---|
| `TestJournal_RequiresSystemManage` | runs now: the registered route's `Perm` is `rbac.Need("core:system.manage")`, and a `core:system.manage` principal bound through `authtest` and `auth.WithPrincipal` gets `200`. The stage-13 table (a `core:system.view`-only principal gets `403` on `GET /api/core/v1/system/logs`; no principal gets `401`) is written now and each cell calls `t.Skip("needs stage 13: F4-08")`; F5-18, which mounts the route after F4-08, deletes the skips. Keep a `// F6-05: the event half (no core.log.recorded.v1 events) is added here` comment where F6-05 adds its rows |
| `TestJournal_OtherUsersLinesNeedUsersView` | over a journal holding imported records (from F5-15's R0-fixture importer path, `logtest` or `logjournal`'s test helper at your base) and new records: a `core:system.manage` principal **without** `core:users.view` gets neither the imported nor the new records the books-style predicate flags, and sees `user_id` blank on the rest; **with** `core:users.view` it gets every record with `user_id` intact |

Coined tests (names coined here), in `routes_test.go`:

- `TestLogRoute_ParamsMapToJournalQuery`: each query parameter lands in the matching `JournalQuery` field (a
  recording fake `JournalStore`); `limit` absent → 50, `0` → 50, `5000` → 1000; a bad `since` → 400 and no query.
- `TestLogRoute_CursorPagesNewestFirst`: over `logjournal`'s native store with 120 records and `limit=50`, three
  pages chained by `next` return all 120 newest first with no duplicate, the last page's `next` is `""`.
- `TestLogRoute_AuthOffAnswers401`: runs now: the route's `Perm` is `rbac.Need("core:system.manage")` and the catalog
  marks that scope `AuthRequired`. Its booted cell (auth disabled, the request answers `401`) is a table cell that
  calls `t.Skip("needs stage 13: F4-08")`, like the permission table above.

**Why the skips.** Stage 12 (binding the principal) and stage 13 (route permission and guards) arrive with the identity
flip F4-08 (W34), after this row (W31); until then `coretest`'s `As`, `WithUser` and `AssertPermission` fail "needs core
authentication" (R1-37, completed by F4-08). With no stage 13 running, a request without the permission gets `200`,
so the `403` and `401` cells cannot pass here. Do **not** add a permission check inside the handler to make them pass
(enforcement in two places, which F4-08's stage 13 would duplicate): the check is the router's. Test the handler's own
logic (parameters, paging, the other-user rule) with a principal bound through `authtest`'s helpers and
`auth.WithPrincipal` on the request context (grep `^func ` in `core/rbac/rbactest/` and `core/auth/authtest/` at your
base), driving the route through a `httpapi.Router` built for the test with `Register(router.CoreGroup(), deps)`.

Write them first, watch them fail to compile, then implement.

## Steps

1. Base checks; STOP on a mismatch.
2. Write the five tests.
3. `routes.go`: types, `Register`, the handler (steps 1–4 above).
4. `cd core && go test ./internal/loghttp/... -race -count=1` green; then the acceptance block.
5. PR description: the route row (path, perm, parameters, response shape), the other-user rule as implemented, and
   "unmounted until F5-18".

## Acceptance (commands and expected output)

```bash
cd core
go build ./... && go vet ./internal/loghttp/                                       # exit 0
go test ./internal/loghttp/ -race -count=1                                          # ok
go test ./internal/loghttp/ -race -count=1 -v -run 'TestJournal_RequiresSystemManage$|TestJournal_OtherUsersLinesNeedUsersView$|TestLogRoute_' | grep -c '^--- PASS'   # 5
grep -c 't.Skip("needs stage 13: F4-08")' internal/loghttp/permissions_test.go internal/loghttp/routes_test.go | awk -F: '{s+=$2} END {print (s>=1)}'   # 1 (the stage-13 cells, un-skipped by F5-18)
go list -deps ./internal/loghttp/ | grep -c 'falkcorp/audiobook-organizer'          # 0 (core holds no ABO import)
grep -rn 'loghttp' --include=*.go kernel/ | wc -l                                   # 0 (unmounted: F5-18 mounts it)
make ci                                                                            # exit 0 (core's)
cd ..
git diff --name-only origin/main | grep -v '^changelog.d/' | sort                  # exactly the three core/internal/loghttp/ files
make ci                                                                            # exit 0
```

## STOP conditions

- `log.JournalStore` or `log.OtherUserPredicate` is missing or differs from 05-logging §3 at your base (F5-15 not
  merged, or merged with another shape).
- `core:system.manage`, `core:system.view` or `core:users.view` is not in core's scope catalog.
- `httpapi` has no way to register a route on `CoreGroup()` from an `internal/<capability>http` package without
  editing `core/httpapi` or the kernel.
- `core/archtest`'s layer test rejects `core/internal/loghttp`.

## Out of scope

Mounting the route, regenerating `core/openapi/core.json` and the client, deleting HEAD's `GET /api/v1/system/logs`
and moving the SPA's log view (all F5-18); the live tail `core.log.recorded.v1` and the event half of
`TestJournal_RequiresSystemManage` (F6-05); the books predicate itself (books registers it through
`Registrar.LogOtherUserData`, F5-18 wires it); any change to the journal store (F5-15).

## Idempotency / rollback

If `core/internal/loghttp/routes.go` exists and the acceptance passes, stop: the work is done. Rollback = revert the
PR; nothing imports the package before F5-18.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task (CLAUDE.md "Worktree Discipline").
- File headers on every created file (`.standards/instructions/file-headers.md`): fresh `guid`
  (`python3 -c 'import uuid;print(uuid.uuid4())'`), version 1.0.0, `last-edited` the day you edit.
  `changelog.d/` fragments carry **no** header.
- One `changelog.d/` fragment (`scriv create`, or a hand-written Markdown file); no hand edit of `CHANGELOG.md`.
- Conventional commit `feat(core/log): the journal's query route GET /api/core/v1/system/logs (dark)`; the body ends
  with `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1), so add none.
- `gh pr create`, then `gh pr merge <n> --rebase` once every required check is green on the PR and in the merge queue; never
  squash, never `--admin` past a required check.
- CLAUDE.md concurrency rules: the handler pages through `Query` with a bounded `Limit` and holds no per-request
  fan-out; the predicate loop is over one page (at most 1000 records), not the journal.
- Status report at the end: exact counts, then `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`,
  `BLOCKED: <n> — <list>` (CLAUDE.md).
