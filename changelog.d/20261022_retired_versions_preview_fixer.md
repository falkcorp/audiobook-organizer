### Added

#### `retired-versions` — preview of moving trashed books' file records onto the live book they lost to

A read-only fixer in the Repairs lane of /review. It sorts every book in the trash into exactly one category and names the live book it would be retired onto: the end of the book's merged-into chain, or its version group's single primary. Either needs the group to have a live member; a merged-into chain on a book with no live group member is shown as evidence only and is never used as a target. A book the preview would retire is `would_retire`, or `would_retire_same_path` when a file record shares its path with a live book's record (both records are kept). Nothing is written: no book or file record changes, and the fixer has no Apply.

The categories that stay put each have their own count and clickable rows, and the first matching one wins, in this order: `trash_labelled_deleted`, `no_book_files`, `target_ambiguous`, `no_target_files_missing` / `no_target_files_present` (no live target; split by whether any file is still on disk), `owner_vetoed`, `itunes_needs_owner_approval`, `owner_manual_only`, `age_unknown` / `too_recent`, `has_listening_state` / `listening_state_unknown`.

- `owner_vetoed`: the owner rejected joining the book and its target (`same_work` or `same_copy`), read through the decisions store.
- `itunes_needs_owner_approval`: a file of the book or of its target is under `books/itunes/**`, with symlinks resolved (a library link into that tree counts, and so does a file row whose only iTunes location is its iTunes path). A path whose location cannot be settled (for example a folder that is a symlink to itself) is treated as possibly under that tree and says so in the reason, instead of being cleared on a guess. This replaces the old outcome where a book whose target was under iTunes showed as `would_retire` and was then skipped as `skipped_itunes`.
- `owner_manual_only`: the book or its target is Doctor Who, Big Finish or Torchwood, by path, series, title, credits, transcribed fields or a `franchise:` tag. The owner applies those by hand. Previously such a row was counted `would_retire` and only then skipped by the Repairs guard, which does not look at trashed books at all.
- `too_recent` uses the 14-day minimum, a waiting period in which a mistaken delete is likely to be noticed; `{"min_age_days":N}` changes it, and 0 turns the gate off. It says nothing about merge undo.
- `trash_labelled_deleted` (was `user_deleted`): the book's library state is "deleted". Three code paths write that label, so it cannot be told apart from a deliberate delete and the book is left alone.

A book that a combine journal not yet undone names as absorbed is no longer held in its own category. It is classified like any other book and its row carries `combine_undo_aware`, with an evidence line naming the journal, because the cleanup is meant to be reversible by undoing that combine rather than to skip those books. The plan's log summary counts these rows separately. If the combine journals cannot be read, the plan fails instead of guessing which rows carry the flag. Auto-merge journals are not read yet.

The plan's rule 5, a content-hash match between a book and its target, is not applied yet, and neither is the check of a rejected retired-versions proposal for a single book.

#### Repairs lane — a fixer can be preview-only

A new `PlanOnly` capability marks a fixer that has no Apply. The fixer list reports `plan_only`, the apply and owner-apply routes answer 409 for it, the apply operation refuses it, the engine refuses it again, and the lane shows a "Preview only" notice with no checkboxes, no Apply group and no Apply button. The retired-versions fixer also opts out of the library-scan stand-down, as it writes nothing a scan writes.

#### Repairs lane — filter a plan's rows by a flag

A row can now carry flags, a second dimension next to its class. The plan's rows page reports `by_flag` counts and accepts the filter `flag:<name>`, answered from the stored row index without reading every row. The retired-versions plan sets `combine_undo_aware` this way, and the plan view shows it as a clickable chip ("From a combine that can still be undone") that opens exactly those rows. A flag name must be a single word without commas or whitespace; a plan whose rows carry one the index cannot hold fails instead of storing a count that would not match its rows. The stored row-index entry gained a fourth field for the flags; plans stored in the previous entry format by the unreleased earlier build are refused the same way as legacy plans.

#### Abandoned-call gauge

`audiobook_organizer_ops_abandoned_calls{source}` reports calls that gave up waiting on a stuck mount but whose thread has not returned yet, for `repairs_resolver` (the Repairs path resolver) and `retired_versions_stat` (the retired-versions file check). Each series reads 0 from startup, and a value that stays above 0 means a dead mount is holding threads. It is an OpenTelemetry instrument exported through the existing Prometheus endpoint, as the repository's client_golang ratchet requires for new families.

### Changed


#### Repairs plans store their rows beside the operation, not inside its result

A Repairs plan used to be one operation result holding every row, about 760 bytes each; the retired-versions plan has about 51,000 rows, roughly 39 MB, and every read of the operation decoded all of it. The operation's result is now the plan's summary (counts and the `row_store` marker, a few kilobytes whatever the plan's size) and each row is stored under its own key with a small index entry, so listing a page reads the index and only that page's rows, and an apply reads only the rows it was asked to apply. The rows are deleted with the operation (including by the hollow-operation sweep). A plan made before this change cannot be read in the new form. Migration 66 strips the inline rows from every such plan's result once at startup, keeping the operation record and its summary, so the large results stop being read and written; it touches only Repairs plan operations, a second run changes nothing, and its log line reports the plans seen, how many were stripped, and the result bytes before, after and freed (the disk space itself comes back when the database compacts). The rows endpoint then answers 409 with the code `REPAIR_PLAN_NEEDS_RERUN` for such a plan (also for a plan whose stored rows are incomplete), and the lane shows a "Run plan again" button that starts the trial, not a Retry that would fetch the same 409. The endpoint answers 503 on a server whose store cannot keep plan rows.

#### Repairs path checks cannot hang a run on a dead mount

The symlink resolution behind the iTunes and owner-manual guards is now bounded per path. A path that gets no answer in time counts as unreadable, so its row is skipped rather than cleared; the folder is remembered as unanswered and not tried again; and once too many calls are still stuck (8 for one run, 64 across the process) the resolver stops trying and answers the same way instead of stacking blocked threads. The shared bounded-call helper now lives in its own package so the Repairs framework and the maintenance plugin use one copy.

#### Retired-versions preview — plan-time safety on a slow or hung mount

The on-disk check of files for books with no target is bounded per file, checks for cancelation before every file, and counts a timed-out or non-"not found" answer (permission denied, I/O error) as present while reporting it as `rows_stat_unknown` on the row. A run that accumulates 8 hung checks fails instead of stacking blocked threads, a plan is refused at its start while that many checks from earlier runs are still hung, and fewer than that are logged as a warning. The scan runs on every CPU core and fails rather than guess if the ownership lookup is not ready. At the end it logs how many no-target books had a merged-into chain or a same-path live book that was not used as a target.

#### Repairs guard — each distinct file path is matched once

The owner-manual and iTunes checks matched a book's own `file_path` again when it was also one of its book-file rows, and again for a resolved spelling equal to the path. Each distinct spelling is now resolved and matched once, in first-seen order, so the reasons are unchanged. On a synthetic 51,000-book fixture (ordinary titles, two files, two authors, a narrator and three tags per book, no franchise match) the guard over every book took 46 s on one core and 12 s on ten before this, and 29 s and 4.8 s after; the franchise regexes dominate what is left.

#### Hollow-operation sweep removes plan rows by a narrow helper

The sweep deletes a plan operation's rows through a helper that range-deletes only that operation's plan-row and row-index prefixes; the key-use ratchet allows that helper by name and still rejects any range delete that could touch operation records.
