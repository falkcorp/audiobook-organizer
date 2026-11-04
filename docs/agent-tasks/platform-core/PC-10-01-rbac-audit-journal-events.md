<!-- file: docs/agent-tasks/platform-core/PC-10-01-rbac-audit-journal-events.md -->
<!-- version: 1.0.0 -->
<!-- guid: d6ecd17f-8aba-42c8-88ff-5bf063660f58 -->
<!-- last-edited: 2026-11-04 -->

# PC-10-01 — core/rbac, core/audit, core/backup/journal types and the events bus (dark)

**ID:** PC-10-01 · **Wave:** W10 (S4 r1, dark) · **Priority:** P1 · **Effort:** L
**Tier:** T3 Opus/high — the atom mapping and the confinement rule decide every legacy route's reach once the flip
enforces them; a wrong `Confined()` hands ambient scopes to a worker key.
**Depends on:** OWNER-10 (the `S3` soak row) and OWNER-09 (a current `UI0` row). **First PR of S4 r1:** it sets
`release: S4r1` in `ci/stage.txt`, so its merge needs both rows (09 §9.2.1 check (a); `UI0` is never the predecessor).
**Stage-Release trailer:** `Stage-Release: S4 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Contracts: rbac, audit `Logger`, journal; Behaviour
rules 1, 3, 23).
**Owning spec:** 05-rbac §3 (§3.1 scopes and requirements, §3.2 catalog, §3.3 roles, §3.4 service and checker, §3.5
audit), §4 (grammar, wildcards, implication, effective permissions, key scopes, admin, roles CRUD rules, seed merge,
module declarations, enforcement points, auth modes), §6.1 (the 13 atoms), §6.3 (stored data, `LegacyAmbientScopes`,
confinement), §7 (audit log), §12; 05-backup §5B.3.6 (journal types); 05-events (the bus API, §"Bus"); 04 §8.1 row S4;
03 §3.3 rows `audit`, `backup/journal`, `events`.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-10-01" -b agent/pc-10-01-rbac-audit origin/main
cd "$REPO/../audiobook-organizer-pc-10-01"
git rebase origin/main
git worktree list
npm ci --prefix web
```

## Goal

