<!-- file: docs/agent-tasks/platform-core/PC-10-05-books-legacy-identity-stores.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9cfd76aa-6e5f-4fd6-98c1-e79d4943a116 -->
<!-- last-edited: 2026-11-04 -->

# PC-10-05 — Books legacy identity stores (dark): auth.Store and rbac.Store over HEAD's rows with legacy.WriteLock, CommitLocked and one instance per family; the P4d decode census

**ID:** PC-10-05 · **Wave:** W10 (S4 r1, dark) · **Priority:** P1 · **Effort:** XL
**Tier:** T4 Opus/xhigh-max — HEAD's identity mutexes must span core's commit, or early fix 6 (a last-use touch
un-revoking a key) and single-use tokens reopen; a second store instance splits the mutexes.
**Depends on:** PC-10-02 (`auth.Store`, `authtest.StoreSuite`); `rbac.Store` and `rbactest.StoreSuite` from PC-10-01.
**Stage-Release trailer:** `Stage-Release: S4 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S4.md` (Contracts: `auth.Store`, `rbac.Store`, package
`legacy`; Behaviour rules 1-3, 14, 16-17).
**Owning spec:** 05-authn §3.4 (store, `legacy` capabilities, lock names and mapping), §5 (lifecycle: P4c build once,
P4d step 1c census at r1), §11 (ABO's legacy store), §10 tests; 05-rbac §3.4, §6 (§6.3 stored data, row rule, write
mapping); 05-storage §5S.3.2, §5S.3.11 (lock ranks), `TestLegacyStoreSuites`; 06 §6.5.1 (P4c/P4d), §6.11.6
(singletons); 09 S4 Releases r1, Data/keys; 09 §9.9.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-10-05" -b agent/pc-10-05-legacy-identity origin/main
cd "$REPO/../audiobook-organizer-pc-10-05"
git rebase origin/main
git worktree list
npm ci --prefix web
```

Suggested split: code-exploration of HEAD's identity rows and mutexes first, then implementation, then a
code-auditing pass on every staging method's lock check.

## Goal

Books supplies `Manifest.LegacyStores.Auth` and `.Roles`: ABO code over HEAD's rows in HEAD's representation (raw
`sess:` sessions, HEAD's one-time tokens and invites, `abk_` keys with explicit non-empty legacy atoms, roles as legacy
atoms only, HEAD's restriction fields). Books' store implements `legacy.WriteLock` exposing HEAD's six identity
mutexes as `storage.Lock` values, `legacy.AuthAtomWriter`, `legacy.AuthReconciler` and `legacy.RBACAtomWriter`. The
kernel builds **one** instance per family at P4c (shared with books' own P6 store), resolves the six lock names at P4c,
and at S4 r1 runs only the read-only **decode census** at P4d step 1c. Core writes no identity row at r1; books' start
hook keeps HEAD's `seedRolesAndTokens`.

## Exact files

Create: `internal/books/store/legacy_auth.go`, `internal/books/store/legacy_rbac.go`,
`internal/books/store/legacy_identity_test.go`, `core/legacy/auth.go`, `core/legacy/legacytest/auth.go`.
Modify: `internal/books/module.go`, `internal/books/store/pebble_store_auth.go`, `core/kernel/` (P4c construction and
lock resolution; P4d step 1c census; skip-and-log of journal auth kinds, `PostRestore`, `BootWrites`, P7 seeding).

## Re-verify (rerun at base)

```bash
test -f internal/books/store/pebble_store_auth.go && echo ok            # PC-07-04 re-homed it from internal/database
grep -n 'apiKeyMu\|inviteMu\|userWriteMu\|sessionWriteMu\|bootstrapMu\|userMergeApplyMu' internal/books/store/*.go | grep 'sync\.' | wc -l   # 6 (one declaration each); record names
grep -n 'counterMu' internal/books/store/*.go | grep 'sync\.' | wc -l   # 1 (not an identity lock; never one of the six)
grep -n 'MarkInviteEmailSent\|RecordInviteGuestPolicy\|RehashUserPassword' internal/books/store/*.go | grep '^.*func' | wc -l   # 3 (covered-mutex methods no stage may call)
grep -n 'func SeedRoles\|func SeedSystemUser' internal/auth/seed.go     # 2 hits (stay through r1)
grep -n 'seedRolesAndTokens' internal/server/server_lifecycle.go       # >= 1 hit (books' start hook keeps it at r1)
grep -n 'LegacyStores' -r core/module/ core/legacy/ | head              # the S2b Manifest.LegacyStores shape you extend
grep -n 'type Store interface' core/auth/*.go core/rbac/*.go            # 2 hits
grep -n 'LockUsers\|LockSessions\|LockAPIKeys\|LockInvites\|LockBootstrap\|LockMerge' -r core/legacy/ | wc -l   # 0 (added here)
```

## Steps

1. Tests first (below), red.
2. **`core/legacy/auth.go`** (outside the API gate), from 05-authn §3.4 and 05-rbac §3.4:
   `AuthReconciler`, `AuthAtomWriter`, `RBACAtomWriter`, the six lock-name constants (`LockUsers = "auth.users"`,
   `LockSessions = "auth.sessions"`, `LockAPIKeys = "auth.apikeys"`, `LockInvites = "auth.invites"`,
   `LockBootstrap = "auth.bootstrap"`, `LockMerge = "auth.merge"`), and `Stores.Auth func(Handles) (auth.Store,
   error)`, `Stores.Roles func(Handles) (rbac.Store, error)` on the existing `legacy.Stores`.
   `core/legacy/legacytest/auth.go`: `AuthStoreSuite` (one case per capability: `Reconcile`, the atom writer,
   `WriteLock` with the race cases) and `RBACStoreSuite` (`RBACAtomWriter`), driving the services.
3. **Books' stores** (`legacy_auth.go`, `legacy_rbac.go`): views over the one `*PebbleStore`; every write stages into
   the caller's batch and first checks `storage.LocksHeld(b, l)` for its lock (mapping: digest rule 16), refusing with
   `storage.ErrNotLocked`; staging methods take **no** mutex and never call `MarkInviteEmailSent`,
   `RecordInviteGuestPolicy` or `RehashUserPassword`. `WriteLocks(names)` returns HEAD's mutexes as `storage.Lock` with
   the rank order `userMergeApplyMu` < `bootstrapMu` < `inviteMu` < `userWriteMu` < `sessionWriteMu` < `apiKeyMu` <
   `configBlobSaveMu` < `opsMu` (05-storage §5S.3.11). Key writes store explicit non-empty legacy atoms; a set that is
   not a union of whole atoms → 422 `no_legacy_atom`, no row (`rbac.ErrNoLegacyAtom`). A legacy-backed store writes
   status `disabled`, never `needs_confirmation`. An undecodable row yields `ErrUndecodableRecord` /
   `ErrUndecodableRole` for that row only. `var _ legacy.AuthAtomWriter = ...` and `var _ legacy.WriteLock = ...`.
4. **`pebble_store_auth.go`**: HEAD's own methods keep their mutexes; refactor only so the legacy store and HEAD's
   methods share one instance and one set of mutexes (no behaviour change for gin's callers).
5. **Singletons** (06 §6.11.6): the kernel calls each P4c constructor once and books' P6 store receives the same
   instance (`TestLegacyStoresAreSingletons`).
6. **Kernel, r1 only** (05-authn §5 item 2): P4c builds the stores and calls `WriteLocks` with the six names, holding
   the result for `Deps.Locks`/`RestoreDeps.Locks` (unused at r1). P4d step 1c runs the read-only census: every
   `ScanUsers`, `ScanAPIKeys` (all owners), `ScanRoles`; counts decodable and undecodable rows per family, logs one
   summary line and lists undecodable row IDs (IDs only, never values) for the owner's P1 read. Journal re-apply's auth
   kinds, `PostRestore`, `BootWrites` and P7 seeding are skipped with one log line each. r1 lists **no** `journal` duty.
   Boot writers: none added (the census writes nothing); if you add any, extend `TestBootCrashMatrix` /
   `TestBootCrashMatrix_ABO` in this PR.
7. **`internal/books/module.go`**: set `Manifest.LegacyStores.Auth` and `.Roles`; keep the start hook's
   `seedRolesAndTokens` sequence unchanged (its `.bootstrap-token` and `.readonly-key` writes go through
   `legacy.ConfigOpenEnv.Files` as from S2b). `ABO_CORE_AUTH_MODE` is read from r1 (digest rule 3): confirm the alias
   table (PC-09-03) has it; if not, STOP.
8. Headers, `changelog.d/` fragment, acceptance.

## Code contract

```go
// package legacy (core/legacy/auth.go)
type AuthReconciler interface{ Reconcile(ctx context.Context, b *storage.Batch) error }
type AuthAtomWriter interface{ IssueAPIKeyAtoms(b *storage.Batch, k *auth.APIKey, atoms []string) (raw string, err error) }
type RBACAtomWriter interface{ PutRoleAtoms(b *storage.Batch, r *rbac.Role, legacyAtoms []string) error }
const ( LockUsers = "auth.users"; LockSessions = "auth.sessions"; LockAPIKeys = "auth.apikeys"; LockInvites = "auth.invites"; LockBootstrap = "auth.bootstrap"; LockMerge = "auth.merge" )
// Stores.Auth func(Handles) (auth.Store, error); Stores.Roles func(Handles) (rbac.Store, error)
```

`auth.Store` and `rbac.Store` are 05-authn §3.4 / 05-rbac §3.4 verbatim.

## TDD — names from the spec

`TestLegacyStoreSuites` (identity half: `authtest.StoreSuite` and `rbactest.StoreSuite` against books' stores with HEAD
fixtures, byte-identical round-trip of every fixture, `legacytest.AuthStoreSuite` and `legacytest.RBACStoreSuite`),
`TestDarkReleaseWritesNoIdentityRow`, `TestDarkReleaseRecordsJournalGap` (r1 lists no `journal` duty; a simulated next
r2 boot appends a `gap` line), `TestDarkRelease_HEADSeedWritersKeepRunning` (the row's "on r1 a fresh store has the four
seed roles and `_system` and every restart writes a new bootstrap token file"; the token redeems; the expiry stamp keeps
HEAD's one-shot flag), `TestLegacyStoresAreSingletons`, `TestLegacyStagedIdentityWriteOutsideLockRefused`,
`TestLegacyRows_ReadInPlace`, `TestLegacyRevocationHonoured`, `TestLockRanksTotalOrder`,
`TestLegacyRoleWrite_RefusesUnrepresentableSet`, `TestUnknownAtom_RoleDegradedBootContinues`.

## Invariants (STOP conditions)

- Core writes no identity row at r1 (census included).
- No staging method takes a mutex; every one refuses without its hold.
- One `*PebbleStore` and one set of identity mutexes per process.
- `auth` never imports `legacy` and never calls `WriteLocks`.
- HEAD's gin auth path still decides every request; `seedRolesAndTokens` still runs on every start.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                          # exit 0
go test ./internal/books/... -run 'TestLegacyStoreSuites|TestDarkReleaseWritesNoIdentityRow|TestDarkReleaseRecordsJournalGap|TestDarkRelease_HEADSeedWritersKeepRunning|TestLegacyStoresAreSingletons|TestLegacyStagedIdentityWriteOutsideLockRefused' -race -count=1   # ok
(cd core && go test ./legacy/... ./kernel/... -race -count=1)                          # ok
go test ./internal/server/... -count=1                                                 # ok (gin auth unchanged)
make -C core ci                                                                        # exit 0
make ci                                                                                # exit 0
```

## Out of scope

P4d steps 2-4, P7 seeding, `BootWrites`, moving `seed.go`/`bootstrap.go`, `LegacyAuthCore` (all PC-11-02); the reach
snapshot (PC-10-06, see its notes); native routes (PC-11-01).

## Rollback

Binary swap; no identity row is written by core at r1 (09 §9.2.1: no row for S4 r1's identity content).

## Repo rules

Headers; `changelog.d/` fragment. Conventional commits with `Stage-Release: S4 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`. Fixtures only. CLAUDE.md concurrency: the
census scans the identity families (users, keys, roles), which are not library-scale collections; a sequential
read-only scan is acceptable. If P1 shows a family large enough to matter for the boot budget, use a bounded pool and
say so in the PR.

## Planner notes

- `internal/books/module.go` is also modified by PC-10-04 (which depends only on PC-10-03); the wave order puts
  PC-10-05 in step 3 and PC-10-04 in step 4, so the edits are ordered, but the depends-on list does not encode it.
