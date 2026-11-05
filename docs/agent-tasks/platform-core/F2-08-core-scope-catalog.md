<!-- file: docs/agent-tasks/platform-core/F2-08-core-scope-catalog.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4cc96c6b-103a-4ffe-91ed-1c8cfd0c4973 -->
<!-- last-edited: 2026-11-05 -->

# F2-08 — The full `core:*` scope catalog, declared by the kernel: every scope any flip, seed role or atom-table row names

| Field | Value |
|---|---|
| ID | F2-08 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W22 |
| Tier | **T2** (Sonnet / high): a closed list with fixed classes and flags, a small catalog type and one gate test; no design left except where marked |
| Depends on | OWNER-03 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-08-scope-catalog
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md).

## Goal

Add `rbac.PermissionSpec`, `rbac.PermissionClass` and the read-only `rbac.Catalog` (05-rbac §3.2) to the R1 `rbac`
leaf, and have the kernel declare **one fixed list** of every `core:*` permission 05-rbac names, with its class and
flags, whichever core subsystems are wired (05-rbac §5 item 3; IMPL-4), so F4's seed roles, M3's writes and F4's
manifest requirements never name a scope the catalog lacks. Gate test: `TestCoreCatalog_FixedFromF2`. Dark: nothing
enforces with the catalog before F4 (05-http-api §8.1 F2 row).

## Spec to read first

05-rbac §3.1 (`Permission`, grammar), §3.2 (the Go block: copy it), §3.3 (core seed roles), §4.3 (implication),
§4.8 (seed table: `editor`/`viewer` core seeds), §5 item 3, §6.1 (atom table and "New permissions with no atom"),
§6.2, §12 row `TestCoreCatalog_FixedFromF2`; 05-library §5L.3 ("Permissions declared by core"); 05-http-api §8.1 F2 row
(`httpapi.Build` validates route requirements against this catalog before F4 enforces).

## Base state: re-verify before editing

```bash
grep -rn 'type Permission ' core/rbac/                        # 1 hit (R1-12), else STOP
grep -rn 'type RoleName ' core/rbac/                          # 1 hit, else STOP
grep -rn 'type PermissionSpec struct' core/rbac/ | wc -l      # 0 (this PR adds it); 1 = applied, see Idempotency
test ! -e core/rbac/catalog.go && echo absent                 # "absent"
grep -rn 'RoleEditor\|RoleViewer\|RoleAdmin' core/rbac/ | head -3   # 0 or more; this PR does not add the seed constants (F4-01)
grep -rn 'func Build' core/httpapi/ | head -2                 # find httpapi.Build (R1-13)
grep -rn 'Catalog\|catalog' core/httpapi/*.go | grep -v _test | head -5   # does Build already take a catalog? (decides step 4)
# the spec's catalog, regenerated at your base (expect the 19 names below)
grep -oh 'core:[a-z][a-z0-9_.]*[a-z0-9_]' docs/specs/platform-core/05-rbac.md | sort -u
```

