<!-- file: docs/agent-tasks/platform-core/F4-09-core-internal-authz-privileged-checkers.md -->
<!-- version: 1.0.1 -->
<!-- guid: 99822a5a-94d2-4caa-8a8b-67cdcf901790 -->
<!-- last-edited: 2026-11-05 -->

# F4-09 — `core/internal/authz`: `authz.Compile` (injected into rbac as `Deps.Compile`), the privileged checkers `authz.AuthDisabled` and `authz.ForSystem`, and `archtest.NoSystemPrincipalOutsideKernel`

> **SPEC-GATED.** Step 0 checks two pieces of 05-rbac text: §3.4's `CompileInput` (how a role's patterns reach
> `authz.Compile`, as `RolePatterns`) and §3.1's `Requirement.Kind()`. If step 0 finds either missing, stop and report;
> do not invent the path.

| Field | Value |
|---|---|
| ID | F4-09 |
| Release / flip | R2, identity flip F4 (dark PR: nothing calls these functions in a running app before F4-08 injects `authz.Compile` at boot) |
| Tier | **T3** (Opus, high): the two privileged checkers bypass role resolution, and the compiled checker decides every permission check |
| Wave | W24 |
| Depends on | F4-01 |
| Owns gate tests | none of 09's list; owns `archtest.NoSystemPrincipalOutsideKernel` and the checker tests of 05-rbac §12 named below |
| Gate | Plan, then execute autonomously in your own worktree and PR. Do not merge with `--admin` past a required check. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-09-authz
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-01 is merged. If not, stop.

## Goal

