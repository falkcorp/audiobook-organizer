### Fixed

#### New `book_file` rows are no longer written with Duration 0 when the file is readable

ABS reports a book's duration as the sum of its `book_file` durations, so a row
created with Duration 0 made the book read "0" in the app, even when the file was
fine and `book.Duration` was already right. On 2026-09-23 `library.scan` imported
"Monster Makers" with a 0-duration, empty-codec row, and `CreateOrganizedVersion`
copied that row verbatim into the organized book.

A new shared helper, `bookfileaudio.EnsureDuration`, now runs at every creation
site before the row is written. Its order of precedence is:

1. A value the caller already read from this file.
2. The book's own duration, when the book has a single file.
3. A bounded header read of the file (`mediainfo.Extract`, 30 s cap).

An estimated duration (fileSize ÷ bitrate) is never written as real, and a
failed read logs a Warn and leaves the row at 0 without failing the caller. It
fills `Codec` only when the field is empty. Bitrate, sample rate and channels
are left alone because `mediainfo` returns hard-coded defaults for them when the
tag does not carry them.

Wired into these sites:

- The scanner's new-book row builder. A single-file book carries the media info
  that ProcessFile already read. Multi-file segments get a header read, skipped
  when the replaced-file probe already ran or when the stored row at that path
  has the same hash and a duration.
- `CreateOrganizedVersion`, which reads the new path.
- `server.ensureSingleFileBookFile`.
- The importer.
- The `backfill-book-files` job, which now runs on a bounded worker pool because
  each row costs a header read.
- `fix-version-groups`.
- `relink-unlinked-books`.
- `fs-regroup-xml`.
- The transcode output row.
- merge `attachVirtualFile`.
