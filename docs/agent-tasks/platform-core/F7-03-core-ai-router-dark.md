<!-- file: docs/agent-tasks/platform-core/F7-03-core-ai-router-dark.md -->
<!-- version: 1.1.3 -->
<!-- guid: c070f730-aa7a-4a14-ab1e-da2d1d3bf68d -->
<!-- last-edited: 2026-11-05 -->

# F7-03 — Core AI, dark: the router (aidispatch's algorithm, no globals), ai.JobStore native over c/ai/, ai.Importer, aitest, the batch apply kinds, the <namespace>_ai_* instruments, and the integrations/ai/openai adapter

| Field | Value |
|---|---|
| ID | F7-03 |
| Release | R2, flip label F7 (dark PR) |
| Tier | **T4** (Opus / xhigh–max): the AI router and durable batch jobs, with secret origin rules; a race between poll, apply and cancel applies a batch twice or never |
| Wave | W33 (beside F5-14, F8-02; file-disjoint) |
| Depends on | F7-01 (integrations registry), F2-11 (F2's minimal `core/integrations/ai` package with `ai.InterimPoll` and the `core.ai.batch-poll` registration), F7-06 (both rows edit `core/go.mod`) |
| Spec | 05-ai §1 (scope; deferred-build members), §3 (Go API), §4 (lifecycle: catalog, router, batch jobs, the native store), §5 (errors), §6 (concurrency), §7 (keys), §8 (metrics), §9 (tests), §10 (migration: the router algorithm, the capability-ID map is books'); 05-metrics §13 item 3 (AI rows); 04 §8.1 F7, §8.5 F7 (the `dispatch.go` cuts); 05-tasks §5T.8.2 (`core.ai.batch-poll`) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's `internal/aidispatch`,
`internal/ai` and `internal/ai/aijobs` keep serving; no AI instrument is registered in ABO's gather; HEAD's
`ai.*`/`ai_dispatch.*` families stay until F7-09 renames them with the dashboards. Core's router runs on `hello`,
`coretest` and `coretest.FakeAI`.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f7-03-ai" -b agent/platform-core-f7-03-ai origin/main
cd "$REPO/.worktrees/pc-f7-03-ai"
git rebase origin/main
```

## Goal

Grow F2-11's minimal `core/integrations/ai` into 05-ai §3's package: the capability catalog (`CapabilitySpec`,
`Capability`, `Registrar.AICapability`), the request/response types for text, embeddings and transcription, the
backend interfaces (`Backend`, `TextGenerator`, `Embedder`, `Transcriber`, `BatchBackend`), `Adapter`, the per-`App`
`Router` with HEAD's `aidispatch` algorithm and its globals removed (failover on `integrations.Class`, per-endpoint
slots, cooldowns, locality floor, pinned embedding model), `GenerateJSON[T]` and `Call[T]`, batch jobs
(`SubmitBatch`, `BatchJob`, `CancelBatch`, `BatchApplyTask`, `JobStore` with `NativeJobStore(ks)` over `c/ai/`,
`Importer` with `NativeImporter(ks)`), the native body of `core.ai.batch-poll` replacing `ai.InterimPoll` (kept until
F7-09 deletes it), the `<namespace>_ai_*` instruments of §8, the public test kit `integrations/ai/aitest`
(`JobStoreSuite`, `Conformance`), `coretest.FakeAI`, and the `core.openai_compat` adapter in
`core/integrations/ai/openai/` over `providerhttp`.

Deferred-build (05-ai §1): `ai.Router.Usage`, `ai.UsageQuery`, `ai.MonthlyTokenCap`, `ai.Pricing`, the usage route.

## Invariants (each is a 05-ai §9 test)

- **I1. No globals.** Two Routers share nothing (`TestRouter_TwoAppsIsolated`); every ported router test runs
  `t.Parallel()`.
- **I2. Default deny.** An endpoint with no ticks serves nothing; wildcards are refused on `Put`
  (`TestRouter_DefaultDenyEmptyTicks`, `TestRouter_WildcardRejectedOnPut`).
- **I3. Failover by class only.** One row per `integrations.Class` (`TestRouter_FailoverTable`); embeddings fail over
  only to the same model and require a pinned model (`TestRouter_EmbedFailoverSameModelOnly`,
  `TestRouter_EmbedRequiresPinnedModel`).
- **I4. Batch durability.** The job row commits before the remote submit (`TestBatch_JobRowBeforeRemote`); the poll
  never calls the handler, it stages the apply run in the `Applying` batch (`TestBatch_PollNeverCallsHandler`); poll vs
  cancel, apply vs cancel and same-state write vs cancel each have exactly one winner
  (`TestAIJobPollVsCancelOneWins`, `TestAIJobApplyVsCancelOneWins`, `TestAIJobSameStateWriteVsCancelOneWins`, with
  `-race`); no blind write (`TestNativeJobStore_BlindWriteRefused`).
- **I5.** `core.integrations.disabled` refuses every endpoint, stores no job, and the poll still advances its clock
  (`TestRouter_IntegrationsDisabledRefusesEveryEndpoint`).
- **I6.** Only the adapter package `integrations/ai/openai` imports `openai-go` (archtest `NoAISDKOutsideAdapters`,
  05-ai §9: "only `integrations/ai/openai` may import `openai-go`"); a private host needs `Local`
  (`TestOpenAICompat_PrivateHostNeedsLocal`).
- **I7. Dark.** ABO's gather and AI behaviour are unchanged in this PR.

## Files

| Action | Path |
|---|---|
| modify / grow | `core/integrations/ai/` (catalog, router, batch, job store, importer, metrics, the `core.ai.batch-poll` native body) |
| create | `core/integrations/ai/aitest/` (`JobStoreSuite`, `Conformance`) |
| create | `core/integrations/ai/openai/` (adapter `core.openai_compat` over `providerhttp`, `openai-go/v3`) |
| create | `core/coretest/ai.go` (`coretest.FakeAI`, 05-ai §9 "Fake"; this row owns this one file of `core/coretest/`; F7-02 owns `provider.go`, F2-16 `tasks.go` and `harness.go`, both in W30) |
| modify | `core/go.mod`, `core/go.sum`, `go.sum` (`github.com/openai/openai-go/v3` into core), `core/archtest/thirdparty_allow.txt` (its allowlist line) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# F2-11's minimal package. Expected: InterimPoll and the batch-poll registration, nothing else of §3
grep -rn 'InterimPoll\|core.ai.batch-poll' core/integrations/ai/*.go
grep -rn 'type Router struct' core/integrations/ai/ 2>/dev/null | wc -l         # expected: 0
# HEAD's router to port. [evidence: internal/aidispatch with Dispatcher, Call, Classify, Health, Slots]
ls internal/aidispatch/ ; grep -n 'defaultHealth' internal/aidispatch/health.go
# The integrations registry (F7-01). Expected: Registry, Lease, Class present
grep -n 'type Registry struct\|func Lease\|type Class uint8' core/integrations/*.go
# providerhttp client (adapter transport). Expected: a Client type (R1 or F2-11)
grep -rn 'type Client struct' core/integrations/providerhttp/ | head -2
# openai-go is a root dependency. Expected: 1 line in go.mod; check core/go.mod after you add it
grep -n 'openai/openai-go' go.mod
```

STOP if `ai.Router` already exists, if F7-01's registry is missing, or if `providerhttp.Client` is absent.

## Steps

1. Read 05-ai §3–§10 in full; copy every type and signature of §3 exactly.
2. Port HEAD's router tests (`internal/aidispatch/*_test.go`) onto an instance-scoped router first; write the rest of
   §9's tests.
3. Implement the catalog and router: the algorithm of `internal/aidispatch` (priority spill, slots, failover table,
   cooldown steps with cap, quota marker detection, deadline failover once, slot release on panic, kind caps,
   locality), classification on `integrations.Class`, no package globals (HEAD's `defaultHealth` becomes the router's
   per-instance health reporting components).
4. Structured output: `GenerateJSON[T]` with schema from the type, JSON-mode fallback, one repair then
   `QualityError`, unknown fields rejected.
5. Batch: `NativeJobStore` over `c/ai/` with `Expect` on every job-family write; `Importer.ImportJob` in any state
   with absent-expectation; `BatchApplyTask` registering one apply kind per batch capability; orphan reconcile by tag;
   the native body of `core.ai.batch-poll` (F2-11's registration extended; the interim body stays wired in ABO until
   F7-09).
6. Metrics: the §8 table under the App's namespace; nothing registered in ABO's gather in this PR. This adds the core `ai` metrics scope: add its `testdata/series.golden` under `core/integrations/ai/`, asserted by
   `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
7. Adapter `core.openai_compat` in `core/integrations/ai/openai/` through `providerhttp`, egress policy and `Local`.
8. `aitest.JobStoreSuite`, `aitest.Conformance` (recorded fixtures; live servers behind `MEDIACORE_LIVE_AI=1`),
   `coretest.FakeAI`.
9. `core/go.mod` gains `github.com/openai/openai-go/v3` (at the root's pinned version) and
   `core/archtest/thirdparty_allow.txt` its allowlist line; both files, `core/go.sum` and `go.sum` are in this row's
   files. F7-06 is a dependency, so it has merged: rebase, then make only the `require` addition and `go mod tidy` in
   `core/`. Headers, fragment, acceptance.

## Code contract (05-ai §3, verbatim excerpts; copy the rest from the spec)

```go
package ai // github.com/falkcorp/mediacore/integrations/ai
func (r *Router) Generate(ctx context.Context, c Capability, req TextRequest, opts ...CallOption) (TextResponse, error)
func (r *Router) Embed(ctx context.Context, c Capability, req EmbedRequest, opts ...CallOption) (EmbedResponse, error)
func (r *Router) Transcribe(ctx context.Context, c Capability, req TranscribeRequest, opts ...CallOption) (TranscribeResponse, error)
func GenerateJSON[T any](ctx context.Context, r *Router, c Capability, req TextRequest, opts ...CallOption) (T, TextResponse, error)
func (r *Router) Candidates(c Capability, opts ...CallOption) ([]EndpointStatus, []Refusal, error)
func (r *Router) Capacity(c Capability, opts ...CallOption) int
func BatchApplyTask(c Capability, h BatchHandler, effects tasks.Effects) tasks.Definition
func (r *Router) SubmitBatch(ctx context.Context, c Capability, items []BatchItem, payload []byte, opts ...CallOption) (BatchJobID, error)
func (r *Router) BatchJob(ctx context.Context, id BatchJobID) (BatchJob, error)
func (r *Router) CancelBatch(ctx context.Context, id BatchJobID) error
func NativeJobStore(ks storage.Keyspace) JobStore
func NativeImporter(ks storage.Keyspace) Importer

// in package module
func (r *Registrar) AICapability(spec ai.CapabilitySpec) ai.Capability // zero handle on error
```

## Tests (write first; spec names copied exactly, 05-ai §9)

Metrics: `metricstest.AssertSeries` over the `ai` scope's `testdata/series.golden` (05-metrics §12).

Router: `TestRouter_DefaultDenyEmptyTicks`, `TestRouter_WildcardRejectedOnPut`, `TestRouter_PrioritySpillToFreeSlot`,
`TestRouter_FailoverTable`, `TestRouter_EmbedFailoverSameModelOnly`, `TestRouter_EmbedRequiresPinnedModel`,
`TestRouter_DeadlineFailsOverOnce`, `TestRouter_CooldownStepsAndCap`, `TestRouter_QuotaMarkerTypeOrCode`,
`TestRouter_SlotReleasedOnPanic`, `TestRouter_KindCapProductionSetter`, `TestRouter_LocalOnlyFloor`,
`TestRouter_TwoAppsIsolated`. Catalog: `TestCatalog_ForeignPrefixRejected`, `TestCatalog_LookupCatalogIDsOnly`,
`TestCatalog_UnknownTickKept`. Structured: `TestGenerateJSON_SchemaFromType`, `TestGenerateJSON_JSONModeFallback`,
`TestGenerateJSON_RepairThenQualityError`, `TestGenerateJSON_UnknownFieldRejected`. Batch:
`TestBatch_JobRowBeforeRemote`, `TestBatch_OrphanReconcileByTag`, `TestBatch_HandlerReplayIdempotent`,
`TestBatch_UsageMetricsRecorded`, `TestBatch_PollNeverCallsHandler`, `TestBatch_ApplyRunUsesModuleEffects`,
`TestBatch_SubmitWithoutApplyTaskRefused`, `TestNativeJobStore_RoundTrip`, `TestImportJob_AnyStateExpectsAbsent`,
`TestAIJobPollVsCancelOneWins`, `TestAIJobApplyVsCancelOneWins`, `TestAIJobSameStateWriteVsCancelOneWins`,
`TestNativeJobStore_BlindWriteRefused`, `TestRouter_IntegrationsDisabledRefusesEveryEndpoint`;
`aitest.JobStoreSuite` against `NativeJobStore`. Egress: `TestOpenAICompat_PrivateHostNeedsLocal`. Adapter:
`aitest.Conformance` against `core.openai_compat`. archtest `NoAISDKOutsideAdapters`.
Not here: `TestNoHEADAIFamilies` (F7-09, when the rename lands) and the migration tests (F7-08).

## Acceptance (commands and expected output)

```bash
# Core AI with the race detector. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./integrations/ai/...)

# No package-level mutable state; adapter boundary. Expected: ok
(cd core && go test -count=1 -run 'NoGlobals|NoAISDKOutsideAdapters' ./archtest/... ./integrations/ai/...)

# Dark: ABO's AI unchanged, no core AI family in ABO's gather. Expected: ok
go test -count=1 ./internal/aidispatch/... ./internal/ai/...
go test -count=1 -run TestABOMetricsGatherNoDuplicate ./...

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Books' capability registrations, the capability-ID map, `OpenAIParser`'s feature methods (books code; F7-09 wires).
- Renaming HEAD's AI metric families and their dashboards (F7-09).
- M6's AI-job and endpoint migration (F7-08).
- The usage ledger, monthly cap and pricing (deferred-build).

## Rollback

Revert the PR; nothing in ABO uses it and no stored data is written.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file; no `go work init` (CLAUDE.md).
- Conventional commit, for example `feat(ai): core AI router, batch jobs and openai_compat adapter (dark)`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.
- Concurrency (CLAUDE.md): the router's per-endpoint slots are the bound; batch result application iterates
  library-scale results through a bounded pool inside the apply task, never unbounded goroutines.
