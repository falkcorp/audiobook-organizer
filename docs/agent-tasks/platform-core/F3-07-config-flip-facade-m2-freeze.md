<!-- file: docs/agent-tasks/platform-core/F3-07-config-flip-facade-m2-freeze.md -->
<!-- version: 1.2.3 -->
<!-- guid: cd0ab95d-94fd-4550-8089-03f6d358f1f1 -->
<!-- last-edited: 2026-11-11 -->

# F3-07 — F3 flip, one PR: core config and secrets wired, ABO's `internal/config` becomes the façade, M2 with M7 and M8 registered, `R1Values` deleted, `/api/v1/config` on core config, the Basic gate into core, canonical names in every deploy file, behind F3-08's R2 freeze

| Field | Value |
|---|---|
| ID | F3-07 |
| Release / flip | R2, **the config flip F3** (one atomic PR, 09 §9.2.1) |
| Tier | **T4** (Opus, xhigh–max): a flip PR carrying a one-way migration and every deploy-file rename |
| Wave | W32 — the only PR of its wave touching `main.go`, `internal/server/` and books' registration, and the wave's one owner of the kernel and module wiring files (`core/kernel/boot.go`, `kernel.go`, `core/module/manifest.go`, `registrar.go`, `deps.go`, `core/app/offline.go`); it **merges alone** and its siblings rebase after it; runs inside an announced **move window** (`internal/server/middleware/basicauth.go` → core `httpapi`) |
| Depends on | F3-05, F3-06, F2-05, F2-08, F2-10, F2-11, F2-14, F2-15, OWNER-04, F2-16, F3-08, F2-17 |
| Owns gate tests | `TestRenameTable_EveryKeyRegistered`; `TestDeployFilesUseCanonicalNamesOnly` (the deploy-files test); `TestBasicGate_ExemptSetGolden`; `TestBasicGate_MoreRestrictiveWins` (the compose cell of `TestPerimeterHonoursBlobOnlyValues`); `TestConfigCutover_NoPlaintextSecretOnDisk`; `TestCutoverRefusedByOlderBuilds`; `TestAuthSwitch_MoreRestrictiveWins` |
| Gate | Plan, then execute autonomously. **The R2 freeze is already on (F3-08)**; this PR re-checks the pre-freeze tag immediately before merge. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f3-07-config-flip
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Before any edit: every dependency is merged (F3-08 included: `ci/prefreeze_release.txt` exists, and the repository variable
`RELEASE_FREEZE` reads `R2`, which the owner set before F3-08 merged and logged in the tracking issue; D15); OWNER-04's answers are recorded in the tracking issue (P1: no `(unregistered)`
prefix; P4: the env names actually set on the owner's hosts, each mapped by the rename table). If any is missing,
stop: this PR may not merge without them. Read F3-01 … F3-06's PR descriptions for the seams and
coined names they left for this PR.

## Goal

Switch configuration and secrets to core in one PR, with every first-party consumer in the same PR (09 §9.2.9):

