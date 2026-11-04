<!-- file: docs/agent-tasks/platform-core/PC-21-03-h2-secrets-cutover.md -->
<!-- version: 1.0.0 -->
<!-- guid: 994c1a06-d974-4029-a23a-d8b8f4b1b5dd -->
<!-- last-edited: 2026-11-04 -->

# PC-21-03 — H2 secrets cutover: plaintext blob secrets and the four encrypted rows move into c/secrets/; gap-fill keys removed; flip

> **SPEC-GATED.** H2's content is decided (05-config §1.12.5), but it has no stage table, and two points the table must
> settle are open. Step 0 checks for them; if they are missing, STOP and report. Nothing here chooses them.

| Field | Value |
|---|---|
| ID | `PC-21-03` |
| Tier | T4 Opus/xhigh–max — a one-way move of every third-party secret; a wrong step strands a credential or leaves a plaintext copy |
| Wave | W21 — release `H2` (or `H2H3` if the owner ships H2 and H3 together) |
| Depends on | `OWNER-31` |
| Priority / effort | P1 / XL |
| Spec | 09 §9.13 (H2 row, release-note text, "A fresh verified archive first"); 05-config §1.12.5 (H2), C16, §1.3.1 (`HEADAuthOffWritable`), §2 (`core/secrets`); 05-storage §5S.3.13; 05-backup §5B.3.9; 14 Q-E |
| Digest | `docs/plans/platform-core/digests/H1-H3.md` §1, §2, §3, §5 rules 1–8 and 11, §6, §7 |
| `Stage-Release:` | `H2 flip` (or `H2H3 flip`) on the flip PR, which sets `shipped_stage:` and `release:` together; `H2 1` (or `H2H3 1`) on content PRs (09 §9.2.1) |

## Step 0 — the spec text this task needs (STOP if missing)

```bash
S=docs/specs/platform-core
awk '/^## 9.13/,/^## Objections/' $S/09-abo-on-core.md | grep -c -E '^#{3,4} +H2(H3)?\b'      # HEAD 9b1545c1a: 0 -> STOP
awk '/^## 9.13/,/^## Objections/' $S/09-abo-on-core.md | grep -c '^| Flip PR'                 # HEAD: 0 -> STOP
```

Then read H2's stage table and STOP unless it states both of these:

1. **Whether H2 is the first cutover that raises `core`.** If it is, it also lifts books' legacy restore bound `{core, 0}`,
   so archive encryption is built and activated with it (09 §9.13; 05-backup §5B.3.9 lists the members:
   `backup.LegacyRestoreBound`, `backup.Manifest.Encrypted`, `…Recipients`, `backup.Archive.Encrypted`,
   `backup.Service.ExportPlain`, `backup.ErrDecrypt`, the age writer and reader). That depends on H1's undecided layout
   (H1 raises `core` only if it moves the identity families, PC-21-02 step 0) and on the owner's order of H1 and H2.
2. **Which owner's step removes what.** H2 writes `c/secrets/` (raising `core`) and removes fields from inside books'
   `setting:config_blob` row and the four encrypted rows (raising `books`). 05-storage §5S.3.13 shapes a cutover as a
   `DataMigration` of the subsystem that reads through the app's legacy store and deletes legacy rows through a
   `storage.Grant` whose modes are only `GrantRead` and `GrantReadDelete`; removing fields from a books row that stays is
   not a delete. The table must say who rewrites the blob, in what order against the two level raises, and which
   `legacy.SecretsStore` capability serves the reads.

**Missing spec text, exactly:** H2's stage table (Flip PR row, PR series, full done-check list), and in it items 1 and 2.

## What the spec already decides (binding)

**What moves** (05-config §1.12.5 H2; 09 §9.13 H2 row; 05-config C16):

- The books store's **plaintext blob secrets** (the secret-class fields of `setting:config_blob`; the list
  `TestSecretClassListMatchesLegacyStore` pins and `scripts/staging/secret_fields.txt` records, both from PC-09-04) **and**
  the **four HEAD-encrypted rows** — OpenAI key, Google Books key, Hardcover token, Basic password (HEAD:
  `internal/config/persistence.go:1955-1958` blanks them in the blob; same lines at 748fd787a) — move into `c/secrets/`,
  encrypted with the one keyring (key IDs, AAD; 05-config §2).
- Their legacy copies **and the gap-fill file's three keys** (`<db parent>/config.yaml`) are removed in the **same boot**.
- The `legacy.SecretsStore` capability goes with it, and with it every remaining `HEADAuthOffWritable` flag on a secret
  it moves (the Basic password and the kept integration secrets; each group's flag ends together, 05-config §1.3.1).
- An older build then has no copy to read, which the floor refuses. 14 Q-E is deferred to H2.
- **Owners raised:** `books` (the blob fields and secret rows) and `core` (`c/secrets/`) (05-config §1.12.5).
  `soak_record.py`'s table already says so (PC-21-01).

**What it cleans** (05-config §1.12.5; 05-storage §5S.3.13 items 1–4; machinery from PC-21-01):

- After the rewrite has committed and verified: every pre-cutover checkpoint of this store (its own attempt's and every
  retained or open one under `<db parent>/.mediacore-checkpoints/`, with their records) is deleted, and the rewritten key
  ranges are compacted with a WAL rotation, so no obsolete SST, WAL segment or checkpoint keeps a plaintext copy.
- Deferred, never failed, while a swap set-aside exists (`ErrCutoverResidue`) or without a fresh verified archive of this
  store (`ErrCutoverNoArchive`). Recovery from a failed H2 is the pre-cutover archive, the floor's restore point.
- Archives taken before H2 still hold every plaintext secret and the encrypted rows in ABO's cipher; H2 cannot reach them.

**Shape and rules** (05-storage §5S.3.13; digest §5 rules 1, 4, 5): `Breaking`, `BootBlocking`, `RaisesLevel` per owner,
`Source` = the legacy ranges moved, `After` = the owning module's highest step, `Parallelism tasks.Parallelism` required;
phases `folding`/`folded`/`raised`/`deleting`/`done`, level raised before the first legacy delete; `RequiresSelfServed`;
the books config store's write lock (`configBlobSaveMu`, held across core's commit since PC-09-04) held across every
read-modify-write of the blob (09 §9.9). A build that later deletes the legacy secrets store declares
`UpgradeFloor{Owner: <subsystem>, Requires: [<cutover>], InstallFirst: <cutover release>}`.

