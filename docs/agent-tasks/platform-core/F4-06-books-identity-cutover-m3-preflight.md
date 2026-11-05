<!-- file: docs/agent-tasks/platform-core/F4-06-books-identity-cutover-m3-preflight.md -->
<!-- version: 1.1.2 -->
<!-- guid: f0c9fb51-61bc-4527-b59b-90c09a73f0ed -->
<!-- last-edited: 2026-11-05 -->

# F4-06 — Books' identity step M3, built dark: users, OAuth identities, the invite allow-list, keys and roles through the importers with every ID kept, the atom mapping and the ambient rule, the read-only cutover preflight, `storage cutover-blockers list|fix|delete`, merge journals rewritten

| Field | Value |
|---|---|
| ID | F4-06 |
| Release / flip | R2, identity flip F4 (dark PR: built and tested, **unregistered** until F4-08) |
| Tier | **T4** (Opus, xhigh–max): a one-way migration of every identity, with the stop-the-upgrade preflight |
| Wave | W29 |
| Depends on | F4-02, F4-01, F3-04, F3-06, F2-13, F2-15 |
| Owns gate tests | `TestCutoverStopsOnUnconvertibleIdentity`; `TestCutoverRefusedWhileMergeInProgress` (the preflight's refusal corpus); `TestIdentityCutover_FixtureFromR0`; `TestIdentityCutover_Resumable`; `TestIdentityCutover_RolesAndKeyScopes`; `TestIdentityCutover_WorkerKeyStaysConfined` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-06-identity-cutover-m3
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-02, F4-01, F3-04, F3-06, F2-13 and F2-15 are merged. If not, stop.

## Goal

Build books' identity cutover step M3 (03 §3.8; 05-authn §11; 05-rbac §6.3) as a books `datamigrate.DataMigration`
(`Cutover: true`, `Breaking`, `BootBlocking`, `After` M2, `Parallelism: tasks.Sequential("tens to hundreds of identity
rows")`), with its `Preflight`, `FixBlocker` and `DeleteBlocker`, and the core offline command
`<app> storage cutover-blockers list [--out <file>] | fix <key> --from <file> | delete <key> --confirm`. Owner decision
D13: an identity row that cannot be converted, or an account merge in progress, **stops the upgrade** before any write;
it is never quarantined. Do not register M3: F4-08 adds it to `internal/books/cutover/steps.go`.

## Spec

- 05-authn §11: the HEAD-family table (users with HEAD's `UserIsActive` predicate, `EmailVerified` from `email_source`,
  blanked hash → `PasswordResetRequired`; API keys with status normalisation, `revoked_at` → `revoked`, HEAD's one-shot
  expiry stamp for a non-revoked key with no expiry, scopes from 05-rbac §6.1/§6.3, never empty; `oauthid:`;
  `inviteallow:`; `role:`; sessions, invites and the bootstrap hash **deleted**; `abs_sess:` and the ABS JWT secret
  untouched; `usermerge:journal:` kept read-only), "Identity rows stop the upgrade", "Merges at the cutover", "Around the
  step"; §3.4 `Importer`; §10 rows `TestIdentityCutover_*` and the preflight cells.
- 05-rbac §6.1 (the 13 atoms and the every-atom row), §6.2 (seed IDs kept), §6.3 (all: seed rows deleted not imported,
  custom roles as `custom.<slug>`, keys' effective set, `core:admin` only for an admin owner, the ambient scopes
  `core:self`, `core:metrics.read`, `books:reading` iff not `Confined()`; one write per record with its audit
  `core.role.create` detail `imported: true`), §12 rows.
- 05-storage §5S.3.6 (`Preflight`, `FixBlocker`, `DeleteBlocker`, `Blocker`), §5S.3.13 (importer rules, Sensitive
  prefixes, **Cutover blockers** in full, the M3 row, Verify with `OrphanedRefs`, the shared `idx:` family, the tests
  `TestCutoverStopsOnUnconvertibleIdentity` and `TestCutoverRefusedWhileMergeInProgress`), §5S.9.1 rows 16 and 19
  ("Rewritten in place by" M3: the expiry flag and the merge journals).
- 09 §9.2.3 (the preflight), §9.2.6 (atoms, ambient rule, keys and roles); 06 §6.4.10 F4 row (`storage
  cutover-blockers`), §6.4.5 (`fix` and `delete` open the store without running any migration step, only on a store
  whose cutover has not started).

## Files

| Action | Path |
|---|---|
| create | `internal/books/cutover/m3/` (the step, decoders of HEAD's identity rows, the mapping, Verify, tests; a Go sub-package of `internal/books/cutover`, which 03 §3.5.1 allows: its new-path count counts root paths only) |
| create | `internal/books/cutover/preflight.go` (the step's `Preflight`, `FixBlocker`, `DeleteBlocker` wiring in package `cutover`) |
| create | `core/app/offline_cutover_blockers.go` (`storage cutover-blockers`) |
| modify | `core/app/offline.go` (append the `storage cutover-blockers` entry to `OfflineCommands`; nothing else) |
| create | `internal/books/testdata/r2corpus/m3/` (expected answers and cell fixtures for the M3 tests) |

## Re-verify at BASE

```bash
ls -d internal/books/cutover | wc -l                                                # 1 (F3-06)
ls internal/books/cutover/preflight.go internal/books/cutover/m3 2>/dev/null | wc -l   # 0
grep -c 'M3\|Identity' internal/books/cutover/stepids.go                           # >= 1: F3-06 reserved M3's step ID (use it)
grep -rn 'type Importer interface' core/auth/ core/rbac/ | wc -l                    # 2 (F4-02, F4-01)
grep -rn 'Preflight ' core/storage/datamigrate/*.go | wc -l                         # >= 1
grep -rn 'ErrCutoverBlocked' core/storage/ | wc -l                                  # >= 1 (F3-04)
ls internal/books/testdata/r2corpus/blockers | wc -l                                # >= 1 (F2-13's blocker and unfinished-merge corpus)
ls -d internal/books/testdata/r2corpus/blockers/*identity-corrupt-apikey* internal/books/testdata/r2corpus/blockers/*identity-corrupt-oauthid* | wc -l   # 2 (F2-13's two R0-written cells)
grep -n 'OfflineCommands' core/app/offline.go | head -1                             # the list you append to
grep -rn 'TestAtomTable_Golden' internal/books/ | wc -l                             # 1 (R1-15: books' atom table)
# HEAD's identity rows and predicates (HEAD f7ac5b11e; after R1-26 the database residue is internal/books/store/)
grep -c 'func UserIsActive' internal/auth/user_active.go                            # 1
grep -rn 'func.*Stamp\|apikey_expiry' internal/server/apikey_expiry_stamp.go | wc -l   # >= 1 (the one-shot stamp, :86)
ls internal/usermerge/journal.go | wc -l                                            # 1
```

## STOP conditions

1. `internal/books/cutover/stepids.go` reserves no M3 step ID: report; do not coin a second one.
2. Any identity row would be quarantined, dropped or imported without its user (D13).
3. The preflight writes anything, or runs on a store whose cutover has started.
4. `cutover-blockers fix|delete` would open a store whose cutover has started, run a migration step, or act on a key the
   current report does not name.
5. A key or role ends with an empty scope list, or a non-admin owner's key gets `core:admin`.

## Steps

1. Write the tests below first, over F2-12's R0-written corpus, F2-13's overlay and blocker corpus.
2. **Decoders** for HEAD's `u:`, `apikey:`, `oauthid:`, `inviteallow:`, `role:`, `sess:`, `invite:`, the bootstrap
   setting, the expiry flag and `usermerge:journal:` rows, with their `idx:` sub-prefixes (the moving sub-prefix list is
   generated from `keyfamilies.go` and the census, F2-15).
3. **Mapping** (05-authn §11 table, 05-rbac §6.3): statuses, hashes and epochs as stored; keys' scopes = effective set
   (atoms → native via books' atom table, an empty list = the owner's role set) ∩ owner's role set, plus the ambient
   scopes iff not `Confined()`; an admin owner's every-atom or empty-list key → `core:admin` plus every row's scopes; seed
   role rows deleted (P7 recreates them); other roles → `custom.<slug>` through `rbac.Importer`, users' role lists
   rewritten in the same step; an unknown atom grants nothing and is reported; a role whose every atom is unknown is
   quarantined (sealed) with the role-list entries naming it.
4. **The step** (its ID is the M3 constant from F3-06's `internal/books/cutover/stepids.go`; do not edit that file):
   per chunk, in one batch, import (`auth.Importer`, `rbac.Importer`), delete the HEAD rows consumed, the
   cursor; sessions, invites and the bootstrap hash deleted by rule; outside `Source`, delete HEAD's one-shot expiry flag
   and rewrite each kept `usermerge:journal:` once with every `invite:<token>` key image replaced by the token's SHA-256;
   `Sensitive` = `u:`, `idx:user:email:`, `apikey:`, `oauthid:`; an importer refusal of an identity row fails the chunk
   with `storage.ErrCutoverBlocked` (the defensive check, applied only to rows not yet imported). `Verify`: read =
   imported + quarantined + deleted-by-rule; every source range empty; every ID a books row references resolves or is
   counted in `OrphanedRefs`; 05-rbac's oracle (allow and deny before = after) is F4-07's test.
5. **Preflight** (`preflight.go`): read-only, through `LegacyRange` with HEAD-format readers (no M1 needed); decode every
   `u:`, `apikey:`, `oauthid:` row with its `idx:` rows, run the importer's validation without staging, and list every
   `usermerge:journal:` record at `applying` or `undoing`; each problem is a `datamigrate.Blocker` (step, key, family,
   reason, fix; never a value). `FixBlocker` stages the operator's repaired value, refused unless it then converts;
   `DeleteBlocker` deletes the row and, for a user, its keys, identities and index rows, returning the IDs.
6. **`storage cutover-blockers`** (`offline_cutover_blockers.go`, appended to `OfflineCommands` in `core/app/offline.go`;
   F4-08 adds its cobra wrapper under `cmd/`): refuses while the service holds the DB lock; opens
   the store without running a step (the explicit exception to `ErrOfflinePendingSteps`) only if the cutover has not
   started; `list` prints keys and reasons; `list --out <file>` also writes raw values (mode 0600, refused if the file
   exists, with the credential-hash warning); `fix`/`delete` act only on a key the current report names, commit one batch
   with a record `c/storage/cutover/blockers/<step-id>/<n>` = `{key, action, ids, reason, at, build}`, audited
   `core.storage.cutover_blocker_fix` / `core.storage.cutover_blocker_delete`; a merge in progress is not fixable here
   (the report says: finish or undo it on the pre-freeze release).
7. Headers, one `changelog.d/` fragment, Acceptance.

## Tests first (spec names)

`TestCutoverStopsOnUnconvertibleIdentity` (cells: a corrupt `u:`, `apikey:` or `oauthid:` row, each from F2-13's blocker
corpus (`blocker-identity-corrupt-apikey` and `blocker-identity-corrupt-oauthid` beside the corrupt `u:` cell, all
written by the R0-tag program; the main overlay quarantines no identity row); a decodable row the
importer rejects; a key or identity whose user does not exist; each refuses in the preflight with exit 78, a report
naming key and reason and no value, the store and directory byte-identical, no checkpoint; `fix` with a repaired value
or `delete` lets the next boot complete, a deleted user's referencing books rows counted in `orphaned_refs`),
`TestCutoverRefusedWhileMergeInProgress` (an `applying` and an `undoing` journal each refuse with nothing written,
naming the fix; a terminal journal does not block, its merge is imported, M3 rewrites the journal once with its invite
tokens hashed; the "undo answers 409 `merge_permanent`" cell lands with F4-08), `TestIdentityCutover_FixtureFromR0`,
`TestIdentityCutover_Resumable` (killed after each chunk commit and inside one; a kill between the user and key chunks
skips the preflight on the next boot and reports no key), `TestIdentityCutover_RolesAndKeyScopes`,
`TestIdentityCutover_WorkerKeyStaysConfined` (here: the imported worker key's checker is `Confined()` with no
`core:self`, `core:metrics.read` or `books:reading`, and satisfies only the worker routes' manifest requirements; the
live-route cell lands with F4-08), `TestUnknownAtom_ReportedNotGranted`, the M3 cells of `TestCutoverCrashAtEveryChunk`.

## Invariants

- Every user, role, key and identity ID is kept; `abk_` keys verify by their stored SHA-256.
- No raw session or invite token survives in the live store; the ABS families are untouched.
- No identity row is quarantined; a blocker writes nothing.
- One batch per chunk: no record exists in both forms.

## Acceptance

```bash
go vet ./internal/books/cutover/...                                                       # exit 0
go test -race -count=1 ./internal/books/cutover/...                                       # ok
go test -count=1 -run 'TestCutoverStopsOnUnconvertibleIdentity|TestCutoverRefusedWhileMergeInProgress|TestIdentityCutover_' ./internal/books/...   # PASS
(cd core && go test -race -count=1 ./app/...)                                             # ok
grep -c 'cutover/m3\|m3\.' internal/books/cutover/steps.go || true                      # 0: not registered (F4-08 adds M3 to steps.go)
make -C core ci                                                                           # exit 0
make ci                                                                                   # exit 0
```

## Out of scope

Registering M3, the gin chain, the live routes, the merge-undo 409 and the canary `TestCutover_NoRawTokenOnDisk`
(F4-08); the reach oracle (F4-07).

## Rollback

Revert the PR; nothing is registered.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(books/cutover): identity step M3 with the read-only preflight and cutover-blockers (unregistered)

Co-Authored-By: Claude <noreply@anthropic.com>
```
