<!-- file: docs/agent-tasks/platform-core/PC-16-02-providerhttp-instance-scoped-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: f91cb61f-3d0e-4cc3-996d-eb6d46ab879a -->
<!-- last-edited: 2026-11-04 -->

# PC-16-02 — `integrations/providerhttp`: instance-scoped throttle, quota, breaker and retry with `GateStore`; safehttp moves in

| Field | Value |
|---|---|
| ID | PC-16-02 |
| Title | integrations/providerhttp: instance-scoped throttle, quota, breaker and retry with GateStore; safehttp moves in |
| Tier | **T3 — Opus / high.** Outbound resilience without package globals, a routed gate store over HEAD's hold and budget rows, and the egress guard that decides what reaches the LAN. |
| Wave | W16 (stage S7, release `S7`) |
| Depends on | PC-16-01 |
| Trailer | `Stage-Release: S7 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 (row `integrations/providerhttp`), §4 (M2.3), §5 rules 10, 13, 16 |
| Owning spec | 05-metadata M2 (M2.1–M2.10); 09 S7 "Moves", "Data/keys"; 04 §8.1 row S7 (`mediacore/integrations/providerhttp`) |

**Gate.** Merges only after the `S6` soak row exists (`check_soak.py` check (b)).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-16-02" -b feat/pc-16-02-providerhttp origin/main
cd "$REPO/../abo-pc-16-02"
git worktree list
make web-deps
```

## Goal

One per-App `providerhttp.Registry` composes, inside the transport, a persisted reason-aware **hold**, a
**circuit breaker** with a probe lease, a **token bucket**, a **daily quota**, the **egress dial guard**, bounded
**retry**, a user agent and **redaction**, keyed per `Scope` and never in a package global (M2.1, M2.6). It is
built in `app.New` before any instance, loads holds and today's counters from `NativeGateStore`
(`c/providerhttp/`) and, for the scopes it covers, from **books' legacy `GateStore`** over HEAD's
`provider_throttle:` and `provider_daily_budget:` rows (M2.10). The SSRF guard of `internal/security/safehttp`
becomes the dial guard for every `EgressPolicy`. ABO's metadata callers move onto instance-scoped clients.
The three HEAD defects of M2.2 (bucket split, breaker stuck half-open, breaker counting our own cancellation) are
fixed by construction.

## Files

Create:
- `core/integrations/providerhttp/` (package `providerhttp`: `Registry`, `Options`, `GateStore`,
  `NativeGateStore`, `Client`, `Policy`, `Classify`, `WithInteractive`, `WithBypassHold`, `export_test.go`,
  `legacy_mirror_test.go` in `package providerhttp_test`).
- `core/integrations/providerhttp/providerhttptest/` (`GateStoreSuite(t, factory)`).

Modify (import rewiring and the moved code; re-derive the importer list at your base, see greps):
- `internal/metadata/providerhttp/` — code reused into core; the ABO package is deleted or reduced to nothing
  once its importers are rewired (no package globals remain anywhere).
- `internal/security/safehttp/` — merged into the dial guard (04 row: "merge"); its importers `internal/covers`
  and `internal/metadata` rewired.
- `internal/metadata/dailyquota/` — stays at its path as the **quota half of books' legacy `GateStore`** over
  `provider_daily_budget:` (04 §2.4, 09 S7 "Moves"); its in-process `registry` map goes.
- `internal/catalog/`, `internal/covers/`, `internal/metafetch/` — callers rewired onto `Registry.Client`.

## Re-verify before editing

