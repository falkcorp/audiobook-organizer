<!-- file: docs/agent-tasks/platform-core/OWNER-01-owner-decisions-before-r0-r1.md -->
<!-- version: 1.2.0 -->
<!-- guid: 6d318a46-2f16-444d-9bfc-a89bd7547f51 -->
<!-- last-edited: 2026-11-05 -->

# OWNER-01 — Owner decisions before R0 ships and before R1's first core PR (recorded 2026-11-05, D15): required-check enforcement (Q-I), the R1 freeze, split-readiness thresholds (Q-G), the P14 production read

> **NOT AGENT WORK.** This is an owner checkpoint. No agent may answer, complete, tick or simulate it, and no agent
> runs the production read below. An agent may only prepare the text the owner reads and, after the owner has
> recorded the answers, cite them.

| Field | Value |
|---|---|
| ID | OWNER-01 |
| Release | owner checkpoint (R0 and R1 gates) |
| Tier | **OWNER**: owner decisions and one read-only production read |
| Wave | W01 (due at once) |
| Depends on | none |
| Status | **Answers recorded 2026-11-05** (D15, spec 02 §2.6; 14 §14.3.2). One owner action is left: apply the ruleset (step 2) before R1-01 merges. |
| Unblocks | **R0-05** (P14 recorded: Branch A); **R1-01** once the ruleset with the merge queue is applied; R1-06's readiness report uses the accepted Q-G thresholds |
| Spec | 02 §2.6 (D15); 09 §9.2.1 (release gating; the R1 freeze), §9.4 R0 Owner, §9.4 R1 Owner, §9.7 P14; 14 §14.3.2 Q-I, Q-G; 12 §12.10 item 3 |

## Recorded answers (2026-11-05)

```
OWNER-01 recorded 2026-11-05
P14: Branch A
Q-I: (a); merge mode: merge queue; /parallel-sweep admin-merge: not allowed
R1 freeze: kept (the owner sets the repository variable RELEASE_FREEZE to R1 at the end of OWNER-02; D15)
Q-G: accepted
```

The owner also decided the questions that sat in later rows (D15): Q-C (F5-14: the 14 jobs that run through a real
scheduler definition keep their cadence, all 15 dead v2 `Schedule` strings are deleted, `deluge.protected-paths-sync`
stays manual), advisor S2 (no flip-upgrade test; M8 registered once as `books.rewrite`) and advisor S3 with S5 (the
freeze is the repository variable `RELEASE_FREEZE`, toggled by the owner; R2-02 folded into OWNER-05).

## What each answer means

1. **P14, Branch A** (09 §9.7 P14): on production the Basic gate is off or a Basic password is saved, so R0-05's
   refusal of an empty stored password locks nobody out. R0-05 may merge.
2. **Q-I (a) with the merge queue** (14 §14.3.2 Q-I). Owner action, before R1-01 merges: a ruleset (or branch
   protection) on `main` that requires the named checks of 09 §9.2.1, with **no bypass actors** (`enforce_admins:
   true`) and **GitHub's merge queue**. Merge tooling (the `/parallel-sweep` coordinator, CLAUDE.md's Quick Fix flow)
   waits for the checks and **never merges with `--admin`** past them (plan §(m) item 1). R1-02 adds the queue's
   `merge_group` trigger to the required workflows. Within a wave the lead enqueues the critical-path rows (plan §(c))
   first. The ruleset itself is not part of this plan's PRs; the owner applies it, or asks an agent to in a separate
   task, and logs it in the tracking issue.
3. **R1 freeze kept** (09 §9.2.1 "R1 freeze"). Since D15 the freeze is the repository variable `RELEASE_FREEZE`, not
   a committed value: R1-35 adds the switch that reads it; the owner sets it to `R1` at the end of OWNER-02 and
   clears it in OWNER-03 after the R1 release, so R1 reaches production as exactly one release. The R2 freeze (the
   owner sets `R2` before F3-08, clears it in OWNER-05) is not overridable (D12 (a)).
4. **Q-G accepted** (14 §14.3.2 Q-G; 12 §12.10 item 3): SC1 = 3 consecutive API-stable checkpoints over 6 weeks;
   SC5 = coverage 70 % core / 60 % SDK. They are readiness reports, not merge gates; R1-06 wires them.
5. The module name needed no answer: D13 fixed `github.com/falkcorp/mediacore` (09 §9.4 R1 Owner).

## Where it is recorded

The block above, as one comment on the workstream's tracking issue (counts and choices only; never a token, host,
path or config value), plus D15 in spec 02 §2.6.

## Done when

The tracking-issue comment exists (done) and the owner has logged the applied ruleset with the merge queue. The lead
then lets R1-01 start its merge gate in merge-queue mode once R1-35 has merged and its post-merge wait has reported,
and passes the Q-G numbers to R1-06.

## What agents must not do

Read production, change an answer, set or clear `RELEASE_FREEZE`, apply a ruleset without the owner asking for it in a
separate task, or merge R1-01 before the ruleset is logged.
