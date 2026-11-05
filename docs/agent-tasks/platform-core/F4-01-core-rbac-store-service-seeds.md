<!-- file: docs/agent-tasks/platform-core/F4-01-core-rbac-store-service-seeds.md -->
<!-- version: 1.1.2 -->
<!-- guid: 59c67fbd-15a4-478a-a671-a3cd8a9542d5 -->
<!-- last-edited: 2026-11-05 -->

# F4-01 — Core rbac, dark: `rbac.Store` native over `c/rbac/`, `rbac.Importer`, `rbactest`, `Service.Compile`, the four seed roles seeded by core, the ambient rule, `Require`

| Field | Value |
|---|---|
| ID | F4-01 |
| Release / flip | R2, identity flip F4 (dark PR) |
| Tier | **T4** (Opus, xhigh–max): the authorization engine and the ambient rule |
| Wave | W25 |
| Depends on | F2-08, F2-07, F4-04 |
| Owns gate test | `TestWorkerKey_Confined` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-01-core-rbac
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F2-08, F2-07 and F4-04 are merged. If not, stop.

## Goal

Complete `github.com/falkcorp/mediacore/rbac` on top of R1's leaf types (R1-12) and F2's scope catalog (F2-08): the
`Service` of 05-rbac §3.4 (catalog assembly with `Declarer`/`Freeze`, roles CRUD with escalation and last-admin rules,
`CanAssign`, `Dominates`, `Compile` and the checker cache, `Generation`, `Can`/`Require`/`RequireAll` with denial
audit), the SDK `Store` and `Importer`, the native store `core/internal/rbacstore` over `c/rbac/`, the conformance kit
`rbac/rbactest` (`StoreSuite` with the importer cases, `ReachSnapshot`, `RowRuleSuite` with a synthetic fixture), the
**seed merge** of the four seed roles (`admin` locked, `editor`, `viewer`, `fp-worker`) at `Freeze`, and the
confinement rule (`Confined()`). `Service.Compile` is the cache in front of the injected `Deps.Compile`; the compile
body, the compiled checker and the two privileged checkers are `core/internal/authz`, which **F4-09** builds in the
next wave on top of this package (05-rbac §3, §3.4: `authz` imports `rbac`, never the reverse). Dark: nothing in ABO
uses it before F4-08; hello may.

## Spec

- 05-rbac §3.1–§3.4 (all types; copy the `Service`, `Deps`, `CheckerInput`, `Checker`, `Store`, `Importer` and error
  blocks exactly), §4 (grammar, wildcards, implication, effective permissions, key scopes, admin as a permission, roles
  CRUD rules incl. rule 5 dominance, **§4.8 seed merge**, §4.9 declarations, §4.10 enforcement and `Confining`, §4.11
  auth-off), §5 (lifecycle), §6.2 (seed role IDs kept), §6.3 ("Native store", the ambient scopes), §8, §9, §10, §11,
  §12 (tests).
- 05-backup §5B.3.2 (`journal.RevocationJournal`: role unassign / narrow / delete lines appended into the change's
  batch before it commits).
