- [ ] **Store-parameter narrowing: 54 declarations remain.** Re-measured 2026-08-17 by AST.
      Supersedes the earlier "24 remain" fragment, which was wrong — the method count (7)
      was right, the free-function count was low by 3, and it counted only the maintenance
      packages. Corrected totals:
      - **Maintenance: 8 left** of 27. The 19 `OK`-tier (no propagation) declarations in
        `internal/plugins/maintenance` are done. The remaining 8 are `PROP`-tier — their
        callees must be narrowed first or propagation re-widens them:
        `firstAudioFile`, `linkProbedFolder`, `relinkOne`, `vgFixAuthorDirPath`,
        `ApplyMultidisc`, `migrateOne`, `ddMergeDuplicateBook`, `processTranscribePage`.
      - **Outside maintenance: 65** across 24 packages. Largest: `internal/server` 12 +
        `internal/server/handlers` 6, `internal/dedup` 6, `internal/versions` 5,
        `internal/reconcile` 4, `internal/plugins/acoustid` 4, `internal/metafetch` 4.
      - **30 of those 65 do not need narrowing at all — the `database.Store` parameter is
        entirely unused.** Delete the parameter instead. (138 declarations repo-wide have an
        unused store param; 66 are `internal/database` migrations whose signature the runner
        fixes, so those stay.)
      Not narrowable and excluded from every count above: 37 `MaintenanceJob.Run` methods
      (an interface method's parameter type is fixed for all implementers) and the migration
      runner's signature.
      Pattern guidance: **B (narrow interface) by default** — it is one line per site and
      changes zero call sites. **Do not sweep C** (split-the-decision); see
      `.claude/notes/2026-08-17-option-b-vs-c-comparison.md`.
