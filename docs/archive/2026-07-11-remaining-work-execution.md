<!-- file: docs/archive/2026-07-11-remaining-work-execution.md -->
<!-- version: 1.0.2 -->
<!-- guid: 8877ac6a-c413-408f-b4b3-fd48a6ece32a -->
<!-- last-edited: 2026-08-11 -->

# Status — Remaining-Work Execution Wave (2026-07-10 – 2026-07-11)

## TL;DR

Ran the planning→execution pipeline end to end: PR #1870 produced a 10-initiative
catalog (INIT-1..10 — specs, plans, and 50 weak-model-proof task briefs) plus an
execution manifest, then execution landed ~15 code PRs across dedup, search,
metadata, and tech-debt, split into a wave-1 batch (#1871–#1875) and a longer
wave-2 batch (#1878–#1888), with three docs-only follow-up PRs (#1884, #1890,
#1891) recording progress along the way. One **confirmed prod data-loss bug**
was surfaced and deliberately left unresolved for a human decision rather than
auto-fixed — see #1887 (Author/Series wiped on `CreateOrganizedVersion`
write-back). Phase B is partly unblocked: the INIT-2 `engine.go` dedup tasks
landed (#1875, #1878, #1881, #1883). Remaining autonomous-lane work is still
open: INIT-1, INIT-3 (T03–T08), INIT-4 (T06), INIT-9 (T01/T02), and INIT-10.

This document itself is a further follow-up: it, and the folder split it
belongs to (`docs/status/` vs `docs/executive-summaries/`), landed in
[PR #1892](https://github.com/falkcorp/audiobook-organizer/pull/1892).

For the polished, stakeholder-facing narrative version of this same body of
work, see the "Remaining-work execution wave" theme in
[`docs/executive-summaries/2026-07-04-monthly-roundup-executive-summary.md`](../executive-summaries/2026-07-04-monthly-roundup-executive-summary.md).

## Shipped this session

| PR | Area | What |
|----|------|------|
| #1870 | planning | 10-initiative planning package (specs + plans + 50 task briefs, INIT-1..10) + execution manifest |
| #1871 | search | Dead Bleve field-boost bug fixed (title/author/series ranking) |
| #1872 | metadata | Duplicate duration-scoring functions unified behind one ratio-tier table (golden-fixture verified) |
| #1873 | reliability | Cache warmers enrolled in shutdown WaitGroup (goroutine-leak-on-restart fix) |
| #1874 | search | **Correctness bug**: per-user search filters (read_status etc.) were silently discarded — fixed |
| #1875 | dedup | `GetFolderDuplicatesCore` implemented on Pebble + MemStore (dedup tier 2 now live) |
| #1878 | dedup | Candidate status secondary index + backfill op |
| #1879 | metadata | Scoring literals extracted to `MetadataScoringConfig` (behavior-preserving) |
| #1880 | tech-debt | sdkguard dependency-guard fixed via decorator inversion + type move |
| #1881 | dedup | Drain-gate parity with the emission chokepoint; drain flag v1→v2 |
| #1882 | search | Bleve hit hydration batched via `GetBooksByIDs` (was per-hit) |
| #1883 | dedup | Full-scan emit() mutex sharded 16-way (CONC-3); -race verified |
| #1884 | docs | Executive summary + changelog for execution wave 2 |
| #1885 | dedup | Boilerplate-title blocklist moved to a config-extendable module |
| #1886 | ci | Mock-freshness CI glob fixed to cover nested mocks dirs |
| #1887 | organizer | **Confirmed prod bug**: Author/Series wiped on `CreateOrganizedVersion` write-back (test documents it, fix deferred) |
| #1888 | search | Bleve facet counts (genres/languages/tags), handler+warmer in lockstep |
| #1890 | docs | Changelog + follow-ups for execution wave 2b |
| #1891 | docs | Consolidated the wave-1/wave-2 executive summaries into the monthly roundup doc |

## In flight

- Phase B (dedup engine work) is partly unblocked — the INIT-2 `engine.go`
  tasks that landed (#1875, #1878, #1881, #1883) clear the path for the
  remaining INIT-2 items, but those have not started yet.

## Blocked / deferred

- **INIT-5 T2** — Deluge spike, needs sign-off before proceeding.
- **INIT-9 REPO-SIZE-1** — history-rewrite plan needs review before execution
  (destructive, one-way operation on repo history).
- **INIT-7** — on hold.
- **Author/Series prod-fix decision (#1887)** — the bug is confirmed and
  documented by a test, but the fix itself is deliberately deferred pending a
  human decision on rollout approach.

## Next steps

- Remaining autonomous-lane initiatives still open: **INIT-1**, **INIT-3**
  (T03–T08), **INIT-4** (T06), **INIT-9** (T01/T02), **INIT-10**.
- Resolve the blocked/deferred items above before resuming their initiatives.
