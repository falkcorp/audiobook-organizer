<!-- file: docs/agent-tasks/platform-core/PC-16-03-integrations-ai-jobstore-books-recorder.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8962d70e-8a49-4813-a14c-3edfa244164b -->
<!-- last-edited: 2026-11-04 -->

# PC-16-03 — `integrations/ai` with `ai.JobStore` and `openai`; books' AI recorder keeps every `ai_*` family

| Field | Value |
|---|---|
| ID | PC-16-03 |
| Title | integrations/ai with ai.JobStore and openai; books' AI recorder keeps every ai_* family |
| Tier | **T3 — Opus / high.** Metric ownership and job persistence both change code path: a second emitter of one HEAD family fails every scrape, and a job write outside HEAD's rows strands open batches on rollback. |
| Wave | W16 (stage S7, release `S7`) |
| Depends on | PC-16-02 |
| Trailer | `Stage-Release: S7 1` |
| Digest | `docs/plans/platform-core/digests/S6-S7.md` §3 (row `integrations/ai`), §4 (05-ai), §5 rules 10, 12 |
| Owning spec | 05-ai §1–§10 (the Go API of §3, §8 metrics, §9 tests, §10 migration); 09 S7 "Moves"; 09 §9.2.7; 05-metrics §6, §13 |

**Gate.** Merges only after the `S6` soak row exists (`check_soak.py` check (b)).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-16-03" -b feat/pc-16-03-integrations-ai origin/main
cd "$REPO/../abo-pc-16-03"
git worktree list
make web-deps
```

## Goal

Move the generic AI router of `internal/aidispatch` (globals removed, classification on `integrations.Class`),
batch jobs (`internal/ai/aijobs`, the global callback map removed) and the OpenAI-compatible client into
`core/integrations/ai` and its adapter `core.openai_compat`. Books keeps every feature method, its 13 capabilities
(registered through `Registrar.AICapability` with `FormerIDs` = the HEAD IDs), its legacy `JobStore` over HEAD's
`aijob:`/`aijob_batch:`/`aijob_payload:` rows, `internal/ai/resultjournal`, and **books' AI recorder**: the one
owner of every HEAD `ai.*` and `ai_dispatch.*` family, fed by `ai.WithObserver`. Core records only
`<namespace>_ai_*`. The series golden diff adds only `<namespace>_ai_*` lines.

## Files

Create:
- `core/integrations/ai/` (router, catalog, structured output, batch jobs, `JobStore`, `NativeJobStore`, observer).
- `core/integrations/ai/openai/` (the `core.openai_compat` adapter; see Planner notes on the directory name).
- `core/integrations/ai/aitest/` (`JobStoreSuite`, `Conformance`).

Modify:
- `internal/ai/` — feature methods call `ai.GenerateJSON[T]`/`Router.Embed`; `ai_metrics.go` and `telemetry.go`
  are **divided**: the recorder half stays here (books' AI recorder), the client-side tracing goes to the adapter;
  `internal/aidispatch/metrics.go`'s five instruments move **here**, not into core.
- `internal/ai/aijobs/` — becomes books' batch apply kinds (`ai.BatchApplyTask`) and books' legacy `JobStore`.
- `internal/aidispatch/` — algorithm moved to core; what is left is deleted or reduced to books' capability specs.
- `internal/dedup/`, `internal/scanner/`, `internal/transcribe/`, `internal/server/handlers/aibackends/` — callers
  rewired onto the router and `module.Deps.AI`.

## Re-verify before editing

Counts at `748fd787a` (unchanged at this plan's HEAD). Zero hits or a moved count: STOP.

```bash
git grep -c 'newCapability(' -- internal/aidispatch/capabilities.go          # 14 = 13 registrations + the func definition (:97)
git grep -n '^var default\(Health\|Slots\|Attribution\) = ' -- internal/aidispatch | grep -v _test   # 3 (health.go:55, inflight.go:60, attribution.go:49)
git grep -n 'registry   = map\[string\]CompletionCallback' -- internal/ai/aijobs/aijobs.go    # 1 (global callback map)
git grep -n 'Int64Counter("ai_dispatch\.\|Int64UpDownCounter("ai_dispatch\.\|Float64Histogram("ai_dispatch\.' -- internal/aidispatch/metrics.go   # 5 (:54-66)
git grep -n 'm.Int64Counter("ai\.\|m.Float64Histogram("ai\.' -- internal/ai/ai_metrics.go     # 5 (:387-403)
git grep -n 'func aiInstrumentsFor\|func RecordParseResult\|func classifyCall' -- internal/ai/ai_metrics.go   # 3
git grep -n 'func attemptMiddleware\|func classifyAttempt\|func recordReplyFailure' -- internal/ai/telemetry.go   # 3
git grep -n 'func TestGuard_NoAIBackendCallsOutsideTheDispatcher' -- internal/aidispatch/guard_test.go      # 1 (:104)
git grep -n 'func poolSecret' -- internal/ai/pool_routing.go                                     # 1
ls internal/ai/resultjournal/journal.go                                                          # exists; it STAYS in ABO
git grep -l 'aijob:' -- '*.go' | grep -v _test | head -3     # books' store file (748fd787a: internal/database/pebble_store_aijobs.go; at your base internal/books/store/)

