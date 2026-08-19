- [ ] 🧊 **`*PebbleStore` struct split — LOWEST PRIORITY. Literally do anything else
      before working on this.** Decision doc:
      [`docs/plans/2026-08-19-pebblestore-struct-split-decision.md`](docs/plans/2026-08-19-pebblestore-struct-split-decision.md).

      **Deliberately parked, not abandoned.** Keeping it visible so it is not
      re-derived from scratch a fourth time — it has now been costed twice and
      corrected twice, and each pass cost real effort to reach the same answer.

      **Why it is parked.** Re-derived by AST at `21808fdc`: only **14 of 558**
      `*PebbleStore` methods (2.5%) touch any domain-local field, while `db` alone is
      touched by **408 of 558** (73.1%) and 117 touch no struct field at all. The
      struct is overwhelmingly one shared handle plus behaviour, so splitting it by
      domain buys separation the field-sharing numbers do not support.

      **Two traps for whoever picks this up.**

      1. **Step 1 is not a deliverable on its own.** Extracting `core` and having
         `PebbleStore` embed it moves zero methods; it leaves all 558 in place *plus*
         a new indirection layer with no consumer. Strictly worse than either endpoint
         unless steps 2-6 also land. Do not ship it as a "first increment".
      2. **`libGen` and `counterMu` are CORE, not domain-local.** Two separate costing
         passes classified them as domain-local and both produced 20/3.6% instead of
         14/2.5%. `libGen` is bumped by `Create`/`Update`/`DeleteBook` and read by
         `LibraryGeneration`; `counterMu` guards the shared `nextID` allocator. Re-read
         the decision doc's own Corrections section before re-running any census — the
         error survived an independent instrument because the instrument faithfully
         reproduced a wrong *definition*.

      **Revisit if** the 14/558 ratio moves materially, or if domain separation becomes
      wanted for a reason other than field sharing (build times, ownership boundaries,
      testability) — in which case the field-touching measurement is not the right
      criterion and the case should be argued on that basis instead.
