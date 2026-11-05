<!-- file: docs/agent-tasks/platform-core/F3-01-core-config-loader-overridestore-importer.md -->
<!-- version: 1.0.0 -->
<!-- guid: 36955afe-b04c-490f-9996-d4b78e06b11a -->
<!-- last-edited: 2026-11-05 -->

# F3-01 — Core config, dark: the loader, `Store.Apply`, `KeyState` and locks, `config.OverrideStore` native over `c/config/`, `config.Importer`, `configtest`

| Field | Value |
|---|---|
| ID | F3-01 |
| Release / flip | R2, config flip F3 (dark PR; nothing switches until F3-07) |
| Tier | **T4** (Opus, xhigh–max): the config engine behind every key — layering, locks, validation and the audited write path |
| Wave | W23 (runs inside an announced **move window**: no other PR of the wave touches `internal/config/`, `internal/arch/layering_test.go` or `scripts/ci/short_test_weights.json`) |
| Depends on | F2-09, F2-07 |
| Owns gate test | `TestStateDirFallbackMatchesHEAD` |
| Gate | Plan, then execute autonomously in your own worktree and PR. Do not merge with `--admin` past a required check. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-01-core-config-loader
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps            # the web install entry point since R1-05
git rev-parse HEAD       # record this as BASE; every count below is regenerated at BASE
```

Confirm in the tracking issue that F2-09 and F2-07 are merged and that the move window naming `internal/config` is
announced (09 §9.2.10). If not, stop.

## Goal

Grow core's `config` package (created by F2-09 with the section declarations only) into the full engine of
05-config §1.3.2–§1.3.4, §1.4 steps 0–3 and 6–9, §1.5, §1.6 and §1.11, **dark**: the store, the typed handles, the
operator lock, `Apply` with protection, the native DB layer `core/internal/configstore` over `c/config/`, the
importer the cutover step M2 will use, the conformance kit `config/configtest`, and the one state-directory rule
`config.ResolveStateDir`. ABO keeps running on its own `internal/config` loader unchanged; F3-07 makes
`internal/config` the façade over this engine and registers M2.

Out of this PR and owned by F3-02 (next wave, it depends on this one): the env and file grammar (canonical env names,
`<NAME>_FILE`, empty-is-unset, ecosystem bindings, strict decoding of env and file values), the `FailClosed`
exception, retired names and `config check` (05-config §1.3.5 "Fail-closed keys", §1.3.6). Build the merge so those
layers plug in without changing this PR's types: here the operator layers you parse are the flags
(`Options.Flags`) and the boot record; file and env sources are an internal seam that F3-02 fills.

## Spec (read before coding)

- 05-config §1.3 (imports list), §1.3.1 (types F2-09 already declared), §1.3.2 (`Registry`, `Register`, `Handle`),
  §1.3.3 (`Options`, `Store`, `OverrideStore`, `Importer`, `Committer`, `Override`, `Commit`, `Patch`, `Actor`,
  `Result`, `Source`, `KeyState`, `AttachReport`, sentinel and typed errors), §1.3.4 (semantics the routes will
  expose; the routes themselves are F4-05's), §1.3.5 (merge order, the operator lock, the boot record), §1.3.7
  (protection checks), §1.4, §1.5, §1.6, §1.7 (`core.paths.*`, pre-boot resolution), §1.11 (tests), §1.12.1 item 2
  (what the façade will need from you; do not build the façade).
- 04 §7 (`internal/config` split), §8.1 F3 row, §8.6 row `internal/config → mediacore/config` (a re-implementation,
  not a byte move).
- 06 §6.5.1 P0, P3, P4c/P4d step 1 (where `Boot`, `Load` and `Attach` run); 06 §6.4.10 F3 row.
- 05-rbac §7.1 (`core.settings.update` audit action, built by F2-07).

## State at HEAD and at BASE

HEAD of the plan branch (`f7ac5b11e`) has no `core/` directory; the spec's anchors are at `748fd787a` and
`internal/config` is unchanged between them. At BASE, R1 and F2 have created:

| Path | Created by | What you rely on |
|---|---|---|
| `core/config/` | F2-09 | `Path`, `Reload`, `Protection`, `Duration`, `Section[T]`, `Define`, `SectionOption`s and every core section on the kernel's list |
| `core/kernel/core_sections.go` | F2-09 | the core-section list the kernel registers at P0 |
| `core/audit/` | F2-07 | `audit.Store`, `audit.RecordIn`, the action `core.settings.update` |
| `core/storage/` | R1-20 | `*storage.Batch`, `GetForUpdate`, `RetryOnConflict`, `ErrConflict`, keyspaces |
| `core/internal/jsonschemax/` | R1-12 | the shared reflector |
| `core/metrics/` | R1-10 | `*metrics.Scope` |
| `core/coretest/` | R1-09, R1-20 | store fixture, fingerprint helper |

## Files

| Action | Path |
|---|---|
| modify / create | `core/config/` (store, handles, apply, keystate, importer, boot record, `resolve_state_dir.go`, schema output; file names are yours) |
| create | `core/config/configtest/` (`OverrideStoreSuite`, fixture sections) |
| create | `core/internal/configstore/` (the native `OverrideStore` over `c/config/` and the kernel's `Committer`) |
| create / modify | `core/kernel/config.go` (P0 `Boot`, P3 `Load`, P4c store build, P4d step 1 `Attach`; dark for ABO, see Invariants) |
| modify | `internal/config/` (only `state_dir.go` re-pointed, see step 6; nothing else changes behaviour) |
| modify (only if a test demands it) | `internal/arch/layering_test.go`, `scripts/ci/short_test_weights.json` |

Not yours: `core/config/env.go`, `core/config/precedence.go`, `core/config/retired.go` (F3-02, next wave), anything
under `core/secrets/` (F3-03), `core/app/` (F3-02, F3-07).

## Re-verify at BASE (stop on a zero-hit edit target or a moved count)

```bash
# F2-09's package exists with the declaration API (expect >= 1 each)
grep -rn 'func Define\[' core/config/ | wc -l
grep -rn 'type Section\[' core/config/ | wc -l
# the core-section list exists (expect 1)
ls core/kernel/core_sections.go | wc -l
# the audit action table carries core.settings.update (expect >= 1)
grep -rn 'core.settings.update' core/audit/ | wc -l
# HEAD's state-dir rule you must equal (expect 1 each; anchors at HEAD f7ac5b11e)
grep -c '^func EnsureSecureStateDir\|^func SecureStateDir' internal/config/state_dir.go   # expect 2 (HEAD: lines 70, 133)
grep -c 'ABK_STATE_DIR' internal/config/state_dir.go   # expect >= 1 (HEAD: line 25)
# where R1 put the pre-boot state-dir resolver (expect exactly 1 definition in core/)
grep -rn 'func ResolveStateDir' core/ | wc -l
# config may import secrets types (05-config §1.3): does core/secrets exist yet? (see STOP 1)
ls -d core/secrets 2>/dev/null | wc -l
```

Record every count in the PR description. HEAD values for the `internal/config` greps: the state-dir file exists and
names `ABK_STATE_DIR` (`internal/config/state_dir.go:25`); a zero there means the move window was not respected:
stop.

## STOP conditions

1. **`core/secrets` does not exist at BASE** and F2-09 left no `secrets.Ref` stand-in. 05-config §1.3 makes `config`
   import `secrets` (`secrets.Ref`, `secrets.Resolver`, `secrets.Write`, `secrets.Meta`), but this row's file list has
   no `core/secrets/` path and F3-03 (which creates it) depends on this row. Stop and report; do not create
   `core/secrets/` here unless the lead amends the row (see Planner notes).
2. Any change you make alters ABO's boot or write path before F3-07: ABO must still load, save and protect settings
   through `internal/config` exactly as at BASE (its whole test suite green, no test edited to pass).
3. A store write before the checkpoint, or any write from `Attach` (`TestNoStoreWriteBeforeCheckpoint`).
4. You need a file outside the list above.

## Steps

1. Run the re-verify block. Write the test files of "Tests first" (they fail).
2. **Registry and handles** (§1.3.2): `Registry` (unexported method, kernel-only implementation), `Register[T]`,
   `Handle[T]` with `Path`, `Get` (one atomic load, an immutable deep-decoded snapshot), `Pending`, `Generation`,
   `OnChange` (one goroutine per subscription, one-slot coalescing mailbox, `stop()` idempotent and waits),
   `AfterCommit` (synchronous after commit, error reported as `*PostCommitError`, never a veto). Registration after
   `Load` returns `ErrSealed`.
3. **Store** (§1.3.3): `NewStore(Options)`, `Boot`, `Load`, `Importer`, `SetSecrets`, `Attach`, `Apply`, `Lookup`,
   `Keys`, `PassthroughValues`, `Schema`, `Generation`, `Close`, `ConfigFileHasSecrets`, `RedactedConfigFile`, and
   `RetiredSet` returning nil until F3-02 fills it. Merge order defaults → file → env → flags → DB overrides (boot
   record instead of DB overrides for `ReloadBoot` keys). **Operator lock** (§1.3.5): a key any operator layer
   supplies is locked; its stored row is ignored at `Attach` (kept, listed in `AttachReport.Ignored`, logged once per
   boot) and `Apply` refuses it with `*LockedError{By}`. `ProtectOperator` keys are never read from or written to the
   DB layer or the boot record.
4. **Apply** (§1.4 step 8, §1.3.7): authorize (section permission, then protection: `ProtectAdmin` =
   `Actor.Can("core:admin") && Actor.Interactive`; `ProtectOwner` adds `Actor.Owner` once an owner is set) → decode
   the candidate per touched section → `Validate` → write the boot record if boot keys changed → `Committer.Commit`
   (one batch: `OverrideStore.Stage`, staged secret writes, the `core.settings.update` audit record) → publish live
   keys → bump generation → `AfterCommit` → wake `OnChange`. A value equal to the default is staged as an unset. On a
   `Commit` failure nothing is published and the previous boot record is restored. One store mutex serialises `Apply`.
5. **Native store and committer** (`core/internal/configstore`): one row per explicitly set key under `c/config/`;
   `Load` is read-only; `Stage` reads with `GetForUpdate`; the committer retries with `storage.RetryOnConflict`, a
   fresh batch per attempt. **Importer** (§1.3.3): `Stage(b, p, v, by)` validates against `p`'s schema and stages the
   row into the caller's batch only; refusals exactly as the contract below. Build `configtest.OverrideStoreSuite`
   and run it over the native store (`TestNativeOverrideStore_RoundTrip`).
6. **State directory** (§1.7, `ResolveStateDir`): implement the pure function `config.ResolveStateDir(explicit, def,
   dbPath, mkdir)` with HEAD's three-step rule (`EnsureSecureStateDir`, `internal/config/state_dir.go:118-155` at HEAD). If the R1 resolver
   found by the re-verify grep lives in `core/kernel/`, make it a one-line delegate to `config.ResolveStateDir`; if it
   lives in a file outside your list, leave it and assert in `TestStateDirFallbackMatchesHEAD` that both functions
   agree on every row (record this in the PR). Re-point ABO's `internal/config/state_dir.go` resolution onto
   `config.ResolveStateDir` only if its own `state_dir_test.go` passes unchanged; otherwise leave ABO's file alone.
7. **Kernel wiring** (`core/kernel/config.go`): P0 `NewStore` + core sections + `Boot`; P3 module sections + `Load`;
   P4c build the native store; P4d step 1 `SetSecrets` (when a resolver exists) then `Attach`. Dark for ABO
   (Invariants).
8. Bump the header of every touched file; add one `changelog.d/` fragment (no header). Run Acceptance.

## Code contract (copy exactly; 05-config §1.3.2, §1.3.3)

```go
func Register[T any](r Registry, s Section[T]) (*Handle[T], error)
func (h *Handle[T]) Get() *T
func (h *Handle[T]) Pending() *T
func (h *Handle[T]) Generation() uint64
func (h *Handle[T]) OnChange(fn func(ctx context.Context, prev, next *T)) (stop func())
func (h *Handle[T]) AfterCommit(fn func(ctx context.Context, prev, next *T) error) (stop func())

