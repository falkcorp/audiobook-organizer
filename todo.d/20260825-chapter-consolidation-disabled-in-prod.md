## Prod has `chapter_consolidation_threshold_min = 0`, which disables multi-file grouping

This is the root cause of the `book_file` creation regression — **12,525 books (20.4% of
the library) have no route to their audio**. Full chain and evidence in
[`docs/audits/2026-08-25-book-file-creation-regression.md`](../docs/audits/2026-08-25-book-file-creation-regression.md).

The intended default is 10 (`config.go:1392`); `0` legitimately means "disable
consolidation". With it disabled, files with no album tag (223 of 224 in the sampled
book) fall to `consolidateChapterGroups`, which returns one Book per file. Each then
arrives at scanner.go site 1487 with `len(SegmentFiles) == 1`, fails the `> 1` gate, and
`createBookFilesForBook` is never called.

Three separate pieces of work fall out of this, and only the first is a config change:

- [ ] Set the production value back to 10. **Fixes future scans only.** Production
      config change — belongs to the operator, not to an agent.
- [ ] Repair the 12,525 existing books with no `book_file` rows, and the ~1,710
      track-titled fragment rows. Already-written damage; the config change does not
      touch it.
- [ ] Fix the silent-disable defect: `ChapterConsolidationThresholdMin` has no
      `omitempty` (`config.go:811`), so a write from a partially-populated struct
      persists a hard `0` that beats viper's default on every later load — with no log
      line and no startup warning. The absence of any signal is why this ran eleven days
      behind a green suite. Blast radius is every field in that struct, so this needs a
      decision, not a one-line patch.

Not established: **when and how the value became 0.** `/var/lib/audiobook-organizer/config.yaml`
is 724 bytes, mtime 2026-08-24T01:24:08 (after the boundary, so it dates the last write,
not the flip), `0600` owned by `audiobook`, and `sudo cat` is not in the NOPASSWD
allowlist.
