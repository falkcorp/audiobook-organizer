<!-- file: docs/agent-tasks/platform-core/F7-01-core-integrations-registry-dark.md -->
<!-- version: 1.1.2 -->
<!-- guid: bc446db6-50ca-4aa7-a750-2bcc30316a89 -->
<!-- last-edited: 2026-11-05 -->

# F7-01 — Core integrations registry, dark: the one registry for integration instances, InstanceStore native over c/integrations/instances/, integrations.Importer, integrationstest, origin binding and the operator overlay

| Field | Value |
|---|---|
| ID | F7-01 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T4** (Opus / xhigh–max): instance records with secret binding and origin trust; a wrong rule sends a stored secret to a host the owner never approved |
| Wave | W27 (beside F2-03, F2-15, F2-17, F3-02, F3-04, F4-02; file-disjoint) |
| Depends on | F3-01 (core config: `Passthrough` operator values), F3-03 (core secrets: `secrets.Ref`, `secrets.Value`), F2-07 (audit store: `core.integration.*` actions) |
| Spec | 05-metadata M1 (M1.1 scope, M1.3 Go API, M1.4 lifecycle incl. the native store and the operator overlay of step 2, M1.5 errors, M1.6 concurrency, M1.7 keys, M1.8 metrics, M1.9 tests, M1.10 the migration table this step's importer serves); 03 §3.3 (`integrations` row, "Store interfaces and importers"); 04 §8.1 F7 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO registers no adapter and no
instance; its Deluge, AI, metadata and webhook code keeps HEAD's singletons until F7-09. The package runs on `hello`
and `coretest`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-01-integrations" -b agent/platform-core-f7-01-integrations origin/main
cd "$REPO/.worktrees/pc-f7-01-integrations"
git rebase origin/main
```

## Goal

Build package `integrations` (05-metadata M1.3), the one owner of every integration-instance record: kinds, instance
and adapter IDs, `Ref`, `Meta`, `Descriptor`/`NewDescriptor`, `InstanceConfig`, `Origin`, `Env`, `SecretReader`,
`HTTP`/`Gate`/`Ticket`, `Limits`/`DailyLimits`/`BreakerPolicy`/`HoldPolicy`, `EgressPolicy`, `Hold`, `GateState`,
`Instance`, `Health`, `InstanceStore` with `NativeInstanceStore(ks)` over `c/integrations/instances/<kind>/<id>`,
`StoredInstance`, `Importer` with `NativeImporter(ks)`, `Registry` (`Adapters`, `Instances`, `Config`, `Put`,
`Delete`, `Test`, `Health`), `Lease[T]`, the error taxonomy (`Class`, `Error`, `ClassOf`), and the public test kit
`integrations/integrationstest` with `InstanceStoreSuite`. Operator-layer values arrive as 05-config `Passthrough`
values and are an **in-memory overlay, never persisted** (M1.4 step 2, SIMP2-5).

Not in R2's build (M1.3 "Deferred-build members"): `integrations.FacadeWriter`, `Registry.FacadeWriter`, the
`"arrfacade"` origin, `InstanceConfig.Tags`.

## Invariants (each is an M1.9 test)

- **I1.** The overlay is never persisted: an env value creates no row and no `c/secrets/` write
  (`TestRegistry_OverlayNeverPersisted`); removing it reverts to the stored row
  (`TestRegistry_OverlayRemovedRevertsToStoredRow`).
- **I2.** Origin binding: a URL or destination change drops stored secrets unless re-supplied
  (`TestRegistry_URLChangeDropsSecrets`, `TestRegistry_DestinationChangeDropsSecrets`,
  `TestPut_EchoedMaskIsNotResupply`); an instance-scoped `Test` with a changed origin uses no stored secret
  (`TestRegistryTest_ChangedURLUsesNoStoredSecret`); an operator secret follows only admin-set origins
  (`TestPut_NonAdminURLDoesNotCarryEnvSecret`, `TestOperatorSecret_AdapterDefaultURLServed`,
  `TestOperatorSecret_AdminStoredHostServed`).
- **I3.** `Local` (private-network reach) needs an interactive admin, never an API key, never auth-off
  (`TestRegistry_LocalNeedsAdmin`, `TestRegistry_APIKeyCannotSetLocal`, `TestAuthOff_LocalNotSettable`).
- **I4.** `Put` writes the row and its secrets atomically (`TestRegistry_PutWritesRowAndSecretsAtomically`);
  `Test` persists nothing and returns no body (`TestRegistry_TestDoesNotPersist`, `TestRegistry_TestReturnsNoBody`).
- **I5.** `core.integrations.disabled` makes no outbound call at all (`TestRegistry_DisabledMakesNoOutboundCall`);
  building does no I/O (`TestRegistry_BuildDoesNoIO`).
- **I6.** The importer stages into the caller's batch only, with Origin `migration` and the given trust fields, and
  keeps and reports a record it cannot validate (`TestImporter_ValidatesAndStagesInCallerBatch`).
- **I7.** Core names no ABO key or adapter (`TestCoreHoldsNoAppKeyNames`); adapter IDs carry their module prefix
  (`TestRegistry_RejectsForeignAdapterPrefix`).

## Files

| Action | Path |
|---|---|
| create | `core/integrations/integrations.go` (kinds, IDs, `Ref`, `Meta`, `Descriptor`, `InstanceConfig`, `Env`, `Limits`, `Health`, errors) |
| create | `core/integrations/registry.go` (`Registry`, overlay, origin binding, `Lease`) |
| create | `core/integrations/instance_store.go` (`InstanceStore`, `StoredInstance`, `NativeInstanceStore`) |
| create | `core/integrations/importer.go` (`Importer`, `NativeImporter`) |
| create | `core/integrations/integrationstest/` (`InstanceStoreSuite`, fakes) |
| create | `core/internal/instancestore/` (the native store's implementation over `storage.Keyspace`) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# Nothing of integrations exists beyond F2-11's minimal providerhttp/ai packages. Expected: no integrations.go
ls core/integrations/ ; ls core/integrations/integrations.go 2>&1 | grep -c 'No such file'
# Secrets refs and values (F3-03). Expected: type Ref and type Value in core/secrets
grep -n 'type Ref \|type Value ' core/secrets/*.go
# Config passthrough (F3-01). Expected: a Passthrough type or accessor in core/config
grep -rn 'Passthrough' core/config/*.go | head -3
# Audit actions (F2-07). Expected: core.integration.* actions in the normative table
grep -rn '"core.integration\.' core/audit/*.go | head
# The fixed core order includes integrations. Expected: "integrations" in the subsystem order list
grep -rn '"integrations"' core/storage/*.go | head -3
```

STOP if `secrets.Ref`/`secrets.Value` or the config passthrough is missing, or if `core/integrations/integrations.go`
already exists.

## Steps

1. Read 05-metadata M1 in full; copy every type, field and signature of M1.3 exactly (the code contract excerpt below
   is not complete).
2. Write the M1.9 tests first (below) and `integrationstest.InstanceStoreSuite`.
3. Implement the types, the native store (`c/integrations/instances/<kind>/<id>`, versioned `Put` returning the new
   version), the importer, and the registry: load, effective view (stored row plus overlay, `Locked` fields),
   `Put`/`Delete` with the origin rules and the ProtectAdmin checks, `Test` on an unsaved config, health, hot swap
   with lease draining (`TestRegistry_HotSwapDrainsLeases` under `synctest`), a store load failure serving none.
4. The error taxonomy and `ClassOf` with the done-parent rule (`TestClassOf_Table`); `Error` redacts `Detail`.
5. archtest `IntegrationsDescriptorCallers`: only kind packages call `NewDescriptor`.
6. Metrics: M1.8's families under the App's namespace. This adds the core `integrations` metrics scope: add its `testdata/series.golden` under `core/integrations/`, asserted by
   `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
7. Wire on `hello`/`coretest` only. Headers, fragment, acceptance.

## Code contract (05-metadata M1.3, verbatim excerpts)

```go
package integrations // github.com/falkcorp/mediacore/integrations
func NewDescriptor[T Instance](m Meta, build func(ctx context.Context, env Env, inst InstanceConfig) (T, error)) Descriptor
type InstanceStore interface {
	Load(ctx context.Context) ([]StoredInstance, error)
	Put(ctx context.Context, b *storage.Batch, inst StoredInstance) (uint64, error)
	Delete(ctx context.Context, b *storage.Batch, ref Ref) error
	// … exactly as M1.3
}
func NativeInstanceStore(ks storage.Keyspace) InstanceStore
type Importer interface {
	ImportInstance(ctx context.Context, b *storage.Batch, inst StoredInstance) error
}
func NativeImporter(ks storage.Keyspace) Importer
func (r *Registry) Adapters(kind Kind) iter.Seq[Meta]
func (r *Registry) Instances(kind Kind) iter.Seq[InstanceConfig]
func (r *Registry) Config(ref Ref) (InstanceConfig, bool)
func (r *Registry) Put(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (InstanceConfig, error)
func (r *Registry) Delete(ctx context.Context, ref Ref) error
func (r *Registry) Test(ctx context.Context, inst InstanceConfig, secretsIn map[string]secrets.Value) (TestResult, error)
func (r *Registry) Health(ctx context.Context, ref Ref) (Health, error)
func Lease[T Instance](ctx context.Context, r *Registry, ref Ref) (T, func(), error)
func ClassOf(parent context.Context, err error) Class
```

## Tests (write first; spec names copied exactly, 05-metadata M1.9)

Metrics: `metricstest.AssertSeries` over the `integrations` scope's `testdata/series.golden` (05-metrics §12).

`TestRegistry_RejectsForeignAdapterPrefix`, `TestRegistry_UnknownAdapterRowRetained`,
`TestRegistry_HotSwapDrainsLeases`, `TestRegistry_OverlayNeverPersisted`,
`TestRegistry_OverlayRemovedRevertsToStoredRow`, `TestRegistry_OperatorOverlayLocksAndTrusts`,
`TestRegistry_LockedFieldWriteRefused`, `TestRegistry_EnvOnlyInstanceHasStableID`,
`TestRegistry_PutWritesRowAndSecretsAtomically`, `TestRegistry_TestDoesNotPersist`,
`TestRegistry_URLChangeDropsSecrets`, `TestRegistry_DestinationChangeDropsSecrets`,
`TestRegistry_APIKeyCannotSetLocal`, `TestRegistry_LocalNeedsAdmin`, `TestAuthOff_LocalNotSettable`,
`TestRegistry_TestReturnsNoBody`, `TestPut_EchoedMaskIsNotResupply`, `TestNativeInstanceStore_RoundTrip`,
`TestRegistry_StoreLoadFailureServesNone`, `TestPut_SecretChangeReleasesAuthHold`,
`TestRegistry_DisabledMakesNoOutboundCall`, `TestPut_NonAdminURLDoesNotCarryEnvSecret`,
`TestOperatorSecret_AdapterDefaultURLServed`, `TestOperatorSecret_AdminStoredHostServed`,
`TestRegistryTest_ChangedURLUsesNoStoredSecret`, `TestImporter_ValidatesAndStagesInCallerBatch`,
`TestRegistry_BuildDoesNoIO`, `TestClassOf_Table`, `TestError_RedactsDetail`; archtest
`IntegrationsDescriptorCallers`; `integrationstest.InstanceStoreSuite` against `NativeInstanceStore`.
Not here: `TestFacadeIndexer_LANRefused` (deferred-build) and the `TestIntegrationsMigration_*` set (F7-08).

## Acceptance (commands and expected output)

```bash
# Package tests with the race detector. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./integrations/ ./integrations/integrationstest/... ./internal/instancestore/...)

# No ABO name in core. Expected: ok
(cd core && go test -count=1 -run 'TestCoreHoldsNoAppKeyNames|TestCoreImportsOnlyCore' ./archtest/...)

# Deferred members absent. Expected: 0
grep -rn 'FacadeWriter\|"arrfacade"' core/integrations/*.go | wc -l

# Dark: nothing in ABO imports it yet. Expected: 0
grep -rl --include='*.go' '"github.com/falkcorp/mediacore/integrations"' internal/ cmd/ main.go 2>/dev/null | wc -l

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- The kind packages (`ai`, `download`, `notify`, `metadata`, `providerhttp` completion: F7-02 … F7-05).
- Books' M6 step (F7-08) and any ABO wiring (F7-09).
- The facade writer and `Tags` (deferred-build).
- The HTTP surface of M1.3 (the adapters list, the `{kind}/instances` routes, the two test routes, health and
  `DELETE …/hold`): F7-10 builds it in `core/internal/integrationhttp/` over this row's `Registry`, and F7-09 mounts
  it. Keep `Registry`'s methods exactly as M1.3 declares them so F7-10 needs nothing more.

## Rollback

Revert the PR; nothing in ABO uses it and no stored data is written.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file.
- Conventional commit, for example `feat(integrations): core instance registry and native store (dark)`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- `Registry` state is per `App` (R5): no package-level mutable state.

