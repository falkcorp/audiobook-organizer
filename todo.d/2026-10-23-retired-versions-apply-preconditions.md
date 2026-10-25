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
      re-homed by this fixer; (5) the combine-journal index is a per-run
      snapshot, so a combine journaled mid-run is not seen (combine undo now
      refuses a retired shell, which covers it); (6) the store-level
      `RevertBookToVersion` shell check runs under the book write stripe but
      retire takes only owner stripes and the merge lock; the metadata revert
      handler and `UnmergeAuto` now take the merge lock around that call, so a
      revert through them cannot race a retire, but any other caller of
      `RevertBookToVersion` that does not hold the merge lock still can; (7) a generic whole-row `UpdateBook` from a
      stale read can still overwrite the merged-into link; (8) the batch hard delete (`internal/batch/service.go` `hardDelete`) has no
      owned-files pre-check at all, live or retired, so a book that owns rows
      has its listening state carried to another version
      (`merge.HardDeleteKeepingUserState`) before `DeleteBook` refuses with
      `ErrBookOwnsFiles`; this predates the retire work, and adding the check
      changes the batch's behaviour for live rows too, so it is left for its own
      change (`DiscardProgressAndPurge`, the `DeleteAudiobook` live branch and
      the purge pre-check now count retired rows); (9) the comment at
      `internal/repairs/writer_files.go:29` still says a whole apply is undone
      from the Operations page, which is not true until PR 5 (#4146); (10) decorator
      stores that do not implement `Unwrap() database.Store` cannot be read for
      retired rows and now fail closed; (11) PR 5's revert of a
      `book_version_create` row must refuse to delete a version that still holds
      rows (reverting the create after the rows' own retire rows are reverted is
      the only order that leaves it empty; a create reverted while rows remain,
      e.g. after a partial revert or a row journaled by another op, would
      delete the version out from under them).

- [ ] **TODO-RETIRED-APPLY-PR8-HOOK** wire the decisions ledger into the retire
      Apply once PR 8 (#4143) and PR 7 are stacked: after a row's
      `RetireRows` commits in `retire()`
      (`internal/plugins/maintenance/retired_versions_apply.go`, see the
      `TODO(PR 8, #4143)` comment), call
      `recordRetiredVersionApplied(f.p.deps.OpsStore(), ev)` with a
      `decisions.RetiredVersionsEvent` keyed by
      `decisions.RetiredVersionsSubjectKey(shell)`.
