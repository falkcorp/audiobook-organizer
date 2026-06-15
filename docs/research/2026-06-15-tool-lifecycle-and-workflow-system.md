<!-- file: docs/research/2026-06-15-tool-lifecycle-and-workflow-system.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9e1f2a3-4b5c-6d7e-8f90-a1b2c3d4e5f6 -->
<!-- last-edited: 2026-06-15 -->

# Managed Tool Lifecycle + Pluggable Workflow System — Research & Devil's Advocate

> Captured 2026-06-15 from a design brain-dump. **Exploratory** — nothing here is
> committed to build. Part 1/2 (tool lifecycle, wizard) are concrete near-term
> features; Part 3 (workflow-engine redesign) needs a dedicated brainstorming →
> spec session. This doc exists so the ideas and the analysis aren't lost.

---

## Verified facts (grounding)

| Question | Answer | Evidence |
|---|---|---|
| Are embeddings cached so we don't recompute every run? | **Yes.** Content-hash + model-keyed cache `emb:c:<model>:<textHash>` → raw float32 blob, plus entity vectors `emb:v:<type>:<id>`, both in PebbleDB. | `internal/database/embedding_store.go:76,83,389` (`GetCachedEmbedding`) |
| Do we shell out to fpcalc for AcoustID fingerprints? | **Yes**, with an `ffmpeg`+Chromaprint fallback, and it already degrades gracefully (`ErrNotAvailable`) when neither is on PATH. | `internal/fingerprint/fpcalc.go:103-106,194-197`, `wholefile.go:56` |
| Is there a static fpcalc binary? | **Yes** — Linux `fpcalc` builds are fully static, published on GitHub releases (`acoustid/chromaprint`). | acoustid/chromaprint releases |
| Is there a static Ollama binary? | **Yes** — `ollama-linux-amd64.tar.zst` (currently v0.30.8 installed rootless on prod). | prod install notes |

**Key consequence:** because embeddings are cached and persisted, Ollama only needs
to be *running* to generate **new/changed** embeddings. A steady-state library is
mostly cache hits → Ollama can be **down almost all the time**. This validates the
duty-cycle idea below.

---

## Part 1 — Managed external-tool lifecycle (Ollama + fpcalc) [near-term, concrete]

### Goal
A uniform "managed tool" abstraction so external binaries (Ollama daemon, fpcalc
CLI, ffmpeg) are detected, optionally auto-downloaded to an **assured path**, and —
for daemons — **started on demand and stopped when idle**, giving the user complete
control and avoiding a constant RAM/CPU cost.

### Resolution order (per tool)
1. **Managed** (auto-download enabled) → download the static binary to
   `/var/lib/audiobook-organizer/tools/<tool>/<version>/` and run from there.
   Assured path, version-pinned, checksum-verified.