1. **Wire** core config (DB layer attached at P4d step 1) and secrets for every app, hello included: add the boot call
   of F3-03's P4b helper (`secrets.Open` at the start of P4b, after the checkpoint; no boot called it before this PR),
   its boot-writer row (06 §6.5.1) and its crash-matrix cells; remove any dark gate F3-01 left; bind F3-03's keyring as
   F3-04's quarantine sealer seam and as F3-05's Basic-gate password resolver seam. Add to `core/module/`
   `Manifest.Cutover`, `Importers.Config`, `Importers.Secrets` and `Deps.Secrets` (06 §6.4.2, §6.4.10 F3 row); add the
   F3 offline commands to `core/app/offline.go`'s list and their cobra wrappers under `cmd/`; and in
   `core/kernel/storage.go` (the kernel's P2) storage's refusal to **create** a store while 05-config reports a
   retired database-path name set (05-storage §5S.4 step 1; 14 K15, E6; `Store.RetiredSet()` from F3-02). Bind the
   quarantine sealer seam (`Seal(ctx, b, name, raw)`, F3-04) through an adapter over `secrets.Importer.ImportSecret`.
   **Live logging config** (05-logging §4 item 3): the kernel subscribes to the `core.log` handle (`OnChange`) and
   applies `level` and `levels` through R1-36's `Logging.SetLevels`, and `redact_keys` (and `add_source`) live, by
   rebuilding and swapping the `Redactor`; format, output and file stay restart-bound.
2. **Façade** (05-config §1.12.1 item 2): ABO's `internal/config` keeps its exported surface and becomes a per-App
   `Facade` value over core handles; `AppConfig` is a projection rebuilt on every core generation change through the
   rename table; `Mutate` becomes one `Store.Apply`. viper, `configFromViper`, `LoadConfigFromDatabase`,
   `SaveConfigToDatabase`, `SaveConfigToFile`, `UpdateService` and the masks leave ABO's boot and write paths; HEAD's
   settings load, encryption init and key guard leave `serve` (05-config C17), and `cmd/child_mode.go`'s
   `loadConfigFromDB`, `internal/startup/`'s `loadConfigFromDB` step and `internal/dedup/unified/config.go`'s direct
   viper read go with them; `internal/books/store/settings.go` loses the masks (05-config §2.10 item 2). Books' own
   config section is declared in `internal/books/config.go` (path coined here). Delete `configupdate`'s `ServiceDef`
   (`internal/config/register.go` and its entry in `internal/books/services.go`' list; 06 §6.11.1 step 4, §6.11.2 row
   `configupdate`). Delete the `KeyConfigUpdate` constant in `internal/serviceregistry/keys.go`. Add `TestAppConfigRatchet`.
3. **Register** M2, books' M7 half and M8 (F3-06) in a new `internal/books/cutover/steps.go`, in `books.Module()`'s
   manifest, and **core's M7 half** (`core.level-<n>`, F3-04's constructor) in the kernel's cutover wiring next to
   books' half, in the same batch order (sidecar first), registered when a manifest declares a cutover; books' and
   core's declared `Spec.Level` become `<n>` in this PR. This is the first application of the flip mechanism (F3-04
   "Flip mechanism"; F3-06): level steps named by target level, one per owner at this first flip, and M8 under its one
   R2 ID, registered here once and last (later flips insert their steps before it and never replace it). Pass
   `RetiredConfigNames` and `ConfigEnvBindings` (`OPENAI_API_KEY`, `OPENAI_BASE_URL` onto
   `integrations.ai.openai.{api_key, base_url}`) from ABO's `main`.
4. **Delete `R1Values`** and its type from `core/app/options.go` and ABO's `main`, with an archtest that fails if either
   is present (05-config §1.12.1, 06 §6.4.10 F3 row). The perimeter keys become hot-reloadable core keys;
   `core.shutdown.budget` is read through core config.
5. **`/api/v1/config`** (05-config §1.12.1 "Between the config flip and the identity flip"): `GET` renders HEAD's flat
   response keys from the façade's projection with each key's lock from `KeyState.LockedBy`; every secret is
   write-only (its `secrets.Meta`: `set`, `source`, `updated_at`, `fingerprint`; never a value or mask); `PUT` turns the
   body into one `Store.Apply` through the rename table, `config.Actor` filled from the gin context (`Subject` = HEAD
   user ID, `Interactive` = `auth.Method.MayChangeCredentials()`, `Owner` = HEAD's owner proof, `Can(perm)` = the
   principal's HEAD atoms mapped by books' atom table); class-`i` fields read and written in the residual blob with the
   frozen codec, `ProtectAdmin` where §1.3.7's shape rule classes the canonical path. The SPA settings page switches to
   the `Meta` form; masks and echo detection are deleted. With auth off the auth-disabled principal holds no
   `core:admin`, so HEAD's `!prior.EnableAuth` exemption ends here.