Counts at `748fd787a` (unchanged at this plan's HEAD). Zero hits on an edit target or a moved count: STOP.

```bash
git grep -n '^\s*limiters\s*= map\|^\s*clients\s*= map\|^\s*overrides\s*= map' -- internal/metadata/providerhttp/providerhttp.go | grep -v _test   # 3 (lines 83-85)
git grep -n 'func SetLimits\|func ResetProvider\|func ClientWithTransport' -- internal/metadata/providerhttp/providerhttp.go   # 3 (92, 109, 239)
git grep -n 'registry = map\[string\]\*DailyBudget' -- internal/metadata/dailyquota/dailyquota.go    # 1 (310)
git grep -n 'const KeyPrefix = "provider_daily_budget:"' -- internal/metadata/dailyquota/dailyquota.go # 1 (85)
git grep -n 'providerThrottlePfx = "provider_throttle:"' -- '*.go' | grep -v _test   # 1; at 748fd787a internal/database/provider_throttle.go:29, at your base internal/books/store/ (PC-07-04)
git grep -n 'func NewTransport\|func NewClient' -- internal/security/safehttp/safehttp.go   # 2 (248, 261)
ls internal/metadata/throttle.go internal/metadata/throttle_registry.go internal/metadata/circuitbreaker.go   # all three exist

# Importers to rewire (counts at 748fd787a; use your base's generated counts in the PR description)
M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/metadata/providerhttp\"" -- '*.go' | grep -v _test.go | wc -l   # 9 files in internal/metadata, internal/metafetch, internal/server
git grep -l "\"$M/internal/security/safehttp\"" -- '*.go' | grep -v _test.go | wc -l       # 2 (internal/covers, internal/metadata)
git grep -l "\"$M/internal/metadata/dailyquota\"" -- '*.go' | grep -v _test.go | wc -l     # 4 (internal/metadata, .../providerhttp, internal/metafetch, internal/server)

# Prerequisite from PC-16-01 (expect >= 1)
git grep -n 'type HTTP interface' -- core/integrations | head -1
git grep -n 'func ProviderHTTPCoverID' -- core/legacy | head -1      # if 0, add it here (M1.3 "Legacy members")
```

## Steps

1. Read 05-metadata M2 in full, plus M1.3 (`Limits`, `EgressPolicy`, `Hold`, `GateState`, `Gate`, `Ticket`),
   M1.4 "Outbound integrations off", and M1.4 step 2a's private-network reach for legacy-backed instances.
2. Write the tests below first.
3. Build `providerhttp` from `internal/metadata/providerhttp` (Client, limits, retry code reused; globals removed),
   `internal/metadata/throttle*.go` (holds; `ClassifyProviderError` → `providerhttp.Classify`),
   `internal/metadata/circuitbreaker.go` (breaker, now with a probe lease; `ProtectedSource`/`NewChainSource`
   deleted, because the gate is in the transport) and `internal/security/safehttp` (dial guard).
4. Transport order per attempt is exactly M2.4's block: ctx done → hold (unless `WithBypassHold`) → breaker
   (single probe lease after cooldown) → `bucket.Wait` → `quota.Reserve(tier, counter)` → egress dial guard on the
   **resolved** address → `base.RoundTrip` → redirect re-check under `EgressPublicOnly` for another host →
   classify → breaker/hold/metrics → retry only if `Class.Retryable()`, attempts remain and the body is replayable.
5. `core.integrations.disabled` true: `Acquire` refuses with `ClassRefusedLocal` before dialling and writes no
   hold, breaker or quota state.
6. Routed gate store: a hold or counter goes to `Options.Legacy` when it covers
   `legacy.ProviderHTTPCoverID(scope, counter)` (`"<counter>|<scope>"`, counter `""` for the hold), else to
   `Options.Store`. Name `legacy.Covering` through an unexported mirror; never import `legacy`.
7. Books' legacy `GateStore`: holds over `provider_throttle:<id>` (books' store code from `internal/books/store`)
   and the default `requests` counter over `provider_daily_budget:<id>` (`internal/metadata/dailyquota`), for
   scopes `metadata/<id>` only; it implements `legacy.Covering` with a compile-time assertion; nothing copied,
   nothing deleted. Wire it as `legacy.Stores.Gates` in `internal/books/module.go` (see Planner notes).
8. Every gate write commits its own batch, outside any `CommitLocked` hold; the Registry serialises
   read-modify-write of one scope's hold and of one counter under its own mutex across the store's commit (M2.3,
   M2.6). Holds and quota load **before** any instance is built (replaces the comment-ordering of
   `cmd/root.go:405-418` at `748fd787a`).