2. **System** → `exec.LookPath("<tool>")` (current fpcalc behavior).
3. **Custom** → user-configured absolute path.
4. **None found and not configured** → **auto-disable the dependent pipeline stage**
   (mirrors today's `ErrNotAvailable`). Layer-2 embeddings / fingerprinting simply
   don't run; everything else proceeds.

### Daemon lifecycle (Ollama only; fpcalc/ffmpeg are one-shot CLIs)
- **Start on demand**: bring Ollama up for the on-startup embedding scan.
- **Stop when drained**: once the scan's embed queue is empty, shut Ollama down.
- **Batch subsequent work**: queue new embed requests (book imports, edits) and flush
  them either (a) at the scheduled maintenance window, or (b) if the user enables
  **"allow periodic Ollama"**, on a short debounce (~10 min): spin up, drain the
  batch, spin down. Never hold the memory/CPU between batches.
- **Supervision**: own the child process — health check, crash restart, graceful
  stop on app shutdown, port-conflict handling, resource caps.

### Why a daemon manager and not "always on"
Embeddings are cached + saved. Running Ollama 24/7 wastes ~5GB RAM + GPU/CPU for a
workload that is bursty and mostly cache-served. On-demand + batched = same result,
fraction of the footprint, and the user can hard-stop it.

### Risks / cost
- **Supply chain**: auto-downloading binaries adds a trust surface — pin versions,
  verify checksums/signatures, allow air-gapped/manual override.
- **Process supervision** is real work: zombies, restarts, partial starts, the
  rootless-dies-on-reboot problem (already flagged for prod).
- **Disk**: static Ollama + models are GBs; make the path and cleanup explicit.

---

## Part 2 — Startup wizard tool-install flow [concrete]

Two-tier choices, each defaulting to "recommended":
1. **"Install all recommended tools?"** vs **"Let me choose what to install"** —
   recommended installs Ollama (+bge-m3), fpcalc/ffmpeg via the managed path.
2. **"Accept recommended configuration?"** vs **"Let me configure the tools"** — the
   custom branch exposes per-tool mode (managed/system/custom/disabled), models,
   dimensions, base URLs, duty-cycle policy, thresholds.

Picking "recommended" both times = a working AI/fingerprint pipeline with zero manual
steps. Picking "custom" = many more steps and full control.

---

## Part 3 — Operations → pluggable workflow system [big idea; exploratory]

### The vision (as stated)
Evolve the operations system into a **pluggable pipeline/workflow engine**: plugins
register **actions** (components) and **workflows** (compositions). New feature = new
plugin → new actions registered → new workflows. Workflows are user-composable via UI
(smart-home / CI-CD / data-pipeline style). Actions declare **dependencies** (on other
plugins/actions and on capabilities like "Ollama enabled"); the system **refuses to
register** an action that calls another package's actions without declaring that
dependency, and **conditionally skips** actions whose capabilities are disabled.
`dedup_embeddings_enabled` and the scattered `scheduled_*` booleans collapse into one
model: **nothing runs unless it's in an enabled workflow**; built-in workflows are
auto-enabled, user-added ones start disabled until explicitly enabled.

### Where we already are
The Unified Operations System (UOS) is **~80% of the skeleton**:
- Op registry (`OperationDef`), plugin-registered ops, dry-run-default, structured
  start/progress/complete logging, `POST /operations/v2`.
- **Dependency-scheduling spec** (PR #1440, docs-only, systemd-inspired
  prereq/condition/batching) — not yet implemented; flagged "core-infra blast radius".

The gaps to the vision: (a) **user-composable** workflows via UI, (b) **conditional /
capability gating**, (c) **action-level dependency declaration + registration-time
enforcement**, (d) a **workflow** object (enable/disable/schedule/version).

### Library survey (Go)

| Option | Fit | Why / why not |
|---|---|---|
| **go-workflows** (cschleiden) | Closest *library* | Embeddable, durable, replay-based; pluggable backends incl. SQLite; in-process. **But** workflows are Go **code**, not UI-composable; no action-registry/marketplace concept. Adopt only if we need durable mid-workflow crash recovery. |
| **Temporal / Cadence** | Wrong fit | Battle-tested durable execution, **but** a separate server cluster + its own DB + gRPC. Breaks single-binary deploy; workflow-as-code, not end-user-composable. Massive ops footprint. |
| **Conductor** (Netflix/Orkes) | Closest *concept* | JSON-defined workflows, **UI builder**, task workers, conditional/dynamic tasks — conceptually the target. **But** JVM server + own persistence; workers via HTTP/gRPC. Can't embed in a Go single binary. |
| **Azure/go-workflow** | Too thin | In-memory DAG step orchestrator, no durability/persistence. Just dependency ordering. |
| **goflow** (s8sg) | Overkill | Distributed DAG, needs external queue (Redis); distributed-first. |
| **n8n / Windmill** | Separate product | Great UI workflow builders, but standalone platforms, not embeddable libraries. |

**Conclusion:** no off-the-shelf Go library satisfies all three of *embeddable +
durable + UI-composable action registry*. Servers (Temporal/Conductor) break the
single-binary model; libraries (go-workflows) are code-only. Adopting any still
leaves the UI/registry/domain-integration layer to build ourselves.

### Devil's advocate — reasons NOT to do the big redesign
1. **You already have 80%.** The gap is incremental UOS evolution, not a rewrite.
   A rewrite risks regressing a working core for features addable on top.
2. **No library fits all three needs** — so "adopt a library" doesn't actually save
   the hard part (UI + registry + domain integration).
3. **Premature extraction.** Splitting the op system into its own repo/package now
   freezes an API we don't fully understand, adds cross-repo release friction, and
   kills refactor velocity. Extract *after* it's proven, never before.
4. **UI workflow builders are a product, not a feature**: graph editor, validation,
   versioning, migrating saved workflows when an action's signature changes,
   partial-failure/retry visualization, debugging UX. Easily the biggest cost here.
5. **Static dependency enforcement is hard in Go.** "Refuse to register if an action
   calls undeclared deps" can't be a true static guarantee with compile-time plugin
   registration — it's convention + runtime capability checks, not enforced isolation.
6. **General engines optimize for what we don't need** (distributed, multi-tenant,
   cross-language workers) and underperform on what we do (in-process speed, tight
   coupling to our domain types).
