<!-- file: docs/operations/pending-prod-actions.md -->
<!-- version: 1.0.1 -->
<!-- guid: 84079e70-633b-4bf5-849a-7b6671f6de61 -->
<!-- last-edited: 2026-07-18 -->

# Pending Prod Actions — the run-on-prod queue

Every outstanding action whose remaining work is a **run against the production
server** (code already merged), consolidated from the 2026-07-17 docs audit.
Two gating classes:

- **operator-run** — an operator can execute when convenient; dry-run first where
  the op supports it.
- **human-decision** — blocked on an explicit human approval recorded via a real
  AskUserQuestion decision (see
  [`docs/plans/DECISIONS-PENDING.md`](../plans/DECISIONS-PENDING.md)); a text
  reply is not a gate release.

Generic op trigger: `POST /api/v1/operations/v2 {"def_id": "<DEF_ID>", "params": {...}}`
with `Authorization: Bearer <api key>`. High-risk dedup actions should be
validated on the dedup sandbox first (private runbook in falkcorp/infra-docs).

| # | Action | What / why | Op / command shape | Gating | Source |
|---|--------|------------|--------------------|--------|--------|
| ~~1~~ ✅ **PH-2 exact-triage (DONE 2026-07-18)** | Run the triage op that classifies the exact-pending dedup backlog into its four populations, then review the population report before any purge | def-id `maintenance.dedup-exact-triage` (read-only classify) → review report → PH-2b per-population purge wave (**never blanket-purge**) | operator-run (triage) → human-decision (PH-2b purge) | **DONE ON PROD 2026-07-18: dismissed 7,891 purgeable, exact-pending 9,074→1,311 (−85.5%), 0 errors** — [`docs/dedup/STATUS.md`](../dedup/STATUS.md); TODO #2 |
| ~~2~~ ✅ **CONS-10 / INIT-2 T6 backlog drain (DONE 2026-07-18)** | Drain/triage the ~15,269-pending candidate backlog now that title-repair + rescore code is merged; run was never executed | **EXECUTED ON PROD 2026-07-18** (deploy g0b474707 → dry-run matched sandbox → apply under human sign-off): exact-pending 9,074→1,311. Sandbox-first, ops #1978/#1982/#2008 | ✅ done | TODO #1 |
| 3 | **Duration-reextract tail** | Re-enqueue the duration-reextract apply for the ~721-book tail that the v3 run left behind | duration-reextract op re-enqueue (see archived design [`2026-06-21-duration-reextract-v3-design.md`](../archive/2026-07-consolidation/specs/2026-06-21-duration-reextract-v3-design.md)); dry-run supported | operator-run | TODO #19 |
| 4 | **iTunes heal Layer-6 re-trigger** | Re-run the iTunes path-heal op so Layer-6 (the last-resort matching layer) reprocesses the residuals (3,720 ambiguous / 5,349 not-found / 4,734 doubled-path) | re-enqueue the iTunes path-heal op after the residual pools shrink | operator-run | TODO #17, #49 |
| 5 | **SLOG-PROD-VERIFY** | Live smoke test that the op-activity logging chain (start/progress/complete + activity tags) actually lands in prod journald/op-log | runbook: [`docs/operations/slog-prod-verify.md`](slog-prod-verify.md) | operator-run | TODO #28 |
| 6 | **PD-3 post-deploy checklist** | The post-deploy verification checklist exists but has never been filled in against a live deploy | checklist: [`docs/pd3-prod-verification.md`](../pd3-prod-verification.md) — run after next `make deploy` | operator-run | TODO #31 |
| 7 | **I1 + I6 prod pprof** | Measurement-only: verify chromem-lazy memory effect and re-audit heap breakdown on prod | `go tool pprof` against the prod pprof endpoint; compare vs [`docs/perf-audit-2026-05-29-heap-breakdown.md`](../perf-audit-2026-05-29-heap-breakdown.md) | operator-run | TODO #32 |
| 8 | **Flip `review_apply_enabled`** | Review-queue apply path merged (#1953) but globally OFF by default (6f2f7ce0); enabling turns dry-run holds into real prod mutations | settings flip (config/Settings UI) — only after a recorded approval | human-decision | TODO #4; [`docs/plans/2026-07-13-review-queue-and-regroup.md`](../plans/2026-07-13-review-queue-and-regroup.md) |
| 9 | **SEC-AUDIT-11 CodeQL dismissals** | Record rationales for the bulk-dismissed CodeQL alerts; console action, not code | GitHub Security console → dismiss-with-rationale per alert group | operator-run (GitHub console) | TODO #30 |

## Standing rules

- Dry-run → apply transitions on prod data always require a fresh dry-run on the
  **current** dataset — stale dry-run reports (pre-merge counts) do not carry over.
- Record each completed run in CHANGELOG.md and tick the matching TODO item; if
  the run fixes/changes user-visible data at scale, check the executive-summary
  criteria (`docs/process/executive-summaries.md`).
- Dedup mutations (rows 1, 2, 8): sandbox-first, then diff the prod dry-run
  against the sandbox dry-run before the human gate.