6. **Basic gate:** stage 8 (F3-05) runs for the legacy mount with the manifest's `BasicExempt` (`/api/health`,
   `/api/v1/health`), `OwnAuth` and the mount's `BasicExemptAssets`; `internal/server/middleware/basicauth.go` and its
   `router.Use` are deleted (04 §7: the 04 key "F3 move `internal/server/middleware` (basicauth.go) → `mediacore/httpapi`").
7. **Deploy files** (05-config §1.12.4): `deploy/audiobook-organizer.service`, `deploy/com.audiobook-organizer.plist`,
   `deploy/local.conf.example` (commented lines included), `docker-compose.yml`, `Dockerfile`, `Dockerfile.build-cgo`
   use canonical names only; compose passes the Basic-gate variables **only when set** (bare-name entries or an
   `env_file`), so a default compose install sets no `ABO_CORE_HTTP_BASIC_GATE_ENABLED=false`; `AUDIOBOOK_ROOT_DIR`,
   `AO_DIR`, `AO_DB`, `OLLAMA_BASE_URL` are removed. The repo's agent skills (`.claude/skills/`) and first-party
   `scripts/` that set env names switch in this PR.
8. **The pre-freeze tag** (09 §9.2.1; 05-storage §5S.3.12 `from_release`): the owner turned the R2 freeze on (the repository
   variable, D15) and F3-08 wrote `ci/prefreeze_release.txt`; this PR edits neither `prerelease.yml` nor that file. Embed the
   file in the build (`//go:embed ci/prefreeze_release.txt` in the root `main.go`) and pass it to core as the value the
   checkpoint record carries as `from_release`. The file has `#` comment lines (F3-08's format: a four-line header and
   one comment line); read the single line that is neither blank nor starts with `#` (the one reader rule F2-15's
   fetch script and F3-08 use: `grep -v '^#' ci/prefreeze_release.txt | grep -v '^[[:space:]]*$'`), and pass that tag,
   not the whole file. Immediately before merge, re-check that the newest published production release
   still equals the file, that `RELEASE_FREEZE` still reads `R2`, and that no `prerelease.yml` or `release-prod.yml` run
   is unfinished (Acceptance); if any check fails, a release was or may still be promoted after F3-08, so stop and
   report to the lead.
