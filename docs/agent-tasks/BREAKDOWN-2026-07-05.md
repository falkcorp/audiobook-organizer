<!-- file: docs/agent-tasks/BREAKDOWN-2026-07-05.md -->
<!-- version: 1.0.0 -->
<!-- guid: acf0805f-ce46-43e0-a822-0155831aab4f -->
<!-- last-edited: 2026-07-05 -->

# Agent-Task Breakdown & Fan-Out Plan — 2026-07-05

Turns the approved concurrency-parallelization spec (`docs/specs/2026-07-05-concurrency-parallelization-design.md`) into
weak-model-proof agent briefs plus a fan-out strategy. See
[`ORCHESTRATION.md`](ORCHESTRATION.md) (coordinator + workers, dependency waves).

## Method

Every task was verified against the current codebase during planning (6 read-only
scouts re-ran every anchor at HEAD — the audit's line numbers had drifted), then
sorted into three buckets. **Only Bucket 1 becomes agent briefs.**

---

## Bucket 1 — Authored as agent briefs (15 tasks, 5 workstreams)

### ⚠️ Same-file collision rule (drives wave ordering)

| Shared file | Tasks that touch it | Resolution |
|-------------|---------------------|------------|
| `internal/dedup/engine.go` | TASK-01, TASK-02, TASK-03, TASK-04 | serialize: wave1=TASK-01, wave2=TASK-02, wave3=TASK-03, wave4=TASK-04 |
| `internal/itunes/service/importer.go` | TASK-01, TASK-02 | serialize: wave1=TASK-01, wave2=TASK-02 |

All other files are touched by exactly one task → those workstreams run their tasks
in a single parallel wave.

### WS-1 — dedup-engine (backend) · maps to CONC-1, CONC-2, CONC-3, CONC-4

| Task | Src id | Title | Tier | Why tier | Wave |
|------|--------|-------|------|----------|------|
| TASK-01 | CONC-1 | Shard BookSignatureScan's O(n²) pairwise loop across a bounded worker pool | **Opus-class** | O(n²) hot path; sharding must guard the emitted map + DB write without changing dedup output — highest-risk change in the package. | 1 |
| TASK-02 | CONC-2 | Parallelize FullScan's unified-scoring pass with a bounded worker pool | **Sonnet-class** | Confirmed prod bottleneck; per-item independent but touches shared stores — logic + store-safety judgment. | 2 |
| TASK-03 | CONC-3 | Parallelize AcoustIDScan's per-book loop with a bounded pool and guard its four shared maps | **Sonnet-class** | Four unguarded shared maps + a counter to guard correctly under a pool — easy to introduce a data race. | 3 |
| TASK-04 | CONC-4 | Parallelize FullScan main-pass Layer-1 checks while keeping Layer-2 embedding batching serial | **Sonnet-class** | Must split parallel Layer-1 from serial Layer-2 circuit-breaker state — design-adjacent, not mechanical. | 4 |

Execution mode: SERIAL WAVES (coordinator-driven) — trigger: all four tasks edit internal/dedup/engine.go (collision table row 1); wave N+1 starts only after wave N merges and siblings rebase.
### WS-2 — backfill-pools (backend) · maps to CONC-5, CONC-6, CONC-7, CONC-8

| Task | Src id | Title | Tier | Why tier | Wave |
|------|--------|-------|------|----------|------|
| TASK-01 | CONC-5 | Parallelize dedup.embed-scan sync path with a rate-limited worker pool | **Sonnet-class** | Network-bound; concurrency must respect the embedding backend rate limit, not NumCPU. | 1 |
| TASK-02 | CONC-6 | Parallelize the startup embedding backfill loops over books and authors | **Sonnet-class** | Two startup loops, network rate-limited; integration with the startup path Reporter. | 1 |
| TASK-03 | CONC-7 | Parallelize the tag-backfill ExtractMetadata loop with a CPU-sized pool | **Sonnet-class** | Straightforward pool but I/O sizing + Reporter wiring warrant Sonnet. | 1 |
| TASK-04 | CONC-8 | Add book-lookup memoization then parallelize mine_gold_labels and dataset_backfill | **Sonnet-class** | Adds memoization AND a pool with a shared-cache guarding decision. | 1 |

Execution mode: /parallel-sweep — trigger: 4 mechanically-similar tasks (≥3 threshold), disjoint files per the collision matrix, gate = `make ci`. Invocation: TASK-01,02,03,04 all in wave 1.
### WS-3 — acoustid-consolidation (backend) · maps to CONC-9

| Task | Src id | Title | Tier | Why tier | Wave |
|------|--------|-------|------|----------|------|
| TASK-01 | CONC-9 | Delete the serial server-side AcoustID backfill and route startup to the parallel plugin op | **Opus-class** | Delete/redirect a code path with caller wiring + a possibly-shared helper — dangling-reference risk. | 1 |

Execution mode: SINGLE-AGENT (strong model) — trigger: judgment work (delete/redirect a code path with caller wiring + a shared helper to check); ⚠ review-critical, coordinator line-review before merge.
### WS-4 — itunes-import (backend) · maps to CONC-10, CONC-11

| Task | Src id | Title | Tier | Why tier | Wave |
|------|--------|-------|------|----------|------|
| TASK-01 | CONC-10 | Parallelize organizeImportedBooks file-organize + UpdateBook over imported books | **Sonnet-class** | File-move parallelism must ensure no two books resolve to the same target path. | 1 |
| TASK-02 | CONC-11 | Parallelize enrichImportedBooks metadata fetch with a bounded pool preserving the rate-limit circuit-breaker | **Sonnet-class** | Circuit-breaker semantics must survive parallelization — reframe 'consecutive' as a shared atomic. | 2 |

Execution mode: SERIAL WAVES (coordinator-driven) — trigger: both tasks edit internal/itunes/service/importer.go (collision table row 2); enrich (TASK-02) serializes after organize (TASK-01).
### WS-5 — bulk-ops-pools (backend) · maps to CONC-12, CONC-13, CONC-14, CONC-15

| Task | Src id | Title | Tier | Why tier | Wave |
|------|--------|-------|------|----------|------|
| TASK-01 | CONC-12 | Parallelize bulkFetchMetadataImpl over req.BookIDs with a conservative request-scoped pool | **Sonnet-class** | Request-scoped; conservative sizing to avoid starving the server / tripping the source rate limit. | 1 |
| TASK-02 | CONC-13 | Parallelize BatchUpdateMetadata and ImportMetadata per-item DB round-trips | **Sonnet-class** | Two request-driven loops in one file with guarded result aggregation. | 1 |
| TASK-03 | CONC-14 | Parallelize the duration-backfill per-book GetBookFiles loop | **Haiku-class** | Mechanical mirror of the other backfill pools; fully specified. | 1 |
| TASK-04 | CONC-15 | Parallelize the itunes path-reconcile per-book GetBookFiles loop | **Haiku-class** | Mechanical mirror of TASK-03; fully specified. | 1 |

Execution mode: /parallel-sweep — trigger: 4 mechanically-similar tasks (≥3 threshold), disjoint files per the collision matrix, gate = `make ci`. Invocation: TASK-01,02,03,04 all in wave 1.

### Coordinator protocol (verbatim)

> **Coordinator owns git. Workers never push.** Each worker operates only inside its
> assigned worktree: edit, test, commit — then stop. Workers never run `git push`,
> `gh pr`, or any merge command. The coordinator runs the gate (`make ci`) in each
> finished worktree, opens the PR, merges (rebase/FF unless the repo profile says
> otherwise), and then **rebases every open sibling worktree** before dispatching
> anything else.
>
> **Per-merge sibling-rebase loop:** after EVERY merge to `origin/main`:
> for each open sibling worktree, `git fetch origin && git rebase
> origin/main`. A sibling that skips a rebase is a future conflict.
>
> **Conflict escalation ladder** (in order, never skip a rung): 1) clean rebase;
> 2) conflict-resolver subagent (Sonnet-class, only when the conflict spans 1–3 small
> files); 3) file-copy cherry-pick fallback — re-apply the task's file states onto a
> fresh branch from HEAD; 4) mark `rebase_blocked`, stop the lane, escalate to a human.
>
> **A wave MUST NOT start** while any of: the previous wave has an unmerged PR; any
> sibling worktree is un-rebased; the gate is red on `origin/main`; or a
> `rebase_blocked` marker is unresolved.

