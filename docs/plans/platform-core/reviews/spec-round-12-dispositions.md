<!-- file: docs/plans/platform-core/reviews/spec-round-12-dispositions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 33fd8ca6-7660-46e8-9dce-58e8d85e7599 -->
<!-- last-edited: 2026-11-04 -->

# Final verification (round 12) — dispositions

Input: `spec-round-12-final-verification.md`. **0 BLOCKER, 3 MAJOR, 7 MINOR. All accepted, 0 rejected.**

The R11-1 scratch experiment passes, and so do its negative controls. **The review loop's exit rule is met:** zero open
BLOCKERs, and every MAJOR has a disposition. The fixes below are applied in one pass before Phase 5. Each one is the
reviewer's concrete fix, so no further review round is needed.

Blockers per round: 20 → 10 → 9 → 6 → 5 → 2 → 1 → 0 (round 8, loop exit) → advisor 1 → 3 → 2 → 1 → **0**.

| ID | Sev | Disposition |
|---|---|---|
| FIN12-1 | MAJOR | Accepted. The bot path list covers all five of HEAD's Dependabot ecosystems, including the Dockerfiles and `.standards` (reviewer's fix). |
| FIN12-2 | MAJOR | Accepted. The H-release `levels:` value is written by the owner command into the soak row, not by a PR. A revert of an H flip PR is admitted only before the H release ships. After that, H is one-way and the floor refuses it (reviewer's fix). |
| FIN12-3 | MAJOR | Accepted. `tasks.Client` gains the methods the three books handlers need: timeline, log download and record discard (reviewer's fix). |
| FIN12-4 | MINOR | Accepted. `/maintenance-window/*` stays books' route (R11-3's rule), and 05-tasks says "kept". |
| FIN12-5 | MINOR | Accepted. UI0's soak runs in parallel with the next stage's, because UI-0 changes no stored state. |
| FIN12-6 | MINOR | Accepted. 09 S2's HTTP row states R11-1. |
| FIN12-7 | MINOR | Accepted. The scope of "otelgin's by construction" is stated exactly. 05-metrics records the experiment's load-bearing details: typed attributes, the exact scope version and an identical description. |
| FIN12-8 | MINOR | Accepted. The shared-series test starts at S2 r1. |
| FIN12-9 | MINOR | Accepted. The merge-commit exemption is deleted. Merges are rebase-only (D4), so no merge commit ever exists. |
| FIN12-10 | MINOR | Accepted. A deferred H1/H2 boot starts the soak clock when the cutover completes, not at boot. |