9. Wire `Env.HTTP` and `Env.Gate` of PC-16-01's Registry to `Registry.Client(ref, policy)` and `GateFor`;
   `Policy.Private(host)` = `InstanceConfig.Local` AND host is one of the instance's configured URL hosts, or host
   in `core.integrations.egress.lan_allow`; never for `EgressPublicOnly`, never for link-local.
10. Rewire ABO callers (`internal/metafetch` chain build, `internal/catalog`, `internal/covers`, `internal/server`
    metadata ops) onto instance-scoped clients; `Client.SetLimits` mutates the bucket in place (no
    `ResetProvider`). Keep HEAD's observable metadata behaviour: same budgets, same hold durations, same tier rule.
11. Run `archtest.TestNoGlobals` over the moved code; there is no package-level throttle or quota map left in ABO
    or core.

## Code contract (05-metadata M2.3; copy exactly)

```go
package providerhttp // github.com/falkcorp/mediacore/integrations/providerhttp
type Options struct { Store GateStore; Legacy GateStore; Base http.RoundTripper; UserAgent string; Metrics *metrics.Scope; Log *slog.Logger; Now func() time.Time }
type GateStore interface {
	Holds(ctx context.Context) ([]integrations.Hold, error)
	PutHold(ctx context.Context, h integrations.Hold) error // zero Until deletes the record
	Quota(ctx context.Context, scope, counter string) (day string, used int, err error)
	PutQuota(ctx context.Context, scope, counter, day string, used int) error
}
func NativeGateStore(ks storage.Keyspace) GateStore
func New(opts Options) (*Registry, error)
func (r *Registry) Client(ref integrations.Ref, p Policy) *Client
type Policy struct { Limits integrations.Limits; Egress integrations.EgressPolicy; Scope string; Redact []string; Private func(host string) bool }
func (c *Client) Client() *http.Client
func (c *Client) Do(req *http.Request) (*http.Response, error)
func (c *Client) GetJSON(ctx context.Context, url string, out any) error
func (c *Client) Gate() integrations.Gate
func (c *Client) SetLimits(l integrations.Limits)
func (r *Registry) GateFor(ref integrations.Ref, p Policy) integrations.Gate
func (r *Registry) ReleaseHold(ctx context.Context, scope string) error
func (r *Registry) Holds() iter.Seq[integrations.Hold]
func WithInteractive(ctx context.Context) context.Context
func WithBypassHold(ctx context.Context) context.Context
func Classify(ctx context.Context, resp *http.Response, body []byte, err error) integrations.Class

package legacy
func ProviderHTTPCoverID(scope, counter string) string // "<counter>|<scope>"
```

