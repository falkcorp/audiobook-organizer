<!-- file: docs/agent-tasks/platform-core/F7-02-providerhttp-and-metadata-contract.md -->
<!-- version: 1.1.2 -->
<!-- guid: 691c2fbf-fba4-4b6d-b8ca-dad283253f2b -->
<!-- last-edited: 2026-11-05 -->

# F7-02 — providerhttp completed and the metadata contract, dark: GateStore native over c/providerhttp/, quotas and throttle overrides, providerhttptest, integrations/metadata; internal/security/safehttp moved

| Field | Value |
|---|---|
| ID | F7-02 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T3** (Opus / high): quota and throttle state with fail-closed reads; the egress guard is the SSRF boundary for every adapter |
| Wave | W30 (beside F2-16, F4-05, F5-15, F6-01, F6-02, F8-01, F8-03; file-disjoint). Runs inside an announced **move window** (04 key `F7 move internal/security/safehttp -> mediacore/integrations/providerhttp`) |
| Depends on | F7-01 (integrations registry: `Ref`, `Gate`, `Hold`, `Limits`, `Class`), F2-11 (F2's minimal `providerhttp` with `providerhttp.InterimHolds` and the `core.integrations.release-holds` registration) |
| Spec | 05-metadata M2 (M2.1–M2.11: `Registry`, `GateStore`/`NativeGateStore`, `Client`, `Policy`, holds, breaker with probe lease, token bucket, daily quota with tiers and zone, egress guard, retry, redaction, `Classify`, the four HEAD defects it removes), M3 (M3.3 Go API: `Provider`, `Lookup`, `Caps`, `Adapter`, `Result`, `Chain`, `Cache`, `Cached`; M3.9 tests); 04 §8.1 F7, §3.4 (`internal/security/safehttp` row); 05-tasks §5T.8.2 (`core.integrations.release-holds` stays on its interim body until F7-09) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's metadata code keeps HEAD's
`internal/metadata/providerhttp`, `dailyquota`, `throttle*` and `circuitbreaker`; `core.integrations.release-holds`
keeps running over `providerhttp.InterimHolds` (books' backend over HEAD's rows) until F7-09. The one change ABO sees is
the safehttp move, which only re-points its two importers with unchanged behaviour.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-02-providerhttp" -b agent/platform-core-f7-02-providerhttp origin/main
cd "$REPO/.worktrees/pc-f7-02-providerhttp"
git rebase origin/main
```

Announce the move window one working day ahead naming the 04 row for `internal/security/safehttp`.

## Goal

1. Complete `core/integrations/providerhttp` (05-metadata M2.3): the per-`App` `Registry` (`New(Options)`), `Client`
   (`Client()`, `Do`, `GetJSON`, `Gate`, `SetLimits` mutating in place), `GateFor`, `ReleaseHold`, `Holds`,
   `WithInteractive`, `WithBypassHold`, `Classify`, the `GateStore` interface with `NativeGateStore(ks)` over
   `c/providerhttp/`, per-provider throttle overrides (`Policy`, `Limits`), holds (reason-aware, clamped, persisted),
   the breaker with a probe lease, the token bucket shared per scope, the daily quota with background/interactive
   tiers and a configurable zone, the egress guard (moved safehttp dialer for every `EgressPolicy`), bounded retry
   honouring `Retry-After`, redaction; and the native body of `core.integrations.release-holds` (Preview lists, Live
   deletes) built beside the interim one.
2. `core/integrations/providerhttp/providerhttptest`: `GateStoreSuite(t, factory)`.
3. `core/integrations/metadata` (M3.3): the generic `Provider[Q, R]` contract, `Chain` (hold-aware, bounded fan-out),
   `Cache` (positive/negative TTL, single-flight, bypass and read-only modes), `Cached`, and the fakes
   `coretest.FakeProvider[Q, R]` and `coretest.ProviderHTTP(t)` (M3.9).
4. Move `internal/security/safehttp` into `core/integrations/providerhttp` (04 §3.4) and re-point
   `internal/covers/covers.go` and `internal/metadata/cover.go`.

## Invariants (each is an M2.9/M3.9 test)

- **I1. HEAD's four defects stay fixed.** One bucket per scope after a limit change
  (`TestBucketSharedAcrossClientsOfScope`, `TestSetLimitsMutatesInPlace`); a breaker probe lease expiring reopens it
  (`TestBreaker_LeaseExpiryReopens`); cancellation never counts (`TestBreaker_IgnoresCanceled`); breaker state
  survives a chain edit (`TestChain_EditKeepsBreaker`).
- **I2. Fail closed.** A quota store read failure refuses (`TestQuotaStoreReadFailureFailsClosed`); concurrent
  reservations lose no count (`TestQuota_ConcurrentReservationsNoLostCount`, `-race`).
- **I3. Egress.** Private ranges refused unless `Local`; link-local always refused; rebinding checked at dial; a `Local`
  instance cannot dial or redirect to another private host (the `TestEgress_*` and `TestLocalInstance_*` tests).
- **I4.** `core.integrations.disabled` refuses before dial, writing nothing (`TestGate_DisabledRefusesBeforeDial`).
- **I5. No globals** in core (`TestNoGlobals`): HEAD's `limiters`, `clients`, `overrides`, `defaultThrottles` and
  `dailyquota.registry` maps have no core counterpart.
- **I6. The move changes no behaviour.** `internal/covers` and `internal/metadata/cover.go` behave as before, with the
  same guard (their existing tests pass unchanged).
- **I7. Dark.** ABO's metadata chain and `core.integrations.release-holds`'s interim body are unchanged.

## Files

| Action | Path |
|---|---|
| modify / grow | `core/integrations/providerhttp/` (registry, client, gate, holds, breaker, bucket, quota, egress, retry, classify, `NativeGateStore`, the native release-holds body) |
| create | `core/integrations/providerhttp/providerhttptest/` |
| create | `core/integrations/metadata/` |
| move (`git mv`) | `internal/security/safehttp/` → `core/integrations/providerhttp/` (as the egress dialer file(s)) |
| modify | `internal/covers/covers.go` and `internal/metadata/cover.go` (the import line only: the two safehttp importers at the plan's base; touch no other file of either package) |
| modify | `internal/arch/layering_test.go` (`internal/security/safehttp` key removed), `scripts/ci/short_test_weights.json` (only if it names the package) |
| create | `core/coretest/provider.go` (`coretest.FakeProvider[Q, R]`, `coretest.ProviderHTTP(t)`; M3.9 puts both in `coretest`; this row owns this one file of `core/coretest/` in W30, F7-03 owns `ai.go`, F2-16 `tasks.go` and `harness.go`) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
M=github.com/falkcorp/audiobook-organizer
# 1. safehttp and its importers. [evidence: safehttp.go + test; importers internal/metadata/cover.go, internal/covers/covers.go]
ls internal/security/safehttp/
grep -rl --include='*.go' "\"$M/internal/security/safehttp\"" . | grep -v '^./internal/security/safehttp/'
# 2. F2-11's minimal providerhttp. Expected: InterimHolds and the release-holds registration; no Registry yet
grep -rn 'InterimHolds\|core.integrations.release-holds' core/integrations/providerhttp/*.go
grep -rn 'type Registry struct' core/integrations/providerhttp/ | wc -l          # expected: 0
# 3. HEAD code whose rules are re-implemented. [evidence: providerhttp.go, dailyquota.go, throttle.go,
#    throttle_registry.go, circuitbreaker.go under internal/metadata]
ls internal/metadata/providerhttp/providerhttp.go internal/metadata/dailyquota/dailyquota.go \
   internal/metadata/throttle.go internal/metadata/throttle_registry.go internal/metadata/circuitbreaker.go
# 4. F7-01's types. Expected: Hold, Gate, Limits, Class
grep -n 'type Hold struct\|type Gate interface\|type Limits struct\|type Class uint8' core/integrations/*.go
# 5. The layerOf key. Expected: 1 line
grep -n '"internal/security/safehttp"' internal/arch/layering_test.go
```

STOP if grep 1 finds an importer other than the two named (plan base: exactly `internal/covers/covers.go` and
`internal/metadata/cover.go`, plus the `layerOf` key in `internal/arch/layering_test.go`), if `providerhttp.Registry` already exists, or if F7-01's
types are missing.

## Steps

1. Read 05-metadata M2 and M3 in full; copy every type and signature exactly.
2. Write the tests first (below).
3. `git mv internal/security/safehttp/safehttp.go core/integrations/providerhttp/egress.go` (and its test); make it the
   dial guard for every `EgressPolicy`; re-point the two importers to the core package (ABO may import core's public
   packages; check that `providerhttp` is public and not under `core/internal/`). Keep this commit building on its own.
4. Implement the registry, client, gate, holds, breaker, bucket, quota and classify, reusing HEAD's code where M2.10
   says so, with the globals removed.
5. `NativeGateStore` over `c/providerhttp/` with `Expect` where a counter is read-modify-written.
6. The native release-holds body (Preview/Live) beside F2-11's interim body; F7-09 switches the registration.
7. `providerhttptest.GateStoreSuite`; `integrations/metadata`'s chain and cache; `coretest.FakeProvider` and
   `coretest.ProviderHTTP` in `core/coretest/provider.go` only.
8. Layering key, headers, fragment, acceptance.

## Code contract (05-metadata M2.3 and M3.3, verbatim excerpts; copy the rest from the spec)

```go
package providerhttp // github.com/falkcorp/mediacore/integrations/providerhttp
type GateStore interface {
	Holds(ctx context.Context) ([]integrations.Hold, error)
	PutHold(ctx context.Context, h integrations.Hold) error // zero Until deletes the record
	Quota(ctx context.Context, scope, counter string) (day string, used int, err error)
	PutQuota(ctx context.Context, scope, counter, day string, used int) error
}
func NativeGateStore(ks storage.Keyspace) GateStore
func New(opts Options) (*Registry, error)
func (r *Registry) Client(ref integrations.Ref, p Policy) *Client
func (c *Client) Do(req *http.Request) (*http.Response, error)
func (c *Client) GetJSON(ctx context.Context, url string, out any) error
func (c *Client) SetLimits(l integrations.Limits)
func (r *Registry) GateFor(ref integrations.Ref, p Policy) integrations.Gate
func (r *Registry) ReleaseHold(ctx context.Context, scope string) error
func Classify(ctx context.Context, resp *http.Response, body []byte, err error) integrations.Class

package metadata // github.com/falkcorp/mediacore/integrations/metadata
func NewChain[Q, R any](reg *integrations.Registry, opts ChainOptions[Q]) (*Chain[Q, R], error)
func (c *Chain[Q, R]) Search(ctx context.Context, q Q) ([]Result[R], []Outcome, error)
func NewCache[Q any](opts CacheOptions[Q]) (*Cache[Q], error)
func Cached[Q, R any](ctx context.Context, c *Cache[Q], p Provider[Q, R], q Q) (rs []R, askedAt time.Time, err error)
```

## Tests (write first; spec names copied exactly)

M2.9: `TestBucketSharedAcrossClientsOfScope`, `TestSetLimitsMutatesInPlace`, `TestHoldPersistsAcrossRegistryRestart`,
`TestHoldClampedToMaxOnLoad`, `TestReleaseHoldsTask_PreviewListsLiveReleases`, `TestHold_EarlySuccessKeepsHold`,
`TestBreaker_LeaseExpiryReopens`, `TestBreaker_IgnoresCanceled`, `TestQuotaTiers`, `TestQuotaDayRollsInZone`,
`TestQuotaStoreReadFailureFailsClosed`, `TestClassify_Google403RateLimitIsRateLimited`,
`TestClassify_DailyQuotaBodyBeatsStatus`, `TestRetryHonoursRetryAfterAndClamp`, `TestNoRetryWithoutGetBody`,
`TestEgressPublicOnlyRefusesPrivate`, `TestEgress_PrivateRefusedUnlessLocal`, `TestEgress_LANAllowListAdmits`,
`TestEgress_LinkLocalAlwaysRefused`, `TestEgress_RebindingCheckedAtDial`, `TestEgressSameHostOrPublic`,
`TestLocalInstance_OtherPrivateHostRefused`, `TestLocalInstance_RedirectToOtherPrivateHostRefused`,
`TestRedactsApikeyInURLError`, `TestNoGlobals`, `TestNativeGateStore_RoundTrip`, `TestGate_DisabledRefusesBeforeDial`,
`TestQuota_ConcurrentReservationsNoLostCount`; `providerhttptest.GateStoreSuite` against `NativeGateStore`.
M3.9: `TestChain_SkipsHeldProvider`, `TestChain_FirstHitStopsInPriorityOrder`, `TestChain_NoAnswerIsError`,
`TestChain_NotFoundIsEmpty`, `TestChain_FanOutBounded`, `TestChain_EditKeepsBreaker`, `TestCache_NegativeTTL`,
`TestCache_ErrorsNeverWritten`, `TestCache_ReadOnlyWritesNothing`, `TestCache_SingleFlight`,
`TestCache_VersionBumpInvalidates`, `TestCache_UndecodableIsMiss`.

## Acceptance (commands and expected output)

```bash
M=github.com/falkcorp/audiobook-organizer
# Core packages with the race detector. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./integrations/providerhttp/... ./integrations/metadata/...)

# safehttp moved. Expected: nothing / 0
ls internal/security/safehttp 2>/dev/null
grep -rl --include='*.go' "\"$M/internal/security/safehttp\"" . | wc -l

# ABO importers unchanged in behaviour. Expected: ok
go test -count=1 ./internal/covers/... ./internal/metadata/...

# Dark: the interim release-holds body is still what ABO registers. Expected: >=1 hit in ABO's wiring
grep -rn 'InterimHolds' --include='*.go' main.go internal/ | head -3

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Books' metadata adapters (`books.<id>`), the chain's use in `internal/metafetch`, deleting HEAD's throttle/quota code
  and dropping HEAD's rows (F7-08 drops the rows in M6; F7-09 wires and deletes).
- Switching `core.integrations.release-holds` to the native body (F7-09).
- The provider-throttle SPA panel (books widget; F7-09/F9).

## Rollback

Revert the PR: safehttp returns to `internal/security/safehttp`; nothing else in ABO changed.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file; the moved file keeps its guid.
- PR description lists `internal/security/safehttp ==> core/integrations/providerhttp` (09 §9.2.10).
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin`. The rename into `core/` makes `scripts/ci/check_commits_build.py` run: every commit
  of the PR must build.
- `Chain`'s fan-out is bounded (`TestChain_FanOutBounded`); never unbounded goroutines (CLAUDE.md).

