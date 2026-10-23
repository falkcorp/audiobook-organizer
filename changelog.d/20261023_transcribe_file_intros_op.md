### Added

#### `maintenance.transcribe-file-intros`: transcribe the opening of every audio file

A new operation transcribes the first 90 seconds of every present `book_file`
row, not only each book's first file, and stores the result on the file row
(`IntroTranscription`, the parsed credits, `IntroMeta`, and the done marker
`IntroSourceHash`). It stacks on the path-based whisper transport
(`transcribe.TranscribeByPathBatch`, branch `feat/whisper-remote-extract`): the
server decodes nothing, each file's path and a 90 second length go to the worker.

- A live run first checks that some worker advertises the path pipeline
  (`transcribe.PathPipelineAvailable`) and stops with the per-endpoint reason if
  none does. The worker's per-file error code decides what is written:
  `not_found` is `source_file_missing`, `ffmpeg_error` is held until
  `retry_failed`, `path_unmapped` and `ffmpeg_timeout` (a lost mount or a stalled
  share, not a verdict on the file) write nothing, anything else is a retryable
  `whisper_error`.
- Dry run by default; the preview counts the work by reason and estimates the
  time (the per-file time is unmeasured for worker-side extraction).
- Ordinal 0 of every book is sent first, library-wide. Files with the same
  `FileHash` are sent once and the result is applied to all; an earlier done
  file with the same hash is copied without a whisper call.
- Resumable with no checkpoint: a file is done when `IntroSourceHash == FileHash`
  (on `BookFileCore`, so selection is one memdb pass). A whisper error is
  retryable; an empty transcript at 90 s is stored as a done file marker; an
  endpoint outage writes nothing and defers.
- Fills a book's transcript from its ordinal-0 file only when the book has none,
  and never from a silent clip. Nothing is ever overwritten under `only_missing`.
- Refuses to start (and `clear_run_id` refuses to start) while `library.scan` or
  `metadata.batch-apply-cached` is queued or running, and re-checks immediately
  before every batch of writes, holding results until it is clear. The scanner's
  batch upsert reads a file row without the per-file lock, so a result written
  during a scan could be erased by it. The op declares no `Writes` on purpose: it runs for days, and
  the dispatcher's write-set gate would keep `acoustid.backfill` (and every other
  op declaring `books`/`book_files`) queued for the whole run. Its writes are
  field-scoped and the shared `book_file` merge keeps the intro group against any
  whole-row writer.
- Worker pool sized to the whisper endpoints' own concurrency (capped), bounded by
  `whisper_max_in_flight`.
- Rollback is by run id: `clear_run_id=<op id>` (dry run first for the count)
  clears that run's per-file results and reverts only the book fills that still
  hold exactly what the run wrote. It writes no undo-ledger rows, and it returns
  files to untranscribed rather than restoring a transcript a forced re-run
  (`only_missing=false`) replaced.
- Counters are kept per file under their own key, `stats:transcribe-files`
  (coverage at the `book_file` grain), served by
  `GET /api/v1/maintenance/file-intro-stats`; the book op's `stats:transcribe`
  is untouched.

Writes go through two new capability methods, `SetBookFileIntro` and
`ClearBookFileIntro`, that write only the intro group of one row (primary key and
memdb), so no book aggregate is recomputed per file.
