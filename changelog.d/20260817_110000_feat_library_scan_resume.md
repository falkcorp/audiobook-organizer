### Added

- **`library.scan` now genuinely resumes instead of starting over.** Its
  definition previously carried the comment "⚠️ THIS RE-RUNS THE SCAN FROM THE
  START, IT DOES NOT CONTINUE MID-SCAN" — accurate, because nothing in the scan
  path called `Checkpoint()` and `libraryScanParams` had no fields for a resume
  point to land in. A production scan on 2026-08-17 ran 3h50m+ against a
  63,044-book library; a restart discarded all of it.

  The scan now checkpoints `resume_folder_idx` / `resume_item_offset` after every
  completed chunk of books and every completed folder. Completed folders are
  skipped whole; the folder that was in flight resumes at the last completed
  chunk boundary.

  Granularity is deliberately per-chunk, not per-folder: one production folder
  holds ~14,000 items, so a checkpoint that could only say "folder 3 of 5" would
  still throw away hours. Books are sorted by path before chunking so an offset
  means the same thing across runs — directory-walk order varies per run, and
  resuming into a differently-ordered slice would skip an arbitrary set.

  A chunk that fails is not checkpointed, so a resume retries it rather than
  stepping over books that were never processed.