9. From this PR every PR runs the R2 cutover suite in `extraction-gates` (F2-15's harness now bites). In
   `internal/books/cutover_suite_test.go` add the cell that runs F3-06's `ValidateCutoverList` over the registered
   list (books' manifest list plus the kernel's core half). Extend `TestBootCrashMatrix_ABO` over M2, both M7 halves and
   M8 and over the keyring and `c/secrets/meta` writes, and core's
   `TestBootCrashMatrix` over hello with the same two secrets writes (09 §9.3: the PR that adds a boot writer extends
   the crash matrix).

## Spec

09 §9.4 R2 flips table F3 row, §9.2.1 (the freeze before F3 merges; the pre-freeze release), §9.2.5, §9.2.9, §9.6 rows 6–8;
03 §3.8 F3; 05-config §1.12.1 items 2–3 and "Between the config flip and the identity flip", §1.12.2–§1.12.4,
§1.3.4, §1.3.5, §1.3.7; 05-http-api §8.1 F3 row, §10, §12 (the Basic-gate tests, `TestPerimeterHonoursBlobOnlyValues`);
06 §6.4.10 F3 row, §6.5.1 P3/P4b/P4d; 05-storage §5S.3.5, §5S.3.7 (`TestCutoverRefusedByOlderBuilds`), §5S.3.12
(`from_release`), §5S.3.13 (M7, M8, canaries), §5S.3.15 (reset next boot); 05-logging §4 item 3; 05-authn §10
(`TestAuthSwitch_MoreRestrictiveWins`); 14 K15, E5, E10.

## Files (the row's list)

| Action | Path |
|---|---|
| modify | `main.go` (also embeds `ci/prefreeze_release.txt`, which F3-08 wrote; read only; its tag is the single non-`#` line), `cmd/` (`root.go`, `child_mode.go`, the offline-command wrappers) |
| modify | `internal/config/` (the façade; `register.go` loses `configupdate`) |
| modify | `internal/books/module.go`, `internal/books/services.go` |
| modify | `internal/serviceregistry/keys.go` (delete the `KeyConfigUpdate` constant) |
| create | `internal/books/config.go` (books' config section; path coined here), `internal/books/cutover/steps.go` |
| modify | `core/app/options.go`, `core/app/offline.go` |
| modify | `core/kernel/boot.go`, `core/kernel/kernel.go`, `core/kernel/storage.go` |
| modify | `core/module/manifest.go`, `core/module/registrar.go`, `core/module/deps.go` |
| modify | `internal/startup/`, `internal/dedup/unified/config.go`, `internal/books/store/settings.go` |
| modify | `internal/server/`, `web/src/`, `internal/telemetry/` |
| modify | `deploy/`, `docker-compose.yml`, `Dockerfile`, `Dockerfile.build-cgo` |
| modify | `internal/books/cutover_suite_test.go` (the cutover-list check cell) |
| modify | `scripts/`, `.claude/skills/` |

This PR is its wave's only owner of the kernel and module wiring files; no sibling of W32 edits them.

## Re-verify at BASE (record every count; regenerate expected values at BASE)

```bash
grep -rn 'R1Values' --include=*.go . | wc -l                        # > 0 now; 0 after this PR (outside the archtest)
grep -c 'servermiddleware.BasicAuth()' internal/server/server.go     # HEAD: 1 (server.go:707)
ls internal/server/middleware/basicauth.go | wc -l                   # 1
grep -rln 'spf13/viper' --include=*.go cmd internal | grep -v _test  # HEAD: cmd/root.go, internal/config/{removed_keys,viper_decode,invite_config,persistence,config}.go, internal/dedup/unified/config.go
grep -rn 'MaskSecret\|IsEchoedMask\|LooksLikeMask' --include=*.go internal | grep -v _test | wc -l   # > 0 now
grep -n 'AUDIOBOOK_ROOT_DIR\|DATABASE_PATH\|FP_PARALLEL_WORKERS' deploy/audiobook-organizer.service  # HEAD: lines 97, 98, 108
grep -n 'AO_DIR\|AO_DB\|BASIC_AUTH_' docker-compose.yml                                           # HEAD: lines 18-22
grep -n 'AUDIOBOOK_ROOT_DIR\|DATABASE_PATH' deploy/com.audiobook-organizer.plist                  # HEAD: lines 35-41
grep -n 'OLLAMA_BASE_URL\|ALLOWED_PEER_CIDRS\|TUNNEL_CONNECTOR_ADDRS\|LOGIN_SATURATED' deploy/local.conf.example
grep -rn 'func ConfigStep\|func RetiredConfigNames\|func RewriteStep' internal/books/cutover/ | wc -l   # F3-06's constructors (names may differ: read F3-06's PR)
grep -rn 'func guardAgainstKeyRegeneration' cmd/root.go | wc -l      # 1 (HEAD line 760); 0 after this PR
gh variable get RELEASE_FREEZE                                         # R2 (the owner's toggle, D15); this PR edits no workflow
test -s ci/prefreeze_release.txt && echo ok                            # ok (F3-08)
test "$(bash scripts/ci/fetch_prefreeze_binary.sh --resolve ci/prefreeze_release.txt)" = "$(grep -v '^#' ci/prefreeze_release.txt | grep -v '^[[:space:]]*$')" && echo RESOLVE_OK   # RESOLVE_OK (F2-15's resolver reads the same tag)
grep -rn 'func ValidateCutoverList' internal/books/cutover/ | wc -l    # 1 (F3-06; name may differ: read F3-06's PR)
grep -c 'loadConfigFromDB' cmd/child_mode.go                          # HEAD: >= 1 (line 87); 0 after this PR
grep -c 'viper' internal/dedup/unified/config.go                      # HEAD: >= 1; 0 after this PR
grep -rn 'openSecrets(' core/kernel/ | wc -l                          # F3-03's helper exists, not yet called from boot (name may differ: read F3-03's PR)
grep -rn 'ConfigUpdate\|configupdate' internal/serviceregistry/keys.go internal/config/register.go | wc -l   # >= 2 now; 0 after this PR
```

## STOP conditions

1. OWNER-04's P1 shows an `(unregistered)` prefix, or P4 shows a set env name the rename table does not map: stop and
   report to the lead (05-config §1.12.3 step 4 makes an unmapped name a generator defect).
2. The config oracle (`TestConfigCutover_EffectiveConfigUnchanged`) or any M2 test regresses on the registered path.
3. The work needs a path outside the row's list: report before editing it.
4. A first-party consumer of a renamed env name or of a deleted route would remain on `main` after this PR.
5. Any secret value or mask appears in a `GET /api/v1/config` response or a log line.
6. The newest published production release (`--exclude-drafts`) differs from the tag line of `ci/prefreeze_release.txt`
   (its single non-`#` line) at merge time, a `prerelease.yml` or `release-prod.yml` run is unfinished, or the
   repository variable `RELEASE_FREEZE` is not `R2`: do not merge; report to the lead (the owner owns the variable,
   F3-08 the file).

## Steps (one PR; keep every commit building)

1. Write the owned gate tests first (below).
2. Kernel, module and app wiring: the P4b `secrets.Open` call with its boot-writer row, config DB layer attached at P4d
   step 1, the quarantine sealer and Basic-gate resolver seams bound to the keyring, health tokens surfacing on F2-10's
   route, `Manifest.Cutover`, `Importers.{Config,Secrets}`, `Deps.Secrets`, the F3 offline commands and storage's
   retired-name refusal in `core/kernel/storage.go`; remove any dark gate.
3. Façade in `internal/config`, `TestAppConfigRatchet`; delete the loader, viper, `UpdateService`, masks and echo
   detection from boot and write paths (`cmd/root.go`, `cmd/child_mode.go`, `internal/startup/`,
   `internal/dedup/unified/config.go`, `internal/books/store/settings.go`); `cmd/root.go` loses the settings load,
   encryption init and key guard; books' section in `internal/books/config.go`; delete `configupdate`'s `ServiceDef`.
4. `steps.go` + `books.Module()` manifest: the cutover list M2 (with books' M7 half before it and M8 after it, the
   order of 03 §3.8), core's M7 half in the kernel's cutover wiring, both `Spec.Level` values bumped;
   ABO's `main` passes `RetiredConfigNames`, `ConfigEnvBindings` and the flags through `FromFlagSet`'s rule (`--dir`,
   `--db`, `--playlists`, `--log-level`, `--config` and the other shipped flags onto canonical paths).
   **`config check` seam (F3-02):** `core/app/offline_config.go` builds `kernel.ConfigCheck` with `ModuleSections` nil,
   so a retired name of a module key (`ABS_JWT_SECRET` → `modules.abs.…`) prints `NOT CHECKED` and exits non-zero.
   When modules hand their config sections to the kernel, pass the enabled modules' sections into
   `ConfigCheck.ModuleSections` there (and the shipped flags, which `config check` takes today as
   `--set <path>=<value>`), so `config check` judges module keys exactly as a start does; add a test that a retired
   module secret then prints `STOPS THE SERVICE FROM STARTING`.
5. Delete `R1Values` and its type; add the archtest.
6. `/api/v1/config` rewrite and the SPA settings page.
7. Basic gate into core; delete `basicauth.go` and its use.
8. Deploy files, skills, scripts; `internal/telemetry` reads the OTLP keys through core config.
9. Embed `ci/prefreeze_release.txt` in `main.go` and hand core its single non-`#` line (the tag) as `from_release`; add the cutover-list check cell to
   `internal/books/cutover_suite_test.go`.
10. Crash matrix extension; run the full cutover suite on the corpus; headers; one `changelog.d/` fragment; R2
    release-note lines left to R2-01 (09 §9.2.9).

## Tests first

- `TestRenameTable_EveryKeyRegistered`: every canonical path of class `k` or `s` is a registered key of the build under
  test (on every PR from here).
- `TestDeployFilesUseCanonicalNamesOnly`: scans the unit, plist, `local.conf.example` (commented lines included),
  compose and both Dockerfiles; every variable is a canonical name, an ecosystem binding, or on the runtime list
  (`HOME`, `GOMEMLIMIT`, `GOGC`, `TZ`, `NOTIFY_SOCKET`); a retired name or anything else fails.
- `TestBasicGate_ExemptSetGolden`: the reviewed golden of stage 8's exempt set over the real manifest, HEAD's until F8:
  the `OwnAuth` ABS routes, `/api/health` and `/api/v1/health`, and the static-asset rule on the `/` owner; `/ping`,
  `/status`, `/login` stay gated.
- `TestBasicGate_MoreRestrictiveWins`: operator `false` with stored `true`, and operator `true` with stored `false`, both
  turn stage 8 on; the first is reported in `AttachReport.FailClosed`, the startup log and health
  `config_fail_closed_override`. Compose cell: the shipped `docker-compose.yml` with no Basic-gate variable on the host
  passes none, so a gate enabled in Settings stays on.
- `TestConfigCutover_NoPlaintextSecretOnDisk`: a canary secret planted in every HEAD form before the upgrade appears in
  no file under the live DB directory, the state directory or the gap-fill file after M2 and M8 (and, from F7, M6); the
  retained checkpoint is the one exception.
- `TestRetiredDatabasePathRefusesStoreCreate` (name coined here): with a retired database-path name set
  (`DATABASE_PATH` or HEAD's flat file key), the kernel's P2 refuses to create a store and nothing is written (14 K15).
- `TestCutoverRefusedByOlderBuilds`: R0's binary and the pre-freeze release's binary each refuse an R2 store and a store
  killed after M7, exit 78, nothing written; the test asserts that **both** owners' stamps (`books` and `core`) are
  raised to this build's levels (F4-08 adds the mid-M3 cell).
- `TestAuthSwitch_MoreRestrictiveWins` (05-authn §10, with 05-config): stored `required` + operator `disabled` → auth on,
  warned and reported in health; stored `disabled` + operator `required` → auth on; the same for the Basic gate's
  `enabled`; a set retired `ENABLE_AUTH` or `BASIC_AUTH_ENABLED` refuses boot naming the canonical variable.
- `TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn` (05-storage §5S.3.15 "Next boot", SEC8-5): after a factory reset
  the next boot is a fresh install at core's defaults, and a stale operator value that disables auth cannot loosen the
  default, because the more restrictive of the operator and the stored value wins (05-config §1.3.5), so auth stays on.
- `TestCoreLogConfig_AppliesLive` (name coined here): an `Apply` of `core.log.level`, `core.log.levels` or
  `core.log.redact_keys` takes effect without a restart (`Enabled` changes for the named module; a new redact key is
  redacted in the next record).
- The `ValidateCutoverList` cell over the registered list
  (levels raised to the build's target before the first step of each flip, versions contiguous per owner, exactly
  one M8, last; F3-06).
- Also green here: `TestPerimeterHonoursBlobOnlyValues` (the compose cell), `TestAuthOff_ProtectedKeysRefused` (the ABO
  half), `TestCLIExposesCoreOfflineCommands` (with `config check`, `storage migrate --dry-run`, `storage quarantine`,
  `secrets export-keyring|rotate-derive|set-install-id`), `TestBootCrashMatrix_ABO`, the cutover suite of F2-15,
  `TestAppConfigRatchet`.

## Invariants

- Every intermediate `main` boots and passes e2e; this PR is atomic.
- No alias, no window, no legacy store (owner decision D10): an old name is retired, not read.
- A stale operator value never opens a gate an admin closed (fail-closed keys).
- No first-party caller keeps an old env name or a mask.

## Acceptance

```bash
go build ./... && go vet ./...                                                   # exit 0
go test -count=1 ./internal/config/... ./internal/server/... ./internal/books/... ./cmd/...   # ok
go test -count=1 -run 'TestRenameTable_EveryKeyRegistered|TestDeployFilesUseCanonicalNamesOnly|TestBasicGate_|TestConfigCutover_|TestCutoverRefusedByOlderBuilds|TestBootCrashMatrix_ABO|TestAppConfigRatchet|TestAuthSwitch_MoreRestrictiveWins|TestFactoryReset_StaleEnableAuthFalseKeepsAuthOn|TestCoreLogConfig_AppliesLive' ./...   # PASS
grep -rn 'R1Values' --include=*.go . | grep -v _test | wc -l                     # 0
ls internal/server/middleware/basicauth.go 2>/dev/null | wc -l                  # 0
grep -rn 'AUDIOBOOK_ROOT_DIR\|AO_DIR\|AO_DB\|OLLAMA_BASE_URL\|DATABASE_PATH\|BASIC_AUTH_' deploy docker-compose.yml Dockerfile Dockerfile.build-cgo .claude/skills scripts | wc -l   # 0
test "$(gh variable get RELEASE_FREEZE)" = R2 && echo frozen                     # frozen (immediately before merge; else STOP 6)
test "$(grep -v '^#' ci/prefreeze_release.txt | grep -v '^[[:space:]]*$')" = "$(gh release list --exclude-pre-releases --exclude-drafts --limit 1 --json tagName -q '.[0].tagName')" && echo same   # same (run again immediately before merge; else STOP 6)
for w in prerelease.yml release-prod.yml; do gh run list --workflow "$w" --limit 50 --json status --jq '[.[]|select(.status!="completed")]|length'; done   # 0 and 0 (immediately before merge; else STOP 6)
grep -rn 'KeyConfigUpdate' --include=*.go internal/config internal/books | grep -v _test | wc -l   # 0
grep -c 'viper' internal/dedup/unified/config.go                                 # 0
(cd web && npm run build && npm test -- --run)                                   # exit 0
make -C core ci                                                                  # exit 0
make ci                                                                          # exit 0
make test-e2e                                                                    # exit 0
```

`extraction-gates`, `unit-lifecycle` (this PR changes `deploy/**` and `main.go`), `docker-image` and `core-ci` green on
the PR.

## Out of scope

The native settings routes and deleting `/api/v1/config` for `k` and `s` keys (F4-08); the class-`i` fields' routes
(F7); identity (F4).

## Rollback

From this merge, rollback of a deployed build is `make rollback-restore` to the pre-freeze release (09 §9.2.1). On
`main`, a revert is a decision for the lead; it does not touch the freeze, which is the owner's repository variable (D15).

## Coordinator additions (2026-11-10)

- **Gate goldens.** `TestBasicGate_ExemptSetGolden` carries golden rows for `POST /api/health` and `HEAD /api/health`, both
  expected to answer 401.
- **Realm.** Pass `Realm: "Audiobook Organizer"` to the stage 8 gate so the `WWW-Authenticate` header matches HEAD's.
- **Posture.** Call `checkPosture` when the `core.http.basic_gate.password` secret key appears and when it disappears,
  not only at boot.
- **One Build.** The gate's facts (exempt set, password present, enabled) must come from the same `Build` that mounts
  the gate; never re-read them from a second Build or from the live config.
- **Unmapped env names (OWNER-04 P4).** Map the 5 env names OWNER-04 P4 left unmapped by the rename table (each gets a rename-table
  row), so STOP condition 1 does not fire on them.

## Repo rules

Worktree only; file headers on every file (fragments headerless); conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer: add none. Move window: list the
`old ==> new` path in the description; every commit builds. `gh pr merge <n> --rebase` alone in its wave, every
required check green (09 §9.2.1), never squash, never `--admin`.

```
feat(config)!: config flip: core config and secrets live, M2/M7/M8 registered, canonical env names

Co-Authored-By: Claude <noreply@anthropic.com>
```
