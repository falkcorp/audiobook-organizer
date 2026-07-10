<!-- file: docs/plans/2026-07-10-community-fingerprint-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: d9103cf7-bfb1-44b8-a61a-d1e641797ff0 -->
<!-- last-edited: 2026-07-10 -->

# Community Audiobook Acoustic-Fingerprint Index — Plan (STOP-FOR-HUMAN stub)

**Gate (verbatim):** STOP-FOR-HUMAN. New-product blast radius. Spec only; NO code, NO task
briefs, NO repo creation, NO external publication until a human approves. The only 'task' is
AWAIT-APPROVAL.

**Goal:** obtain a human decision on the design spec
`docs/specs/2026-07-10-community-fingerprint-index-design.md` (INIT-8). This plan intentionally
contains **no execution steps** — there is nothing to execute until the gate lifts.

**Spec:** `docs/specs/2026-07-10-community-fingerprint-index-design.md` — answers the master
plan's five design-question clusters (D1, D3–D6: on-disk format, PR-bot loop, identity unit,
trust/governance/license, AcoustID relationship) plus D2, the repo+Actions architecture — an
in-scope sub-question of the master plan's locked "GitHub repo + Actions (no hosting budget)"
storage mandate — with options and a recommended choice each, losing options named, a
v1-minimal-core vs deferred-scale partition, and disaster-recovery + provenance as first-class
goals.

---

## Task skeleton

| Task | exact_files | depends_on | Polarity | Priority | Wave |
|---|---|---|---|---|---|
| AWAIT-APPROVAL | *(none — human decision, no files)* | spec above | n/a (no code) | P1 | 0 |

**Wave 0 — Execution mode: NOT AGENT WORK** (0 code tasks — below every dispatch trigger,
including the /parallel-sweep ≥3-similar-tasks threshold; this is a human decision).

**Collision matrix:** empty — no task touches any file. **File-ownership:** n/a (no code).

---

## Decision points awaiting the human (see spec for full argument)

1. **D1** on-disk format — recommended: sharded canonical JSONL; Parquet/SQLite on Releases
   deferred until a named consumer exists (losing: Parquet-canonical, checked-in Pebble
   snapshot, file-per-record).
2. **D2** repo + Actions architecture — recommended: single public repo, 256 shards,
   ~150K-record federation trigger (losing: LFS, Pages-primary, multi-repo day 1).
3. **D3** PR-bot loop — recommended: gated export op → bounded PRs → CI validation →
   maintainer rebase-merge; trusted-submitter auto-merge later (losing: issues-queue,
   direct-push bot, hosted GitHub App). *Note:* the master plan's §INIT-8 phrase "CI that
   validates and applies" is satisfied here as CI-gates-humans-apply — "apply" == the
   maintainer's rebase-merge of a CI-green PR. Deliberate phrasing refinement, not a scope
   change (the spec's D3 step 4 states the same).
4. **D4** identity unit — recommended: record = one acoustic edition, record_id content-hashed
   from the whole-book signature (addressing only — near-dup detection is corpus-wide, never
   shard-scoped, masked comparator when either side is partial-coverage); works cluster records
   via `work_key` — but both the `work_key` field and the works.jsonl override layer are
   deferred until a consumer consumes cross-edition grouping (`work_key` is `omitempty`,
   addable at zero cost) (losing: metadata keying, full part fingerprints in-repo).
5. **D5** trust/governance/license — recommended: CC0-1.0 data, PR-only writes, challenge →
   tombstone/revert, layered poisoning resistance (losing: ODbL, CC-BY-4.0).
6. **D6** AcoustID — supersede for audiobooks; keep submission as an optional export (locked
   by the master plan; recorded, not relitigated).
7. **OQ1–OQ5** — repo home, code sharing, near-dup threshold calibration, seed scope,
   CC0-irrevocability/PII sign-off.

## After approval (informational only — no steps authorized now)

A follow-up plan-op session produces the real implementation package (M1–M5 per the spec's
Milestones section), each milestone worktree+PR-gated per house rules. **Spec approval (M0)
authorizes only M1 (repo scaffold — no data, no organizer code). The first irreversible publish
(M3 seed load, run through C3's dry-run → AskUserQuestion path) and the community opening (M5)
each re-gate on their own explicit human approval, and OQ5 (CC0-irrevocability / PII sign-off)
must be resolved before any seed PR merges.** Until then: no code, no task briefs beyond
AWAIT-APPROVAL, no repo creation, no external publication.