Create `core/internal/authz` (layer L4, "the implementation of `rbac`", 05-rbac §3; 04 key "F4 create
mediacore/internal/authz"):

1. `authz.Compile`, the compiled checker of 05-rbac §4.4 and §4.10, which the kernel injects as `rbac.Deps.Compile`
   (F4-08 does the injection; F4-01's `rbac.Service.Compile` is the cache in front of it);
2. the two privileged checkers that bypass role resolution, `authz.AuthDisabled` (05-rbac §4.11) and `authz.ForSystem`
   (the `_system` principal of background tasks, 05-rbac §3.4);
3. the core architecture guard `archtest.NoSystemPrincipalOutsideKernel` (05-rbac §12): the two privileged
   constructors are called only from `auth`, the `tasks` executor (`core/internal/executor`, 05-tasks §5T.3) and
   `kernel`, and `rbac` imports nothing from `authz`.

`authz` imports `rbac`; `rbac` never imports `authz` (05-rbac §3, COR4-12 in its text). Go's `internal/` rule keeps
`authz` out of app code. F4-02 builds `auth` on these functions; F4-08 injects `Compile` at boot.

## What the spec fixes (why step 0 is a check)

05-rbac §3.4 fixes `Deps.Compile func(CompileInput, *Catalog) Checker`. `CompileInput` embeds `CheckerInput{Roles
[]RoleName, Scopes []Permission, Interactive bool}` and adds `RolePatterns []Permission`, the union of the known roles'
patterns, which `rbac.Service.Compile` resolves from its role cache (at the current `Generation`, unknown role IDs
skipped and counted) before it calls `Deps.Compile`. `authz.Compile` never reads the store (`c/rbac/role/<name>`) or the
role cache itself (§4.4). §3.1 gives `Requirement` a `Kind() RequirementKind` (`KindPublic`, `KindAuthenticated`,
`KindAllOf`, `KindAnyOf`, `KindDelegated`; `KindInvalid` for the zero value), so `authz` can tell a requirement's kind
from outside `rbac`. Step 0 checks that both are in the spec.

## Spec (read before coding)

- 05-rbac §3 (package intro: `authz` is L4, imports `rbac`, injected as `Deps.Compile`), §3.1 (`Permission`,
  `Requirement` with `Kind()`, `Need`, `AllOf`, `AnyOf`, `Public`, `AnyAuthenticated`, `Delegated`, `CoreAdmin`), §3.2 (`Catalog`,
  `PermissionSpec` flags `Sensitive`, `AuthRequired`, `Confining`, `Implies`), §3.4 (`Checker`, `CheckerInput`, `CompileInput`,
  "Privileged checkers are not SDK", "A module's system actor"), §4.2 (wildcards), §4.3 (implication; `core:admin`
  implies everything subject to §4.4), §4.4 (effective permissions, key intersection, the admin flag rule, confinement
  on the effective set, the three-step `Has` order, the cache key), §4.10 (`AnyAuthenticated`; confinement: a
  `Confined()` principal satisfies a requirement only when it names one of its scopes literally), §4.11 (auth disabled:
  every concrete scope that is neither `AuthRequired` nor `Sensitive`, `admin=false`, no implication from `core:admin`,
  non-interactive; the auth-off evaluation rule for `AllOf`), §9 (checkers immutable), §12 (tests).
- 03 §3.3 (`core/internal/...` rows; `internal/*` takes the layer of the package it implements), R4 (layers).
- 05-tasks §5T.3 (`core/internal/executor` is the executor package).

## State at BASE

| Path | Created by | What you rely on |
|---|---|---|
| `core/rbac/` leaf | R1-12 | `Permission`, `Requirement` (`IsZero`, `Kind`, `Permissions`, `String`), `Checker` interface, `RoleName` |
| `core/rbac/catalog.go` | F2-08 | `PermissionSpec`, `Catalog` with `Lookup`, `All`, `Namespace`, `Expand` |
| `core/rbac/service.go` | F4-01 | `CheckerInput`, `CompileInput`, `Service.Compile` resolving `RolePatterns` and calling `Deps.Compile(in, s.Catalog())` |
| `core/archtest/` | R1-01 | the shared loader (`go list -e -deps -test -json` over the tag-set union) |

## Files

| Action | Path |
|---|---|
| create | `core/internal/authz/` (`Compile`, `AuthDisabled`, `ForSystem`, the checker type, tests) |
| create | `core/archtest/nosystemprincipal.go` (the guard) |
| create | `core/archtest/nosystemprincipal_test.go` (the guard's self-test with fixtures, and its run over core) |
| create | `core/rbac/testdata/match_vectors.json` (the matching vectors 05-rbac §4.10 places there, shared later with vitest in `@mediacore/shell`) |

Not yours: every other file under `core/rbac/` (F4-01), `core/auth/` (F4-02), `core/kernel/` (F4-08).

## Re-verify at BASE (record every count)

```bash
ls -d core/internal/authz 2>/dev/null | wc -l                                       # expect 0
ls core/archtest/nosystemprincipal.go 2>/dev/null | wc -l                           # expect 0
grep -rn 'type CheckerInput struct' core/rbac/ | wc -l                              # expect 1 (F4-01)
grep -rn 'type CompileInput struct' core/rbac/ | wc -l                              # expect 1 (F4-01; carries RolePatterns)
grep -rn 'Compile .*func(CompileInput, \*Catalog) Checker' core/rbac/ | wc -l      # expect 1 (Deps.Compile, F4-01)
grep -rn 'type Checker interface' core/rbac/ | wc -l                                # expect 1 (R1-12)
grep -rn 'func (c \*Catalog) Lookup\|func (c \*Catalog) Expand' core/rbac/ | wc -l  # expect 2 (F2-08)
grep -rn 'mediacore/internal/authz' core/rbac/ | wc -l                              # expect 0
# how authz tells a Requirement's kind (Need/AllOf/AnyOf/Public/AnyAuthenticated/Delegated) from outside rbac
grep -n '^func (r Requirement) Kind() RequirementKind' core/rbac/*.go | wc -l       # expect 1 (see STOP 2)
ls core/rbac/testdata/match_vectors.json 2>/dev/null | wc -l                        # expect 0
ls -d core/internal/executor 2>/dev/null | wc -l                                    # 0 or 1 (F2's executor); the guard allows it either way
```

## STOP conditions

0. Step 0 does not find `CompileInput` with `RolePatterns` (05-rbac §3.4) and `Requirement.Kind()` (§3.1) in the spec.
1. Anything here would make `rbac` import `authz`, or would require editing a `core/rbac/` file other than the new
   `testdata/match_vectors.json`.
2. `rbac.Requirement` has no exported `Kind()` at BASE (the re-verify grep prints 0), or `Deps.Compile` still takes a
   bare `CheckerInput`: F4-01's files do not match 05-rbac §3.1 and §3.4. Do not parse `String()`. Report: `core/rbac/`
   (F4-01's) needs `Kind()` or `CompileInput`.
3. A privileged checker would hold `core:admin`, or `AuthDisabled` would hold an `AuthRequired` or `Sensitive` scope.

## Steps

0. **Spec check.** Run:
   ```bash
   sed -n '/^type CompileInput struct/,/^}/p' docs/specs/platform-core/05-rbac.md
   grep -c 'func (r Requirement) Kind() RequirementKind' docs/specs/platform-core/05-rbac.md
   ```
   Expected: the struct prints the embedded `CheckerInput` and `RolePatterns []Permission` (05-rbac §3.4: the union of
   the known roles' patterns, resolved by `Service.Compile`), and the count is `1` (§3.1). Proceed only on exactly
   that. If the struct is absent or has no `RolePatterns`, or the count is `0`, STOP and report that the spec is behind
   this brief; do not invent the path. If a later spec change alters `CompileInput`, `CheckerInput` or `Catalog`, the
   change is in `core/rbac/` and belongs to a row that owns that file, not to this one.
1. Write the tests below first (they fail).
2. **`Compile`** (§4.4): from `in.RolePatterns` (already resolved by `rbac.Service.Compile`; read no store or role cache) build an immutable checker holding a hash set of concrete scopes
   (implication closures from the catalog precomputed in), the namespace wildcards, the action wildcards, the `admin`
   flag and the `interactive` bit. With key scopes (`in.Scopes != nil`) the checker is the intersection: a scope is
   held iff the roles hold it and the key names it, after implication; the `admin` flag is set iff the roles hold
   `core:admin` **and** the key names it. `Has(s)`: (1) `s` is `Sensitive` and not interactive → false, even with
   admin; (2) admin → true; (3) exact, `ns:*` or `ns:action.*` → true. `Confined()`: the concrete set is non-empty and
   every member is `Confining` (an admin is never confined), decided on the effective set. `Concrete()`: expanded
   against the catalog. `Satisfies(r)`: `Public` → true; `AnyAuthenticated` → holds `core:self` or any non-`Confining`
   scope, never for a `Confined()` principal; `Need`/`AllOf`/`AnyOf` (kinds read through `Requirement.Kind()` and `Permissions()`, never `String()`) over `Has`, except that a `Confined()` principal
   satisfies only a requirement that **names** one of its scopes literally (no implication, §4.10); `Delegated` →
   false (enforced by the mounted handler, 05-http-api §8.2); the zero `Requirement` → false.
3. **`AuthDisabled(cat)`** (§4.11): every concrete catalog scope that is neither `AuthRequired` nor `Sensitive`;
   `admin=false`; no implication from `core:admin`; non-interactive; never `Confined()`. Its `Satisfies` alone applies
   the auth-off evaluation rule: for an `AllOf` it drops the `AuthRequired` members and requires the rest; a requirement
   all of whose scopes are `AuthRequired` is refused.
4. **`ForSystem(perms)`** (§3.4): a checker holding exactly the given concrete scopes, non-interactive, no admin flag.
   The kernel passes the union of `Perms.Start` of the module's own kinds plus `Manifest.TaskStarts`, never `core:admin`
   or an `AuthRequired` scope (that computation is the kernel's, 05-tasks §5T.3.10). The signature carries no catalog:
   record in the PR how implication and `Confined()` behave for it (design latitude; the invariants below hold).
5. **Guard** (`core/archtest/nosystemprincipal.go`): `func NoSystemPrincipalOutsideKernel(t testing.TB)` (signature
   coined here; the guard's name is the spec's). Using R1-01's loader over core's tag-set union, fail when (a) any
   non-test file outside `github.com/falkcorp/mediacore/auth`, `.../internal/executor`, `.../kernel` and `authz` itself
   calls `authz.AuthDisabled` or `authz.ForSystem` (AST scan of selector calls on the import), or (b) `.../rbac` or any
   of its sub-packages imports `.../internal/authz`. The self-test runs the guard over two fixture trees (one allowed,
   one with a forbidden call and a forbidden import) and over the real core module.
6. **Vectors** (`core/rbac/testdata/match_vectors.json`): a JSON array of cases `{roles patterns, key
   scopes or null, interactive, catalog flags, check, expected}` covering wildcards, implication, key intersection,
   admin, `Sensitive` before admin, confinement and `AnyAuthenticated`. `TestChecker_MatchVectors` runs every case
   through `Compile`.
7. Bump headers, add one `changelog.d/` fragment (no header), run Acceptance.

## Code contract (copy exactly; 05-rbac §3.4)

```go
package authz

func Compile(in rbac.CompileInput, cat *rbac.Catalog) rbac.Checker   // the value of rbac.Deps.Compile
func AuthDisabled(cat *rbac.Catalog) rbac.Checker
func ForSystem(perms []rbac.Permission) rbac.Checker
```

The returned checkers implement `rbac.Checker` (`Has`, `Satisfies`, `Confined`, `Concrete`) and are immutable (05-rbac
§9). The checker type is unexported.

## Tests first (05-rbac §12 names; coined names marked)

`TestChecker_MatchVectors` (runs the vectors file), `TestCompile_KeyNarrowsRoles` (incl. a wildcard role ∩ a concrete
key), `TestAdmin_IsScopeAndKeyScopingApplies` (the checker cell: an admin user's `books:view` key does not satisfy
`Need(core:admin)`; the 403 route cell runs with the live mount at F4-08), `TestSensitive_EvaluatedBeforeAdmin` (a
bootstrap-style `["core:admin"]` key: `Has(core:roles.manage)` false; an interactive admin session: true),
`TestAuthDisabled_CheckerHasNoAdmin` (no `AuthRequired` or `Sensitive` scope, admin false, `Has(core:system.manage)`
and `Has(core:audit.view)` false), `TestWorkerKey_Confined` (this package's checker cells, beside F4-01's seed cells in
`rbac`: an admin-owned key scoped `["books:fingerprint.worker"]`, an `fp-worker` user's key and an `fp-worker` session
are `Confined()` and do not satisfy `core:self`, `core:metrics.read`, `books:reading` or `AnyAuthenticated`, and do
satisfy `Need(books:fingerprint.worker)`; F4-08 adds the route cells), `archtest.NoSystemPrincipalOutsideKernel` (step
5). Coined here: `TestAuthDisabled_AllOfDropsAuthRequired` (an `AllOf` of seven ordinary scopes plus
`core:system.manage` is satisfied; an `AllOf` of `AuthRequired` scopes only is refused; the rule does not apply to a
`Compile` checker), `TestForSystem_HoldsExactlyGivenPerms` (holds the given scopes, nothing else, non-interactive, no
admin), `TestChecker_Immutable` (`-race`: concurrent reads of one checker; `Concrete()` returns a copy).

## Invariants

- `rbac` imports nothing from `authz`; only `auth`, `core/internal/executor` and `kernel` call the two privileged
  constructors.
- The auth-disabled principal never holds `core:admin`, an `AuthRequired` or a `Sensitive` scope.
- A key's checker is role ∩ key; its admin flag needs the key to name `core:admin`.
- A `Confined()` principal satisfies only requirements naming its scopes literally; never `AnyAuthenticated`.
- Checkers are immutable; no package-level state (R5).

## Acceptance (commands and expected output)

```bash
(cd core && go vet ./internal/authz/... ./archtest/...)                                         # exit 0
(cd core && go test -race -count=1 ./internal/authz/... ./archtest/...)                         # ok for each package
(cd core && go test -count=1 -run 'TestChecker_MatchVectors|TestCompile_KeyNarrowsRoles|TestAdmin_IsScopeAndKeyScopingApplies|TestSensitive_EvaluatedBeforeAdmin|TestAuthDisabled_|TestWorkerKey_Confined|TestForSystem_' ./internal/authz/...)   # PASS
(cd core && go test -count=1 -run 'NoSystemPrincipal' ./archtest/...)                           # PASS
(cd core && go list -deps ./rbac/... | grep -c 'mediacore/internal/authz')                      # 0
make -C core ci                                                                                 # exit 0
make ci                                                                                         # exit 0
```

## Out of scope

The role store, service and seeds (F4-01); `auth` and its use of the privileged checkers (F4-02); injecting `Compile`
at boot and the system actor's permission union (F4-08, F5); the route cells of `TestWorkerKey_Confined` and
`TestAdmin_IsScopeAndKeyScopingApplies` (F4-08); `TestAuthOff_SettingsManageKeepsReach`, an ABO test (F4-08).

## Rollback

Revert the PR. Nothing calls `authz` in a running app before F4-08.

## Repo rules

- Worktree only; never edit `main` or the primary checkout; never run `go work init`.
- File header (file, version, guid, last-edited) on every created file; the JSON vectors file carries none if JSON
  cannot hold comments: follow the convention of the repo's other JSON testdata at BASE. `changelog.d/` fragments carry
  no header.
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip
  trailer: add none.
- `gh pr merge <n> --rebase` once every required check is green on an up-to-date branch; never squash, never
  `--admin`.

```
feat(core/authz): compiled checker, auth-disabled and system checkers, NoSystemPrincipalOutsideKernel (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
