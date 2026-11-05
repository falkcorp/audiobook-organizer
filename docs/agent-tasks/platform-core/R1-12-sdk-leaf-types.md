<!-- file: docs/agent-tasks/platform-core/R1-12-sdk-leaf-types.md -->
<!-- version: 1.0.0 -->
<!-- guid: 399ee337-063e-4053-bb5f-a69a8711c119 -->
<!-- last-edited: 2026-11-05 -->

# R1-12 — SDK leaf types: the rbac leaf (Permission, Requirement, Need, AllOf, AnyOf, Public, AnyAuthenticated, Delegated, LegacyAtom, LegacyCheck, Checker, RoleName), the auth leaf, the ui leaf, internal/jsonschemax

| Field | Value |
|---|---|
| ID | `R1-12` |
| Tier | **T2** (Sonnet / high): dependency-free leaf types whose names other packages bind to |
| Release / wave | R1 / W05 |
| Depends on | R1-01 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 06 §6.4.10 (R1 row and the rbac/auth leaf paragraph); 05-rbac §3.1; 04 §8.1 R1 |
| Branch | `agent/platform-core-r1-12-sdk-leaf-types` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/rbac/`
- `core/auth/`
- `core/ui/`
- `core/internal/jsonschemax/`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-12" -b agent/platform-core-r1-12-sdk-leaf-types origin/main
cd "$REPO/.worktrees/platform-core-r1-12"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Land the dependency-free SDK leaf types that later R1 packages bind to (06 §6.4.10, the R1 row and its preceding
paragraph; 05-rbac §3.1; 04 §8.1 R1 "the `rbac` leaf types", "the `auth` leaf types", "`ui.ReservedSegments()` and
the dependency-free leaf types", "`internal/jsonschemax` (the one reflector)"). Types only: no store, no checker
implementation, no middleware. Nothing in ABO changes.

| package (layer, 03 R4) | members in this PR (exact names from the spec) |
|---|---|
| `core/rbac` (L4; leaf part) | `Permission` with `ParsePermission`, `ParsePattern`, `Namespace`, `Action`, `IsWildcard`; `CoreAdmin`; `Requirement` (zero value invalid) with `IsZero`, `Permissions`, `String`; `Public`, `AnyAuthenticated`; `Need`, `AllOf`, `AnyOf`, `Delegated`; `LegacyAtom(atom)` and `LegacyCheck(atom, readSide)` as values that **only carry the atom** (04 §8.1 R1; their evaluation lands at F4); the `Checker` interface (`Has`, `Satisfies`, `Confined`, `Concrete`); `RoleName` and the three seed-role constants `RoleAdmin`, `RoleEditor`, `RoleViewer` (05-rbac §3.1 and §3.2 text at the `RoleName` declaration). |
| `core/auth` (L4; leaf part) | `UserID`, `UserStatus` (and its constants), `User`, `Method` (with `Interactive`, `MayChangeCredentials` and the core method constants), `Assurance`, `Principal`, `MethodSpec`, plus `WithPrincipal`/`PrincipalFrom` (05-authn §3: the type block at lines ~100–200 and `MethodSpec` at ~496). 06 §6.4.10 lists the leaf as `MethodSpec`, `Principal`, `User`, `UserID`, `UserStatus`, `Assurance`; 04 §8.1 adds `Method`; take both lists' union. |
| `core/ui` (L6; leaf part, one file) | `ReservedSegments() []string`, `ModuleManifest`, `UIIDs` (07 §7.6 code block around line 875: exactly those three; `WriteManifestTS` and the rest of `ui` land with the shell work, so **not** here); **no `Redirect`**: 04 §8.1 R1 still names `ui.Redirect`, but 07 deleted it with D13's "no old-URL redirects" (07 review history, round 13), so it is not created. |
| `core/internal/jsonschemax` (L0) | `Overrides` with `With`, `For[T]`, `Base()` (05-config §1.10.2 code block, around line 956): `Base()` is the only owner of `time.Time`; both are functions returning fresh maps (03 R5). |

Copy every signature and doc comment from the spec text exactly; where a type's full definition references a member
that lands later (for example a method that needs a store), keep the type and drop that member, and list each drop in
the PR (06 §6.4.10: "a member exists from the release that ships the package it names; before that it is absent, not
stubbed").

## Dependency check

R1-01 merged (`test -f core/go.mod && test -d core/archtest && echo ok`).

## Re-verify (spec anchors; expected in this checkout)

```bash
S=docs/specs/platform-core
grep -n '^type Permission string\|^func Need(\|^func Delegated(\|^type Requirement struct' $S/05-rbac.md      # expect 4 hits in §3.1
grep -n '^type RoleName string\|^type Checker interface' $S/05-rbac.md                                         # expect 207 and 305
grep -n '^type UserID string\|^type Principal struct\|^type MethodSpec struct\|^type Assurance uint8' $S/05-authn.md   # expect 4
grep -n '^func ReservedSegments() \[\]string\|^type ModuleManifest struct\|^type UIIDs struct' $S/07-ui-shell.md       # expect 3
grep -n '^package jsonschemax' -A8 $S/05-config.md | grep -c 'func '                                           # expect 3 (With, For, Base)
grep -n 'jsonschema-go' $S/13-toolchain.md | head -1                                                            # expect the v0.4.3 row
test ! -e core/rbac && test ! -e core/auth && test ! -e core/ui && echo NEW                                    # expect NEW
```

## Steps

1. Tests first (names coined here unless the spec names them): `TestParsePermission_Grammar` (ns
   `[a-z][a-z0-9]{1,15}`, action and sub `[a-z][a-z0-9_]{0,31}`, one sub level, wildcards only through
   `ParsePattern`), `TestRequirement_ZeroIsInvalid`, `TestRequirement_String` (`"books:edit"`, `"public"`,
   `"authenticated"`, `"all(a,b)"`), `TestLegacyAtom_CarriesAtomOnly` (the value reports its atom and is not zero;
   no evaluation API exists), `TestMethod_Classification` (only `MethodSession` is interactive among core methods;
   `MethodAuthDisabled` is non-interactive and may not change credentials; unknown methods neither), `TestReservedSegments_FreshSlice`
   (two calls return equal, distinct slices), `TestBase_OwnsTimeOnly`, `TestOverrides_DuplicateTypePanics`.
2. Implement the four packages. `rbac` and `auth` import only the standard library; `auth` may import `rbac`
   (same layer) for `Checker`/`RoleName` if the spec's `Principal`/`User` name them. `ui` imports nothing above L6.
   `internal/jsonschemax` imports `github.com/google/jsonschema-go` at **v0.4.3** (13 §13.5.2 table; pinned).
3. Module files: `core/go.mod` gains `github.com/google/jsonschema-go v0.4.3`; run `go -C core mod tidy`, then root
   `go mod tidy` (03 §3.2 item 4: the root carries the result in the same PR); `check_module_skew.py` must pass. Add
   the pin `--pin github.com/google/jsonschema-go@v0.4.3` to the `check_module_skew.py` call in root `make ci` and a
   Dependabot `ignore` for that module (13 §13.5.2 "Dependencies that must not float"). Add the module to
   `TestCoreImportsOnlyCore`'s allowlist file. These files are outside the row's list (see Planner notes). R1-02, in the same wave, also edits `Makefile`: merge this PR after R1-02 and rebase onto it before editing `Makefile`.
4. Core layering guard: give the four packages their layers (rbac L4, auth L4, ui L6, internal/jsonschemax L0).
5. Header bumps; fragment `changelog.d/<today>_sdk_leaf_types.md` (`### Added`: "Internal: core SDK leaf types for
   permissions, identity, UI manifests and the JSON Schema reflector."). Commit:
   `feat(core): rbac, auth and ui leaf types and internal/jsonschemax`.

