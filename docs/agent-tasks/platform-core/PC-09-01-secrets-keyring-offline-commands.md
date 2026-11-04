<!-- file: docs/agent-tasks/platform-core/PC-09-01-secrets-keyring-offline-commands.md -->
<!-- version: 1.0.0 -->
<!-- guid: bf82b2f9-e1dd-4c09-bec3-376bbca6e064 -->
<!-- last-edited: 2026-11-04 -->

# PC-09-01 — core/secrets native keyring and the offline secrets commands; core/internal/keyedmutex

**ID:** PC-09-01 · **Wave:** W09 (S3, release `S3`) · **Priority:** P2 · **Effort:** L
**Tier:** T3 Opus/high — key material, AAD and the keyring file location on every install kind; a wrong
default loses every secret on a container recreate.
**Depends on:** OWNER-08 (the `S2br2` soak row and the P4 env census). Merges only after OWNER-08's row is in
`ci/soak.txt`; the PR may be opened and made green before that.
**Stage-Release trailer:** `Stage-Release: S3 1` on every commit.
**Normative digest:** `docs/plans/platform-core/digests/S3.md` (Contracts: `secrets`; Behaviour rule 14).
**Owning spec:** 05-config §2 (Part 2, `core/secrets`), §2.3, §2.4, §2.5, §2.6, §2.9; 09 S3 Moves; 09 §9.6 row 17;
05-storage §5S.3.15 (`set-install-id`); 06 §6.4.5 (`app.OfflineCommands`).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-09-01" -b agent/pc-09-01-secrets-keyring origin/main
cd "$REPO/../audiobook-organizer-pc-09-01"
git rebase origin/main
git worktree list          # confirm you are in the new worktree, not the primary checkout
npm ci --prefix web        # or `make web-deps` (PC-01-06)
```

Do **not** run `go work init` anywhere (CLAUDE.md). Verify with real `go build ./... && (cd core && go build ./...)`.

## Goal

Land `github.com/falkcorp/mediacore/secrets` (L2) as the platform's one keyring: the `Value`/`Ref`/`Name`/`Scope`
types, the native encrypted record store under `c/secrets/`, the keyring file `<state_dir>/secrets.keyring`, the
`legacyStore` mirror of `legacy.SecretsStore`, and the three offline commands (`secrets export-keyring`,
`secrets rotate-derive`, `secrets set-install-id`) exposed through `app.OfflineCommands` and wrapped by ABO's cobra
tree. Add the stdlib-only leaf `core/internal/keyedmutex` (a copy of ABO's `internal/keyedmutex`; ABO keeps its own).
Nothing in config reads a secret yet: the config write path (PC-09-04) and the façade (PC-09-06) are the consumers.

## Exact files

Create:
- `core/secrets/` — `secrets.go` (types, `Open`, `Store` methods), `keyring.go` (file format, atomic write via
  `os.Root`), `record.go` (record and cleared-marker codec, AAD), `scope.go`, `legacy.go` (unexported `legacyStore`
  mirror, `LegacyCopies`, `LegacyCheck`, `ReadLegacyKey`), `errors.go`, `metrics.go`, `export_test.go`
  (exports the test alias `LegacyStoreMirror`), `legacy_mirror_test.go` (package `secrets_test`, written so it can be
  completed in PC-09-04 when `legacy.SecretsStore` exists — see step 9), `secrets_test.go`.
- `core/internal/keyedmutex/keyedmutex.go`, `core/internal/keyedmutex/keyedmutex_test.go`.
- `cmd/secrets.go`, `cmd/secrets_test.go`.

Modify:
- `core/app/offline.go` (created by PC-07-08): add the three secrets commands to `OfflineCommands`.
- `.github/workflows/docker-image.yml` (created by PC-01-05): keyring assertions (step 10).
- See "Planner notes" for the boot wiring and crash-matrix files this row's file list omits.

## Re-verify before editing (run at your base; expected counts are at the plan checkout `f07f516be`)

```bash
# ABO's keyedmutex, the copy source: 1 type + 1 method
grep -n '^type Mutex struct\|^func (k \*Mutex) Lock(key string) (unlock func())' internal/keyedmutex/keyedmutex.go   # 2 hits
grep -c '^import "sync"' internal/keyedmutex/keyedmutex.go                                                            # 1 (stdlib only)
# HEAD's legacy cipher and key file stay ABO's (never touched here)
grep -n 'func EncryptValue\|func DecryptValue\|func InitEncryption' internal/database/settings.go                     # 3 hits (moves to internal/books/store at PC-07-04: re-run there)
grep -n 'ABK_ALLOW_ENCRYPTION_KEY_REGEN' cmd/root.go                                                                  # >=1 hit
# Files earlier waves create (0 at the plan checkout; must exist at your base, else STOP)
test -f core/app/offline.go && echo ok          # PC-07-08
test -f .github/workflows/docker-image.yml && echo ok   # PC-01-05
test -d core/storage && echo ok                 # PC-07-01 (storage.Batch, Keyspace, CommitLocked, LocksHeld)
grep -n 'func CommitLocked\|func LocksHeld\|ErrLegacyStoreType\|ErrNotLocked' -r core/storage/ | head                # >=4 hits
# Nothing named secrets exists yet in core
test -d core/secrets && echo EXISTS-STOP || echo absent                                                                # absent
```

Zero hits on a copy source or an existing-file check = STOP and report. `core/secrets` already existing = this task
is applied; run the acceptance checks instead.

## Steps

1. **keyedmutex copy.** Copy `internal/keyedmutex/keyedmutex.go` and its test to `core/internal/keyedmutex/`, change
   the header `file:` line and give the new files fresh guids. Same API: `type Mutex struct`,
   `func (k *Mutex) Lock(key string) (unlock func())`. It must import only `sync` (03 §3.3 row and R4; L0 leaf).
2. **Write the tests of §2.9 first** (TDD list below) against stub signatures, run them, watch them fail.
3. **Types** exactly as 05-config §2.3 (code contract below). `Value` redacts every format path; JSON and text
   marshal return `ErrNotSerializable`. `Name.Validate` enforces the grammar comment in §2.3.
4. **Keyring** (§2.3 "Formats"): `<state_dir>/secrets.keyring`, mode 0600, directory 0700, written atomically through
   `Options.StateDir` (`*os.Root`): temp file, fsync, rename. Format `mediacore-keyring/1`; exactly one `active` key per
   `use` (`data`, `derive`); `k0-legacy` (`use: legacy`) holds `SecretsStore.KeyMaterial()` opaque and is never used to
   encrypt or decrypt. Subkeys via `crypto/hkdf` SHA-256 with the two `info` strings of §2.3. The header carries
   `install_id` (written when the keyring is created or the field is absent; rewritten only when `Options.ResetBoot`).
5. **Open** follows §2.4 step 1 in order: assert the `legacyStore` mirror on a non-nil `Options.LegacyStore`
   (`storage.ErrLegacyStoreType`, boot fails, nothing written); check `c/secrets/meta` against `Options.AppID`
   (`ErrAppIDMismatch`); resolve the keyring (present → load, fix 0644 to 0600 or `ErrKeyfileInsecure`; absent with no
   records → generate `k1` and `d1`; absent with records → `ErrKeyMissing` unless `AllowKeyRegen`, which keeps the old
   records under their kid); record or replace `k0-legacy`. `Open` writes nothing but the keyring and, on a new store,
   `c/secrets/meta` (`TestNoStoreWriteBeforeCheckpoint` is PC-09-04's, keep it passable).
6. **Records** at `c/secrets/v/<name>`: `{"v":1,"kid","nonce","ct","at","by"}`, AES-256-GCM, AAD
   `"mediacore/secrets/v1\x00" + AppID + "\x00" + name`; cleared marker `{"v":1,"cleared":true,...}`. Read order of
   `Get`/`Meta` exactly as §2.3 "Read order" (six steps). A kept name (`SecretsStore.Kept`) never gets a record: `Put`,
   `Stage`, `Delete` call `SecretsStore.Write` in the batch; `Put` and `Delete` of a kept name commit through
   `storage.CommitLocked(ctx, b, d, stage, o.SettingsLocks...)` and take the keyed mutex **inside** `stage`, after the
   config lock (§2.6). `Stage` takes the caller's `*storage.Batch` (COR2-12).
7. **Scope, DeriveKey, rotate-derive state** (`verify` keys, `retire_after`, `RetireUnused`, `DeriveKeyFor`) per §2.3.
   `Open` never retires a key.
8. **Offline commands** in `core/app/offline.go`: append `secrets export-keyring --out <file>` (opens **no** store:
   reads the keyring file and its header's `install_id` from the state dir only; refuses an output looser than 0600;
   writes `<state_dir>/secrets.keyring.exported` with key IDs and time, no material), `secrets rotate-derive` and
   `secrets set-install-id --install-id <id>` (both through `kernel.OpenOffline`, refused while the server holds the
   store lock; `set-install-id` refuses an ID that differs from the `c/storage/install` marker read offline, and a
   missing keyring; it rewrites only the header). In `cmd/secrets.go` wrap each `app.Command` as a cobra subcommand of
   a `secrets` parent, exactly as ABO wraps `storage restore-checkpoint` (find that wrapper with
   `grep -rn 'restore-checkpoint' cmd/`). Extend `TestCLIExposesCoreOfflineCommands` (where it lives at your base:
   `grep -rn 'TestCLIExposesCoreOfflineCommands' cmd/ core/`) to list the three names.
9. **Mirror test.** `legacy.SecretsStore` lands in PC-09-04 (`core/legacy/secrets.go`). In this PR write
   `legacy_mirror_test.go` asserting `LegacyStoreMirror` has exactly the five methods of 05-config §2.3's
   `legacyStore` (reflection over the interface's method set and signatures); PC-09-04 adds the mutual-assignability
   assertion against `legacy.SecretsStore`.
10. **Image job** (09 S3 Done when; OPS5-7, R3-4). In `docker-image.yml`, after the existing run-with-seeded-volume
    step: assert `secrets.keyring` exists on the data volume next to the database with mode 0600, recreate the
    container from the same volume and assert the server boots and a value written before decrypts after (use the
    offline `secrets export-keyring` output's key IDs as the probe; no secret value is printed); a second run whose
    `--db` sits on a second volume mounted at `/config` asserts `secrets.keyring` on `/config`. No shipped file may set
    the canonical state-directory variable (09 S0 Deploy row; OPS2-23).
11. Bump headers, add a `changelog.d/` fragment (no header), run the acceptance block.

## Code contract (05-config §2.3; copy names exactly)

```go
package secrets // github.com/falkcorp/mediacore/secrets; imports stdlib, mediacore/storage, mediacore/metrics only