Land, dark (nothing in ABO calls them yet; gin's auth still decides every request):

- the rest of `mediacore/rbac` (L4): `Permission`, `Requirement`, `LegacyAtom`, `LegacyCheck` (S2 already has the
  `LegacyAtom` leaf constructor; keep its value identical), `Catalog` with `Legacy`/`LegacyReach`, `LegacyAlias`, roles,
  `Service` (`New`, `Freeze`, `CanAssign`, `Dominates`, `Compile`), `Checker` with `Confined()`, `Store`, the errors
  (`ErrSeedRoleLocked`, `ErrNoLegacyAtom`, `ErrRestrictedGrant`, `ErrLastAdmin`, `ErrDominance`), `CoreAdmin`;
- `rbac/rbactest.StoreSuite` and the native rbac store over `c/rbac/` (see Planner notes for its path);
- `mediacore/audit` (L4): `Logger` (`Record`, `RecordIn`, `Emit`), the `c/audit/` append-only store with `seq`
  reservation, denial coalescing, redaction (05-rbac §3.5, §7), moved from `internal/server/absauth/audit.go` per 04
  §8.1 with the `internal/logger` cut to a passed-in `*slog.Logger`;
- `mediacore/backup/journal` (L4): `RevocationJournal`, `Revocation`, `RestoreHook`, `RestoreContext` (types only;
  05-backup §5B.3.6);
- `mediacore/events` (the in-process bus; 05-events): `Bus`, `Scope`, `Open`, `Close`; no outbox (S6);
- `ci/stage.txt`: `release: S4r1` (`shipped_stage:` stays `S3`; S4 r1 is never a `shipped_stage:` value).

## Exact files

Create: `core/rbac/`, `core/rbac/rbactest/`, `core/audit/`, `core/backup/journal/`, `core/events/`.
Modify: `ci/stage.txt`.
Also needed (Planner notes): `core/internal/rbacstore/` (native store) and `core/internal/authz/` (`Compile`, the
privileged checkers including the auth-disabled checker).

## Re-verify (rerun at your base)

```bash
grep -n 'release:' ci/stage.txt                                   # release: S3 at base (after PC-09-06); STOP otherwise
grep -n 'shipped_stage:' ci/stage.txt                             # shipped_stage: S3
grep -rn 'func LegacyAtom(atom string) Requirement' core/rbac/    # 1 hit (S2 leaf, PC-04-02 or PC-05-01); keep it
grep -c 'Perm[A-Z][A-Za-z]* *Permission = "' internal/auth/permissions.go   # 13 at the plan checkout (the 13 atoms, 05-rbac §2 anchor :24-85)
grep -n 'func SeedRoles' internal/auth/seed.go                    # 1 hit (HEAD's seed; moves at r2, not here)
test -f internal/server/absauth/audit.go && echo ok               # the audit source (04 §8.1 S4)
grep -n '"github.com/falkcorp/audiobook-organizer/internal/logger"' internal/server/absauth/audit.go   # 1 hit (cut to *slog.Logger)
test -d core/events && echo EXISTS-STOP || echo absent            # absent
```

## Steps

1. Tests first (below), red.
2. `core/rbac` from 05-rbac §3.1-§3.4 exactly (contract below). `Compile` decides `Confined()` **first**, then adds
   `CheckerInput.Ambient` after the key intersection, and only to a principal that is not confined; for a key the test
   is its effective set (owner role atoms ∩ the expansion of the key's atoms; an empty list means the role set), never
   the owner alone (05-rbac §4.4, §6.3). `Freeze` refuses `LegacyAmbientScopes` on a native role store and any ambient
   scope that is `Sensitive`, `AuthRequired`, `Confining`, `*:settings.manage` or `*:integrations.manage`; it refuses an
   app-level alias whose `Reach` does not dominate `New` or names an unknown scope, and the same alias in a module
   manifest (`ErrCoreGrant`). `core:system.status` is never ambient.
3. The 13 atoms' mapping (05-rbac §6.1) is **app data**: core holds no ABO atom (06 §6.4.5; 05-rbac §4.9 "one home for
   the legacy alias table"). Test it with core's synthetic fixture `core/rbac/testdata/legacy_reach_synthetic.json`
   (05-rbac §12 `TestRowRuleDominatesLegacyReach` note) and run `rbactest.RowRuleSuite` over it; ABO's real snapshot
   arrives in PC-10-05 / PC-11-02.
4. Native rbac store over `c/rbac/` passing `rbactest.StoreSuite` (`TestNativeStore_RoundTrip`); it implements no
   `legacy` capability.
5. `core/audit` per 05-rbac §3.5 and §7 (records, `seq` reservation that never reissues after a crash, the volume
   control, retention, redaction by key and prefix, a bounded queue that drops without blocking). `RecordIn` stages the
   record into the caller's batch (the same-batch rule).
6. `core/backup/journal`: types only, copied from 05-backup §5B.3.6.
7. `core/events`: the bus of 05-events (per-subscription goroutine started by `Open`, no delivery before `Open`,
   `Close` drains until ctx then drops and counts). `rbac` never imports `events` (the kernel publishes
   `core.rbac.changed.v1` from `Deps.OnChange`).
8. Archtests: `TestNoGlobals` (no package-level registries in `rbac`/`audit`), `archtest.NoSystemPrincipalOutsideKernel`
   (privileged `authz` constructors called only from `auth`, the tasks executor and `kernel`; `rbac` imports nothing
   from `authz`), and the layer guard (rbac, audit, events, backup/journal at L4; 03 §3.5 R4).
9. `ci/stage.txt`: change only `release: S3` to `release: S4r1`. This PR therefore runs `compat-rehearsal` (09 §9.2.1:
   every PR that changes `release:`).
10. Headers, `changelog.d/` fragment, acceptance.

## Code contract (05-rbac §3; digest "Contracts")

```go
func LegacyAtom(atom string) Requirement
func LegacyCheck(atom string, readSide Permission) Requirement
type LegacyAlias struct{ Old string; New, Reach []Permission }
func (c *Catalog) Legacy(old string) ([]Permission, bool)
func (c *Catalog) LegacyReach(old string) (Requirement, bool)
func New(ctx context.Context, opts Options, deps Deps) (*Service, error)
func (s *Service) Freeze(ctx context.Context) error
func (s *Service) CanAssign(actor Checker, roles []RoleName) error
func (s *Service) Dominates(actor Checker, target []RoleName) error
func (s *Service) Compile(in CheckerInput) Checker
type CheckerInput struct { Roles []RoleName; Scopes []Permission; Interactive, LegacyKey bool; LegacyAtoms []string; Ambient []Permission }
type Checker interface { Has(s Permission) bool; Satisfies(r Requirement) bool; Confined() bool; Concrete() []Permission }
type Store interface {
	GetRole(ctx context.Context, name RoleName) (*Role, error)
	ScanRoles(ctx context.Context) iter.Seq2[*Role, error]
	PutRole(b *storage.Batch, r *Role) error
	DeleteRole(b *storage.Batch, r *Role) error
}
// package audit
type Logger interface {
	Record(ctx context.Context, e Event) error
	RecordIn(ctx context.Context, b *storage.Batch, e Event) error
	Emit(ctx context.Context, e Event)
}
// package journal (05-backup §5B.3.6)
type RevocationJournal interface {
	Append(ctx context.Context, b *storage.Batch, r Revocation) error
	Gap(b *storage.Batch, since time.Time) error
}
type RestoreHook func(ctx context.Context, rc RestoreContext) (counts map[string]int, err error)
```

`Deps` of `rbac.New` per 05-rbac §3.4 (`Store`, `Compile`, `Audit`, `OnChange`, `Revocations`, `Log`, `Metrics`).

## TDD — names from 05-rbac §12

`FuzzParsePermission`, `TestScopeGrammar_Table`, `TestWildcard_RolesAllowedKeysRejected`, `TestChecker_MatchVectors`,
`TestImplies_ClosureAndCycle`, `TestCompile_KeyNarrowsRoles`, `TestAdmin_IsScopeAndKeyScopingApplies`,
`TestSensitive_EvaluatedBeforeAdmin`, `TestAuthDisabled_CheckerHasNoAdmin`, `TestSeedRole_EditRefused` (service level),
`TestSeed_NoNonConfiningScopeOnConfinedRole`, `TestAppLegacyAliases_MayTargetCore`,
`TestManifest_RejectsForeignNamespace` / `_RejectsCoreGrant` / `_RejectsCoreLegacyAliasFromModule` /
`_RejectsRestrictedDefaultGrant`, `TestSeedMerge_NewSeedScopeReachesRole` / `_Idempotent` / `_AdminOutsideMerge`,
`TestSeedMerge_RestrictedAdditionHeldPending`, `TestRoleWrite_EscalationRefused`, `TestCanAssign_CannotGrantUnheld`,
`TestCanAssign_RemovalCheckedLikeAssignment`, `TestUsersManage_CannotTempLoginAdmin`, `TestLastAdmin_ConcurrentDemotions`
(`testing/synctest`), `TestAuditAction_Grammar`, `TestAudit_SyncInSameBatch`, `TestAudit_SeqUniqueAcrossCrash`,
`TestAudit_DenialCoalescing`, `TestAudit_RedactsSecretsByKeyAndPrefix`, `TestAudit_QueueFullDropsWithoutBlocking`,
`TestNativeStore_RoundTrip` with `rbactest.StoreSuite`, `rbactest.RowRuleSuite` over the synthetic fixture,
`TestNoGlobals`, `archtest.NoSystemPrincipalOutsideKernel`. The row's "Confined() and the LegacyAmbientScopes rule unit
tests": core-level cases named `TestCompile_ConfinedDecidedBeforeAmbient` and `TestFreeze_RefusesRestrictedAmbientScope`
(names coined here); ABO's `TestLegacyAmbientScopes` and `TestWorkerKey_NoAmbientScopes` run at the flip (PC-11-02).

## Invariants (STOP conditions)

- Core holds no ABO atom string and no ABO key name (`TestCoreHoldsNoAppKeyNames`, `TestCoreImportsOnlyCore`).
- `rbac` does not import `events`, `authz` or `legacy`.
- Nothing in the root module calls `rbac.New`, `audit` or the bus yet (dark): `grep -rn 'mediacore/rbac"\|mediacore/audit"\|mediacore/events"' --include=*.go internal cmd main.go | grep -v _test` finds only imports of the
  pre-existing S2 leaf (`rbac.LegacyAtom` in the manifest).

## Acceptance

```bash
(cd core && go build ./... && go test ./rbac/... ./audit/... ./backup/journal/... ./events/... ./internal/rbacstore/... ./internal/authz/... -race -count=1)   # ok
(cd core && go test ./archtest/... -count=1)                       # ok
grep -n '^release: S4r1$' ci/stage.txt && grep -n '^shipped_stage: S3$' ci/stage.txt   # 2 hits (adjust to the file's exact key format at base)
make compat-rehearsal                                              # exit 0 (release: changed)
make -C core ci                                                    # exit 0
make ci                                                            # exit 0
```

## Out of scope

`auth` (PC-10-02); books' legacy stores (PC-10-05); the reach snapshot file and ABO's alias table data (PC-10-05 /
PC-11-02); native routes (PC-11-01); enforcement (PC-11-02); the outbox (S6).

## Rollback

Binary swap; no row is written by this PR. A revert also restores `release: S3` (a marker change; coordinate with the
next marker PR).

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S4 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase` after the gate. CLAUDE.md concurrency: no
library-scale loop here.

## Planner notes

- **Missing paths:** the native rbac store (`core/internal/rbacstore`, 05-rbac §3.4 `Deps.Store`) and
  `core/internal/authz` (`Compile` injected by the kernel, the auth-disabled checker of `TestAuthDisabled_CheckerHasNoAdmin`,
  `archtest.NoSystemPrincipalOutsideKernel`) are in no inventory row. `rbactest.StoreSuite` "over the native store" (this
  row's done-test) needs the first; the checker tests need the second. Add both to this row (or `authz` to PC-10-02).
