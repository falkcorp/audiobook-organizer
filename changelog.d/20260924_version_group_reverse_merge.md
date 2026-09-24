### Fixed

#### MATCH-4 no longer merges an organized library copy into its own source

- The metadata-source-hash dedup (MATCH-4) kept "most files, then earliest created" as the survivor. For an organize pair, that is the unorganized source, so the library copy (the only one ABS can list) was merged into it. Members of one version group are now never flagged against each other. When any candidate is eligible, the survivor is ranked with the shared `versionprimary` rule (eligible first, then content tier), with the old rule as the tie-break.

### Added

- `maintenance.version-group-primary-repair` has three new decisions:
  - `revive_merged_copy` clears a same-group merge and crowns the organized copy, only when the election would pick it and never for an iTunes file.
  - `leftover_merged_elsewhere` labels a held group whose organized copy was merged into another group's book. It is report-only.
  - `demote_nonlive` sets merge losers still counted primary to explicit false, after the live primary is written. A group with no live primary keeps them (`nonlive_kept_no_live_primary`) because ABS lists merge losers.
- Groups whose live members look correct but that carry a same-group merge loser, or a loser still counted primary, are now candidates.
- `merged_into_book_id` changes get history rows, so undo-last-apply restores the merge.
