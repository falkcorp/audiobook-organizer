## `ensureSingleFileBookFile` is now a backlog-only backfill — decide its retirement

The scan now creates a `book_file` row for genuinely single-file books
(`createSingleFileBookFile`, called from `ProcessBooksParallel`). Before that,
the only thing that ever gave those books a file row was
`ensureSingleFileBookFile` in `internal/server/server.go`, called from the
auto-organize hook.

That backfill is **still needed** — every single-file book imported before this
change still has no row, and the auto-organize hook is currently the only thing
that repairs them. But it is no longer the mechanism for NEW books, and leaving
two writers for the same row indefinitely is how the two drift.

Note the two are deliberately NOT identical, and the difference matters:

- `createSingleFileBookFile` reads the file's tags (via `createBookFilesForBook`),
  so the row carries `RawTags`, the real `TrackNumber`/`DiscNumber` from the tag,
  and a content hash.
- `ensureSingleFileBookFile` hand-builds the row with `TrackNumber: 1`, no tags
  and no hash.

So rows created by the backfill are **thinner** than rows created by the scan.
Anything that later reads `RawTags` or `FileHash` off a book_file will see a
difference that depends only on which writer got there first.

- [ ] Size the backlog: how many books have zero `book_file` rows and a
      regular-file `FilePath`? (Do not assume it is small — 41.8% of a sampled
      cohort of file rows already point at bytes that are gone.)
- [ ] Repair the backlog once, from the scan's writer rather than the thin one,
      so every row has tags and a hash
- [ ] Then delete `ensureSingleFileBookFile` and its call, rather than leaving a
      second writer for the same row
- [ ] Until then, do not "simplify" the two into one by keeping the thin version
