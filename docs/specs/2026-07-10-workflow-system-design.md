<!-- file: docs/specs/2026-07-10-workflow-system-design.md -->
<!-- version: 1.1.0 -->
<!-- guid: a3c496fd-5102-4b2a-a71e-c48847ac243f -->
<!-- last-edited: 2026-07-17 -->

# Pluggable Workflow System (WF-2..WF-6) — Design Spec

**Status:** Draft — STOP-FOR-HUMAN review required
**Scope:** Core-infra (op registry `internal/operations/registry/`, config `internal/config/`, plugins, UI last). Spec-only — this document presents OPTIONS, not one committed design.
**Parent task:** INIT-6 (master plan `.claude/notes/2026-07-10-remaining-work-master-plan.md`); GitHub #1471–#1475.

> **GATE (verbatim):** STOP-FOR-HUMAN. Spec-only initiative: core-infra blast radius. NO code, NO task briefs, NO execution until a human approves the spec. The only 'task' is AWAIT-APPROVAL.

---

## Motivation

The operations system can run, serialize, cron-schedule, and event-trigger ops, and — since the
UOS dependency-scheduling work landed flag-off — can also express hard prerequisites
(`Requires []Requirement`, `ReqOpCompleted`/`ReqFieldSet`, `WithRequires(...)` enqueue option;
verified at `internal/operations/registry/types.go:85-90,217-232,268-272`, additive: "an op
without Requires behaves exactly as today"). What it cannot express:

- **"Is this pipeline enabled, and when does it run?" lives in scattered config booleans.** The
  `Scheduled.*` config tree has **eight** families (`scheduled_dedup_refresh_enabled`,
  `scheduled_author_split_enabled`, `scheduled_db_optimize_enabled`,
  `scheduled_metadata_refresh_enabled`, `scheduled_resolve_production_authors_enabled`,
  `scheduled_series_prune_enabled`, `scheduled_ai_dedup_batch_enabled`,
  `scheduled_reconcile_enabled`, each with `_interval` / `_on_startup` twins; enumerated
  exhaustively via `grep -oE 'scheduled_[a-z_]+_enabled' internal/config/persistence.go`) plus
  feature booleans like `dedup_embeddings_enabled` are
  per-feature ad-hoc keys in `internal/config/`. Adding a scheduled pipeline means touching config
  structs, persistence, the settings UI, and the scheduler wiring every time.
- **No capability gating.** Ops that need Ollama / OpenAI / fpcalc discover absence at runtime,
  per-callsite (`ErrNotAvailable` in the fingerprint path; embedding ops fail or skip ad-hoc).
  There is no declarative `requires: [ollama]` an operator or the UI can reason about.
- **No composition object.** "Re-mine gold labels, then recalibrate the composite, then report
  drift" (INIT-1 T6) has no home: it would today be either one monolithic op or hand-wired chained
  enqueues. There is nothing a user can enable/disable/schedule as a unit.

Research base: `docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md:1` ("Managed Tool
Lifecycle + Pluggable Workflow System — Research & Devil's Advocate"). Its library survey
concluded no off-the-shelf Go engine is simultaneously embeddable + durable + UI-composable;
Temporal/Conductor break the single-binary deploy; `go-workflows` is code-only. Its
recommendation — evolve UOS incrementally, never big-bang — is adopted here as locked.

**Goal:** one approved design for evolving the op registry into enable/disable/schedule-able
workflow composition (WF-2→WF-5), chosen from the options below by the human reviewer.

## Goals

> Issue-number caveat: the master plan states only the range "GitHub #1471–#1475" for WF-2..WF-6
> without a per-item mapping. The sequential assignment below (WF-2=#1471 … WF-6=#1475) is
> **inferred, not verified** — check each number against the actual GitHub issues before approval.

- WF-2 (#1471): action-level capability/requirement declarations (`requires: [ollama, openai, fpcalc]`).
- WF-3 (#1472): a persisted `Workflow` object — enable/disable/schedule-able composition of
  registered ops — that collapses the `scheduled_*` toggles and `dedup_embeddings_enabled` into
  workflow state, and gives INIT-1 T6's scheduled refinement loop its natural home.
- WF-4 (#1473): registration-time dependency checks (refuse/flag undeclared cross-plugin use).
- WF-5 (#1474): UI workflow builder — LAST, after the backend model is proven.
- Migration/compat: existing ops and existing `scheduled_*` config keep working unchanged until an
  explicit, reversible cutover.

## Non-goals (v1)

- WF-6 (#1475): adopting `go-workflows` — **re-evaluate-only**, and only iff durable mid-run crash
  recovery becomes a hard requirement. Not part of any milestone.
- Distributed / multi-process execution — single-process registry as today.
- Extracting the op system into a standalone package/repo — explicitly deferred until a second
  consumer exists (research doc, devil's-advocate point 3).
- Durable mid-step workflow state / replay semantics — v1 workflow runs are sequences of ordinary
  ops; each op is already individually resumable/cancellable via the registry.
- The managed tool lifecycle itself (research Part 1: auto-download, Ollama duty-cycle). WF-2 only
  defines the capability *declaration/gating* surface that Part 1 would later plug a real
  prober/manager into. **The boundary, precisely:** a WF-2 `CapabilityProber` is a *stateless
  availability check only* — "is this tool reachable right now, yes/no + reason" (an HTTP ping,
  an exec-lookup). Anything that *manages* the tool — download/install, process lifecycle,
  Ollama duty-cycling, retry/queue-until-up — is Part 1 and stays out of scope; if an M1 prober
  needs more than a read-only probe to answer, that is the signal it has crossed the line.

## Decisions (locked — do not relitigate)

1. **Evolution of UOS, not a rewrite:** the workflow layer composes existing `OperationDef`s and
   the landed `Requires` machinery; no external engine (Temporal/Conductor/n8n rejected — server
   footprint breaks single-binary deploy; `go-workflows` rejected for now — code-only workflows).
2. **Sequencing + additive posture are locked; WF-3's *existence* is not.** What is locked: IF
   the pieces ship, they ship in the order WF-2 → WF-3 → WF-4 → WF-5, each independently
   shippable and additive, and **WF-6 is re-evaluate-only**. What is NOT locked: whether WF-3
   (the Workflow object) is justified for v1 at all — that is open question 9, and it is the
   central open call, not settled here. Everything downstream of WF-3 in this spec (Axis 2, the
   seed map, M2/M3, WF-5) is **conditional on Q9 resolving "yes"**; if Q9 resolves "no"
   (keep `scheduled_*` + INIT-1 T6 as a plain op chain), the milestone chain collapses to
   M1(WF-2) + M4(WF-4) and the rest of this spec is shelved. Note: when WF-3 does ship, it lands
   across two milestones — flag-off build (M2) then a distinct, reversible cutover (M3) — so the
   milestone list reads M1(WF-2) → M2/M3(WF-3) → M4(WF-4) → M5(WF-5); same sequence, with the
   cutover made explicit.
3. **Enablement model:** built-in workflows are auto-enabled (preserving today's behavior);
   user-added workflows start disabled until explicitly enabled (#1472).
4. **UI builder ships LAST** (#1474), only after the backend model has run real built-ins.
5. **Everything lands flag-off/additive** until a single, reversible cutover milestone — the same
   posture the dependency-scheduling layer used.

## Current state (grounded)

| Fact | Evidence |
|---|---|
| Op registry has `Requires []Requirement`, `RequirementKind` (`ReqOpCompleted`, `ReqFieldSet`), `WithRequires` enqueue option — landed, opt-in | `internal/operations/registry/types.go:85-90,217-232,268-272` (re-verify: `grep -n 'Requires \[\]Requirement\|RequirementKind\|ReqOpCompleted' internal/operations/registry/types.go`) |
| **`OperationDef` ALREADY has `Capabilities []Capability`** — a static, coarse permission vocabulary ("system capabilities the op needs", lint-enforced today, runtime-enforced vNext) with constants incl. `CapNetworkOpenAI = "network.openai"`, `CapSubprocessSpawn`, etc. Populated by ~28 live ops and it is the documented plugin-authoring pattern | `internal/operations/registry/types.go:73` (field), `:173-193` (type + constants, `CapNetworkOpenAI` at `:182`); re-exported via `pkg/plugin/sdk/capability.go:21`; live declarations e.g. `internal/plugins/dedup/llm_review.go:29` (`sdk.CapNetworkOpenAI`), `calibrate_embedding_thresholds.go:123`, `reembed_embeddings.go:101`; docs `docs/development/writing-a-plugin.md:155-217`. Re-verify: `grep -rn 'Capabilities: \[\]sdk.Capability' internal/ \| wc -l` |
| `OperationDef` also has `DependsOn []string` — op def IDs that must **NOT be running** for this op to start (mutual exclusion, NOT an invocation graph) | `internal/operations/registry/types.go:83` |
| Dependency/condition/batching design (systemd-inspired) already specified | `docs/archive/2026-07-consolidation/specs/2026-06-13-uos-dependency-scheduling-design.md` |
| Research base incl. Go workflow-library survey + devil's advocate | `docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md:1` (re-verify: `grep -n 'Managed Tool Lifecycle' docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md`) |
| Scattered scheduling/feature toggles to collapse — **eight** `scheduled_*` families, exhaustive | `internal/config/` `Scheduled.*` tree (`scheduled_dedup_refresh_*`, `scheduled_author_split_*`, `scheduled_db_optimize_*`, `scheduled_metadata_refresh_*`, `scheduled_resolve_production_authors_*`, `scheduled_series_prune_*`, `scheduled_ai_dedup_batch_*` [`persistence.go:558,1285`], `scheduled_reconcile_*` [`persistence.go:561,1298`]) + `dedup_embeddings_enabled` (config `persistence.go` / `update_service.go`; consumer `internal/plugins/dedup/reembed_embeddings.go`). Re-verify the set is still complete at impl time: `grep -oE 'scheduled_[a-z_]+_enabled' internal/config/persistence.go \| sort -u` |

---

## Design options

Each axis presents 2–3 options with blast radius. **The human reviewer picks one per axis** (or
sends the axis back for more work). Recommendations are marked but not committed.

### Axis 1 — WF-2: capability declarations

*What "capability" means here:* a named external dependency with a **stateless** probe function
answering "available right now?" (see the Non-goals boundary — no download, no lifecycle, no
duty-cycle). v1 ships exactly the three probers the master plan mandates: `ollama`, `openai`,
`fpcalc`. `ffmpeg` and `whisper` are natural later additions under the same contract but are
NOT part of M1 — each extra prober widens the WF-2/Part-1 boundary and none is required by an
in-scope declaration yet.

**⚠ Naming collision — TWO distinct "capability" concepts must be reconciled.** `OperationDef`
**already** carries `Capabilities []Capability` (`types.go:73`): a *static coarse permission*
declaration (`network.openai`, `library.write`, `subprocess.spawn`, ... — `types.go:173-193`,
lint-enforced, populated by ~28 live ops, re-exported through `pkg/plugin/sdk/capability.go` and
taught in `docs/development/writing-a-plugin.md:155-217`). WF-2 proposes something different: a
*runtime availability probe* ("is Ollama reachable right now?"). These are orthogonal — one is
"what the op is permitted/expected to touch", the other is "is the external tool up" — and
whichever axis option is chosen, the implementation must NOT reuse the `Capabilities` field or
`Capability` type name for the probe concept. This spec calls the WF-2 concept an **availability
requirement** (prober names `ollama`/`openai`/`fpcalc`) to keep the two apart. Useful synergy,
not conflict: ops already declaring the static `CapNetworkOpenAI` are exactly the candidate set
for a `ReqCapability: openai` availability declaration (same for `subprocess.spawn`+`fpcalc`),
and M1's "declarations on the embedding/fingerprint ops" work item should be seeded from that
existing static list.

| | Option 1A — a new availability field on `OperationDef` (e.g. `RequiresTools []string`) | Option 1B — new `RequirementKind` (`ReqCapability`) reusing the landed `Requires` machinery | Option 1C — separate capability manifest per plugin (registered alongside ops) |
|---|---|---|---|
| Mechanism | Registry checks an availability prober at dispatch; unmet → op skipped/parked with reason | `Requirement{Kind: ReqCapability, Capability: "ollama"}` evaluated by the existing satisfied/park/`waiting_deps` path | Plugin registers `CapabilityManifest{Provides, Requires}`; registry cross-checks at init |
| Blast radius | **Medium — NOT the "low, additive" it first appears.** An earlier draft proposed "new `Capabilities []string` field" — that is a **name+type collision** with the EXISTING `Capabilities []Capability` static-permission field every op already sets (~28 declarations, plugin-SDK re-export, documented authoring pattern). Overloading the existing field with runtime-probe semantics silently changes the meaning of every live declaration; a distinctly-named new field avoids the collision but puts a SECOND capability-ish notion on the same struct, which must be documented against the first | **Low-medium.** Touches the requirement evaluator + park/wake paths (core scheduler code, `internal/operations/registry/deps_scheduler.go`), but gets park-until-available, failure propagation, and enqueue-time overrides for free; no collision with the static `Capabilities` field (different machinery, different name) | **Medium.** New registration surface across every plugin; most of it unused until WF-4 |
| Interaction | Needs its own skip/park semantics built from scratch; must be reconciled in docs against the static `Capabilities` field on the same struct | Unifies with `ReqOpCompleted`/`ReqFieldSet` — one evaluation model, one status (`waiting_deps`), one UI surface later | Best positioned for WF-4's registration-time graph, worst for incremental delivery |
| Risk | Semantics drift from the Requires layer (two gating systems) + permanent two-capability-notions confusion on `OperationDef` | A capability flap (Ollama down) parks ops; needs a "skip instead of park" per-op policy knob; prober name namespace (`ollama`) must stay visibly distinct from static capability namespace (`network.openai`) | Over-designs before WF-3 proves the model |

**Recommendation: 1B** — one gating model, and the wake-on-change plumbing already exists (the
periodic `waiting_deps` sweep re-evaluates when a capability comes back). Add a per-requirement
`OnUnmet: park|skip` policy (**default `skip`** for capability kind, matching today's
`ErrNotAvailable` degrade-gracefully behavior; `park` opt-in for ops that should wait).
**OnUnmet is two-valued in v1.** No in-scope declaration needs a `fail` policy (skip covers the
degrade path, park covers wait-for-Ollama), and a third value adds a distinct
failure-propagation branch to the runner's skip-vs-fail matrix (Axis 2) that nothing exercises;
`fail` is a documented later addition, adopted only when a concrete op needs it.

Illustrative type (normative *if 1B is chosen*):

```go
// Addition to internal/operations/registry/types.go (illustrative — pinned at impl time).
// NOTE naming: this is the WF-2 *availability* concept. It intentionally does NOT touch or
// reuse the existing static-permission field OperationDef.Capabilities []Capability
// (types.go:73,173-193) — see the collision warning at the top of Axis 1.
const ReqCapability RequirementKind = "capability" // named external tool/service must be available

// Requirement gains:
//   Capability string        `json:"capability,omitempty"` // ReqCapability: PROBER name ("ollama"),
//                                                          // a distinct namespace from the static
//                                                          // Capability constants ("network.openai")
//   OnUnmet    UnmetPolicy   `json:"on_unmet,omitempty"`   // park | skip; default skip for capability
//                                                          // ("fail" deliberately deferred — see above)

// CapabilityProber answers availability; registered once per prober name.
type CapabilityProber interface {
    Name() string                    // "ollama", "fpcalc", ...
    Available(ctx context.Context) (bool, string) // ok, human reason
}
```

### Axis 2 — WF-3: the `Workflow` object (the core decision)

| | Option 2A — ordered step list ("recipe") | Option 2B — DAG via existing `Requires` | Option 2C — full engine (per-step durable state, retries, branches) |
|---|---|---|---|
| Shape | `Workflow = {ID, Name, Builtin, Enabled, Schedule, Steps []Step}`; steps run sequentially, each step = one op enqueue (+ params); step N+1 enqueued only after N completes | Workflow = a *set* of op enqueues; ordering expressed by stamping `WithRequires(ReqOpCompleted...)` between steps; the scheduler's existing park/wake machinery does the sequencing | Workflow rows + per-step state rows + retry policy + conditional branches + compensation |
| Blast radius | **Low-medium.** New `workflow.go` + Pebble keyspace + a runner that is itself an op ("workflow.run") — zero changes to existing scheduler internals | **Medium.** No new runner, but subject semantics leak: `ReqOpCompleted` is subject-scoped (book v1), while workflow steps are usually global/maintenance ops with empty subjects, which "cannot be required-on" per the UOS design — that gap must be closed in core scheduler code | **High.** This is the "product, not feature" trap the research doc warns about; overlaps WF-6 territory |
| Fits INIT-1 T6? | Yes — re-mine → recalibrate → report is exactly a 3-step recipe | Yes, but needs the global-subject gap fixed first | Yes, overkill |
| UI (WF-5) later | Trivial to render/edit (ordered list) | Graph editor needed (much bigger UI product) | Graph editor + state visualizer |
| Failure semantics | Step fails → workflow run fails (v1); step *skipped* (capability unmet under WF-2's `OnUnmet: skip`) → run halts as `degraded` — see the pinned skip-vs-fail semantics below the table | Inherited from Requires failure propagation | Configurable, complex |

**Recommendation: 2A for v1**, with the explicit note that 2A can grow into 2B later (a `Step` can
gain a `DependsOnSteps []string` field, turning list into DAG) without a persistence break.
Option 2C is rejected for v1 by locked decision 1.

**Skip-vs-fail propagation (pinned — must be implemented as stated in M2, not deferred).** Skip
is not failure, so the intersection of WF-2's fail-open `OnUnmet: skip` and WF-3's sequential
runner must be defined explicitly or a capability-gated step (e.g. Ollama down) silently
passes through and step N+1 consumes output that was never produced — a fail-open,
silently-degrading-data path. v1 semantics: **a skipped step halts the run** with run status
`degraded` (distinct from `failed`), recording which step skipped and the prober's reason;
subsequent steps are NOT enqueued. Pass-through ("skip and continue") is **forbidden for any
step whose successor consumes its output**; a later per-step `ContinueOnError`/`SkipIsOK` knob
may relax this only for steps declared independent. The runner test suite must include "step
skipped (capability unmet) with a dependent successor → successor not enqueued, run marked
degraded".

Illustrative model (normative *if 2A is chosen*):

```go
// internal/operations/workflow/workflow.go (new package; illustrative — pinned at impl time).
type Workflow struct {
    ID          string    `json:"id"`           // "builtin.dedup-refresh", "user.<uuid>"
    Name        string    `json:"name"`
    Builtin     bool      `json:"builtin"`      // seeded by code; builtins auto-enabled
    Enabled     bool      `json:"enabled"`      // THE enable bit (replaces scheduled_*_enabled)
    Schedule    string    `json:"schedule"`     // cron expr or "@every 6h"; "" = manual/event only
    OnStartup   bool      `json:"on_startup"`   // replaces scheduled_*_on_startup
    Steps       []Step    `json:"steps"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
    // Deferred growth path (NOT v1): Version int — per-definition version as a UI migration
    // hook. Speculative until WF-5 proves a need; adding a field later is not a persistence break.
}

type Step struct {
    OpType string            `json:"op_type"` // registered OperationDef ID — validated at save
    Params map[string]any    `json:"params"`
    // Future (2A→2B growth path): DependsOnSteps []string
}
```

**Persistence:** `wf:def:<workflowID>` → Workflow JSON; `wf:run:<workflowID>:<runID>` → run row
(status, current step, per-step op IDs) in the existing op store. A workflow *run* is itself an
op (`workflow.run`) so it inherits logging, progress, cancel, and history for free.

### Axis 3 — WF-4: registration-time dependency checks

The research doc is blunt: true static "refuses to register an action that calls another
package's actions without declaring it" is **not achievable** in Go with compile-time plugin
registration — it can only be convention + checks.

| | Option 3A — declared-graph validation | Option 3B — runtime call interception | Option 3C — CI-side static lint |
|---|---|---|---|
| Mechanism | `OperationDef` gains `Uses []string` (op types it enqueues/invokes); at registry init, validate every `Uses` target exists, capability closure is coherent, and the graph is acyclic; log+fail startup in dev, warn in prod | Wrap cross-op invocation in a registry call (`registry.Invoke(ctx, opType, ...)`) that errors when the caller's def doesn't declare the target | `go vet`-style analyzer in CI greps/analyzes for `EnqueueOp` calls and cross-checks declared `Uses` |
| Blast radius | **Low.** Additive metadata + init-time walk | **Medium-high.** Requires routing existing direct calls through the registry — many touchpoints | **Zero runtime.** CI-only; can drift from reality |
| Honesty | Declarative; only as true as declarations | Actually enforces at the boundary it controls | Best-effort |

**Naming reconcile (do not confuse the two graphs):** the proposed `Uses []string` (ops this op
*enqueues/invokes* — an invocation graph) is the near-inverse of the EXISTING
`OperationDef.DependsOn []string` (`types.go:83` — op def IDs that must **NOT be running** for
this op to start; a mutual-exclusion graph). Same struct, opposite semantics; the field docs
must state the distinction explicitly or implementers will conflate them.

**Recommendation: 3A only; 3B rejected for v1** — the call-rerouting churn is the highest-touch
part of the whole initiative for the least user value. 3C is **deferred entirely** to a
follow-up decision after 3A has run (the master plan mandates a single mechanism —
registration-time checks — and a CI analyzer that can drift from reality is a second
enforcement path for the least value; revisit only if 3A's declared graph proves insufficient).

**Evidence gap (for the human review):** no concrete undeclared-cross-plugin-invocation incident
is on record in this codebase, and the research doc concedes true static enforcement is "not
achievable" — 3A is convention + checks built ahead of any observed violation. WF-4 stays in
scope because the master plan mandates it (#1473), but M4's acceptance criteria must cite at
least one *real* cross-op invocation the declared graph would catch, so its value is testable —
otherwise the human should consider deferring WF-4 until a first incident exists.

### Axis 4 — WF-5: UI workflow builder (LAST)

| | Option 4A — settings-page list editor | Option 4B — graph canvas builder |
|---|---|---|
| Shape | Workflows page: table of workflows (enable toggle, schedule field, run-now button) + a step-list editor (add step = pick op from registry catalog + params form) | Node/edge canvas, drag-drop, live validation |
| Cost | Small — fits the existing React settings patterns | A product: graph editor, validation UX, versioned-workflow migration when op signatures change (research devil's-advocate point 4) |

**Recommendation: 4A**, matching 2A's list model. 4B only ever if 2B's DAG growth path is taken
**and** demand exists. Either way WF-5 starts only after built-in workflows have run in prod for
a soak period (locked decision 4).

### WF-6 — go-workflows: re-evaluation criteria only (no work planned)

Adopt **iff** all three become true: (a) a workflow emerges whose steps must survive a mid-*step*
process crash with exactly-once semantics (today: each step is a restartable op; a crashed run is
re-runnable because constituent ops are idempotent/dry-run-gated); (b) that workflow cannot be
decomposed into idempotent ops; (c) the maintenance cost of hand-rolled recovery exceeds the cost
of embedding a replay-based engine. Record the evaluation in a dated doc under `docs/research/`
before any adoption decision.

---

## How WF-3 subsumes INIT-1 T6 and the `scheduled_*` toggles

**INIT-1 T6 (scheduled refinement loop).** T6 is "a scheduled (owner-toggle) op chain: re-mine →
recalibrate composite → report drift. Built-in-disabled until enabled (aligns with WF-3)." Under
option 2A it is literally a seeded workflow — no bespoke scheduler wiring:

```jsonc
// Seeded builtin (disabled by default — the one builtin that is NOT auto-enabled,
// per INIT-1 T6's explicit owner-toggle requirement):
{
  "id": "builtin.dedup-refinement-loop",
  "builtin": true, "enabled": false, "schedule": "@every 168h",
  "steps": [
    {"op_type": "dedup.rebuild-gold-labels",              "params": {"apply": false}},
    {"op_type": "dedup.calibrate-composite",              "params": {"apply": false}},  // <TBD — INIT-1 T6 deliverable, NOT registered today>
    {"op_type": "dedup.calibration-drift-report",         "params": {}}                 // <TBD — INIT-1 T6 deliverable, NOT registered today>
  ]
}
```

**This block is NOT copy-ready.** Of the three op IDs, only `dedup.rebuild-gold-labels` exists
today (`internal/plugins/dedup/rebuild_gold_labels.go:97`). `dedup.calibrate-composite` and
`dedup.calibration-drift-report` **do not exist yet** — they are INIT-1 T6 deliverables
(composite recalibration + drift report), owned by that initiative, not by INIT-6; the closest
existing op is `dedup.calibrate-embedding-thresholds`
(`calibrate_embedding_thresholds.go:111`), which sweeps only the single embedding cosine
cut-point and is NOT the composite recalibration T6 will build. Because `Step.OpType` is
validated against the registry at save, this workflow can only be seeded once those ops are
registered — so the "literally a seeded workflow" simplicity claim is contingent on INIT-1 T6
shipping its ops first (which is exactly the sequencing open question 7 covers).

For THIS builtin (the T6 refinement loop), data-mutating steps stay dry-run inside the workflow;
applying results remains a human AskUserQuestion-gated action outside the loop (prod-apply
review gate is not weakened by workflow automation). **That claim is scoped to
interactive/dry-run-then-apply ops and must not be generalized:** most seeded builtins below
fire *autonomous* scheduled maintenance ops (author-split, db-optimize, metadata-refresh,
series-prune, ...) that mutate on cron with NO per-run AskUserQuestion — that is what
"scheduled" means. For that class the actual safety envelope is the `Enabled` bit plus schedule
fidelity (the M3 seeded-value diff + soak gates), not an interactive apply gate.
**Sequencing note:** if INIT-1 T6 ships before WF-3 exists, it ships as a
plain scheduled op chain and is *converted* to a builtin workflow during WF-3's seeding — the
seed map below simply gains one more row. Neither initiative blocks the other.

**`scheduled_*` toggles.** Each of the **eight** `Scheduled.*` config families maps 1:1 to a
seeded builtin workflow — the mapping below is exhaustive against
`grep -oE 'scheduled_[a-z_]+_enabled' internal/config/persistence.go` (8 hits), and the
migration test suite must include a completeness check that **fails if any `scheduled_*` family
lacks a seeded builtin**, so a family added later cannot be silently dropped at cutover. The
three legacy fields map onto the workflow verbatim (`_enabled`→`Enabled`,
`_interval`→`Schedule`, `_on_startup`→`OnStartup`):

| Legacy config keys | Seeded builtin workflow |
|---|---|
| `scheduled_dedup_refresh_*` | `builtin.dedup-refresh` |
| `scheduled_author_split_*` | `builtin.author-split` |
| `scheduled_db_optimize_*` | `builtin.db-optimize` |
| `scheduled_metadata_refresh_*` | `builtin.metadata-refresh` |
| `scheduled_resolve_production_authors_*` | `builtin.resolve-production-authors` |
| `scheduled_series_prune_*` | `builtin.series-prune` |
| `scheduled_ai_dedup_batch_*` | `builtin.ai-dedup-batch` |
| `scheduled_reconcile_*` | `builtin.reconcile` |
| `dedup_embeddings_enabled` | `builtin.dedup-embeddings` (`Enabled` bit gates the embed pipeline; its ops declare `ReqCapability: ollama` under WF-2) |

Migration seeding is **per-builtin idempotent, evaluated on EVERY startup** — NOT a one-shot
"first startup with the workflow store empty" guard. On each startup, for every legacy
`scheduled_*` family (plus `dedup_embeddings_enabled`) that has **no persisted builtin**, seed
that builtin with `Enabled`/`Schedule`/`OnStartup` **copied from the live legacy values** (not
defaults), so behavior is preserved bit-for-bit; builtins that already exist (including any
operator edits) are never overwritten. Rationale — the store-level guard is rejected because it
fails open on a mid-batch crash: a seed that writes 4 of 9 builtins and dies leaves the store
non-empty, so the remaining prod-mutating pipelines (author-split, series-prune,
resolve-production-authors, ...) would never be seeded, would vacuously pass any diff that
iterates the seeded set, and would silently stop firing at cutover; revert+redeploy would not
recover (store non-empty → seed skipped). Per-builtin seeding self-heals on the next startup.
**Seed-write failure is fail-closed:** if persisting any missing builtin fails, startup HALTS
(do not continue with a partial seed) — a partial seed must never be able to reach cutover.
Nuclear recovery remains documented: delete the affected `wf:def:builtin.*` keys (or wipe
`wf:def:*`) and restart; per-builtin seeding restores from live legacy values. The seeding step
**must log a full seed manifest at INFO before persisting** — one line per builtin: source
legacy keys → resulting `Enabled`/`Schedule`/`OnStartup` — so a mis-mapped key (e.g. a disabled
legacy pipeline seeded enabled) is operator-visible before it can fire anything. Legacy keys then become a read-compat
shim (the old settings UI keys proxy through) until a Phase-D-style retirement — the same
pattern as CFG-2 (#1536). **Shim write authority must be single-writer:** open question 3 (which
surface is authoritative) must be resolved **before M2 seeds the shim** — the simplest safe
posture is that one surface is canonical and the other is a read-only proxy that forwards
writes to it, so an operator disabling a prod-mutating pipeline (author-split, series-prune,
resolve-production-authors, ...) on EITHER surface always lands on the canonical enable bit. A
test must assert a legacy-side disable propagates to the workflow `Enabled` bit. **Forward-write
failure mode (required, part of the Q3 resolution):** if the proxy's forward write to the
canonical bit FAILS, the failure must be surfaced to the operator (the legacy-side call returns
an error; the UI must not report a disable that never landed) — a silently-lost disable of a
prod-mutating pipeline while the op keeps firing is the exact fail-open path this gate exists to
prevent. A test must assert a failed forward write is reported, not swallowed.

## Migration / compat for existing ops

- **Ops are untouched.** A workflow references op def IDs; `OperationDef`, handlers, params, and
  every existing enqueue path (manual POST /operations/v2, event triggers, cron) keep working.
  Nothing runs *differently* because workflows exist; the "nothing runs unless in an enabled
  workflow" end-state is aspirational and only ever reached per-pipeline via the seeded-builtin
  migration above — never by turning ops off wholesale.
- **Cutover flag:** the legacy `Scheduled.*` scheduler keeps firing until
  `WorkflowSchedulerEnabled` (default **off**) flips scheduling authority to the workflow layer.
  Double-fire is prevented by the flag being the single arbiter (legacy path checks it and yields).
  Instant revert = flip the flag back; the legacy config values are still present via the shim.
- **Capability declarations (WF-2)** are added op-by-op in follow-up PRs; an op without them
  behaves exactly as today (mirrors the `Requires` posture).

## Milestones (proposed — NOT authorized; execution requires human approval of this spec)

- **M1 — WF-2 capability layer.** Chosen Axis-1 option + the three mandated probers
  (ollama/openai/fpcalc — stateless availability checks only, per the Non-goals boundary)
  + declarations on the embedding/fingerprint ops. Additive; default `skip` preserves behavior.
- **M2 — WF-3 workflow object + runner.** Chosen Axis-2 option, Pebble persistence, `workflow.run`
  op, seeded builtins from legacy config (all eight `scheduled_*` families + `dedup_embeddings_enabled`;
  per-builtin idempotent seeding on every startup, fail-closed on seed-write failure,
  INFO seed manifest logged before persisting), read-compat shim. Flag-off
  (`WorkflowSchedulerEnabled=false`). **Entry gate:** open question 3 (shim write authority)
  resolved to a single authoritative writer with a surfaced forward-write failure mode BEFORE
  the shim is seeded. *All-at-once vs pilot — acknowledged tradeoff:* seeding all nine families
  in one milestone is the largest single blast-radius step and proves the Workflow object only
  at cutover; a one-pipeline pilot would validate the seed/shim/runner mechanics with far less
  to diff. Big-bang seeding is chosen DELIBERATELY to avoid a legacy/workflow split-brain (a
  partial seed means two scheduling authorities to reason about during the compat window), and
  because seeded builtins are dormant while the flag is off — the behavior change is entirely
  deferred to M3.
- **M3 — cutover.** Flip scheduling authority to workflows (the ONE behavior change), soak, then
  schedule legacy-key retirement as a separate CFG-2-style phase. **Explicit M3 gates:** (a) a
  diff **iterating the LEGACY family set** (every `scheduled_*` family + `dedup_embeddings_enabled`):
  each family MUST have a persisted builtin (gate FAILS on a missing builtin — iterating the
  seeded set would pass vacuously over an unseeded family) and that builtin's
  `{Enabled, Schedule, OnStartup}` must equal the live legacy values — zero deltas required —
  and (b) a soak with the flag on that verifies schedule fidelity for EVERY seeded builtin:
  either the soak covers at least one firing of the longest-interval builtin (≥168h if a weekly
  schedule is seeded — "one full cron-cycle" is otherwise ambiguous across `@every 6h`..`@every 168h`),
  or schedule fidelity for the long-interval builtins is asserted structurally (parsed-cron /
  parsed-interval equality against the legacy `_interval`) with observed firings required for
  the shorter cycles, before any legacy-key retirement is scheduled. The seeded builtins fire prod-MUTATING ops
  (author-split, series-prune, resolve-production-authors, db-optimize, metadata-refresh); a
  mistranslated `_interval`→`Schedule` expression or a disabled pipeline seeded enabled fires a
  mutating op on the wrong cadence, so schedule-fidelity verification is NOT skippable.
- **M4 — WF-4 registration-time checks.** `Uses` declarations + init-time graph validation only
  (CI lint backstop 3C deferred — see Axis 3).
- **M5 — WF-5 UI.** Chosen Axis-4 option, after M3 soak.

Each milestone is independently shippable and additive until M3.

## Files likely modified (indicative, for blast-radius review only)

| Area | Files |
|---|---|
| Registry (WF-2/WF-4) | `internal/operations/registry/types.go`, `internal/operations/registry/deps_scheduler.go` (the park/wake requirement evaluator Option 1B touches; further dispatcher files pinned at impl) |
| Workflow layer (WF-3) | new `internal/operations/workflow/` package |
| Config shim (WF-3) | `internal/config/persistence.go`, `internal/config/update_service.go` |
| Legacy scheduled-op wiring / cutover arbiter (WF-3 M3) | `internal/scheduler/tasks.go` (23 `Scheduled.*` references — the highest-collision surface; also where INIT-1 T6's plain op chain would land) + `internal/scheduler/scheduler.go` |
| Seed consumers | `internal/plugins/dedup/reembed_embeddings.go` |
| UI (WF-5) | `web/` settings/workflows pages |

## Testing (indicative)

| Test | Asserts |
|---|---|
| Capability requirement unit tests | available→runs; unavailable→skip/park per policy (v1 is two-valued — `fail` deferred, see Axis 1); prober error = unavailable (fail-closed for gating, fail-open for the pipeline via `skip`) |
| Workflow runner tests | step ordering; step failure fails the run (v1); **step skipped (capability unmet) with a dependent successor → successor NOT enqueued, run marked `degraded` (no silent pass-through)**; cancel mid-run cancels the current op and stops |
| Seeding/migration tests | legacy values copied verbatim; **a DISABLED legacy pipeline seeds disabled**; **per-builtin idempotency: second startup with all builtins present seeds nothing; a startup with one builtin MISSING (simulated partial/crashed seed) seeds ONLY the missing one from live legacy values and never overwrites existing builtins (incl. operator edits)**; **seed-write failure halts startup (fail-closed)**; seed manifest logged at INFO before persisting; **runtime completeness: every `scheduled_*_enabled` family in `internal/config/persistence.go` has a PERSISTED builtin (asserted against the store, not just the code's seed map — test FAILS if one is missing)**; legacy-side disable propagates to the workflow `Enabled` bit (single-writer shim); **a FAILED forward write from the legacy proxy surfaces an error (no silent lost-disable)** |
| Cutover tests | flag off → legacy fires, workflow doesn't; flag on → inverse; never both; gate (a) diff **iterates the legacy family set** — fails if any family lacks a persisted builtin, and each builtin's `{Enabled, Schedule, OnStartup}` equals the live legacy values |
| Registration validation tests (WF-4) | unknown `Uses` target rejected; cycle rejected; clean graph passes |

## Rollback

> **GATE (verbatim):** STOP-FOR-HUMAN. Spec-only initiative: core-infra blast radius. NO code, NO task briefs, NO execution until a human approves the spec. The only 'task' is AWAIT-APPROVAL.

- This spec authorizes **nothing**. There is no code to roll back; the rollback posture below is
  what the human is asked to approve for the eventual implementation.
- M1/M4 additive and dormant (capability declarations absent = today's behavior). Revert =
  revert PR.
- **M2 is roll-forward once seeding has run**, not a clean PR revert: the seed writes the
  persisted `wf:def:*` keyspace, and reverting the PR does NOT unseed it. Behavior stays safe
  either way (seeded builtins are dormant while `WorkflowSchedulerEnabled` is off — legacy
  scheduler unchanged), but the honest down-operation for a BAD seed is: (a) with per-builtin
  idempotent seeding (the specified design), redeploy a corrected build — missing/deleted
  builtins re-seed from live legacy values on next startup; or (b) manually delete the affected
  `wf:def:builtin.*` keys (or wipe `wf:def:*`) and restart. "Revert = revert PR" applies to M2's
  CODE only, not to its persisted keyspace.
- M3 is the single behavior change, gated by `WorkflowSchedulerEnabled` (default **off**); revert
  = flip the flag, legacy config still live via the shim. Legacy-key deletion happens only in a
  later, separately-gated retirement phase.
- **Scoped mutation claim:** the workflow LAYER introduces no NEW prod data mutation beyond the
  workflow store's own keyspace — but the ops it schedules mutate exactly as they do today, and
  after M3 the workflow scheduler is what fires prod-mutating pipelines (author-split,
  series-prune, resolve-production-authors, db-optimize, metadata-refresh). Do NOT read this as
  "cutover is mutation-free": M3's schedule-fidelity gates (legacy-set seeded-value diff + soak,
  see Milestones) exist precisely because a wrong seed fires a mutating op on the wrong
  cadence. **Two op classes, two safety envelopes:** for *interactive dry-run-then-apply* ops
  (e.g. the T6 refinement-loop steps), workflows never bypass the op's own
  dry-run/AskUserQuestion apply gate. For the *autonomous scheduled* maintenance pipelines the
  seeded builtins fire (author-split, series-prune, db-optimize, metadata-refresh, ...), there
  is NO per-run apply gate — cron fires them mutating — so their real safety envelope is the
  `Enabled` bit + the M3 schedule-fidelity gates, and this rollback section must not be read as
  claiming an interactive gate protects them.

## Open questions (UNRESOLVED — for the human review; the spec is not approvable without answers)

1. **Axis picks:** 1A/1B/1C? 2A/2B/2C? 3A/3B/3C? 4A/4B? (Recommendations: 1B, 2A, 3A, 4A.)
2. **Capability flap policy:** is default-`skip` right for `ReqCapability`, or should embedding
   ops `park` and drain when Ollama returns (ties into the research Part-1 duty-cycle idea)?
3. **Enable-bit ownership during compat:** while the shim exists, which surface is authoritative
   on conflicting concurrent writes — workflow store or legacy config? **This is an M2 entry
   gate, not a someday question:** it must be resolved to a SINGLE authoritative writer before
   the shim is seeded, or an operator's disable of a prod-mutating pipeline via the legacy
   settings UI can leave the workflow copy `Enabled` and the op keeps firing against explicit
   intent. Proposed: workflow store canonical, legacy keys a read-only proxy that forwards
   writes to it (test: legacy-side disable propagates). **Required criterion on any resolution:**
   the forward-write FAILURE mode must be surfaced to the operator (legacy-side call errors; no
   UI state showing a disable that never landed on the canonical bit) — resolving ownership
   without resolving the failure mode does not satisfy the M2 entry gate.
4. **Does `dedup_embeddings_enabled` really collapse?** It gates a *pipeline stage inside* other
   ops, not just a scheduled op. Is "membership of ops in an enabled `builtin.dedup-embeddings`
   workflow" an acceptable replacement semantics, or does it stay a plain feature flag?
5. **Global-subject gap:** if 2B (DAG-via-Requires) is chosen, closing "empty-subject ops cannot
   be required-on" is core scheduler surgery — accept that cost, or does that force 2A?
6. **Workflow-run concurrency:** may two runs of the same workflow overlap (cron fires while a
   manual run is live)? Proposed: no — singleton per workflow ID, second enqueue coalesces.
7. **INIT-1 T6 sequencing:** ship T6 as a plain op chain first and convert at WF-3 seeding, or
   hold T6 until WF-3 exists? (Proposed: ship first, convert later — don't block INIT-1.)
8. **Scope of "nothing runs unless in an enabled workflow":** end-state ambition or explicit
   non-goal? v1 treats it as non-goal; confirm.
9. **Is WF-3 justified at all for v1?** The Workflow object's sole concrete v1 payoff is
   collapsing the eight scattered `scheduled_*` families (+ `dedup_embeddings_enabled`) into one
   surface; INIT-1 T6 works fine as a plain scheduled op chain (Q7). The simplest dominating
   alternative is "keep `scheduled_*` + one op chain" — weigh explicitly whether the collapse
   alone justifies the new object, or whether T6-as-op-chain defers WF-3 indefinitely.
