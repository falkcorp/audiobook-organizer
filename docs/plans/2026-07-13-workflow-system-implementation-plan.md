<!-- file: docs/plans/2026-07-13-workflow-system-implementation-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb86ca88-9159-4e1e-b133-df7f67c28389 -->
<!-- last-edited: 2026-07-13 -->

# INIT-6 Pluggable Workflow System (WF-2..6) — Implementation Plan (for owner sign-off)

**Status:** Owner-review — this document does NOT authorize any code. It grounds the
`2026-07-10` design spec against HEAD (`17043fbf`), reports where the spec has drifted, and
lays out a phased build order the owner can approve or reject **per phase**.

**Spec under review:** `docs/specs/2026-07-10-workflow-system-design.md`
**Gate (verbatim, from the manifest):** STOP-FOR-HUMAN — spec-only, core-infra blast radius.
**Grounded at:** HEAD `17043fbf` (2026-07-12), verified 2026-07-13.

---

## Executive recommendation (read this first)

**Build WF-2 now (small, well-grounded). Defer WF-3 / WF-4 / WF-5.**

The single most important finding: **the spec's marquee justification for WF-3 has already
shipped without WF-3.** Between the spec's date (2026-07-10) and HEAD, INIT-1 **T5 and T6 both
landed** — `dedup.calibrate-composite` exists (`internal/plugins/dedup/calibrate_composite.go:123`)
and the scheduled refinement loop is live as a built-in-disabled scheduled op chain
(`internal/scheduler/tasks.go:207`, `runLabelRefinementChain` at `:853`). The spec presents
WF-3 (the `Workflow` object) as the "natural home" for that refinement loop (spec §Motivation,
§"How WF-3 subsumes INIT-1 T6"). That home was built and shipped as a plain code-defined op
chain, with a proven-inert test (`TestLabelRefinementDisabledByDefault`). This empirically
resolves the spec's own open questions **Q7** (T6 sequencing — done, shipped-first) and
substantially settles **Q9** (is WF-3 justified at all — its headline use case did not need it).

