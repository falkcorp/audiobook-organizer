- [ ] **TODO-REVERTDEDUPE** `auto-revert.yml`'s own "File the bug" step
      (`.github/workflows/auto-revert.yml` ~L305, `gh issue create`) has no
      pre-check against an already-open issue for the same failing SHA —
      unlike the new `auto-revert-backstop.yml`, which gained a `gh issue
      list --state open --search` dedupe check specifically because this gap
      exists. A flapping CI failure that `auto-revert.yml` handles repeatedly
      (e.g. `workflow_run` firing more than once for the same commit, or a
      revert that does not fix the build) could already be filing duplicate
      issues today, independent of the backstop. Add the same dedupe check to
      `auto-revert.yml`'s issue-filing step.
