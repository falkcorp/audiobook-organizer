<!-- file: docs/agent-tasks/platform-core/PC-16-01-integrations-registry-routed-instance-store.md -->
<!-- version: 1.0.0 -->
<!-- guid: 09ce2a65-3171-4164-b1e0-9419483ae110 -->
<!-- last-edited: 2026-11-04 -->

# PC-16-01 — `core/integrations` registry: routed instance store, errors, `Test`'s ProtectAdmin rule

| Field | Value |
|---|---|
| ID | PC-16-01 |
| Title | core/integrations registry: routed instance store (native `c/integrations/instances/` plus legacy-backed via books' configstore), errors, Test's ProtectAdmin rule |
| Tier | **T3 — Opus / high.** A routed store that decides per record, HEAD-parity reach for legacy-backed instances and HEAD's lock discipline across core's commit; mistakes here leak an operator secret or lose a write. |
| Wave | W16 (stage S7, release `S7`) |
| Depends on | OWNER-19 (the `S6` soak row; P9 webhooks census) |
| Trailer | `Stage-Release: S7 1` (live, state-writing PR: it can write `c/integrations/instances/`) |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 (S7 table), §4 (05-metadata M1.3), §5 rules 7, 8, 10, 16 |
| Owning spec | 05-metadata M1 (M1.1–M1.10); 03 §3.3 "Two selection models"; 09 S7 "Data/keys"; 05-storage §5S.3.11 (legacy capabilities, `CommitLocked`) |

**Gate.** You may open the PR and make it green at any time; it merges only after `ci/soak.txt` holds a current
`S6` row (OWNER-19). `check_soak.py` check (b) refuses it otherwise. Do not try to work around the gate.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-16-01" -b feat/pc-16-01-integrations-registry origin/main
cd "$REPO/../abo-pc-16-01"
git worktree list          # you must be in abo-pc-16-01, never the primary checkout
make web-deps              # frontend deps from this worktree (PC-01-06 entry point)
```

Do not run `go work init` (CLAUDE.md: it breaks the build).

## Goal

Build the one integration-instance registry of 05-metadata M1 in core: `integrations.Kind`, `Ref`, `Meta`,
`Descriptor`, `InstanceConfig`, `StoredInstance`, `InstanceStore`, `NativeInstanceStore`, `Registry`
(`Adapters`, `Instances`, `Config`, `Put`, `Delete`, `Test`, `Health`, `Lease`), the error taxonomy (`Class`,
`Error`, `ClassOf`, the sentinels) and the conformance suite `integrations/integrationstest`. On the ABO side,
build **books' legacy `InstanceStore`** in `internal/books/configstore/instances.go`, which serves ABO's existing
instances (Deluge, AI endpoints, metadata sources, webhooks) in place, in HEAD's blob fields and secret rows,
and hand it to core through `Manifest.LegacyStores` (`legacy.Stores.Integrations`).

No integration call path of ABO changes in this PR: HEAD's Deluge, AI, metadata and webhook code keep serving
every call. Adapters, providerhttp, notify and the routes arrive in PC-16-02 … PC-16-06.

## Files

Create:
- `core/integrations/` — package `integrations` (types, `Registry`, `NativeInstanceStore`, errors,
  `export_test.go`, `legacy_mirror_test.go` in `package integrations_test`).
- `core/integrations/integrationstest/` — `InstanceStoreSuite(t, factory)`.
- `internal/books/configstore/instances.go` — books' legacy `InstanceStore`, implementing `legacy.Covering`
  and `legacy.WriteLock`, with compile-time assertions (`var _ legacy.Covering = (*instanceStore)(nil)`).
- `internal/books/configstore/instances_test.go` — HEAD-fixture tests and the two suites.

Modify:
- `internal/books/module.go` — set `Manifest.LegacyStores.Integrations` to the store above (one instance per
  App, shared with books' legacy config store; `TestLegacyStoresAreSingletons`).

See "Planner notes" for files this PR will also need that the inventory row does not list.

## Re-verify before editing

Run each at your base commit. Counts are at the evidence commit `748fd787a` (equal at this plan's HEAD for these
paths). A zero-hit edit target, or a moved count, means **STOP and report**; never patch around it.

```bash
# Prerequisites built by earlier waves (expect >= 1 each; 0 = the prerequisite has not merged: STOP)
test -d core/legacy && echo ok                       # PC-04-02
test -d internal/books/configstore && echo ok        # PC-04-05
git grep -n 'LegacyStores' -- internal/books/module.go | head -3
git grep -n 'func CommitLocked' -- core/storage | head -1          # PC-07-01
git grep -n 'func LocksHeld' -- core/storage | head -1             # PC-07-01
git grep -n 'LockConfigSettings' -- core/legacy | head -1          # 05-storage §5S.3.11 (S3/S4 waves)

# HEAD's lock for the settings blob (1 declaration at 748fd787a: internal/config/persistence.go:1897).
# At your base it may live under internal/books/configstore (05-config §1.6); require exactly 1 declaration.
git grep -n 'var configBlobSaveMu' -- '*.go' | grep -v _test        # expect 1

# Legacy sources the store serves (1 hit each at 748fd787a)
git grep -n 'type MetadataSource struct' -- internal/config/config.go        # config.go:148
git grep -n 'type AIEndpoint struct' -- internal/config/ai_endpoints.go      # ai_endpoints.go:36
git grep -n 'type PluginConfig struct' -- internal/config/config.go          # config.go:244
git grep -n 'func poolSecret' -- internal/ai/pool_routing.go                  # pool_routing.go:75
git grep -n 'DelugeWebURL ' -- internal/config/config.go | head -1           # config.go:1776
git grep -n 'func migrateAIEndpointsBlob' -- internal/config/ai_endpoints.go # ai_endpoints.go:291

# The package must not exist yet (0 hits); if it exists, this task may already be applied: run Acceptance instead
test -d core/integrations && echo EXISTS
```

## Steps

1. **Read first:** 05-metadata M1.1–M1.10 in full; 03 §3.3 "Two selection models" and "Legacy-only identifiers";
   05-storage §5S.3.11 (`legacy.Covering`, `legacy.WriteLock`, `storage.CommitLocked`, `storage.LocksHeld`,
   lock ranks); 05-config §1.3.7 (ProtectAdmin), §1.6 (the pending blob render), §2.2 (`legacy.SecretsStore`).
2. **Tests first** (list below): write them failing in `core/integrations` and `internal/books/configstore`.
3. Implement package `integrations` exactly as M1.3 declares it (copy the signatures below). Deferred-build members
   (`FacadeWriter`, `Registry.FacadeWriter`, `InstanceConfig.Tags`, the `"arrfacade"` origin) are **not** built.
   The Registry names `legacy.Covering` only through an unexported mirror interface with the identical method set,
   detected once per store; `integrations` never imports `legacy` (M1.1). `export_test.go` exports test aliases of
   the mirror and of the unexported record-ID helper; `legacy_mirror_test.go` asserts both directions.
4. Implement `NativeInstanceStore(ks)` over `c/integrations/instances/<kind>/<id>` with value
   `codec.JSON[integrations.StoredInstance](1)` and no index rows (M1.4 "Native store").
5. Add the legacy members to `core/legacy` as M1.3 "Legacy members" declares them: `IntegrationsCoverID(ref,
   adapter)` = `"<adapter>|<kind>/<instance>"`, and the `Integrations` field of `legacy.Stores`; add
   `legacytest.IntegrationsInstanceStoreSuite` to `core/legacy/legacytest`.
6. Implement the Registry rules of M1.3 `Put`/`Test` and M1.4 steps 2, 2a, 4 and M1.5, in particular:
   - routing per record by `Covers(legacy.IntegrationsCoverID(ref, adapter))`; a legacy half without `Covers`
     covers every record; the native half is never asked `Covers`;
   - a legacy-backed instance reads `Local=true`, stores no `Local`/`LocalOrigin`/`SecretsOrigin`/
     `OperatorSecretOrigin`, and a `Put` that sets a field HEAD's format has no place for is refused whole with
     `ErrFieldNotStorable` (422);
   - creating a legacy-backed instance, changing its destination or secret fields, or changing a field that
     selects which secret is sent (`auth_ref`, a metadata source's credential keys, the family-level webhook
     secret) needs the ProtectAdmin check (core:admin AND interactive), else 403 with nothing written — on `Put`
     and on `Test` (both routes' semantics; the routes themselves land in PC-16-06);
   - with auth off, the auth-disabled principal passes that check **only** for an instance routed to the legacy
     store (HEAD's auth-off rule); a native instance never inherits it;
   - operator overlay (`config.Store.PassthroughValues()`) is in memory only, never persisted; `BelowStored`
     names fill only an unstored field and lock nothing; canonical names overlay and lock (`ErrFieldLocked`, 409);
   - a destination change clears every secret not re-supplied (echoed/mask-shaped values are not re-supply),
     resets a native row's `Local`, audits `core.integration.change_url` with `secrets_dropped=true`;
   - `core.integrations.disabled` (M1.7): records load and `Put` stores, but no instance is built, `Lease`
     returns `ErrDisabled`, `Test` is refused 409, health reports `HealthDisabled` reason `integrations_disabled`.
7. **Commit path (R9-1, R10-4, R10-5, DATA10-3).** Every batch the legacy store staged into commits through
   `storage.CommitLocked(ctx, b, d, stage, r.settingsLocks...)`. The Registry receives `SettingsLocks
   []storage.Lock` from the kernel, which calls `WriteLocks([]string{legacy.LockConfigSettings})` at P4c; the
   Registry never calls `WriteLocks` and never names a lock string. The Registry's writer mutex is taken
   **before** `CommitLocked` and never inside a stage function. Wire the kernel side of that at P4c.
8. Implement books' store (`internal/books/configstore/instances.go`) over the M1.10 legacy-backed instance table
   (copy the table's instance IDs and secret names verbatim):
   - `download/deluge`, `ai/openai`, `ai/<row id>`, `metadata/google-books`, `metadata/hardcover`,
     `metadata/<source id>`, `notify/webhook-<n>` (n = 1-based position in `urls`, empty entries counted; a
     Delete blanks the entry, a create appends). `notify/email` is **not** legacy-backed.
   - It writes the settings blob through books' legacy config store, in the caller's batch, sharing that store's
     pending blob render; it offers `legacy.WriteLock` returning HEAD's `configBlobSaveMu` for
     `legacy.LockConfigSettings` (the same `storage.Lock` value books' config store returns); it refuses a write
     staged into a batch without a hold of that lock (`storage.LocksHeld(b, l)` false: nothing staged).
   - Its post-commit step rewrites the three gap-fill keys of `config.yaml` (OpenAI key, Google Books key,
     Hardcover token) from rows read inside the hold, never from the commit's own values.
   - It never reads or writes a secret field; secrets go through `secrets` and books' `legacy.SecretsStore`.
   - It keeps `openai_base_url` in step on a `Put` of `ai/openai`, as HEAD does (05-ai §10).
   - `Version` is a 64-bit truncated hash of the instance's legacy fields.
   - Legacy IDs outside the `InstanceID` grammar: lower-case, `_` → `-` on read, mapped back on write, logged once.
9. Wire `internal/books/module.go`: `LegacyStores.Integrations` = the one store instance.
10. Extend `TestLegacyFamiliesWrittenOnlyByLegacyStores`' allow-list only if its existing entries do not already
    admit `internal/books/configstore` for the blob family (09 §9.2.1; it should).
11. **Shippable alone (09 §9.2.1 "Soaks").** After this PR deploys, no ABO behaviour changes: no HEAD
    integration call goes through the Registry yet, and no new health token appears outside
    `ci/soak_health_allow.txt`. If you cannot meet that, STOP and report.
12. Run Acceptance; add a `changelog.d/` fragment (headerless); open the PR.

## Code contract (copy exactly; 05-metadata M1.3)

```go
package integrations // github.com/falkcorp/mediacore/integrations

type Kind string
const ( KindAI Kind = "ai"; KindDownload Kind = "download"; KindIndexer Kind = "indexer"; KindMetadata Kind = "metadata"; KindNotify Kind = "notify" )
type InstanceID string   // [a-z0-9][a-z0-9-]{0,31}
type AdapterID string    // "<moduleID>.<name>"
type Ref struct { Kind Kind; Instance InstanceID }
func (r Ref) String() string { return string(r.Kind) + "/" + string(r.Instance) }

func NewDescriptor[T Instance](m Meta, build func(ctx context.Context, env Env, inst InstanceConfig) (T, error)) Descriptor
func (d Descriptor) Meta() Meta

type InstanceStore interface {
	Load(ctx context.Context) ([]StoredInstance, error)
	Put(ctx context.Context, b *storage.Batch, inst StoredInstance) (uint64, error)
	Delete(ctx context.Context, b *storage.Batch, ref Ref) error
}
func NativeInstanceStore(ks storage.Keyspace) InstanceStore

func (r *Registry) Adapters(kind Kind) iter.Seq[Meta]
func (r *Registry) Instances(kind Kind) iter.Seq[InstanceConfig]
func (r *Registry) Config(ref Ref) (InstanceConfig, bool)
func (r *Registry) Put(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (InstanceConfig, error)
func (r *Registry) Delete(ctx context.Context, ref Ref) error
func (r *Registry) Test(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error)
func (r *Registry) Health(ctx context.Context, ref Ref) (Health, error)
type TestResult struct { OK bool; Class Class; Status int }
func Lease[T Instance](ctx context.Context, r *Registry, ref Ref) (T, func(), error)

func ClassOf(parent context.Context, err error) Class
var ( ErrNotConfigured, ErrDisabled, ErrUnknownAdapter, ErrFieldLocked, ErrFieldNotStorable, ErrOperatorSecretWithheld error ) // texts as M1.3

package legacy
func IntegrationsCoverID(ref integrations.Ref, a integrations.AdapterID) string // "<adapter>|<kind>/<instance>"
```

`InstanceConfig`, `StoredInstance`, `Meta`, `Env`, `Limits`, `EgressPolicy`, `Hold`, `GateState`, `Health`,
`HealthStatus`, `Class` and `Error` are declared field for field in M1.3; copy them, do not paraphrase. The
`HTTP`/`Gate`/`Ticket` interfaces are declared here (not in providerhttp) so `integrations` does not import
`providerhttp`. Registration slice: `func (r *Registrar) Adapter(d ...integrations.Descriptor)` in package
`module`, and `app.Options.Adapters` (06 owns both; S7 adds them, 06 §6.6).

## Tests to write first (TDD; names from 05-metadata M1.9 unless marked)

Core (`core/integrations`):
`TestRegistry_RejectsForeignAdapterPrefix`, `TestRegistry_UnknownAdapterRowRetained`,
`TestRegistry_HotSwapDrainsLeases` (synctest), `TestRegistry_OverlayNeverPersisted`,
`TestRegistry_OverlayRemovedRevertsToStoredRow`, `TestRegistry_LockedFieldWriteRefused`,
`TestRegistry_EnvOnlyInstanceHasStableID`, `TestRegistry_PutWritesRowAndSecretsAtomically`,
`TestRegistry_TestDoesNotPersist`, `TestRegistry_URLChangeDropsSecrets`, `TestRegistry_DestinationChangeDropsSecrets`,
`TestRegistry_APIKeyCannotSetLocal`, `TestRegistry_LocalNeedsAdmin`, `TestRegistry_TestReturnsNoBody`,
`TestPut_EchoedMaskIsNotResupply`, `TestNativeInstanceStore_RoundTrip`, `TestLegacyBacked_NoNativeCopy`,
`TestLegacyBacked_NativeOnlyFieldRefused`, `TestLegacyBacked_StoreLoadFailureServesNone`,
`TestRegistry_LegacyWithoutCoveringCoversAll`, `TestLegacyMirrorsMatch`,
`TestRegistry_BelowStoredFillsOnlyUnstoredField`, `TestPassthroughBelowStored_StoredURLNotLanTrusted`,
`TestRegistry_CanonicalOverlayLocksAndTrusts`, `TestRegistry_DisabledMakesNoOutboundCall`,
`TestPut_NonAdminURLDoesNotCarryEnvSecret`, `TestOperatorSecret_AdapterDefaultURLServed`,
`TestOperatorSecret_AdminStoredHostServed`, `TestRegistryTest_ChangedURLUsesNoStoredSecret`,
`TestRegistry_BuildDoesNoIO`, `TestClassOf_Table`, `TestError_RedactsDetail`, archtest
`IntegrationsDescriptorCallers`, and `integrationstest.InstanceStoreSuite` against `NativeInstanceStore`.

ABO (`internal/books/configstore`, with HEAD fixtures):
`integrationstest.InstanceStoreSuite` and `legacytest.IntegrationsInstanceStoreSuite` against books' store (both
run under the umbrella `TestLegacyStoreSuites`), `TestLegacyBacked_PlaintextSecretWrittenInPlace`,
`TestLegacyBacked_EncryptedSecretKeptInPlace`, `TestLegacyBacked_NoRecordBesideLegacyFields`,
`TestLegacyBacked_ConfigAliasGoesThroughPut`, `TestLegacyBacked_OperatorSecretServedAsHEAD`,
`TestPut_LegacyBackedDestinationNeedsProtectAdmin`, `TestPut_LegacyBackedCreateNeedsProtectAdmin`,
`TestPut_LegacyBackedAuthRefNeedsProtectAdmin`, `TestNonAdminCannotBindOperatorSecret`,
`TestRegistryTest_NonAdminCannotSendOperatorSecret`, `TestAuthOff_LegacyBackedIntegrationWritableAsHEAD`,
`TestPut_LegacyBackedCommitsUnderWriteLock`, `TestRegistryPutRewritesGapFillCopy`,
`TestGapFillClearNotUndoneByLateSet` (synctest), `TestRegistryPut_RacesLegacyConfigAlias` (synctest, `-race`),
`TestLegacyInstanceStageOutsideHoldRefused`, `TestLegacyWebhook_SharedFieldsFamilyWide`,
`TestPut_SecretChangeReleasesAuthHold` (may stay skipped until PC-16-02 lands the hold; say so in the PR).

`TestLegacyBacked_ConfiguredHostKeepsHEADReach` needs providerhttp's dial guard: it belongs to PC-16-02.
"ErrFieldNotStorable answers 422" is covered by `TestLegacyBacked_NativeOnlyFieldRefused` here; add the HTTP
422 case (name coined here: `TestIntegrationsProblem_FieldNotStorableIs422`) only if this PR declares the error's
Problem status; otherwise PC-16-06 adds it with the routes.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                     # exit 0
(cd core && go test ./integrations/... ./legacy/... -race -count=1) # every line "ok"; no FAIL
go test ./internal/books/... -race -count=1                       # every line "ok"; no FAIL
go test ./internal/books/... -run 'TestLegacyStoreSuites|TestLegacyFamiliesWrittenOnlyByLegacyStores|TestLegacyStoresAreSingletons' -count=1 -v | grep -E '^(--- FAIL|ok|FAIL)'   # no "--- FAIL"
git grep -n 'mediacore/legacy"' -- core/integrations | grep -v _test.go   # expect 0 lines (integrations never imports legacy)
git grep -n 'FacadeWriter\|Tags \[\]string' -- core/integrations | grep -v _test.go   # expect 0 (deferred-build)
make ci                                                           # exit 0
make -C core ci                                                   # exit 0
git status --porcelain                                            # empty after the builds
```

On the PR: `extraction-gates` (prev-build one-format tests included), `core-ci`, `docker-image`, `vet-darwin`,
`vet-integration` green on an up-to-date branch.

## Out of scope

providerhttp and the dial guard (PC-16-02); any adapter (PC-16-03, PC-16-04); notify and its rule store
(PC-16-04); the HTTP routes and aliases (PC-16-06); deleting `internal/plugin` (PC-16-06); the health task
`core.integrations.health` firing against real instances (no adapter exists yet); any cutover of a legacy family;
`FacadeWriter`, `Tags`, indexer.

## Rollback

Binary swap (09 S7 Rollback). Books' store writes HEAD's blob fields and secret rows in HEAD's format, so S6 reads
them. A native row in `c/integrations/instances/` is inert on S6 (09 §9.2.1 row 8; its release-note line is the
flip's). A revert of this PR is admitted by `check_soak.py` only as an exact `git revert` of its commits.

## Repo rules

- Never edit the primary checkout or `main`; one worktree and branch per task; remove the worktree after merge
  (`git worktree remove`, `git worktree prune`).
- Every new or changed file gets or bumps its 4-line versioned header; `changelog.d/` and `todo.d/` fragments
  get none. One `changelog.d/` fragment per PR.
- Conventional commits; every commit carries `Stage-Release: S7 1` and ends with
  `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr create`, then `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- CLAUDE.md concurrency: no library-scale loop without a bounded pool; the Registry's health parallelism is
  `core.integrations.health_parallelism` (`tasks.Fixed`), never unbounded goroutines.
- Post-task: CHANGELOG fragment, TODO, and the executive-summary criteria (`docs/process/executive-summaries.md`).
- Report with exact counts and the three lines `COMPLETED:`, `REMAINING:`, `BLOCKED:`.

## Planner notes

- The row lists no core wiring outside `core/integrations/`, but the brief cannot be done without: `core/legacy/`
  (`IntegrationsCoverID`, `Stores.Integrations`) and `core/legacy/legacytest/`
  (`IntegrationsInstanceStoreSuite`), `core/module/` (`Registrar.Adapter`, `Deps.Integrations`, `Manifest.Requires`
  where 06 §6.6 puts it at S7), `core/app/options.go` (`Options.Adapters`) and the kernel's P4c wiring of
  `SettingsLocks`. Add them to the row's files; no other W16 row edits them before PC-16-06 except
  `core/app/options.go`, which no W16 row lists.
- The done-test "ErrFieldNotStorable answers 422 (new test)" overlaps the spec-named
  `TestLegacyBacked_NativeOnlyFieldRefused`; the 422 HTTP mapping needs a route, which is PC-16-06's.