The last command lists 22 strings at the plan commit: the 19 below plus `core:events.subscribe`, `core:tasks.run`
(both **dropped** in §6.1's notes) and `core:library` (a prose fragment). If your base lists a `core:*` name not in the
table below and not dropped by the spec, STOP and report it.

## Files

Create `core/rbac/catalog.go`, `core/rbac/catalog_test.go`, `core/kernel/catalog.go`, and `core/kernel/catalog_test.go`
(the kernel half of the gate; see planner note). Modify nothing else, except one call from the kernel's boot sequence
into `core/kernel/catalog.go` (one line in R1's kernel boot file; say so in the PR body).

## Code contract

In `core/rbac/catalog.go`, copy 05-rbac §3.2's Go block verbatim: `PermissionSpec` (all fields: `Name`, `Title`,
`Description`, `Class`, `DefaultGrants`, `Implies`, `Sensitive`, `AuthRequired`, `Confining`, `Warn`, `Deprecated`),
`PermissionClass` (`ClassAction` = zero value, `ClassView`), `RoleSeed`, `Declarer`, `Catalog` with `Lookup`, `All`,
`Namespace`, `Expand`. Add the constructor `func NewCatalog(specs ...PermissionSpec) (*Catalog, error)` (name coined
here) that validates: each `Name` parses as a concrete `Permission`; no duplicate; `Implies` stays in the declarer's
namespace; the implication closure is acyclic (`ErrImpliesCycle`, 05-rbac §3.4 sentinel: declare it here if R1 did
not); a `Confining` scope implies nothing and is implied by nothing. `Freeze`, seed merge and module declarations are
F4-01's (`Service`); do not add them.

In `core/rbac/catalog.go`: `func CoreScopes() []PermissionSpec` (name coined here) returns exactly the 19 specs below, as
a fresh slice on every call (no package-level mutable state, R5). In `core/kernel/catalog.go` the kernel declares them,
whichever subsystem is wired: it builds `rbac.NewCatalog(rbac.CoreScopes()...)` once at boot, keeps the `*rbac.Catalog`, and fails boot
on an error. If the base-state grep shows `httpapi.Build` already validates route requirements against a catalog,
pass this one; if it does not, leave `Build` unchanged (enforcement lands at F4).

The fixed list (05-rbac §6.1 "New permissions with no atom", the atom table's core scopes, §4.8, 05-library §5L.3):

| Name | Class | Flags | Implies | DefaultGrants |
|---|---|---|---|---|
| `core:admin` | Action | `AuthRequired` | — (implies everything by rule §4.3, not by list) | — (the locked `admin` role is outside the merge, §4.8) |
| `core:self` | Action | — | — | `editor`, `viewer` |
| `core:roles.view` | View | — | — | — |
| `core:roles.manage` | Action | `Sensitive` | — | — |
| `core:users.view` | View | — | — | — |
| `core:users.manage` | Action | — | `core:users.view` | — |
| `core:audit.view` | View | `AuthRequired` | — | — |
| `core:system.status` | View | — | — | `editor`, `viewer` |
| `core:system.view` | View | — | — | — |
| `core:system.manage` | Action | `AuthRequired` | — | — |
| `core:backup.restore` | Action | `Sensitive`, `AuthRequired` | — | — |
| `core:integrations.read` | View | — | — | — |
| `core:integrations.manage` | Action | — | `core:integrations.read` | — |
| `core:tasks.view` | View | — | — | `editor`, `viewer` |
| `core:tasks.manage` | Action | — | — | — |
| `core:metrics.read` | View | — | — | `editor`, `viewer` |
| `core:library.manage` | Action | — | — | — |
| `core:library.browse` | Action | — | — | — |
| `core:settings.manage` | Action | — | — | — |

Rules behind the table: classes and flags are the ones §6.1 states; a scope §6.1 does not class is `ClassAction`
(the zero value); `Implies` only where §6.1 writes "implies"; `DefaultGrants` only for the four core seeds of §4.8's
`editor`/`viewer` row (`core:self`, `core:tasks.view`, `core:system.status`, `core:metrics.read`). `RoleName` values
`"editor"` and `"viewer"` are literal strings here (the seed constants arrive with F4-01; use them if they already
exist). `Title` and `Description`: one short line each, written from the scope's purpose in §6.1/§6.2 (for example
`core:system.status` "System status" / "Readiness and maintenance-banner tokens only"). Do not mark any scope
`Confining` (only `books:fingerprint.worker` is, a books scope).

## TDD: tests to write first

- `TestCoreCatalog_FixedFromF2` (05-rbac §12, IMPL-4) in `core/rbac/catalog_test.go` (the row's file, and where F4-01's
  brief looks for it): `rbac.CoreScopes()` holds every one of the 19 scopes with exactly the class and flags above; every core scope written by §6.1's atom table
  (`core:tasks.view`, `core:integrations.manage`, `core:integrations.read`, `core:users.manage`, `core:users.view`,
  `core:roles.view`, `core:settings.manage`, `core:tasks.manage`, `core:system.view`, `core:system.manage`,
  `core:library.manage`, `core:library.browse`, `core:admin`) and by the `editor`/`viewer` seeds is in it; and it holds
  no other `core:` scope.
- `TestKernel_DeclaresCoreScopesWithNoSubsystemWired` (name coined here) in `core/kernel/catalog_test.go`: a kernel booted
  with **no optional core subsystem wired** (the zero-module boot R1's kernel tests use) holds a catalog whose `core`
  namespace equals `rbac.CoreScopes()` exactly. Together the two tests are the spec's "a kernel with no optional core
  subsystem wired declares every `core:*` scope".
- In `core/rbac/catalog_test.go` (names coined here): `TestCatalog_RejectsDuplicate`, `TestCatalog_RejectsForeignImplies`,
  `TestCatalog_RejectsImpliesCycle`, `TestCatalog_ExpandPattern` (`core:users.*` expands to `core:users.manage` and
  `core:users.view`), `TestCatalog_LookupAndNamespace`.

## Steps

1. Base-state greps (including the regenerated scope list); plan in the PR body.
2. Tests first.
3. `catalog.go` types, `NewCatalog` and `CoreScopes()`; kernel construction.
4. Headers; changelog fragment; acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./rbac/... ./kernel/...                                   # exit 0
go test ./rbac/... ./kernel/... -race -count=1                                     # ok
go test ./rbac/ -run TestCoreCatalog_FixedFromF2 -count=1 -v | grep -c '^--- PASS: TestCoreCatalog_FixedFromF2'   # 1
go test ./kernel/ -run TestKernel_DeclaresCoreScopesWithNoSubsystemWired -count=1 -v | grep -c '^--- PASS'          # 1
grep -c '"core:' rbac/catalog.go                                                    # >=19 (one Name per scope; Implies add more)
grep -n 'core:events.subscribe\|core:tasks.run' rbac/catalog.go kernel/catalog.go | wc -l   # 0 (dropped scopes)
cd ..
make -C core ci                                                                    # exit 0
make ci                                                                            # exit 0
```

## Out of scope

`rbac.Service`, `Freeze`, seed merge, `rbac.Store` and module declarations (F4-01); enforcement (F4); books' atom table
(R1-15 owns `TestAtomTable_Golden`); any ABO edit.

## Idempotency / rollback

If `type PermissionSpec struct` exists in `core/rbac/`, run the acceptance instead. Rollback = revert; nothing enforces
the catalog before F4.

## Repo rules

File headers; conventional commit (`feat(core/rbac): fixed core scope catalog declared by the kernel (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment.

## Planner notes

- `TestCoreCatalog_FixedFromF2`'s subject is "a kernel with no optional core subsystem wired" (05-rbac §12), but the row
  puts it in `core/rbac/catalog_test.go`, and `core/rbac` cannot import `core/kernel`. The brief keeps the named test in
  `core/rbac` over `rbac.CoreScopes()` (F4-01's brief greps for it there) and adds the kernel half as a coined test in
  `core/kernel/catalog_test.go`; add that file to the row.
- 05-rbac does not give titles, descriptions, or a constructor for `Catalog`; the brief coins `NewCatalog` and asks for
  one-line titles. `Freeze` (F4-01) will wrap or replace `NewCatalog`; F4-01's brief should say which.
- Shared kernel boot file: see F2-07's planner note (five W22 rows each add one call).
