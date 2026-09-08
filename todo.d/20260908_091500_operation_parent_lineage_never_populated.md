## Operation parent/child lineage is never populated

`registry.WithParent` (`internal/operations/registry/types.go:382`) is the only thing
that sets `EnqueueOptions.ParentID`, and it has **no production callers**. Every
operation the API returns therefore has `parent_id: null` — verified against prod on
2026-09-08 (40 operations across a 6-hour window, all parentless).

Two consequences:

- [ ] Decide whether op lineage should actually be wired up. Several ops obviously fan
      out into children (`library.scan` → per-folder work, `metadata.batch-apply-cached`
      → per-book applies) and threading `WithParent` through would make the Activity
      page's existing hierarchy rendering meaningful.
- [ ] If it should NOT be wired up, delete the dead hierarchy code in
      `web/src/pages/ActivityLog.tsx`: `collapsedParents`, the seeding effect (whose ref
      only latches when `defaults.size > 0`, so it re-runs on every poll and can clobber
      a user's expansion the moment some parent reaches 3 children), `isHiddenByCollapse`,
      `getDepth`, and the per-row `indent`.

Expand All / Collapse All were repointed at the Pending/Active/Completed sections in
the meantime, so the buttons work regardless of which way this goes.
