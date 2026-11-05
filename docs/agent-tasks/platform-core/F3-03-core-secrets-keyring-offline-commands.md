<!-- file: docs/agent-tasks/platform-core/F3-03-core-secrets-keyring-offline-commands.md -->
<!-- version: 1.1.2 -->
<!-- guid: b8fef3e3-a3ab-4134-ba71-0a78a133d3d3 -->
<!-- last-edited: 2026-11-05 -->

# F3-03 — Core secrets and the keyring: `secrets.Open` after the checkpoint, `secrets.Importer`, `export-keyring`, `rotate-derive`, `set-install-id`, the secrets health tokens, `core/internal/keyedmutex`

| Field | Value |
|---|---|
| ID | F3-03 |
| Release / flip | R2, config flip F3 (dark PR) |
| Tier | **T4** (Opus, xhigh–max): the keyring holds every secret, and `secrets.Open` is a durable boot write |
| Wave | W26 |
| Depends on | F3-01 |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-03-core-secrets
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F3-01 is merged. If not, stop.

## Goal

Build `github.com/falkcorp/mediacore/secrets` (L2) exactly as 05-config Part 2 specifies: the redacting `Value`,
`Ref`, `Resolver`, `Scope`, `Store` with `Open`/`Get`/`Meta`/`Put`/`Delete`/`Stage`/`StageUnreadable`,
`Importer`, `Ephemeral`, `All`, `KeyIDsReferenced`, the derive-key API with `RetireUnused`, the keyring file
format, the record formats, the three offline commands, the health tokens, and core's own L0 copy of ABO's keyed
mutex. Give the kernel its P4b open helper (`core/kernel/secrets.go`), but **do not call it from the boot sequence**:
F3-07 adds that call for every app, hello included, so this PR needs no dark gate and changes no boot. Declare the
two `secrets.Ref` keys in the kernel's core-section list.

## Spec

