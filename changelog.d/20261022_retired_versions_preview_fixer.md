### Added

#### `retired-versions` — preview of moving trashed books' file records onto the live book they lost to

A read-only fixer in the Repairs lane of /review. It sorts every book in the trash into exactly one category and names the live book it would be retired onto: the end of the book's merged-into chain, or its version group's single primary. Either needs the group to have a live member; a merged-into chain on a book with no live group member is shown as evidence only and is never used as a target. A book the preview would retire is `would_retire`, or `would_retire_same_path` when a file record shares its path with a live book's record (both records are kept). Nothing is written: no book or file record changes, and the fixer has no Apply.

The categories that stay put each have their own count and clickable rows, and the first matching one wins, in this order: `undo_still_possible`, `trash_labelled_deleted`, `no_book_files`, `target_ambiguous`, `no_target_files_missing` / `no_target_files_present` (no live target; split by whether any file is still on disk), `owner_vetoed`, `itunes_needs_owner_approval`, `age_unknown` / `too_recent`, `has_listening_state` / `listening_state_unknown`.

- `undo_still_possible`: a combine journal that is not undone still names the book as absorbed, so retiring or purging it would make that merge impossible to undo. It is checked first, ahead of every other category, because an absorbed book is usually labelled deleted, often has no group and may own no file records. Applied journals do not expire, so a combine-merged book stays here until its journal is undone; the 14-day age gate is a separate check. If the combine journals cannot be read, the plan fails instead of guessing.
- `owner_vetoed`: the owner rejected joining the book and its target (`same_work` or `same_copy`), read through the decisions store.
- `itunes_needs_owner_approval`: a file of the book or of its target is under `books/itunes/**`, with symlinks resolved (a library link into that tree counts, and so does a file row whose only iTunes location is its iTunes path). This replaces the old outcome where a book whose target was under iTunes showed as `would_retire` and was then skipped as `skipped_itunes`.
- `too_recent` uses the 14-day minimum; `{"min_age_days":N}` changes it, and 0 turns the gate off.
- `trash_labelled_deleted` (was `user_deleted`): the book's library state is "deleted". Three code paths write that label, so it cannot be told apart from a deliberate delete and the book is left alone.

The plan's rule 5, a content-hash match between a book and its target, is not applied yet, and neither is the check of a rejected retired-versions proposal for a single book; both are deferred to later PRs of the retired-version plan.

#### Repairs lane — a fixer can be preview-only

A new `PlanOnly` capability marks a fixer that has no Apply. The fixer list reports `plan_only`, the apply and owner-apply routes answer 409 for it, the apply operation refuses it, and the lane shows a "Preview only" notice with no checkboxes, no Apply group and no Apply button. The retired-versions fixer also opts out of the library-scan stand-down, as it writes nothing a scan writes.

#### Retired-versions preview — plan-time safety on a slow or hung mount

The on-disk check of files for books with no target is bounded per file, checks for cancelation before every file, and counts a timed-out or non-"not found" answer (permission denied, I/O error) as present while reporting it as `rows_stat_unknown` on the row. A run that accumulates 8 hung checks fails instead of stacking blocked threads, and a process-wide count of still-hung checks is logged at the start of the next plan. The scan runs on every CPU core and fails rather than guess if the ownership lookup is not ready. At the end it logs how many no-target books had a merged-into chain or a same-path live book that was not used as a target.
