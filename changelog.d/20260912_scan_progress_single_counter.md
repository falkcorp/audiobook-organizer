### Fixed

#### Library scan progress no longer flips between 0/1 and the real total

A library scan's progress bar used to jump between "0/1", "1/1" (or "2/2")
and the real book count (around 61,000 in production) as the scan moved from
step to step. The scan's one progress row was being overwritten by the steps
it runs: the folder walk ("Discovering folders", "Scanning folders: n/m" for
the current import folder only), the AI parse batches, and the post-scan
auto-organize (its database backup at 0/1 and its per-folder organize count).

The scan now has a single owner of its progress numbers: books processed out
of books discovered, across all folders. The steps still report, so their
messages stay visible (prefixed with "Folder n/m:" or "Auto-organize:") and
they still keep the stuck-operation watchdog from firing, but they publish the
scan's cumulative numbers instead of their own.

Organize phases whose size is not known (loading the library, the database
backup) now report an unknown total, which the UI shows as an animated bar,
instead of 0/1, which it showed as a bar stuck at 0%. This also applies to a
stand-alone organize run.