WF-3 does still add one *real* capability the current system lacks — **persisted, user-authored
workflows editable from the UI** (today's scheduled tasks are code-defined). The defer
recommendation is **not** "it's just refactoring." It is:

1. **No demonstrated demand** for user-authored/UI-composed workflows. Every in-scope need
   (the eight `scheduled_*` families, the T6 loop, `dedup_embeddings_enabled`) is already served
   by code-defined tasks + a uniform config struct + an existing settings UI.
2. **Highest and partly-irreversible blast radius** of the whole initiative: it seeds a new
   persisted `wf:def:*` Pebble keyspace (revert-PR does NOT unseed it — spec §Rollback admits
   this) and its M3 cutover moves scheduling authority for **prod-mutating** pipelines
   (author-split, series-prune, db-optimize, metadata-refresh, resolve-production-authors) onto
   a brand-new arbiter.
3. **The spec's premise is overstated.** "Scattered config booleans" is largely already
   addressed: config is a uniform nested `ScheduledTaskConfig{Enabled,Interval,OnStartup}`
   (`internal/config/config.go:394`), and `TaskScheduler` is already a task registry with an
   exported `RegisterTask` (plugin-extensible), a `TaskInfo` API view, and a settings UI
   (`web/src/components/settings/ScheduledTasksSection.tsx`). WF-3's delta over this is
   "persisted + user-editable," not "impose order on chaos."

WF-2 (capability probers) is genuinely additive, low-risk, and its three mandated probes
(ollama/openai/fpcalc) **already exist** as functions — WF-2 mostly *formalizes* runtime
availability gating into one declarative model. Honest caveat: the existing `ErrNotAvailable`
degrade paths already handle the in-scope cases, so WF-2 *formalizes* rather than *enables* —
modest user value, low risk.

**Three-sentence take.** INIT-6 is **not** ready to build as a whole; only WF-2 is a clean
build-now, and even it is a "nice formalization" rather than a capability gap. The biggest risk
is WF-3's M3 cutover: two scheduling authorities (legacy `TaskScheduler` + new workflow runner)
that could double-fire prod-*mutating* maintenance ops if the single-arbiter flag is imperfect,
plus an irreversible seed of the `wf:def:*` keyspace. Recommendation: **greenlight WF-2 as one
small PR; defer WF-3/WF-4/WF-5** until there is a concrete demand for user-authored workflows or
a real undeclared-cross-op incident (WF-4) — revisit at that point, not now.

---

## 1. What WF-2..6 actually are (plain terms + user-facing capability)

- **WF-2 — availability/capability declarations.** Lets an op declare "I need Ollama / OpenAI /
  fpcalc to be reachable," and the registry checks a stateless prober at dispatch time; if the
  tool is down the op is **skipped** (default) or **parked** to wait. *User-facing capability:*
  ops that need an external tool degrade or wait deterministically with an operator-visible
  reason, instead of each op discovering absence ad-hoc at its own callsite. **Note:** the
  underlying probes already exist (see §2), so this is a *consolidation of behavior that already
  works*, exposed as a declaration the UI could later reason about.

- **WF-3 — the persisted `Workflow` object.** A saved, enable/disable/schedule-able composition
  of registered ops (an ordered "recipe" of steps in the recommended Axis-2 option 2A). It would
  collapse the `scheduled_*` toggles into workflow rows and let users define their own pipelines.
  *User-facing capability:* users can create/enable/schedule multi-step maintenance pipelines
  from the UI without a code change. **This is the one genuinely new capability in the
  initiative — and the one with no demonstrated demand and the largest blast radius.**

- **WF-4 — registration-time dependency checks.** `OperationDef` gains a `Uses []string`
  (ops it invokes); at startup the registry validates targets exist and the graph is acyclic.
  *User-facing capability:* essentially none directly — it's a developer-safety guardrail. The
  spec itself concedes (spec §Axis 3 "Evidence gap") that no real undeclared-cross-op incident is
  on record and true static enforcement is "not achievable" in Go.

- **WF-5 — UI workflow builder.** A settings page to list/enable/schedule workflows and edit
  their step lists (Axis-4 option 4A). *User-facing capability:* the front door for WF-3.
  Explicitly LAST, gated on WF-3 having soaked in prod.

- **WF-6 — adopt `go-workflows`.** Explicitly **re-evaluate-only, no work planned.** Adopt only
  if a workflow ever needs durable mid-*step* crash recovery that can't be decomposed into
  idempotent ops. Out of scope for every milestone.

## 2. Current-state grounding (verified at HEAD `17043fbf`)

### What the spec got right (re-verified true)

| Claim | HEAD evidence |
|---|---|
| Op registry has `Requires []Requirement`, `RequirementKind` (`ReqOpCompleted`/`ReqFieldSet`), `WithRequires` | `internal/operations/registry/types.go:90,217-236,271` — verified |
| `OperationDef.Capabilities []Capability` (static coarse permissions) with `CapNetworkOpenAI` etc.; ~28 live declarations | `types.go:73` (field), `:182` (`CapNetworkOpenAI`); **29** live `Capabilities: []sdk.Capability` declarations (`grep -c` at HEAD) |
| `OperationDef.DependsOn []string` (mutual-exclusion, NOT invocation graph) | `types.go:83` — verified |
| `deps_scheduler.go` implements park/promote for `waiting_deps` ops | `internal/operations/registry/deps_scheduler.go:6-8,44-49,73-85` — exists, as described |
| Eight flat `scheduled_*_enabled` families in persistence.go | `grep -oE 'scheduled_[a-z_]+_enabled' internal/config/persistence.go` → exactly 8 — verified |
| `dedup_embeddings_enabled` config key + `reembed_embeddings.go` consumer | `internal/config/persistence.go:209,237,1343`, `internal/plugins/dedup/reembed_embeddings.go:32` — verified |
| No `internal/operations/workflow/` package yet; no `WorkflowSchedulerEnabled` flag | `ls` → absent; `grep` → 0 hits — verified (nothing built) |

### What the spec MISSED — the three drift findings the owner needs

**DRIFT-1 (biggest): INIT-1 T5 *and* T6 both shipped since the spec was written.** The spec
(spec §"How WF-3 subsumes INIT-1 T6") states `dedup.calibrate-composite` and
`dedup.calibration-drift-report` "**do not exist yet** — they are INIT-1 T6 deliverables." At HEAD:
- `dedup.calibrate-composite` **exists and is registered** —
  `internal/plugins/dedup/calibrate_composite.go:123` ("op dedup.calibrate-composite (INIT-1 T5)").
- The refinement loop **is live** as a built-in-disabled scheduled op chain, NOT waiting for
  WF-3: `internal/scheduler/tasks.go:207-214` registers task `label_refinement`;
  `runLabelRefinementChain` (`tasks.go:853`) enqueues `dedup.rebuild-gold-labels` then
  `dedup.calibrate-composite` in dry-run. It has proven-inert tests
  (`internal/scheduler/tasks_label_refinement_test.go`, `TestLabelRefinementDisabledByDefault`).
- `dedup.calibration-drift-report` still does NOT exist — the shipped T6 is a 2-step
  rebuild→calibrate chain (calibrate emits the report via its own log line), not the spec's
  illustrative 3-step seed. **So the spec's illustrative T6 seed block is doubly stale.**

**Consequence:** the spec's headline argument for WF-3 ("gives INIT-1 T6's scheduled refinement
loop its natural home," spec §Goals WF-3) is moot — the loop already has a home and runs in
prod-shippable form. This resolves spec **Q7** and is the strongest single piece of evidence for
spec **Q9** ("is WF-3 justified at all").

**DRIFT-2: the spec's own completeness safety-gate is already blind to a real, shipped family.**
The spec mandates a migration completeness check driven by
`grep -oE 'scheduled_[a-z_]+_enabled' internal/config/persistence.go` (spec §"scheduled_* toggles",
§Testing "runtime completeness") that "**fails if any `scheduled_*` family lacks a seeded
builtin**." But `label_refinement` (INIT-1 T6, a *ninth* scheduled family) uses the **nested**
`ScheduledTaskConfig` shape — `scheduled.label_refinement.enabled` (`internal/config/config.go:412,780`)
— and has **no flat `scheduled_*_enabled` alias in persistence.go** (grep for it returns empty).
The spec's completeness grep **structurally cannot see** this shipped, prod-relevant task. If WF-3
were built to the spec as written, its migration safety mechanism would silently omit
`label_refinement` from the seeded set. **This is a concrete defect in the migration's guardrail,
not just weakened motivation** — the "exhaustive 8 families" claim (spec §Current state) is already
wrong (it is 9, and the 9th is invisible to the spec's detection method).

**DRIFT-3: "scattered config booleans" is largely already addressed.** The spec's framing (spec
§Motivation: "'Is this pipeline enabled…?' lives in scattered config booleans") overstates the
disorder. At HEAD the scheduling config is a **uniform nested struct**: `ScheduledTasksConfig`
holds 9 `ScheduledTaskConfig{Enabled int; Interval int; OnStartup bool}` sub-structs
(`internal/config/config.go:393-419`). The flat `scheduled_*_enabled` keys are just
persistence/UI aliases over that struct. And `TaskScheduler` (`internal/scheduler/scheduler.go:57`
`TaskDefinition`; `:124` exported `RegisterTask`; `:274` `GetTask`; `TaskInfo` API view at `:71`)
is already a uniform, plugin-extensible task registry with a settings UI
(`web/src/components/settings/ScheduledTasksSection.tsx`). WF-3's real delta is
**persistence + user-authoring**, not consolidation — the consolidation already happened.

### Minor drift (note, don't block)

- Spec cites `internal/scheduler/tasks.go` as "23 `Scheduled.*` references"; HEAD has **26**
  (`grep -c 'Scheduled\.'`). Consistent with the file having grown (T6 added). Confirms tasks.go
  is the highest-collision surface, as the spec's ownership note warns.
- Spec cites `types.go:73,85-90,173-193` line anchors that are ~current (fields at 73/83/90).

### WF-2 is well-grounded: the three probes already exist

| Prober | Existing probe function |
|---|---|
| `ollama` | `internal/ai/embedding_client.go:165` `ProbeOllamaAvailable(ctx, baseURL, timeout) bool` |
| `fpcalc` | `internal/fingerprint/fpcalc.go:127` `Available() bool` + `ErrNotAvailable` (`:42`) |
| generic tool registry | `internal/tools/registry.go:112` `ToolRegistry.Available(name) bool`; dedup plugin already consumes it via `SetToolRegistry(r interface{ Available(string) bool })` (`internal/plugins/dedup/plugin.go:30`) |

WF-2's `CapabilityProber` interface (spec §Axis 1) is a thin adapter over functions that already
exist. This is why WF-2 is the low-risk build-now piece.

## 3. Blast radius & risk (why this was human-gated)

**Core-infra files touched** (spec §"Files likely modified", verified as real surfaces):
- `internal/operations/registry/types.go` — the `OperationDef`/`Requirement` shared type; every
  op depends on it. Single-owner.
- `internal/operations/registry/deps_scheduler.go` — the park/wake requirement evaluator (WF-2
  option 1B routes through it). Core scheduler code.
- `internal/scheduler/tasks.go` — 26 `Scheduled.*` references; where the M3 cutover arbiter lands
  **and** where INIT-1 T6's chain already lives. Highest-collision surface.
- New `internal/operations/workflow/` package + a new `wf:def:*` / `wf:run:*` Pebble keyspace.

**What could break / what is irreversible:**
- **Irreversible seed (WF-3/M2).** The spec itself admits (spec §Rollback) that once seeding runs,
  reverting the PR does NOT unseed `wf:def:*`. Recovery is roll-forward (redeploy) or manual
  keyspace deletion. This is the partly-irreversible part.
- **M3 cutover double-fire — the top concurrency risk.** After cutover, two scheduling
  authorities exist during the compat window: the legacy `TaskScheduler` (which holds a mutex and
  a `maintenanceOrder`, `internal/scheduler/scheduler.go:84-118`) and the new workflow runner.
  If the single-arbiter `WorkflowSchedulerEnabled` flag is imperfectly checked on *either* path,
  a prod-**mutating** maintenance op (author-split, series-prune, db-optimize, metadata-refresh,
  resolve-production-authors) could fire **twice** on the same cadence. These ops mutate the
  library; a mistranslated `_interval`→`Schedule` also fires them on the *wrong* cadence.
- **Data-integrity tie-ins (this repo's known failure classes):**
  - *Merge-serialization data-loss class* (recently fixed; MEMORY: Author/Series write-back wipe,
    `feedback_memdb_roundtrip_footgun`). The workflow runner must NOT introduce a path that runs a
    library-mutating op concurrently with another instance of itself — spec §Q6 proposes
    singleton-per-workflow, which MUST be honored, and the ops it fires must keep their existing
    concurrency guards (CLAUDE.md "Concurrency — Prefer Multi-Core Design"; per-op
    `ConcurrencyKey`, e.g. `calibrate_composite.go:134`).
  - *Enable-bit split-brain (spec §Q3, an M2 entry gate).* While the legacy config + workflow
    store both exist, an operator disabling a prod-mutating pipeline on one surface must land on
    the canonical bit, and a failed forward-write must surface (not silently swallow a disable
    while the op keeps firing). This is the spec's own fail-open concern and it is real.
- **Shim seeding fail-open (spec §"scheduled_* toggles").** The spec correctly rejects the
  store-empty guard in favor of per-builtin idempotent seeding, fail-closed on write error. That
  design is sound — but see DRIFT-2: the completeness detection it relies on is already blind to
  `label_refinement`.

**WF-2's blast radius is genuinely low:** additive metadata + one requirement kind routed through
existing park/wake machinery; an op without a declaration behaves exactly as today.

## 4. Phased build order

Recommendation is **defer WF-3/4/5**. The phasing below is the go/no-go menu the owner approves
*per phase*. Each phase is independently shippable and inert-by-default until an explicit cutover.

### Phase 0 (prerequisite, no code) — resolve spec open questions

The spec is **not approvable without answers** to its 9 open questions (spec §Open questions).
The critical ones before ANY build: **Q1 axis picks** (recommend 1B/2A/3A/4A), **Q3 shim write
authority** (an M2 *entry gate*, not a someday question), **Q9 build-vs-defer WF-3** (this plan
recommends defer). See §7.

### Phase 1 — WF-2 capability layer (BUILD-NOW candidate; ~2-3 PRs)

Maps to spec **M1**. Autonomous-safe after Q1/Q2 answered.

- **PR 1.1 — `ReqCapability` requirement kind + prober registry (option 1B).** Add
  `ReqCapability RequirementKind`, a `Capability`/`OnUnmet` field on `Requirement`, and a
  `CapabilityProber` registry, wired into `deps_scheduler.go`'s existing satisfied/park path.
  Default `OnUnmet: skip` (matches today's `ErrNotAvailable` degrade). Two-valued policy
  (skip|park) only — `fail` deferred per spec. **No op declares it yet → fully inert.**
  *Naming guard (spec §Axis 1 collision warning):* MUST NOT reuse the existing `Capabilities`
  field/`Capability` type; prober namespace (`ollama`) stays distinct from static capability
  namespace (`network.openai`).
- **PR 1.2 — register the three probers** (`ollama`/`openai`/`fpcalc`) as thin adapters over the
  existing `ProbeOllamaAvailable` / `fingerprint.Available` / `ToolRegistry.Available`. Still
  inert (no op references them).
- **PR 1.3 — add declarations to the embedding/fingerprint ops**, seeded from the existing static
  `CapNetworkOpenAI` / `subprocess.spawn` declaration set (spec §Axis 1 "useful synergy"). This is
  the first behavior-affecting PR; ship it last and small. Each op an independent revert.

**Autonomy:** 1.1/1.2 autonomous. 1.3 is per-op behavior change — **human checkpoint** to confirm
the skip-vs-park policy per op (esp. Ollama-gated embedding ops, spec §Q2).

### Phase 2 — WF-3 workflow object + runner (DEFER; human checkpoint required)

Maps to spec **M2**. Do NOT build without an explicit owner "yes" on Q9. If greenlit:
- **PR 2.1 — `internal/operations/workflow/` package** (Workflow/Step types, option 2A recipe
  model) + Pebble `wf:def:*`/`wf:run:*` keyspace + a `workflow.run` op. No seeding, no scheduler
  wiring. Inert.
- **PR 2.2 — skip-vs-fail runner semantics** (spec §Axis 2 pinned): a skipped capability-gated step
  **halts the run as `degraded`**, successor NOT enqueued. Ships with the runner, not deferred.
- **PR 2.3 — seeding + read-compat shim**, per-builtin idempotent, fail-closed, INFO seed manifest.
  **MUST FIX DRIFT-2 first:** the seed/completeness logic must enumerate the **nested
  `ScheduledTasksConfig` struct fields** (9, incl. `label_refinement`), NOT `grep persistence.go`
  for flat `scheduled_*_enabled` (which misses `label_refinement`). **Entry gate: Q3 resolved to a
  single authoritative writer with a surfaced forward-write failure** before the shim is seeded.
  Flag-off (`WorkflowSchedulerEnabled=false`). Roll-forward only once seeded.

### Phase 3 — M3 cutover (DEFER; the one behavior change; explicit human sign-off)

Flip `WorkflowSchedulerEnabled` on; legacy `TaskScheduler` yields via the single-arbiter flag.
Gated by the spec's M3 gates (seeded-value diff iterating the **legacy family set including
`label_refinement`**, + schedule-fidelity soak/structural assertion). This is where the
double-fire risk lives. **Never autonomous.**

### Phase 4 — WF-4 registration-time checks (DEFER, weakest ROI; ~1-2 PRs)

Maps to spec **M4**. `Uses []string` + init-time acyclic-graph validation (option 3A only; 3B/3C
rejected/deferred by spec). **Blocked on the spec's own acceptance criterion:** M4 must cite ≥1
*real* cross-op invocation the graph would catch (spec §Axis 3 evidence gap). None is on record →
defer until a first incident exists.

### Phase 5 — WF-5 UI (DEFER; after Phase-3 soak)

Maps to spec **M5**. Option 4A settings-list editor, reusing `ScheduledTasksSection.tsx` patterns.
Only after built-in workflows have soaked in prod.

## 5. Test strategy

- **Inertness (the critical proof).** Replicate the **already-proven template**
  `TestLabelRefinementDisabledByDefault` (`internal/scheduler/tasks_label_refinement_test.go`):
  assert default/zero-value config ⇒ feature disabled, not scheduled, not run-on-startup, absent
  from any run order. For WF-2: with no op declaring `ReqCapability`, dispatch behavior is
  byte-for-byte today's. For WF-3: with `WorkflowSchedulerEnabled=false`, the legacy scheduler
  fires and the workflow runner does not.
- **WF-2 unit tests** (spec §Testing): available→runs; unavailable→skip (default) / park (opt-in);
  prober error = unavailable (fail-closed for gating, fail-open for the pipeline via skip).
- **WF-3 runner tests:** step ordering; step failure fails the run; **skipped capability-gated step
  with a dependent successor → successor NOT enqueued, run `degraded`** (the anti-silent-passthrough
  test the spec pins); cancel mid-run stops.
- **Seeding/migration tests** (spec §Testing) **+ the DRIFT-2 fix:** a completeness test that
  iterates the **nested `ScheduledTasksConfig` fields** and asserts every one (incl.
  `label_refinement`) has a persisted builtin — and a regression test that *fails* if anyone
  re-introduces the flat-grep approach that misses `label_refinement`. Disabled-legacy seeds
  disabled; per-builtin idempotency; seed-write failure halts startup; legacy-disable propagates;
  failed forward-write surfaces an error.
- **Concurrency / `-race` coverage (CLAUDE.md mandate).** Run the workflow runner and cutover
  arbiter tests under `-race`. Specifically test **cutover double-fire**: flag off → only legacy
  fires; flag on → only workflow fires; **never both**, asserted with the two authorities racing.
  Honor singleton-per-workflow (spec §Q6): a cron fire while a manual run is live must coalesce,
  not double-run a library-mutating op.
- **Proving inertness end-to-end:** `make ci` (30% gate) green with the new code present but no
  declarations/flags enabled, demonstrating zero behavior delta.

## 6. Rollback (per phase)

- **Phase 1 (WF-2):** additive + dormant. 1.1/1.2 = revert PR (no op references them). 1.3 = revert
  the per-op declaration PR; op returns to today's `ErrNotAvailable` behavior. Clean.
- **Phase 2 (WF-3 build):** code is revert-PR; **the seeded `wf:def:*` keyspace is NOT** (spec
  §Rollback). Down-op for a bad seed: redeploy corrected build (per-builtin idempotent re-seed) or
  delete `wf:def:builtin.*` and restart. Behavior stays safe because seeded builtins are dormant
  while the flag is off.
- **Phase 3 (M3 cutover):** the single behavior change — revert = **flip `WorkflowSchedulerEnabled`
  back off**; legacy config still live via the shim; legacy scheduler resumes. Legacy-key deletion
  happens only in a later, separately-gated retirement phase (do NOT delete legacy keys at cutover).
- **Phase 4 (WF-4):** additive metadata + init-time walk; revert PR. If the walk is too strict in
  prod, it warns rather than fails (per spec option 3A).
- **Phase 5 (WF-5):** frontend-only; revert PR.

## 7. Open questions / decisions the owner MUST make before build

Mirrors the spec's 9 open questions, updated with what HEAD now settles:

1. **Q9 first — build WF-3 at all?** This plan recommends **defer** (DRIFT-1: its headline use
   case shipped without it; no demand for user-authored workflows; highest/irreversible blast
   radius). Owner must explicitly accept or override.
2. **Q1 axis picks** (only relevant if WF-2/WF-3 proceed): recommend **1B, 2A, 3A, 4A**.
3. **Q3 shim write authority** — M2 *entry gate*: single authoritative writer (recommend workflow
   store canonical, legacy = read-only forwarding proxy) **with a surfaced forward-write failure
   mode**. Must resolve before any shim is seeded.
4. **Q2 capability-unmet default** — `skip` vs `park` for Ollama-gated embedding ops. Affects
   Phase 1.3.
5. **DRIFT-2 decision** — if WF-3 proceeds, mandate the seed/completeness logic enumerate the
   **nested `ScheduledTasksConfig` fields** (not the flat grep), so `label_refinement` is covered.
6. **Q7** — settled by HEAD: T6 shipped as a plain chain. If WF-3 is built, T6 converts to a
   seeded builtin then (one more row). Confirm.
7. **Q4** — does `dedup_embeddings_enabled` (a *pipeline-stage-inside-ops* flag) truly collapse
   into workflow membership, or stay a plain feature flag? (Leaning: stays a flag; it is not a
   scheduled task.)
8. **Q5** — moot unless Axis 2 option 2B is chosen (recommend 2A → no global-subject surgery).
9. **Q6** — workflow-run concurrency singleton-per-ID (recommend yes) — a data-integrity guard.
10. **Q8** — confirm "nothing runs unless in an enabled workflow" is a v1 non-goal (recommend yes).
11. **WF-4 (Q — new):** defer until ≥1 real undeclared-cross-op incident exists (spec's own
    acceptance bar). Owner: accept deferral?

## 8. Effort estimate

| Phase | PRs | Character | Autonomy |
|---|---|---|---|
| WF-2 (M1) | 2-3 | Mostly mechanical (adapters over existing probes) + 1 design-light policy call | 1.1/1.2 autonomous; 1.3 human checkpoint |
| WF-3 build (M2) | 3-4 | **Design-heavy** (new package, keyspace, seeding, shim, skip-vs-fail semantics) | Human checkpoint per PR; Q3 entry gate |
| WF-3 cutover (M3) | 1 + soak | **Highest-risk** (double-fire, prod-mutating cadence) | Never autonomous |
| WF-4 (M4) | 1-2 | Mechanical, but blocked on evidence | Deferred |
| WF-5 (M5) | 2-3 | Frontend, follows existing settings patterns | Deferred, after soak |

**Bottom line:** WF-2 is ~2-3 small PRs of mostly-mechanical work with modest value. WF-3+ is
~6-8 PRs of design-heavy, partly-irreversible, prod-mutating-scheduler work whose headline
justification has already been met without it. Recommend shipping WF-2 (if the owner wants the
formalization) and deferring the rest.

---

## Provenance

- Grounded at HEAD `17043fbf` (2026-07-12), verified 2026-07-13 via direct grep/read of
  `internal/operations/registry/{types,deps_scheduler}.go`, `internal/config/{config,persistence,update_service}.go`,
  `internal/scheduler/{scheduler,tasks}.go`, `internal/scheduler/tasks_label_refinement_test.go`,
  `internal/plugins/dedup/{calibrate_composite,rebuild_gold_labels,reembed_embeddings}.go`,
  `internal/ai/embedding_client.go`, `internal/fingerprint/fpcalc.go`, `internal/tools/registry.go`.
- READ-ONLY review. No production code was written, scaffolded, or stubbed. The only artifact is
  this plan doc (+ CHANGELOG/TODO note).
- Three drift findings (INIT-1 T5/T6 shipped; nested-config-invisible-to-completeness-gate;
  scattered-booleans-overstated) are the material changes since the spec's 2026-07-10 date.
