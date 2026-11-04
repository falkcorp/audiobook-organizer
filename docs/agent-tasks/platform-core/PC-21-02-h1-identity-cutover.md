<!-- file: docs/agent-tasks/platform-core/PC-21-02-h1-identity-cutover.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb72136a-2e8d-4fd7-b39c-06b9031f2148 -->
<!-- last-edited: 2026-11-04 -->

# PC-21-02 — H1 identity cutover: hashed sessions and tokens (existing ones deleted), mck_ keys and native scopes from each key's effective set, roles with native scopes; flip

> **SPEC-GATED.** The spec does not yet give enough to implement this task. Step 0 checks that the missing text exists;
> if it does not, STOP and report. Everything below "What the spec already decides" is binding and must be honoured by
> the stage table when it is written; nothing here chooses the open points.

| Field | Value |
|---|---|
| ID | `PC-21-02` |
| Tier | T4 Opus/xhigh–max — rewrites every identity row once, one way; ambient reach and confinement must carry over exactly |
| Wave | W21 — release `H1` |
| Depends on | `PC-21-01` |
| Priority / effort | P1 / XL |
| Spec | 09 §9.13 (H1 row, release-note text, "A fresh verified archive first"); 05-authn §11.2; 05-rbac §6.3, §3.6, §4.8; 05-storage §5S.3.13; 14 Q-D |
| Digest | `docs/plans/platform-core/digests/H1-H3.md` §1, §2, §3, §5 rules 1–10, §6, §7 |
| `Stage-Release:` | `H1 flip` on the flip PR (sets `shipped_stage:` and `release:` to `H1` together); `H1 1` on H1's content PRs (09 §9.2.1, §9.13) |

## Step 0 — the spec text this task needs (STOP if missing)

```bash
S=docs/specs/platform-core
# 0.1 H1's own stage table with a "Flip PR" row (09 §9.13: written "when the owner schedules the cutover, before its first PR").
awk '/^## 9.13/,/^## Objections/' $S/09-abo-on-core.md | grep -c -E '^#{3,4} +H1\b'          # HEAD 9b1545c1a: 0 -> STOP
awk '/^## 9.13/,/^## Objections/' $S/09-abo-on-core.md | grep -c '^| Flip PR'               # HEAD: 0 -> STOP
# 0.2 The identity layout is decided (move to c/auth/ and c/rbac/, or rewrite in place). At HEAD the H1 row still reads:
grep -c 'plus `core` when the families move to `c/auth/`/`c/rbac/`' $S/09-abo-on-core.md   # HEAD: 1 -> STOP while it is 1
grep -c 'or are rewritten in place, as 05-authn and 05-rbac decide' $S/09-abo-on-core.md   # HEAD: 1 -> STOP while it is 1
```

**Missing spec text, exactly:**

1. **No H1 stage table.** 09 §9.13 gives only a planning-level row; the stage table "with its Flip PR row and full
   done-check list" is written when the owner schedules the cutover. Without it there is no flip PR, no PR series and no
   full done-check list.
2. **The identity storage layout is undecided.** 09 §9.13's H1 row: "the identity families move to `c/auth/`/`c/rbac/` or
   are rewritten in place, as 05-authn and 05-rbac decide". Neither decides: 05-authn §11.2's H1 row says it "raises
   `books`, and `core` for the `c/auth/` and `c/rbac/` rows"; 05-storage §5S.3.13 speaks of "H1's identity rows in
   place"; 05-rbac §6.3 says only that what a native role could hold "waits for the identity hardening cutover".
3. **What the layout decides, so it is open too:** the owners H1 raises (`books`, or `books` and `core`, 09 §9.13 "Level
   raised"); whether the data migration is core's (`core/auth`, `core/rbac`, reading books' rows through a `storage.Grant`,
   05-storage §5S.3.13 "Shape" and "The grant") or books'; whether H1 is the first cutover that raises `core`, which lifts
   books' legacy restore bound `{core, 0}` and must build and activate backup encryption (09 §9.13; 05-backup §5B.3.9);
   and `soak_record.py`'s H1 entry (PC-21-01 left it refused).

**This brief does not choose the layout.** Report the missing items to the owner; the planner's inventory files below
assume the in-place layout and must be re-derived once the decision exists.

## What the spec already decides (binding on the stage table and the implementation)

**What changes** (09 §9.13 H1 row; 05-authn §11.2 table):

- Sessions and one-time tokens are stored hashed, in the native forms `mcs_` (sessions), `mci_` (invites), `mct_`
  (temp-login tokens), `mcb_` (bootstrap tokens) (05-authn §4.1). **Every existing session, invite and other one-time token
  is deleted, not rehashed**, so a raw token in an older archive is dead against the live store; the residual is bounded
  by the tokens' TTLs. Everyone signs in again once.