---

## Bucket 2 — NOT briefs: needs brainstorm/design first

| Item | Why it needs design first |
|------|---------------------------|
| CONC-B2-1 — AutoResolveCertain (internal/dedup/auto_resolve.go:112) | Per-candidate MergeBooks; a naive pool could double-merge one book across two pairs processed concurrently in the same run. Needs a partition-by-disjoint-book-ID-set redesign, not a blind fan-out (fix-pattern #3). Design session first. |
| CONC-B2-2 — applyRegroupPlan (internal/plugins/maintenance/itunes_regroup.go:244) | Sequential DB writes over shared book/PID state; likely intentionally serialized to keep PID reassignment consistent. Confirm the ordering invariant before touching — could be correct as-is (fix-pattern #3). |

---

## Bucket 3 — NOT tasks: operational / low-value (no brief)

CONC-B3-1 (PurgeStaleCandidates) · CONC-B3-2 (One-time migrations/backfills).

---

## Cost / efficiency strategy (fan-out)

- **Tier split:** Haiku-class for the two mechanical N+1 mirrors (WS-5/T03–T04);
  Sonnet-class for the logic/integration tasks; Opus-class for the two ⚠
  review-critical ones (WS-1/T01 O(n²) sharding, WS-3/T01 delete-redirect).
- **Coordinator owns git/gh:** workers stay in their worktree and report done; only
  the coordinator merges + rebases siblings (protocol above).
- **Waves respect the collision table** — WS-1's four `engine.go` tasks and WS-4's two
  `importer.go` tasks serialize; every other workstream is one parallel wave.
- **Cheapest-first across workstreams:** all 5 workstreams touch disjoint files, so
  the whole package can run concurrently; rank orders them by value when capacity is
  limited (WS-1 first — the confirmed prod incident).
- **Every task ships a `-race` test** proving identical output to the serial version;
  a green `make ci` without the race test is not a pass for this initiative.