- 05-config C8, C13, C16; Part 2: §2.1–§2.11 (all), in particular §2.3 (API and formats), §2.4 (lifecycle, the
  state-file table, backups and loss, `export-keyring`, `set-install-id`, how the CLI reaches the commands, the dry
  run's throw-away keyring), §2.5–§2.9.
- 06 §6.5.1 "Write rule" (`c/secrets/meta` written at the start of P4b), the P4 row, the boot-writer table (add a
  row for `secrets.Open`'s keyring write and `c/secrets/meta`); 06 §6.4.5 (`OfflineCommands`: the three secrets
  commands from F3; `export-keyring` opens no store; `rotate-derive` and `set-install-id` go through
  `kernel.OpenOffline`); 06 §6.4.10 F3 row (`module.Deps.Secrets`).
- 04 §2.3 and §8.1 F3 row (`core/internal/keyedmutex` is an L0 leaf importing only the standard library; ABO keeps
  `internal/keyedmutex`).
- 05-storage §5S.3.12 "Scratch copies" (the dry run's throw-away keyring lives there), §5S.3.15 (`ResetBoot`).
- 05-health: the tokens `secrets_undecryptable`, `secrets_keyring_not_exported`, `derive_key_retire_blocked` on the
  kernel component `core.secrets` that F2-10 registered.

## Files

| Action | Path |
|---|---|
| create | `core/secrets/` (package, tests) |
| create | `core/internal/keyedmutex/` (stdlib-only copy of ABO's `internal/keyedmutex`, with its test) |
| create | `core/kernel/secrets.go` (P4b open, keyspace `c/secrets/` spec, `ResetBoot`, health reporting on `core.secrets`, the boot-writer row) |
| create | `core/app/offline_secrets.go` (`secrets export-keyring`, `secrets rotate-derive`, `secrets set-install-id` as `app.Command`s) |
| modify | `core/app/offline.go` (append the three commands to `OfflineCommands`' list; nothing else) |
| modify | `core/kernel/core_sections.go` (the two `secrets.Ref` keys only: `core.auth.oidc.<id>.client_secret` and `core.http.basic_gate.password`) |

`core/secrets/types.go` exists at BASE (F3-01 created it with `Ref`, `Resolver`, `Write`, `Meta`). Grow the package
around it; change those four declarations only if 05-config §2.3 differs from them, and say so in the PR.
`core/kernel/core_sections.go` is also edited by F3-02 (next wave) and F2-04 (later): touch only the two secret keys.
Not yours: `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/module/` (F3-07 wires the call, `Importers.Secrets`
and `Deps.Secrets`).

## Re-verify at BASE

```bash
ls core/secrets/*.go 2>/dev/null | wc -l          # expect 1 (F3-01's types.go)
grep -c 'type Ref struct\|type Resolver interface\|type Meta struct\|type Write struct' core/secrets/types.go   # expect 4
grep -c 'secrets.Ref' core/kernel/core_sections.go   # expect 0 (you add two)
grep -c 'basic_gate' core/kernel/core_sections.go    # expect >= 1 (F2-09's section)
ls -d core/internal/keyedmutex 2>/dev/null | wc -l   # expect 0
grep -c 'func (k \*Mutex) Lock' internal/keyedmutex/keyedmutex.go   # expect 1 (HEAD: line 32; the source to copy)
grep -c '^import "sync"' internal/keyedmutex/keyedmutex.go          # expect 1 (stdlib only)
grep -rn 'core.secrets' core/kernel/health.go | wc -l                # expect >= 1 (F2-10's component)
grep -rn 'func OpenOffline' core/kernel/ | wc -l                     # expect 1 (R1-25)
grep -rn 'func OfflineCommands' core/app/ | wc -l                    # expect 1
grep -rn 'Checkpoint\|checkpoint' core/kernel/migrate.go | wc -l     # expect >= 1 (F2-06/R1-23: the P4a checkpoint precedes P4b)
# HEAD's cipher and guard that M2 (F3-06) freezes; you do NOT read them (HEAD f7ac5b11e; after R1-26 the database
# residue lives at internal/books/store/)
grep -rn 'func DecryptValue\|func EncryptValue' internal/database/settings.go internal/books/store/settings.go 2>/dev/null | wc -l   # expect 2
grep -c 'func guardAgainstKeyRegeneration' cmd/root.go              # expect 1 (HEAD: line 760)
```

## STOP conditions

1. On this PR's `main`, any boot (ABO's or hello's) would create `secrets.keyring`, write `c/secrets/meta` or any
   `c/secrets/` row. No boot calls `secrets.Open` until F3-07 (`TestNoStoreWriteBeforeCheckpoint` and both crash
   matrices unchanged). Your tests call `Open` directly.
2. A secrets write is placed before the P4a checkpoint (06 §6.5.1 write rule).
3. Anything would read HEAD's `.encryption_key`: only M2 reads it (05-config C17).
4. You need a file outside the list.

## Steps

1. Write the tests below first.
2. `core/internal/keyedmutex`: copy ABO's `Mutex` (`Lock(key string) (unlock func())`, reference-counted) with its
   test; imports only `sync`. Do not change ABO's package.
3. `Value`, `Ref`, `Resolver`, `Name`, `Source`, `Meta`, `Write`, `Options`, errors: copy §2.3 exactly. Every
   formatting path of `Value` prints `[REDACTED]`; `MarshalJSON`/`MarshalText` return `ErrNotSerializable`.
4. **Keyring** `<state_dir>/secrets.keyring` (0600; directory 0700; temp + fsync + rename through `os.Root`):
   `{"format":"mediacore-keyring/1","keys":[…]}`, exactly one `active` key per `use` (`data`, `derive`), the header
   `install_id` written when absent and rewritten only on `ResetBoot`. Subkeys by HKDF-SHA256: data info
   `"mediacore/secrets/aes-256-gcm/v1"`, fingerprint info `"mediacore/secrets/fingerprint/v1"`.
5. **Records** at `c/secrets/v/<name>`: `{"v":1,"kid":…,"nonce":…,"ct":…,"at":…,"by":…}`, AES-256-GCM, AAD =
   `"mediacore/secrets/v1\x00" + AppID + "\x00" + name`; cleared marker `{"v":1,"cleared":true,…}`; unreadable marker
   `{"v":1,"unreadable":"key"|"corrupt",…}` written only by `StageUnreadable`. Store metadata `c/secrets/meta` =
   `{"app_id":…,"keyring_format":"mediacore-keyring/1","created":…}`; mismatch → `ErrAppIDMismatch`.
6. **Open** (§2.4 step 1): keyring present → load, fix 0600 or `ErrKeyfileInsecure`; absent with no stored records →
   generate `k1` and `d1`; absent with records → `ErrKeyMissing` unless `AllowKeyRegen` (then new keys, old records
   kept with `Meta.Err = ErrKeyUnavailable`, nothing deleted). `ResetBoot` rewrites the header `install_id` to the
   fresh `c/storage/install` marker. A dry run passes a `KeyFile` inside its scratch directory and never touches the
   state directory's keyring (F3-04 calls it that way).
7. **Read order** (`Get`, `Meta`): operator `Ephemeral` value (locks the key) → record (`ErrNotSet` for a cleared
   marker; `Meta.Err` for an unreadable marker) → `ErrNotSet`. `Put`/`Stage`/`Delete` serialize per name through
   `core/internal/keyedmutex`; `Stage(b *storage.Batch, w Write)` binds `c/secrets/` to the caller's batch.
8. **Importer** (`ImportSecret`, `ImportUnreadable`): seals with the active data key, stages into the caller's batch,
   never commits, create-only (an existing record with other bytes → `storage.ErrImportConflict`). F3-07 binds F3-04's
   quarantine sealer seam (`Seal(ctx, b, name, raw)`, declared in `core/storage` because `storage` cannot import
   `secrets`) to `ImportSecret` through a small adapter; nothing here implements that seam.
9. **Derive keys**: `DeriveKey`, `ActiveDeriveID`, `DeriveKeyFor`, `Scope.DeriveKey(info)` prefixes `info` with the
   scope; `RetireUnused(ctx, inUse)` decides retirement in memory only, never inside `Open`.
10. **Offline commands** (`offline_secrets.go`, appended to `OfflineCommands` in `core/app/offline.go`): `secrets export-keyring --out <file>` opens **no store** (reads the
    keyring and its header `install_id` from the state dir, refuses a destination looser than 0600, writes
    `<state_dir>/secrets.keyring.exported` with the key IDs and time); `secrets rotate-derive` (through
    `kernel.OpenOffline`, refused while the server holds the store lock; adds `d<n+1>` active, previous `verify` with
    `retire_after` = now + max(backup retention, longest refresh-token TTL) read from config); `secrets
    set-install-id --install-id <id>` (offline, read-only marker read; refuses an ID that differs from the store's
    `c/storage/install` marker and a missing keyring; rewrites only the header).
11. **Kernel helper** (`core/kernel/secrets.go`): the `c/secrets/` keyspace spec and an unexported P4b helper
    (name coined here: `openSecrets`) that calls `Open` with the state dir, `AppID`, `ResetBoot` and the two §2.7
    keys, and returns the store for F3-07 to hand on as `module.Importers.Secrets` and `module.Deps.Secrets`; a
    health reporter raising `secrets_undecryptable` (count of `Meta.Err` records), `secrets_keyring_not_exported` (a
    key missing from the export record) and `derive_key_retire_blocked` on `core.secrets`. Unit-test the helper with
    a scratch state dir. **Do not** call it from `boot.go`, add the boot-writer row or extend `TestBootCrashMatrix`:
    the boot write starts with F3-07, which adds the call, the boot-writer row and the crash-matrix cells together
    (09 §9.3: the PR that adds a boot writer extends the crash matrix).
12. **Secret keys** (`core/kernel/core_sections.go`; 05-config §2.2, §1.12.3 rows `oauth_*_client_secret` and
    `basic_auth_password`; 05-http-api §10): add `client_secret secrets.Ref` to the `core.auth.oidc.<id>` entry and
    `password secrets.Ref` to `core.http.basic_gate`, at `ProtectAdmin` (05-config §1.3.7 `FieldSignIn`). No
    `reload:"boot"` on either (`Register` refuses it). The password is a plain operator-locked key, not `FailClosed`.
13. Headers, one `changelog.d/` fragment, Acceptance.

## Code contract (05-config §2.3; copy exactly)

```go
func NewValue(s string) Value
func (v Value) Reveal() string
func (v Value) Bytes() []byte
func (v Value) IsZero() bool
func RefTo(n Name) Ref
func (r Ref) Resolve(ctx context.Context) (Value, error)
type Resolver interface {
	Get(ctx context.Context, n Name) (Value, Meta, error)
	Meta(ctx context.Context, n Name) (Meta, error)
}
func (s *Store) Scope(prefix string) Scope
func Open(ctx context.Context, o Options) (*Store, error)
func (s *Store) Get(ctx context.Context, n Name) (Value, Meta, error)
func (s *Store) Meta(ctx context.Context, n Name) (Meta, error)
func (s *Store) Put(ctx context.Context, n Name, v Value, by string) (Meta, error)
func (s *Store) Delete(ctx context.Context, n Name, by string) error
func (s *Store) Stage(b *storage.Batch, w Write) error
func (s *Store) StageUnreadable(b *storage.Batch, n Name, reason string, by string) error
type Importer interface {
	ImportSecret(ctx context.Context, b *storage.Batch, w Write) error
	ImportUnreadable(ctx context.Context, b *storage.Batch, n Name, reason, by string) error
}
func (s *Store) Importer() Importer
func (s *Store) Ephemeral(n Name, v Value, src Source)
func (s *Store) All(ctx context.Context) iter.Seq2[Meta, error]
func (s *Store) KeyIDsReferenced(ctx context.Context, r storage.Reader) ([]string, error)
func (s *Store) DeriveKey(info string) ([]byte, error)
func (s *Store) ActiveDeriveID() string
func (s *Store) DeriveKeyFor(kid, info string) ([]byte, error)
func (s *Store) RetireUnused(ctx context.Context, inUse func(ctx context.Context, kid string) (bool, error)) (retired []string, err error)
func (s *Store) Close() error
```

`Options` fields `StateDir *os.Root`, `KeyFile`, `Keyspace`, `AppID`, `AllowKeyRegen`, `ResetBoot`, `Logger`,
`Metrics`, `Now`; errors `ErrNotSet`, `ErrKeyUnavailable`, `ErrCorrupt`, `ErrUnbound`, `ErrNotSerializable`,
`ErrKeyfileInsecure`, `ErrKeyMissing`, `ErrOutOfScope`, `ErrAppIDMismatch`; config keys `core.secrets.key_file`
(default `secrets.keyring`) and `core.secrets.allow_key_regen` (default `false`), both `ProtectOperator` (§2.7);
metrics §2.8 on scope `mediacore.secrets`. Imports: stdlib crypto, `storage`, `metrics` only. This row adds core's
`secrets` metrics scope, so it adds `core/secrets/testdata/series.golden` for that scope, asserted by
`metricstest.AssertSeries` (R1-10's `core/metrics/metricstest`; 05-metrics §5 item 5).

## Tests first (05-config §2.9)

`TestValue_NeverFormats`, `TestAAD_SwapBetweenNamesFails`, `TestOpen_AppIDMismatchRefuses`,
`TestOpen_MissingKeyringWithRecordsRefuses`, `TestOpen_RegenKeepsOrphans`, `TestOpen_KeyringPermissions`,
`TestDeriveKey_IndependentOfDataKey`, `TestScope_RefusesOutsidePrefix`, `TestGet_ReadOrder`,
`TestStageUnreadable_ReadsReenter`, `TestExportKeyring_OpensNoStore`, `TestEphemeral_NeverPersisted`,
`TestFingerprint_StableNotReversible`, `TestExportKeyring_ClearsHealthWarning`, `TestRotateDerive_VerifyThenRetire`,
`TestRotateDerive_NotRetiredWhileArchiveUsesIt`, `TestRotateDerive_JournalStaysVerifiable` (with a fake in-use
check; backup's real one lands at F8), `TestOpen_ResetBootRewritesInstallID`, `TestSetInstallID_RestoresLineageOffline`,
`TestStage_TakesBatchPointer`; `TestImporterCreateOnly` (05-storage's importer rule, run over `secrets.Importer`);
the secrets half of 05-config's `TestNoStoreWriteBeforeCheckpoint`; `TestKeyedMutex_SerializesPerKey` (name coined
here) for the copy; `TestSecretsSeries_Golden` (name coined here: `metricstest.AssertSeries` over the `secrets` scope).

## Invariants

- Exactly one `active` key per use; the format carries key IDs from v1 (no format change when rotation lands).
- No plaintext secret is written under `c/` or into any log, error or JSON body.
- `Open` deletes nothing, ever.
- The keyring is the install's only key artifact; no identity file.
- `export-keyring` works while the server runs (opens no store).

## Acceptance

```bash
(cd core && go vet ./secrets/... ./internal/keyedmutex/... ./kernel/... ./app/...)       # exit 0
(cd core && go test -race -count=1 ./secrets/... ./internal/keyedmutex/... ./kernel/... ./app/...)   # ok
(cd core && go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/keyedmutex)   # one line: github.com/falkcorp/mediacore/internal/keyedmutex
(cd core && go test -count=1 -run 'TestBootCrashMatrix' ./...)                            # PASS, unchanged
go test -count=1 -run 'TestBootCrashMatrix_ABO' ./...                                      # PASS, unchanged for ABO
(cd core && grep -rn 'openSecrets(' kernel/ | grep -v _test.go | grep -vc 'func openSecrets')   # 0: no boot call yet
make -C core ci                                                                            # exit 0
make ci                                                                                    # exit 0
```

## Out of scope

M2 and HEAD's key file (F3-06); calling `secrets.Open` from the boot sequence, the boot-writer row, the crash-matrix
cells, `Importers.Secrets`, `Deps.Secrets` and the façade (F3-07); `Committer` staging of
secret writes is F3-01's and only calls `Stage`; backup's in-use check (F8-01); data-key rotation and prune (deferred,
§2.11 item 1).

## Rollback

Revert the PR. ABO never opened the keyring before F3-07, so nothing on disk changes.

## Repo rules

Worktree only; file headers (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`.

```
feat(core/secrets): keyring, sealed records, importer, offline keyring commands (dark)

Co-Authored-By: Claude <noreply@anthropic.com>
```
