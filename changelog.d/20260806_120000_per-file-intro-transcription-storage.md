<!-- file: changelog.d/20260806_120000_per-file-intro-transcription-storage.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e3a7c14-6b95-4d02-a1f8-27d59b4e0c63 -->
<!-- last-edited: 2026-08-06 -->

### Added

- **Per-file intro transcription storage.** The spoken
  "<Title> by <Author>, read by <Narrator>" opening is now recorded per
  `BookFile`, not only per `Book`. Eight fields were added to `BookFile`
  (`IntroTranscription`, `TranscribedTitle/Author/Narrator`,
  `IntroTranscribedAt`, `TranscribeStatus`, `TranscribeError`,
  `TranscribeAttemptedAt`), mirroring the existing `Book` fields exactly, and
  surfaced through `GET /audiobooks/:id/files` and the frontend `BookFile` type.

  Storing this per book captured exactly ONE file's opening, so a folder of 12
  files that are one book was indistinguishable from 12 files that are 12
  separate books. Per file the sequence is decisive: real library rows read
  "This is a reading of Overlord, Book 7. This part includes the prologue and
  Chapter 1" / "...includes Chapter 2" / "...Volume 7, Chapter 3", which proves
  continuation rather than a new book start.

  `Book.IntroTranscription` is unchanged and still populated, so existing
  consumers (notably `maintenance.auto-match-transcribed`) keep working.

  Only the raw transcript is stripped from the in-memory projection. At
  ~0.5-1.5 KB across ~317K files it would add ~160-475 MB to a memdb that
  stripping already takes from ~10 GB to ~2 GB; the other seven fields are small,
  are what queries actually filter on, and are retained. Stripping one field
  instead of eight also narrows the write-back preserve-guard to a single field.

### Fixed

- **`transcribe-book-intros` picked the wrong "first file" on multi-disc books.**
  `nthAudioFile` sorted candidates by `(track, path)`, ignoring `DiscNumber`, so
  disc-2-track-1 tied with disc-1-track-1 and the file path broke the tie
  arbitrarily — a book whose disc 2 sorted lexically first had disc 2's opening
  transcribed as its intro. The comparator now matches
  `PebbleStore.GetBookFiles` verbatim: `(disc, track, path)`.

  Books written by the iTunes regroup path were never affected (`assignDiscTrack`
  flattens discs to `DiscNumber=0` with contiguous track numbers, so the two
  comparators agree); the break was in legacy rows carrying real disc numbers
  from tag scans. This is load-bearing for the per-file signal above, which rests
  on "track 1 carries the opening, tracks 2..N do not" — a sort that disagrees
  about which row is track 1 makes that discriminator read the wrong row.