- API keys become `mck_` keys with **native scopes derived from each key's effective set** (05-rbac §6.3). Keys are
  SHA-256 hashes of high-entropy tokens at HEAD, so an archive exposes no usable key and **no key re-issue is asked**
  (09 §9.13; SEC10-7).
- Roles hold native scopes (core-only scopes allowed, so an ABO role can hold `core:metrics.read`), with `SeedDigest`/
  `PendingSeed`, the seed-role operator diff and the lift of `ErrSeedRoleLocked` (05-rbac §3.6, §4.8);
  `needs_confirmation` and `SignInRestricted` become stored fields; the native `LastSeen`.
- **The ambient-scope rule, one rule (05-rbac §6.3; 09 §9.13):** a role gains `core:self`, `core:metrics.read` and
  `books:reading` in its native scope set **iff it is not `Confined()`**; a key gains them iff its **effective** S4–S10 set
  before Ambient (the owner's role atoms ∩ the expansion of the key's atoms, an empty list meaning the role set) is not
  `Confined()` — never decided by the key's owner. HEAD's confinement rule (`internal/server/middleware/auth.go:268-297`,
  `workerOnly` at line 271 at HEAD) stays, so an admin's `["fingerprint.worker"]` key stays confined and every principal's
  answers are unchanged across H1.
- Then the done-phase clearing PC-21-01 built: every pre-cutover checkpoint of the store and every old Pebble file under
  the DB directory, through a forced compaction of the rewritten ranges and a WAL rotation (raw tokens sit in keys:
  `sess:<raw>`, `idx:sess:user:<user>:<raw>`, `invite:<raw>`; at HEAD `sess:` writes are at
  `internal/database/pebble_store_auth.go:787`, `invite:` at `:1347`).

**Machinery and preconditions** (PC-21-01; 05-storage §5S.3.13):

- A `datamigrate.DataMigration`: `Breaking`, `BootBlocking`, `RaisesLevel` = the cutover build's supported level for each
  owner it raises, `Source` = the legacy ranges it moves, `After` = the highest step the owning module registers,
  `Parallelism tasks.Parallelism` **required** (CPU, fixed or declared sequential with a reason; never a plain sequential
  loop, CLAUDE.md). Phases `folding`/`folded` (legacy authoritative), `raised` (sidecars first, then the level batch),
  `deleting` (chunks with a cursor), `done`. The level is always raised before the first legacy delete.
- Runs behind the pre-migration checkpoint and the per-owner level guard; never on the first boot of a new release
  (`RequiresSelfServed`); deferred, never failed, without a fresh verified archive or while a swap set-aside exists
  (`TestH1H2_DeferredWithoutFreshVerifiedArchive`, `TestH1H2_DeferredWhileSwapSetAsideExists`).
- Per-owner write locks of the legacy identity stores (`legacy.WriteLock`: `apiKeyMu`, `inviteMu`, `userWriteMu`,
  `sessionWriteMu`, `bootstrapMu`, `userMergeApplyMu`) are held across every in-process read-modify-write (09 §9.9;
  09 S4 "Data/keys"). Steps take `ctx` and honour cancellation between batches.
- Floor: the H1 release itself. A build that later deletes the legacy identity store declares
  `UpgradeFloor{Owner: <subsystem>, Requires: [<cutover>], InstallFirst: <cutover release>}` (05-storage §5S.3.13 "After
  it"); the cutover build itself still sets `legacy.Stores.Auth`/`Roles`.
- **Removed after it** (05-authn §11.2, removed in the first release after the cutover that no longer sets the members,
  behind the floor, as a minor, 05-storage §5S.3.13): `legacy.Stores.Auth`/`Roles`, `legacy.AuthReconciler`,
  `legacy.AuthAtomWriter`, the identity keys of `legacy.WriteLock`, `legacy.RBACAtomWriter` and their `legacytest` suites;
  `app.Options.LegacyAmbientScopes`; `LegacyFullScope` for any key still holding an empty list.

**Done-checks (09 §9.13 minimum; names fixed):**

- `TestHardeningCutoverRefusedByEveryOlderRelease` with H1's exact owner vector (PC-21-01's harness).
- `TestH1H2_DeferredWithoutFreshVerifiedArchive` and `TestH1H2_DeferredWhileSwapSetAsideExists` for H1.
- `TestH1_NoRawTokenOnDisk`: a canary session token planted **before** a fixture upgrade (so a retained checkpoint holds
  it) is in no file under the DB directory or a checkpoint directory after the cutover.
