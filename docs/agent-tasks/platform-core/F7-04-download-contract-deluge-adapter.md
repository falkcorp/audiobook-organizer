<!-- file: docs/agent-tasks/platform-core/F7-04-download-contract-deluge-adapter.md -->
<!-- version: 1.1.2 -->
<!-- guid: 8ecb5fb3-8574-4b87-94e6-110557990756 -->
<!-- last-edited: 2026-11-05 -->

# F7-04 — Core download contract and the Deluge adapter core.deluge with Pool, dark

| Field | Value |
|---|---|
| ID | F7-04 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T2** (Sonnet / high): one adapter over an existing client and a contract the spec fixes; phase 1 only |
| Wave | W28 (beside F2-04, F3-06, F4-11, F7-10; file-disjoint) |
| Depends on | F7-01 (integrations registry: instances, `Env`, `Lease`) |
| Spec | 05-download §1 (phase-1 build scope; deferred-build members), §3 (Go API, phase-1 members only), §4.1 (instances and adapters), §4.2 (state normalization), §4.4 (path mapping), §4.6 (shutdown), §5–§8, §9 (phase-1 tests), §10 (migration, the `internal/deluge/client.go` row); 03 §3.3 (`integrations/download`); 04 §8.1 F7 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's `internal/deluge` keeps its
global client and every caller; nothing in ABO registers `core.deluge` until F7-09.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-04-download" -b agent/platform-core-f7-04-download origin/main
cd "$REPO/.worktrees/pc-f7-04-download"
git rebase origin/main
```

## Goal

1. `core/integrations/download` with **only the phase-1 members** of 05-download §1's table: `Protocol`, `ItemID`,
   `ItemRef`, `State` and its constants, `Item`, `Client` (`Caps`, `Status`, `Items`), the optional `FileLister` and
   `Relocator`, the phase-1 `Caps` fields, `Adapter`, path mapping (§4.4), and `Pool` with `Instances`, `Lease`,
   `Items`, `Files`, `Relocate`.
2. `core/integrations/download/deluge`: the `core.deluge` adapter, reusing the RPC and auth code of HEAD's
   `internal/deluge/client.go` (generation-guarded re-login, `NormalizeTorrentID`), with **every method taking a
   `ctx`**, the `http.Client` from `env.HTTP` (the 5-minute timeout becomes per-operation limits), and its
   `Descriptor()` for `app.Options.Adapters`.
3. The conformance suite `coretest.DownloadConformance(t, factory, fixtures)` (phase-1 cases) and
   `coretest.FakeDownloadClient`.

Nothing of phase 2 is built: no `Adder`, `Remover`, `Categorizer`, `Pauser`, `SeedLimiter`, no grab types, no
`Pool.Grab`/`Remove`/`MarkImported`/`Refresh`/`Tracked`, no `Watch`, no monitor, no `core.download.poll` kind, no
`core.download.item.transitioned.v1` topic, no `core.download.*` keys (05-download §1 "Deferred-build members").

## Files

| Action | Path |
|---|---|
| create | `core/integrations/download/` (`download.go` types, `pool.go`, `paths.go`, tests) |
| create | `core/integrations/download/deluge/` (`client.go` ported, `adapter.go` with `Descriptor()`, tests with recorded fixtures) |
| create | `core/coretest/download.go` (`DownloadConformance`, `FakeDownloadClient`; 05-download §9 puts both in `coretest`; this row owns this one file of `core/coretest/` in W28) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# HEAD's client to port. Expected: these functions in internal/deluge/client.go
#  [evidence: New :136, callCtx :152, callAuthedCtx :198, NormalizeTorrentID :243, LoginContext :266,
#   ListTorrents :297, ListTorrentsByLabel :314, ListLabels :331, GetTorrent :348, MoveStorage :368, Connected :377,
#   GetTorrentDetailContext :399, ListTorrentDetails :429, ListTorrentDetailsByLabel :467, RemoveTorrent :483]
grep -n '^func ' internal/deluge/client.go
# RemoveTorrent has no non-test caller (phase 1 drops it). Expected: 0
grep -rn '\.RemoveTorrent(' --include='*.go' internal/ | grep -v _test | wc -l
# ListTorrentDetails's only non-test caller is ListTorrentDetailsByLabel inside client.go. Expected: 1
#  [evidence: internal/deluge/client.go:476]; keep it as an unexported helper of the label listing
grep -rn '\.ListTorrentDetails(' --include='*.go' internal/ | grep -v _test | wc -l
# F7-01's registry. Expected: Registry, Lease, Env, NewDescriptor
grep -n 'type Registry struct\|func Lease\|type Env struct\|func NewDescriptor' core/integrations/*.go
# Nothing of download exists yet. Expected: 0
ls core/integrations/download 2>/dev/null | wc -l
```

