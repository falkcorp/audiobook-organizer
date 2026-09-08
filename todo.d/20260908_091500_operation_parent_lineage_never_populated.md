## Operation parent/child lineage is never populated by the BACKEND

`registry.WithParent` (`internal/operations/registry/types.go:382`) is the only thing
that sets `EnqueueOptions.ParentID`, and it has **no production callers**. Every
operation the API returns therefore has `parent_id: null` — verified against prod on
2026-09-08 (40 operations across a 6-hour window, all parentless).

- [ ] Decide whether SERVER-SIDE op lineage should be wired up. Several ops obviously
      fan out into children (`library.scan` → per-folder work,
      `metadata.batch-apply-cached` → per-book applies) and threading `WithParent`
      through would make those relationships real rather than inferred from timing.

**The "delete the dead hierarchy code" half of this is CLOSED** (2026-09-08). The
Activity page's `collapsedParents` / `isHiddenByCollapse` / `getDepth` / per-row
`indent` now have a producer: `web/src/stores/operationGrouping.ts` synthesizes parent
rows at read time for runs of consecutive same-kind operations. The render path was
never the problem — it was correct and merely unfed. The seeding-effect defect named
here (the ref only latched when `defaults.size > 0`, so it re-ran on every poll and
could clobber a user's expansion) is fixed too: seeding is now once-per-parent via
`seededParentsRef`, not once-per-page.

Note that read-time synthesis does **not** answer the bullet above. A synthetic group
says "these ran back to back", which is a claim about time; real lineage would say
"this op spawned that one", which is a claim about causation. Wiring `WithParent` would
still add something, and the two can coexist — `groupOperations` only ever sets
`parent_id` on rows that have none.