type Name string
func (n Name) Validate() error
type Value struct{ b []byte }
func NewValue(s string) Value
func (v Value) Reveal() string
func (v Value) Bytes() []byte
func (v Value) IsZero() bool
type Ref struct{ name Name; r Resolver }
func RefTo(n Name) Ref
func (r Ref) Resolve(ctx context.Context) (Value, error)
type Resolver interface {
	Get(ctx context.Context, n Name) (Value, Meta, error)
	Meta(ctx context.Context, n Name) (Meta, error)
}
type Source uint8   // SourceStored, SourceEnv, SourceFile, SourceLegacy
type LegacyForm uint8 // LegacyNoForm, LegacyPlain, LegacyEncrypted, LegacySecondary
type Write struct { Name Name; Value Value; Delete bool; By string }
type Options struct {
	StateDir *os.Root; KeyFile string; Keyspace storage.Keyspace; AppID string; AllowKeyRegen bool
	LegacyStore any; SettingsLocks []storage.Lock; ResetBoot bool
	Logger *slog.Logger; Metrics *metrics.Scope; Now func() time.Time
}
func Open(ctx context.Context, o Options) (*Store, error)
func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
func (s *Store) Stage(b *storage.Batch, w Write) error
func (s *Store) Ephemeral(n Name, v Value, src Source)
func (s *Store) EphemeralDefault(n Name, v Value, src Source)
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]
func (s *Store) KeyIDsReferenced(ctx context.Context, r storage.Reader) ([]string, error)
func (s *Store) DeriveKey(info string) ([]byte, error)
func (s *Store) ActiveDeriveID() string
func (s *Store) DeriveKeyFor(kid, info string) ([]byte, error)
func (s *Store) RetireUnused(ctx context.Context, inUse func(ctx context.Context, kid string) (bool, error)) (retired []string, err error)
func (s *Store) Scope(prefix string) Scope
func (s *Store) Close() error
func ReadLegacyKey(stateDir *os.Root, keyFile string) (material []byte, ok bool, err error)
var ErrNotSet, ErrKeyUnavailable, ErrCorrupt, ErrUnbound, ErrNotSerializable, ErrKeyfileInsecure,
	ErrKeyMissing, ErrOutOfScope, ErrAppIDMismatch error // texts as in §2.3
