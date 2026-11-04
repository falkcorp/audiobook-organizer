<!-- file: docs/agent-tasks/platform-core/PC-04-05-books-root-config-bridge.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8510030b-0bbd-438f-9e37-dd01ba6c2fa5 -->
<!-- last-edited: 2026-11-04 -->

# PC-04-05 — Books composition root and the S2 legacy config bridge: internal/books, internal/books/configstore and core/config's loader read half

| Field | Value |
|---|---|
| ID | PC-04-05 |
| Tier | **T3 Opus/high** — the first legacy store: precedence of stored versus env values and the `Changed` signal must match HEAD exactly, or a UI-saved perimeter value is ignored |
| Wave | W04 (S2, release `S2r1`), serialized step 2 (beside PC-04-03: no shared file) |
| Depends on | PC-04-02, PC-04-04 |
| Trailer | `Stage-Release: S2 1` |
| Spec | 09 §9.4 S2 "Moves" and "Config"; 05-config §1.3.3 (`NewStore`, `ResolveStateDir`, `OverrideStore` read half), §1.3.5 (precedence, `BelowStored`), §1.3.6 (aliases), §1.4 steps 0–3 and 8, §1.7 (state dir, `core.storage.path` pre-boot from S2), §1.10.2, §1.11, §1.12.2 ("Package", "Reading", "Edits made outside core config"); legacy `ConfigExternalWriter` (05-config, the `legacy` block after §1.3.3); 06 §6.4.10 S2 row, §6.4.11 (mirror test); 04 §2.4 rows `internal/books`, `internal/books/configstore`, §8.4 note 3; 09 §9.2.10 table row `TestBooksMinCoreVersion` |
| Digest | `docs/plans/platform-core/digests/S2.md` §3, §4.1, §4.2, §5 rules 2 and 7 |

**Gate.** Open and make green any time; merge only after OWNER-03's `S1` row and PC-04-02 and PC-04-04.