M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/aidispatch\"" -- '*.go' | grep -v _test.go | wc -l   # 7 (internal/ai, internal/config, internal/server/handlers/aibackends, internal/transcribe)
git grep -l "\"$M/internal/ai/aijobs\"" -- '*.go' | grep -v _test.go | wc -l    # 4 (internal/ai, internal/dedup, internal/server)
```

Also locate the seeded series contract test and the gather test at your base (they are the oracles):
`git grep -ln 'func TestHistogramBucketsPinned'` and `git grep -ln 'func TestABOMetricsGatherNoDuplicateOrMissing'`
(PC-12-08 put the latter in `internal/books/metrics_gather_test.go`). Each must give exactly 1 file.

## Steps

1. Read 05-ai §3 (whole Go API), §4 (lifecycle, step 2 tick vocabulary, step 5 job state table), §5, §6, §8, §9,
   §10; 05-metrics §6 and §13 item 3; 09 §9.2.7.
2. Write the tests below first, including `TestUnprefixedAIFamiliesRecordedByBooks` against the current code
   (it must pass on HEAD's recorder before you move anything; it is the oracle for the move).
3. Build the router in `core/integrations/ai` from `internal/aidispatch` (default-deny ticks, priority spill,
   per-endpoint concurrency, failover table, embed failover to the same model only, pinned model, per-attempt
   timeout after the slot, panic-safe slot release). Health, slots and attribution are per-`Router` fields; no
   package global. Capabilities are registered through `Registrar.AICapability`; the catalog resolves `FormerIDs`.
4. Delete every HEAD instrument call from the files you move (`dispatch.go`'s `requests.Add` … `failover.Add`);
   core records only the `<namespace>_ai_*` table of 05-ai §8.
5. Books' AI recorder (in `internal/ai`): keep `aiInstrumentsFor`, `modelLabelSet`, `callMeta.normalized`,
   `classifyCall`, `RecordParseResult`, `attemptMiddleware`, `classifyAttempt`, `observe`'s rules and
   `recordReplyFailure`, plus the five `ai_dispatch.*` instruments created once on the App's `MeterProvider`
   through ABO's kept `internal/telemetry` helper, in HEAD's meter scopes, outside any `Scope`. It wraps every
   router call books makes with `ai.WithObserver(...)` and maps core values onto HEAD's label values exactly as
   05-ai §8's bullet list says (capability → HEAD ID; endpoint → `ai_endpoints` row ID; provider →
   `providerForBaseURL`; model → `callMeta.normalized`; dispatch outcome/class; request outcome and reason per HTTP
   attempt from `Observer.Transport`). Observer callbacks are synchronous and must not block.
6. HEAD's two histograms (`ai_dispatch.slot_wait`, `ai.request.duration`) keep HEAD's buckets
   (`internal/telemetry/views.go` at `748fd787a`) through 05-metrics' bucket option; `TestHistogramBucketsPinned`
   must stay green.
7. Batch jobs: `Router.SubmitBatch` and one `ai.BatchApplyTask` books kind per batch capability; the poll never
   calls a handler (it stages the apply run in the `Applying` batch). Books' legacy `JobStore` (HEAD's row formats
   of `pebble_store_aijobs.go`, now under `internal/books/store`) serves open and new jobs; every HEAD writer
   becomes a `Transition` with the commit-time check over the three `Serialized` families; nothing is written
   under `c/ai/`. Pass it as `legacy.Stores.AIJobs` (see Planner notes). Its batches commit through
   `storage.CommitLocked` as 05-ai §9's commit-path test states; books' `JobStore` names no lock.
8. The OpenAI-compatible adapter uses `env.HTTP` (providerhttp, PC-16-02); only the adapter package may import
   `openai-go` (archtest `NoAISDKOutsideAdapters`, successor of `TestGuard_NoAIBackendCallsOutsideTheDispatcher`).
9. `ai_endpoints` rows are served as legacy-backed `ai/<id>` instances through PC-16-01's books store; stored
   `capabilities` ticks are not rewritten (they resolve through `FormerIDs`). `auth_ref` is resolved at call time
   as HEAD's `poolSecret` does (`openai_api_key` → `integrations.ai.openai.api_key`, any other name → none).
10. `core.integrations.disabled`: no request reaches an endpoint, `SubmitBatch` stores no job, the poll changes
    nothing, the scheduled poll run is still enqueued and advances its clock.
11. Rewire `internal/dedup`, `internal/scanner`, `internal/transcribe` (the `books.whisper_server` adapter; the
    pool-wide cap becomes `core.ai.kind_limits.transcribe`, with a production setter), and
    `internal/server/handlers/aibackends`.
12. Update the seeded series golden: **additions only**, every added line `<namespace>_ai_*`
    (`audiobook_organizer_ai_*` for ABO). No HEAD line changes. Update the gather test's expected additions.

## Code contract (05-ai §3; copy exactly, full declarations in the file)

```go
func WithObserver(o Observer) CallOption   // Observer{SlotWait, SlotHeld, Attempt, Transport, Failover, NoCapable}
func (r *Router) Generate(ctx context.Context, c Capability, req TextRequest, opts ...CallOption) (TextResponse, error)
func (r *Router) Embed(ctx context.Context, c Capability, req EmbedRequest, opts ...CallOption) (EmbedResponse, error)
func (r *Router) Transcribe(ctx context.Context, c Capability, req TranscribeRequest, opts ...CallOption) (TranscribeResponse, error)
func GenerateJSON[T any](ctx context.Context, r *Router, c Capability, req TextRequest, opts ...CallOption) (T, TextResponse, error)
func BatchApplyTask(c Capability, h BatchHandler, effects tasks.Effects) tasks.Definition
func (r *Router) SubmitBatch(ctx context.Context, c Capability, items []BatchItem, payload []byte, opts ...CallOption) (BatchJobID, error)
func (r *Router) CancelBatch(ctx context.Context, id BatchJobID) error
type JobStore interface {
	Get(ctx context.Context, id BatchJobID) (StoredJob, error)
	Open(ctx context.Context) iter.Seq2[StoredJob, error]
	Create(ctx context.Context, b *storage.Batch, j StoredJob) error
	Transition(ctx context.Context, b *storage.Batch, id BatchJobID, from JobState, next StoredJob) error // ErrJobConflict
}
func NativeJobStore(ks storage.Keyspace) JobStore
func (r *Registrar) AICapability(spec ai.CapabilitySpec) ai.Capability // package module
```

Books capability IDs (05-ai §10, verbatim): `books.filename_parse`, `books.audiobook_parse`,
`books.cover_art_vision`, `books.author_review`, `books.author_discovery`, `books.metadata_rerank`,
`books.dedup_review`, `books.author_dedup_batch`, `books.diagnostics`, `books.embed_text`,
`books.embed_text_batch`, `books.transcribe_batch`, `books.transcribe_intro_clip`; each with `FormerIDs` = its HEAD
ID. `books.diagnostics` is **not** made `LocalOnly` (open item 2).

## Tests to write first (05-ai §8, §9 names)

Router: `TestRouter_DefaultDenyEmptyTicks`, `TestRouter_WildcardRejectedOnPut`, `TestRouter_PrioritySpillToFreeSlot`,
`TestRouter_FailoverTable`, `TestRouter_EmbedFailoverSameModelOnly`, `TestRouter_EmbedRequiresPinnedModel`,
`TestRouter_DeadlineFailsOverOnce`, `TestRouter_CooldownStepsAndCap`, `TestRouter_QuotaMarkerTypeOrCode`,
`TestRouter_SlotReleasedOnPanic`, `TestRouter_KindCapProductionSetter`, `TestRouter_LocalOnlyFloor`,
`TestRouter_TwoAppsIsolated`, `TestRouter_IntegrationsDisabledRefusesEveryEndpoint`.
Catalog: `TestCatalog_ForeignPrefixRejected`, `TestCatalog_FormerIDsResolve`, `TestCatalog_UnknownTickKept`.
Structured: `TestGenerateJSON_SchemaFromType`, `TestGenerateJSON_JSONModeFallback`,
`TestGenerateJSON_RepairThenQualityError`, `TestGenerateJSON_UnknownFieldRejected`.
Batch: `TestBatch_JobRowBeforeRemote`, `TestBatch_OrphanReconcileByTag`, `TestBatch_HandlerReplayIdempotent`,
`TestBatch_UsageMetricsRecorded`, `TestBatch_PollNeverCallsHandler`, `TestBatch_ApplyRunUsesModuleEffects`,
`TestBatch_SubmitWithoutApplyTaskRefused`, `TestBatch_LegacyStoreServesOpenJobs`, `TestNativeJobStore_RoundTrip`,
`TestBatch_StaleTerminalNeverRestaged`, `TestBatch_StaleOpenJobContinuesFromLegacyStatus`,
`TestLegacyJobStore_HEADRowsOnly`, `TestBatch_RepeatedPollStagesOneQueuedApply`, `TestAIJobPollVsCancelOneWins`,
`TestAIJobApplyVsCancelOneWins`, `TestAIJobSameStateWriteVsCancelOneWins`, `TestNativeJobStore_BlindWriteRefused`,
`TestBatch_LegacyJobWritesCommitThroughCommitLocked`, and `aitest.JobStoreSuite` on both stores.
Observer: `TestObserver_TransportPerHTTPAttempt`, `TestObserver_AttemptTimeoutFlag`.
Egress: `TestOpenAICompat_PrivateHostNeedsLocal`. Adapter: `aitest.Conformance` with recorded fixtures (live mode
only behind `MEDIACORE_LIVE_AI=1`, never in CI). archtest `NoAISDKOutsideAdapters`.
Metrics (ABO): `TestUnprefixedAIFamiliesRecordedByBooks` (including the 429-then-200 call and the per-attempt
timeout case of 05-ai §8), `TestABOMetricsGatherNoDuplicateOrMissing` with books' AI recorder and one AI call
through a fake endpoint, `TestHistogramBucketsPinned`.

## Acceptance

```bash
go build ./... && (cd core && go build ./...)                                   # exit 0
(cd core && go test ./integrations/ai/... -race -count=1)                       # all "ok"
go test ./internal/ai/... ./internal/dedup/... ./internal/scanner/... ./internal/transcribe/... ./internal/server/handlers/aibackends/... ./internal/books/... -race -count=1   # all "ok"
go test ./... -run 'TestUnprefixedAIFamiliesRecordedByBooks|TestABOMetricsGatherNoDuplicateOrMissing|TestHistogramBucketsPinned' -count=1 -v 2>&1 | grep -E '^(--- FAIL|--- PASS)'   # PASS lines only
git grep -n '"ai_dispatch\.\|"ai\.requests"\|"ai\.tokens"' -- core | grep -v _test.go             # 0 lines: core creates no HEAD AI instrument
git grep -n 'openai-go' -- '*.go' | grep -v _test.go | grep -v '^core/integrations/ai/openai'    # 0 lines
git diff origin/main -- '*series*.golden' | grep '^[-+][^-+]' | grep -v '^+audiobook_organizer_ai_'   # 0 lines: the golden only gains <namespace>_ai_* lines
make ci                                                                         # exit 0
make -C core ci                                                                 # exit 0
```

## Out of scope

The deferred usage ledger, monthly cap and pricing (05-ai §1 "Deferred-build members"); making
`books.diagnostics` local-only; download, notify (PC-16-04); routes and aliases for `/api/v1/ai` (PC-16-06); any
change to stored `ai_endpoints` ticks; moving `internal/ai/resultjournal`.

## Rollback

Binary swap: job rows stay HEAD's `aijob:` families, so S6 reads and polls every open job; no `c/ai/` row is
written for ABO. Metric names and label values do not change, so dashboards survive a rollback.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on every new or changed file; one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S7 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- CLAUDE.md concurrency: keep HEAD's per-endpoint concurrency (0 → 1) and slot semantics; do not add parallelism
  in this PR.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- 05-ai names the adapter `core.openai_compat` and its archtest rule says "only `integrations/ai/openaicompat`
  may import `openai-go`", while the inventory row creates `core/integrations/ai/openai/` (and 09 S7 says
  "`integrations/ai` (+`openai`)"). This brief keeps the row's directory; the archtest allow-list must name the
  directory actually created. The spec should pick one directory name.
- The row omits `internal/books/module.go` (`legacy.Stores.AIJobs`, the 13 `AICapability` registrations, the
  batch apply kinds), `internal/config/` (an importer of `internal/aidispatch` at `748fd787a`) and `core/module/`
  (`Registrar.AICapability`, `Deps.AI`). `internal/books/module.go` is also edited by PC-16-01 and PC-16-06; the
  chain orders them.