func NewStore(o Options) (*Store, error)
func ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error)
func (s *Store) Registry() Registry
func (s *Store) Boot(ctx context.Context) (BootView, error)
func (s *Store) Load(ctx context.Context) error
func (s *Store) Importer() Importer
func (s *Store) SetSecrets(r secrets.Resolver)
func (s *Store) Attach(ctx context.Context, o OverrideStore, c Committer) (AttachReport, error)
func (s *Store) Apply(ctx context.Context, p Patch, a Actor) (Result, error)
func (s *Store) Lookup(p Path) (KeyState, bool)
func (s *Store) Keys() iter.Seq2[Path, KeyState]
func (s *Store) RetiredSet() []RetiredUse
func (s *Store) PassthroughValues() iter.Seq2[Path, OperatorValue]
func (s *Store) Schema(include func(section Path) bool) (json.RawMessage, error)
func (s *Store) Generation() uint64
func (s *Store) Close(ctx context.Context) error
func (s *Store) ConfigFileHasSecrets() bool
func (s *Store) RedactedConfigFile() ([]byte, error)

type OverrideStore interface {
	Load(ctx context.Context) iter.Seq2[Override, error]
	Stage(ctx context.Context, b *storage.Batch, set []Override, unset []Path) error
}
type Importer interface {
	Stage(b *storage.Batch, p Path, v json.RawMessage, by string) error
	Default(p Path) (json.RawMessage, error)
}
type Committer interface {
	Commit(ctx context.Context, c Commit) error
}
```

`Options`, `EnvName`, `RetiredName`, `RetiredUse`, `BootView`, `OperatorValue`, `Override`, `Commit`, `Patch`,
`Actor`, `Result`, `Source` (with `SourceDefault … SourceOverride`), `KeyState`, `AttachReport`, the eight sentinel
errors (`ErrUnknownKey`, `ErrLocked`, `ErrProtected`, `ErrForbidden`, `ErrInvalid`, `ErrSealed`, `ErrDuplicate`,
`ErrRetiredName`) and the typed errors `LockedError`, `ProtectedError`, `PostCommitError`, `RetiredNameError` are
copied field for field from 05-config §1.3.3. `Importer.Stage` refuses an unregistered or passthrough path with
`ErrUnknownKey`, a `secrets.Ref` key and a `ProtectOperator` key with `ErrInvalid`, a bad value with `*FieldError`,
and stages nothing for a default value. Boot record: `<state_dir>/config.boot.json`, `{"format":"mediacore-boot/1",
"gen":N,"values":{…}}`, mode 0600, directory 0700, temp file + fsync + rename through `Options.BootRecordRoot`;
unparsable → renamed `.bad`, health `config_boot_record_quarantined`. Metrics (§1.8) on scope `mediacore.config`.

## Tests first (names are the spec's; 05-config §1.11)

`TestStateDirFallbackMatchesHEAD` (gate test, this row), `TestBoot_ResolvesPathsWithoutStorage`,
`TestOverride_IgnoredWhenOperatorLocked` (flag lock here; F3-02 adds the env and file cells),
`TestApply_RefusesLocked`, `TestApply_RefusesProtected`, `TestApply_RefusesForbidden`,
`TestApply_RefusesUnknownKey`, `TestApply_RefusesInvalid` (05-config writes these five as
`TestApply_RefusesLocked` / `_Protected` / `_Forbidden` / `_UnknownKey` / `_Invalid`),
`TestApply_SettingsManageCannotChangeAdminKeys`, `TestAuthOff_ProtectedKeysRefused` (the hello half; the ABO half
lands with F3-07), `TestApply_RestartKeyNotPublished`, `TestApply_CommitFailureNothingPublished`,
`TestApply_SecretAndOverrideOneBatch` (needs `secrets.Write`, STOP 1), `TestOwnerTrustRootKeysAreOwnerProtected`,
`TestBoot_ModuleEnablementFromBootRecord`, `TestBootRecord_AtomicAndQuarantine`,
`TestAttach_QuarantineInvalidSection`, `TestOnChange_CoalescesUnderSynctest`,
`TestAfterCommit_ErrorReportedNotRolledBack`, `TestNoStoreWriteBeforeCheckpoint` (the config half),
`TestNativeOverrideStore_RoundTrip`, `configtest.OverrideStoreSuite`, `TestImporter_StagesOnlyValidNonDefault`,
`TestSectionPath_NestedNoOverlap`, `TestConfigFileHasSecrets_Redacted` (file values via the seam F3-02 fills: use a
test-only file source), `TestPassthrough_NotOwnedNotWritable`, `TestSchema_Golden`,
`TestSchema_SameOverridesAsOpenAPI`, `TestSchema_OverrideSetsDisjoint`, `TestSchema_SecretIsWriteOnly`,
`TestKeys_NeverContainSecretValues`, `TestSnapshot_ImmutableUnderRace` (`-race`), `TestRegistry_SealedAfterLoad`,
`TestConfigImportsOnlyAllowed`, `FuzzPatchDecode`.

`TestStateDirFallbackMatchesHEAD` table (§1.11): explicit set or not × default creatable or not × DB path absolute /
relative / empty: explicit never falls back; uncreatable default → `Dir(core.storage.path)` with the warning;
relative or empty DB path → boot error naming `<APP>_CORE_PATHS_STATE_DIR`; a fallback whose DB path the config file
later moves → boot error naming both remedies; P0a and `Boot` receive the same directory. Compare against HEAD's
`internal/config/state_dir.go` behaviour row by row.

## Invariants

- Core holds no ABO key name, row format or env name (03 rule R1; core's archtest `TestCoreHoldsNoAppKeyNames`).
- **Dark:** on this PR's `main`, ABO's boot writes no `c/config/` row and reads no DB layer through core (ABO still
  registers no config DB layer until F3-07). hello may run the full path. If the kernel wiring would run in ABO's boot,
  gate it on something only F3-07 sets and say which in the PR; a state-shape switch is not allowed (09 §9.2.8), so the
  gate must be the presence of what F3-07 wires, not a flag.
- No goroutine before `Load`; `Close` joins all (verify with `testing/synctest`).
- `Get` snapshots are never shared or mutated (coretest fingerprint).

## Acceptance (commands and expected output)

```bash
(cd core && go build ./... && go vet ./config/... ./internal/configstore/... ./kernel/...)   # exit 0, no output
(cd core && go test -race -count=1 ./config/... ./internal/configstore/... ./kernel/...)     # ok for each package
(cd core && go test -count=1 -run 'TestStateDirFallbackMatchesHEAD' ./config/...)           # --- PASS
go test -count=1 ./internal/config/...       # ok (ABO unchanged; no test edited to pass)
go test -count=1 ./internal/arch/...         # ok
make -C core ci                              # exit 0
make ci                                      # exit 0
git diff --stat origin/main -- internal/config | tail -1   # only state_dir.go (or nothing) changed
```

## Out of scope

The env and file grammar, `FailClosed`, retired names, `config check` (F3-02); secrets (F3-03); the façade,
`/api/v1/config`, deleting viper or `UpdateService` (F3-07); the settings routes (F4-05); M2 (F3-06).

## Rollback

Revert the PR. Nothing in ABO reads the new engine before F3-07, so a revert changes no stored data.

## Repo rules

- Worktree only; never edit `main` or the primary checkout; never run `go work init`.
- File header (file, version, guid, last-edited) on every created or modified file; bump the version on change; keep
  an existing guid. `changelog.d/` fragments carry no header.
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip
  trailer: add none.
- Move window: list every `old ==> new` path (if any) in the PR description; each commit builds
  (`scripts/ci/check_commits_build.py`).
- `gh pr merge <n> --rebase` once every required check of 09 §9.2.1 is green on an up-to-date branch; never squash,
  never `--admin`.
- CLAUDE.md concurrency rules apply to any loop over a library-scale collection.

Commit message:

```
feat(core/config): store, handles, operator lock, Apply, native OverrideStore, Importer (dark)