- 04 §8.1 F4 row; 06 §6.4.10 F4 row; 06 §6.5.1 P5 (`rbac.New`, `Declarer` per manifest) and P7 (seed roles reconciled,
  default grants seeded, through rbac's store).

## Files

| Action | Path |
|---|---|
| create | `core/rbac/store.go` (SDK `Store`, `Importer`, `ErrImportRejected`) |
| create | `core/rbac/service.go` (`Service`, `Deps`, `Options`, CRUD, `CanAssign`, `Dominates`, `Compile`, `Can`/`Require`) |
| create | `core/rbac/seeds.go` (core seed table, seed merge, `PendingSeed`, `AcceptSeed`) |
| create | `core/rbac/rbactest/` |
| create | `core/internal/rbacstore/` |
| not yours | `core/internal/authz/` (F4-09), the kernel injection of `authz.Compile` (F4-08) |

## Re-verify at BASE

```bash
grep -rn 'type Permission\b\|type Permission string' core/rbac/ | wc -l     # expect 1 (R1-12)
grep -rn 'type Checker interface' core/rbac/ | wc -l                        # expect 1 (R1-12 leaf)
grep -rn 'func LegacyAtom\|LegacyCheck' core/rbac/ | wc -l                  # expect >= 1 (R1-12)
ls core/rbac/catalog.go | wc -l                                             # expect 1 (F2-08)
grep -rn 'TestCoreCatalog_FixedFromF2' core/rbac/ | wc -l                   # expect 1 (F2-08)
grep -rn 'core.role.create\|core.role.update_seed' core/audit/ | wc -l      # expect >= 2 (F2-07's action table)
grep -rn 'type RevocationJournal interface' core/backup/journal/ | wc -l    # expect 1 (F4-04)
ls -d core/internal/authz 2>/dev/null | wc -l                               # expect 0 (F4-09 creates it after you)
ls core/rbac/store.go core/rbac/service.go core/rbac/seeds.go 2>/dev/null | wc -l   # expect 0
# HEAD's rules you port (HEAD f7ac5b11e)
grep -n 'func ' internal/auth/seed.go | head -5                             # HEAD seed rewrite (seed.go:80-129)
sed -n 265,298p internal/server/middleware/auth.go | grep -c 'worker'      # >= 1: HEAD's workerOnly confinement
```

## STOP conditions

1. Anything here would create `core/internal/authz/` or construct an auth-disabled or system checker: those are
   F4-09's, and `rbac` never names them (05-rbac §3.4 "Privileged checkers are not SDK").
2. `rbac` would import `authz`, `auth`, `events` or `audit` beyond `audit.Logger` (rbac never imports `events`: the
   kernel publishes `core.rbac.changed.v1` from `Deps.OnChange`).
3. Anything changes ABO's authorization before F4-08.

## Steps

1. Write the tests below first.
2. `Store`/`Importer` and `core/internal/rbacstore`: key `c/rbac/role/<name>`, value `codec.JSON[Role](1)`; every
   write takes the caller's `*storage.Batch` and stages an `Expect` on the role's `Version`; `ScanRoles` yields
   `ErrUndecodableRole` per bad row. `ImportRole` stores a custom role (`OriginCustom`, name `custom.<slug>`) with its
   native permissions and refuses an unknown scope, a non-`custom.` name or a seed name with `ErrImportRejected`,
   staging nothing.
3. `Service`: `Declarer(moduleID)` and `Freeze` (validation of §4.9, then the seed merge of §4.8); CRUD with escalation
   (`ErrEscalation`), dominance (`ErrDominance`), last admin (`ErrLastAdmin`), seed roles read-only except
   `AcceptSeed` (`ErrSeedRoleLocked`), `admin` locked (`ErrLockedRole`); each change appends its revocation line
   (`role.unassigned`, `role.narrowed`, `role.deleted`) through `Deps.Revocations` into the change's batch before it
   commits, with its audit record in the same batch; `OnChange(generation)` after commit.
4. `Compile(CheckerInput)`: cached by role set + scopes + interactive + generation; unknown role IDs skipped and counted;
   the body calls `Deps.Compile(in, s.Catalog())`. Key-scope narrowing (role ∩ key), `Sensitive` before admin and
   `Confined()` are the compiled checker's semantics, built in `authz.Compile` by F4-09. Test the cache with a
   recording `Deps.Compile` (counts calls; a generation bump invalidates; unknown roles are counted).
5. **Seeds** (§4.8 table): `admin` always `{core:admin}`, outside the merge; `editor` and `viewer` with `core:self`,
   `core:tasks.view`, `core:system.status`, `core:metrics.read` (plus module default grants); `fp-worker` from books'
   `RoleSeed` with no non-`Confining` scope; `SeedPermissions`, `SeedDigest`, `PendingSeed` for restricted additions
   (a `Sensitive`, `AuthRequired` or `Confining` scope, or any `*:settings.manage` / `*:integrations.manage`, added to
   an existing role). Seed writes happen at P7 through the store, never in `New` or a constructor.
6. `rbactest`: `StoreSuite` (round trip, `Expect` on version, undecodable row, the importer cases), `ReachSnapshot` and
   `RowRuleSuite` with a synthetic fixture (books' frozen reach snapshot is ABO data, F4-07).
7. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract (copy exactly; 05-rbac §3.4)

```go
func New(ctx context.Context, opts Options, deps Deps) (*Service, error)
type Deps struct {
	Store       Store
	Compile     func(CheckerInput, *Catalog) Checker
	Audit       audit.Logger
	OnChange    func(generation uint64)
	Revocations journal.RevocationJournal
	Log         *slog.Logger
	Metrics     *metrics.Scope
}
func (s *Service) Declarer(moduleID string) Declarer
func (s *Service) Freeze(ctx context.Context) error
func (s *Service) CanAssign(actor Checker, roles []RoleName) error
func (s *Service) Dominates(actor Checker, target []RoleName) error
func (s *Service) Compile(in CheckerInput) Checker
func Can(ctx context.Context, s Permission) bool
func Require(ctx context.Context, s Permission) error
func RequireAll(ctx context.Context, s ...Permission) error
type Store interface {
	GetRole(ctx context.Context, name RoleName) (*Role, error)
	ScanRoles(ctx context.Context) iter.Seq2[*Role, error]
	PutRole(b *storage.Batch, r *Role) error
	DeleteRole(b *storage.Batch, r *Role) error
}
type Importer interface {
	ImportRole(b *storage.Batch, r *Role) error
}
```

The full `Service` method set, `CheckerInput`, `Checker` (`Has`, `Satisfies`, `Confined`, `Concrete`),
`WithChecker`/`CheckerFrom`, `ReservedNamespaces()` and the sixteen errors are 05-rbac §3.4's, copied verbatim.

## Tests first (05-rbac §12)

`TestWorkerKey_Confined` (gate test, created here with the cells rbac alone can prove: the `fp-worker` seed holds only
`Confining` scopes after every seed merge; an admin-owned key scoped `["books:fingerprint.worker"]`, an `fp-worker`
user's key and an `fp-worker` session each produce the `CheckerInput` that `Deps.Compile` receives, with the key's
scopes intact. F4-09 adds the compiled-checker cells in `core/internal/authz`'s own `TestWorkerKey_Confined` (`Confined()` true, `core:self`, `core:metrics.read` and
`books:reading` not satisfied); F4-08 adds the route cells: 403 on `/api/core/v1/me`, own sessions, `/metrics` and a
reading-state route, 200 on the worker routes), `FuzzParsePermission`, `TestScopeGrammar_Table`,
`TestWildcard_RolesAllowedKeysRejected`, `TestImplies_ClosureAndCycle`, `TestSeedRole_EditRefused`,
`TestSeed_NoNonConfiningScopeOnConfinedRole`, `TestSeedMerge_NewSeedScopeReachesRole`, `TestSeedMerge_Idempotent`,
`TestSeedMerge_AdminOutsideMerge`, `TestSeedMerge_RestrictedAdditionHeldPending`, `TestRoleWrite_EscalationRefused`,
`TestCanAssign_CannotGrantUnheld`, `TestCanAssign_RemovalCheckedLikeAssignment`, `TestLastAdmin_ConcurrentDemotions`,
`TestNativeStore_RoundTrip` / `rbactest.StoreSuite`, `TestImporter_RejectsBrokenRole`, `TestNoGlobals` (rbac),
`TestCompileCache_GenerationInvalidates` (name coined here: the recording `Deps.Compile` of step 4).
F4-09's, not yours: `TestChecker_MatchVectors`, `TestCompile_KeyNarrowsRoles`, `TestAdmin_IsScopeAndKeyScopingApplies`,
`TestSensitive_EvaluatedBeforeAdmin`, `TestAuthDisabled_CheckerHasNoAdmin`, `archtest.NoSystemPrincipalOutsideKernel`.

## Invariants

- An API key never has empty or wildcard scopes; a key's checker is role ∩ key.
- No module may grant a core scope or a restricted scope by default.
- `admin` is always exactly `{core:admin}`; the last `core:admin` holder cannot be removed.
- A confined principal never gains a non-`Confining` scope through a seed.
- No package-level registry (R5).

## Acceptance

```bash
(cd core && go vet ./rbac/... ./internal/rbacstore/...)                                   # exit 0
(cd core && go test -race -count=1 ./rbac/... ./internal/rbacstore/...)                   # ok
(cd core && go test -count=1 -run 'TestWorkerKey_Confined|TestSeedMerge_|TestNativeStore_RoundTrip' ./rbac/...)   # PASS
(cd core && go list -deps ./rbac | grep -c 'mediacore/internal/authz')                    # 0
make -C core ci                                                                           # exit 0
make ci                                                                                   # exit 0
```

## Out of scope

`core/internal/authz`, the compiled checker and its tests (F4-09); `auth` (F4-02); the role routes (F4-05); books' atom table data and the identity cutover (F4-06); the reach oracle
(F4-07); rbac's restore hook, `core/rbac/restore_hook.go` (05-rbac §7.1 "Restore hooks"; F4-11 writes it); the kernel
wiring that makes rbac live for ABO (F4-08); the operator seed diff (deferred).

## Rollback

Revert the PR; nothing uses it in ABO.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/rbac): role store, importer, service, seed merge, confinement (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```

## Planner notes

- **`TestWorkerKey_Confined` split three ways.** The inventory gives this row the test, but the compiled checker that
  decides `Confined()` is `authz.Compile` (F4-09, next wave). This brief creates the test with the seed and
  `CheckerInput` cells, F4-09 adds the checker cells and F4-08 the route cells. If the lead prefers one owner, move the
  gate test to F4-09 (checker cells) and F4-08 (route cells).
- **How role permissions reach `Deps.Compile` is unspecified.** 05-rbac §3.4's `Deps.Compile func(CheckerInput,
  *Catalog) Checker` carries role names only, and neither type holds a role's `Permissions` (F4-09 is SPEC-GATED on
  this). Whatever the spec settles (a resolved-patterns field on `CheckerInput`, a `Catalog` role lookup, or a new
  signature) lands in this row's `core/rbac/service.go`; if the spec is fixed before this row runs, follow it here. The
  same holds for an exported kind accessor on `rbac.Requirement`, which an out-of-package checker needs.
