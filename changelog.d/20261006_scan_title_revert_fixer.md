### Added

- **Repairs: "Revert titles a scan rewrote" (`maintenance.scan-title-revert`).**
  An input-driven fixer for the 2026-10-05 nightly scan, which rewrote
  existing books' titles from file tags before #3799 and journaled none of
  it, so the scan op's revert cannot undo it. Plan params (the body of
  `POST /api/v1/repairs/maintenance.scan-title-revert/plan`): `book_ids`
  (required), `since` (RFC3339, the scan's start; required) and an optional
  `source_op_id` recorded on each row. Each listed book's title is proposed
  back from its earliest `book_ver` snapshot stamped at or after `since` (a
  snapshot holds the row before the write it is stamped with, so that is the
  row as the scan found it). Held with the reason: `gone` (deleted, merged or
  unknown), `itunes` (an iTunes id on the book, a file row or a live external
  id), `locked` (title override or repair lock), `no_snapshot`,
  `old_title_empty`, `same`, `changed_since_scan` (the title is no longer
  the one the scan wrote) and `skipped_owner_manual` when the title to
  restore marks Doctor Who / Big Finish / Torchwood; the framework guards run
  as for every fixer. Apply writes the title only, compare-and-set against
  the planned title with a fresh lock check inside the write (history row
  after the write, then an op-journal row), so the apply operation's revert
  and "undo last apply" both restore the scan's title. The title is not
  locked. After the run one forced metadata candidate fetch is enqueued for
  the changed books (fetch only).