Grows core config into the engine of 05-config §1.3: typed handles,
the operator lock, Apply with protection and one audited batch, the
native OverrideStore over c/config/, config.Importer for the cutover,
configtest.OverrideStoreSuite and config.ResolveStateDir with HEAD's
fallback. ABO keeps its own loader until the config flip.

Co-Authored-By: Claude <noreply@anthropic.com>
```

```bash
git push -u origin "agent/$SLUG"
gh pr create --fill
gh pr merge <number> --rebase
```

## Planner notes

- **Inverted dependency on `core/secrets`.** 05-config §1.3 has `config` import `secrets` (`Ref`, `Resolver`,
  `Write`, `Meta`), and F2-09 declares secret keys, but `core/secrets/` is created only by F3-03, which depends on this
  row. Either F2-09 or this row needs a leaf file of `secrets` types (for example `core/secrets/ref.go`, types only),
  or F3-03's types land first. This brief stops (STOP 1) rather than create a path outside its row.
- **Split with F3-02.** The row title says "locks" and F3-02's says "the operator lock". This brief gives F3-01 the
  lock mechanism over the layers it parses (flags, boot record) and F3-02 the env and file layers plus `FailClosed`.
  `TestPrecedence_Matrix` needs env and file values, so it is F3-02's.
- **The R1 state-dir resolver** may live in a `core/app/` file outside this row; step 6 then proves equality by test
  instead of delegating.
