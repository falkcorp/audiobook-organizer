- [ ] **A `todo.d` fragment assembled between the PR that files it and the PR
      that finishes it leaves an open task in `TODO.md` for completed work.**
      Hit for real on 2026-08-10; found only because `TODO.md` happened to be
      re-read after the merge. Nothing reported it.

      **Exact timeline** (`git log` on `main`):

          04:25 EDT  a75b9ad2  PR #2272 adds
                               todo.d/20260810-library-exhaustive-deps-…md
          04:51 EDT  6658d1a8  assemble_todo.py folds it into TODO.md
                               and `git rm`s the fragment  [skip ci]
          05:12 EDT  a655753e  PR #2273 does the work and deletes the
                               same fragment

      Result: the fragment is gone, the work is done, and `TODO.md` carries an
      unchecked `- [ ]` entry describing it — including instructions that PR
      #2273 had just proven wrong. Cleaned up by hand in #2274.

      **Why it is easy to miss.** `scripts/assemble_todo.py` *consumes*
      fragments: `git_rm(fragments)` at `main()`'s end deletes each one as it
      folds it in. So by the time the finishing PR merges, the fragment is
      already gone from `main`, and that PR's own deletion of it is a silent
      no-op. The absence of the fragment therefore proves nothing either way —
      it looks identical whether the task was assembled or never existed.

      The window is not narrow: 26 minutes here.

      **A mechanical check is harder than it first looks.** The obvious one —
      "if a PR deletes a `todo.d` fragment, require the matching `TODO.md`
      entry to be checked off" — will not fire reliably, because after a rebase
      the deletion may not be in the PR's diff at all (assemble already removed
      the file upstream). And "flag any `- [ ]` entry whose
      `<!-- file: todo.d/… -->` marker points at a missing fragment" matches
      *every* assembled entry, since assemble always deletes. Both directions
      are dead ends without knowing whether the work happened, which is not
      derivable from the files.

      **So the practical fix is a rule, not a script:** when a PR completes work
      that had a `todo.d` fragment, `grep TODO.md` for it before merging and
      check the entry off there if assemble got to it first. Worth adding to
      `todo.d/README.md` and to the post-task hygiene list in `CLAUDE.md`,
      beside the existing CHANGELOG/TODO/executive-summary triple.

      **If a mechanical guard is wanted anyway,** the least-bad version is
      probably a check on the *PR body*: PRs that say "closes the todo.d
      fragment …" or delete a fragment must also touch `TODO.md`. That is a
      heuristic, and it should be written as one rather than as a guarantee.
