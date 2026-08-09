- [ ] **The unified Dedup view has no e2e coverage at all.** Found 2026-08-09
      while repairing `dedup.spec.ts`.

      `/dedup` was redesigned: it now renders a unified candidate surface
      (bands All / Certain / High / Medium / Review, a candidate table, "Find
      Duplicates" / "Rescore" / "Force Full Rescan" actions). The old tabbed
      Books / Authors / Series UI still exists but sits behind a **"Legacy
      View"** toggle persisted as `sessionStorage.dedup_show_legacy`.

      Every test in `dedup.spec.ts` covers the **legacy** view — they now opt
      into it explicitly via `enableLegacyDedupView()`. That was the right fix
      (the legacy features are still shipping and were previously untested by
      accident), but it means the surface a user actually lands on has **zero**
      automated coverage.

      Worth covering: band filtering, the candidate table, merge/dismiss
      actions, and the legacy toggle itself round-tripping through
      sessionStorage.

      Note the gap was invisible for the same reason the last one was: the
      specs did not fail with "this UI no longer exists", they failed with
      `element(s) not found`, which reads identically to a broken selector.