## Code contract

Exactly the spec signatures named in Goal (05-rbac §3.1, 05-authn §3, 07 §7.6, 05-config §1.10.2); `LegacyAtom(atom string) Requirement` and `LegacyCheck(atom string, readSide bool) Requirement` take 04 §8.1's parameter lists.

## Acceptance

```bash
go -C core build ./... && go -C core test ./rbac/ ./auth/ ./ui/ ./internal/jsonschemax/ -count=1   # expect ok x4
git grep -n 'func init()\|^var [A-Z][A-Za-z]* = \(map\|\[\]\)' -- core/rbac core/auth core/ui core/internal/jsonschemax | wc -l   # expect 0 (03 R5)
python3 scripts/ci/check_module_skew.py --pin github.com/google/jsonschema-go@v0.4.3                # expect exit 0
go build ./...                                                     # expect exit 0 (root tidy result committed)
make ci && make -C core ci                                         # expect exit 0
```

## Out of scope

`rbac.Store`, the scope catalog, `Freeze`, the checker implementation, seed-role writers (F2/F4); `auth.Service`,
`Identities`, `Admin` (F4); the rest of `ui` (F9); any ABO change.

## Rollback

Revert the PR.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.

## Planner notes

- Missing files in the row: `internal/jsonschemax` brings core's first pinned third-party module
  (`github.com/google/jsonschema-go` v0.4.3, 13 §13.5.2), so the PR must also change `core/go.mod`, `core/go.sum`,
  root `go.mod`/`go.sum` (tidy coupling, 03 §3.2 item 4), the `check_module_skew.py --pin` call in `Makefile` and a
  Dependabot `ignore` in `.github/dependabot.yml`, plus `TestCoreImportsOnlyCore`'s allowlist. None is in the row.
  W05's other rows (R1-02 edits `Makefile`!) collide on `Makefile`: R1-02 adds `vet-darwin` there. Either move the
  pin wiring to R1-02/R1-03 or serialize R1-12 after R1-02.
- `rbac.LegacyAtom`/`LegacyCheck` have no signature in 05-rbac's main text (only 04 §8.1's `LegacyAtom(atom)`,
  `LegacyCheck(atom, readSide)` and review-history lines); the brief takes 04's parameter lists. `ui.Redirect` is
  named in 04 §8.1 R1, but 07 removed it in round 13 (D13: no old-URL redirects); 04 §8.1's mention is stale and
  should be deleted. The brief does not create it.
