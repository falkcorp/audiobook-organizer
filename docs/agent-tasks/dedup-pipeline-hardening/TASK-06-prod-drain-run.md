<!-- file: docs/agent-tasks/dedup-pipeline-hardening/TASK-06-prod-drain-run.md -->
<!-- version: 1.0.0 -->
<!-- guid: a2572138-6b24-4e2d-a8de-ef442ccc3c79 -->
<!-- last-edited: 2026-07-10 -->

# TASK-06 — Prod drain of the ~387k exact-candidate backlog: dry-run → AskUserQuestion → apply → verify (INIT-2 T6) [NOT AGENT WORK — human + coordinator]

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a real AskUserQuestion apply gate. — THIS TASK IS THE EXCEPTION: nothing here is autonomous past the dry-run. The apply requires a real AskUserQuestion decision; a text-reply approval does NOT count (memory: feedback_prod_apply_review_gate).
**File-ownership:** none — zero code files. This is an operational runbook against production (172.16.2.30).

**Priority:** P1 · **Effort:** S · **Recommended subagent:** NONE — human + coordinator session · **Why:** prod-data mutation behind a human decision gate is not delegable to a subagent · **Depends on:** TASK-03 merged + deployed — the ONLY hard prerequisite. Deploy-batching preference: deploy once after TASK-05 also merges to avoid a second deploy; but if T05 (P2) is blocked or delayed, deploy on T03 alone and accept a second deploy later — never stall this P1 drain on the P2 perf task.

## ⛔ START HERE (do this first, exactly)

