<!-- Documentation only — a todo.d fragment recording a process hazard found on
     2026-08-10. No code, no behaviour change, so this fragment is deliberately
     a no-op comment. See changelog.d/README.md.

     assemble_todo.py consumes fragments (it git-rm's each one as it folds it
     into TODO.md). If a fragment is filed and finished in separate PRs and an
     assemble run lands between them, TODO.md keeps an open task for completed
     work, and the finishing PR's own deletion of the fragment is a silent
     no-op. Hit for real between PR #2272 and PR #2273; cleaned up in #2274. -->
