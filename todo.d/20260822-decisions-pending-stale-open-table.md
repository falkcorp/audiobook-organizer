### 📋 `DECISIONS-PENDING.md` contradicts itself — settled decisions still listed as open

- [ ] **Reconcile `docs/plans/DECISIONS-PENDING.md`'s open table with its own recorded-decisions
      table.** Surfaced by TASK-058 (PR #2715) while verifying the execution-manifest gates.

  The file carries both a "Decisions recorded 2026-08-21 (owner, via AskUserQuestion)" table
  **and** an open/pending table that still lists the same rows 1–5 as awaiting a decision. It also
  still says PR #1935 "stays open"; `gh pr view 1935` reports **MERGED**. So the document asserts
  two contradictory states about the same five items, and a reader landing on the open table gets
  the wrong one.

  **Why this is worth fixing rather than ignoring:** the manifest at
  `docs/plans/2026-07-10-execution-manifest.md` was just corrected to match the *recorded* table.
  Leaving the stale open table in place recreates exactly the drift that correction removed, and
  the next reader has no way to tell which table is authoritative.

  **Two nuances to preserve when reconciling** — both were nearly lost once already:

  - INIT-7 is **HOLD CONFIRMED**, not "parked". The owner answered "KEEP ON HOLD".
    `SCOUT-INSTRUCTIONS.md:14`'s `ON HOLD → "parked"` is the scout package's classification
    convention for excluding an item from briefing, **not** a decision the owner made.
  - INIT-6's #1935 merged, but it was the plan doc *"for owner sign-off"*. The STOP-FOR-HUMAN
    spec review was never held. Recording a bare "merged" reads as approved and would contradict
    the item's own hold status.

  Related: `TODO.md` item 33 still calls REPO-SIZE-1 STOP-FOR-HUMAN, though
  `docs/plans/2026-07-10-repo-size-history-rewrite-plan.md:223` records "Adopt Option (d)…Do not
  rewrite history." Same reconciliation pass should cover it.
