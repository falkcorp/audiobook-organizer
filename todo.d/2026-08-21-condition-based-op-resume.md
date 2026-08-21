- [ ] Replace the fixed `resume_policy` enum with a **condition-based** resume
      decision, with elapsed time as one available condition rather than the
      only one. Today `resume_policy` is a single static value per op-def
      (`restart` / `resume` / `drop`), decided without reference to the state
      the op was in when it stopped.
      Motivating case (2026-08-21): a deploy restart hit `library.scan` at
      13,922/40,089 books, ~80 minutes in. Its policy is `restart`, so all of
      that was discarded and the scan began again from zero — even though the
      partial work was minutes old and still valid.
      Wanted: resume when a condition set says the prior progress is still
      trustworthy, otherwise restart. Time is the obvious first condition
      ("resumed within ~3h"), but it should not be special-cased — express it
      as one predicate among others, e.g.:
        - elapsed since `started_at` / since the last checkpoint
        - whether the scanned root's mtime or file count changed meanwhile
        - whether a conflicting op ran in between
        - config or op-def version drift since the checkpoint
      Design note: this is a real expansion, not a tweak. `resume_policy` is
      consumed wherever ops restart, so the change is a policy *evaluator*
      taking op state + environment, not a new enum member. Keep the current
      static values working as degenerate always-true/always-false conditions
      so the migration is incremental.
      See `internal/config` op-def plumbing and the measured resume behaviour
      note (applies do NOT resume; `batch-apply-cached` = ResumeDrop).