**Dark.** ABO's `main` does not call `books.Module()` or pass `LegacyConfigStore` yet (PC-04-07 does). This PR adds
the code, its tests and the two `layerOf` entries. `serve` behaviour is unchanged.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-04-05 -b feat/pc-04-05-books-root origin/main
cd ../audiobook-organizer-pc-04-05 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path | Holds |
|---|---|---|
| create | `core/config/` | `Store`, `Options`, `NewStore`, `Registry` (sections), `Boot`/`BootView`, `Load`, `Attach` (read-only, at P3 at S2), `Lookup`, `Keys`, `Aliases`, `Generation`, `Close`, `ResolveStateDir`, `FromFlagSet`, `Legacy` and `Alias` (with `BelowStored`, `LegacyYAML`, `LegacyBlob`, `FileOnly`), `OverrideStore` with **`Load` only**, `Override`, `Path`, `Source`, the unexported mirror of `legacy.ConfigExternalWriter`, and the core sections S2 reads (`core.paths.state_dir`, `core.config.*`, `core.storage.path` as a kernel pre-boot key, `core.http.*` of 05-http-api §10, `core.log.*` of 05-logging §8, `core.shutdown.*` of 05-health §5H.7) |
| create | `core/legacy/config.go` | `ConfigExternalWriter` only (05-config: `Changed() <-chan struct{}`) |
| create | `internal/books/module.go` | `books.Module(store)` — `Manifest()` and `Register(r)`; `Register` builds today's `*server.Server` over the given store and calls `r.MountLegacy` with `Auth: LegacyAuthGin`, `ServesSPA: true`, PC-04-01's manifest, `TopSegments` (`ping`, `status`, `logout`, `socket.io`, `public`), `SPAShared` (`login`), `CSPReport: "/api/v1/csp-report"`; `r.AuthMethod` for `books.abs` |
| create | `internal/books/module_test.go` | `TestBooksMinCoreVersion`, manifest tests |
| create | `internal/books/configstore/store.go` | the legacy `config.OverrideStore` over the store `serve` opened, plus `legacy.ConfigExternalWriter` |
| create | `internal/books/configstore/store_test.go` | the configstore tests below |
| create | `core/config/legacy_mirror_test.go` and `core/config/export_test.go` | the first mirror test (06 §6.4.11 rule) |
| modify | `internal/arch/layering_test.go` | add `internal/books` (computed 6) and `internal/books/configstore` (computed 2) to `layerOf`, and the guard assertion "only `.`, `cmd` and `internal/books` import `mediacore/module` or `mediacore/app`" (non-test imports) if PC-01-03 did not already add it |
| modify | `internal/config/persistence.go` | a post-write hook fired after every successful `config_blob` write |
| modify | `core/app/` | add `Options.LegacyConfig *config.Legacy`, `Options.LegacyConfigStore config.OverrideStore`, `ResolveStateDir(opts Options) (string, error)` (06 §6.4.5) — see Planner notes |
| modify | `core/module/` | add `Manifest.Settings` if PC-04-02 left it out — see Planner notes |
| modify | `core/kernel/` | P0 `config.NewStore` (EnvPrefix = `Options.EnvPrefix`), P3 `Load` then attach `Options.LegacyConfigStore` read-only, `Changed` → re-`Load` and republish live keys; budget keys read from config — see Planner notes |
| create | `changelog.d/<date>_pc_04_05_books_root.md` | headerless |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
ls core/app core/module core/kernel core/legacy/doc.go core/httpapi/legacy.go   # exist (PC-04-01, PC-04-02)
ls core/config internal/books 2>&1 | grep -c 'No such file'                      # 2
grep -n '^func SaveConfigToDatabase' internal/config/persistence.go             # 1 (~1932)
grep -n 'store.SetSetting("config_blob", string(blobJSON)' internal/config/persistence.go   # 1 (~1977)
grep -n '^func saveRawBlob' internal/config/persistence.go                      # 1 (~883)
grep -n '^func LoadConfigFromDatabase' internal/config/persistence.go           # 1 (~915)
grep -c '^func migrate[A-Za-z]*Blob' internal/config/persistence.go             # 9 at f07f516be (05-config cites "the ten migrate*Blob read transforms"; recount, see STOP)
grep -n '"internal/config": 1' internal/arch/layering_test.go                   # 1 (format may differ after PC-01-03)
grep -n 'EnsureSecureStateDir\|const SecureStateDirEnv' internal/config/state_dir.go   # >= 2 (the HEAD rule ResolveStateDir must equal)
grep -n 'func (s \*IndexService)' internal/search/register.go | head -1         # exists; PC-04-04 added the extendStart field
grep -n 'NotifySocketEnv' core/lifecycle/*.go                                   # 1 (PC-04-04)
```

**STOP and report** if PC-04-01/PC-04-02/PC-04-04 files are missing, or if the blob write sites moved.

## Steps

1. **`core/legacy/config.go`** — exactly:
   ```go
   // ConfigExternalWriter: Changed fires when a writer other than config.Store changed stored values.
   // A stage bridge: it exists from S2 and is deleted in the S3 PR (05-config).
   type ConfigExternalWriter interface {
   	Changed() <-chan struct{}
   }
   ```
2. **`core/config`** — the read half only (05-config §1.3.3 "Landing (COR5-14)": "the interface with Load lands at
   S2 (no storage or secrets type in it)"): `OverrideStore` has `Load(ctx) iter.Seq2[Override, error]` and nothing
   else; no `Stage`, no `Committer`, no `Apply`, no `secrets`. Precedence per §1.3.5: defaults < file < env <
   flags for canonical names; a legacy env name or file key is `Alias.BelowStored` — a value stored in the blob beats
   it, as at HEAD (so compose's `BASIC_AUTH_ENABLED=false` cannot override a UI-saved value, and a stored
   `enable_rate_limit: true` beats `ENABLE_RATE_LIMIT=false`); the DB layer applies to unlocked keys only; a stored
   value equal to this binary's default reports `SourceDefault` only when no operator or `BelowStored` value is set
   (05-config §1.12.2 "Reading"). Canonical `core.*` names are accepted and undocumented (09 S2 "Config").
   `ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error)`
   is the pure rule of §1.7 (explicit never falls back; else the default created `0700`; else
   `Dir(core.storage.path)` from defaults, the operator file, env and flags when absolute, with HEAD's warning;
   else error) — it must equal HEAD's `internal/config/state_dir.go` decisions. Register `core.storage.path` as a
   kernel pre-boot key with the app's aliases (`--db`, `DATABASE_PATH`; §1.7, COR4-20).
   The S2 attach is at P3 (§1.4 step 3): read-only, no `Open` call (`legacy.ConfigOpener` is S2b). When the attached
   store implements the unexported mirror of `legacy.ConfigExternalWriter`, a goroutine bound to the store's
   lifetime waits on `Changed()`, re-runs `Load`, republishes changed live keys of core sections (atomic swap) and
   marks restart keys pending (§1.4 step 8).
3. **Mirror test** (06 §6.4.11): `core/config/export_test.go` (`package config`) exports
   `type ExternalWriterMirror = externalWriter` only; `core/config/legacy_mirror_test.go` (`package config_test`)
   asserts both directions against `legacy.ConfigExternalWriter`.
4. **`core/app`, `core/module`, `core/kernel` additions** — `Options.LegacyConfig`, `Options.LegacyConfigStore`
   (06 §6.4.5 types and comments), `app.ResolveStateDir(opts)` calling `config.ResolveStateDir` with the inputs the
   kernel uses; kernel P0 (`config.NewStore`, core sections registered with `Options.LegacyConfig.Aliases`) and P3
   (`Load`, then attach `LegacyConfigStore`); the kernel reads `core.shutdown.budget`/`http_drain` from config from
   here on (the `ABO_CORE_SHUTDOWN_BUDGET` env of 09 S2 resolves through `EnvPrefix`).
5. **`internal/config/persistence.go`** — add (names coined here):
   ```go
   // OnConfigBlobWritten registers fn to run after every successful write of the
   // config_blob setting (SaveConfigToDatabase and saveRawBlob). Used by books'
   // legacy config store to fire legacy.ConfigExternalWriter.Changed (05-config §1.12.2).
   func OnConfigBlobWritten(fn func()) (unregister func())
   ```
   Call the registered funcs after `SaveConfigToDatabase`'s blob write returns nil (outside `configBlobSaveMu` is not
   required; they must not block: document that) and after `saveRawBlob` returns nil. No other behaviour change.
6. **`internal/books/configstore/store.go`** — `New(store database.SettingsStore, legacy *config.Legacy) *Store`
   (coined): `Load` reads `config_blob` once per call with HEAD's read transforms (`migrate*Blob`) and maps fields
   to canonical paths through the alias table (`Alias.Convert`); secret fields are not part of the layer before S3;
   `FileOnly` keys (peer allow-list, trusted proxies) are never returned (05-config §1.12.2 table). `Load` writes
   nothing. `Changed()` returns a channel signalled (buffered 1, non-blocking send, so bursts coalesce) by the
   `OnConfigBlobWritten` hook. Imports: `internal/config`, the store residue, core `config` and `legacy`; only
   `internal/books` and `cmd` import it (04 §2.4).
7. **`internal/books/module.go`** — `func Module(store database.Store, opts Options) module.Module` (the one
   constructor name, 09 S2; 03 spells it `books.Module(…)`), with `type Options struct{ Version string; WebFS fs.FS }`
   (coined here) carrying what `main` passes today through `server.SetVersion`/`SetEmbeddedFS`; PC-04-07 stops
   `main` calling those setters (09 S2 Moves) and decides how `Register` hands the two values to the server. `Manifest()`: `ID: "books"`, `MinCoreVersion` (≤ core `version.Version`), `LegacyPrefixes` from 06
   §6.4.1's comment (`auth`, `socket.io`, `ping`, `status`, `logout`, `public`, `login`, `invite`), `PublicRoutes`
   empty (books has no native route), `DefaultEnabled: true`. `Register`: builds today's `*server.Server` over `store`
   (the one recorded exception to the side-effect rule, digest §4.2), records `r.AuthMethod(auth.MethodSpec{Method:
   "books.abs"})`, wires `search.SetStartExtender(r.Deps().ExtendStart)` (PC-04-04's seam) before the server
   builds its container, and calls `r.MountLegacy(...)` with the fields listed in "Exact files", taking `Handler`
   from `srv.Engine()` and `Routes` from `srv.LegacyRoutes()` (PC-04-01's accessors; an error fails `Register`). `DefaultCSP`,
   `BuildID`, `BasicExemptAssets` and `NoCompress`-driven perimeter values are consumed only from S2 r2 (PC-05-02);
   set `DefaultCSP` to `server`'s `contentSecurityPolicy` accessor only if one is exported, else leave nil with a
   `// PC-05-02` comment.
8. **`internal/arch/layering_test.go`** — add both paths to `layerOf` with their computed layers; add the guard
   assertion on non-test imports if absent.
9. Tests first (below), then implement. Headers; changelog fragment.

## Code contract

```go
// core/config (05-config §1.3.3, S2 subset)
func NewStore(o Options) (*Store, error)
func ResolveStateDir(explicit, def, dbPath string, mkdir func(string) error) (dir string, fellBack bool, err error)
func (s *Store) Load(ctx context.Context) error
func (s *Store) Attach(ctx context.Context, o OverrideStore, c Committer) (AttachReport, error) // the kernel passes c == nil at S2 (no committer before S3)
type Committer interface{ Commit(ctx context.Context, c Commit) error } // 05-config §1.3.3, unchanged
type Commit struct { Set []Override; Unset []Path; Gen uint64 }
// Commit's Secrets []secrets.Write field is added at S3 with secrets (an additive struct field, the rule
// 05-http-api §3 states for later fields); it cannot exist at S2 because secrets does not.
type OverrideStore interface {
	Load(ctx context.Context) iter.Seq2[Override, error]
}

// core/legacy
type ConfigExternalWriter interface{ Changed() <-chan struct{} }

// core/app (06 §6.4.5)
LegacyConfig      *config.Legacy
LegacyConfigStore config.OverrideStore
func ResolveStateDir(opts Options) (string, error)

// internal/books
func Module(store database.Store, opts Options) module.Module
type Options struct{ Version string; WebFS fs.FS } // coined here
```

## TDD tests to write first

- `TestBooksMinCoreVersion` (09 §9.2.10 table; root module, `internal/books`) — `Module(nil, Options{}).Manifest().MinCoreVersion`
  ≤ core `version.Version`.
- `TestOverrideStore_ChangedRepublishesAtS2AndS2b` (05-config §1.11; `core/config`) — the S2 shape only in this PR
  (a fake legacy store attached at P3 through `app.Options.LegacyConfigStore`): a core key set only in the stored row
  (`core.http.rate_limit.enabled=false`, a CORS origin) is effective after attach; an outside write signalled on
  `Changed` reaches the live key without a restart. Leave the S2b half as a `t.Skip("S2b shape lands with storage")`.
- `TestStateDirFallbackMatchesHEAD` and `TestStoragePathPreBootFromS2` (05-config §1.11; `core/config` and
  `core/kernel`).
- `TestLegacyConfigHonoured` (06 §6.10; `core/kernel` or `core/coretest`) — S2 half.
- The **static half of `TestPerimeterHonoursBlobOnlyValues`** (05-http-api §12; `internal/books/configstore`) — a blob
  with `enable_rate_limit: false` only in the blob (no env) yields `core.http.rate_limit.enabled=false` from
  `Store.Lookup` after a kernel P3 attach through `configstore`; with `ENABLE_RATE_LIMIT=true` in env and the blob
  saying `false`, the stored value still wins (`BelowStored`). Stage 10 does not exist yet; PC-05-02 completes the
  test against the perimeter.
- `TestConfigstore_ChangedFiresAfterPUTConfig` (coined; the row's "(new test)") — over a real test server
  (`setupTestServer`), wrap its store in `configstore.New`, `PUT /api/v1/config` one field, assert one receive on
  `Changed()` within 1 s and that `Load` returns the new value.
- `TestConfigstore_LoadWritesNothing` (coined) — snapshot every setting row, call `Load` twice, compare.
- `TestConfigstore_FileOnlyKeysNeverLoaded` (coined) — `allowed_peer_cidrs` set in the operator file is not in `Load`'s
  output.
- The mirror test of step 3.

## Acceptance

```bash
(cd core && go build ./... && go test ./config/ ./legacy/ ./app/ ./kernel/ -count=1)        # ok each
go test ./internal/books/... -count=1                                                         # ok
go test ./internal/config/ -run 'Blob|Save|Load' -count=1                                     # ok (HEAD tests unchanged)
go test ./internal/arch/ -count=1                                                             # ok (layerOf matches computed layers)
grep -rln '"github.com/falkcorp/mediacore/module"\|"github.com/falkcorp/mediacore/app"' --include=*.go . | grep -v _test | grep -v '^./core/' | sort
#   expect: only ./internal/books/module.go (main and cmd join in PC-04-07)
grep -rln 'internal/books/configstore"' --include=*.go . | grep -v _test | grep -v '^./internal/books/'   # no output yet (cmd joins in PC-04-07)
make -C core ci                                                                               # exit 0
make ci                                                                                       # exit 0
```

## Invariants and STOP conditions

- `Load` never writes, renders or repairs (05-config §1.3.3). Any test showing a write → STOP.
- HEAD's precedence for legacy names holds (`BelowStored`). If a legacy key's HEAD precedence is not "stored beats
  env" (check `internal/config/persistence.go:1131-1170`'s env overlay before trusting the rule for each key you map)
  STOP and report the key: 05-config §1.3.5 owns the exceptions.
- No cycle: `internal/config` never imports `internal/books/configstore`. `go build ./...` fails otherwise.
- The S2 bridge never calls `Open` and the kernel opens no storage.
- 05-config says "the ten `migrate*Blob` read transforms"; the grep counts 9 functions named `migrate*Blob` at
  `f07f516be`. Use every read transform HEAD's loader applies (read `LoadConfigFromDatabase`, not the count); if the
  loader applies one that is not a `migrate*Blob` function, include it and note it in the PR.
- CLAUDE.md concurrency: no library-scale loop. The `Changed` goroutine is one per attached store, stopped by the app
  context.

## Out of scope

Wiring `books.Module` and `LegacyConfigStore` into `main`/`serve`, deleting ABO's `EnsureSecureStateDir` call
(PC-04-07); the perimeter reading these keys (PC-05-01, PC-05-02); write path, `Stage`, `Committer`, secrets (S3);
`legacy.ConfigOpener`, `Manifest.LegacyStores` (S2b).

## Rollback

Revert the PR; nothing calls the new code and no stored format changes.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on every new or changed file; headerless changelog
fragment; conventional commit `feat(config): S2 config loader read half and books legacy config store` ending
with `Stage-Release: S2 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
never squash; never `--admin` past a required check.

## Planner notes

- Depends-on gap: `books.Module().Register` calls `r.MountLegacy` with PC-04-01's manifest and `srv.Engine()` /
  `srv.LegacyRoutes()` accessors, so this PR needs PC-04-01 merged; the row lists only PC-04-02 and PC-04-04.
- Files missing from the row: `core/app/`, `core/module/`, `core/kernel/` (the S2 members that name core `config`:
  `Options.LegacyConfig`, `Options.LegacyConfigStore`, `ResolveStateDir`, `Manifest.Settings`, kernel P0/P3), and
  `internal/books/module.go`'s call into `internal/search` (PC-04-04's seam). PC-04-02 cannot add them because core
  `config` does not exist until this PR.
- 09 §9.4.1's batch table lists "config loader over `LegacyConfigStore`" under S2 r2, while the plan puts the loader
  in r1 (this PR, dark) and the perimeter's use of it in r2 (PC-05-02). Not a conflict as long as r1 only loads; noted
  for the reviewer.
- `TestPerimeterHonoursBlobOnlyValues` is split across PC-04-05 (static half, config only) and PC-05-02 (stage 10);
  05-http-api §12 names one test, so PC-05-02 must extend this test rather than add a second one.
- Spec gap (resolved here, flag for review): 05-config §1.3.3 gives `Attach` a `Committer` whose `Commit` struct
  names `secrets.Write`, which cannot exist at S2. This brief declares `Commit` without the `Secrets` field and adds
  it at S3 as an additive field.