```

The unexported `legacyStore` interface (`Read`, `Kept`, `Write`, `KeyMaterial`, `Check`) and `LegacyCopies`,
`LegacyCheck` are copied verbatim from §2.3. Metrics: the four families of §2.8 on scope `mediacore.secrets`.
Config keys `core.secrets.key_file`, `core.secrets.allow_key_regen` (§2.7) are declared by the kernel's secrets
section; `ABK_ALLOW_ENCRYPTION_KEY_REGEN` is an ABO window alias that PC-09-03 generates (not here).

## TDD — write these first (names from 05-config §2.9)

`TestValue_NeverFormats`, `TestAAD_SwapBetweenNamesFails`, `TestOpen_AppIDMismatchRefuses`,
`TestOpen_MissingKeyringWithRecordsRefuses`, `TestOpen_RegenKeepsOrphans`, `TestOpen_KeyringPermissions`,
`TestDeriveKey_IndependentOfDataKey`, `TestScope_RefusesOutsidePrefix`, `TestLegacyKeyMaterial_RecordedOpaque`,
`TestGet_ReadOrder`, `TestStage_KeptNameWritesInPlace` (with a fake `legacyStore`), `TestExportKeyring_OpensNoStore`,
`TestKeyIDsReferenced_ReportsLegacyRows`, `TestLegacyCheck_TrialDecryptForeignArchive`, `TestEphemeral_NeverPersisted`,
`TestFingerprint_StableNotReversible`, `TestExportKeyring_ClearsHealthWarning`, `TestRotateDerive_VerifyThenRetire`,
`TestRotateDerive_NotRetiredWhileArchiveUsesIt`, `TestRotateDerive_JournalStaysVerifiable`,
`TestOpen_LegacyStoreTypeMismatchFails`, `TestOpen_ResetBootRewritesInstallID`,
`TestSetInstallID_RestoresLineageOffline`, `TestStage_TakesBatchPointer`. ABO: `TestCLIExposesCoreOfflineCommands`
extended. Use `testing/synctest` for clock-driven cases (`retire_after`).

## Invariants (STOP if any cannot hold)

- No key material in any log line, error string, metric label or HTTP response (`TestValue_NeverFormats`).
- `secrets` imports only stdlib, `mediacore/storage`, `mediacore/metrics` (`go list -deps ./core/secrets` shows no
  other mediacore or ABO package). A needed import outside that set = STOP.
- `export-keyring` never opens the store and is not a foreign open.
- `k0-legacy` is never used to encrypt or decrypt.
- Nothing is ever deleted automatically on key loss.

## Acceptance (commands and expected output)

```bash
go build ./... && (cd core && go build ./...)                          # exit 0
(cd core && go test ./secrets/... ./internal/keyedmutex/... -count=1 -race)   # ok ... for both packages
go test ./cmd/... -run 'TestCLIExposesCoreOfflineCommands' -count=1    # ok
(cd core && go list -deps ./secrets | grep -E 'falkcorp/(mediacore|audiobook-organizer)/' | grep -v -E 'mediacore/(secrets|storage|metrics)($|/)')   # no output
(cd core && go list -deps ./internal/keyedmutex | grep falkcorp)      # only .../internal/keyedmutex itself
go run . secrets --help | grep -E 'export-keyring|rotate-derive|set-install-id' | wc -l   # 3
make -C core ci                                                         # exit 0
make ci                                                                 # exit 0
```

The `docker-image` required check is green on the PR with the step-10 assertions visible in its log.

## Out of scope

The config write path, `legacy.SecretsStore` and books' implementation (PC-09-04); the façade and viper removal
(PC-09-06); rotation of the data key and prune (deferred, §2.11 item 1); a UI recovery kit (§2.11 item 2).

## Rollback

Binary swap. The keyring file and `c/secrets/meta` are read by no older build; HEAD's `.encryption_key` is left
untouched (§2.4 step 1 last bullet), so older builds keep decrypting the four legacy rows.

## Repo rules

File headers on every new or changed file (bump version and `last-edited`); `changelog.d/` fragment with no header.
Conventional commits, each with `Stage-Release: S3 1` and ending with `Co-Authored-By: Claude <noreply@anthropic.com>`.
`gh pr create`, then `gh pr merge <n> --rebase` once the gate allows (never squash, never `--admin` past a required
check). CLAUDE.md concurrency: no library-scale loop is added here. From S2b every boot-time durable write extends the
crash-matrix instance that owns it in the same PR (09 §9.3).

## Planner notes

- **Boot wiring missing from the file list.** 05-config §2.4 puts `secrets.Open` at P4d step 1(ii) and 09 S3's done
  list has the image job assert the keyring on the data volume, yet the row lists no `core/kernel/` file and the
  digest (rule 14) requires the keyring and `c/secrets/meta` rows in 06 §6.5.1 plus `TestBootCrashMatrix` and
  `TestBootCrashMatrix_ABO` in the same PR that adds the boot writer. With the row's files the image-job done-test
  cannot pass. Coordinator choice: (a) add `core/kernel/` (P4d step 1(ii) call with `LegacyStore: nil` until PC-09-04)
  and the two crash-matrix files to this row, or (b) move the step-10 assertions and the boot call to PC-09-04 or the
  flip PC-09-06. Until decided, implement steps 1-9 and step 10's job step; if the job cannot pass without a kernel
  edit, STOP and report rather than editing `core/kernel/` unasked.
- `legacy_mirror_test.go` can only be completed once `legacy.SecretsStore` exists (PC-09-04); the mutual-assignability
  half belongs there.