STOP if F7-01's registry is missing, if `core/integrations/download` exists, or if `RemoveTorrent` now has a
non-test caller or `ListTorrentDetails` one outside `client.go` (then 05-download's phase-1 scope no longer matches
HEAD; report it).

## Steps

1. Read 05-download §1, §3 (phase-1 members only), §4.1, §4.2, §4.4, §4.6, §5–§10.
2. Write the tests first (below), including recorded JSON-RPC fixtures from `internal/deluge/client_test.go`.
3. `download` package: the phase-1 types copied exactly from §3; `Pool` over `integrations.Registry` (`Lease` per
   instance; `Items` across all download instances with an `ItemFilter`; `Files`; `Relocate` grouping refs by
   instance); path mapping per §4.4 (remote→local, Windows remote paths, an unmapped relocate refused).
4. `deluge` adapter: port `client.go` with `ctx` on every call; map Deluge states to `State` per §4.2's golden table;
   implement `FileLister` and `Relocator`; **do not** port `RemoveTorrent` or an add method; egress through
   `env.HTTP`, a private host only with `Local` (`TestDeluge_PrivateHostNeedsLocal`).
5. `core.integrations.disabled`: no client call, no snapshot change (`TestPool_IntegrationsDisabledNoClientCall`).
6. Metrics: only the phase-1 (unmarked) families of 05-download §8 under the App's namespace; no phase-2 family.
   This adds the core `download` metrics scope: add its `testdata/series.golden` under `core/integrations/download/`, asserted by
   `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
7. `coretest.DownloadConformance` (phase-1 cases: items/state golden table, status, files, relocate, auth failure and
   re-login, unknown state) and `coretest.FakeDownloadClient` (`Advance(id, State)`, capability toggles, faults).
8. Headers, fragment, acceptance.

## Code contract (05-download §3, verbatim excerpts; copy every phase-1 member from the spec)

```go
package download // github.com/falkcorp/mediacore/integrations/download
type ItemID string
type ItemRef struct {
	Instance integrations.Ref
	ID       ItemID
}
type State uint8
const (
	StateUnknown State = iota
	StateQueued
	StatePaused
	StateDownloading
	StateCompleted
	StateFailed
	StateWarning
)
```

## Tests (write first; spec names copied exactly, 05-download §9 phase 1)

`TestPool_ItemsFillsLocalPath`, `TestPool_RelocateGroupsByInstance`, `TestPaths_UnmappedRelocateRefused`,
`TestPaths_WindowsRemote`, `TestDeluge_PrivateHostNeedsLocal`, `TestPool_IntegrationsDisabledNoClientCall`;
`metricstest.AssertSeries` over the `download` scope's `testdata/series.golden` (05-metrics §12);
`coretest.DownloadConformance` run against `core.deluge` with recorded fixtures (live mode behind
`MEDIACORE_LIVE_DOWNLOAD=1`). Not here: `TestDelugeMigration_InstanceAndPassword` (books' M6, F7-08) and every phase-2
test.

## Acceptance (commands and expected output)

```bash
# Package tests. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./integrations/download/... ./coretest/...)

# No phase-2 member built. Expected: 0
grep -rn -E 'type (Adder|Remover|Categorizer|Pauser|SeedLimiter|GrabRequest) |func \(p \*Pool\) (Grab|Remove|MarkImported|Refresh|Tracked)\(' core/integrations/download/ | wc -l

# No ABO name in core. Expected: ok
(cd core && go test -count=1 -run 'TestCoreHoldsNoAppKeyNames|TestCoreImportsOnlyCore' ./archtest/...)

# Dark: ABO's deluge package unchanged. Expected: ok
go test -count=1 ./internal/deluge/...

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Phase 2 (grab, monitor, `Watch`, tracked downloads) and every adapter but Deluge (deferred-build, plan §(j)).
- ABO's discovery, import, mark-in-place and protected paths (stay books'; F7-09 re-points them at `Pool`).
- Deleting `internal/deluge`'s global client (F7-09).

## Rollback

Revert the PR; nothing in ABO uses it.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file (ported code gets a new file header in its new path).
- Conventional commit, for example `feat(download): core download contract and Deluge adapter (dark)`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- `Pool.Items` across instances and `Relocate` grouped by instance: if you fan out per instance, bound it (CLAUDE.md).

