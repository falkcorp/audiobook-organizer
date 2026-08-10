- [ ] **`Library.tsx:707` — an `exhaustive-deps` warning whose suggested fix
      would silently undo the URL filter-drop guard.** Introduced 2026-08-10 by
      PR #2271; noticed while linting an unrelated branch.

      `npx eslint .` in `web/` reports:

          707:6  warning  React Hook useEffect has a missing dependency:
                 'searchParams'. Either include it or remove the dependency
                 array   react-hooks/exhaustive-deps

      The omission is deliberate. That effect is the URL **writer**, and #2271
      added a guard at the top of it that reads `searchParams` precisely to
      detect "the URL changed under us since the last commit":

          const currentSearch = searchParams.toString();
          const urlChangedUnderUs = currentSearch !== seenSearch.current;
          if (urlChangedUnderUs && currentSearch !== lastWrittenSearch.current) return;

      Reading a value without depending on it is the whole point — the guard
      needs the *current* URL compared against a ref that a **later** effect
      advances, so effect declaration order is load-bearing. See the comment on
      `seenSearch` and the one inside the write effect.

      **Why this is worth a task rather than a shrug:** the warning tells the
      next reader to add `searchParams` to the deps array. That is plausible,
      one keystroke, and makes the warning go away. Whether it actually breaks
      the guard is **not established** — it may merely cause extra runs — but
      nobody has tested it, and the failure it would reintroduce is a
      transient, sub-frame filter drop that took a `history.pushState`
      interceptor to observe at all (rAF sampling at ~16ms was too coarse to
      see it). A warning that recommends an untested change to a race fix is a
      trap with a countdown on it.

      **Do:** replace the warning with an explicit
      `// eslint-disable-next-line react-hooks/exhaustive-deps` carrying a
      one-line reason that points at the `seenSearch` comment. Before
      committing, run the negative control that validated the original fix —
      disable the guard body and confirm
      `library-sidebar-filters.spec.ts` goes red (it failed 4 of 6 runs on
      webkit with the guard disabled, and passed 24 consecutive with it) — so
      the disable is verified to be protecting something real rather than just
      silencing lint.

      **Do not** simply add the dependency to make the warning disappear
      without running that control.