No worktree — no code changes are permitted in this task. Preconditions:

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
cd "$REPO" && git fetch origin && git log origin/main --oneline -5
# TASK-03 must be on main (>=1 hit, else STOP):
grep -n "dedup_stale_drain_v2_done" internal/plugins/dedup/drain_stale.go
# Deploy the merged code to prod (run verbatim — never substitute an SSH/scp workaround):
make deploy
# Auth: if .claude/.api-token is missing/expired, run the server-bootstrap skill first.
```

## Goal

Execute the gated production drain of the stale exact-layer candidate backlog (#1512, ~387k
pending rows) using the existing op `dedup.drain-stale` — dry-run report first, a REAL
AskUserQuestion apply decision second, apply third, verified shrink fourth. No new code; the
op, its checkpointing, its soft `stale-drain` reclassification, and its v2 done-flag (from
TASK-03) already exist. REUSE the op API exactly — do not hand-edit rows over SSH.

## Background (verify before running)

- Op contract: `dedup.drain-stale` is dry-run by default (`apply=false` tallies only);
  `apply=true` soft-reclassifies would-purge rows to status `"stale-drain"` — NEVER a hard
  delete, so the run is auditable and the data survives (M0 purge_legacy_fp precedent; but
  see Rollback below — recovery is roll-forward only, no restore op exists today). Apply
  checkpoints page offsets and resumes; dry-runs always full-scan so report totals are
  complete. A versioned done-flag (`dedup_stale_drain_v2_done` after TASK-03) makes a second
  apply a no-op.
- The report buckets would-purge counts by first-rejecting gate (reason buckets incl.
  `part_vs_whole`, `boilerplate_title`, `short_duration`, `identifier_conflict`,
  `missing_book`, plus any TASK-03 additions).
- API auth: `Authorization: Bearer <abk_...>` from `.claude/.api-token` (3-line file — use the
  api_key line only). X-API-Key is NOT supported.
- Timeout: the op registers a 60-minute timeout; the ~387k backlog paged at 500/page has
  historically fit, but if a run times out, the apply resumes from its checkpoint on re-run.
- **Per-candidate error policy (so partial tallies are interpretable):** (a) a failed or
  missing `GetBookByID` for a candidate's book is NOT an error path — the candidate buckets
  as `missing_book` and counts in `would_purge` (fail-open into a purge bucket); (b) a
  page-level `ListCandidates` error HALTS the run and returns the partial result with an
  error (fail-closed) — a report accompanied by an error is incomplete, do not present it as
  a full dry-run; (c) on apply, a failed `UpdateCandidateStatus` for one row is
  logged (`drain-stale: reclassify failed`) and SKIPPED (fail-open, row stays `pending`) — so
  post-apply pending may exceed `baseline − would_purge` by exactly the number of logged
  reclassify failures; check the op log for that line when the shrink math doesn't close.

- **Re-verify these anchors before running** — line numbers drift:
  Both commands use `grep -E` (POSIX extended regex) so alternation (`|`) works
  identically under GNU grep, BSD/macOS grep, and ripgrep shims — do not rewrite
  them into BRE `\|` form.

  ```bash
  # Op ID + params + flag (>=3 hits)
  grep -nE '"dedup.drain-stale"|drainStaleDoneFlag = |Apply bool' internal/plugins/dedup/drain_stale.go
  # Engine contract: soft status + reason buckets (>=3 hits)
  grep -nE 'staleDrainStatus|drainReasonPartVsWhole|func \(de \*Engine\) DrainStaleCandidates' internal/dedup/drain_stale.go
  ```
  Zero hits = the deployed code is not what this runbook assumes — STOP and report.

## Step-by-step

1. Preconditions block above (TASK-03 on main, `make deploy` done, service healthy — check
   `systemctl is-active` via `ssh 172.16.2.30` and the server-logs skill if in doubt).
2. **Baseline count (read-only):** query pending exact candidates via the candidates list API
   (status=pending, layer=exact) and record the exact total. Use the existing full-scan path
   as-is — do NOT run `dedup.build-candidate-status-index` before the drain. Pre-drain,
   `pending` ≈ the whole table, so activating the index makes a `status=pending` read
   point-read ~387k `dedup:r:` records one-by-one — SLOWER than the sequential scan it
   replaces (spec §C4 "Selectivity timing"). Index activation is step 8, after the drain.
3. **Dry-run:** trigger op `dedup.drain-stale` with `{"apply": false}` via the operations API.
   Wait for completion; capture `inspected`, `would_purge`, `kept`, and the full reason-bucket
   breakdown + samples from the op log/summary.
4. **Present the report** to the owner: exact counts per bucket, sample pairs, and the
   before-count from step 2. Sanity checks before asking: `inspected` ≈ baseline pending
   count; no single bucket is implausibly large (e.g. `boilerplate_title` claiming >80% wants
   a sample inspection first).
5. **AskUserQuestion gate (mandatory):** ask, as a real AskUserQuestion tool decision with
   options (Apply / Abort / Investigate samples first), whether to run apply. A chat-text
   "yes" does not satisfy the gate. Record the decision.
6. **Apply (only on an explicit Apply decision):** trigger `dedup.drain-stale` with
   `{"apply": true}`. Monitor progress; on timeout/interrupt, re-trigger — it resumes from
   the checkpoint; the done-flag is set only at full completion.
7. **Verify shrink:** re-run the step-2 count and a fresh `{"apply": false}` dry-run. Expect:
   pending count reduced by ≈ `would_purge` (minus any logged reclassify failures — see the
   per-candidate error policy above); new dry-run `would_purge` ≈ 0; rows now carry status
   `stale-drain` (spot-check via the candidates API with status=stale-drain).
8. **Activate the status index (named, owned step — this is where T4's perf win turns on):**
   with the drain applied and `pending` now small, run `dedup.build-candidate-status-index`
   on prod (requires TASK-04 merged + deployed; if it is not deployed yet, record this step
   as REMAINING — it is the deliberate post-drain activation spec §C4 defers to T6, and no
   other task performs it). The op writes only rebuildable index rows, so it needs no apply
   gate; verify it completes and sets `dedup_candidate_status_index_v1_done`, then spot-check
   that a status-filtered candidates list still returns correct results.
9. **Report with exact numbers** (house style): baseline N, would_purge M, post-apply pending
   P, residual dry-run R, index-activation done/REMAINING — plus COMPLETED/REMAINING/BLOCKED
   lines. Update TODO.md/CHANGELOG via the normal quick-fix PR flow (that PR is ordinary
   autonomous work, not part of this gate).

## How to test

```bash
# Gate for this task is operational, not make ci:
# (1) dry-run report captured BEFORE apply; (2) AskUserQuestion decision recorded;
# (3) post-apply verification counts reported with exact numbers.
# Read-only verification example (adjust host/port/token path as provisioned):
TOKEN=$(grep -o 'abk_[A-Za-z0-9_-]*' .claude/.api-token | head -1)
curl -s -H "Authorization: Bearer $TOKEN" 'http://172.16.2.30:8080/api/v1/dedup/candidates?status=pending&layer=exact&limit=1' | head -c 400
```

Expected: JSON containing a `total` field — record it exactly; if the route/shape differs,
list the registered routes and the handler from the two files that define them
(`grep -rn 'dedup/candidates' internal/server/wire_dedup_routes.go internal/server/handlers/dedup/handler.go | head`)
rather than guessing — `wire_dedup_routes.go` registers `GET /dedup/candidates` and
`handlers/dedup/handler.go` implements `ListDedupCandidates`.

## Acceptance criteria

- [ ] Dry-run report (inspected / would_purge / kept + per-reason counts + samples) captured and presented BEFORE any apply
- [ ] A real AskUserQuestion decision (Apply / Abort / Investigate) recorded before `apply=true`; no apply on a text-reply approval
- [ ] Post-apply: baseline vs post-apply pending counts and residual dry-run would_purge reported as exact numbers
- [ ] No hard deletes: reclassified rows are status `stale-drain` (spot-check reported)
- [ ] Post-drain index activation: `dedup.build-candidate-status-index` run AFTER shrink verification (or explicitly recorded as REMAINING if TASK-04 is not yet deployed); NEVER run pre-drain
- [ ] Anti-over-suppression: N/A (no code/filter is added by this task; the drain's own `kept` count and TASK-03's parity tests are the safeguards)
- [ ] Status update ends with COMPLETED/REMAINING/BLOCKED counts.

## Commit message

No code commit. If TODO.md/CHANGELOG.md are updated afterward, use:

```
docs(dedup): record prod drain-stale apply results (INIT-2 T6, #1512)

Baseline/would-purge/post-apply counts from the gated dedup.drain-stale
run; rows soft-reclassified to stale-drain, none deleted.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

Not applicable to the drain itself (no code). The optional TODO/CHANGELOG doc commit follows
the standard quick-fix flow: branch → PR → `gh pr merge <number> --rebase`.

## Idempotency / Rollback

Already-done check (op-level, not grep): a `{"apply": true}` trigger that immediately logs
"already completed; skipping (flag set)" means the v2 apply has run — do only the
verification steps 7–9. Dry-runs are always safe to repeat.

**Rollback — roll-forward only (stated limitation, do not be misled):** the drain is a soft
reclassification (`stale-drain`, never a delete), so the data survives intact and auditable —
but NO status-restore op exists today (`internal/plugins/dedup` contains only the drain op;
there is no inverse). If recovery were ever needed, a NEW restore op would have to be built
first: it must route through `UpdateCandidateStatus` so that, if TASK-04's index flag is
active, the `dedup:s:` rows move with the records (a raw status rewrite would silently desync
the index), and it would itself be a dry-run + AskUserQuestion gated prod-data mutation.
There is no instant inverse; plan the apply decision accordingly.