7. **Highest blast radius change in the codebase.** The op system is core-infra; the
   UOS memory already warns "NO code yet (core-infra blast radius)."

### Pros — reasons it's compelling
1. **Plug-and-play extensibility**: new feature = plugin + actions + workflows. Real.
2. **User empowerment**: UI-composed automations are a strong differentiator for a
   self-hosted tool.
3. **Unifies enable/disable**: one model ("nothing runs unless in an enabled
   workflow") replaces scattered booleans (`dedup_embeddings_enabled`, `scheduled_*`).
4. **Conditional tool-gating falls out for free** and ties Part 1 ↔ Part 3: an action
   declares `requires: ollama`; if Ollama is disabled, any workflow using it is
   auto-skipped/disabled.
5. **Dependency-aware = safer**: actions can't run without prerequisites present.

### Recommended path (my take — incremental, not big-bang)
1. **Land PR #1440** dependency-scheduling first (prerequisite for everything).
2. **Add action-level capability declarations** (`requires: [ollama, openai, fpcalc]`)
   → enables the conditional gating that also powers Part 1's auto-disable.
3. **Introduce a `Workflow`** = persisted, enable/disable/schedule-able composition of
   registered ops (DAG or ordered). Seed built-in workflows from today's scheduled ops.
   Collapse the `scheduled_*` / `dedup_embeddings_enabled` flags into workflow state.
4. **UI workflow builder LAST**, once the backend model is proven in code.
5. **Re-evaluate go-workflows** only if durable mid-run crash recovery becomes a hard
   requirement. For cron-like + on-import ops, registry + dependency scheduling likely
   suffices.
6. **Revisit standalone-package extraction** only after the model stabilizes and a
   second consumer actually wants it.

**Net:** the vision is right and achievable as an *evolution of UOS*. The thing to
resist is adopting a heavyweight external engine or extracting a package before the
model is proven. Recommend a dedicated brainstorming → spec session for Part 3 before
any code.

---

## Sources
- [acoustid/chromaprint releases](https://github.com/acoustid/chromaprint/releases) — static Linux fpcalc
- [go-workflows](https://cschleiden.github.io/go-workflows/) — embeddable durable Go workflows
- [Azure/go-workflow](https://github.com/Azure/go-workflow) — in-memory DAG step orchestration
- [goflow (s8sg)](https://github.com/s8sg/goflow) — distributed DAG framework
- [awesome-workflow-engines](https://github.com/meirwah/awesome-workflow-engines) — landscape index
- [Conductor OSS](https://conductor-oss.github.io/conductor/) — UI-composable workflow platform
