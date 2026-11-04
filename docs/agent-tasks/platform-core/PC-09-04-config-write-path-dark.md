<!-- file: docs/agent-tasks/platform-core/PC-09-04-config-write-path-dark.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f9978cf-7385-454d-bb0d-03218d3c50d7 -->
<!-- last-edited: 2026-11-04 -->

# PC-09-04 — Config write path (dark): OverrideStore writes and Store.Apply through books' legacy store, in place, under configBlobSaveMu with the gap-fill write inside the hold

**ID:** PC-09-04 · **Wave:** W09 (S3, release `S3`) · **Priority:** P1 · **Effort:** XL
**Tier:** T4 Opus/xhigh-max — every secret must stay where HEAD keeps it, in HEAD's form; one wrong write strands a
rollback (an older build reads a value it cannot use) or reopens the Basic gate.
**Depends on:** PC-09-01 (`secrets`), PC-09-03 (the alias table; SPEC-GATED, see its brief).
**Stage-Release trailer:** `Stage-Release: S3 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S3.md` (Contracts: `Store`, `OverrideStore`, `Committer`,
package `legacy`, `CommitLocked`; Behaviour rules 9-16).
**Owning spec:** 05-config §1.3.3, §1.6, §1.12.2 (the whole section is the contract), §2.3 (`legacyStore`), §2.5,
§2.6, §2.9 (ABO-side tests); 05-storage §5S.3.2 (`CommitLocked`, `LocksHeld`, `OnCommitted`), §5S.3.11
(`WriteLock`, lock ranks: `legacy.LockConfigSettings` rank 160, secrets' keyed mutex rank 900); 09 S3 Mechanism;
09 §9.9; review disposition A-3 (no `CheckedWrites` on legacy rows; HEAD's lock discipline).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-09-04" -b agent/pc-09-04-config-write-path origin/main
cd "$REPO/../audiobook-organizer-pc-09-04"
git rebase origin/main
git worktree list
npm ci --prefix web
```

No `go work init`. Recommended split: a code-exploration pass mapping HEAD's save path first, then implementation,
then a code-auditing pass over the lock order before opening the PR.

## Goal

Build the S3 write path **dark**: `config.Store.Apply` → the kernel's `Committer` → one storage batch staged by the
routing store (books' legacy config store for keys with a legacy blob field, the native `c/config/` store for keys
with none) and committed by `storage.CommitLocked` under the locks the kernel resolved for
`legacy.LockConfigSettings` (HEAD's `configBlobSaveMu`), with books' gap-fill rewrite of `<db parent>/config.yaml`
registered as a post-commit step that runs inside the hold. Books' store also becomes the `legacy.SecretsStore` of
the four encrypted rows and the plaintext blob secrets, written in place in HEAD's cipher and shape. Nothing in ABO
calls `Apply` until the flip (PC-09-06); `PUT /api/v1/config` keeps HEAD's path in this PR.

## Exact files

Create: `core/config/apply.go`, `core/config/apply_test.go`, `core/legacy/secrets.go`,
`internal/books/configstore/write.go`, `internal/books/configstore/write_test.go`.
Modify: `internal/books/configstore/store.go` (PC-04-05, PC-07-05), `internal/config/persistence.go` (expose HEAD's
render and merge helpers to the store without changing HEAD's behaviour), `internal/server/handlers/system/handler.go`,
`scripts/staging/secret_fields.txt` (PC-02-05).
See "Planner notes" for files this row omits.

## Re-verify (expected at the plan checkout `f07f516be` unless marked; rerun at your base)

```bash
grep -n '^var configBlobSaveMu sync.Mutex' internal/config/persistence.go          # 1 hit (HEAD's save mutex)
grep -n 'configBlobSaveMu.Lock()' internal/config/persistence.go                   # 1 hit (inside SaveConfigToDatabase)
grep -n '^func SaveConfigToFile() error' internal/config/persistence.go            # 1 hit (the gap-fill merge, :121)
grep -n '"openai_api_key"\|"google_books_api_key"\|"hardcover_api_token"\|"basic_auth_password"' internal/config/persistence.go | head   # the 4 encrypted rows (05-config §2.2 anchors :1955-1958)
grep -rln 'SaveConfigToDatabase(' --include=*.go internal cmd | grep -v _test
#   internal/config/persistence.go, internal/config/update_service.go,
#   internal/server/handlers/scheduler_admin.go, internal/server/handlers/system/handler.go   (4 files)
grep -n 'func (h \*Handler) SetInternalFlag' internal/server/handlers/operations/handler.go   # 1 hit (:379)
grep -n 'ResetToDefaults\|SaveConfigToDatabase' internal/server/handlers/system/handler.go    # 3 hits at the plan checkout; PC-07-07 removes the reset ones (expect 0-1 at base)
# created by earlier rows (0 at the plan checkout; must exist at base, else STOP)
test -f internal/books/configstore/store.go && echo ok       # PC-04-05
grep -n 'func CommitLocked\|func LocksHeld\|func OnCommitted\|ErrNotLocked\|ErrLockOrder' -r core/storage/ | wc -l   # >= 5 (PC-07-01)
grep -n 'type OverrideStore interface\|type Committer interface\|func (s \*Store) Attach' -r core/config/ | wc -l     # 3 (PC-04-05 / PC-07-05)
grep -n 'type WriteLock interface' -r core/legacy/            # 1 hit (05-storage §5S.3.11; else add it here, see step 3)
test -d core/secrets && echo ok                               # PC-09-01
grep -n 'func LegacyAliases() \[\]config.Alias' internal/config/aliases_gen.go   # 1 hit (PC-09-03)
```

## Steps

1. **Tests first** (list below), against stubs. Run them red.
2. **`core/config/apply.go`.** `Apply(ctx, Patch, Actor)` per 05-config §1.3.3: validate every path (unknown →
   `ErrUnknownKey`; operator-locked → `*LockedError`; protection class vs `Actor` → `*ProtectedError{Path, Need}` /
   `ErrForbidden`; schema → `ErrInvalid`); build one `Commit{Set, Unset, Secrets, Gen}`; call `Committer.Commit`; on
   success publish the new generation (restart-bound keys go to `RestartPending`, not `Get`); on error publish nothing.
   At S3 the committer logs each committed change at INFO after the commit (section, changed key names, actor =
   `Override.By`; never a value) and stages **no** audit record (05-config §1.3.3; `audit` lands at S4).
3. **`core/legacy/secrets.go`** (package `legacy`, outside the API-stability gate), copied from 05-config §1.3.3:
   `ConfigSecrets`, `SecretsStore` (five methods, identical to `secrets`' unexported mirror), the constant
   `LockConfigSettings = "config.settings"`, and `WriteLock` if 05-storage's file did not already add it. Complete
   PC-09-01's `legacy_mirror_test.go` with the mutual-assignability assertion. Delete `legacy.ConfigExternalWriter`
   here only if no S2b caller remains; otherwise PC-09-06 deletes it (05-config §1.12.2 "deleted in the S3 PR").
4. **Committer.** The kernel's committer and routing store live in `core/internal/configstore` (05-config §1.3.3 code
   comments at lines 416-456 of the spec file). Implement `Commit`: one `storage.Batch`; the routing store stages each
   `Override` into books' store (`Alias.LegacyBlob != ""`) or the native `c/config/` store (no legacy name); each
   `secrets.Write` through `secrets.(*Store).Stage(b, w)`; the batch is committed with
   `storage.CommitLocked(ctx, b, d, stage, settingsLocks...)` where `settingsLocks` are what the kernel got at P4c from
   books' store's `WriteLocks([]string{legacy.LockConfigSettings})`. Config and secrets never call `WriteLocks`.
5. **Books' `write.go`.**
   - `Stage(ctx, b, set, unset)`: refuse unless `storage.LocksHeld(b, l)` for the store's settings lock (wrap
     `storage.ErrNotLocked`, stage nothing). Read the blob with `GetForUpdate`, decode with HEAD's codec (the
     `migrate*Blob` read transforms are pure), replace **only** the changed fields (`Alias.Revert` for converted keys;
     an unset writes this binary's default explicitly and never removes the field), re-encode in HEAD's JSON shape,
     put it into `b`. Never write a `FileOnly` key; never write a key with no legacy name into the blob.
   - `WriteLocks(names)` returns the `storage.Lock` that **is** HEAD's `configBlobSaveMu` (rank 160) for
     `legacy.LockConfigSettings`. HEAD's own `SaveConfigToDatabase` keeps taking the same mutex, so the old path and the
     new path serialise until the flip.
   - `Secrets()` returns the store's `legacy.SecretsStore`: `Kept(n)` is true exactly for the four encrypted rows and the
     plaintext blob secrets of 05-config §2.2 (false for the "—" rows); `Read` returns the primary copy (blob field or
     the encrypted row decrypted with HEAD's cipher) and the gap-fill secondary; `Write(b, n, v, clear)` refuses without
     the hold, writes a plaintext secret as its blob field in the same render and an encrypted one as its row in HEAD's
     cipher, and on `clear` deletes the row and removes the file key **during staging** (a failed removal fails the
     write); `KeyMaterial()` returns HEAD's key; `Check` trial-decrypts per 05-config §2.3.
   - **Post-commit step.** Every staging of the blob or a kept secret registers one step on the batch with
     `storage.OnCommitted(b, CommitStep{Key: "books.config.gapfill", Run: ...})` (key name coined here). The step runs
     HEAD's `SaveConfigToFile` merge (read, merge, temp file, fsync, rename) under the store's file mutex (a leaf lock),
     rendering only the twelve settings and three encrypted secrets HEAD's `SaveConfigToFile` manages from the rows and
     blob it **re-reads inside the hold**, never from the commit's values, and keeps every other key byte-for-byte. A
     failed step is logged (fail-open); a batch that failed to commit runs no step.
6. **`persistence.go`.** Export (or move behind an exported func in the same package) exactly the pieces the store needs:
   the blob render, the gap-fill merge with its managed-key list, and access to `configBlobSaveMu` as a lock value.
   HEAD's callers keep calling them; behaviour is byte-identical (prove with the existing persistence tests unchanged).
7. **Exclusive writer.** Add ABO's archtest `TestOnlyLegacyConfigStoreWritesSettingsRows` (the config instance of
   `TestLegacyFamiliesWrittenOnlyByLegacyStores`; put it where that umbrella lives at your base). At this dark stage
   HEAD's writers (`update_service.go`, `scheduler_admin.go`, `system/handler.go`, `persistence.go`) still write: list
   them in the test's allow-list with a comment that PC-09-06 empties it. `POST /operations/set-internal-flag` refuses
   (400, nothing written) every key of 05-storage §5S.9.1 row 7 (`setting:config_blob` and the four secret rows) and
   every key another store interface serves (09 §9.8): `TestSetInternalFlagRefusesConfigRows`.
8. **`system/handler.go`.** Remove any remaining direct blob write (`config.SaveConfigToDatabase`, `ResetToDefaults`
   followed by a save) that PC-07-07 left; the reset paths stage through `legacy.Maintenance.StageFactoryReset` and
   write no setting in the request (05-config §1.12.2 "The live settings are never written with defaults"). If none
   remains, leave the file unchanged and say so in the PR.
9. **`secret_fields.txt`.** Make it equal the generated secret-class list (the names `Kept` reports, plus the "—" rows,
   plus `plugins[].settings`); `TestSecretClassListMatchesLegacyStore` asserts equality (05-config §2.2).
10. Run `configtest.OverrideStoreSuite` and `legacytest.ConfigOverrideStoreSuite` against books' store with HEAD fixtures.
11. Headers, `changelog.d/` fragment, acceptance.

## Code contract (05-config §1.3.3, §2.3; 05-storage §5S.3.2 — names exact)

```go
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error)
type OverrideStore interface {
	Load(ctx context.Context) iter.Seq2[Override, error]
	Stage(ctx context.Context, b *storage.Batch, set []Override, unset []Path) error
}
type Committer interface{ Commit(ctx context.Context, c Commit) error }
type Commit struct { Set []Override; Unset []Path; Secrets []secrets.Write; Gen uint64 }
type Patch struct { Set map[Path]json.RawMessage; Unset []Path }
type Actor struct { Subject string; Interactive bool; Owner bool; Can func(perm string) bool }
type Result struct { Generation uint64; Applied, RestartPending []Path; PostCommit error }

// package legacy
type ConfigSecrets interface{ Secrets() SecretsStore }
type SecretsStore interface {
	Read(ctx context.Context, n secrets.Name) (secrets.LegacyCopies, error)
	Kept(n secrets.Name) bool
	Write(b *storage.Batch, n secrets.Name, v secrets.Value, clear bool) error
	KeyMaterial() []byte
	Check(ctx context.Context, r storage.Reader, key []byte) (secrets.LegacyCheck, error)
}
const LockConfigSettings = "config.settings"
type WriteLock interface{ WriteLocks(names []string) []storage.Lock }

// package storage
func CommitLocked(ctx context.Context, b *Batch, d Durability, stage func(ctx context.Context, b *Batch) error, locks ...Lock) error
func LocksHeld(b *Batch, l Lock) bool
func OnCommitted(b *Batch, step CommitStep) error
```

`secrets.LegacyCopies` and `secrets.LegacyCheck` are exported by PC-09-01. Errors: `ErrUnknownKey`, `ErrLocked`
(`*LockedError`), `ErrProtected` (`*ProtectedError{Path, Need}`), `ErrForbidden`, `ErrInvalid`, `ErrSealed`.

## TDD — write first

Named by the spec: `TestCoreWritesOnlyHEADGapFillKeys`, `TestSecretClassListMatchesLegacyStore`,
`TestSetInternalFlagRefusesConfigRows` (the row's "set-internal-flag refuses a store-interface family"),
`TestOnlyLegacyConfigStoreWritesSettingsRows`, `TestRoutedStore_OneStorageRule`, `TestStage_KeptNameWritesInPlace`
(books side), `TestGapFillClearNotUndoneByLateSet`, `TestGapFillStepOnEveryWriterPath` (the `Apply` and
`secrets.Put`/`Delete` paths here; `Registry.Put` and the notify rule store join at S7),
`TestGapFillFileRewriteSerialised`, `TestLegacyOpen_NoCorruptSecretRepair`, `TestApply_RefusesLocked` /
`_Protected` / `_Forbidden` / `_UnknownKey` / `_Invalid`, `TestApply_SecretAndOverrideOneBatch`,
`TestApply_CommitFailureNothingPublished`, `TestApply_RestartKeyNotPublished`, `TestLockRanksTotalOrder` (ABO, from
S3), `TestCommitLockedTakesEachLockOnceInOrder`, `TestLegacyUnsetWritesDefault` (inside `configtest.OverrideStoreSuite`).
Coined here: `TestOneFormat_ConfigSecretsReadByPrevBuild` — the row's "05-config's prev-build one-format test": a
settings save through `Apply` that changes a plaintext secret (the AcoustID key) and an encrypted one (the Basic
password) is read with the same values by the `prev` binary built from `PREV_REF` (09 §9.2.1 prev-build mechanism,
`extraction-gates`).

## Invariants (STOP if any cannot hold)

- Every secret stays where HEAD keeps it: nothing written to `c/secrets/` for a kept name; no sentinel; no copy.
- No core build writes a key into `<db parent>/config.yaml` that HEAD's `SaveConfigToFile` does not manage.
- The gap-fill step runs after the commit and before `configBlobSaveMu` is released; it renders from rows read inside
  the hold.
- Lock order: `configBlobSaveMu` (160) before secrets' keyed mutex (900); no books method that takes a covered mutex
  is called from a `stage` function. A deadlock or `ErrLockOrder` you cannot remove without reordering HEAD's own
  locking = STOP.
- `Open` still writes nothing (`legacytest.ConfigOverrideStoreSuite`).
- Dark: no production code path calls `Apply` in this PR (`grep -rn '\.Apply(ctx' internal/ cmd/ | grep -v _test`
  finds no new ABO caller).

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                                   # exit 0
(cd core && go test ./config/... ./legacy/... ./internal/configstore/... ./secrets/... -race -count=1)   # ok
go test ./internal/books/configstore/... ./internal/config/... ./internal/server/handlers/operations/... -race -count=1   # ok
go test ./... -run 'TestCoreWritesOnlyHEADGapFillKeys|TestSecretClassListMatchesLegacyStore|TestSetInternalFlagRefusesConfigRows|TestOnlyLegacyConfigStoreWritesSettingsRows|TestOneFormat_ConfigSecretsReadByPrevBuild' -count=1   # ok
make -C core ci                                                                                   # exit 0
make ci                                                                                           # exit 0
```

## Out of scope

Switching `PUT /api/v1/config` and `Mutate` to `Apply` (PC-09-06); the façade; the Basic gate (PC-09-05);
`integrations.Registry.Put` and the notify rule store (S7); the audit record (S4); any change to HEAD's cipher.

## Rollback

Binary swap: every byte this path writes is HEAD's representation in HEAD's rows; keys with no legacy name live only
in `c/config/`, which older builds never read (09 §9.2.1 row 2).

## Repo rules

Headers; `changelog.d/` fragment (no header). Conventional commits with `Stage-Release: S3 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`. `gh pr merge <n> --rebase`. Fixtures only: never run a write
against a store you did not create. Boot writers (none expected here) would extend the crash matrices in the same PR.

## Planner notes

- **Missing file:** the committer and routing store live in `core/internal/configstore` (05-config §1.3.3; the S3
  digest's moves table), but no row of the inventory creates or modifies that path. This brief puts the committer
  there; add `core/internal/configstore/` to this row's files.
- **Missing file:** `internal/server/handlers/scheduler_admin.go` and `internal/config/update_service.go` call
  `SaveConfigToDatabase` at the plan checkout; they are HEAD writers of the blob that the exclusive-writer ratchet must
  allow until the flip and that PC-09-06 must convert. Neither row lists them.
- **Possibly wrong file:** `POST /operations/set-internal-flag` is in `internal/server/handlers/operations/handler.go`
  (`SetInternalFlag`, :379 at the plan checkout), not in `internal/server/handlers/system/handler.go`, which the row
  lists; this brief edits the operations handler for that test and touches the system handler only for a remaining
  blob write.
- Depends on PC-09-03, which is SPEC-GATED.
