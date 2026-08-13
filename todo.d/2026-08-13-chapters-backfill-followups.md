- [ ] 🎧 **Run `maintenance.chapters-backfill` against production.** The op ships
      dry-run-by-default and has never been run on the real library. Sequence:
      (1) dry run over the `job (chapter-backfill test cohort)` static playlist
      (id `01KZXMN8F8ZEXVQQPZ2SF74T0A`, 77 books, 58 single-file) via
      `{"bookIds": [...]}`; (2) apply over that cohort and verify against the
      ffprobe oracle — `Deadly Jobs` must report **231** chapters, `The Icarus Job`
      **28**, `The Colchis Job` **20**, and the two markerless files
      (`132 132 - Job.m4b`, `Delve 132 - Chapter 132 - Job.m4b`) must stay at the
      synthesized single chapter; (3) only then a whole-library apply. Expect
      roughly 14,600 single-file candidates of which about half carry markers.

- [ ] 🔁 **Wire a durable "probed, found none" marker before this op is ever
      scheduled.** `SaveChaptersForBook` deletes its key on an empty slice, so a
      book with no embedded markers is byte-identical to one that was never
      examined, and every run re-ffprobes that whole population (~half of
      single-file containers). That is acceptable for a manual op and NOT
      acceptable nightly. `internal/operations/freshness` already provides
      `Stamp`/`ClearStamps` but has **zero non-test callers** — reaching it from an
      op needs a new `ServerDeps` accessor plus server wiring. Adding a `Schedule`
      to the op without doing this first is the bug.
      `TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes` pins the current
      behaviour and will fail loudly when the marker lands.

- [ ] 🔍 **Index track names so smart playlists can match them.** The Bleve
      `BookDocument` (`internal/search/document.go:19`) carries only book-level
      fields — title, author, narrator, series, publisher, description, file_path.
      Track names live in `BookFile.FilePath` / `BookFile.Title` and are never
      indexed, and smart playlists evaluate exclusively through Bleve, so **no
      dynamic playlist can match a track name**. Verified: three copies of the
      Scourby Bible readings have a track literally named `Job` and appear in zero
      search results for "job". Needs a `TrackNames []string` field on
      `BookDocument`, a text field mapping, and a full reindex. Until then,
      track-name cohorts must be built as static playlists.

- [ ] 🧩 **Investigate per-chapter split files standing as their own books.** While
      probing, item `97e56ed2` turned out to be a 463 s fragment
      (`01 Angel in the Whirlwind - 1 - The.m4b`) registered as a standalone book,
      and several sampled "single-file" books are per-chapter splits mis-grouped
      the same way. Unrelated to chapter extraction — those files genuinely have no
      markers — but it inflates the single-file population and produces 8-minute
      "audiobooks".