- `TestH1_AmbientScopesPreserved`: before/after parity matrix over every principal kind on `/metrics`,
  `/api/v1/auth/me`, `/api/v1/auth/sessions` and a reading-state route.
- `TestH1_ConfinedKeyStaysConfined`: an admin-owned `["fingerprint.worker"]` key, an fp-worker user's key and an
  empty-effective-set key keep exactly their pre-H1 answers; `TestWorkerKey_NoAmbientScopes` keeps passing.
- `TestLegacyRoleReachUnchanged` stays green on the legacy routes.
- When built (05-storage §5S.3.13): `TestCutoverCrashAtEveryPhase`, `TestCutoverRefusesOlderBuild`,
  `TestCutoverFloorRefusesSkippedStore`, and `TestCutoverGrantDeletesOnlyAfterRaised` if H1 declares a grant.
- The done-check states plainly that archives made before H1 still hold raw session and invite tokens.

**Release note (09 §9.13, verbatim, with the one re-login stated beside it):** "Backups made before this release still
contain sign-in tokens. They no longer work against this server, but restoring such a backup brings them back until they
expire; delete those backups or take new ones."

## Files (inventory; valid only for the in-place layout)

| Action | Path |
|---|---|
| create | `internal/books/store/h1_cutover.go`, `internal/books/store/h1_cutover_test.go` |
| modify | `internal/books/store/legacy_auth.go`, `internal/books/store/legacy_rbac.go`, `core/legacy/auth.go`, `internal/books/module.go`, `ci/stage.txt` |

If the stage table chooses the move to `c/auth/`/`c/rbac/`, the data migration belongs to the subsystem (`core/auth`,
`core/rbac`) with a books `storage.Grant` (05-storage §5S.3.13), and this list is wrong: STOP and ask for a re-plan.

## START HERE (only after step 0 passes)

```bash
REPO=<your clone>
TASK=pc-21-02-h1-identity-cutover
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list
npm ci --prefix web
git rev-parse --short HEAD
```

## Steps (after the stage table exists)

1. Re-run step 0; then read H1's stage table end to end and follow its PR series and Flip PR row. Where the table and this
   brief differ, the table wins; note each difference in the PR description.
2. Re-verify, at the base commit, that PC-21-01 landed: `grep -rn 'ErrCutoverNoArchive\|ErrCutoverResidue' core/storage | wc -l`
   (expected ≥ 2) and that `soak_record.py` refuses `--release H1` (expected: refused until this task adds H1's entry with
   the table's owner set).
3. Write the done-check tests above first; then the data migration, its preconditions, the done-phase clearing call, the
   ambient rule, the harness vector, the `soak_record.py` H1 entry and `ci/stage.txt` (`shipped_stage: H1`,
   `release: H1`, in the flip PR only; no `levels:` line).
4. Acceptance: every done-check above green; `make ci` and `make -C core ci` exit 0; `compat-rehearsal` and
   `unit-lifecycle` green on the flip PR.

## Out of scope

- Choosing the layout or the owners raised (step 0).
- Deleting the legacy identity members (the first release after the cutover, as a minor).
- H2 and H3; any change to the Basic gate or `enable_auth` precedence.

## Rollback

One-way. Before a production tag contains the flip: a revert PR (admitted with no soak row, 09 §9.8 revert row). After:
no binary swap goes below the H1 release; only a restore of the pre-cutover archive goes back (OWNER-31 keeps it off the
host), losing everything written since.

## Repo rules

- File headers; one `changelog.d/` fragment; conventional commits with `Stage-Release: H1 flip` (flip PR) or `H1 1`
  (content PRs) on every commit, ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`,
  never squash, never `--admin` past a required check.
- Concurrency (CLAUDE.md): the rewrite is a `DataMigration` with its own `tasks.Parallelism`; never a sequential loop
  over identity rows; partition by row so no two workers touch the same key.
- Destructive paths on fixtures only. Never print a token, key or session value; tests use canaries.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.

## Planner notes

- **SPEC-GATED:** no H1 stage table, and the move-or-rewrite-in-place decision is open (05-authn §11.2 and 05-storage
  §5S.3.13 lean opposite ways). This brief does not choose.
- **Files:** the inventory's files presuppose the in-place layout (`internal/books/store/h1_cutover.go`); the move layout
  would put the data migration in `core/auth`/`core/rbac` with a grant.
- **Depends-on:** no inventory row owns writing H1's stage table and the layout decision, which 09 §9.13 requires before
  H1's first PR; `PC-21-02` should depend on such an owner row.
