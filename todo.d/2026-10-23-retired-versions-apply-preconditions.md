- [ ] **TODO-RETIRED-APPLY-PRECONDITIONS** before `retire_versions_apply_enabled`
      is turned on in production: (1) PR 5 must land the undo cases for
      `book_version_create`, `book_file_retire` and `retired_version_rehome`
      (PR 7 declares the strings locally in
      `internal/plugins/maintenance/retired_versions_apply.go` and journals
      through `Writer.JournalStep`/`JournalSteps`; swap in PR 5's Writer
      primitives behind the `retireWriter` interface); (2) the combine-undo link
      of plan section 6.5 (`combine_undo_aware` rows are held as retry_later
      until `merge/combine_journal.go` UndoCombine reverses a retire first);
      (3) the `dedup:automerge:` journals are not read (the plugin deps cannot
      reach the embedding store; the 14-day minimum age is the only guard); (4)
      a trashed book that holds only retired rows for other shells and owns no
      live row is classed `no_book_files` by the PR 1 classifier and is not
      re-homed by this fixer.

- [ ] **TODO-RETIRED-APPLY-PR8-HOOK** wire the decisions ledger into the retire
      Apply once PR 8 (#4143) and PR 7 are stacked: after a row's
      `RetireRows` commits in `retire()`
      (`internal/plugins/maintenance/retired_versions_apply.go`, see the
      `TODO(PR 8, #4143)` comment), call
      `recordRetiredVersionApplied(f.p.deps.OpsStore(), ev)` with a
      `decisions.RetiredVersionsEvent` keyed by
      `decisions.RetiredVersionsSubjectKey(shell)`.