Native keys (M2.4): `c/providerhttp/holds/<scope>` (`codec.JSON[integrations.Hold](1)`) and
`c/providerhttp/quota/<scope>/<counter>/<day>` (`codec.JSON[quotaCount](1)`). Config keys and defaults: M2.7.
Metrics: M2.8 (new with core, under ABO's namespace; no HEAD series renamed).

## Tests to write first (05-metadata M2.9 names)

`TestBucketSharedAcrossClientsOfScope`, `TestSetLimitsMutatesInPlace`, `TestHoldPersistsAcrossRegistryRestart`,
`TestHoldClampedToMaxOnLoad`, `TestHold_EarlySuccessKeepsHold` (synctest), `TestBreaker_LeaseExpiryReopens`
(synctest), `TestBreaker_IgnoresCanceled`, `TestQuotaTiers`, `TestQuotaDayRollsInZone`,
`TestQuotaStoreReadFailureFailsClosed`, `TestClassify_Google403RateLimitIsRateLimited`,
`TestClassify_DailyQuotaBodyBeatsStatus`, `TestRetryHonoursRetryAfterAndClamp`, `TestNoRetryWithoutGetBody`,
`TestEgressPublicOnlyRefusesPrivate`, `TestEgress_PrivateRefusedUnlessLocal`, `TestEgress_LANAllowListAdmits`,
`TestEgress_LinkLocalAlwaysRefused`, `TestEgress_RebindingCheckedAtDial`, `TestEgressSameHostOrPublic`,
`TestLocalInstance_OtherPrivateHostRefused`, `TestLocalInstance_RedirectToOtherPrivateHostRefused`,
`TestRedactsApikeyInURLError`, `TestNoGlobals`, `TestNativeGateStore_RoundTrip`,
`TestGate_LegacyCoveredScopesNeverNative`, `TestGate_LegacyWithoutCoveringCoversAll`, `TestGateLegacyMirrorsMatch`,
`TestGate_DisabledRefusesBeforeDial`, `TestQuota_ConcurrentReservationsNoLostCount` (`-race`), and
`providerhttptest.GateStoreSuite` against `NativeGateStore`.

ABO: `providerhttptest.GateStoreSuite` and `legacytest.ProviderHTTPGateStoreSuite` against books' gate store with
HEAD fixtures (a hold and count it writes are read by HEAD's loader; one written by HEAD is honoured), under
`TestLegacyStoreSuites`; `TestLegacyBacked_ConfiguredHostKeepsHEADReach` (05-metadata M1.9: a legacy-backed instance
whose stored URL is a private address is dialled; a redirect to another private host and a data-derived private URL
are refused); un-skip `TestPut_SecretChangeReleasesAuthHold` from PC-16-01.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                  # exit 0
(cd core && go test ./integrations/... -race -count=1)                         # all "ok"
go test ./internal/metadata/... ./internal/metafetch/... ./internal/catalog/... ./internal/covers/... ./internal/books/... -race -count=1   # all "ok"
(cd core && go test ./archtest/... -run 'TestNoGlobals' -count=1 -v) | grep -E '^(--- FAIL|ok)'   # "ok" lines only (03 R5; the scan covers core/integrations/providerhttp)
git grep -n 'limiters\s*= map\|overrides\s*= map\|registry = map\[string\]\*DailyBudget' -- '*.go' | grep -v _test   # 0 lines
git grep -n 'mediacore/legacy"' -- core/integrations/providerhttp | grep -v _test.go   # 0 lines
make ci                                                                        # exit 0
make -C core ci                                                                # exit 0
```

`TestLegacyFamiliesWrittenOnlyByLegacyStores` stays green with `internal/metadata/dailyquota` in its allow-list
(09 S7 Done when).

## Out of scope

AI, download and notify adapters (PC-16-03, PC-16-04); moving the metadata scopes into `c/providerhttp/` (the
integrations cutover, M1.10); the `/api/v1/metadata/providers/throttles` alias and the native health/hold routes
(PC-16-06); a whole-call deadline (M2.11 item 2).

## Rollback

Binary swap. Metadata holds and the day's count stay in HEAD's rows, so a rollback keeps them and holds an older
build sets are honoured on roll-forward; native-only scopes in `c/providerhttp/` are invisible to S6 (M2.10).

## Repo rules

- Worktree per task, never the primary checkout or `main`; remove it after merge.
- Versioned headers on every new or changed file; headerless `changelog.d/` fragment, one per PR.
- Conventional commits, each with `Stage-Release: S7 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- CLAUDE.md concurrency: one bucket, breaker and quota counter per `Scope`; no goroutine per request.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row omits `internal/metadata/` (`throttle.go`, `throttle_registry.go`, `circuitbreaker.go`, the callers of
  the moved files; 04 §8.1 row S7 lists `internal/metadata` as an origin and M2.10 maps all three files),
  `internal/server/` (an importer of `internal/metadata/providerhttp` and `dailyquota` at `748fd787a`; it also
  appears in PC-16-03/04/06, so this PR must run before PC-16-03, which the chain already enforces),
  `internal/books/module.go` (`legacy.Stores.Gates`; also edited by PC-16-01 and PC-16-06 in the same wave, ordered
  by the chain) and `core/legacy/` (`ProviderHTTPCoverID`, `legacytest.ProviderHTTPGateStoreSuite`).
- 05-metadata M2.10 says `safehttp` "stays for non-integration callers (`internal/covers`) until 04 moves it",
  while 04's row says "merge" into providerhttp at S7. This brief follows 04 and the row (move in, rewire
  `internal/covers`); if review prefers M2.10's reading, keep `internal/security/safehttp` for `internal/covers`
  only and say so in the PR description.