**Done-checks (09 §9.13 minimum; names fixed):**

- `TestHardeningCutoverRefusedByEveryOlderRelease` with the vector `{books, core}`.
- `TestH1H2_DeferredWithoutFreshVerifiedArchive` for H2; `TestH1H2_DeferredWhileSwapSetAsideExists` for H2.
- `TestH2_NoPlaintextSecretOnDisk`: a canary secret planted **before** a fixture upgrade, a restore and a reset (so
  retained checkpoints and set-asides hold it), the cutover run, then a scan of the DB directory, `.mediacore-checkpoints/`,
  `.mediacore-restore/` beside the DB, every recorded beside-itself destination, `<state_dir>/restore/` and
  `<db parent>/config.yaml` finds no canary.
- When built (05-storage §5S.3.13): `TestCutoverCrashAtEveryPhase`, `TestCutoverRefusesOlderBuild`,
  `TestCutoverFloorRefusesSkippedStore`, `TestCutoverGrantDeletesOnlyAfterRaised`.
- The done-check states plainly that archives made before H2 still hold every plaintext third-party secret, valid at its
  provider.

**Release note (09 §9.13, verbatim):** "Backups made before this release still contain plaintext third-party secrets.
Delete them or take new ones; if the backup directory may have been exposed, rotate those secrets."

## Files (inventory; re-derive once the stage table settles item 2)

| Action | Path |
|---|---|
| create | `internal/books/configstore/h2_cutover.go`, `internal/books/configstore/h2_cutover_test.go` |
| modify | `internal/books/configstore/write.go`, `core/legacy/secrets.go`, `core/secrets/`, `internal/config/aliases_gen.go`, `ci/stage.txt` |

## START HERE (only after step 0 passes)

```bash
REPO=<your clone>
TASK=pc-21-03-h2-secrets-cutover
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list
npm ci --prefix web
git rev-parse --short HEAD
```

## Steps (after the stage table exists)

1. Re-run step 0; follow H2's stage table (it wins over this brief; note differences in the PR).
2. Re-verify at the base: PC-21-01's members (`grep -rn 'ErrCutoverNoArchive\|ErrCutoverResidue' core/storage | wc -l` ≥ 2);
   the secret-class list (`grep -rn 'func TestSecretClassListMatchesLegacyStore' --include='*_test.go' . | wc -l` = 1;
   `test -f scripts/staging/secret_fields.txt`); the keyring (`ls core/secrets/*.go | wc -l` ≥ 1).
3. Tests first (the done-checks above), then the steps the table names, the harness vector and `ci/stage.txt`
   (`shipped_stage:` and `release:` to `H2`, or `H2H3`, in the flip PR only; never a `levels:` line).
4. Acceptance: every done-check green; `make ci` and `make -C core ci` exit 0; `compat-rehearsal` and `unit-lifecycle`
   green on the flip PR.

## Out of scope

- Deciding items 1 and 2 of step 0; H1 and H3; the settings-family cutover; the integrations cutover (05-metadata M1.10).
- File-only secrets HEAD never stores (05-config C16 "—" rows): operator layers only, untouched.

## Rollback

One-way. Before a production tag contains the flip: a revert PR. After: no binary swap below the H2 release; only a
restore of the pre-cutover archive (OWNER-32 keeps it off the host) goes back, losing everything written since.

## Repo rules

- File headers; one `changelog.d/` fragment; conventional commits with `Stage-Release: H2 flip` / `H2 1` (or the `H2H3`
  forms) on every commit, ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`,
  never squash, never `--admin` past a required check.
- Concurrency (CLAUDE.md): the move is a `DataMigration` with its own `tasks.Parallelism`; never a sequential loop.
- Never print a secret; tests use canaries. Destructive paths on fixtures only.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.

## Planner notes

- **SPEC-GATED:** no H2 stage table. Content is decided by 05-config §1.12.5, but the table must also settle (1) whether
  H2 builds and activates backup encryption (first cutover raising `core`; depends on H1's open layout and the owner's
  order) and (2) how a blob-field removal fits 05-storage §5S.3.13's read/delete-only grants.
- **Files:** the inventory places the migration in `internal/books/configstore/`; 05-storage §5S.3.13's "Shape" makes a
  cutover a data migration of the subsystem (`core/secrets` here). Re-derive the files from the stage table.
- **Depends-on:** no inventory row owns writing H2's stage table; `PC-21-03` should depend on such an owner row.
