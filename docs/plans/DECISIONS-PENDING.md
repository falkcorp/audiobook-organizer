<!-- file: docs/plans/DECISIONS-PENDING.md -->
<!-- version: 1.0.0 -->
<!-- guid: 38115603-5a52-4745-83a5-2c9ca4528a9d -->
<!-- last-edited: 2026-07-17 -->

# Decisions Pending — the STOP-FOR-HUMAN queue

Single queue of every item blocked on an explicit human decision (per the
prod-apply review gate, a decision means a real recorded AskUserQuestion answer,
not a passing text reply). Consolidated from the 2026-07-17 docs audit.

| # | Decision | Options | Recommendation (if recorded) | Blocking | Source |
|---|----------|---------|------------------------------|----------|--------|
| 1 | **REPO-SIZE-1** — how to shrink repo/history size | (a) full history rewrite; (b) targeted BFG purge; (c) fresh-start repo; (d) forward-only hygiene + GitHub Support gc | Plan recommends **Option (d)** | INIT-9 close-out; any history-rewrite work | [`2026-07-10-repo-size-history-rewrite-plan.md`](2026-07-10-repo-size-history-rewrite-plan.md), [`2026-07-12-repo-size-targeted-purge-package.md`](2026-07-12-repo-size-targeted-purge-package.md) |
| 2 | **INIT-5 T2** — Deluge integration spike sign-off (API viability, auth, label conventions) | approve spike result / redirect approach / drop torrent relocation | — | INIT-5 T3–T7 (whole remaining torrent-relocation track) | [`2026-07-10-torrent-relocation.md`](2026-07-10-torrent-relocation.md) |
| 3 | **INIT-6** — workflow-system spec review (WF-0 brainstorm output) | approve spec / revise scope / not-doing | — | WF-2/3/4/5 implementation; PR #1935 sits open | [`2026-07-10-workflow-system.md`](2026-07-10-workflow-system.md) |
| 4 | **INIT-8** — community fingerprint index: brainstorm/review session (privacy, hosting, export scope) | proceed to spec / park / not-doing | — | Entire community-index track (spec-only today) | [`../specs/2026-07-10-community-fingerprint-index-design.md`](../specs/2026-07-10-community-fingerprint-index-design.md) |
| 5 | **INIT-7 hold** — greenlight the Responses-API migration (AI-RESP-A/B/E/F) | greenlight / keep on hold / drop | Held deliberately (vendor-API churn risk) | AI-RESP-A/B/E/F tasks | [`2026-07-10-responses-api-migration.md`](2026-07-10-responses-api-migration.md) |
| 6 | **server test-package structure** (INTERNAL-SERVER-PKG-STALL residual) | (a) raise package test timeout; (b) split `internal/server` test package; (c) migrate ~60 call sites to a `newTestServer` helper | — | Closing the CI-stall class for `internal/server` | TODO #26 (H1:849-877) |
| 7 | **Product rename** (1.17) | pick a name | — | Branding sweep across UI/docs/binary | TODO #37 (H1:2751) |
| 8 | **Flip `review_apply_enabled` ON in prod** | enable globally / enable per-hold-type / keep OFF | Sandbox-first validation before any enable | Review-queue apply path doing real work (merged #1953, default OFF) | [`2026-07-13-review-queue-and-regroup.md`](2026-07-13-review-queue-and-regroup.md); [`../operations/pending-prod-actions.md`](../operations/pending-prod-actions.md) row 8 |
| 9 | **PH-2b purge wave scope** — which residual populations get purged, and how | per-population ops (fragment-floor / title-leak / stub) vs review-only drain | Never blanket-purge (four-population analysis) | Draining the ~9,074 exact-pending backlog | [`../dedup/STATUS.md`](../dedup/STATUS.md); [`../operations/pending-prod-actions.md`](../operations/pending-prod-actions.md) row 1 |

## Process

- When a decision lands, move the item's execution steps into TODO.md (if code)
  or [`pending-prod-actions.md`](../operations/pending-prod-actions.md) (if a
  prod run), record the decision here with date + outcome, and drop the row in a
  follow-up edit.
- Items 1–5 are the execution-manifest human gates
  ([manifest](2026-07-10-execution-manifest.md)); resolving them closes out the
  remaining INIT briefs.
